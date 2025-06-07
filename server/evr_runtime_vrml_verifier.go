package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/echotools/vrmlgo/v5"
	"github.com/go-redis/redis"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

var (
	ErrQueueEmpty = errors.New("VRML scan queue is empty")
)

const (
	StorageKeyVRMLVerificationLedger = "EntitlementLedger"
)

type VRMLScanQueueEntry struct {
	UserID   string `json:"user_id"`
	Token    string `json:"token"`               // Will only be set if the user is new
	PlayerID string `json:"player_id,omitempty"` // Optional, used for VRML player ID
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
	oauthRedirectURL string
	oauthClientID    string
}

func NewVRMLScanQueue(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer, appBot *discordgo.Session) (*VRMLScanQueue, error) {
	vars := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

	// Connect to the redis server for the queue and cache
	redisUri := vars["VRML_REDIS_URI"]
	if redisUri == "" {
		return nil, errors.New("missing VRML_REDIS_URI in server config")
	}

	redisOptions, err := redis.ParseURL(redisUri)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URI: %v", err)
	}

	redisClient := redis.NewClient(redisOptions)

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
		oauthRedirectURL: vars["VRML_OAUTH_REDIRECT_URL"],
		oauthClientID:    vars["VRML_OAUTH_CLIENT_ID"],
	}

	// Register the RPC function
	if err = initializer.RegisterRpc("oauth/vrml_redirect", verifier.RedirectRPC); err != nil {
		return nil, errors.New("unable to register rpc")
	}

	verifier.Start()

	return verifier, nil
}
func (v *VRMLScanQueue) Start() error {

	_, err := v.redisClient.Ping().Result()
	if err != nil {
		return fmt.Errorf("failed to connect to Redis: %v", err)
	}

	ledger, err := VRMLEntitlementLedgerLoad(v.ctx, v.nk)
	if err != nil {
		return fmt.Errorf("failed to load VRML entitlement ledger: %v", err)
	}

	go func() {

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
				"user_id":   entry.UserID,
				"player_id": entry.PlayerID,
			})

			var (
				vg         = v.newSession(entry.Token)
				vrmlUserID string
			)

			if entry.Token == "" {
				// Get the user's VRML ID from their account metadata
				metadata, err := EVRProfileLoad(v.ctx, v.nk, entry.UserID)
				if err != nil {
					logger.WithField("error", err).Error("Failed to load account metadata")
					continue
				}
				vrmlUserID = metadata.VRMLUserID()
				member, err := vg.Member(vrmlUserID, vrmlgo.WithUseCache(false))
				if err != nil {
					logger.WithField("error", err).Error("Failed to get member data")
					continue
				}
				vrmlUserID = member.User.ID
			} else {
				// Get the vrmlUserID from the token
				vrmlUser, err := vg.Me(vrmlgo.WithUseCache(false))
				if err != nil {
					logger.WithField("error", err).Warn("Failed to get @Me data")
					continue
				}
				vrmlUserID = vrmlUser.ID
			}

			// Get the player summary
			summary, err := v.playerSummary(vg, vrmlUserID, entry.PlayerID)
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
			if err := AssignEntitlements(v.ctx, logger, v.nk, SystemUserID, "", entry.UserID, vrmlUserID, entitlements); err != nil {
				logger.WithField("error", err).Error("Failed to assign entitlements")
				continue
			}

			// Store the entitlements in the ledger
			ledger.Entries = append(ledger.Entries, &VRMLEntitlementLedgerEntry{
				UserID:       entry.UserID,
				VRMLUserID:   vrmlUserID,
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

func (v *VRMLScanQueue) newSession(token string) *vrmlgo.Session {
	vg := vrmlgo.New(token)
	vg.Cache = v.cache
	vg.CacheEnabled = true
	return vg
}

func (v *VRMLScanQueue) Add(userID, token, playerID string) error {
	if userID == "" && token == "" {
		return fmt.Errorf("userID and token cannot both be empty")
	}
	// Send it to the queue channel
	return v.enqueue(VRMLScanQueueEntry{
		UserID:   userID,
		Token:    token,
		PlayerID: playerID,
	})
}

func (v *VRMLScanQueue) enqueue(entry VRMLScanQueueEntry) error {
	return v.redisClient.RPush(v.queueKey, entry.String()).Err()
}

func (v *VRMLScanQueue) dequeue() (VRMLScanQueueEntry, error) {
	val, err := v.redisClient.LPop(v.queueKey).Bytes()
	if err == redis.Nil {
		return VRMLScanQueueEntry{}, ErrQueueEmpty
	}
	entry := VRMLScanQueueEntry{}
	if err := json.Unmarshal(val, &entry); err != nil {
		return VRMLScanQueueEntry{}, fmt.Errorf("failed to unmarshal queue entry: %v", err)
	}
	return entry, err
}

func (v *VRMLScanQueue) playerSummary(vg *vrmlgo.Session, memberID string, playerID string) (*VRMLPlayerSummary, error) {
	var (
		err     error
		player  *vrmlgo.Player
		teamIDs []string
		teams   = make(map[string]*vrmlgo.Team)
	)
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

	if playerID != "" {
		// If a player ID is provided, skip the search
		player, err = vg.Player(playerID)
		if err != nil {
			return nil, fmt.Errorf("failed to get player: %v", err)
		}
		teamIDs = player.ThisGame.TeamIDs()
	} else {
		// If no player ID is provided, try to find the player by member ID
		account, err := vg.Member(memberID, vrmlgo.WithUseCache(false))
		if err != nil {
			return nil, fmt.Errorf("failed to get member data: %v", err)
		}
		if playerID := account.PlayerID(VRMLEchoArenaShortName); playerID != "" {
			// Get the player details
			player, err = vg.Player(playerID)
			if err != nil {
				return nil, fmt.Errorf("failed to get player: %v", err)
			}
			teamIDs = account.TeamIDs(VRMLEchoArenaShortName)
		} else {
			// Try to discover the player ID if it is not found
			player, err = v.searchPlayerBySeasons(vg, seasons, memberID)
			if err != nil {
				return nil, fmt.Errorf("failed to find player: %v", err)
			}
			teamIDs = player.ThisGame.TeamIDs()
		}
	}

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
	member, err := vg.Member(memberID, vrmlgo.WithUseCache(false))
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

	if profile.VRMLUserID() != "" {
		// User is already linked
		// Check if the User is valid.
		if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{
				Flags: discordgo.MessageFlagsLoading | discordgo.MessageFlagsEphemeral,
			},
		}); err != nil {
			logger.Error("Failed to send interaction response", zap.Error(err))
		}
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
			logger.Error("Failed to start OAuth flow", zap.Error(err))
			return
		}

		// Send the link to the user
		if err := editResponseFn(fmt.Sprintf("To assign your cosmetics, [Verify your VRML account](%s)", flow.url)); err != nil {
			logger.Error("Failed to edit response", zap.Error(err))
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
				logger.Error("Failed to edit response", zap.Error(err))
			}
			return
		}

		// Link the accounts
		if err := LinkVRMLAccount(ctx, db, nk, userID, vrmlUser.ID, "", token); err != nil {
			if err, ok := err.(*AccountAlreadyLinkedError); ok {
				// Account is already linked to another user
				logger.WithField("owner_user_id", err.OwnerUserID).Error("Account already linked to another user.")
				if err := editResponseFn("Account already owned by another user, [Contact EchoVRCE](%s) if you need to unlink it.", ServiceSettings().ReportURL); err != nil {
					logger.Error("Failed to edit response", zap.Error(err))
				}
				return
			}
			logger.Error("Failed to link accounts", zap.Error(err))
			if err := editResponseFn("Failed to link accounts"); err != nil {
				logger.Error("Failed to edit response", zap.Error(err))
			}
			return
		}
		logger.Info("Linked VRML account")
		if err := editResponseFn(fmt.Sprintf("Your VRML account (`%s`) has been verified/linked. It will take a few minutes--up to a few hours--to update your entitlements.", vrmlUser.UserName)); err != nil {
			logger.Error("Failed to edit response", zap.Error(err))
			return
		}

	}()

	return nil
}
