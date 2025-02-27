package server

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"regexp"
	goruntime "runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	anyascii "github.com/anyascii/go"
	"github.com/bwmarrin/discordgo"
	"github.com/echotools/vrmlgo/v3"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/thriftrw/ptr"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	CustomizationStorageCollection = "Customization"
	SavedOutfitsStorageKey         = "outfits"
)

type DiscordAppBot struct {
	sync.Mutex

	ctx      context.Context
	cancelFn context.CancelFunc
	logger   runtime.Logger

	config          Config
	metrics         Metrics
	pipeline        *Pipeline
	profileRegistry *ProfileCache
	statusRegistry  StatusRegistry
	nk              runtime.NakamaModule
	db              *sql.DB
	dg              *discordgo.Session

	cache       *DiscordIntegrator
	ipqsCache   *IPQSClient
	choiceCache *MapOf[string, []*discordgo.ApplicationCommandOptionChoice]

	debugChannels  map[string]string // map[groupID]channelID
	userID         string            // Nakama UserID of the bot
	partyStatusChs *MapOf[string, chan error]

	prepareMatchRatePerMinute rate.Limit
	prepareMatchBurst         int
	prepareMatchRateLimiters  *MapOf[string, *rate.Limiter]
}

func NewDiscordAppBot(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB, metrics Metrics, pipeline *Pipeline, config Config, discordCache *DiscordIntegrator, profileRegistry *ProfileCache, statusRegistry StatusRegistry, dg *discordgo.Session, ipqsCache *IPQSClient) (*DiscordAppBot, error) {

	logger = logger.WithField("system", "discordAppBot")

	appbot := DiscordAppBot{
		ctx: ctx,

		logger:   logger,
		nk:       nk,
		db:       db,
		pipeline: pipeline,
		metrics:  metrics,
		config:   config,

		profileRegistry: profileRegistry,
		statusRegistry:  statusRegistry,

		cache:       discordCache,
		ipqsCache:   ipqsCache,
		choiceCache: &MapOf[string, []*discordgo.ApplicationCommandOptionChoice]{},

		dg: dg,

		prepareMatchRatePerMinute: 1,
		prepareMatchBurst:         1,
		prepareMatchRateLimiters:  &MapOf[string, *rate.Limiter]{},
		partyStatusChs:            &MapOf[string, chan error]{},
		debugChannels:             make(map[string]string),
	}

	discordgo.Logger = appbot.discordGoLogger

	bot := dg

	//bot.LogLevel = discordgo.LogDebug
	dg.StateEnabled = true

	bot.Identify.Intents |= discordgo.IntentAutoModerationExecution
	bot.Identify.Intents |= discordgo.IntentGuilds
	bot.Identify.Intents |= discordgo.IntentGuildMembers
	bot.Identify.Intents |= discordgo.IntentGuildBans
	bot.Identify.Intents |= discordgo.IntentGuildEmojis
	bot.Identify.Intents |= discordgo.IntentGuildWebhooks
	bot.Identify.Intents |= discordgo.IntentGuildInvites
	//bot.Identify.Intents |= discordgo.IntentGuildPresences
	bot.Identify.Intents |= discordgo.IntentGuildMessages
	bot.Identify.Intents |= discordgo.IntentMessageContent
	//bot.Identify.Intents |= discordgo.IntentGuildMessageReactions
	bot.Identify.Intents |= discordgo.IntentDirectMessages
	//bot.Identify.Intents |= discordgo.IntentDirectMessageReactions
	bot.Identify.Intents |= discordgo.IntentAutoModerationConfiguration
	bot.Identify.Intents |= discordgo.IntentAutoModerationExecution

	bot.AddHandlerOnce(func(s *discordgo.Session, m *discordgo.Ready) {

		// Create a user for the bot based on it's discord profile
		userID, _, _, err := nk.AuthenticateCustom(ctx, m.User.ID, s.State.User.Username, true)
		if err != nil {
			logger.Error("Error creating discordbot user: %s", err)
		}
		appbot.userID = userID
		// Synchronize the guilds with nakama groups

		displayName := bot.State.User.GlobalName
		if displayName == "" {
			displayName = bot.State.User.Username
		}

		if err := appbot.RegisterSlashCommands(); err != nil {
			logger.Error("Failed to register slash commands: %w", err)
		}

		logger.Info("Bot `%s` ready in %d guilds", displayName, len(bot.State.Guilds))
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.RateLimit) {
		logger.WithField("rate_limit", m).Warn("Discord rate limit")
	})

	// Update the status with the number of matches and players
	go func() {
		updateTicker := time.NewTicker(1 * time.Minute)
		defer updateTicker.Stop()
		for {
			select {
			case <-updateTicker.C:
				if !bot.DataReady {
					continue
				}

				// Get all the matches
				minSize := 2
				maxSize := MatchLobbyMaxSize + 1
				matches, err := nk.MatchList(ctx, 1000, true, "", &minSize, &maxSize, "*")
				if err != nil {
					logger.WithField("err", err).Warn("Error fetching matches.")
					continue
				}
				playerCount := 0
				matchCount := 0
				for _, match := range matches {
					playerCount += int(match.Size) - 1
					matchCount++

				}
				status := fmt.Sprintf("with %d players in %d matches", playerCount, matchCount)
				if err := bot.UpdateGameStatus(0, status); err != nil {
					logger.WithField("err", err).Warn("Failed to update status")
					continue
				}

			case <-ctx.Done():
				updateTicker.Stop()
				return
			}
		}
	}()

	return &appbot, nil
}

func (e *DiscordAppBot) discordGoLogger(msgL int, caller int, format string, a ...interface{}) {

	pc, file, line, _ := goruntime.Caller(caller)

	files := strings.Split(file, "/")
	file = files[len(files)-1]

	name := goruntime.FuncForPC(pc).Name()
	fns := strings.Split(name, ".")
	name = fns[len(fns)-1]

	logger := e.logger.WithFields(map[string]interface{}{
		"file": file,
		"line": line,
		"func": name,
	})

	switch msgL {
	case discordgo.LogError:
		logger.Error(format, a...)
	case discordgo.LogWarning:
		logger.Warn(format, a...)
	case discordgo.LogInformational:
		logger.Info(format, a...)
	case discordgo.LogDebug:
		logger.Debug(format, a...)
	default:
		logger.Info(format, a...)
	}
}

func (e *DiscordAppBot) loadPrepareMatchRateLimiter(userID, groupID string) *rate.Limiter {
	key := strings.Join([]string{userID, groupID}, ":")
	limiter, _ := e.prepareMatchRateLimiters.LoadOrStore(key, rate.NewLimiter(e.prepareMatchRatePerMinute, e.prepareMatchBurst))
	return limiter
}

var (
	partyGroupIDPattern = regexp.MustCompile("^[a-z0-9]+$")
	vrmlIDPattern       = regexp.MustCompile("^[-a-zA-Z0-9]{24}$")

	mainSlashCommands = []*discordgo.ApplicationCommand{

		{
			Name:        "link-headset",
			Description: "Link your headset device to your discord account.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "link-code",
					Description: "Your four character link code.",
					Required:    true,
				},
			},
		},
		{
			Name:        "unlink-headset",
			Description: "Unlink a headset from your discord account.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "device-link",
					Description:  "device link from /whoami",
					Required:     false,
					Autocomplete: true,
				},
			},
		},
		{
			Name:        "check-server",
			Description: "Check if a game server is actively responding on a port.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "address",
					Description: "host:port of the game server",
					Required:    true,
				},
			},
		},
		{
			Name:        "reset-password",
			Description: "Clear your echo password.",
		},
		{
			Name:        "whoami",
			Description: "Receive your account information (privately).",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "include-detail",
					Description: "Include extra details",
					Required:    false,
				},
			},
		},
		{
			Name:        "next-match",
			Description: "Set your next match.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "match-id",
					Description: "Match ID or Spark link",
					Required:    true,
				},
			},
		},
		{
			Name:        "throw-settings",
			Description: "See your throw settings.",
		},
		{
			Name:        "vrml-verify",
			Description: "Link your VRML account.",
		},
		{
			Name:        "set-lobby",
			Description: "Set your default lobby to this Discord server/guild.",
		},
		{
			Name:        "verify",
			Description: "Verify new login locations.",
		},
		{
			Name:        "lookup",
			Description: "Lookup information about players.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "User to lookup",
					Required:    true,
				},
			},
		},
		{
			Name:        "search",
			Description: "Search for a player by display name.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "pattern",
					Description: "Partial name to use in search pattern",
					Required:    true,
				},
			},
		},
		{
			Name:        "hash",
			Description: "Convert a string into a symbol hash.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "token",
					Description: "string to convert",
					Required:    true,
				},
			},
		},
		/*
			{
				Name:        "kick",
				Description: "Force user to go through community values in the social lobby.",
				Options:     []*discordgo.ApplicationCommandOption{},
			},
		*/
		{
			Name:        "trigger-cv",
			Description: "Force user to go through community values in the social lobby.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionUser,
					Name:         "user",
					Description:  "Target user",
					Required:     true,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reason",
					Description: "Reason for the CV",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "timeout_mins",
					Description: "Timeout in minutes (<= 15)",
					Required:    false,
				},
			},
		},
		{
			Name:        "join-player",
			Description: "Join a player's session as a moderator.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Target user",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reason",
					Description: "Reason for joining the player's session.",
					Required:    true,
				},
			},
		},
		{
			Name:        "igp",
			Description: "Use the mod panel.",
		},
		{
			Name:        "party-status",
			Description: "Use the mod panel.",
		},
		{
			Name:        "kick-player",
			Description: "Kick a player's sessions.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:         discordgo.ApplicationCommandOptionUser,
					Name:         "user",
					Description:  "Target user",
					Required:     true,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reason",
					Description: "Reason for the kick",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "timeout",
					Description: "Timeout in minutes",
					Required:    false,
				},
			},
		},
		{
			Name:        "jersey-number",
			Description: "Set your in-game jersey number.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "number",
					Description: "Your jersey number, that will be displayed when you select loadout number as your decal.",
					Required:    true,
				},
			},
		},
		{
			Name:        "badges",
			Description: "manage badge entitlements",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "assign",
					Description: "assign badges to a player",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionUser,
							Name:        "user",
							Description: "target user",
							Required:    true,
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "badges",
							Description: "comma seperated list of badges (i.e p,1,2,5c,6f)",
							Required:    true,
						},
					},
				},
			},
		},
		{
			Name:        "stream-list",
			Description: "list presences for a stream",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "mode",
					Description: "the stream mode",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{
							Name:  "Party",
							Value: StreamModeParty,
						},
						{
							Name:  "Match",
							Value: StreamModeMatchAuthoritative,
						},
						{
							Name:  "GameServer",
							Value: StreamModeGameServer,
						},
						{
							Name:  "Service",
							Value: StreamModeService,
						},
						{
							Name:  "Entrant",
							Value: StreamModeEntrant,
						},
						{
							Name:  "Matchmaking",
							Value: StreamModeMatchmaking,
						},
						{
							Name:  "Channel",
							Value: StreamModeChannel,
						},
						{
							Name:  "Group",
							Value: StreamModeGroup,
						},
						{
							Name:  "DM",
							Value: StreamModeDM,
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "subject",
					Description: "stream subject",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "subcontext",
					Description: "stream subcontext",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "label",
					Description: "stream label",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "limit",
					Description: "limit the number of results",
					Required:    false,
				},
			},
		},
		{
			Name:        "set-roles",
			Description: "link roles to Echo VR features. Non-members can only join private matches.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionRole,
					Name:        "member",
					Description: "If defined, this role allows joining social lobbies, matchmaking, or creating private matches.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionRole,
					Name:        "moderator",
					Description: "Allowed access to more detailed `/lookup`information and moderation tools.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionRole,
					Name:        "serverhost",
					Description: "Allowed to host an Echo VR Game Server for the guild.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionRole,
					Name:        "suspension",
					Description: "Disallowed from joining any guild matches.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionRole,
					Name:        "allocator",
					Description: "Allowed to reserve game servers.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionRole,
					Name:        "is-linked",
					Description: "Assigned/Removed by Nakama denoting if an account is linked to a headset.",
					Required:    true,
				},
			},
		},
		{
			Name:        "allocate",
			Description: "Allocate a session on a game server in a specific region",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "mode",
					Description: "Game mode",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{
							Name:  "Echo Arena Private",
							Value: "echo_arena_private",
						},
						{
							Name:  "Echo Arena Public",
							Value: "echo_arena",
						},
						{
							Name:  "Echo Combat Public",
							Value: "echo_combat",
						},
						{
							Name:  "Echo Combat Private",
							Value: "echo_combat_private",
						},
						{
							Name:  "Social Private",
							Value: "social_2.0_private",
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "region",
					Description: "Region to allocate the session in",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "level",
					Description: "Level for the the session",
					Required:    false,
					Choices: func() []*discordgo.ApplicationCommandOptionChoice {
						choices := make([]*discordgo.ApplicationCommandOptionChoice, 0)
						for _, levels := range evr.LevelsByMode {
							for _, level := range levels {
								choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
									Name:  level.Token().String(),
									Value: level,
								})
							}
						}
						return choices
					}(),
				},
			},
		},
		{
			Name:        "create",
			Description: "Create an EVR game session on a game server in a specific region",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "mode",
					Description: "Game mode",
					Required:    true,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{
							Name:  "Private Arena Match",
							Value: "echo_arena_private",
						},
						{
							Name:  "Private Combat Match",
							Value: "echo_combat_private",
						},
						{
							Name:  "Private Social Lobby",
							Value: "social_2.0_private",
						},
						{
							Name:  "Public Arena Match",
							Value: "echo_arena",
						},
						{
							Name:  "Public Combat Match",
							Value: "echo_combat",
						},
					},
				},
				{
					Type:         discordgo.ApplicationCommandOptionString,
					Name:         "region",
					Description:  "Region to allocate the session in (leave blank to use the best server for you)",
					Required:     false,
					Autocomplete: true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "level",
					Description: "Level for the the session (combat only)",
					Required:    false,
					Choices: func() []*discordgo.ApplicationCommandOptionChoice {
						choices := make([]*discordgo.ApplicationCommandOptionChoice, 0)
						for _, level := range evr.LevelsByMode[evr.ModeCombatPublic] {
							choices = append(choices, &discordgo.ApplicationCommandOptionChoice{
								Name:  level.Token().String(),
								Value: level,
							})
						}
						return choices
					}(),
				},
			},
		},
		{
			Name:        "region-status",
			Description: "Get the status of game servers in a specific region",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "region",
					Description: "Region to check the status of",
					Required:    true,
				},
			},
		},
		{
			Name:        "party",
			Description: "Manage EchoVR parties.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "group",
					Description: "Set your matchmaking group name.",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "group-name",
							Description: "Your matchmaking group name.",
							Required:    true,
						},
					},
				},
				{
					Name:        "members",
					Description: "See members of your party.",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
				},
				/*
					{
						Name:        "invite",
						Description: "Invite a user to your party.",
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Options: []*discordgo.ApplicationCommandOption{
							{
								Type:        discordgo.ApplicationCommandOptionUser,
								Name:        "user-option",
								Description: "User to invite to your party.",
								Required:    false,
							},
						},
					},

					{
						Name:        "invite",
						Description: "Invite a user to your party.",
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Options: []*discordgo.ApplicationCommandOption{
							{
								Type:        discordgo.ApplicationCommandOptionUser,
								Name:        "user-option",
								Description: "User to invite to your party.",
								Required:    false,
							},
						},
					},
					{
						Name:        "cancel",
						Description: "cancel a party invite (or leave blank to cancel all).",
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Options: []*discordgo.ApplicationCommandOption{
							{
								Type:        discordgo.ApplicationCommandOptionUser,
								Name:        "user-option",
								Description: "User to cancel invite for.",
								Required:    false,
							},
						},
					},
					{
						Name:        "transfer",
						Description: "Make another user the party leader.",
						Type:        discordgo.ApplicationCommandOptionSubCommand,
						Options: []*discordgo.ApplicationCommandOption{
							{
								Type:        discordgo.ApplicationCommandOptionUser,
								Name:        "user-option",
								Description: "User to transfer party to.",
								Required:    true,
							},
						},
					},
					{
						Name:        "help",
						Description: "Help with party commands.",
						Type:        discordgo.ApplicationCommandOptionSubCommand,
					},
					{
						Name:        "status",
						Description: "Status of your party.",
						Type:        discordgo.ApplicationCommandOptionSubCommand,
					},
					{
						Name:        "warp",
						Description: "Warp your party to your lobby.",
						Type:        discordgo.ApplicationCommandOptionSubCommand,
					},
					{
						Name:        "leave",
						Description: "Leave the party.",
						Type:        discordgo.ApplicationCommandOptionSubCommand,
					},
				*/
			},
		},

		/*
			{
				Name:        "responses",
				Description: "Interaction responses testing initiative",
				Options: []*discordgo.ApplicationCommandOption{
					{
						Name:        "resp-type",
						Description: "Response type",
						Type:        discordgo.ApplicationCommandOptionInteger,
						Choices: []*discordgo.ApplicationCommandOptionChoice{
							{
								Name:  "Channel message with source",
								Value: 4,
							},
							{
								Name:  "Deferred response With Source",
								Value: 5,
							},
						},
						Required: true,
					},
				},
			},
		*/

		{
			Name:        "outfits",
			Description: "Manage user-defined cosmetic loadouts.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "manage",
					Description: "Manage user-defined cosmetic loadouts.",
					Options: []*discordgo.ApplicationCommandOption{
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "action",
							Description: "Action to perform on the specified loadout.",
							Required:    true,
							Choices: []*discordgo.ApplicationCommandOptionChoice{
								{
									Name:  "Save Loadout",
									Value: "save",
								},
								{
									Name:  "Apply Loadout",
									Value: "load",
								},
								{
									Name:  "Delete Loadout",
									Value: "delete",
								},
							},
						},
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "name",
							Description: "Name of the loadout.",
							Required:    true,
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Name:        "list",
					Description: "List all of user's cosmetic loadouts.",
				},
			},
		},
	}
)

