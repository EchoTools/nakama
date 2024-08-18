package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	EchoTaxiPrefix      = "https://echo.taxi/"
	SprockLinkPrefix    = "https://sprock.io/"
	sprockLinkDiscordID = "1102051447597707294"

	EchoTaxiStorageCollection = "EchoTaxi"
	EchoTaxiStorageKey        = "Hail"
	TaxiEmoji                 = "🚕"

	LinkPattern = `(?i)(?P<httpPrefix>https://echo.taxi/|https://sprock.io/)?(?P<appLinkPrefix>(?:spark://[scj]/|Aether://)?)(?P<matchID>[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12})`
)

var (
	LinkRegex = regexp.MustCompile(LinkPattern)
)

type TaxiBot struct {
	sync.Mutex
	node   string
	ctx    context.Context
	logger runtime.Logger
	nk     runtime.NakamaModule
	db     *sql.DB
	dg     *discordgo.Session

	HailCount    int
	linkRegistry *TaxiLinkRegistry

	deconflict   *MapOf[string, bool]
	userChannels *MapOf[string, string]

	rateLimiters         *MapOf[string, *rate.Limiter]
	messageRatePerSecond rate.Limit
	messageBurst         int
}

func NewTaxiBot(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB, node string, dg *discordgo.Session) *TaxiBot {

	nkgo := nk.(*RuntimeGoNakamaModule)
	linkRegistry := NewTaxiLinkRegistry(ctx, logger, nk, nkgo.config, dg)
	taxi := &TaxiBot{
		node:   node,
		ctx:    ctx,
		logger: logger,
		nk:     nk,
		dg:     dg,
		db:     db,

		linkRegistry: linkRegistry,
		HailCount:    0,
		deconflict:   &MapOf[string, bool]{},   // sprockLinkChannels maps discord channel ids to boolean
		userChannels: &MapOf[string, string]{}, // userChannels maps discord user ids to channel ids

		rateLimiters:         &MapOf[string, *rate.Limiter]{},
		messageRatePerSecond: 1 / 10.0,
		messageBurst:         3,
	}

	// Start the processing/deconflict queue
	err := taxi.loadCount()
	if err != nil {
		logger.WithField("error", err.Error()).Warn("Error loading count")
	}

	go func() {
		statusTicker := time.NewTicker(10 * time.Second)
		storeHailCountTicker := time.NewTicker(5 * time.Minute)
		messagePruneTicker := time.NewTicker(30 * time.Second)
		limiterTicker := time.NewTicker(60 * time.Minute)
		defer statusTicker.Stop()
		defer storeHailCountTicker.Stop()
		defer messagePruneTicker.Stop()
		defer limiterTicker.Stop()
		for {
			select {
			case <-ctx.Done():
				return

			case <-statusTicker.C:
				// Update the status
				if err := linkRegistry.setStatus(taxi.HailCount, taxi.linkRegistry.Count()); err != nil {
					logger.WithField("error", err.Error()).Warn("Error setting status")
				}
			case <-storeHailCountTicker.C:
				// Save the hail count
				if err := taxi.saveCount(); err != nil {
					logger.WithField("error", err.Error()).Warn("Error saving count")
				}

			case <-messagePruneTicker.C:
				// do housekeeping on a tick to remove inactive matches
				_, _, err := linkRegistry.Prune()
				if err != nil {
					logger.WithField("error", err.Error()).Warn("Error pruning matches")
				}

			case <-limiterTicker.C:
				// Clear the rate limiters for the channels
				taxi.pruneRateLimiters()
			}
		}
	}()

	return taxi
}

