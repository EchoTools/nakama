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
	"github.com/gofrs/uuid/v5"

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
	targetRegexs = []*regexp.Regexp{
		regexp.MustCompile(`([-0-9A-Fa-f]{36})`),
		//regexp.MustCompile(`.*(spark:\/\/[jsc]\/[-0-9A-Fa-f]{36}).*`),
		//regexp.MustCompile(`.*([Aa]ether:\/\/[-0-9A-Fa-f]{36}).*`),
	}
	replacementPatterns = []*regexp.Regexp{
		regexp.MustCompile(`.*(echo\.taxi\/|sprock\.io\/)spark:\/\/([jsc]\/[-0-9A-Fa-f]{36}).*`),
		regexp.MustCompile(`.*(echo\.taxi\/|sprock\.io\/)[Aa]ether:\/\/([-0-9A-Fa-f]{36}).*`),
	}
	matchIDPattern = regexp.MustCompile(`([-0-9A-Fa-f]{36})`)
)

type EchoTaxiHail struct {
	UserID    string    `json:"user_id"`
	MatchID   string    `json:"match_id"`
	Timestamp time.Time `json:"timestamp"`
}

func NewEchoTaxiHail(userID, matchID string) *EchoTaxiHail {
	return &EchoTaxiHail{
		UserID:    userID,
		MatchID:   matchID,
		Timestamp: time.Now(),
	}
}

type EchoTaxiMessage struct {
	messageID string
	channelID string
	matchID   string
	hails     []string // UserID
}

func clearTaxiReactions(dg *discordgo.Session, channelID, messageID string, all bool) error {
	// Remove other users reactions
	users, err := dg.MessageReactions(channelID, messageID, TaxiEmoji, 100, "", "")
	if err != nil {
		return err
	}
	if len(users) == 0 {
		return nil
	}

	for _, user := range users {
		if !all && user.Bot {
			continue
		}
		err = dg.MessageReactionRemove(channelID, messageID, TaxiEmoji, user.ID)
		if err != nil {
			return err
		}
	}
	return nil
}

func (t *EchoTaxiMessage) ClearTaxiReactions(dg *discordgo.Session, all bool) error {
	return clearTaxiReactions(dg, t.channelID, t.messageID, all)
}

type EchoTaxi struct {
	sync.Mutex
	ctx         context.Context
	ctxCancelFn context.CancelFunc

	node   string
	db     *sql.DB
	nk     runtime.NakamaModule
	logger runtime.Logger
	dg     *discordgo.Session

	taxiMessages *MapOf[string, *EchoTaxiMessage] // Message ID -> EchoTaxiMessage

	userChannels      *MapOf[string, string] // User ID -> Channel ID
	linkReplyChannels *MapOf[string, bool]

	HailCount int
}

func NewEchoTaxi(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dg *discordgo.Session) *EchoTaxi {
	ctx, cancel := context.WithCancel(ctx)

	// Load the hail count
	taxi := &EchoTaxi{
		node: ctx.Value(runtime.RUNTIME_CTX_NODE).(string),

		ctx:         ctx,
		ctxCancelFn: cancel,

		db:     db,
		nk:     nk,
		logger: logger,
		dg:     dg,

		taxiMessages:      &MapOf[string, *EchoTaxiMessage]{},
		userChannels:      &MapOf[string, string]{},
		linkReplyChannels: &MapOf[string, bool]{},
	}

	// do housekeeping on a tickdo housekeeping
	go func() {
		defer cancel()
		ticker := time.NewTicker(30 * time.Second)
		for {
			select {
			case <-ticker.C:
				_, cnt := taxi.Prune()
				taxi.SaveConfiguration()
				taxi.UpdateStatus(cnt)
			case <-ctx.Done():
				return
			}
		}
	}()

	return taxi
}

func (e *EchoTaxi) SaveConfiguration() error {
	e.Lock()
	defer e.Unlock()
	payload, err := json.Marshal(e)
	if err != nil {
		return err
	}
	_, err = e.nk.StorageWrite(e.ctx, []*runtime.StorageWrite{
		{
			Collection: EchoTaxiStorageCollection,
			Key:        "config",
			UserID:     SystemUserId,
			Value:      string(payload),
		},
	})
	return err
}

func (e *EchoTaxi) Stop() {
	e.ctxCancelFn()
}

func (e *EchoTaxi) UpdateStatus(count int) error {
	status := fmt.Sprintf("%s x %d", TaxiEmoji, count)
	err := e.dg.UpdateGameStatus(0, status)
	if err != nil {
		e.logger.Warn("Error setting status: %v", err)
	}
	return nil
}