// InitializeDiscordBot initializes the discord bot and synchronizes the guilds with nakama groups. It also registers the bot's handlers.
func (d *DiscordAppBot) InitializeDiscordBot() error {

	bot := d.dg
	if bot == nil {
		return nil
	}

	return nil
}

func (d *DiscordAppBot) UnregisterCommandsAll(ctx context.Context, logger runtime.Logger, dg *discordgo.Session) {
	guilds, err := dg.UserGuilds(100, "", "", false)
	if err != nil {
		logger.Error("Error fetching guilds,", zap.Error(err))
		return
	}
	for _, guild := range guilds {
		d.UnregisterCommands(ctx, logger, dg, guild.ID)
	}

}

// If guildID is empty, it will unregister all global commands.
func (d *DiscordAppBot) UnregisterCommands(ctx context.Context, logger runtime.Logger, dg *discordgo.Session, guildID string) {
	commands, err := dg.ApplicationCommands(dg.State.User.ID, guildID)
	if err != nil {
		logger.Error("Error fetching commands,", zap.Error(err))
		return
	}

	for _, command := range commands {
		err := dg.ApplicationCommandDelete(dg.State.User.ID, guildID, command.ID)
		if err != nil {
			logger.Error("Error deleting command,", zap.Error(err))
		} else {
			logger.Info("Deleted command", zap.String("command", command.Name))
		}
	}
}

type DiscordCommandHandlerFn func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error