func (e *TaxiBot) Initialize(dg *discordgo.Session) error {

	dg.Identify.Intents |= discordgo.IntentGuildMessages
	dg.Identify.Intents |= discordgo.IntentGuildMessageReactions
	dg.Identify.Intents |= discordgo.IntentDirectMessages
	dg.Identify.Intents |= discordgo.IntentDirectMessageReactions
	dg.Identify.Intents |= discordgo.IntentMessageContent

	//dg.LogLevel = discordgo.LogDebug

	dg.StateEnabled = true
	dg.AddHandler(e.handleMessageCreate)
	dg.AddHandler(e.handleMessageReactionAdd)

	dg.AddHandler(func(s *discordgo.Session, r *discordgo.RateLimit) {
		e.logger.WithField("rate_limit", r).Warn("EchoTaxi is being rate limited.")
	})

	err := dg.Open()
	if err != nil {
		return fmt.Errorf("Error opening EchoTaxi connection to Discord: %v", err)
	}

	e.logger.Info("Initialized EchoTaxi runtime module.")
	return nil
}

func (e *TaxiBot) loadLimiter(channelID string, rateLimit rate.Limit, burst int) *rate.Limiter {
	limiter, found := e.rateLimiters.Load(channelID)
	if !found {
		limiter = rate.NewLimiter(rateLimit, burst)
		e.rateLimiters.Store(channelID, limiter)
	}
	return limiter
}

func (e *TaxiBot) pruneRateLimiters() {
	e.rateLimiters.Range(func(key string, value *rate.Limiter) bool {
		if value.Tokens() >= float64(value.Burst()) {
			e.rateLimiters.Delete(key)
		}
		return true
	})
}

func (e *TaxiBot) handleMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		if m.Embeds != nil { // If the message has embeds, check the footer for a lobbyid
			// Use the footer as the content
			for _, embed := range m.Embeds {
				if embed.Footer != nil {
					m.Content = embed.Footer.Text
					break
				}
			}
		} else {
			return // Ignore messages from the bot
		}
	}

	matchLink := e.matchLinkFromStringOrNil(m.Content)
	if matchLink == nil {
		return
	}

	// Validate that the match exists
	label, err := MatchLabelByID(e.ctx, e.nk, matchLink.MatchID)
	if err != nil || label == nil {
		return
	}

	logger := e.logger.WithFields(map[string]interface{}{
		"func":       "handleMessageCreate",
		"discord_id": m.Author.ID,
		"channel_id": m.ChannelID,
		"match_id":   matchLink.MatchID.String(),
		"msg":        m,
	})

	// Get the channel rate limiter
	limiter := e.loadLimiter(m.ChannelID, e.messageRatePerSecond, e.messageBurst)
	if !limiter.Allow() {
		logger.Warn("Channel has been rate limited.")
		return
	}

	if m.Author.ID == sprockLinkDiscordID {
		// If the message is from SprockLink, then deconflict
		e.deconflict.Store(m.ChannelID, true)
		// Send this trackID to the queue
	} else {
		reactOnly, isDeconflicted := e.deconflict.LoadOrStore(m.ChannelID, false)
		if isDeconflicted && reactOnly {
			// If the channel is deconflicted, then ignore the message
			return
		}
		if !isDeconflicted {
			// If the channel is not deconflicted, then wait for the deconflict
			<-time.After(2 * time.Second)
			reactOnly, isDeconflicted := e.deconflict.Load(m.ChannelID)
			if isDeconflicted && reactOnly {
				// Delete the deconfliction
				e.deconflict.Delete(m.ChannelID)
				return // There will be another message from sprocklink that will be processed
			}
		}

		if matchLink.HTTPPrefix == "" {
			// Reply with a clickable link
			if r, err := e.dg.ChannelMessageSend(m.ChannelID, matchLink.EchoTaxiLink()); err == nil {
				// Track the response message
				m.ID = r.ID
			} else {
				logger.Warn("Error sending message: %v", err)
			}
		}
	}

	// Track it for reactions
	e.linkRegistry.Queue(matchLink.MatchID, m.ChannelID, m.ID)
}

// setStatus updates the status of the bot
func (e *TaxiLinkRegistry) setStatus(total, active int) error {

	// Use the taxi emoji to indicate the number of active taxis
	status := fmt.Sprintf("%s x %d (%d)", TaxiEmoji, total, active)

	// Update the status
	if err := e.dg.UpdateGameStatus(0, status); err != nil {
		return fmt.Errorf("Error setting status: %v", err)
	}

	return nil
}

