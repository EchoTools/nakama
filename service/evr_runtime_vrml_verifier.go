package service

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/echotools/vrmlgo/v5"
	"github.com/go-redis/redis"
	"github.com/heroiclabs/nakama-common/runtime"
	"golang.org/x/time/rate"
)

var (
	ErrQueueEmpty = errors.New("VRML scan queue is empty")
)

const (
	StorageKeyVRMLVerificationLedger = "EntitlementLedger"
	VRMLPlayerMapKeyPrefix           = "VRMLUserPlayerMap:"
)

type VRMLScanQueueEntry struct {
	UserID     string `json:"user_id"` // The user ID of the Nakama user
	VRMLUserID string `json:"vrml_user_id,omitempty"`
}

func (e *VRMLScanQueueEntry) String() string {
	data, _ := json.Marshal(e)
	return string(data)
}

type VRMLScanQueue struct {
	ctx    context.Context
	logger runtime.Logger
	db     *sql.DB
	nk     runtime.NakamaModule

	redisClient      *redis.Client
	cache            *VRMLCache
	queueKey         string
	playerKey        string
	oauthRedirectURL string
	oauthClientID    string
	seasons          []*vrmlgo.Season
}

func NewVRMLScanQueue(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer, appBot *discordgo.Session, redisClient *redis.Client, oauthRedirectURL, oauthCLientID string) (*VRMLScanQueue, error) {

	ctx = context.Background()

	// Configure the client with the redis cache
	verifier := &VRMLScanQueue{
		ctx:    ctx,
		logger: logger,
		db:     db,
		nk:     nk,

		redisClient:      redisClient,
		cache:            NewVRMLCache(redisClient, "VRMLCache:cache:"),
		queueKey:         "VRMLVerifier:queue",
		oauthRedirectURL: oauthRedirectURL,
		oauthClientID:    oauthCLientID,
	}

	// Register the RPC function
	if err := initializer.RegisterRpc("oauth/vrml_redirect", verifier.RedirectRPC); err != nil {
		return nil, fmt.Errorf("failed to register VRML redirect RPC: %v", err)
	}

	verifier.Start()

	return verifier, nil
}
func (v *VRMLScanQueue) Start() error {

	ledger, err := VRMLEntitlementLedgerLoad(v.ctx, v.nk)
	if err != nil {
		return fmt.Errorf("failed to load VRML entitlement ledger: %v", err)
	}

	go func() {
		vg := vrmlgo.New("")
		seasons, err := vg.GameSeasons(VRMLEchoArenaShortName)
		if err != nil {
			v.logger.WithField("error", err).Error("Failed to get seasons")
		}
		v.seasons = seasons

		if err := v.cachePlayerLists(); err != nil {
			v.logger.WithField("error", err).Error("Failed to cache player lists")
			return
		}

		for {
			select {
			case <-v.ctx.Done():
				return
			case <-time.After(1 * time.Second):
			}

			entry, err := v.dequeue()
			if err != nil {
				if errors.Is(err, ErrQueueEmpty) {
					continue
				}
				v.logger.Error("Failed to dequeue item %v", err)
				continue
			}

			logger := v.logger.WithFields(map[string]any{
				"user_id":      entry.UserID,
				"vrml_user_id": entry.VRMLUserID,
			})

			playerID, err := v.getPlayerIDByUserID(entry.VRMLUserID)
			if err != nil {
				logger.WithField("error", err).Warn("Failed to get player ID by user ID")
				continue
			}
			logger = logger.WithField("player_id", playerID)

			player, err := vg.Player(playerID)
			if err != nil {
				logger.WithField("error", err).Warn("Failed to get player details")
				continue
			}

			vg = v.newSession("") // Uses cache
			// Get the player summary
			summary, err := v.playerSummary(vg, player)
			if err != nil {
				logger.WithField("error", err).Warn("Failed to get player summary")
				continue
			}

			// Store the summary
			data, err := json.Marshal(summary)
			if err != nil {
				logger.WithField("error", err).Warn("Failed to marshal player summary")
				continue
			}

			// Store the VRML user data in the database (effectively linking the account)
			if _, err := v.nk.StorageWrite(v.ctx, []*runtime.StorageWrite{
				{
					Collection:      StorageCollectionVRML,
					Key:             StorageKeyVRMLSummary,
					UserID:          entry.UserID,
					Value:           string(data),
					PermissionRead:  1,
					PermissionWrite: 0,
				},
			}); err != nil {
				logger.WithField("error", err).Error("Failed to store user")
				continue
			}
			// Count the number of matches played by season
			entitlements := summary.Entitlements()

			// Assign the cosmetics to the user
			if err := AssignEntitlements(v.ctx, logger, v.nk, SystemUserID, "", entry.UserID, player.User.UserID, entitlements); err != nil {
				logger.WithField("error", err).Error("Failed to assign entitlements")
				continue
			}

			// Store the entitlements in the ledger
			ledger.Entries = append(ledger.Entries, &VRMLEntitlementLedgerEntry{
				UserID:       entry.UserID,
				VRMLUserID:   player.User.UserID,
				VRMLPlayerID: player.ThisGame.PlayerID,
				Entitlements: entitlements,
			})

			if err := VRMLEntitlementLedgerStore(v.ctx, v.nk, ledger); err != nil {
				logger.WithField("error", err).Error("Failed to store ledger")
				continue
			}
		}

	}()

	return nil
}