func (d *DiscordAppBot) RegisterSlashCommands() error {
	ctx := d.ctx
	nk := d.nk
	db := d.db
	dg := d.dg

	commandHandlers := map[string]DiscordCommandHandlerFn{

		"hash": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options
			if len(options) == 0 {
				return errors.New("no options provided")
			}
			token := options[0].StringValue()
			symbol := evr.ToSymbol(token)
			bytes := binary.LittleEndian.AppendUint64([]byte{}, uint64(symbol))

			return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags: discordgo.MessageFlagsEphemeral,
					Embeds: []*discordgo.MessageEmbed{
						{
							Title: token,
							Color: 0xCCCCCC,
							Fields: []*discordgo.MessageEmbedField{
								{
									Name:   "uint64",
									Value:  strconv.FormatUint(uint64(symbol), 10),
									Inline: false,
								},
								{
									Name:   "int64",
									Value:  strconv.FormatInt(int64(symbol), 10),
									Inline: false,
								},
								{
									Name:   "hex",
									Value:  symbol.HexString(),
									Inline: false,
								},
								{
									Name:   "cached?",
									Value:  strconv.FormatBool(lo.Contains(lo.Keys(evr.SymbolCache), symbol)),
									Inline: false,
								},
								{
									Name:   "LE bytes",
									Value:  fmt.Sprintf("%#v", bytes),
									Inline: false,
								},
							},
						},
					},
				},
			})
		},

		"link-headset": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options
			if len(options) == 0 {
				return errors.New("no options provided")
			}
			linkCode := options[0].StringValue()

			if user == nil {
				return nil
			}

			// Validate the link code as a 4 character string
			if len(linkCode) != 4 {
				return errors.New("invalid link code: link code must be (4) letters long (i.e. ABCD)")
			}

			if err := func() error {

				// Exchange the link code for a device auth.
				ticket, err := ExchangeLinkCode(ctx, nk, logger, linkCode)
				if err != nil {
					return fmt.Errorf("failed to exchange link code: %w", err)
				}

				tags := map[string]string{
					"group_id":     groupID,
					"headset_type": normalizeHeadsetType(ticket.LoginProfile.SystemInfo.HeadsetType),
					"is_pcvr":      fmt.Sprintf("%t", ticket.LoginProfile.BuildNumber != evr.StandaloneBuildNumber),
					"new_account":  "false",
				}

				// Authenticate/create an account.
				if userID == "" {
					tags["new_account"] = "true"
					userID, _, _, err = d.nk.AuthenticateCustom(ctx, user.ID, user.Username, true)
					if err != nil {
						return fmt.Errorf("failed to authenticate (or create) user %s: %w", user.ID, err)
					}
				}

				if err := d.nk.GroupUsersAdd(ctx, SystemUserID, groupID, []string{userID}); err != nil {
					return fmt.Errorf("error joining group: %w", err)
				}

				if err := nk.LinkDevice(ctx, userID, ticket.XPID.Token()); err != nil {
					return fmt.Errorf("failed to link headset: %w", err)
				}
				d.metrics.CustomCounter("link_headset", tags, 1)
				// Set the client IP as authorized in the LoginHistory
				history, err := LoginHistoryLoad(ctx, nk, userID)
				if err != nil {
					return fmt.Errorf("failed to load login history: %w", err)
				}

				history.AuthorizeIP(ticket.ClientIP)

				if err := LoginHistoryStore(ctx, nk, userID, history); err != nil {
					return fmt.Errorf("failed to save login history: %w", err)
				}
				return nil
			}(); err != nil {
				logger.WithFields(map[string]interface{}{
					"discord_id": user.ID,
					"link_code":  linkCode,
					"error":      err,
				}).Error("Failed to link headset")
				return err
			}

			content := "Your headset has been linked. Restart EchoVR."

			d.cache.QueueSyncMember(i.GuildID, user.ID)

			// Send the response
			return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral,
					Content: content,
				},
			})
		},
		"party-status": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			// Check if this user is online and currently in a party.
			groupName, partyUUID, err := GetLobbyGroupID(ctx, d.db, userID)
			if err != nil {
				return fmt.Errorf("failed to get party group ID: %w", err)
			}

			if groupName == "" {
				return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: "You do not have a party group set. use `/party group` to set one.",
					},
				})
			}

			go func() {
				// "Close" the embed on return
				defer func() {
					if _, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
						Content: ptr.String("Party Status (Expired)"),
						Embeds: &[]*discordgo.MessageEmbed{{
							Title: "Party has been empty for more than 2 minutes.",
						}},
					}); err != nil {
						logger.Error("Failed to edit interaction response", zap.Error(err))
					}
				}()

				embeds := make([]*discordgo.MessageEmbed, 4)
				// Create all four embeds for the party members.
				for i := range embeds {
					embeds[i] = &discordgo.MessageEmbed{
						Title: "*Empty Slot*",
					}
				}

				partyStr := partyUUID.String()

				var message *discordgo.Message
				var err error
				var members []runtime.Presence
				var lastDiscordIDs []string
				var memberCache = make(map[string]*discordgo.Member)

				updateInterval := 3 * time.Second
				ticker := time.NewTicker(1 * time.Second)
				defer ticker.Stop()

				emptyTimer := time.NewTimer(120 * time.Second)
				defer emptyTimer.Stop()

				// Loop until the party is empty for 2 minutes.
				for {

					select {
					case <-emptyTimer.C:

						// Check if the party is still empty.
						if len(members) > 0 {
							emptyTimer.Reset(120 * time.Second)
							continue
						}
						return

					case <-ticker.C:
						ticker.Reset(updateInterval)
					}

					// List the party members.
					members, err = nk.StreamUserList(StreamModeParty, partyStr, "", d.pipeline.node, false, true)
					if err != nil {
						logger.Error("Failed to list stream users", zap.Error(err))
						return
					}

					emptyState := &LobbySessionParameters{}

					matchmakingStates := make(map[string]*LobbySessionParameters, len(members)) // If they are matchmaking
					for _, p := range members {
						matchmakingStates[p.GetUserId()] = emptyState
					}

					discordIDs := make([]string, 0, len(members))
					for _, p := range members {
						discordIDs = append(discordIDs, d.cache.UserIDToDiscordID(p.GetUserId()))
					}

					slices.Sort(discordIDs)

					groupID := d.cache.GuildIDToGroupID(i.GuildID)

					// List who is matchmaking
					currentlyMatchmaking, err := nk.StreamUserList(StreamModeMatchmaking, groupID, "", "", false, true)
					if err != nil {
						logger.Error("Failed to list stream users", zap.Error(err))
						return
					}

					idleColor := 0x0000FF // no one is matchmaking

					for _, p := range currentlyMatchmaking {
						if _, ok := matchmakingStates[p.GetUserId()]; ok {

							// Unmarshal the user status
							if err := json.Unmarshal([]byte(p.GetStatus()), matchmakingStates[p.GetUserId()]); err != nil {
								logger.Error("Failed to unmarshal user status", zap.Error(err))
								continue
							}

							idleColor = 0xFF0000 // Someone is matchmaking
						}
					}

					// Update the embeds with the current party members.
					for j := range embeds {
						if j < len(discordIDs) {

							// Set the display name
							var member *discordgo.Member
							var ok bool

							if member, ok = memberCache[discordIDs[j]]; !ok {
								if member, err = d.cache.GuildMember(i.GuildID, discordIDs[j]); err != nil {
									logger.Error("Failed to get guild member", zap.Error(err))
									embeds[j].Title = fmt.Sprintf("*Unknown User*")
									continue
								} else if member != nil {
									memberCache[discordIDs[j]] = member
								} else {
									embeds[j].Title = fmt.Sprintf("*Unknown User*")
									continue
								}
							}

							embeds[j].Title = member.DisplayName()
							embeds[j].Thumbnail = &discordgo.MessageEmbedThumbnail{
								URL: member.User.AvatarURL(""),
							}
							userID := d.cache.DiscordIDToUserID(member.User.ID)
							if state, ok := matchmakingStates[userID]; ok && state != emptyState {

								embeds[j].Color = 0x00FF00
								embeds[j].Description = "Matchmaking"
							} else {

								embeds[j].Color = idleColor
								embeds[j].Description = "In Party"
							}
						} else {
							embeds[j].Title = fmt.Sprintf("*Empty Slot*")
							embeds[j].Color = 0xCCCCCC
						}
					}

					if message == nil {

						// Send the initial message
						if err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
							Type: discordgo.InteractionResponseChannelMessageWithSource,
							Data: &discordgo.InteractionResponseData{
								Flags:  discordgo.MessageFlagsEphemeral,
								Embeds: embeds,
							},
						}); err != nil {
							logger.Error("Failed to send interaction response", zap.Error(err))
							return
						}

					} else if slices.Equal(discordIDs, lastDiscordIDs) {
						// No changes, skip the update.
						continue
					}

					lastDiscordIDs = discordIDs

					// Edit the message with the updated party members.
					if message, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
						Content: ptr.String("Party Status"),
						Embeds:  &embeds,
					}); err != nil {
						logger.Error("Failed to edit interaction response", zap.Error(err))
						return
					}

				}
			}()
			return nil

		},
		"igp": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			// find the user in the streams
			presences, err := nk.StreamUserList(StreamModeService, userID, "", StreamLabelMatchService, false, true)
			if err != nil {
				return fmt.Errorf("failed to list igp users: %w", err)
			}

			var presence runtime.Presence
			for _, p := range presences {
				if p.GetUserId() == userID {
					presence = p
					break
				}
			}

			if presence == nil {
				return fmt.Errorf("You are not currently in game.")
			}

			_nk := d.nk.(*RuntimeGoNakamaModule)

			session := _nk.sessionRegistry.Get(uuid.FromStringOrNil(presence.GetSessionId()))
			if session == nil {
				return fmt.Errorf("You are not currently in game.")
			}

			params, ok := LoadParams(session.Context())
			if !ok {
				return fmt.Errorf("failed to load params")
			}

			isGold := params.isGoldNameTag.Load()

			params.isGoldNameTag.Store(!isGold)

			content := "You are now using the gold name tag."
			if !isGold {
				content = "You are no longer using the gold name tag."
			}

			return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral,
					Content: content,
				},
			})

			components, err := d.ModPanelMessageEmbed(ctx, logger, nk, member.User.ID)
			if err != nil {
				if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: "Failed to load mod panel: " + err.Error(),
					},
				}); err != nil {
					logger.Error("Failed to send mod panel error message", zap.Error(err))
					return nil
				}
			}

			response := &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Content: "Select a player to kick",
					Flags:   discordgo.MessageFlagsEphemeral,
					Components: []discordgo.MessageComponent{
						discordgo.ActionsRow{
							Components: components,
						},
					},
				},
			}
			return s.InteractionRespond(i.Interaction, response)
		},
		"unlink-headset": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options
			if len(options) == 0 {

				account, err := nk.AccountGetId(ctx, userID)
				if err != nil {
					logger.Error("Failed to get account", zap.Error(err))
					return err
				}
				if len(account.Devices) == 0 {
					return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Flags:   discordgo.MessageFlagsEphemeral,
							Content: "No headsets are linked to this account.",
						},
					})
				}

				loginHistory, err := LoginHistoryLoad(ctx, nk, userID)
				if err != nil {
					return err
				}

				options := make([]discordgo.SelectMenuOption, 0, len(account.Devices))
				for _, device := range account.Devices {

					description := ""
					if ts, ok := loginHistory.XPIs[device.GetId()]; ok {
						hours := int(time.Since(ts).Hours())
						if hours < 1 {
							minutes := int(time.Since(ts).Minutes())
							if minutes < 1 {
								description = "Just now"
							} else {
								description = fmt.Sprintf("%d minutes ago", minutes)
							}
						} else if hours < 24 {
							description = fmt.Sprintf("%d hours ago", hours)
						} else {
							description = fmt.Sprintf("%d days ago", int(time.Since(ts).Hours()/24))
						}
					}

					options = append(options, discordgo.SelectMenuOption{
						Label: device.GetId(),
						Value: device.GetId(),
						Emoji: &discordgo.ComponentEmoji{
							Name: "🔗",
						},
						Description: description,
					})
				}

				response := &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Content: "Select a device to unlink",
						Flags:   discordgo.MessageFlagsEphemeral,
						Components: []discordgo.MessageComponent{
							discordgo.ActionsRow{
								Components: []discordgo.MessageComponent{
									discordgo.SelectMenu{
										// Select menu, as other components, must have a customID, so we set it to this value.
										CustomID:    "unlink-headset",
										Placeholder: "<select a device to unlink>",
										Options:     options,
									},
								},
							},
						},
					},
				}
				return s.InteractionRespond(i.Interaction, response)
			}
			xpid := options[0].StringValue()
			// Validate the link code as a 4 character string

			if user == nil {
				return nil
			}

			if err := func() error {

				return nk.UnlinkDevice(ctx, userID, xpid)

			}(); err != nil {
				return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: err.Error(),
					},
				})
			}
			d.metrics.CustomCounter("unlink_headset", nil, 1)
			content := "Your headset has been unlinked. Restart EchoVR."
			d.cache.QueueSyncMember(i.GuildID, user.ID)

			return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral,
					Content: content,
				},
			})
		},
		"check-server": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			options := i.ApplicationCommandData().Options
			if len(options) == 0 {
				return errors.New("no options provided")

			}
			target := options[0].StringValue()

			// 1.1.1.1[:6792[-6820]]
			parts := strings.SplitN(target, ":", 2)
			if len(parts) == 0 {
				return errors.New("no address provided")

			}
			if parts[0] == "" {
				return errors.New("invalid address")

			}
			// Parse the address
			remoteIP := net.ParseIP(parts[0])
			if remoteIP == nil {
				// Try resolving the hostname
				ips, err := net.LookupIP(parts[0])
				if err != nil {
					return fmt.Errorf("failed to resolve address: %w", err)
				}
				// Select the ipv4 address
				for _, remoteIP = range ips {
					if remoteIP.To4() != nil {
						break
					}
				}
				if remoteIP == nil {
					return errors.New("failed to resolve address to an ipv4 address")

				}
			}

			// Parse the (optional) port range
			var startPort, endPort int
			var err error
			if len(parts) > 1 {
				// If a port range is specified, scan the specified range
				portRange := strings.SplitN(parts[1], "-", 2)
				if startPort, err = strconv.Atoi(portRange[0]); err != nil {
					return fmt.Errorf("invalid start port: %w", err)
				}
				if len(portRange) == 1 {
					// If a single port is specified, do not scan
					endPort = startPort
				} else {
					// If a port range is specified, scan the specified range
					if endPort, err = strconv.Atoi(portRange[1]); err != nil {
						return fmt.Errorf("invalid end port: %w", err)
					}
				}
			} else {
				// If no port range is specified, scan the default port
				startPort = 6792
				endPort = 6820
			}

			// Do some basic validation
			switch {
			case remoteIP == nil:
				return errors.New("invalid IP address")

			case startPort < 0:
				return errors.New("start port must be greater than or equal to 0")

			case startPort > endPort:
				return errors.New("start port must be less than or equal to end port")

			case endPort-startPort > 100:
				return errors.New("port range must be less than or equal to 100")

			case startPort < 1024:
				return errors.New("start port must be greater than or equal to 1024")

			case endPort > 65535:
				return errors.New("end port must be less than or equal to 65535")

			}
			localIP, err := DetermineLocalIPAddress()
			if startPort == endPort {
				count := 5
				interval := 100 * time.Millisecond
				timeout := 500 * time.Millisecond

				if err != nil {
					return fmt.Errorf("failed to determine local IP address: %w", err)
				}

				// If a single port is specified, do not scan
				rtts, err := BroadcasterRTTcheck(localIP, remoteIP, startPort, count, interval, timeout)
				if err != nil {
					return fmt.Errorf("failed to healthcheck game server: %w", err)
				}
				var sum time.Duration
				// Craft a message that contains the comma-delimited list of the rtts. Use a * for any failed pings (rtt == -1)
				rttStrings := make([]string, len(rtts))
				for i, rtt := range rtts {
					if rtt != -1 {
						sum += rtt
						count++
					}
					if rtt == -1 {
						rttStrings[i] = "*"
					} else {
						rttStrings[i] = fmt.Sprintf("%.0fms", rtt.Seconds()*1000)
					}
				}
				rttMessage := strings.Join(rttStrings, ", ")

				// Calculate the average rtt
				avgrtt := sum / time.Duration(count)

				if avgrtt > 0 {
					return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,

						Data: &discordgo.InteractionResponseData{
							Flags:   discordgo.MessageFlagsEphemeral,
							Content: fmt.Sprintf("game server %s:%d RTTs (AVG: %.0f): %s", remoteIP, startPort, avgrtt.Seconds()*1000, rttMessage),
						},
					})
				} else {
					return errors.New("no response from game server")

				}
			} else {

				// Scan the address for responding game servers and then return the results as a newline-delimited list of ip:port
				responses, _ := BroadcasterPortScan(localIP, remoteIP, 6792, 6820, 500*time.Millisecond)
				if len(responses) == 0 {
					return errors.New("no game servers are responding")
				}

				// Craft a message that contains the newline-delimited list of the responding game servers
				var b strings.Builder
				for port, r := range responses {
					b.WriteString(fmt.Sprintf("%s:%-5d %3.0fms\n", remoteIP, port, r.Seconds()*1000))
				}

				return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: fmt.Sprintf("```%s```", b.String()),
					},
				})

			}
		},
		"reset-password": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			if user == nil {
				return nil
			}

			if err := func() error {
				// Get the account
				account, err := nk.AccountGetId(ctx, userID)
				if err != nil {
					return err
				}
				// Clear the password
				return nk.UnlinkEmail(ctx, userID, account.GetEmail())

			}(); err != nil {
				return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: err.Error(),
					},
				})
			} else {
				// Send the response
				return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: "Your password has been cleared.",
					},
				})
			}
		},

		"jersey-number": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options
			if len(options) == 0 {
				return errors.New("no options provided")
			}
			number := int(options[0].IntValue())
			if number < 0 || number > 99 {
				return errors.New("invalid number. Must be between 0 and 99")
			}
			if userID == "" {
				return errors.New("no user ID")
			}
			// Get the user's profile

			md, err := AccountMetadataLoad(ctx, nk, userID)
			if err != nil {
				return fmt.Errorf("failed to get account metadata: %w", err)
			}

			// Update the jersey number

			md.LoadoutCosmetics.JerseyNumber = int64(number)
			md.LoadoutCosmetics.Loadout.Decal = "loadout_number"
			md.LoadoutCosmetics.Loadout.DecalBody = "loadout_number"

			// Save the profile
			if err := AccountMetadataUpdate(ctx, nk, userID, md); err != nil {
				return fmt.Errorf("failed to update account metadata: %w", err)
			}

			// Send the response
			return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral,
					Content: fmt.Sprintf("Your jersey number has been set to %d", number),
				},
			})
		},

		"badges": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options
			var err error

			if user == nil {
				return nil
			}

			switch options[0].Name {
			case "assign":
				options = options[0].Options
				// Check that the user is a developer

				var isMember bool
				isMember, err = CheckSystemGroupMembership(ctx, db, userID, GroupGlobalBadgeAdmins)
				if err != nil {
					return status.Error(codes.Internal, "failed to check group membership")
				}
				if !isMember {
					return status.Error(codes.PermissionDenied, "you do not have permission to use this command")
				}
				if len(options) < 2 {
					return status.Error(codes.InvalidArgument, "you must specify a user and a badge")
				}
				// Get the target user's discord ID
				target := options[0].UserValue(s)
				if target == nil {
					return status.Error(codes.InvalidArgument, "you must specify a user")
				}
				targetUserID := d.cache.DiscordIDToUserID(target.ID)
				if targetUserID == "" {
					return status.Error(codes.NotFound, "target user not found")
				}

				account, err := nk.AccountGetId(ctx, targetUserID)
				if err != nil {
					return status.Error(codes.Internal, "failed to get account")
				}

				wallet := make(map[string]int64)

				if err := json.Unmarshal([]byte(account.Wallet), &wallet); err != nil {
					return status.Error(codes.Internal, "failed to unmarshal wallet")
				}

				// Get the badge name
				badgeCodestr := options[1].StringValue()
				badgeCodes := strings.Split(strings.ToLower(badgeCodestr), ",")

				entitlements := make([]*VRMLEntitlement, 0, len(badgeCodes))

				for _, c := range badgeCodes {

					c := strings.TrimSpace(c)
					if c == "" {
						continue
					}

					if len(c) > 2 {
						return status.Error(codes.InvalidArgument, "invalid badge code, it must be one or two characters")
					}

					seasonCode, prestigeCode := c[:1], c[1:]

					var seasonMap = map[string]VRMLSeasonID{
						"p": VRMLPreSeason,
						"1": VRMLSeason1,
						"2": VRMLSeason2,
						"3": VRMLSeason3,
						"4": VRMLSeason4,
						"5": VRMLSeason5,
						"6": VRMLSeason6,
						"7": VRMLSeason7,
					}

					seasonID, ok := seasonMap[seasonCode]
					if !ok {
						return status.Error(codes.InvalidArgument, "invalid season code (p, 1-7)")
					}

					switch prestigeCode {
					case "c":
						entitlements = append(entitlements, &VRMLEntitlement{seasonID, VRMLChampion})
					case "f":
						entitlements = append(entitlements, &VRMLEntitlement{seasonID, VRMLFinalist})
					case "":
						entitlements = append(entitlements, &VRMLEntitlement{seasonID, VRMLPlayer})
					default:
						return status.Error(codes.InvalidArgument, "invalid prestige code; it must be 'c' or 'f'")
					}

				}

				// Assign the badge to the user
				if err := AssignEntitlements(ctx, logger, nk, userID, i.Member.User.Username, targetUserID, "", entitlements); err != nil {
					return status.Error(codes.Internal, "failed to assign badge")
				}

				// Send a message to the channel
				channel := "1232462244797874247"
				_, err = s.ChannelMessageSend(channel, fmt.Sprintf("%s assigned VRML cosmetics `%s` to user `%s`", user.Mention(), badgeCodestr, target.Username))
				if err != nil {
					logger.WithFields(map[string]interface{}{
						"error": err,
					}).Error("Failed to send badge channel update message")
				}
				simpleInteractionResponse(s, i, fmt.Sprintf("Assigned VRML cosmetics `%s` to user `%s`", badgeCodestr, target.Username))
			}

			return nil
		},
		"whoami": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			if user == nil {
				return nil
			}
			if member == nil {
				return errors.New("this command must be used from a guild")
			}
			// check for the with-detail boolean option
			d.cache.Purge(user.ID)
			d.cache.QueueSyncMember(i.GuildID, user.ID)

			err := d.handleProfileRequest(ctx, logger, nk, s, i, user.ID, user.Username, true, true, false)
			logger.WithFields(map[string]interface{}{
				"discord_id":       user.ID,
				"discord_username": user.Username,
				"error":            err,
			}).Debug("whoami")
			return err
		},
		"next-match": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			if user == nil {
				return nil
			}
			if len(i.ApplicationCommandData().Options) == 0 {
				return errors.New("no match provided.")
			}

			matchIDStr := i.ApplicationCommandData().Options[0].StringValue()
			matchIDStr = MatchUUIDPattern.FindString(strings.ToLower(matchIDStr))
			if matchIDStr == "" {
				return fmt.Errorf("invalid match ID: %s", matchIDStr)
			}

			matchID, err := MatchIDFromString(matchIDStr + "." + d.pipeline.node)
			if err != nil {
				return fmt.Errorf("failed to parse match ID: %w", err)
			}

			// Make sure the match exists
			if _, err := MatchLabelByID(ctx, nk, matchID); err != nil {
				return fmt.Errorf("failed to get match: %w", err)
			}

			// Set the next match ID
			if err := SetNextMatchID(ctx, nk, userID, matchID, AnyTeam, ""); err != nil {
				return fmt.Errorf("failed to set next match ID: %w", err)
			}

			return simpleInteractionResponse(s, i, fmt.Sprintf("Next match set to `%s`", matchID))
		},
		"throw-settings": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userIDStr string, groupID string) error {
			metadata, err := AccountMetadataLoad(ctx, nk, userIDStr)
			if err != nil {
				return fmt.Errorf("failed to get account metadata: %w", err)
			}

			embed := &discordgo.MessageEmbed{
				Title: "Game Settings",
				Color: 5814783, // A hexadecimal color code
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:   "Grab Deadzone",
						Value:  strconv.FormatFloat(metadata.GamePauseSettings.Grabdeadzone, 'f', -1, 64),
						Inline: true,
					},
					{
						Name:   "Release Distance",
						Value:  strconv.FormatFloat(metadata.GamePauseSettings.Releasedistance, 'f', -1, 64),
						Inline: true,
					},
					{
						Name:   "Wrist Angle Offset",
						Value:  strconv.FormatFloat(metadata.GamePauseSettings.Wristangleoffset, 'f', -1, 64),
						Inline: true,
					},
				},
			}

			if err = s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:  discordgo.MessageFlagsEphemeral,
					Embeds: []*discordgo.MessageEmbed{embed},
				},
			}); err != nil {
				return fmt.Errorf("failed to send game settings message: %w", err)
			}
			return nil
		},

		"set-lobby": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userIDStr string, groupID string) error {
			if member == nil {
				return fmt.Errorf("this command must be used from a guild")
			}

			d.cache.QueueSyncMember(i.GuildID, member.User.ID)

			// Set the metadata
			md, err := AccountMetadataLoad(ctx, nk, userIDStr)
			if err != nil {
				return err
			}
			md.SetActiveGroupID(uuid.FromStringOrNil(groupID))

			if err := AccountMetadataUpdate(ctx, nk, userIDStr, md); err != nil {
				return err
			}

			guild, err := s.Guild(i.GuildID)
			if err != nil {
				logger.Error("Failed to get guild", zap.Error(err))
				return err
			}

			return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags: discordgo.MessageFlagsEphemeral,
					Content: strings.Join([]string{
						fmt.Sprintf("EchoVR lobby changed to **%s**.", guild.Name),
						"- Matchmaking will prioritize members",
						"- Social lobbies will contain only members",
						"- Private matches that you create will prioritize guild's game servers.",
					}, "\n"),
				},
			})
		},
		"verify": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userIDStr string, groupID string) error {
			if member == nil {
				return fmt.Errorf("this command must be used from a guild")
			}
			loginHistory, err := LoginHistoryLoad(ctx, nk, userIDStr)
			if err != nil {
				return fmt.Errorf("failed to load login history: %w", err)
			}
			if len(loginHistory.PendingAuthorizations) == 0 {
				return simpleInteractionResponse(s, i, "No pending IP verifications")
			}

			requests := make([]*LoginHistoryEntry, 0, len(loginHistory.PendingAuthorizations))
			for ip, e := range loginHistory.PendingAuthorizations {
				if time.Since(e.UpdatedAt) > 10*time.Minute {
					delete(loginHistory.PendingAuthorizations, ip)
				}
				requests = append(requests, e)
			}

			if len(requests) == 0 {
				return simpleInteractionResponse(s, i, "No pending IP verifications")
			}

			// Sort by time
			sort.Slice(requests, func(i, j int) bool {
				return requests[i].UpdatedAt.After(requests[j].UpdatedAt)
			})
			// Show the first one.
			request := requests[0]

			ipqs, _ := d.ipqsCache.Get(ctx, request.ClientIP)

			embeds, components := IPVerificationEmbed(request, ipqs)
			// Send it as the interaction response
			if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:      discordgo.MessageFlagsEphemeral,
					Embeds:     embeds,
					Components: components,
				},
			}); err != nil {
				logger.Error("Failed to send IP verification message", zap.Error(err))
				return err
			}

			return nil

		},
		"lookup": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userIDStr string, groupID string) error {

			if user == nil {
				return nil
			}

			options := i.ApplicationCommandData().Options

			if len(options) == 0 {
				return errors.New("no options provided")
			}

			caller := user
			target := options[0].UserValue(s)

			// Clear the cache of the caller and target
			d.cache.Purge(caller.ID)
			d.cache.Purge(target.ID)

			callerUserID := d.cache.DiscordIDToUserID(caller.ID)
			if callerUserID == "" {
				return errors.New("caller not found")
			}

			targetUserID := d.cache.DiscordIDToUserID(target.ID)
			if targetUserID == "" {
				return errors.New("player not found")
			}

			// Get the caller's nakama user ID
			callerGuildGroups := make(map[string]*GuildGroup)
			callerGuildGroups, err := GuildUserGroupsList(ctx, d.nk, callerUserID)
			if err != nil {
				return fmt.Errorf("failed to get guild groups: %w", err)
			}

			isSelf := caller.ID == target.ID

			isGuildModerator := false
			if gg, ok := callerGuildGroups[groupID]; ok {
				isGuildModerator = gg.IsModerator(callerUserID)
			}

			// Get the caller's nakama user ID

			isGlobalModerator, err := CheckSystemGroupMembership(ctx, db, userIDStr, GroupGlobalModerators)
			if err != nil {
				return errors.New("error checking global moderator status")
			}

			d.cache.Purge(target.ID)
			if isGuildModerator {
				d.cache.QueueSyncMember(i.GuildID, target.ID)
			}
			includePrivate := isSelf || isGlobalModerator
			includePriviledged := includePrivate || isGuildModerator
			includeSystem := isGlobalModerator

			return d.handleProfileRequest(ctx, logger, nk, s, i, target.ID, target.Username, includePriviledged, includePrivate, includeSystem)
		},
		"search": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userIDStr string, groupID string) error {

			if user == nil {
				return nil
			}

			options := i.ApplicationCommandData().Options

			if len(options) == 0 {
				return errors.New("no options provided")
			}

			pattern := options[0].StringValue()
			if pattern == "" {
				return errors.New("invalid pattern")
			}

			matches, err := DisplayNameCacheRegexSearch(ctx, nk, pattern, 5)
			if err != nil {
				logger.WithField("error", err).Error("Failed to search display name history")
				return fmt.Errorf("failed to search display name history: %w", err)
			}

			if len(matches) == 0 {
				return simpleInteractionResponse(s, i, "No results found")
			}

			type result struct {
				account *api.Account
				updated time.Time
				matches map[string]time.Time
			}

			results := make([]result, 0, len(matches))

			for userID, byGroup := range matches {

				account, err := nk.AccountGetId(ctx, userID)
				if err != nil {
					logger.WithFields(map[string]interface{}{}).Warn("Failed to get account")
					continue
				}

				result := result{
					account: account,
					matches: make(map[string]time.Time, len(byGroup)),
				}

				for _, names := range byGroup {
					for dn, ts := range names {

						if ts.After(result.matches[dn]) {
							result.matches[dn] = ts

							if ts.After(result.updated) {
								result.updated = ts
							}
						}
					}
				}

				if len(result.matches) == 0 {
					continue
				}

				results = append(results, result)
			}

			// Sort the results by last updated
			sort.Slice(results, func(i, j int) bool {
				return results[i].updated.Before(results[j].updated)
			})

			// Create the embeds
			embeds := make([]*discordgo.MessageEmbed, 0, len(matches))

			for _, r := range results {

				if r.account.User.AvatarUrl != "" && !strings.HasPrefix(r.account.User.AvatarUrl, "https://") {
					r.account.User.AvatarUrl = discordgo.EndpointUserAvatar(r.account.CustomId, r.account.User.AvatarUrl)
				}
				// Discord-ish green
				color := 0x43b581
				footer := ""
				if r.account.DisableTime != nil {
					// Discord-ish red
					color = 0xf04747
					footer = "Account disabled"
				} else if len(r.account.Devices) == 0 {
					// Discord-ish grey
					color = 0x747f8d
					footer = "Account is inactive (no linked devices)"
				}

				embed := &discordgo.MessageEmbed{
					Author: &discordgo.MessageEmbedAuthor{
						IconURL: r.account.User.AvatarUrl,
						Name:    r.account.User.DisplayName,
					},
					Description: fmt.Sprintf("<@%s>", r.account.CustomId),
					Color:       color,
					Fields:      make([]*discordgo.MessageEmbedField, 0, 2),
					Footer: &discordgo.MessageEmbedFooter{
						Text: footer,
					},
				}

				embeds = append(embeds, embed)

				names := &discordgo.MessageEmbedField{
					Name:   "In-Game Name     ",
					Inline: true,
				}

				lastActive := &discordgo.MessageEmbedField{
					Name:   "Last Active",
					Inline: true,
				}

				embed.Fields = append(embed.Fields,
					names,
					lastActive,
				)

				type item struct {
					displayName string
					lastActive  time.Time
				}

				displayNames := make([]item, 0, len(r.matches))
				reserved := make([]string, 0, 1)
				for dn, ts := range r.matches {
					if ts.IsZero() {
						reserved = append(reserved, dn)
					} else {
						displayNames = append(displayNames, item{dn, ts})
					}
				}

				sort.Slice(displayNames, func(i, j int) bool {
					return displayNames[i].lastActive.Before(displayNames[j].lastActive)
				})

				if len(displayNames) > 5 {
					displayNames = displayNames[:5]
				}
				for _, n := range displayNames {
					names.Value += fmt.Sprintf("%s\n", EscapeDiscordMarkdown(n.displayName))
					lastActive.Value += fmt.Sprintf("<t:%d:R>\n", n.lastActive.UTC().Unix())
				}
				for _, n := range reserved {
					names.Value += fmt.Sprintf("%s\n", EscapeDiscordMarkdown(n))
					lastActive.Value += "*reserved*\n"
				}
			}

			if len(embeds) == 0 {
				return simpleInteractionResponse(s, i, "No results found")
			}

			// Send the response
			return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:  discordgo.MessageFlagsEphemeral,
					Embeds: embeds,
				},
			})
		},
		"create": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options

			if member == nil {
				return simpleInteractionResponse(s, i, "this command must be used from a guild")
			}

			if len(options) == 0 {
				return simpleInteractionResponse(s, i, "no options provided")
			}

			mode := evr.ModeArenaPrivate
			region := "default"
			level := evr.LevelUnspecified
			for _, o := range options {
				switch o.Name {
				case "region":
					region = o.StringValue()
				case "mode":
					mode = evr.ToSymbol(o.StringValue())
				case "level":
					level = evr.ToSymbol(o.StringValue())
				}
			}

			if levels, ok := evr.LevelsByMode[mode]; !ok {
				return fmt.Errorf("invalid mode `%s`", mode)
			} else if level != evr.LevelUnspecified && !slices.Contains(levels, level) {
				return fmt.Errorf("invalid level `%s`", level)
			}

			startTime := time.Now().Add(90 * time.Second)

			logger = logger.WithFields(map[string]interface{}{
				"userID":    userID,
				"guildID":   i.GuildID,
				"region":    region,
				"mode":      mode.String(),
				"level":     level.String(),
				"startTime": startTime,
			})

			label, rttMs, err := d.handleCreateMatch(ctx, logger, userID, i.GuildID, region, mode, level, startTime)
			if err != nil {
				return err
			}

			// set the player's next match
			if err := SetNextMatchID(ctx, nk, userID, label.ID, AnyTeam, ""); err != nil {
				logger.Error("Failed to set next match ID", zap.Error(err))
				return fmt.Errorf("failed to set next match ID: %w", err)
			}

			logger.WithFields(map[string]any{
				"match_id": label.ID.String(),
				"rtt_ms":   rttMs,
			}).Info("Match created.")

			content := fmt.Sprintf("Reservation will timeout <t:%d:R>. \n\nClick play or start matchmaking to automatically join your match.", startTime.Unix())

			niceNameMap := map[evr.Symbol]string{
				evr.ModeArenaPrivate:  "Private Arena Match",
				evr.ModeArenaPublic:   "Public Arena Match",
				evr.ModeCombatPrivate: "Private Combat Match",
				evr.ModeCombatPublic:  "Public Combat Match",
				evr.ModeSocialPrivate: "Private Social Lobby",
			}
			prettyName, ok := niceNameMap[mode]
			if !ok {
				prettyName = mode.String()
			}

			serverLocation := "Unknown"

			serverExtIP := label.GameServer.Endpoint.ExternalIP.String()
			if ipqs, err := d.ipqsCache.Get(ctx, serverExtIP); err != nil {
				logger.Error("Failed to get IPQS data", zap.Error(err))
			} else {
				serverLocation = ipqs.Region
			}

			// local the guild name
			guild, err := s.Guild(i.GuildID)
			if err != nil {
				logger.Error("Failed to get guild", zap.Error(err))
			}
			responseContent := &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags: discordgo.MessageFlagsEphemeral,
					Embeds: []*discordgo.MessageEmbed{{
						Title:       "Match Created",
						Description: content,
						Color:       0x9656ce,
						Fields: []*discordgo.MessageEmbedField{
							{
								Name:   "Guild",
								Value:  guild.Name,
								Inline: false,
							},
							{
								Name:   "Mode",
								Value:  prettyName,
								Inline: true,
							},
							{
								Name:   "Region Code",
								Value:  region,
								Inline: false,
							},
							{
								Name:  "Server Location",
								Value: serverLocation,
							},
							{
								Name:   "Ping Latency",
								Value:  fmt.Sprintf("%dms", rttMs),
								Inline: false,
							},
							{
								Name:   "Spark Link",
								Value:  fmt.Sprintf("[Spark Link](https://echo.taxi/spark://c/%s)", strings.ToUpper(label.ID.UUID.String())),
								Inline: false,
							},
							{
								Name:  "Participants",
								Value: "No participants yet",
							},
						},
					}},
				},
			}

			go func() {

				// Monitor the match and update the interaction
				for {
					startCheckTimer := time.NewTicker(15 * time.Second)
					updateTicker := time.NewTicker(30 * time.Second)
					select {
					case <-startCheckTimer.C:
					case <-updateTicker.C:
					}

					presences, err := d.nk.StreamUserList(StreamModeMatchAuthoritative, label.ID.UUID.String(), "", label.ID.Node, false, true)
					if err != nil {
						logger.Error("Failed to get user list", zap.Error(err))
					}
					if len(presences) == 0 {
						// Match is gone. update the response.
						responseContent.Data.Embeds[0].Title = "Match Over"
						responseContent.Data.Embeds[0].Description = "The match expired/ended."

						if _, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
							Embeds: &responseContent.Data.Embeds,
						}); err != nil {
							logger.Error("Failed to update interaction", zap.Error(err))
							return
						}
					}

					// Update the list of players in the interaction response
					var players strings.Builder
					for _, p := range presences {
						if p.GetSessionId() == label.GameServer.SessionID.String() {
							continue
						}
						players.WriteString(fmt.Sprintf("<@%s>\n", d.cache.UserIDToDiscordID(p.GetUserId())))
					}
					responseContent.Data.Embeds[0].Fields[4].Value = players.String()

					if _, err = s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
						Embeds: &responseContent.Data.Embeds,
					}); err != nil {
						logger.Error("Failed to update interaction", zap.Error(err))
						return
					}

				}
			}()

			return s.InteractionRespond(i.Interaction, responseContent)
		},
		"allocate": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options

			if member == nil {
				return simpleInteractionResponse(s, i, "this command must be used from a guild")

			}
			mode := evr.ModeArenaPrivate
			region := "default"
			level := evr.LevelUnspecified
			for _, o := range options {
				switch o.Name {
				case "region":
					region = o.StringValue()
				case "mode":
					mode = evr.ToSymbol(o.StringValue())
				case "level":
					level = evr.ToSymbol(o.StringValue())
				}
			}

			if levels, ok := evr.LevelsByMode[mode]; !ok {
				return fmt.Errorf("invalid mode `%s`", mode)
			} else if level != evr.LevelUnspecified && !slices.Contains(levels, level) {
				return fmt.Errorf("invalid level `%s`", level)
			}

			startTime := time.Now()

			logger = logger.WithFields(map[string]interface{}{
				"userID":    userID,
				"guildID":   i.GuildID,
				"region":    region,
				"mode":      mode.String(),
				"level":     level.String(),
				"startTime": startTime,
			})

			label, _, err := d.handleAllocateMatch(ctx, logger, userID, i.GuildID, region, mode, level, startTime)
			if err != nil {
				return err
			}

			logger.WithField("label", label).Info("Match prepared")
			return simpleInteractionResponse(s, i, fmt.Sprintf("Match prepared with label ```json\n%s\n```\nhttps://echo.taxi/spark://c/%s", label.GetLabelIndented(), strings.ToUpper(label.ID.UUID.String())))
		},
		"trigger-cv": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			if user == nil {
				return nil
			}

			if len(i.ApplicationCommandData().Options) < 2 {
				return errors.New("no options provided")
			}

			var target *discordgo.User
			var targetUserID string
			var timeoutDuration time.Duration
			for _, o := range i.ApplicationCommandData().Options {
				switch o.Name {
				case "user":
					target = o.UserValue(s)
					if target == nil {
						return errors.New("no user provided")
					}
					targetUserID = d.cache.DiscordIDToUserID(target.ID)
					if targetUserID == "" {
						return errors.New("failed to get target user ID")
					}
				case "timeout_mins":
					timeoutDuration = time.Duration(o.IntValue()) * time.Minute
				}
			}

			metadata, err := GetGuildGroupMetadata(ctx, d.db, groupID)
			if err != nil {
				return errors.New("failed to get guild group metadata")
			}

			metadata.CommunityValuesUserIDsAdd(targetUserID, timeoutDuration)

			data, err := metadata.MarshalToMap()
			if err != nil {
				return fmt.Errorf("error marshalling group data: %w", err)
			}

			if err := nk.GroupUpdate(ctx, groupID, SystemUserID, "", "", "", "", "", false, data, 1000000); err != nil {
				return fmt.Errorf("error updating group: %w", err)
			}

			presences, err := d.nk.StreamUserList(StreamModeService, targetUserID, "", StreamLabelMatchService, false, true)
			if err != nil {
				return err
			}

			cnt := 0
			for _, p := range presences {
				disconnectDelay := 0
				if p.GetUserId() == targetUserID {
					cnt += 1
					if label, _ := MatchLabelByID(ctx, d.nk, MatchIDFromStringOrNil(p.GetStatus())); label != nil {

						if label.GameServer.SessionID.String() == p.GetSessionId() {
							continue
						}

						if label.GetGroupID().String() != groupID {
							return errors.New("user's match is not from this guild")
						}

						// Kick the player from the match
						if err := KickPlayerFromMatch(ctx, d.nk, label.ID, targetUserID); err != nil {
							return err
						}
						_, _ = d.LogAuditMessage(ctx, groupID, fmt.Sprintf("(trigger-cv) %s kicked player %s from [%s](https://echo.taxi/spark://c/%s) match.", user.Mention(), target.Mention(), label.Mode.String(), strings.ToUpper(label.ID.UUID.String())), false)
						disconnectDelay = 15
					}

					go func() {
						<-time.After(time.Second * time.Duration(disconnectDelay))
						// Just disconnect the user, wholesale
						if _, err := DisconnectUserID(ctx, d.nk, targetUserID, false); err != nil {
							logger.Warn("Failed to disconnect user", zap.Error(err))
						}
					}()

					cnt++
				}
			}

			return simpleInteractionResponse(s, i, fmt.Sprintf("%s is required to complete *Community Values* when entering the next social lobby, with a timeout of %d minutes. (Disconnected %d sessions)", target.Mention(), int(timeoutDuration.Minutes()), cnt))
		},
		"kick-player": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			if user == nil {
				return nil
			}

			isGlobalModerator, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalModerators)
			if err != nil {
				return errors.New("error checking global moderator status")
			}

			var target *discordgo.User
			var targetUserID, reason string
			var timeout time.Duration
			for _, o := range i.ApplicationCommandData().Options {
				switch o.Name {
				case "user":
					target = o.UserValue(s)
					targetUserID = d.cache.DiscordIDToUserID(target.ID)
					if targetUserID == "" {
						return errors.New("failed to get target user ID")
					}
				case "reason":
					reason = o.StringValue()
				case "timeout":
					timeout = time.Duration(o.IntValue()) * time.Minute
				}
			}

			presences, err := d.nk.StreamUserList(StreamModeService, targetUserID, "", StreamLabelMatchService, false, true)
			if err != nil {
				return err
			}

			cnt := 0

			if timeout > 0 {
				metadata, err := GetGuildGroupMetadata(ctx, d.db, groupID)
				if err != nil {
					return errors.New("failed to get guild group metadata")
				}

				metadata.TimeoutAdd(targetUserID, timeout)

				data, err := metadata.MarshalToMap()
				if err != nil {
					return fmt.Errorf("error marshalling group data: %w", err)
				}

				if err := nk.GroupUpdate(ctx, groupID, SystemUserID, "", "", "", "", "", false, data, 1000000); err != nil {
					return fmt.Errorf("error updating group: %w", err)
				}
			}

			result := ""
			for _, p := range presences {

				if p.GetUserId() != targetUserID {
					continue
				}

				label, _ := MatchLabelByID(ctx, d.nk, MatchIDFromStringOrNil(p.GetStatus()))
				if label == nil {
					continue
				}

				if label.GameServer.SessionID.String() == p.GetSessionId() {
					continue
				}

				if label.GetGroupID().String() != groupID && !isGlobalModerator {
					result = "user's lobby is not from this guild"
					continue
				}

				cnt += 1

				// Kick the player from the match
				if err := KickPlayerFromMatch(ctx, d.nk, label.ID, targetUserID); err != nil {
					result = fmt.Sprintf("failed to kick player from [%s](https://echo.taxi/spark://c/%s) (error: %s)", label.Mode.String(), strings.ToUpper(label.ID.UUID.String()), err.Error())
					continue
				}
				result := fmt.Sprintf("kicked player %s from [%s](https://echo.taxi/spark://c/%s) match. (%s)", target.Mention(), label.Mode.String(), strings.ToUpper(label.ID.UUID.String()), reason)
				_, _ = d.LogAuditMessage(ctx, groupID, fmt.Sprintf("<@%s> %s", user.Mention(), result), false)

				cnt++

			}

			if result != "" {
				return simpleInteractionResponse(s, i, result)
			}

			if cnt == 0 {
				return simpleInteractionResponse(s, i, "No sessions found.")
			}

			go func() {
				<-time.After(time.Second * 15)
				// Just disconnect the user, wholesale
				if _, err := DisconnectUserID(ctx, d.nk, targetUserID, false); err != nil {
					logger.Warn("Failed to disconnect user", zap.Error(err))
				} else {
					_, _ = d.LogAuditMessage(ctx, groupID, fmt.Sprintf("<@%s> disconnected player <@%s> from match service.", user.ID, target.ID), false)
				}
			}()

			return simpleInteractionResponse(s, i, fmt.Sprintf("%s (%d sessions)", result, cnt))

		},
		"join-player": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			if user == nil {
				return nil
			}

			options := i.ApplicationCommandData().Options
			if len(options) == 0 {
				return errors.New("no options provided")
			}

			target := options[0].UserValue(s)
			targetUserID := d.cache.DiscordIDToUserID(target.ID)
			if targetUserID == "" {
				return errors.New("failed to get target user ID")
			}

			presences, err := d.nk.StreamUserList(StreamModeService, targetUserID, "", StreamLabelMatchService, false, true)
			if err != nil {
				return err
			}

			if len(presences) == 0 {
				return simpleInteractionResponse(s, i, "No sessions found.")
			}
			presence := presences[0]
			if label, _ := MatchLabelByID(ctx, d.nk, MatchIDFromStringOrNil(presence.GetStatus())); label != nil {

				if label.GetGroupID().String() != groupID {
					return errors.New("user's match is not from this guild")
				}

				if err := SetNextMatchID(ctx, d.nk, userID, label.ID, Moderator, ""); err != nil {
					return fmt.Errorf("failed to set next match ID: %w", err)
				}

				_, _ = d.LogAuditMessage(ctx, groupID, fmt.Sprintf("<@%s> join player <@%s> at [%s](https://echo.taxi/spark://c/%s) match.", user.ID, target.ID, label.Mode.String(), strings.ToUpper(label.ID.UUID.String())), false)
				content := fmt.Sprintf("Joining %s [%s](https://echo.taxi/spark://c/%s) match next.", target.Mention(), label.Mode.String(), strings.ToUpper(label.ID.UUID.String()))
				return simpleInteractionResponse(s, i, content)
			}
			return simpleInteractionResponse(s, i, "No match found.")
		},
		"set-roles": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options

			// Ensure the user is the owner of the guild
			if user == nil || i.Member == nil || i.Member.User.ID == "" || i.GuildID == "" {
				return nil
			}

			guild, err := s.Guild(i.GuildID)
			if err != nil || guild == nil {
				return errors.New("failed to get guild")
			}

			if guild.OwnerID != user.ID {
				// Check if the user is a global developer
				if ok, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalDevelopers); err != nil {
					return errors.New("failed to check group membership")
				} else if !ok {
					return errors.New("you do not have permission to use this command")
				}
			}

			// Get the metadata
			metadata, err := GetGuildGroupMetadata(ctx, d.db, groupID)
			if err != nil {
				return errors.New("failed to get guild group metadata")

			}
			roles := metadata.Roles
			for _, o := range options {
				roleID := o.RoleValue(s, guild.ID).ID
				switch o.Name {
				case "moderator":
					roles.Moderator = roleID
				case "serverhost":
					roles.ServerHost = roleID
				case "suspension":
					roles.Suspended = roleID
				case "member":
					roles.Member = roleID
				case "allocator":
					roles.Allocator = roleID
				case "is_linked":
					roles.AccountLinked = roleID
				}
			}

			data, err := metadata.MarshalToMap()
			if err != nil {
				return fmt.Errorf("error marshalling group data: %w", err)
			}

			if err := nk.GroupUpdate(ctx, groupID, SystemUserID, "", "", "", "", "", false, data, 1000000); err != nil {
				return fmt.Errorf("error updating group: %w", err)
			}

			return simpleInteractionResponse(s, i, "roles set!")
		},

		"region-status": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options

			if user == nil {
				return nil
			}

			regionStr := options[0].StringValue()
			if regionStr == "" {
				return errors.New("no region provided")
			}

			return d.createRegionStatusEmbed(ctx, logger, regionStr, i.Interaction.ChannelID, nil)
		},
		"stream-list": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options

			if user == nil {
				return nil
			}

			// Limit access to global developers
			if ok, err := CheckSystemGroupMembership(ctx, d.db, userID, GroupGlobalDevelopers); err != nil {
				return errors.New("failed to check group membership")
			} else if !ok {
				return errors.New("you do not have permission to use this command")
			}

			var subject, subcontext, label string
			var mode, limit int64
			for _, o := range options {
				switch o.Name {
				case "mode":
					mode = o.IntValue()
				case "subject":
					subject = o.StringValue()
				case "subcontext":
					subcontext = o.StringValue()
				case "label":
					label = o.StringValue()
				case "limit":
					limit = o.IntValue()
				}
			}

			includeHidden := true
			includeOffline := true

			presences, err := nk.StreamUserList(uint8(mode), subject, subcontext, label, includeHidden, includeOffline)
			if err != nil {
				return errors.New("failed to list stream users")

			}
			if len(presences) == 0 {
				return errors.New("no stream users found")
			}
			channel, err := s.UserChannelCreate(user.ID)
			if err != nil {
				return errors.New("failed to create user channel")
			}
			if err := simpleInteractionResponse(s, i, "Sending stream list to your DMs"); err != nil {
				return errors.New("failed to send interaction response")
			}
			if limit == 0 {
				limit = 10
			}

			var b strings.Builder
			if len(presences) > int(limit) {
				presences = presences[:limit]
			}

			type presenceMessage struct {
				UserID    string
				Username  string
				SessionID string
				Status    any
			}

			messages := make([]string, 0)
			for _, presence := range presences {
				m := presenceMessage{
					UserID:    presence.GetUserId(),
					Username:  presence.GetUsername(),
					SessionID: presence.GetSessionId(),
				}
				// Try to unmarshal the status
				status := make(map[string]any, 0)
				if err := json.Unmarshal([]byte(presence.GetStatus()), &status); err != nil {
					m.Status = presence.GetStatus()
				}
				m.Status = status

				data, err := json.MarshalIndent(m, "", "  ")
				if err != nil {
					return errors.New("failed to marshal presence data")
				}
				if b.Len()+len(data) > 1900 {
					messages = append(messages, b.String())
					b.Reset()
				}
				_, err = b.WriteString(fmt.Sprintf("```json\n%s\n```\n", data))
				if err != nil {
					return errors.New("failed to write presence data")
				}
			}
			messages = append(messages, b.String())

			go func() {
				for _, m := range messages {
					if _, err := s.ChannelMessageSend(channel.ID, m); err != nil {
						logger.Warn("Failed to send message", zap.Error(err))
						return
					}
					// Ensure it's stays below 25 messages per second
					time.Sleep(time.Millisecond * 50)
				}
			}()
			return nil
		},

		"party": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			if user == nil {
				return nil
			}

			options := i.ApplicationCommandData().Options
			switch options[0].Name {
			case "invite":
				options := options[0].Options
				inviter := i.User
				invitee := options[0].UserValue(s)

				if err := d.sendPartyInvite(ctx, s, i, inviter, invitee); err != nil {
					return err
				}
			case "members":

				// List the other players in this party group
				objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
					{
						Collection: MatchmakerStorageCollection,
						Key:        MatchmakingConfigStorageKey,
						UserID:     userID,
					},
				})
				if err != nil {
					logger.Error("Failed to read matchmaking config", zap.Error(err))
				}
				matchmakingConfig := &MatchmakingSettings{}
				if len(objs) != 0 {
					if err := json.Unmarshal([]byte(objs[0].Value), matchmakingConfig); err != nil {
						return fmt.Errorf("failed to unmarshal matchmaking config: %w", err)
					}
				}
				if matchmakingConfig.LobbyGroupName == "" {
					return errors.New("set a group ID first with `/party group`")
				}

				//logger = logger.WithField("group_id", matchmakingConfig.GroupID)
				groupName, partyUUID, err := GetLobbyGroupID(ctx, d.db, userID)
				if err != nil {
					return fmt.Errorf("failed to get party group ID: %w", err)
				}
				// Look for presences

				partyMembers, err := nk.StreamUserList(StreamModeParty, partyUUID.String(), "", d.pipeline.node, false, true)
				if err != nil {
					return fmt.Errorf("failed to list stream users: %w", err)
				}

				activeIDs := make([]string, 0, len(partyMembers))
				for _, partyMember := range partyMembers {
					activeIDs = append(activeIDs, d.cache.UserIDToDiscordID(partyMember.GetUserId()))
				}

				// Get a list of the all the inactive users in the party group
				userIDs, err := GetPartyGroupUserIDs(ctx, d.nk, groupName)
				if err != nil {
					return fmt.Errorf("failed to get party group user IDs: %w", err)
				}

				// remove the partymembers from the inactive list
				inactiveIDs := make([]string, 0, len(userIDs))

			OuterLoop:
				for _, u := range userIDs {
					for _, partyMember := range partyMembers {
						if partyMember.GetUserId() == u {
							continue OuterLoop
						}
					}
					inactiveIDs = append(inactiveIDs, d.cache.UserIDToDiscordID(u))
				}

				// Create a list of the members, and the mode of the lobby they are currently in, linked to an echotaxi link, and whether they are matchmaking.
				// <@discord_id> - [mode](https://echo.taxi/spark://c/match_id) (matchmaking)

				var content string
				if len(activeIDs) == 0 && len(inactiveIDs) == 0 {
					content = "No members in your party group."
				} else {
					b := strings.Builder{}
					b.WriteString(fmt.Sprintf("Members in party group `%s`:\n", groupName))
					for i, discordID := range activeIDs {
						if i > 0 {
							b.WriteString(", ")
						}
						b.WriteString("<@" + discordID + ">")
					}
					if len(inactiveIDs) > 0 {
						b.WriteString("\n\nInactive or offline members:\n")
						for i, discordID := range inactiveIDs {
							if i > 0 {
								b.WriteString(", ")
							}
							b.WriteString("<@" + discordID + ">")
						}
					}
					content = b.String()
				}

				// Send the message to the user
				return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: content,
					},
				})

			case "group":

				options := options[0].Options
				groupName := options[0].StringValue()
				// Validate the group is 1 to 12 characters long
				if len(groupName) < 1 || len(groupName) > 12 {
					return errors.New("invalid group ID. It must be between one (1) and eight (8) characters long")
				}
				// Validate the group is alphanumeric
				if !partyGroupIDPattern.MatchString(groupName) {
					return errors.New("invalid group ID. It must be alphanumeric")
				}
				// Validate the group is not a reserved group
				if lo.Contains([]string{"admin", "moderator", "verified", "serverhosts"}, groupName) {
					return errors.New("invalid group ID. It is a reserved group")
				}
				// lowercase the group
				groupName = strings.ToLower(groupName)

				objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
					{
						Collection: MatchmakerStorageCollection,
						Key:        MatchmakingConfigStorageKey,
						UserID:     userID,
					},
				})
				if err != nil {
					logger.Error("Failed to read matchmaking config", zap.Error(err))
				}
				matchmakingConfig := &MatchmakingSettings{}
				if len(objs) != 0 {
					if err := json.Unmarshal([]byte(objs[0].Value), matchmakingConfig); err != nil {
						return fmt.Errorf("failed to unmarshal matchmaking config: %w", err)
					}
				}
				matchmakingConfig.LobbyGroupName = groupName
				// Store it back

				data, err := json.Marshal(matchmakingConfig)
				if err != nil {
					return fmt.Errorf("failed to marshal matchmaking config: %w", err)
				}

				if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
					{
						Collection:      MatchmakerStorageCollection,
						Key:             MatchmakingConfigStorageKey,
						UserID:          userID,
						Value:           string(data),
						PermissionRead:  1,
						PermissionWrite: 0,
					},
				}); err != nil {
					return fmt.Errorf("failed to write matchmaking config: %w", err)
				}

				// Inform the user of the groupid
				return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: fmt.Sprintf("Your group ID has been set to `%s`. Everyone must matchmake at the same time (~15-30 seconds)", groupName),
					},
				})
			}
			return discordgo.ErrNilState
		},
		"outfits": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			if user == nil {
				return nil
			}

			options := i.ApplicationCommandData().Options

			if len(options) == 0 {
				return nil
			}

			outfits := make(map[string]*AccountCosmetics)

			objs, err := d.nk.StorageRead(ctx, []*runtime.StorageRead{
				{
					Collection: CustomizationStorageCollection,
					Key:        SavedOutfitsStorageKey,
					UserID:     userID,
				},
			})
			if err != nil {
				return fmt.Errorf("Failed to read saved outfits: %w", err)
			}

			if len(objs) != 0 {
				if err := json.Unmarshal([]byte(objs[0].Value), &outfits); err != nil {
					return fmt.Errorf("Failed to unmarshal saved outfits: %w", err)
				}
			}

			metadata, err := AccountMetadataLoad(ctx, d.nk, userID)
			if err != nil {
				return fmt.Errorf("Failed to get account metadata: %w", err)
			}

			switch options[0].Name {
			case "manage":
				outfitName := options[0].Options[1].StringValue()
				if len(outfitName) > 72 {
					return errors.New("Invalid profile name. It must be less than 72 characters long.")
				}

				switch options[0].Options[0].StringValue() {
				case "save":
					// limit set arbitrarily
					if len(outfits) >= 25 {
						return fmt.Errorf("Cannot save more than 25 outfits.")

					}

					outfits[outfitName] = &metadata.LoadoutCosmetics

					data, err := json.Marshal(outfits)
					if err != nil {
						return fmt.Errorf("Failed to marshal outfits: %w", err)
					}
					if _, err := d.nk.StorageWrite(ctx, []*runtime.StorageWrite{
						{
							Collection: CustomizationStorageCollection,
							Key:        SavedOutfitsStorageKey,
							UserID:     userID,
							Value:      string(data),
						},
					}); err != nil {
						return fmt.Errorf("Failed to save outfits: %w", err)
					}

					return simpleInteractionResponse(s, i, fmt.Sprintf("Saved current outfit as `%s`", outfitName))

				case "load":
					if _, ok := outfits[outfitName]; !ok {
						return simpleInteractionResponse(s, i, fmt.Sprintf("Outfit `%s` does not exist.", outfitName))
					}

					metadata.LoadoutCosmetics = *outfits[outfitName]

					if err := AccountMetadataUpdate(ctx, d.nk, userID, metadata); err != nil {
						return fmt.Errorf("Failed to set account metadata: %w", err)
					}

					return simpleInteractionResponse(s, i, fmt.Sprintf("Applied outfit `%s`. If the changes do not take effect in your next match, Please re-open your game.", outfitName))

				case "delete":
					if _, ok := outfits[outfitName]; !ok {
						simpleInteractionResponse(s, i, fmt.Sprintf("Outfit `%s` does not exist.", outfitName))
						return nil
					}

					delete(outfits, outfitName)

					if err := AccountMetadataUpdate(ctx, d.nk, userID, metadata); err != nil {
						return fmt.Errorf("Failed to set account metadata: %w", err)
					}

					return simpleInteractionResponse(s, i, fmt.Sprintf("Deleted loadout profile `%s`", outfitName))
				}

			case "list":
				if len(outfits) == 0 {
					return simpleInteractionResponse(s, i, "No saved outfits.")
				}

				responseString := "Available profiles: "

				for k := range outfits {
					responseString += fmt.Sprintf("`%s`, ", k)
				}

				return simpleInteractionResponse(s, i, responseString[:len(responseString)-2])
			}

			return discordgo.ErrNilState
		},
		"vrml-verify": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			// accountLinkCommandHandler handles the account link command from Discord

			nk := d.nk
			db := d.db

			// Check if the user is already linked
			userID, err := GetUserIDByDiscordID(ctx, db, user.ID)
			if err != nil {
				return fmt.Errorf("failed to get userID by DiscordID: %w", err)
			}

			if objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
				{
					Collection: StorageCollectionSocial,
					Key:        StorageKeyVRMLUser,
					UserID:     userID,
				},
			}); err != nil {
				return fmt.Errorf("failed to read storage: %w", err)
			} else {
				if len(objs) > 0 {
					return simpleInteractionResponse(s, i, fmt.Sprintf("Your VRML account is already linked. Contact EchoVRCE %s if you need to unlink it.", ServiceSettings().ReportURL))
				}
			}

			vars, _ := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

			// Start the OAuth flow
			timeoutDuration := 5 * time.Minute
			flow, err := NewVRMLOAuthFlow(vars["VRML_OAUTH_CLIENT_ID"], vars["VRML_OAUTH_REDIRECT_URL"], timeoutDuration)
			if err != nil {
				return fmt.Errorf("failed to start OAuth flow: %w", err)
			}

			editResponseFn := func(content string) error {
				_, err := s.InteractionResponseEdit(i.Interaction, &discordgo.WebhookEdit{
					Content: &content,
				})
				return err
			}

			go func() {
				<-time.After(1 * time.Second)
				// Wait for the token to be returned
				select {
				case token := <-flow.tokenCh:
					logger := logger.WithFields(map[string]interface{}{
						"user_id":    userID,
						"discord_id": i.Member.User.ID,
					})

					vg := vrmlgo.New(token)

					vrmlUser, err := vg.Me(vrmlgo.WithUseCache(false))
					if err != nil {
						logger.Error("Failed to get VRML user data")
						return
					}
					logger = logger.WithFields(map[string]interface{}{
						"vrml_id":         vrmlUser.ID,
						"vrml_discord_id": vrmlUser.GetDiscordID(),
					})

					if i.Member.User.ID != vrmlUser.GetDiscordID() {
						logger.Error("Discord ID mismatch")
						editResponseFn("Discord ID mismatch. Please make sure your VRML account is linked to the correct Discord account.")
						return
					}

					// Check if the account is already owned by another user
					if ownerID, err := flow.checkCurrentOwner(ctx, nk, vrmlUser.ID); err != nil {
						logger.Error("Failed to check current owner")
						editResponseFn("Failed to check current owner")
						return
					} else if ownerID != "" && ownerID != userID {
						content := fmt.Sprintf("Account already owned by another user. [Contact EchoVRCE](%s) if you need to unlink it.", ServiceSettings().ReportURL)
						logger.WithField("owner_id", ownerID).Error(content)
						editResponseFn("Account already owned by another user")
						return
					}

					// Link the accounts
					if err := flow.linkAccounts(ctx, nk, vg, userID); err != nil {
						editResponseFn("Failed to link accounts")
						return
					}

					editResponseFn("Your VRML account has been verified. It may take a few minutes (and up to a few hours) for your cosmetics to appear.\nNote: this system does not retain the token.")

				case <-time.After(timeoutDuration):
					simpleInteractionResponse(s, i, "OAuth flow timed out. Please run the command again.")
				}
			}()

			content := fmt.Sprintf("To assign your cosmetics, [Verify your VRML account](%s)", flow.url)

			// Send the link to the user
			return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral | discordgo.MessageFlagsSuppressEmbeds,
					Content: content,
				},
			})

		},
	}

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		user, _ := getScopedUserMember(i)

		logger := d.logger.WithFields(map[string]any{
			"discord_id": user.ID,
			"username":   user.Username,
			"guild_id":   i.GuildID,
			"channel_id": i.ChannelID,
			"user_id":    d.cache.DiscordIDToUserID(user.ID),
			"group_id":   d.cache.GuildIDToGroupID(i.GuildID),
		})

		switch i.Type {

		case discordgo.InteractionApplicationCommand:

			appCommandName := i.ApplicationCommandData().Name
			logger = logger.WithFields(map[string]any{
				"app_command": appCommandName,
				"options":     i.ApplicationCommandData().Options,
			})

			logger.Info("Handling application command.")
			if handler, ok := commandHandlers[appCommandName]; ok {
				err := d.handleInteractionApplicationCommand(logger, s, i, appCommandName, handler)
				if err != nil {
					logger.WithField("err", err).Error("Failed to handle interaction")
					// Queue the user to be updated in the cache
					userID := d.cache.DiscordIDToUserID(user.ID)
					groupID := d.cache.GuildIDToGroupID(i.GuildID)
					if userID != "" && groupID != "" {
						d.cache.QueueSyncMember(i.GuildID, user.ID)
					}
					if err := simpleInteractionResponse(s, i, err.Error()); err != nil {
						return
					}
				}
			} else {
				logger.Info("Unhandled command: %v", appCommandName)
			}
		case discordgo.InteractionMessageComponent:

			customID := i.MessageComponentData().CustomID
			commandName, value, _ := strings.Cut(customID, ":")

			logger = logger.WithFields(map[string]any{
				"custom_id": commandName,
				"value":     value,
			})

			logger.Info("Handling interaction message component.")

			err := d.handleInteractionMessageComponent(logger, s, i, commandName, value)
			if err != nil {
				logger.WithField("err", err).Error("Failed to handle interaction message component")
				if err := simpleInteractionResponse(s, i, err.Error()); err != nil {
					return
				}
			}
		case discordgo.InteractionApplicationCommandAutocomplete:

			data := i.ApplicationCommandData()

			logger = logger.WithFields(map[string]any{
				"app_command_autocomplete": data.Name,
				"options":                  data.Options,
				"data":                     data,
			})

			switch data.Name {
			case "unlink-headset":
				userID := d.cache.DiscordIDToUserID(user.ID)
				if userID == "" {
					logger.Error("Failed to get user ID")
					return
				}

				account, err := nk.AccountGetId(ctx, userID)
				if err != nil {
					logger.Error("Failed to get account", zap.Error(err))
				}

				devices := make([]string, 0, len(account.Devices))
				for _, device := range account.Devices {
					devices = append(devices, device.GetId())
				}

				if data.Options[0].StringValue() != "" {
					// User is typing a custom device name
					for i := 0; i < len(devices); i++ {
						if !strings.Contains(strings.ToLower(devices[i]), strings.ToLower(data.Options[0].StringValue())) {
							devices = append(devices[:i], devices[i+1:]...)
							i--
						}
					}
				}

				choices := make([]*discordgo.ApplicationCommandOptionChoice, len(account.Devices))
				for i, device := range account.Devices {
					choices[i] = &discordgo.ApplicationCommandOptionChoice{
						Name:  device.GetId(),
						Value: device.GetId(),
					}
				}

				if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionApplicationCommandAutocompleteResult,
					Data: &discordgo.InteractionResponseData{
						Choices: choices, // This is basically the whole purpose of autocomplete interaction - return custom options to the user.
					},
				}); err != nil {
					logger.Error("Failed to respond to interaction", zap.Error(err))
				}

			case "create":

				partial := ""

				for _, o := range data.Options {
					if o.Name == "region" {

						// Only response if the region is focused
						if !o.Focused {
							return
						}

						partial = o.StringValue()
					}
				}

				var found bool
				var err error
				var choices []*discordgo.ApplicationCommandOptionChoice

				if choices, found = d.choiceCache.Load(user.ID); !found {

					groupID := d.cache.GuildIDToGroupID(i.GuildID)
					if groupID == "" {
						return
					}

					userID := d.cache.DiscordIDToUserID(user.ID)
					if userID == "" {
						return
					}

					choices, err = d.autocompleteRegions(ctx, logger, userID, groupID)
					if err != nil {
						logger.Error("Failed to get regions", zap.Error(err))
						return
					}
					d.choiceCache.Store(user.ID, choices)

					go func() {
						<-time.After(20 * time.Second)
						d.choiceCache.Delete(user.ID)
					}()
				}

				partial = strings.ToLower(partial)
				partialCode := anyascii.Transliterate(partial)
				for i := 0; i < len(choices); i++ {
					if !strings.Contains(strings.ToLower(choices[i].Name), partial) && !strings.Contains(strings.ToLower(choices[i].Name), partialCode) {
						choices = append(choices[:i], choices[i+1:]...)
						i--
					}
				}

				if err := d.dg.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionApplicationCommandAutocompleteResult,
					Data: &discordgo.InteractionResponseData{
						Choices: choices, // This is basically the whole purpose of autocomplete interaction - return custom options to the user.
					},
				}); err != nil {
					logger.Error("Failed to respond to interaction", zap.Error(err))
				}
			}
		default:
			logger.Info("Unhandled interaction type: %v", i.Type)
		}
	})

	d.logger.Info("Registering slash commands.")
	// Register global guild commands
	d.updateSlashCommands(dg, d.logger, "")
	d.logger.Info("%d Slash commands registered/updated in %d guilds.", len(mainSlashCommands), len(dg.State.Guilds))

	return nil
}
func (d *DiscordAppBot) updateSlashCommands(s *discordgo.Session, logger runtime.Logger, guildID string) {
	// create a map of current commands
	currentCommands := make(map[string]*discordgo.ApplicationCommand, 0)
	for _, command := range mainSlashCommands {
		currentCommands[command.Name] = command
	}

	// Get the bot's current global application commands
	commands, err := s.ApplicationCommands(s.State.Application.ID, guildID)
	if err != nil {
		logger.WithField("err", err).Error("Failed to get application commands.")
		return
	}

	for _, command := range commands {
		if _, ok := currentCommands[command.Name]; !ok {
			logger.Debug("Deleting %s command: %s", guildID, command.Name)
			if err := s.ApplicationCommandDelete(s.State.Application.ID, guildID, command.ID); err != nil {
				logger.WithField("err", err).Error("Failed to delete application command.")
			}
		}
	}

	commands, err = s.ApplicationCommandBulkOverwrite(s.State.Application.ID, guildID, mainSlashCommands)
	if err != nil {
		logger.WithField("err", err).Error("Failed to bulk overwrite application commands.")
	}

}