func (e *EchoTaxi) SetNextMatch(userID, matchID string) error {
	if matchID != "" {
		e.HailCount++
	}
	settings := MatchmakingSettings{}
	objs, err := e.nk.StorageRead(e.ctx, []*runtime.StorageRead{
		{
			Collection: MatchmakingConfigStorageCollection,
			Key:        MatchmakingConfigStorageKey,
			UserID:     userID,
		},
	})
	if err != nil {
		e.logger.Warn("Error reading matchmaking config: %s", err.Error())
	}
	if len(objs) > 0 {
		err = json.Unmarshal([]byte(objs[0].Value), &settings)
		if err != nil {
			e.logger.Warn("Error unmarshalling matchmaking config: %s", err.Error())
		}
	}
	// Update the NextMatchID
	settings.NextMatchID = matchID
	payload, err := json.Marshal(settings)
	if err != nil {
		e.logger.Warn("Error marshalling matchmaking config: %s", err.Error())
	}

	_, err = e.nk.StorageWrite(e.ctx, []*runtime.StorageWrite{
		{
			Collection: MatchmakingConfigStorageCollection,
			Key:        MatchmakingConfigStorageKey,
			UserID:     userID,
			Value:      string(payload),
		},
	})
	if err != nil {
		e.logger.Warn("Error writing hail: %s", err.Error())
		return err
	}
	return nil
}

func (e *EchoTaxi) CheckMatch(matchID string) (found bool) {
	// Ensure the matchID is in the correct format
	matchComponents := strings.SplitN(matchID, ".", 2)
	if len(matchComponents) == 1 {
		matchID = matchID + "." + e.node
	}
	// Check if the match is in progress
	match, _ := e.nk.MatchGet(e.ctx, matchID)
	return match != nil
}

func (e *EchoTaxi) Prune() (pruned int, remain int) {
	// Get all the hails

	e.taxiMessages.Range(func(matchID string, m *EchoTaxiMessage) bool {
		// Get the match
		found := e.CheckMatch(matchID)
		if !found {
			pruned++
			// Remove the message
			m.ClearTaxiReactions(e.dg, true)
		}
		remain++
		return true
	})
	return
}

func (e *EchoTaxi) Count() (cnt int) {
	e.taxiMessages.Range(func(matchID string, m *EchoTaxiMessage) bool {
		cnt++
		return true
	})
	return
}

func EchoTaxiRuntimeModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) (err error) {
	env := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)
	botToken, ok := env["ECHOTAXI_DISCORD_BOT_TOKEN"]
	if !ok {
		logger.Error("No Discord bot token found in environment variables. Please set ECHOTAXI_DISCORD_BOT_TOKEN.")
		return nil
	}

	bot, err := discordgo.New("Bot " + botToken)
	if err != nil {
		return err
	}

	taxi := NewEchoTaxi(ctx, logger, db, nk, bot)

	// Only activate in channels where SprockLink is NOT present
	// If sprocklink says something, then we will NOT reply to that channel in the future
	bot.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.ID == sprockLinkDiscordId {
			taxi.linkReplyChannels.Store(m.ChannelID, false)
		}
	})

	//bot.Identify.Intents |= discordgo.IntentAutoModerationExecution
	//bot.Identify.Intents |= discordgo.IntentGuilds
	//bot.Identify.Intents |= discordgo.IntentGuildMembers
	//bot.Identify.Intents |= discordgo.IntentGuildBans
	//bot.Identify.Intents |= discordgo.IntentGuildEmojis
	//bot.Identify.Intents |= discordgo.IntentGuildWebhooks
	//bot.Identify.Intents |= discordgo.IntentGuildInvites
	//bot.Identify.Intents |= discordgo.IntentGuildPresences
	bot.Identify.Intents |= discordgo.IntentGuildMessages
	bot.Identify.Intents |= discordgo.IntentGuildMessageReactions
	bot.Identify.Intents |= discordgo.IntentDirectMessages
	bot.Identify.Intents |= discordgo.IntentDirectMessageReactions
	bot.Identify.Intents |= discordgo.IntentMessageContent
	//bot.Identify.Intents |= discordgo.IntentAutoModerationConfiguration
	//bot.Identify.Intents |= discordgo.IntentAutoModerationExecution

	bot.StateEnabled = true

	bot.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
		logger.Info("Bot is connected as %s", bot.State.User.Username+"#"+bot.State.User.Discriminator)
	})

	// Respond to messages
	respond := true

	if s, ok := env["DISABLE_DISCORD_BOT"]; ok && s == "true" {
		respond = false
	}

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.MessageReactionAdd) {
		taxi.handleMessageReactionAdd(s, m)
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.MessageReactionRemove) {
		taxi.handleMessageReactionRemove(s, m)
	})

	if respond {
		bot.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
			if m.Author.Bot || m.Author.ID == sprockLinkDiscordId {
				return
			}
			taxi.handleMessageCreate_EchoTaxi_React(s, m)
			taxi.checkForSparkLink(s, m)
		})
	}

	err = bot.Open()
	if err != nil {
		return fmt.Errorf("Error opening EchoTaxi connection to Discord: %v", err)
	}

	logger.Info("Initialized EchoTaxi runtime module.")
	return nil
}

