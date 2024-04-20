package server

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"context"
	"database/sql"

	"github.com/bwmarrin/discordgo"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	echoTaxiPrefix      = "https://echo.taxi/"
	sprockLinkPrefix    = "https://sprock.io/"
	sprockLinkDiscordId = "1102051447597707294"

	EchoTaxiStorageCollection = "EchoTaxi"
	EchoTaxiStorageKey        = "Hail"
	TaxiEmoji                 = "🚕"
)

var (
	matchIDPattern = regexp.MustCompile(`([-0-9A-Fa-f]{36})`)
)

type TaxiLinkMessage struct {
	ChannelID string
	MessageID string
}

type TaxiLinkRegistry struct {
	sync.Mutex
	ctx         context.Context
	ctxCancelFn context.CancelFunc
	nk          runtime.NakamaModule
	logger      runtime.Logger
	dg          *discordgo.Session

	node    string
	tracked map[MatchToken][]TaxiLinkMessage

	reactOnlyChannels sync.Map
}

func NewTaxiLinkRegistry(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, dg *discordgo.Session) *TaxiLinkRegistry {
	node := ctx.Value(runtime.RUNTIME_CTX_NODE).(string)
	ctx, cancel := context.WithCancel(ctx)

	taxi := &TaxiLinkRegistry{
		ctx:         ctx,
		node:        node,
		ctxCancelFn: cancel,
		nk:          nk,
		logger:      logger,
		dg:          dg,
		tracked:     make(map[MatchToken][]TaxiLinkMessage),
	}

	// do housekeeping on a tick to remove inactive matches
	go func() {
		defer cancel()
		ticker := time.NewTicker(30 * time.Second)
		for {
			select {
			case <-ticker.C:
				cnt, _ := taxi.Prune()

				taxi.Status(cnt)
			case <-ctx.Done():
				return
			}
		}
	}()
	return taxi
}

func (e *TaxiLinkRegistry) Stop() {
	e.ctxCancelFn()
}

// React adds a taxi reaction to a message
func (e *TaxiLinkRegistry) React(channelID, messageID string) error {
	err := e.dg.MessageReactionAdd(channelID, messageID, TaxiEmoji)
	if err != nil {
		return err
	}

	// Remove any other taxi reactions
	_ = e.Clear(channelID, messageID, false)

	return nil
}

// Track adds a message to the tracker
func (e *TaxiLinkRegistry) Track(matchToken MatchToken, channelID, messageID string) {

	t := TaxiLinkMessage{
		ChannelID: channelID,
		MessageID: messageID,
	}

	e.Lock()
	existing, found := e.tracked[matchToken]
	if !found {
		e.tracked[matchToken] = []TaxiLinkMessage{t}
	}
	existing = append(existing, t)
	e.Unlock()
}

func (e *TaxiLinkRegistry) Remove(matchToken MatchToken) error {
	e.Lock()
	defer e.Unlock()

	tracks, found := e.tracked[matchToken]
	if !found {
		return nil
	}

	for _, t := range tracks {
		if err := e.Clear(t.ChannelID, t.MessageID, true); err != nil {
			return err
		}
	}

	delete(e.tracked, matchToken)
	return nil
}

// Clear removes all taxi reactions
func (e *TaxiLinkRegistry) Clear(channelID, messageID string, all bool) error {

	// List the users that reacted with the taxi emoji
	users, err := e.dg.MessageReactions(channelID, messageID, TaxiEmoji, 100, "", "")
	if err != nil || len(users) == 0 {
		return err
	}

	// Remove all reactions, except the bot's (if all is false)
	for _, user := range users {
		if !all && user.ID == e.dg.State.User.ID {
			continue
		}

		// Remove the reaction
		if err = e.dg.MessageReactionRemove(channelID, messageID, TaxiEmoji, user.ID); err != nil {
			return err
		}
	}

	return nil
}

// FindAppLink extracts the app link from a message
func (e *TaxiLinkRegistry) FindAppLink(content string) string {

	// Known App prefixes
	knownPrefixes := []string{
		"spark://",
		"Aether://",
	}

	// Check if the message contains an already prefixed link
	for _, prefix := range knownPrefixes {
		if !strings.Contains(content, prefix) {
			continue
		}

		// Find the word that contains the match ID
		for _, word := range strings.Fields(content) {

			// Check if the word has a known prefix, and a match ID suffix
			if strings.HasPrefix(word, prefix) && strings.HasSuffix(word, prefix) {

				return word
			}
		}
	}

	return ""
}