func (v *VRMLScanQueue) cachePlayerLists() error {
	// Check if the redis cache has the complete marker
	const VRMLPlayerListCompleteKey = VRMLPlayerMapKeyPrefix + "complete"
	count := v.redisClient.Exists(VRMLPlayerListCompleteKey)
	if count.Val() < 1 {

		playerCh := make(chan VRMLPlayerListItems, 1000)
		go v.retrievePlayerMap(playerCh)

		for {
			select {
			case <-v.ctx.Done():
				return nil // Exit if the context is done
			case playerList, ok := <-playerCh:
				if !ok {
					v.logger.Info("Finished retrieving player list from VRML API")
					return nil // Exit if the channel is closed
				}
				// Process the player list
				for _, player := range playerList.Players {
					// Store the player in the redis cache
					if err := v.redisClient.Set(VRMLPlayerMapKeyPrefix+player.UserID, player.PlayerID, 0).Err(); err != nil {
						return fmt.Errorf("failed to store player in cache: %v", err)
					}
					// Log the player being cached
				}
			}
		}
	}

	// Mark the cache as complete
	if err := v.redisClient.Set(VRMLPlayerListCompleteKey, "1", 0).Err(); err != nil {
		return fmt.Errorf("failed to mark VRML player list cache as complete: %v", err)
	}
	v.logger.Info("VRML player list cache populated successfully")
	return nil
}