func (d *DiscordAppBot) getPartyDiscordIds(ctx context.Context, partyHandler *PartyHandler) (map[string]string, error) {
	partyHandler.RLock()
	defer partyHandler.RUnlock()
	memberMap := make(map[string]string, len(partyHandler.members.presences)+1)
	leaderID, err := GetDiscordIDByUserID(ctx, d.db, partyHandler.leader.UserPresence.GetUserId())
	if err != nil {
		return nil, err
	}
	memberMap[leaderID] = partyHandler.leader.UserPresence.GetUserId()

	for _, presence := range partyHandler.members.presences {
		if presence.UserPresence.GetUserId() == partyHandler.leader.UserPresence.GetUserId() {
			continue
		}
		discordID, err := GetDiscordIDByUserID(ctx, d.db, presence.UserPresence.GetUserId())
		if err != nil {
			return nil, err
		}
		memberMap[discordID] = presence.UserPresence.UserId
	}
	return memberMap, nil
}

func (d *DiscordAppBot) ManageUserGroups(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer, callerUsername string, action string, usernames []string, groupNames []string) error {
	// FIXME validate the discord caller has rights to add to this group (i.e. is a admin of the group)
	// lookup the nakama group

	// Get nakama User ID from the discord ID
	users, err := nk.UsersGetUsername(ctx, append(usernames, callerUsername))
	if err != nil {
		logger.WithField("err", err).Error("Users get username error.")
	}

	callerId := ""
	userIds := make([]string, 0, len(users))
	for _, user := range users {
		if user.GetUsername() == callerUsername {
			callerId = user.GetId()
			continue
		}
		userIds = append(userIds, user.GetId())
	}

	if callerId == "" {
		logger.WithField("err", err).Error("Users get username error.")
		return fmt.Errorf("users get caller user id error: %w", err)
	}
	if len(userIds) == 0 {
		logger.WithField("err", err).Error("Users get username error.")
		return fmt.Errorf("get user id error: %w", err)
	}

	// Get the group ids
	for _, groupName := range groupNames {
		list, _, err := nk.GroupsList(ctx, groupName, "", nil, nil, 1, "")
		if err != nil || (list == nil) || (len(list) == 0) {
			logger.WithField("err", err).Error("Group list error.")
			return fmt.Errorf("group (%s) list error: %w", groupName, err)
		}
		groupId := list[0].GetId()

		switch action {
		case "add":
			if err := nk.GroupUsersAdd(ctx, callerId, groupId, userIds); err != nil {
				logger.WithField("err", err).Error("Group user add error.")
				return fmt.Errorf("group add user failed: %w", err)
			}
		case "remove":
			if err := nk.GroupUsersKick(ctx, callerId, groupId, userIds); err != nil {
				logger.WithField("err", err).Error("Group user add error.")
				return fmt.Errorf("group add user failed: %w", err)
			}
		case "ban":
			if err := nk.GroupUsersBan(ctx, callerId, groupId, userIds); err != nil {
				logger.WithField("err", err).Error("Group user add error.")
				return fmt.Errorf("group add user failed: %w", err)
			}
		}
	}

	return nil
}