type EchoTaxiStorageData struct {
	HailCount int `json:"hail_count"`
}

func (e *TaxiBot) saveCount() error {

	data := EchoTaxiStorageData{
		HailCount: e.HailCount,
	}
	jsonData, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("Error marshalling data: %s", err.Error())
	}

	// Save the count
	_, err = e.nk.StorageWrite(e.ctx, []*runtime.StorageWrite{
		{
			Collection:      EchoTaxiStorageCollection,
			Key:             EchoTaxiStorageKey,
			UserID:          uuid.Nil.String(),
			Value:           string(jsonData),
			PermissionRead:  1,
			PermissionWrite: 1,
		},
	})
	if err != nil {
		return fmt.Errorf("Error saving count: %s", err.Error())
	}
	return nil
}

func (e *TaxiBot) loadCount() error {
	// Load the count
	objs, err := e.nk.StorageRead(e.ctx, []*runtime.StorageRead{
		{
			Collection: EchoTaxiStorageCollection,
			Key:        EchoTaxiStorageKey,
			UserID:     uuid.Nil.String(),
		},
	})
	if err != nil {
		return fmt.Errorf("Error loading count: %s", err.Error())
	}
	if len(objs) == 0 {
		e.HailCount = 0
	}
	data := EchoTaxiStorageData{}
	err = json.Unmarshal([]byte(objs[0].Value), &data)
	if err != nil {
		return fmt.Errorf("Error unmarshalling data: %s", err.Error())
	}
	e.HailCount = data.HailCount
	return nil
}

func (e *TaxiBot) matchLinkFromStringOrNil(content string) *MatchLink {
	match := LinkRegex.FindStringSubmatch(content)
	if len(match) == 0 {
		return nil
	}

	matchLink := &MatchLink{}
	for i, name := range LinkRegex.SubexpNames() {
		v := match[i]
		switch name {
		case "httpPrefix":
			matchLink.HTTPPrefix = v
		case "appLinkPrefix":
			matchLink.AppLinkPrefix = v
		case "matchID":
			if !strings.Contains(v, ".") {
				v = v + "." + e.node
			}
			matchLink.MatchID = MatchIDFromStringOrNil(v)
			if matchLink.MatchID.IsNil() {
				return nil
			}
		}
	}

	return matchLink
}

// Hail sets the next match for a user
func (e *TaxiBot) Hail(logger runtime.Logger, discordID string, matchID MatchID) error {
	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Second)
	defer cancel()

	// Get the nakama user id from the discord user id
	userID, err := GetUserIDByDiscordID(ctx, e.db, discordID)
	if err != nil {
		return fmt.Errorf("Error getting user id from discord id: %s", err.Error())
	}

	// Increment the hail count
	if !matchID.IsNil() {
		e.HailCount++
	}

	settings := MatchmakingSettings{}
	if err := LoadFromStorage(ctx, e.nk, userID, &settings, true); err != nil {
		return fmt.Errorf("Error loading matchmaking config: %s", err.Error())
	}
	// Update the NextMatchID
	settings.NextMatchID = matchID

	if err := SaveToStorage(ctx, e.nk, userID, &settings); err != nil {
		return fmt.Errorf("Error storing matchmaking config: %s", err.Error())
	}

	return nil
}