// Process processes a message for taxi reactions and link responses
func (e *TaxiLinkRegistry) Process(channelID, messageID, content string, httpOnly bool) (err error) {
	// ignore dm reactions and reactions from the bot

	// The Echo Taxi prefix
	const EchoTaxiPrefix = "https://echo.taxi/"

	// Detect a matchID in the message
	var matchID string
	if matchID = matchIDPattern.FindString(content); matchID == "" {
		return
	}

	// Construct a match token
	matchToken := MatchTokenFromStringOrNil(matchID)
	if matchToken == "" {
		return
	}

	// Check if the match is in progress
	if match, _ := e.nk.MatchGet(e.ctx, matchToken.String()); match == nil {
		return
	}

	// Check if the link is clickable (i.e. HTTP(s))
	if !httpOnly && !strings.HasPrefix(content, "http") {

		// If the link is not clickable, extract the app link
		applink := e.FindAppLink(content)

		// If the app link is not found, then ignore the message
		if applink == "" {
			return
		}

		// Try to respond in the channel with a "clickable" link
		if r, err := e.dg.ChannelMessageSend(channelID, echoTaxiPrefix+applink); err == nil {

			// If the response was successful, then react and track the new message
			messageID = r.ID
		}
	}

	// Track the message/response
	if err = e.React(channelID, messageID); err == nil {

		// Track the message
		e.Track(matchToken, channelID, messageID)

		return nil
	}
	return
}

// Status updates the status of the bot
func (e *TaxiLinkRegistry) Status(count int) error {

	// Use the taxi emoji to indicate the number of active taxis
	status := fmt.Sprintf("%s x %d", TaxiEmoji, count)

	// Update the status
	if err := e.dg.UpdateGameStatus(0, status); err != nil {
		return fmt.Errorf("Error setting status: %v", err)
	}

	return nil
}

// Prune removes all inactive matches, and their reactions
func (e *TaxiLinkRegistry) Prune() (pruned int, err error) {

	// Check all the tracked matches
	for m, tracks := range e.tracked {

		// Check if the match is still active
		if _, err := e.nk.MatchGet(e.ctx, m.String()); err != nil {
			continue
		}

		// If the match is not active, then remove the match from the tracker
		delete(e.tracked, m)

		// Remove all the taxi reactions from the channel messages
		for _, t := range tracks {
			// Clear the taxi reactions
			if err := e.Clear(t.ChannelID, t.MessageID, true); err != nil {
				e.logger.Warn("Error clearing taxi reactions: %v", err)
			}
			pruned++
		}
	}
	return pruned, nil
}

// Count returns the number of actively tracked URLs
func (e *TaxiLinkRegistry) Count() (cnt int) {
	for _, tracks := range e.tracked {
		cnt += len(tracks)
	}

	return cnt
}

func EchoTaxiRuntimeModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) (err error) {
	env := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

	if s, ok := env["DISABLE_DISCORD_BOT"]; ok && s == "true" {
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

	linkRegistry := NewTaxiLinkRegistry(ctx, logger, nk, dg)

	// Initialize the taxi bot
	taxi := NewTaxiBot(ctx, logger, nk, node, linkRegistry, dg)
	if err != nil {
		return err
	}

	err = taxi.Initialize(dg)
	if err != nil {
		return err
	}

	return nil
}

type TaxiBot struct {
	node   string
	ctx    context.Context
	logger runtime.Logger
	nk     runtime.NakamaModule
	dg     *discordgo.Session

	HailCount    int
	linkRegistry *TaxiLinkRegistry

	sprockLinkChannels *MapOf[string, bool]
	userChannels       *MapOf[string, string]
}

func NewTaxiBot(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, node string, linkRegistry *TaxiLinkRegistry, dg *discordgo.Session) *TaxiBot {

	taxi := &TaxiBot{
		node:   node,
		ctx:    ctx,
		logger: logger,
		nk:     nk,
		dg:     dg,

		linkRegistry:       linkRegistry,
		HailCount:          0,
		sprockLinkChannels: &MapOf[string, bool]{},   // sprockLinkChannels maps discord channel ids to boolean
		userChannels:       &MapOf[string, string]{}, // userChannels maps discord user ids to channel ids
	}

	return taxi
}

func (e *TaxiBot) Initialize(dg *discordgo.Session) error {

	dg.Identify.Intents |= discordgo.IntentGuildMessages
	dg.Identify.Intents |= discordgo.IntentGuildMessageReactions
	dg.Identify.Intents |= discordgo.IntentDirectMessages
	dg.Identify.Intents |= discordgo.IntentDirectMessageReactions
	dg.Identify.Intents |= discordgo.IntentMessageContent

	dg.StateEnabled = true

	err := dg.Open()
	if err != nil {
		return fmt.Errorf("Error opening EchoTaxi connection to Discord: %v", err)
	}

	e.logger.Info("Initialized EchoTaxi runtime module.")
	return nil
}

func (e *TaxiBot) handleMessageCreate(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Ignore messages from the bot
	if m.Author.ID == s.State.User.ID {
		return
	}

	httpOnly := false
	// Deconflict avoids double messages from SprockLink
	if reactOnly := e.Deconflict(m); reactOnly {
		httpOnly = true // only react to http links (like echo.taxi or sprock.io)
	}

	// Process the message
	if err := e.linkRegistry.Process(m.ChannelID, m.ID, m.Content, httpOnly); err != nil {
		e.logger.Warn("Error processing message: %v", err)
	}
}

// Deconflict avoids double messages from SprockLink
func (e *TaxiBot) Deconflict(m *discordgo.MessageCreate) (reactOnly bool) {

	if m.Author.Bot {
		return
	}

	if m.Author.ID == sprockLinkDiscordId {

		// If the message is from SprockLink, then only react in the channel
		e.sprockLinkChannels.Store(m.ChannelID, true)
	}

	reactOnly, _ = e.sprockLinkChannels.LoadAndDelete(m.ChannelID)

	if reactOnly {
		// Wait two seconds for sprocklink to respond
		<-time.After(2 * time.Second)
		reactOnly, _ = e.sprockLinkChannels.Load(m.ChannelID)
	}

	return reactOnly
}

// Hail sets the next match for a user
func (e *TaxiBot) Hail(logger runtime.Logger, discordID string, matchToken MatchToken) error {

	// Get the nakama user id from the discord user id
	userID, _, _, err := e.nk.AuthenticateCustom(e.ctx, discordID, "", true)
	if err != nil {
		return fmt.Errorf("Error getting user id from discord id: %s", err.Error())
	}

	// Increment the hail count
	if matchToken != "" {
		e.HailCount++
	}

	// Get the user's current matchmaking settings
	ops := []*runtime.StorageRead{
		{
			Collection: MatchmakingConfigStorageCollection,
			Key:        MatchmakingConfigStorageKey,
			UserID:     userID,
		},
	}

	if e.HailCount == 1 {
		// If this is the first hail, get the hail count from storage.
		ops = append(ops, &runtime.StorageRead{
			Collection: EchoTaxiStorageCollection,
			Key:        EchoTaxiStorageKey,
			UserID:     SystemUserID,
		})
	}

	objs, err := e.nk.StorageRead(e.ctx, ops)

	if err != nil || len(objs) == 0 {
		return fmt.Errorf("Error reading matchmaking config: %s", err.Error())
	}
	settings := MatchmakingSettings{}
	if err = json.Unmarshal([]byte(objs[0].Value), &settings); err != nil {
		return fmt.Errorf("Error unmarshalling matchmaking config: %s", err.Error())
	}
	for _, obj := range objs {
		switch obj.Collection {
		case EchoTaxiStorageCollection:

			// Get the echo taxi hail count
			echoTaxi := TaxiBot{}
			if err = json.Unmarshal([]byte(obj.Value), &echoTaxi); err != nil {
				return fmt.Errorf("Error unmarshalling echo taxi hail count: %s", err.Error())
			}
			e.HailCount = echoTaxi.HailCount

		case MatchmakingConfigStorageCollection:

			// Get the user's matchmaking settings
			if err = json.Unmarshal([]byte(obj.Value), &settings); err != nil {
				return fmt.Errorf("Error unmarshalling matchmaking config: %s", err.Error())
			}

		}
	}

	// Update the NextMatchID
	settings.NextMatchToken = matchToken

	// Save the user's matchmaking settings, and the echo taxi hail count
	payload, err := json.Marshal(settings)
	if err != nil {
		return fmt.Errorf("Error marshalling matchmaking config: %s", err.Error())
	}

	if _, err = e.nk.StorageWrite(e.ctx, []*runtime.StorageWrite{
		{
			Collection: MatchmakingConfigStorageCollection,
			Key:        MatchmakingConfigStorageKey,
			UserID:     userID,
			Value:      string(payload),
		},
		{
			Collection: EchoTaxiStorageCollection,
			Key:        EchoTaxiStorageKey,
			UserID:     SystemUserID,
			Value:      fmt.Sprintf(`{"HailCount": %d}`, e.HailCount),
		},
	}); err != nil {
		return fmt.Errorf("Error writing matchmaking config: %s", err.Error())
	}
	return nil
}

// handleMessageReactionAdd handles the reaction add event
// It checks if the reaction is a taxi, and if so, it arms the taxi redirect
func (e *TaxiBot) handleMessageReactionAdd(s *discordgo.Session, reaction *discordgo.MessageReactionAdd) {
	var err error
	logger := e.logger.WithField("func", "handleMessageReactionAdd").WithField("reaction", reaction)
	// ignore own reactions, and non-taxi reactions
	if reaction.UserID == s.State.User.ID || reaction.Emoji.Name != TaxiEmoji {
		return
	}

	// Search for a match token in the tracked messages
	matchID := matchIDPattern.FindString(reaction.MessageID)
	if matchID == "" {
		return
	}

	matchToken := MatchTokenFromStringOrNil(matchID + "." + e.node)

	// Check if the match is live
	if _, err = e.nk.MatchGet(e.ctx, matchToken.String()); err != nil {
		logger.Warn("Error getting match: %s", err.Error())

		err := e.linkRegistry.Remove(matchToken)

		if err != nil {
			logger.Warn("Error clearing match: %s", err.Error())
		}
		return
	}

	e.linkRegistry.Lock()

	// Ensure the match is tracked
	e.linkRegistry.Track(matchToken, reaction.ChannelID, reaction.MessageID)

	// Clear the reactions (except for the bot)
	if err = e.linkRegistry.Clear(reaction.ChannelID, reaction.MessageID, false); err != nil {
		logger.Warn("Error clearing taxi reactions: %v", err)
	}

	// Hail the taxi
	err = e.Hail(logger, reaction.UserID, matchToken)
	if err != nil {
		logger.Warn("Error adding hail: %s", err.Error())
		return
	}

	// Get the user's DM channel
	dmChannelID, found := e.userChannels.Load(reaction.UserID)
	if !found {
		st, err := s.UserChannelCreate(reaction.UserID)
		if err != nil {
			logger.Warn("Error creating DM channel: %s", err.Error())
			return
		}
		dmChannelID = st.ID
		e.userChannels.Store(reaction.UserID, dmChannelID)
	}

	// Message the user
	// Create an echo taxi link for the message
	matchComponents := strings.SplitN(matchID, ".", 2)
	matchStr := fmt.Sprintf("<https://echo.taxi/spark://c/%s>", strings.ToUpper(matchComponents[0]))
	dmMessage, err := s.ChannelMessageSend(dmChannelID, fmt.Sprintf("You have hailed a taxi to %s. Go into the game and click 'Play' on the main menu, or 'Find Match' on the lobby terminal. ", matchStr))
	if err != nil {
		logger.Warn("Error sending message: %v", err)
	}
	// React to the message
	if err = s.MessageReactionAdd(dmChannelID, dmMessage.ID, TaxiEmoji); err != nil {
		logger.Warn("Error reacting to message: %v", err)
	}
	// track the DM message
	e.linkRegistry.Track(matchToken, dmChannelID, dmMessage.ID)

	logger.Debug("%s hailed a taxi to %s", reaction.UserID, matchID)
}

func (e *TaxiBot) handleMessageReactionRemove(s *discordgo.Session, reaction *discordgo.MessageReactionRemove) {
	if reaction.UserID == s.State.User.ID || reaction.Emoji.Name != TaxiEmoji {
		// ignore dm reactions and reactions from the bot
		return
	}

	// If the reaction is a taxi, remove the hail for the user
	userID, _, _, err := e.nk.AuthenticateCustom(e.ctx, reaction.UserID, "", true)
	if err != nil {
		e.logger.Warn("Error removing hail: %s", err.Error())
		return
	}
	// Remove the hail
	e.Hail(e.logger, userID, "")

	// Clear any non-bot taxi reactions
	if err := e.linkRegistry.Clear(reaction.ChannelID, reaction.MessageID, false); err != nil {
		e.logger.Warn("Error clearing taxi reactions: %v", err)
	}
}

type EchoTaxiHailRPCRequest struct {
	UserID     string     `json:"user_id"`
	MatchToken MatchToken `json:"match_token"`
}

type EchoTaxiHailRPCResponse struct {
	UserID     string        `json:"user_id"`
	MatchToken MatchToken    `json:"match_token"`
	Label      EvrMatchState `json:"label"`
}

func (r *EchoTaxiHailRPCResponse) String() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

func (e *TaxiBot) EchoTaxiHailRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	request := &EchoTaxiHailRPCRequest{}
	err := json.Unmarshal([]byte(payload), request)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error unmarshalling payload: %s", err.Error()), StatusInvalidArgument)
	}

	matchToken := request.MatchToken
	userID := request.UserID

	response := &EchoTaxiHailRPCResponse{
		UserID:     request.UserID,
		MatchToken: matchToken,
	}

	// If the MatchID is blank, remove the hail
	if matchToken == "" {
		// Delete the hail
		err = e.Hail(logger, userID, MatchToken(""))
		if err != nil {
			return "", runtime.NewError(fmt.Sprintf("Error removing hail: %s", err.Error()), StatusInternalError)
		}
		matchToken = ""

		return response.String(), nil
	}

	if !matchToken.IsValid() {
		return "", runtime.NewError(fmt.Sprintf("Invalid matchID: %s", matchToken), StatusInvalidArgument)
	}

	match, err := nk.MatchGet(ctx, matchToken.String())
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error getting match: %s", err.Error()), StatusInternalError)
	}

	if match == nil {
		return "", runtime.NewError(fmt.Sprintf("Match not found: %s", matchToken), StatusNotFound)
	}
	label := EvrMatchState{}
	err = json.Unmarshal([]byte(match.GetLabel().Value), &label)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error unmarshalling label: %s", err.Error()), StatusInternalError)
	}

	err = e.Hail(logger, userID, matchToken)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error adding hail: %s", err.Error()), StatusInternalError)
	}
	response.Label = label
	response.MatchToken = request.MatchToken

	return response.String(), nil
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
			st, err = matchStatusEmbed(ctx, s, nk, logger, "1102748367949418620", match.MatchId)
			if err != nil {
				logger.Warn("Error getting match status: %s", err.Error())
			}

			st, found := embedMap[match.MatchId]
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
				embedMap[match.MatchId] = st

			}
		}

	}()

}

func matchStatusEmbed(ctx context.Context, s *discordgo.Session, nk runtime.NakamaModule, logger runtime.Logger, channelID string, matchId string) (*discordgo.Message, []discordgo.MessageComponent, error) {

	// Get the match
	match, err := nk.MatchGet(ctx, matchId)
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
	data, err := nk.MatchSignal(ctx, matchId, string(signalJson))
	if err != nil {
		return err
	}
	presences := make([]*EvrMatchPresence, 0, MatchMaxSize)
	err = json.Unmarshal([]byte(data), &presences)
	if err != nil {
		return err
	}

	// Get the LAbel for
	matchId = match.MatchId[:strings.LastIndex(match.MatchId, ".")]
	sparkLink := "https://echo.taxi/spark://c/" + strings.ToUpper(matchId)

	// Unmarshal the label
	label := &EvrMatchState{}
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
