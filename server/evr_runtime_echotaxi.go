package server

import (
	"fmt"
	"log"
	"regexp"
	"strings"
	"sync"
	"time"

	"context"
	"database/sql"

	"github.com/bwmarrin/discordgo"

	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	echoTaxiPrefix      = "https://echo.taxi/"
	sprockLinkPrefix    = "https://sprock.io/"
	sprockLinkDiscordId = "1102051447597707294"
)

var (
	targetRegexs = []*regexp.Regexp{
		regexp.MustCompile(`.*(spark:\/\/[jsc]\/[-0-9A-Fa-f]{36}).*`),
		regexp.MustCompile(`.*([Aa]ether:\/\/[-0-9A-Fa-f]{36}).*`),
	}
	reactionRegexs = []*regexp.Regexp{
		regexp.MustCompile(`.*(echo\.taxi\/|sprock\.io\/)spark:\/\/([jsc]\/[-0-9A-Fa-f]{36}).*`),
		regexp.MustCompile(`.*(echo\.taxi\/|sprock\.io\/)[Aa]ether:\/\/([-0-9A-Fa-f]{36}).*`),
	}
	matchIdRegex = regexp.MustCompile(`([-0-9A-Fa-f]{36})`)

	taxiDeferredLinks = &sync.Map{}
	deferredChannels  = &sync.Map{}
	_, _              = taxiDeferredLinks, deferredChannels
)

type echoTaxiHailRegistry struct {
	matchIdByUserId   sync.Map
	userIdByDiscordId sync.Map
	linkReplyChannels sync.Map
}

func EchoTaxiRuntimeModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) (err error) {
	env := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)
	botToken, ok := env["ECHOTAXI_DISCORD_BOT_TOKEN"]
	if !ok {
		logger.Error("No Discord bot token found in environment variables. Please set ECHOTAXI_DISCORD_BOT_TOKEN.")
		return nil
	}

	hailRegistry := &echoTaxiHailRegistry{
		matchIdByUserId:   sync.Map{},
		userIdByDiscordId: sync.Map{},
		linkReplyChannels: sync.Map{},
	}

	absDays := int(time.Since(time.Date(2023, 11, 01, 00, 00, 00, 0, time.FixedZone("-07:00", -7*60*60))).Hours() / 24)

	activityName := fmt.Sprintf("EchoVR for %d days", absDays)

	_ = activityName
	bot, err := discordgo.New("Bot " + botToken)
	if err != nil {
		return err
	}

	// Only activate in channels where SprockLink is NOT present
	// If sprocklink says something, then we will NOT reply to that channel in the future
	bot.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.ID == sprockLinkDiscordId {
			hailRegistry.linkReplyChannels.Store(m.ChannelID, false)
		}
	})

	//bot.Identify.Intents |= discordgo.IntentAutoModerationExecution
	bot.Identify.Intents |= discordgo.IntentGuilds
	bot.Identify.Intents |= discordgo.IntentGuildMembers
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

	/*
		bot.AddHandler(func(s *discordgo.Session, r *discordgo.Ready) {
			log.Printf("Bot is connected as %s", bot.State.User.Username+"#"+bot.State.User.Discriminator)
			err = s.UpdateGameStatus(0, activityName)
			if err != nil {
				log.Println("Error setting status:", err)
			}
		})
	*/

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.MessageReactionAdd) {
		handleMessageReactionAdd(ctx, s, m, nk, logger, hailRegistry)
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.MessageReactionRemove) {
		handleMessageReactionRemove(ctx, s, m, logger, hailRegistry)
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {

	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		handleMessageCreate_EchoTaxi_React(ctx, s, m, logger, nk)
	})
	bot.AddHandler(func(s *discordgo.Session, m *discordgo.MessageCreate) {
		if m.Author.Bot || m.Author.ID == sprockLinkDiscordId {
			return
		}
		checkForSparkLink(s, m, hailRegistry)
	})

	err = bot.Open()
	if err != nil {
		return err
	}

	if err := initializer.RegisterBeforeRt("MatchJoin", func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *rtapi.Envelope) (*rtapi.Envelope, error) {
		// Get the user's information from the context
		username := ctx.Value(runtime.RUNTIME_CTX_USERNAME).(string)
		userId := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)

		// Do not lookup hails for broadcasters.
		if strings.HasPrefix(username, "broadcaster") {
			return in, nil
		}

		// Check if the user has a matchId in the hailRegistry
		v, found := hailRegistry.matchIdByUserId.LoadAndDelete(userId)
		if !found {
			return in, nil
		}
		matchId := v.(string)
		matchId = strings.ToLower(matchId)

		// check that the match exists.
		_, err := nk.MatchGet(ctx, matchId)
		if err != nil {
			logger.Warn("Error getting match: %s", err.Error())
			return in, nil
		}

		in.Message.(*rtapi.Envelope_MatchJoin).MatchJoin.Id = &rtapi.MatchJoin_MatchId{MatchId: matchId}

		return in, nil
	}); err != nil {
		return err
	}
	logger.Info("Initialized EchoTaxi runtime module.")
	return nil
}