func (d *DiscordAppBot) sendPartyInvite(ctx context.Context, s *discordgo.Session, i *discordgo.InteractionCreate, inviter, invitee *discordgo.User) error {
	/*

		if inviter.ID == invitee.ID {
			return fmt.Errorf("you cannot invite yourself to a party")
		}
			// Get the inviter's session id
			userID, sessionID, err := getLoginSessionForUser(ctx, inviter.ID, discordRegistry, pipeline)
			if err != nil {
				return err
			}

			// Get the invitee's session id
			inviteeUserID, inviteeSessionID, err := getLoginSessionForUser(ctx, invitee.ID, discordRegistry, pipeline)
			if err != nil {
				return err
			}

			// Get or create the party
			ph, err := getOrCreateParty(ctx, pipeline, discordRegistry, userID, inviter.Username, sessionID, inviter.ID)
			if err != nil {
				return err
			}

			ph.Lock()
			defer ph.Unlock()
			// Check if the invitee is already in the party
			for _, p := range ph.members.presences {
				if p.UserPresence.UserId == inviteeUserID.String() {
					return fmt.Errorf("<@%s> is already in your party.", invitee.ID)
				}
			}

			// Create join request for invitee
			_, err = pipeline.partyRegistry.PartyJoinRequest(ctx, ph.ID, pipeline.node, &Presence{
				ID: PresenceID{
					Node:      pipeline.node,
					SessionID: inviteeSessionID,
				},
				// Presence stream not needed.
				UserID: inviteeSessionID,
				Meta: PresenceMeta{
					Username: invitee.Username,
					// Other meta fields not needed.
				},
			})
			if err != nil {
				return fmt.Errorf("failed to create join request: %w", err)
			}
	*/

	partyID := uuid.Must(uuid.NewV4())
	inviteeSessionID := uuid.Must(uuid.NewV4())
	// Send ephemeral message to inviter
	if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: fmt.Sprintf("Invited %s to a party. Waiting for response.", invitee.Username),
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.Button{
							Label:    "Cancel Invitation",
							Style:    discordgo.DangerButton,
							Disabled: false,
							CustomID: fmt.Sprintf("fd_cancel_invite:%s:%s", partyID, inviteeSessionID),
						},
					},
				},
			},
		},
	}); err != nil {
		return fmt.Errorf("failed to send interaction response: %w", err)
	}

	// Send invite message to invitee
	channel, err := s.UserChannelCreate(invitee.ID)
	if err != nil {
		return fmt.Errorf("failed to create user channel: %w", err)
	}

	// Send the invite message
	s.ChannelMessageSendComplex(channel.ID, &discordgo.MessageSend{
		Content: fmt.Sprintf("%s has invited you to their in-game EchoVR party.", inviter.Username),
		Components: []discordgo.MessageComponent{
			discordgo.ActionsRow{
				Components: []discordgo.MessageComponent{
					discordgo.Button{
						// Label is what the user will see on the button.
						Label: "Accept",
						// Style provides coloring of the button. There are not so many styles tho.
						Style: discordgo.PrimaryButton,
						// Disabled allows bot to disable some buttons for users.
						Disabled: false,
						// CustomID is a thing telling Discord which data to send when this button will be pressed.
						CustomID: fmt.Sprintf("fd_accept_invite:%s:%s", partyID, inviteeSessionID),
					},
					discordgo.Button{
						Label:    "Decline",
						Style:    discordgo.DangerButton,
						Disabled: false,
						CustomID: fmt.Sprintf("fd_decline_invite:%s:%s", partyID, inviteeSessionID),
					},
				},
			},
		},
	})

	// Set a timer to delete the messages after 30 seconds
	time.AfterFunc(30*time.Second, func() {
		s.ChannelMessageDelete(inviter.ID, i.ID)
	})
	return nil
}