func (v *VRMLScanQueue) retrievePlayerMap(playerCh chan VRMLPlayerListItems) {
	// Retrieve all players from the VRML API using a cursor-based approach
	defer close(playerCh)
	baseURLs := []string{
		"https://api.vrmasterleague.com/EchoArena/Players/Inactive",
		"https://api.vrmasterleague.com/EchoArena/Substitutes",
	}

	for _, season := range v.seasons {
		baseURLs = append(baseURLs,
			"https://api.vrmasterleague.com/EchoArena/Players?season="+season.ID,
		)
	}

	client := &http.Client{Timeout: 30 * time.Second}
	buf := bytes.NewBuffer(nil)

	rateLimiter := rate.NewLimiter(rate.Every(1100*time.Millisecond), 0) // Limit to 10 requests per second

	for _, url := range baseURLs {
		cursor := 1
		for {
			buf.Reset()

			seperator := "&"
			if !strings.Contains(url, "?") {
				seperator = "?"
			}

			url := fmt.Sprintf("%s%sposMin=%d", url, seperator, cursor)
			logger := v.logger.WithField("url", url)

			data, ok, err := v.cache.Get(url)
			if err != nil {
				logger.WithField("error", err).Error("Failed to get player list from cache")
				return
			}
			if ok {
				buf.WriteString(data)
			} else {
				logger.Debug("retrieving VRML player list")

				req, err := http.NewRequest("GET", url, nil)
				if err != nil {
					logger.WithField("error", err).Error("Failed to create request")

				}

				req.Header.Set("Accept", "application/json")
				req.Header.Set("User-Agent", "EchoVRCE-Entitlement-Checker/1.0 (contact: @sprockee)")

				rateLimiter.Wait(v.ctx) // Wait for the rate limiter

				resp, err := client.Do(req)
				if err != nil {
					logger.WithField("error", err).Error("Failed to get player list from VRML API")
					return
				}
				defer resp.Body.Close()

				switch resp.StatusCode {
				case http.StatusOK:

				case http.StatusTooManyRequests:
					// Handle rate limiting
					logger.WithFields(map[string]any{
						"reset_after": resp.Header.Get("X-RateLimit-Reset-After"),
						"new_limit":   resp.Header.Get("X-RateLimit-Limit"),
					}).Warn("Rate limit exceeded, retrying after delay")

					if h := resp.Header.Get("X-RateLimit-Reset-After"); h != "" {
						resetAfter, err := time.ParseDuration(resp.Header.Get("X-RateLimit-Reset-After") + "s")
						if err != nil {
							logger.WithField("error", err).Error("Failed to parse Retry-After header")
							return
						}
						// Parse as "X per Y seconds"
						parts := strings.Split(resp.Header.Get("X-RateLimit-Limit"), " ")
						if len(parts) != 4 {
							logger.WithField("header", h).Error("Invalid X-RateLimit-Limit header format")
							return
						}

						tokens, err := strconv.Atoi(parts[0])
						if err != nil {
							logger.WithField("error", err).Error("Failed to parse X-RateLimit-Limit header")
							return
						}

						seconds, err := time.ParseDuration(parts[2] + "s")
						if err != nil {
							logger.WithField("error", err).Error("Failed to parse X-RateLimit-Reset-After header")
							return
						}
						newRate := rate.Every(seconds / time.Duration(tokens))
						logger.WithFields(map[string]any{
							"new_rate":    newRate,
							"reset_after": resetAfter,
						}).Info("Resetting rate limiter")

						<-time.After(resetAfter)                  // Wait for the reset duration before continuing
						rateLimiter = rate.NewLimiter(newRate, 0) // Reset the rate limiter with the new limit and burst
						continue
					}

				}
				// Read the response body
				if _, err := buf.ReadFrom(resp.Body); err != nil {
					logger.WithField("error", err).Error("Failed to read player list response body")
					return
				}
				if buf.Len() == 0 {
					break
				}
				// Cache the response
				if err := v.cache.Set(url, buf.String()); err != nil {
					logger.WithField("error", err).Error("Failed to cache player list")
					return
				}
			}

			var list VRMLPlayerListItems
			if err := json.NewDecoder(buf).Decode(&list); err != nil {
				logger.WithField("error", err).Error("Failed to decode player list response")
				return
			}

			if len(list.Players) == 0 {
				break
			}

			// Send the players to the channel
			playerCh <- list

			cursor = cursor + len(list.Players)
		}
	}
}

func (v *VRMLScanQueue) newSession(token string) *vrmlgo.Session {
	vg := vrmlgo.New(token)
	vg.Cache = v.cache
	vg.CacheEnabled = true
	return vg
}

func (v *VRMLScanQueue) Add(userID, playerID string) error {
	if userID == "" || playerID == "" {
		return fmt.Errorf("userID and token cannot be empty")
	}
	// Send it to the queue channel
	return v.enqueue(VRMLScanQueueEntry{
		UserID:     userID,
		VRMLUserID: playerID,
	})
}