// handleMessageReactionAdd handles the reaction add event
// It checks if the reaction is a taxi, and if so, it arms the taxi redirect
func (e *TaxiBot) handleMessageReactionAdd(s *discordgo.Session, reaction *discordgo.MessageReactionAdd) {
	var err error
	// ignore own reactions, and non-taxi reactions
	if reaction.UserID == s.State.User.ID || reaction.Emoji.Name != TaxiEmoji {
		return
	}
	limiter := e.loadLimiter(reaction.ChannelID+reaction.UserID, 1/10.0, 1)
	if !limiter.Allow() {
		e.logger.WithField("discord_id", reaction.UserID).Warn("Rate limited reaction from single user")
	}
	// Search for a match token in the tracked messages
	// See if this is a tracked message
	t := TrackID{
		ChannelID: reaction.ChannelID,
		MessageID: reaction.MessageID,
	}

	matchID, found := e.linkRegistry.load(t)
	if !found {
		// Check if the message is a match link
		message, err := s.ChannelMessage(reaction.ChannelID, reaction.MessageID)
		if err != nil {
			e.logger.WithField("error", err).Warn("Error getting message")
		}
		matchLink := e.matchLinkFromStringOrNil(message.Content)
		if matchLink != nil {
			// Remove all other taxi reactions
			if err := s.MessageReactionsRemoveEmoji(reaction.ChannelID, reaction.MessageID, TaxiEmoji); err != nil {
				e.logger.WithField("error", err).Warn("Error removing reactions")
			}
		}

		return
	}

	logger := e.logger.WithFields(map[string]any{
		"func":       "handleMessageReactionAdd",
		"discord_id": reaction.UserID,
		"match_id":   matchID.String(),
	})
	// Check if the match is live
	if _, err = e.nk.MatchGet(e.ctx, matchID.String()); err != nil {
		logger.WithField("error", err).Warn("Error getting match")
		e.linkRegistry.Remove(t)
		return
	}
	e.linkRegistry.Clear(t, false)

	// Hail the taxi
	err = e.Hail(logger, reaction.UserID, matchID)
	if err != nil {
		logger.WithField("error", err).Warn("Error adding hail.")
		return
	}

	// Get the user's DM channel
	dmChannelID, found := e.userChannels.Load(reaction.UserID)
	if !found {
		st, err := s.UserChannelCreate(reaction.UserID)
		if err != nil {
			logger.WithField("error", err).Warn("Error creating DM channel")
			return
		}
		if st == nil {
			return
		}
		dmChannelID = st.ID
		e.userChannels.Store(reaction.UserID, dmChannelID)
	}

	// Get the rate limiter for the DM channel
	limiter = e.loadLimiter(dmChannelID, 1/10.0, 1)
	if !limiter.Allow() {
		logger.Warn("Rate limited hail DM message")
	} else {
		// Message the user
		// Create an echo taxi link for the message
		applink := fmt.Sprintf("<%sspark://c/%s>", EchoTaxiPrefix, strings.ToUpper(matchID.UUID.String()))
		dmMessage, err := s.ChannelMessageSend(dmChannelID, fmt.Sprintf("You have hailed a taxi to %s.\n\nGo into the game and click 'Play' on the main menu, or 'Find Match' on the lobby terminal. ", applink))
		if err != nil {
			logger.WithField("error", err).Warn("Error sending message")
		}
		if dmMessage == nil {
			return
		}
		// React to the message
		if err = s.MessageReactionAdd(dmChannelID, dmMessage.ID, TaxiEmoji); err != nil {
			logger.WithField("error", err).Warn("Error reacting to message")
		}
		// track the DM message
		e.linkRegistry.Queue(matchID, dmMessage.ChannelID, dmMessage.ID)
	}

	e.logger.WithFields(map[string]any{
		"discord_id": reaction.UserID,
		"match_id":   matchID.String(),
	}).Debug("taxi hailed.")
}

func (e *TaxiBot) handleMessageReactionRemove(s *discordgo.Session, reaction *discordgo.MessageReactionRemove) {
	if reaction.UserID == s.State.User.ID || reaction.Emoji.Name != TaxiEmoji {
		// ignore dm reactions and reactions from the bot
		return
	}

	ctx, cancel := context.WithTimeout(e.ctx, 2*time.Second)
	defer cancel()
	// Get the nakama user id from the discord user id
	userID, err := GetUserIDByDiscordID(ctx, e.db, reaction.UserID)
	if err != nil {
		return
	}

	// Remove the hail
	e.Hail(e.logger, userID, NilMatchID)

	// Clear any non-bot taxi reactions
	trackID := TrackID{
		ChannelID: reaction.ChannelID,
		MessageID: reaction.MessageID,
	}
	e.linkRegistry.Clear(trackID, false)
}