func getScopedUser(i *discordgo.InteractionCreate) *discordgo.User {
	switch {
	case i.User != nil:
		return i.User
	case i.Member.User != nil:
		return i.Member.User
	default:
		return nil
	}
}

func getScopedUserMember(i *discordgo.InteractionCreate) (user *discordgo.User, member *discordgo.Member) {
	if i.User != nil {
		user = i.User
	}

	if i.Member != nil {
		member = i.Member
		if i.Member.User != nil {
			user = i.Member.User
		}
	}
	return user, member
}

func simpleInteractionResponse(s *discordgo.Session, i *discordgo.InteractionCreate, content string) error {
	return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags:   discordgo.MessageFlagsEphemeral,
			Content: content,
		},
	})
}

func (d *DiscordAppBot) createRegionStatusEmbed(ctx context.Context, logger runtime.Logger, regionStr string, channelID string, existingMessage *discordgo.Message) error {
	// list all the matches

	matches, err := d.nk.MatchList(ctx, 100, true, "", nil, nil, "")
	if err != nil {
		return err
	}

	regionHash := evr.ToSymbol(regionStr).String()

	tracked := make([]*MatchLabel, 0, len(matches))

	for _, match := range matches {

		state := &MatchLabel{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), state); err != nil {
			logger.Error("Failed to unmarshal match label", zap.Error(err))
			continue
		}

		for _, r := range state.GameServer.RegionCodes {
			if regionStr == r || regionHash == r {
				tracked = append(tracked, state)
				continue
			}
		}
	}

	if len(tracked) == 0 {
		return fmt.Errorf("no matches found in region %s", regionStr)
	}

	// Create a message embed that contains a table of the server, the creation time, the number of players, and the spark link
	embed := &discordgo.MessageEmbed{
		Title:       fmt.Sprintf("Region %s", regionStr),
		Description: fmt.Sprintf("updated <t:%d:f>", time.Now().UTC().Unix()),
		Fields:      make([]*discordgo.MessageEmbedField, 0),
	}

	for _, state := range tracked {
		var status string

		if state.LobbyType == UnassignedLobby {
			status = "Unassigned"
		} else if state.Size == 0 {
			if !state.Started() {
				spawnedBy := "unknown"
				if state.SpawnedBy != "" {
					spawnedBy, err = GetDiscordIDByUserID(ctx, d.db, state.SpawnedBy)
					if err != nil {
						logger.Error("Failed to get discord ID", zap.Error(err))
					}
				}
				status = fmt.Sprintf("Reserved by <@%s> <t:%d:R>", spawnedBy, state.StartTime.UTC().Unix())
			} else {
				status = "Empty"
			}
		} else {
			players := make([]string, 0, state.Size)
			for _, player := range state.Players {
				players = append(players, fmt.Sprintf("`%s` (`%s`)", player.DisplayName, player.Username))
			}
			status = fmt.Sprintf("%s: %s", state.Mode.String(), strings.Join(players, ", "))

		}

		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   strconv.FormatUint(state.GameServer.ServerID, 10),
			Value:  status,
			Inline: false,
		})
	}

	if existingMessage != nil {
		t, err := discordgo.SnowflakeTimestamp(existingMessage.ID)
		if err != nil {
			return err
		}

		embed.Footer = &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Expires %s", t.Format(time.RFC1123)),
		}
		// Update the message for the given region
		_, err = d.dg.ChannelMessageEditEmbed(channelID, existingMessage.ID, embed)
		if err != nil {
			return err
		}

		return nil
	} else {
		// Create the message and update it regularly
		msg, err := d.dg.ChannelMessageSendEmbed(channelID, embed)
		if err != nil {
			return err
		}

		go func() {
			timer := time.NewTimer(24 * time.Hour)
			defer timer.Stop()
			ticker := time.NewTicker(30 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-d.ctx.Done():
					// Delete the message
					if err := d.dg.ChannelMessageDelete(channelID, msg.ID); err != nil {
						logger.Error("Failed to delete region status message: %s", err.Error())

					}
					return
				case <-timer.C:
					// Delete the message
					if err := d.dg.ChannelMessageDelete(channelID, msg.ID); err != nil {
						logger.Error("Failed to delete region status message: %s", err.Error())
					}
					return
				case <-ticker.C:
					// Update the message
					err := d.createRegionStatusEmbed(ctx, logger, regionStr, channelID, msg)
					if err != nil {
						logger.Error("Failed to update region status message: %s", err.Error())
						return
					}
				}
			}
		}()
	}
	return nil
}

