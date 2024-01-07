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
	}

	absDays := int(time.Since(time.Date(2023, 11, 01, 00, 00, 00, 0, time.FixedZone("-07:00", -7*60*60))).Hours() / 24)

	activityName := fmt.Sprintf("EchoVR for %d days", absDays)

	_ = activityName
	bot, err := discordgo.New("Bot " + botToken)
	if err != nil {
		return err
	}
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
		handleMessageReactionAdd(ctx, s, m, nk, hailRegistry)
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.MessageReactionRemove) {
		handleMessageReactionRemove(ctx, s, m, hailRegistry)
	})

	bot.AddHandler(handleMessageCreate_EchoTaxi_React)
	//bot.AddHandler(handleMessageCreate_EchoTaxi_LinkReply)

	err = bot.Open()
	if err != nil {
		return err
	}

	if err := initializer.RegisterBeforeRt("MatchJoin", func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *rtapi.Envelope) (*rtapi.Envelope, error) {
		// Get the user's information from the context
		username := ctx.Value(runtime.RUNTIME_CTX_USERNAME).(string)
		userId := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)

		// Do not lookup hails for broadcasters.
		if strings.Contains(username, "broadcaster:") {
			return in, nil
		}

		// Check if the user has a matchId in the hailRegistry
		if matchId, found := hailRegistry.matchIdByUserId.LoadAndDelete(userId); found {
			// Set the matchId in the envelope
			in.Message.(*rtapi.Envelope_MatchJoin).MatchJoin.Id = &rtapi.MatchJoin_MatchId{MatchId: matchId.(string)}
		}

		return in, nil
	}); err != nil {
		return err
	}
	logger.Info("Initialized EchoTaxi runtime module.")
	return nil
}

// handleMessageCreate_EchoTaxi_React adds a taxi emoji to spark link messages
func handleMessageCreate_EchoTaxi_React(s *discordgo.Session, m *discordgo.MessageCreate) {
	for _, regex := range reactionRegexs {
		if regex.MatchString(m.Content) {
			err := s.MessageReactionAdd(m.ChannelID, m.ID, "🚕")
			if err != nil {
				log.Println("Error adding reaction:", err)
			}
		}
	}
}

// handleMessageReactionAdd handles the reaction add event
// It checks if the reaction is a taxi, and if so, it arms the taxi redirect
func handleMessageReactionAdd(ctx context.Context, s *discordgo.Session, reaction *discordgo.MessageReactionAdd, nk runtime.NakamaModule, hailRegistry *echoTaxiHailRegistry) {
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
	if strings.Contains(message.Content, echoTaxiPrefix) {

		// Get the nakama user id from the discord user id
		userId, _, _, err := nk.AuthenticateCustom(ctx, reaction.UserID, "", true)
		if err != nil {
			log.Println("Error getting user id:", err)
			return
		}

		// Extract the matchId from the message
		matchId := matchIdRegex.FindString(message.Content)
		if matchId == "" {
			return
		}
		// Set the user id for the discord user
		hailRegistry.userIdByDiscordId.Store(reaction.UserID, userId)
		// Set the match id for the user
		hailRegistry.matchIdByUserId.Store(userId, strings.ToLower(matchId))
	}

}

func handleMessageReactionRemove(_ context.Context, s *discordgo.Session, reaction *discordgo.MessageReactionRemove, hailRegistry *echoTaxiHailRegistry) {
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
func handleMessageCreate_EchoTaxi_LinkReply(s *discordgo.Session, m *discordgo.MessageCreate) {
	if m.Author.Bot {
		return
	}

	for _, regex := range targetRegexs {
		if regex.MatchString(m.Content) {
			channel, err := s.Channel(m.ChannelID)
			if err != nil {
				log.Println("Error getting channel:", err)
				return
			}
			matchId := matchIdRegex.FindString(m.Content)
			// If it has the prefix, then try to cancel any pending message for this link
			if strings.Contains(m.Content, echoTaxiPrefix) {
				if cancel, ok := taxiDeferredLinks.Load(matchId); ok {
					cancel.(context.CancelFunc)()
				}
				// Ignore the message and add a delay to future messages
				deferredChannels.Store(channel.ID, 2000)
				return
			}

			message := regex.ReplaceAllString(m.Content, "<"+echoTaxiPrefix+"$1>")

			delay := time.Duration(0)
			// If there's no delay... then send the message
			value, ok := deferredChannels.Load(channel.ID)
			if ok {
				delay = value.(time.Duration)
			}

			// There is a delay. Send the message after the delay.
			go func() {
				// Create a new context and cancel function
				ctx, cancel := context.WithCancel(context.Background())
				taxiDeferredLinks.Store(matchId, cancel)
				defer cancel()

				// Wait the delay, then send the message
				select {
				case <-ctx.Done():
					// The context was cancelled. Some other bot answered this message.
					// Put the channel in the deferredChannels map
					deferredChannels.Store(channel.ID, delay)
					return
				case <-time.After(delay * time.Millisecond):
					// No other bot answered the message, so send it.
					// Remove the channel from the deferredChannels map
					deferredChannels.Delete(channel.ID)
					// Remove the match from the taxiDeferredLinks map
					taxiDeferredLinks.Delete(matchId)
					_, err := s.ChannelMessageSend(channel.ID, message)
					if err != nil {
						log.Println("Error sending message:", err)
					}
				}
			}()

		}
	}
}