func isDMChannel(channel *discordgo.Channel) bool {
	return channel.Type == discordgo.ChannelTypeDM || channel.Type == discordgo.ChannelTypeGroupDM
}

func EchoTaxiRuntimeModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) (err error) {
	env := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

	if db == nil {
		logger.Warn("No database connection available, skipping EchoTaxi startup.")
		return
	}

	if s, ok := env["DISABLE_DISCORD_RESPONSE_HANDLERS"]; ok && s == "true" {
		return nil
	}

	node := ctx.Value(runtime.RUNTIME_CTX_NODE).(string)

	botToken, ok := env["ECHOTAXI_DISCORD_BOT_TOKEN"]
	if !ok {
		logger.Error("No Discord bot token found in environment variables. Please set ECHOTAXI_DISCORD_BOT_TOKEN.")
		return nil
	}

	dg, err := discordgo.New("Bot " + botToken)

	if err != nil {
		return err
	}

	// Initialize the taxi bot
	taxi := NewTaxiBot(ctx, logger, nk, db, node, dg)

	err = taxi.Initialize(dg)
	if err != nil {
		return err
	}

	return nil
}

type MatchLink struct {
	HTTPPrefix    string
	AppLinkPrefix string
	MatchID       MatchID
}

func (l MatchLink) AppLink() string {
	prefix := l.AppLinkPrefix
	if prefix == "" {
		prefix = "spark://c/"
	}
	id := strings.ToUpper(l.MatchID.UUID.String())
	return prefix + id
}

func (l MatchLink) EchoTaxiLink() string {
	return EchoTaxiPrefix + l.AppLink()
}

func (l MatchLink) SprockLink() string {
	return SprockLinkPrefix + l.AppLink()
}