func (v *VRMLScanQueue) enqueue(entry VRMLScanQueueEntry) error {
	// Check if it's already in the queue
	exists, err := v.redisClient.SIsMember(v.queueKey, entry.String()).Result()
	if err != nil {
		return fmt.Errorf("failed to check if entry exists in queue: %v", err)
	}
	if exists {
		v.logger.WithField("entry", entry.String()).Debug("Entry already exists in queue, skipping enqueue")
		return nil
	}
	// Add the entry to the queue
	return v.redisClient.RPush(v.queueKey, entry.String()).Err()
}

func (v *VRMLScanQueue) dequeue() (VRMLScanQueueEntry, error) {
	res := v.redisClient.LPop(v.queueKey)
	if res.Err() == redis.Nil {
		return VRMLScanQueueEntry{}, ErrQueueEmpty
	}
	data, err := res.Bytes()
	if err != nil {
		return VRMLScanQueueEntry{}, fmt.Errorf("failed to read queue entry: %v", err)
	}

	entry := VRMLScanQueueEntry{}
	if err := json.Unmarshal(data, &entry); err != nil {
		return VRMLScanQueueEntry{}, fmt.Errorf("failed to unmarshal queue entry: %v", err)
	}
	return entry, err
}

func (v *VRMLScanQueue) playerSummary(vg *vrmlgo.Session, player *vrmlgo.Player) (*VRMLPlayerSummary, error) {

	// Get the seasons for the game
	seasons, err := vg.GameSeasons(VRMLEchoArenaShortName)
	if err != nil {
		return nil, fmt.Errorf("failed to get seasons: %v", err)
	}
	// Create a map of seasons
	seasonNameMap := make(map[string]*vrmlgo.Season)
	for _, s := range seasons {
		seasonNameMap[s.Name] = s
	}

	teamIDs := player.ThisGame.TeamIDs()
	teams := make(map[string]*vrmlgo.Team)
	// Get the teams for the player
	for _, teamID := range teamIDs {

		details, err := vg.Team(teamID)
		if err != nil {
			return nil, fmt.Errorf("failed to get team details: %v", err)
		}
		teams[teamID] = details.Team
	}

	// Get the match history for each team
	matchesByTeamBySeason := make(map[VRMLSeasonID]map[string][]string)
	for _, teamID := range teamIDs {

		details, err := vg.Team(teamID)
		if err != nil {
			return nil, fmt.Errorf("failed to get team details: %v", err)
		}

		t := details.Team

		history, err := vg.TeamMatchesHistory(t.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to get team match history: %v", err)
		}

		// Create a map of matches by team, season
		for _, h := range history {
			seasonID := VRMLSeasonID(seasonNameMap[h.SeasonName].ID)
			if _, ok := matchesByTeamBySeason[seasonID]; !ok {
				matchesByTeamBySeason[seasonID] = make(map[string][]string)
			}

			if _, ok := matchesByTeamBySeason[seasonID][t.ID]; !ok {
				matchesByTeamBySeason[seasonID][t.ID] = make([]string, 0)
			}

			matchesByTeamBySeason[seasonID][t.ID] = append(matchesByTeamBySeason[seasonID][t.ID], h.MatchID)
		}
	}

	// Get the match details for the first two matches of each season
	matchCountsBySeasonID := make(map[VRMLSeasonID]map[string]int)

	for sID, matchesByTeam := range matchesByTeamBySeason {

		for tID, matchIDs := range matchesByTeam {

			for _, mID := range matchIDs {

				// Get the match details
				matchDetails, err := vg.GameMatch(VRMLEchoArenaShortName, mID)
				if err != nil {
					return nil, fmt.Errorf("failed to get match details: %v", err)
				}

				// Skip forfeits
				if matchDetails.Match.IsForfeit {
					continue
				}

				// Count the number of matches the player is in
				for _, p := range matchDetails.Players() {

					// Check if the player is in the match
					if p.ID == player.ThisGame.PlayerID {
						if _, ok := matchCountsBySeasonID[sID]; !ok {
							matchCountsBySeasonID[sID] = make(map[string]int)
						}
						matchCountsBySeasonID[sID][tID]++
					}
				}
			}
		}
	}
	member, err := vg.Member(player.User.UserID, vrmlgo.WithUseCache(false))
	if err != nil {
		return nil, fmt.Errorf("failed to get member data: %v", err)
	}

	return &VRMLPlayerSummary{
		User:                      member.User,
		Player:                    player,
		Teams:                     teams,
		MatchCountsBySeasonByTeam: matchCountsBySeasonID,
	}, nil
}