var discordMarkdownEscapeReplacer = strings.NewReplacer(
	`\`, `\\`,
	"`", "\\`",
	"~", "\\~",
	"|", "\\|",
	"**", "\\**",
	"~~", "\\~~",
	"@", "@\u200B",
	"<", "<\u200B",
	">", ">\u200B",
	"_", "\\_",
)

func EscapeDiscordMarkdown(s string) string {
	return discordMarkdownEscapeReplacer.Replace(s)
}

func (d *DiscordAppBot) SendIPApprovalRequest(ctx context.Context, userID string, e *LoginHistoryEntry, ipqs *IPQSResponse) error {
	// Get the user's discord ID
	discordID, err := GetDiscordIDByUserID(ctx, d.db, userID)
	if err != nil {
		return err
	}

	// Send the message to the user
	channel, err := d.dg.UserChannelCreate(discordID)
	if err != nil {
		return err
	}

	// Send the message
	embeds, components := IPVerificationEmbed(e, ipqs)
	_, err = d.dg.ChannelMessageSendComplex(channel.ID, &discordgo.MessageSend{
		Embeds:     embeds,
		Components: components,
	})
	if err != nil {
		return err
	}

	return nil
}

func IPVerificationEmbed(entry *LoginHistoryEntry, ipqs *IPQSResponse) ([]*discordgo.MessageEmbed, []discordgo.MessageComponent) {

	code := fmt.Sprintf("%02d", entry.CreatedAt.Nanosecond()%100)

	codes := []string{code}

	numCodes := 5

	// Generate 3 more random numbers
	for len(codes) < numCodes {
		s := fmt.Sprintf("%02d", rand.Intn(100))
		if slices.Contains(codes, s) {
			continue
		}
		codes = append(codes, s)
	}

	// Shuffle the numbers
	rand.Shuffle(len(codes), func(i, j int) {
		codes[i], codes[j] = codes[j], codes[i]
	})

	options := make([]discordgo.SelectMenuOption, 0, len(codes))

	for _, code := range codes {
		options = append(options, discordgo.SelectMenuOption{
			Label: code,
			Value: entry.ClientIP + ":" + code,
		})
	}

	embed := &discordgo.MessageEmbed{
		Title:       "New Login Location",
		Description: "Please verify the login attempt.",
		Color:       0x00ff00,
		Fields: []*discordgo.MessageEmbedField{
			{
				Name:   "IP Address",
				Value:  entry.ClientIP,
				Inline: true,
			}},
	}

	if ipqs != nil {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Location (may be inaccurate)",
			Value:  fmt.Sprintf("%s, %s", ipqs.City, ipqs.Region),
			Inline: true,
		})

	}
	embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
		Name:   "Note",
		Value:  "Report this message, If you were not instructed (in your headset) to look for this message. Use the **Report** button below.",
		Inline: false,
	})

	components := []discordgo.MessageComponent{
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				&discordgo.SelectMenu{
					CustomID:    "approve_ip",
					Placeholder: "Select the correct code",
					Options:     options,
				},
			},
		},
		discordgo.ActionsRow{
			Components: []discordgo.MessageComponent{
				&discordgo.Button{
					Label:    "Report to EchoVRCE",
					Style:    discordgo.LinkButton,
					URL:      ServiceSettings().ReportURL,
					Disabled: false,
				},
			},
		},
	}

	return []*discordgo.MessageEmbed{embed}, components
}

func (d *DiscordAppBot) interactionToSignature(prefix string, options []*discordgo.ApplicationCommandInteractionDataOption) string {
	args := make([]string, 0, len(options))
	sep := ": "

	for _, opt := range options {
		strval := ""
		switch opt.Type {
		case discordgo.ApplicationCommandOptionSubCommand:
			strval = d.interactionToSignature(opt.Name, opt.Options)
		case discordgo.ApplicationCommandOptionSubCommandGroup:
			strval = d.interactionToSignature(opt.Name, opt.Options)
		case discordgo.ApplicationCommandOptionString:
			strval = opt.StringValue()
		case discordgo.ApplicationCommandOptionNumber:
			strval = fmt.Sprintf("%f", opt.FloatValue())
		case discordgo.ApplicationCommandOptionInteger:
			strval = fmt.Sprintf("%d", opt.IntValue())
		case discordgo.ApplicationCommandOptionBoolean:
			strval = fmt.Sprintf("%t", opt.BoolValue())
		case discordgo.ApplicationCommandOptionUser:
			strval = fmt.Sprintf("<@%s>", opt.Value)
		case discordgo.ApplicationCommandOptionChannel:
			strval = fmt.Sprintf("<#%s>", opt.Value)
		case discordgo.ApplicationCommandOptionRole:
			strval = fmt.Sprintf("<@&%s>", opt.Value)
		case discordgo.ApplicationCommandOptionMentionable:
			strval = fmt.Sprintf("<@%s>", opt.Value)
		default:
			strval = fmt.Sprintf("unknown type %d", opt.Type)
		}
		if strval != "" {
			args = append(args, opt.Name+sep+strval)
		}
	}
	return fmt.Sprintf("`/%s` { %s }", prefix, strings.Join(args, ", "))
}

func (d *DiscordAppBot) LogInteractionToChannel(i *discordgo.InteractionCreate, channelID string) error {
	data := i.ApplicationCommandData()
	signature := d.interactionToSignature(data.Name, data.Options)

	content := fmt.Sprintf("<@%s> used %s", i.Member.User.ID, signature)
	d.dg.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         content,
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	return nil
}

func (d *DiscordAppBot) LogAuditMessage(ctx context.Context, groupID string, message string, replaceMentions bool) (*discordgo.Message, error) {
	// replace all <@uuid> mentions with <@discordID>
	if replaceMentions {
		message = d.cache.ReplaceMentions(message)
	}

	groupMetadata, err := GetGuildGroupMetadata(ctx, d.db, groupID)
	if err != nil {
		return nil, err
	}

	if groupMetadata.AuditChannelID != "" {
		return d.dg.ChannelMessageSend(groupMetadata.AuditChannelID, message)
	}

	if cID := ServiceSettings().GlobalAuditChannelID; cID != "" {

	}
	return nil, nil
}

func (d *DiscordAppBot) LogUserErrorMessage(ctx context.Context, groupID string, message string, replaceMentions bool) (*discordgo.Message, error) {
	// replace all <@uuid> mentions with <@discordID>
	if replaceMentions {
		message = d.cache.ReplaceMentions(message)
	}

	groupMetadata, err := GetGuildGroupMetadata(ctx, d.db, groupID)
	if err != nil {
		return nil, err
	}

	if groupMetadata.ErrorChannelID != "" {
		return d.dg.ChannelMessageSend(groupMetadata.ErrorChannelID, message)
	}

	if cID := ServiceSettings().GlobalErrorChannelID; cID != "" {
		return d.dg.ChannelMessageSend(cID, message)
	}

	return nil, nil
}

func (d *DiscordAppBot) SendErrorToUser(userID string, userErr error) error {

	if userErr == nil {
		return nil
	}
	var err error
	if d.dg == nil {
		return fmt.Errorf("discord session is not initialized")
	}

	discordID := d.cache.UserIDToDiscordID(userID)
	if discordID == "" {
		return fmt.Errorf("discordID not found for user %s", userID)
	}

	_, err = d.dg.UserChannelCreate(discordID)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	_, err = d.dg.ChannelMessageSend(discordID, userErr.Error())
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

func (d *DiscordAppBot) SendIPAuthorizationNotification(userID string, ip string) error {
	if d == nil {
		return fmt.Errorf("discord bot is not initialized")
	}

	if d.dg == nil {
		return fmt.Errorf("discord session is not initialized")
	}

	discordID := d.cache.UserIDToDiscordID(userID)
	if discordID == "" {
		return fmt.Errorf("discordID not found for user %s", userID)
	}

	channel, err := d.dg.UserChannelCreate(discordID)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	if _, err = d.dg.ChannelMessageSend(channel.ID, fmt.Sprintf("IP `%s` has been automatically authorized, because you used discordID/password authentication.", ip)); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}