// handleMessageCreate_EchoTaxi_React adds a taxi emoji to spark link messages
func (e *EchoTaxi) handleMessageCreate_EchoTaxi_React(s *discordgo.Session, m *discordgo.MessageCreate) {
	// Check if the message contains an already prefixed link
	for _, prefix := range []string{echoTaxiPrefix, sprockLinkPrefix} {
		if !strings.Contains(m.Content, prefix) {
			continue
		}
		// If the message contains a match id, then add a taxi reaction
		matchID := extractMatchIDFromMessage(m.Content)
		if matchID == "" {
			return
		}
		// Check if the match is in progress
		if e.CheckMatch(matchID) {
			err := s.MessageReactionAdd(m.ChannelID, m.ID, TaxiEmoji)
			if err != nil {
				e.logger.Warn("Error adding reaction:", err)
			}
		}
		return
	}
}

// handleMessageReactionAdd handles the reaction add event
// It checks if the reaction is a taxi, and if so, it arms the taxi redirect
func (e *EchoTaxi) handleMessageReactionAdd(s *discordgo.Session, reaction *discordgo.MessageReactionAdd) {
	// ignore own reactions, and non-taxi reactions
	if reaction.UserID == s.State.User.ID || reaction.Emoji.Name != TaxiEmoji {
		return
	}

	ctx := e.ctx
	logger := e.logger
	nk := e.nk

	// Get the message that the reaction was added to
	message, err := s.ChannelMessage(reaction.ChannelID, reaction.MessageID)
	if err != nil {
		e.logger.Error("Error getting message: %v", err)
		return
	}
	matchID := ""
	for _, prefix := range []string{echoTaxiPrefix, sprockLinkPrefix} {
		// Only accept one prefix
		if strings.Count(message.Content, prefix) != 1 {
			continue
		}
		// If the message contains a match id, then add a taxi reaction
		matchID = extractMatchIDFromMessage(message.Content)
		if matchID == "" {
			return
		}
		// Check if the match is in progress
		if !e.CheckMatch(matchID) {
			if m, found := e.taxiMessages.LoadAndDelete(matchID); found {
				m.ClearTaxiReactions(s, true)
				return
			}
			if err := s.MessageReactionRemove(reaction.ChannelID, reaction.MessageID, TaxiEmoji, reaction.UserID); err != nil {
				logger.Warn("Error removing reaction: %v", err)
			}
			return
		}
		// Clear just the users reactions
		if err := clearTaxiReactions(s, reaction.ChannelID, reaction.MessageID, false); err != nil {
			logger.Warn("Error clearing taxi reactions: %v", err)
		}
		break
	}

	matchID = strings.ToLower(fmt.Sprintf("%s.%s", matchID, e.node))

	// Get the nakama user id from the discord user id
	userID, username, _, err := nk.AuthenticateCustom(ctx, reaction.UserID, "", true)
	if err != nil {
		logger.Warn("Error getting user id from discord id: %s", err.Error())
		return
	}

	err = e.SetNextMatch(userID, matchID)
	if err != nil {
		logger.Warn("Error adding hail: %s", err.Error())
		return
	}

	// If this is a dm, store the channel
	if reaction.GuildID == "" {
		e.userChannels.Store(userID, reaction.ChannelID)
	}

	// DM the user that they have hailed a taxi
	channelID, found := e.userChannels.Load(userID)
	if !found {
		st, err := s.UserChannelCreate(reaction.UserID)
		if err != nil {
			logger.Warn("Error creating DM channel: %s", err.Error())
			return
		}
		channelID = st.ID
	}

	e.userChannels.Store(userID, channelID)

	// Message the user
	// Create an echo taxi link for the message
	matchComponents := strings.SplitN(matchID, ".", 2)
	matchStr := fmt.Sprintf("<https://echo.taxi/spark://c/%s>", strings.ToUpper(matchComponents[0]))
	_, err = s.ChannelMessageSend(channelID, fmt.Sprintf("You have hailed a taxi to %s. Go into the game and click 'Play' on the main menu, or 'Find Match' on the lobby terminal. ", matchStr))
	if err != nil {
		logger.Warn("Error sending message: %v", err)
	}

	logger.Debug("%s hailed a taxi to %s", username, matchID)
}