// Use a brute force search for the player ID, by searching for the player name in all seasons
func (*VRMLScanQueue) searchPlayerBySeasons(vg *vrmlgo.Session, seasons []*vrmlgo.Season, vrmlID string) (*vrmlgo.Player, error) {

	member, err := vg.Member(vrmlID, vrmlgo.WithUseCache(false))
	if err != nil {
		return nil, fmt.Errorf("failed to get member data: %v", err)
	}

	for _, s := range seasons {

		players, err := vg.GamePlayersSearch(s.GameURLShort, s.ID, member.User.UserName)
		if err != nil {
			return nil, fmt.Errorf("failed to search for player: %v", err)
		}

		for _, p := range players {
			player, err := vg.Player(p.ID)
			if err != nil {
				return nil, fmt.Errorf("failed to get player: %v", err)
			}

			if player.User.UserID == member.User.ID {
				return player, nil
			}
		}
	}

	return nil, ErrPlayerNotFound
}

func (d *DiscordAppBot) handleVRMLVerify(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
	// accountLinkCommandHandler handles the account link command from Discord

	var (
		nk = d.nk
		db = d.db
	)

	logger = logger.WithFields(map[string]any{
		"discord_id": i.Member.User.ID,
		"username":   i.Member.User.Username,
		"uid":        userID,
	})

	editResponseFn := func(format string, a ...any) error {
		content := fmt.Sprintf(format, a...)
		_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{Content: &content})
		return err
	}

	profile, err := EVRProfileLoad(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("failed to load profile: %w", err)
	}

	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsLoading | discordgo.MessageFlagsEphemeral,
		},
	}); err != nil {
		logger.WithField("error", err).Error("Failed to send interaction response")
	}

	if profile.VRMLUserID() != "" {
		// User is already linked
		// Check if the User is valid.

		// Retrieve the VRML user data
		vg := vrmlgo.New("")
		m, err := vg.Member(profile.VRMLUserID(), vrmlgo.WithUseCache(false))
		if err != nil {
			return fmt.Errorf("failed to get member data: %w", err)
		}
		vrmlUser := m.User

		if vrmlUser.GetDiscordID() != i.Member.User.ID {
			// VRML User is linked to a different Discord account
			logger.Warn("Discord ID mismatch")
			vrmlLink := fmt.Sprintf("[%s](https://vrmasterleague.com/EchoArena/Users/%s)", vrmlUser.UserName, vrmlUser.ID)
			thisDiscordTag := fmt.Sprintf("%s#%s", user.Username, user.Discriminator)
			return editResponseFn(fmt.Sprintf("VRML account %s is currently linked to %s. Please relink the VRML account to this Discord account (%s).", vrmlLink, vrmlUser.DiscordTag, thisDiscordTag))
		}

		// Queue the event to count matches and assign entitlements
		if err := SendEvent(ctx, nk, &EventVRMLAccountLink{
			UserID:     userID,
			VRMLUserID: vrmlUser.ID,
		}); err != nil {
			return fmt.Errorf("failed to queue VRML account linked event: %w", err)
		}

		return editResponseFn("Your [VRML account](%s) is already linked. Reverifying your entitlements...", "https://vrmasterleague.com/Users/"+profile.VRMLUserID())
	}

	// User is not linked
	go func() {
		// Start the OAuth flow

		vars, _ := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

		// Start the OAuth flow
		timeoutDuration := 5 * time.Minute
		flow, err := NewVRMLOAuthFlow(vars["VRML_OAUTH_CLIENT_ID"], vars["VRML_OAUTH_REDIRECT_URL"], timeoutDuration)
		if err != nil {
			logger.WithField("error", err).Error("Failed to start OAuth flow")
			return
		}

		// Send the link to the user
		if err := editResponseFn(fmt.Sprintf("To assign your cosmetics, [Verify your VRML account](%s)", flow.url)); err != nil {
			logger.WithField("error", err).Error("Failed to edit response")
		}

		var token string
		var vrmlUser *vrmlgo.User

		// Wait for the token to be returned
		select {
		case <-time.After(timeoutDuration):
			editResponseFn("OAuth flow timed out. Please run the command again.")
			return // Timeout
		case token = <-flow.tokenCh:
			// Token received
			vg := vrmlgo.New(token)
			vrmlUser, err = vg.Me(vrmlgo.WithUseCache(false))
			if err != nil {
				logger.Error("Failed to get VRML user data")
				return
			}
		}
		logger = logger.WithFields(map[string]any{
			"vrml_id":         vrmlUser.ID,
			"vrml_username":   vrmlUser.UserName,
			"vrml_discord_id": vrmlUser.GetDiscordID(),
		})

		if vrmlUser.GetDiscordID() != i.Member.User.ID {
			logger.Warn("Discord ID mismatch")
			// VRML User is linked to a different Discord account
			vrmlLink := fmt.Sprintf("[%s](https://vrmasterleague.com/EchoArena/Users/%s)", vrmlUser.UserName, vrmlUser.ID)
			thisDiscordTag := fmt.Sprintf("%s#%s", user.Username, user.Discriminator)
			if err := editResponseFn(fmt.Sprintf("VRML account %s is currently linked to %s. Please relink the VRML account to this Discord account (%s).", vrmlLink, vrmlUser.DiscordTag, thisDiscordTag)); err != nil {
				logger.WithField("error", err).Error("Failed to edit response")
			}
			return
		}

		// Link the accounts
		if err := LinkVRMLAccount(ctx, db, nk, userID, vrmlUser.ID); err != nil {
			if err, ok := err.(*AccountAlreadyLinkedError); ok {
				// Account is already linked to another user
				logger.WithField("owner_user_id", err.OwnerUserID).Error("Account already linked to another user.")
				if err := editResponseFn("Account already owned by another user, [Contact EchoVRCE](%s) if you need to unlink it.", ServiceSettings().ReportURL); err != nil {
					logger.WithField("error", err).Error("Failed to edit response")
				}
				return
			}
			logger.WithField("error", err).Error("Failed to link accounts")
			if err := editResponseFn("Failed to link accounts"); err != nil {
				logger.WithField("error", err).Error("Failed to edit response")
			}
			return
		}
		logger.Info("Linked VRML account")
		if err := editResponseFn(fmt.Sprintf("Your VRML account (`%s`) has been verified/linked. It will take a few minutes--up to a few hours--to update your entitlements.", vrmlUser.UserName)); err != nil {
			logger.WithField("error", err).Error("Failed to edit response")
			return
		}

	}()

	return nil
}

func (v *VRMLScanQueue) getPlayerIDByUserID(userID string) (string, error) {
	// Get the VRML player by user ID
	resp := v.redisClient.Get(VRMLPlayerMapKeyPrefix + userID)
	if resp.Err() != nil {
		if resp.Err() == redis.Nil {
			return "", fmt.Errorf("no VRML player found for user ID %s", userID)
		}
		return "", fmt.Errorf("failed to get VRML player for user ID %s: %v", userID, resp.Err())
	}
	playerID, err := resp.Result()
	if err != nil {
		return "", fmt.Errorf("failed to parse VRML player ID for user ID %s: %v", userID, err)
	}
	return playerID, nil
}