// handleMessageCreate_EchoTaxi_React adds a taxi emoji to spark link messages
func handleMessageCreate_EchoTaxi_React(ctx context.Context, s *discordgo.Session, m *discordgo.MessageCreate, logger runtime.Logger, nk runtime.NakamaModule) {
	for _, regex := range reactionRegexs {
		if regex.MatchString(m.Content) {
			results := regex.FindString(m.Content)

			node := ctx.Value(runtime.RUNTIME_CTX_NODE).(string)
			if node == "" {
				logger.Warn("Node not found in context")
				return
			}
			matchId := matchIdRegex.FindString(results) + "." + node

			// If the message contains an in-process match ID, then add a taxi reaction
			match, err := nk.MatchGet(ctx, matchId)
			if err != nil {
				logger.Warn("Error getting match: %s", err.Error())
				return
			}
			if match.GetSize() <= 1 {
				return
			}

			match.GetSize()

			err = s.MessageReactionAdd(m.ChannelID, m.ID, "🚕")
			if err != nil {
				log.Println("Error adding reaction:", err)
			}
		}
	}
}

// handleMessageReactionAdd handles the reaction add event
// It checks if the reaction is a taxi, and if so, it arms the taxi redirect
func handleMessageReactionAdd(ctx context.Context, s *discordgo.Session, reaction *discordgo.MessageReactionAdd, nk runtime.NakamaModule, logger runtime.Logger, hailRegistry *echoTaxiHailRegistry) {
	node, found := ctx.Value(runtime.RUNTIME_CTX_NODE).(string)
	if !found {
		logger.Warn("Node not found in context")
		return
	}

	if reaction.GuildID == "" || reaction.UserID == s.State.User.ID {
		// ignore dm reactions and reactions from the bot
		return
	}

	if reaction.Emoji.Name != "🚕" {
		// ignore non-taxi reactions
		return
	}

	// Get the message that the reaction was added to
	message, err := s.ChannelMessage(reaction.ChannelID, reaction.MessageID)
	if err != nil {
		log.Println("Error getting message:", err)
		return
	}

	// if the message content contains the prefix, arm the taxi redirect
	// Ignore messages with a space in them. They likely have two or more links in them.
	if !strings.Contains(message.Content, " ") && (strings.Contains(message.Content, echoTaxiPrefix) || strings.Contains(message.Content, sprockLinkPrefix)) {
		// Extract the matchId from the message
		mid := matchIdRegex.FindString(message.Content)
		if mid == "" {
			return
		}
		matchId := mid + "." + node
		// Verify that the match is in process
		_, err := nk.MatchGet(ctx, matchId)
		if err != nil {
			logger.Warn("Error getting match: %s", err.Error())
			return
		}

		// Get the nakama user id from the discord user id
		userId, username, _, err := nk.AuthenticateCustom(ctx, reaction.UserID, "", true)
		if err != nil {
			logger.Warn("Error getting user id from discord id: %s", err.Error())
			return
		}

		logger.Debug("%s hailed a taxi to %s", username, matchId)
		// Set the user id for the discord user
		hailRegistry.userIdByDiscordId.Store(reaction.UserID, userId)

		// Set the match id for the user
		hailRegistry.matchIdByUserId.Store(userId, strings.ToLower(matchId))
	}

}

func handleMessageReactionRemove(_ context.Context, s *discordgo.Session, reaction *discordgo.MessageReactionRemove, logger runtime.Logger, hailRegistry *echoTaxiHailRegistry) {
	if reaction.GuildID == "" || reaction.UserID == s.State.User.ID {
		// ignore dm reactions and reactions from the bot
		return
	}
	// If the reaction is a taxi, remove the hail for the user
	if reaction.Emoji.Name == "🚕" {
		userId, found := hailRegistry.userIdByDiscordId.Load(reaction.UserID)
		if !found || userId == "" {
			return
		}

		if userId, found := hailRegistry.userIdByDiscordId.Load(userId); found {
			hailRegistry.matchIdByUserId.Delete(userId)
		}
	}
}

// handleMessageCreate_EchoTaxi_LinkReply replies to spark link messages with an echo.taxi link
func checkForSparkLink(s *discordgo.Session, m *discordgo.MessageCreate, hailRegistry *echoTaxiHailRegistry) {
	message := m.ContentWithMentionsReplaced()
	channel, err := s.Channel(m.ChannelID)
	if err != nil {
		log.Println("Error getting channel:", err)
		return
	}

	// If it already has a prefix, then ignore the message.
	if strings.Contains(m.Content, echoTaxiPrefix) || strings.Contains(message, sprockLinkPrefix) {
		return
	}
	if strings.Contains(m.Content, echoTaxiPrefix) || strings.Contains(message, sprockLinkPrefix) {
		return
	}
	for _, regex := range targetRegexs {
		if regex.MatchString(message) {
			// If the message contains a match id, then replace the match id with the echo.taxi link
			message = regex.ReplaceAllString(m.Content, "<"+echoTaxiPrefix+"$1>")
			if message != m.ContentWithMentionsReplaced() {
				// If the message was changed, then send the new message

				// Check if this channel has already been replied to
				reply, found := hailRegistry.linkReplyChannels.LoadAndDelete(channel.ID)
				if found && !reply.(bool) {
					// Just return. If sprocklink replies, then it will save the state again
					return
				} else if !found {
					// If this is a new channel, then sleep for 1 second to give time for sprocklink to reply
					time.Sleep(time.Second)
					// If sprocklink replied in the same time, then this will have loaded as true
					reply, loaded := hailRegistry.linkReplyChannels.LoadOrStore(channel.ID, true)
					if loaded && !reply.(bool) {
						// Sprocklink replied, so don't send the message
						return
					}
					// If sprocklink didn't reply, then send the message
					_, err := s.ChannelMessageSend(channel.ID, message)
					if err != nil {
						log.Println("Error sending message:", err)
					}
				}
			}
		}
	}
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
						Name: "🚕",
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