func (e *EchoTaxi) handleMessageReactionRemove(s *discordgo.Session, reaction *discordgo.MessageReactionRemove) {
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
	e.SetNextMatch(userID, "")

	// Clear any non-bot taxi reactions
	if err := clearTaxiReactions(s, reaction.ChannelID, reaction.MessageID, false); err != nil {
		e.logger.Warn("Error clearing taxi reactions: %v", err)
	}
}

// handleMessageCreate_EchoTaxi_LinkReply replies to spark link messages with an echo.taxi link
func (e *EchoTaxi) checkForSparkLink(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.ID == s.State.User.ID {
		// ignore dm reactions and reactions from the bot
		return
	}

	// If it already has a prefix, then ignore the message.
	for _, prefix := range []string{echoTaxiPrefix, sprockLinkPrefix} {
		if strings.Contains(m.Content, prefix) {
			return
		}
	}
	response := ""
	// Use a replacement pattern on the message
	for _, p := range replacementPatterns {
		if p.MatchString(m.Content) {
			response = p.ReplaceAllString(m.Content, fmt.Sprintf("<%s$1>", echoTaxiPrefix))
			break
		}
	}
	if response == "" {
		return
	}
	// Check if this channel has already been replied to
	shouldReply, found := e.linkReplyChannels.LoadAndDelete(m.ChannelID)
	// If this is a new channel, wait to see if sprocklink replies
	if !found {
		time.Sleep(time.Second)
		// If sprocklink replied in the same time, then this will have loaded as false
		shouldReply, _ = e.linkReplyChannels.LoadOrStore(m.ChannelID, true)
	}
	if !shouldReply {
		// Sprocklink replied, so don't send the message
		return
	}

	// If sprocklink didn't reply, then send the message
	message, err := s.ChannelMessageSend(m.ChannelID, response)
	if err != nil {
		e.logger.Warn("Error sending message: %v", err)
	}
	// Add the taxi to the message
	err = s.MessageReactionAdd(m.ChannelID, message.ID, TaxiEmoji)
	if err != nil {
		e.logger.Warn("Error adding reaction:", err)
	}
}

func extractMatchIDFromMessage(message string) string {
	for _, p := range targetRegexs {
		if p.MatchString(message) {
			return p.FindString(message)
		}
	}
	return ""
}

type EchoTaxiHailRPCRequest struct {
	UserID  string `json:"user_id"`
	MatchID string `json:"match_id"`
}

type EchoTaxiHailRPCResponse struct {
	UserID  string        `json:"user_id"`
	MatchID string        `json:"match_id"`
	Label   EvrMatchState `json:"label"`
}

func (r *EchoTaxiHailRPCResponse) String() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

func (e *EchoTaxi) EchoTaxiHailRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	request := &EchoTaxiHailRPCRequest{}
	err := json.Unmarshal([]byte(payload), request)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error unmarshalling payload: %s", err.Error()), StatusInvalidArgument)
	}

	response := &EchoTaxiHailRPCResponse{
		UserID: request.UserID,
	}

	// If the MatchID is blank, remove the hail
	if request.MatchID == "" {
		// Delete the hail
		err = e.SetNextMatch(request.UserID, "")
		if err != nil {
			return "", runtime.NewError(fmt.Sprintf("Error removing hail: %s", err.Error()), StatusInternalError)
		}
		response.MatchID = ""

		return response.String(), nil
	}

	// Validate the matchID
	s := strings.SplitN(request.MatchID, ".", 2)

	if len(s) == 2 && s[1] != e.node {
		return "", runtime.NewError(fmt.Sprintf("Invalid matchID: %s", request.MatchID), StatusInvalidArgument)
	}

	if uuid.FromStringOrNil(s[0]) == uuid.Nil {
		return "", runtime.NewError(fmt.Sprintf("Invalid matchID: %s", request.MatchID), StatusInvalidArgument)
	}

	matchIDStr := fmt.Sprintf("%s.%s", s[0], e.node)

	match, err := nk.MatchGet(ctx, matchIDStr)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error getting match: %s", err.Error()), StatusInternalError)
	}

	if match == nil {
		return "", runtime.NewError(fmt.Sprintf("Match not found: %s", matchIDStr), StatusNotFound)
	}
	label := EvrMatchState{}
	err = json.Unmarshal([]byte(match.GetLabel().Value), &label)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error unmarshalling label: %s", err.Error()), StatusInternalError)
	}

	err = e.SetNextMatch(request.UserID, request.MatchID)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("Error adding hail: %s", err.Error()), StatusInternalError)
	}
	response.Label = label
	response.MatchID = request.MatchID

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