/*
func matchDetails(ctx context.Context, s *discordgo.Session, nk runtime.NakamaModule, logger runtime.Logger) {
	embedMap := make(map[string]*discordgo.Message)
	// Get the matches

	go func() {
		matches, err := nk.MatchList(ctx, 100, true, "", lo.ToPtr(2), lo.ToPtr(MatchMaxSize), "*")
		if err != nil {
			logger.Warn("Error getting matches: %s", err.Error())
			return
		}
		for _, match := range matches {
			// Get the match
			st, err = matchStatusEmbed(ctx, s, nk, logger, "1102748367949418620", match.MatchID)
			if err != nil {
				logger.Warn("Error getting match status: %s", err.Error())
			}

			st, found := embedMap[match.MatchID]
			if found {
				// If the match is already in the map, then update the status
				_ = st
				// Send an update to the embed
				_, err = s.ChannelMessageEditEmbed("1102748367949418620", st.ID, st, st.Embeds[0])
				if err != nil {
					logger.Warn("Error editing message: %s", err.Error())
				}
			} else {

				// Creating the message
				st, err := s.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
					Content:         "",
					TTS:             false,
					Components:      components,
					Embeds:          []*discordgo.MessageEmbed{&embed},
					AllowedMentions: &discordgo.MessageAllowedMentions{Parse: []discordgo.AllowedMentionType{discordgo.AllowedMentionTypeUsers}},
				})

				if err != nil {
					// Handle the error
					println("Error sending message:", err)
				}
				embedMap[match.MatchID] = st

			}
		}

	}()

}

func matchStatusEmbed(ctx context.Context, s *discordgo.Session, nk runtime.NakamaModule, logger runtime.Logger, channelID string, MatchID string) (*discordgo.Message, []discordgo.MessageComponent, error) {

	// Get the match
	match, err := nk.MatchGet(ctx, MatchID)
	if err != nil {
		return err
	}

	signal := EvrSignal{
		Signal: SignalGetPresences,
		Data:   []byte{},
	}
	signalJson, err := json.Marshal(signal)
	if err != nil {
		logger.Warn("Error marshalling signal: %s", err.Error())

	}
	// Signal the match to get the presences
	data, err := nk.MatchSignal(ctx, MatchID, string(signalJson))
	if err != nil {
		return err
	}
	presences := make([]*EvrMatchPresence, 0, MatchMaxSize)
	err = json.Unmarshal([]byte(data), &presences)
	if err != nil {
		return err
	}

	// Get the LAbel for
	MatchID = match.MatchID[:strings.LastIndex(match.MatchID, ".")]
	sparkLink := "https://echo.taxi/spark://c/" + strings.ToUpper(MatchID)

	// Unmarshal the label
	label := &matchState{}
	err = json.Unmarshal([]byte(match.GetLabel().Value), label)
	if err != nil {
		return err
	}

	// Get the guild
	guild, err := s.Guild(label.GuildID)
	if err != nil {
		return err
	}
	guildID := guild.ID
	guildName := guild.Name
	serverLocation := label.Broadcaster.IPinfo.Location

	gameType := ""
	switch label.LobbyType {
	case LobbyType(evr.ModeSocialPublic):
		gameType = "Public Social Lobby"
	case LobbyType(evr.ModeSocialPrivate):
		gameType = "Private Social Lobby"
	case LobbyType(evr.ModeCombatPrivate):
		gameType = "Private Combat Match"
	case LobbyType(evr.ModeCombatPublic):
		gameType = "Public Combat Match"
	case LobbyType(evr.ModeArenaPrivate):
		gameType = "Private Arena Match"
	case LobbyType(evr.ModeArenaPublic):
		gameType = "Public Arena Match"
	}

	// Create a comma-delimited list of the players by their discordIds
	players := make([]string, 0, len(presences))
	for _, p := range presences {
		// Get the user
		s := "<@" + p.DiscordID + ">"
		players = append(players, s)
	}
	// put the blue players on the left of a :small_orange_diamond:
	// put the orange players on the right of a :small_orange_diamond:
	bluePlayers := make([]string, 0)
	orangePlayers := make([]string, 0)

	for _, p := range presences {
		if p.TeamIndex == 0 {
			bluePlayers = append(bluePlayers, "<@"+p.DiscordID+">")
		} else if p.TeamIndex == 1 {
			orangePlayers = append(orangePlayers, "<@"+p.DiscordID+">")
		}
	}

	playersList := strings.Join(bluePlayers, ", ") + " :small_blue_diamond: :small_orange_diamond: " + strings.Join(orangePlayers, ", ")

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				discordgo.Button{
					Style: discordgo.LinkButton,
					Label: "Spark",
					URL:   sparkLink,
				},
				discordgo.Button{
					Style:    discordgo.PrimaryButton,
					Label:    "EchoTaxi",
					CustomID: "row_0_button_1",
					Emoji: &discordgo.ComponentEmoji{
						Name: TaxiEmoji,
					},
				},
				discordgo.SelectMenu{
					CustomID:    "row_0_select_2",
					Placeholder: "Select Team",
					Options: []discordgo.SelectMenuOption{
						{
							Label:       "Blue",
							Value:       "Orange",
							Description: "Spectator",
						},
					},
					MinValues: lo.ToPtr(1),
					MaxValues: 1,
				},
			},
		},
	}

	// Constructing the embed
	embed := discordgo.MessageEmbed{
		Type:        "rich",
		Title:       gameType,
		Description: serverLocation,
		Color:       0x0d8b8b,
		Author: &discordgo.MessageEmbedAuthor{
			Name: guildName,
			URL:  "https://discord.com/channels/" + guildID,
		},
		Footer: &discordgo.MessageEmbedFooter{
			Text: serverLocation + " " + playersList,
		},
		URL: sparkLink,
	}

	return embeds, components, nil
}
*/
