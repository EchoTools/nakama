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
	"strconv"
	"strings"
	"sync"
	"time"

	anyascii "github.com/anyascii/go"
	"github.com/bwmarrin/discordgo"
	"github.com/echotools/vrmlgo/v5"
	"github.com/gofrs/uuid/v5"
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
	EmbedColorBlue   = 0x2274A5 // #2274A5
	EmbedColorOrange = 0xFE9920 // #FE9920
	EmbedColorGreen  = 0x499F68 // #499F68
	EmbedColorViolet = 0x4B244A // #4B244A
	EmbedColorRed    = 0xB02E0C // #B02E0C

	MaximumOutfitCount      = 100 // Maximum number of saved outfits per user
	MaximumOutfitNameLength = 72  // Maximum length of an outfit name
)

type DiscordAppBot struct {
	sync.Mutex

	ctx    context.Context
	logger runtime.Logger

	nk runtime.NakamaModule
	db *sql.DB
	dg *discordgo.Session

	config             Config
	metrics            Metrics
	pipeline           *Pipeline
	profileRegistry    *ProfileCache
	statusRegistry     StatusRegistry
	guildGroupRegistry *GuildGroupRegistry

	cache       *DiscordIntegrator
	ipInfoCache *IPInfoCache
	choiceCache *MapOf[string, []*discordgo.ApplicationCommandOptionChoice]
	igpRegistry *MapOf[string, *InGamePanel]

	debugChannels  map[string]string // map[groupID]channelID
	userID         string            // Nakama UserID of the bot
	partyStatusChs *MapOf[string, chan error]

	prepareMatchRatePerSecond rate.Limit
	prepareMatchBurst         int
	prepareMatchRateLimiters  *MapOf[string, *rate.Limiter]
}

func NewDiscordAppBot(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB, metrics Metrics, pipeline *Pipeline, config Config, discordCache *DiscordIntegrator, profileRegistry *ProfileCache, statusRegistry StatusRegistry, dg *discordgo.Session, ipInfoCache *IPInfoCache, guildGroupRegistry *GuildGroupRegistry) (*DiscordAppBot, error) {

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

		dg:                 dg,
		guildGroupRegistry: guildGroupRegistry,
		cache:              discordCache,
		ipInfoCache:        ipInfoCache,
		choiceCache:        &MapOf[string, []*discordgo.ApplicationCommandOptionChoice]{},

		igpRegistry:               &MapOf[string, *InGamePanel]{},
		prepareMatchRatePerSecond: 1.0 / 60,
		prepareMatchBurst:         1,
		prepareMatchRateLimiters:  &MapOf[string, *rate.Limiter]{},
		partyStatusChs:            &MapOf[string, chan error]{},
		debugChannels:             make(map[string]string),
	}

	discordgo.Logger = appbot.discordGoLogger

	//bot.LogLevel = discordgo.LogDebug
	dg.StateEnabled = true

	dg.Identify.Intents = discordgo.IntentsNone
	dg.Identify.Intents |= discordgo.IntentGuilds
	dg.Identify.Intents |= discordgo.IntentGuildMembers
	dg.Identify.Intents |= discordgo.IntentGuildBans

	dg.AddHandlerOnce(func(s *discordgo.Session, m *discordgo.Ready) {

		// Create a user for the bot based on it's discord profile
		userID, _, _, err := nk.AuthenticateCustom(ctx, m.User.ID, s.State.User.Username, true)
		if err != nil {
			logger.Error("Error creating discordbot user: %s", err)
		}

		// Update the global settings with the bots ID
		settings := *ServiceSettings()
		settings.DiscordBotUserID = userID
		ServiceSettingsUpdate(&settings)
		if err := ServiceSettingsSave(ctx, nk); err != nil {
			logger.Error("Error saving global settings: %s", err)
		}

		appbot.userID = userID

		displayName := dg.State.User.GlobalName
		if displayName == "" {
			displayName = dg.State.User.Username
		}

		if err := appbot.RegisterSlashCommands(); err != nil {
			logger.Error("Failed to register slash commands: %w", err)
		}

		logger.Info("Bot `%s` ready in %d guilds", displayName, len(dg.State.Guilds))
	})

	dg.AddHandler(func(s *discordgo.Session, m *discordgo.RateLimit) {
		logger.WithField("rate_limit", m).Warn("Discord rate limit")
	})

	// Update the status with the number of matches and players
	go func() {
		updateTicker := time.NewTicker(15 * time.Second)
		defer updateTicker.Stop()
		for {
			select {
			case <-updateTicker.C:
				dg.Lock()
				if !dg.DataReady {
					dg.Unlock()
					continue
				}
				dg.Unlock()

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
				statusMessage := fmt.Sprintf("%d players in %d matches", playerCount, matchCount)
				if err := dg.UpdateGameStatus(0, "with "+statusMessage); err != nil {
					logger.WithField("err", err).Warn("Failed to update status")
					continue
				}

				// Update the service status
				settings := *ServiceSettings()
				if settings.serviceStatusMessage != statusMessage {
					settings.serviceStatusMessage = statusMessage
					ServiceSettingsUpdate(&settings)
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
	limiter, _ := e.prepareMatchRateLimiters.LoadOrStore(key, rate.NewLimiter(e.prepareMatchRatePerSecond, e.prepareMatchBurst))
	return limiter
}

var (
	partyGroupIDPattern = regexp.MustCompile("^[a-z0-9]+$")

	mainSlashCommands = []*discordgo.ApplicationCommand{
		{
			Name:        "link",
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
			Name:        "unlink",
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
			Name:        "shutdown-match",
			Description: "Shutdown a match or game server.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "match-id",
					Description: "Match ID or Game Server ID",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "reason",
					Description: "Reason for the shutdown.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "disconnect-game-server",
					Description: "Disconnect the game server instead of just removing it from match listing",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionInteger,
					Name:        "grace-seconds",
					Description: "Seconds to wait before forcing the shutdown.",
					Required:    false,
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
		},
		{
			Name:        "whereami",
			Description: "Get information about your current match and game server.",
		},
		{
			Name:        "report-server-issue",
			Description: "Report an issue with the current game server.",
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
		{
			Name:        "set-command-channel",
			Description: "Set the command channel for this guild.",
		},
		{
			Name:        "generate-button",
			Description: "Place a link button in this channel.",
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
			Description: "Enable the gold nametag for your current session as a moderator.",
		},
		{
			Name:        "party-status",
			Description: "Show the members of your party.",
		},
		{
			Name:        "ign",
			Description: "Set in-game-name override. (use `-` to reset)",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "display-name",
					Description: "Your in-game name.",
					Required:    true,
				},
			},
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
					Name:        "user_notice",
					Description: "Reason for the kick (displayed to the user; 48 character max)",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "suspension_duration",
					Description: "Suspension duration (e.g. 1m, 2h, 3d, 4w)",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "moderator_notes",
					Description: "Notes for the audit log (other moderators)",
					Required:    false,
				},
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "allow_private_lobbies",
					Description: "Limit the user to only joining private lobbies.",
				},
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "require_community_values",
					Description: "Require the user to accept the community values before they can rejoin.",
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
				{
					Name:        "link-player",
					Description: "manually link a vrml player page to a player",
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
							Name:        "player-id",
							Description: "e.g. 4rPCIjBhKhGpG4uDnfHlfg2",
							Required:    true,
						},
						{
							Type:        discordgo.ApplicationCommandOptionBoolean,
							Name:        "force",
							Description: "force the link even if the discord ID doesn't match",
							Required:    false,
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
			Description: "link roles to EchoVRCE features. Non-members can only join private matches.",
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
					Description: "Allowed to host a game server for the guild.",
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
						{
							Name:  "Social Public",
							Value: "social_2.0",
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
			Description: "Manage EchoVRCE parties.",
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
			},
		},
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

type DiscordCommandHandlerFn func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error

func (d *DiscordAppBot) RegisterSlashCommands() error {
	ctx := d.ctx
	nk := d.nk
	db := d.db
	dg := d.dg

	commandHandlers := map[string]DiscordCommandHandlerFn{

		"set-command-channel": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			// Needs to have a green button that says link
			components := []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						&discordgo.Button{
							Label:    "Link EchoVRCE",
							Style:    discordgo.PrimaryButton,
							CustomID: "link-headset-modal",
							Emoji:    &discordgo.ComponentEmoji{Name: "🔗"},
						},
					},
				},
			}

			/// Create the channel message
			if _, err := d.dg.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
				Components: components,
			}); err != nil {
				return fmt.Errorf("failed to send message: %w", err)
			}

			// Set the command channel in the guild group metadata

			if md, err := GuildGroupLoad(ctx, nk, groupID); err != nil {
				return fmt.Errorf("failed to load guild group metadata: %w", err)
			} else {
				md.CommandChannelID = i.ChannelID
				if err := GuildGroupStore(ctx, nk, d.guildGroupRegistry, md); err != nil {
					return fmt.Errorf("failed to save guild group metadata: %w", err)
				}
			}

			// Respond to the interaction

			if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral,
					Content: "Command channel set to this channel.",
				},
			}); err != nil {
				return fmt.Errorf("failed to respond to interaction: %w", err)
			}

			return nil
		},
		"generate-button": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			// Needs to have a green button that says link
			components := []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						&discordgo.Button{
							Label:    "Link EchoVRCE",
							Style:    discordgo.PrimaryButton,
							CustomID: "link-headset-modal",
							Emoji:    &discordgo.ComponentEmoji{Name: "🔗"},
						},
					},
				},
			}

			if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral,
					Content: "Added link button",
				},
			}); err != nil {
				return fmt.Errorf("failed to respond to interaction: %w", err)
			}

			/// Create the channel message
			if message, err := d.dg.ChannelMessageSendComplex(i.ChannelID, &discordgo.MessageSend{
				Components: components,
			}); err != nil {
				return fmt.Errorf("failed to send message: %w", err)
			} else {
				go func() {
					<-time.After(10 * time.Minute)
					// Delete the message after 5 minutes
					if err := s.ChannelMessageDelete(i.ChannelID, message.ID); err != nil {
						logger.Error("Failed to delete message", zap.Error(err))
					}
				}()
			}

			return nil
		},
		"hash": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
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

		"party-status": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

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
									embeds[j].Title = "*Unknown User*"
									continue
								} else if member != nil {
									memberCache[discordIDs[j]] = member
								} else {
									embeds[j].Title = "*Unknown User*"
									continue
								}
							}
							displayName := member.DisplayName()
							if displayName == "" {
								displayName = member.User.Username
							}
							embeds[j].Title = displayName
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
							embeds[j].Title = "*Empty Slot*"
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
		"ign": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			options := i.ApplicationCommandData().Options
			if len(options) == 0 {
				return errors.New("no options provided")
			}
			displayName := options[0].StringValue()

			md, err := EVRProfileLoad(ctx, nk, userID)
			if err != nil {
				return fmt.Errorf("failed to load account metadata: %w", err)
			}

			// Check if the IGN is locked for this group
			if groupIGN, exists := md.InGameNames[groupID]; exists && groupIGN.IsLocked {
				return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: "Your display name is locked and cannot be changed. Contact a moderator for assistance.",
					},
				})
			}

			if displayName == "" || displayName == "-" || displayName == user.Username {
				// Prevent deletion if the IGN entry is locked
				if groupIGN, exists := md.InGameNames[groupID]; exists && groupIGN.IsLocked {
					return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Flags:   discordgo.MessageFlagsEphemeral,
							Content: "Your display name is locked and cannot be changed. Contact a moderator for assistance.",
						},
					})
				}
				delete(md.InGameNames, groupID)
			} else {
				if md.InGameNames == nil {
					md.InGameNames = make(map[string]GroupInGameName, 1)
				}
				// Store the in-game name override for this groupID
				md.InGameNames[groupID] = GroupInGameName{
					GroupID:     groupID,
					DisplayName: displayName,
					IsOverride:  true,
				}
			}

			if err := EVRProfileUpdate(ctx, nk, userID, md); err != nil {
				return fmt.Errorf("failed to store account metadata: %w", err)
			}

			return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral,
					Content: "Your display name has been updated.",
				},
			})
		},

		"igp":            d.handleInGamePanel,
		"link":           d.handleLinkHeadset,
		"unlink":         d.handleUnlinkHeadset,
		"link-headset":   d.handleLinkHeadset,
		"unlink-headset": d.handleUnlinkHeadset,
		"check-server": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

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
				timeout := 500 * time.Millisecond

				if err != nil {
					return fmt.Errorf("failed to determine local IP address: %w", err)
				}

				// If a single port is specified, do not scan
				rtts, err := BroadcasterRTTcheck(localIP, remoteIP, startPort, count, timeout)
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
		"shutdown-match": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			if user == nil {
				return nil
			}

			var (
				err              error
				options          = i.ApplicationCommandData().Options
				reason           string
				matchID          MatchID
				disconnectServer bool
				graceSeconds     int
			)

			isGlobalOperator, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalOperators)
			if err != nil {
				return fmt.Errorf("error checking global operator status: %w", err)
			}

			for _, option := range options {
				switch option.Name {
				case "match-id":

					matchIDStr := option.StringValue()
					matchIDStr = MatchUUIDPattern.FindString(strings.ToLower(matchIDStr))
					if matchIDStr == "" {
						return fmt.Errorf("invalid match ID: %s", matchIDStr)
					}

					matchID, err = MatchIDFromString(matchIDStr + "." + d.pipeline.node)
					if err != nil {
						return fmt.Errorf("failed to parse match ID: %w", err)
					}
				case "disconnect-game-server":
					disconnectServer = option.BoolValue()
				case "graceful":
					graceSeconds = int(option.IntValue())
				case "reason":
					reason = option.StringValue()
				}

			}
			_ = reason
			if err := func() error {
				if matchID.IsNil() {
					return errors.New("no match ID provided")
				}

				// Verify that the match is owned by the user, or is a guild enforcer
				label, err := MatchLabelByID(ctx, nk, matchID)
				if err != nil {
					return fmt.Errorf("failed to get match label: %w", err)
				}

				if !isGlobalOperator && label.GetGroupID().String() != groupID && label.GameServer.OperatorID.String() != userID {
					return errors.New("you do not have permission to shut down this match")
				}

				signal := SignalShutdownPayload{
					GraceSeconds:         graceSeconds,
					DisconnectGameServer: disconnectServer,
					DisconnectUsers:      false,
				}

				data := NewSignalEnvelope(userID, SignalShutdown, signal).String()

				// Signal the match to lock the session
				if _, err := nk.MatchSignal(ctx, matchID.String(), data); err != nil {
					return fmt.Errorf("failed to signal match: %w", err)
				}
				return nil
			}(); err != nil {
				return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: err.Error(),
					},
				})
			}

			// Send the response
			return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral,
					Content: "The match has been shut down.",
				},
			})
		},

		"reset-password": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

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

		"jersey-number": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
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

			md, err := EVRProfileLoad(ctx, nk, userID)
			if err != nil {
				return fmt.Errorf("failed to get account metadata: %w", err)
			}

			// Update the jersey number

			md.LoadoutCosmetics.JerseyNumber = int64(number)
			md.LoadoutCosmetics.Loadout.Decal = "loadout_number"
			md.LoadoutCosmetics.Loadout.DecalBody = "loadout_number"

			// Save the profile
			if err := EVRProfileUpdate(ctx, nk, userID, md); err != nil {
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

		"badges": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options
			var err error

			if user == nil {
				return nil
			}

			var isMember bool
			isMember, err = CheckSystemGroupMembership(ctx, db, userID, GroupGlobalBadgeAdmins)
			if err != nil {
				return status.Error(codes.Internal, "failed to check group membership")
			}
			if !isMember {
				return status.Error(codes.PermissionDenied, "you do not have permission to use this command")
			}

			switch options[0].Name {
			case "assign":
				options = options[0].Options
				// Check that the user is a developer

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
				channel := ServiceSettings().VRMLEntitlementNotifyChannelID
				if channel != "" {
					_, err = s.ChannelMessageSend(channel, fmt.Sprintf("%s assigned VRML cosmetics `%s` to user `%s`", user.Mention(), badgeCodestr, target.Username))
					if err != nil {
						logger.WithFields(map[string]interface{}{
							"error": err,
						}).Error("Failed to send badge channel update message")
					}
				}
				simpleInteractionResponse(s, i, fmt.Sprintf("Assigned VRML cosmetics `%s` to user `%s`", badgeCodestr, target.Username))
			case "link-player":
				// Link the player page directly
				options = options[0].Options
				if len(options) == 0 {
					return errors.New("no options provided")
				}
				var (
					target    *discordgo.User
					playerID  string
					forceLink bool
				)
				for _, option := range options {
					switch option.Name {
					case "user":
						target = option.UserValue(s)
					case "player-id":
						playerID = option.StringValue()
					case "force":
						forceLink = option.BoolValue()
					}
				}

				if target == nil {
					return status.Error(codes.InvalidArgument, "you must specify a user")
				}
				if playerID == "" {
					return status.Error(codes.InvalidArgument, "you must specify a player ID")
				}

				targetUserID := d.cache.DiscordIDToUserID(target.ID)
				if targetUserID == "" {
					return status.Error(codes.NotFound, "target user not found")
				}
				vg := vrmlgo.New("")

				vrmlPlayer, err := vg.Player(playerID)
				if err != nil {
					return fmt.Errorf("failed to get player: %w", err)
				} else if vrmlPlayer == nil {
					return fmt.Errorf("failed to get player: %w", err)
				}
				vrmlUser := vrmlPlayer.User
				if vrmlUser.UserID == "" {
					return fmt.Errorf("failed to get player's user")
				}
				playerURL := "https://vrmasterleague.com/EchoArena/Players/" + playerID

				vrmlDiscordID := strconv.FormatUint(uint64(vrmlUser.DiscordID), 10)
				if vrmlDiscordID != target.ID && !forceLink {
					return fmt.Errorf("VRML player [%s](%s) is not linked to discord user %s", vrmlUser.UserName, playerURL, target.Mention())
				}
				if err := LinkVRMLAccount(ctx, db, nk, targetUserID, vrmlPlayer.User.UserID); err != nil {
					if err, ok := err.(*AccountAlreadyLinkedError); ok {
						ownerID := d.cache.UserIDToDiscordID(err.OwnerUserID)
						return fmt.Errorf("VRML player [%s](%s) is already linked to <@%s>", vrmlUser.UserName, playerURL, ownerID)
					}
				}

				logger.WithFields(map[string]any{
					"discord_id":      user.ID,
					"username":        user.Username,
					"target_id":       target.ID,
					"target_username": target.Username,
					"vrml_player_id":  playerID,
					"vrml_user_id":    vrmlUser.UserID,
					"vrml_username":   vrmlUser.UserName,
				}).Debug("Manually linked VRML account")

				// Send the response
				if err := s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: fmt.Sprintf("Linked VRML player [%s](%s) to discord user %s", vrmlUser.UserName, playerURL, target.Mention()),
					},
				}); err != nil {
					return fmt.Errorf("failed to send interaction response: %w", err)
				}

				// Send a message to the channel
				channel := ServiceSettings().VRMLEntitlementNotifyChannelID
				if channel != "" {
					content := fmt.Sprintf("%s manually linked %s to VRML player [%s](%s)", user.Mention(), target.Mention(), vrmlUser.UserName, playerURL)
					if forceLink {
						content += " (forced)"
					}
					_, err = s.ChannelMessageSend(channel, content)
					if err != nil {
						logger.WithFields(map[string]any{
							"error": err,
						}).Error("Failed to send badge channel update message")
					}
				}

			}
			return nil
		},
		"whoami": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			if user == nil {
				return nil
			}
			if member == nil {
				return errors.New("this command must be used from a guild")
			}
			// check for the with-detail boolean option
			d.cache.Purge(user.ID)
			d.cache.QueueSyncMember(i.GuildID, user.ID, true)

			// Default options

			opts := UserProfileRequestOptions{
				IncludeSuspensionsEmbed:      true,
				IncludePastSuspensions:       false,
				IncludeCurrentMatchesEmbed:   true,
				IncludeVRMLHistoryEmbed:      true,
				IncludePastDisplayNamesEmbed: false,
				IncludeAlternatesEmbed:       false,

				IncludeDiscordDisplayName:      true,
				IncludeSuspensionAuditorNotes:  false,
				IncludeInactiveSuspensions:     false,
				ErrorIfAccountDisabled:         true,
				IncludePartyGroupName:          true,
				IncludeDefaultMatchmakingGuild: true,
				IncludeLinkedDevices:           true,
				StripIPAddresses:               false,
				IncludeRecentLogins:            true,
				IncludePasswordSetState:        true,
				IncludeGuildRoles:              true,
				IncludeAllGuilds:               true,
				IncludeMatchmakingTier:         true,
				ShowLoginsSince:                time.Now().Add(-30 * 24 * time.Hour),
				SendFileOnError:                false,
			}
			err := d.handleProfileRequest(ctx, logger, nk, s, i, user, opts)
			logger.WithFields(map[string]any{
				"discord_id":       user.ID,
				"discord_username": user.Username,
				"error":            err,
			}).Debug("whoami")
			return err
		},
		"whereami":            d.handleWhereAmI,
		"report-server-issue": d.handleReportServerIssue,
		"next-match": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			if user == nil {
				return nil
			}
			if len(i.ApplicationCommandData().Options) == 0 {
				return errors.New("no match provided")
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
		"throw-settings": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userIDStr string, groupID string) error {
			metadata, err := EVRProfileLoad(ctx, nk, userIDStr)
			if err != nil {
				return fmt.Errorf("failed to get account metadata: %w", err)
			}

			if metadata.GamePauseSettings == nil {
				return fmt.Errorf("GamePauseSettings is nil")
			}

			embed := &discordgo.MessageEmbed{
				Title: "Game Settings",
				Color: 5814783, // A hexadecimal color code
				Fields: []*discordgo.MessageEmbedField{
					{
						Name:   "Grab Deadzone",
						Value:  strconv.FormatFloat(metadata.GamePauseSettings.GrabDeadZone, 'f', -1, 64),
						Inline: true,
					},
					{
						Name:   "Release Distance",
						Value:  strconv.FormatFloat(metadata.GamePauseSettings.ReleaseDistance, 'f', -1, 64),
						Inline: true,
					},
					{
						Name:   "Wrist Angle Offset",
						Value:  strconv.FormatFloat(metadata.GamePauseSettings.WristAngleOffset, 'f', -1, 64),
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

		"set-lobby": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userIDStr string, groupID string) error {
			if member == nil {
				return fmt.Errorf("this command must be used from a guild")
			}

			d.cache.QueueSyncMember(i.GuildID, member.User.ID, true)

			// Set the metadata
			md, err := EVRProfileLoad(ctx, nk, userIDStr)
			if err != nil {
				return err
			}
			md.SetActiveGroupID(uuid.FromStringOrNil(groupID))

			if err := EVRProfileUpdate(ctx, nk, userIDStr, md); err != nil {
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
						fmt.Sprintf("EchoVRCE lobby changed to **%s**.", guild.Name),
						"- Matchmaking will prioritize members",
						"- Social lobbies will contain only members",
						"- Private matches that you create will prioritize guild's game servers.",
					}, "\n"),
				},
			})
		},
		"verify": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userIDStr string, groupID string) error {
			if member == nil {
				return fmt.Errorf("this command must be used from a guild")
			}

			loginHistory := &LoginHistory{}
			if err := StorableRead(ctx, nk, userIDStr, loginHistory, false); err != nil {
				return fmt.Errorf("failed to load login history: %w", err)
			}

			if len(loginHistory.PendingAuthorizations) == 0 {
				return simpleInteractionResponse(s, i, "No pending IP verifications")
			}

			var request *LoginHistoryEntry
			var latestTime time.Time
			for _, e := range loginHistory.PendingAuthorizations {
				if time.Since(e.UpdatedAt) > 10*time.Minute {
					continue
				}
				if e.UpdatedAt.After(latestTime) {
					latestTime = e.UpdatedAt
					request = e
				}
			}

			if request == nil {
				return simpleInteractionResponse(s, i, "No pending IP verifications")
			}

			ipInfo, _ := d.ipInfoCache.Get(ctx, request.ClientIP)

			embeds, components := IPVerificationEmbed(request, ipInfo)
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
		"lookup": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userIDStr string, groupID string) error {

			if user == nil {
				return nil
			}

			options := i.ApplicationCommandData().Options

			if len(options) == 0 {
				return errors.New("no options provided")
			}

			caller := user
			target := options[0].UserValue(s)

			if target.Bot {
				return simpleInteractionResponse(s, i, "Bots don't have accounts")
			}

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

			callerGuildGroups, err := GuildUserGroupsList(ctx, d.nk, d.guildGroupRegistry, callerUserID)
			if err != nil {
				return fmt.Errorf("failed to get guild groups: %w", err)
			}

			isGuildAuditor := false
			if gg, ok := callerGuildGroups[groupID]; ok && gg.IsAuditor(callerUserID) {
				isGuildAuditor = true
				d.cache.QueueSyncMember(i.GuildID, target.ID, true)
			}

			isGuildEnforcer := false
			if gg, ok := callerGuildGroups[groupID]; ok && gg.IsEnforcer(callerUserID) {
				isGuildEnforcer = true
			}

			isGlobalOperator, err := CheckSystemGroupMembership(ctx, db, userIDStr, GroupGlobalOperators)
			if err != nil {
				return fmt.Errorf("error checking global operator status: %w", err)
			}

			isGuildAuditor = isGuildAuditor || isGlobalOperator
			isGuildEnforcer = isGuildEnforcer || isGuildAuditor || isGlobalOperator

			loginsSince := time.Now().Add(-30 * 24 * time.Hour)
			if !isGlobalOperator {
				loginsSince = time.Time{}
			}

			opts := UserProfileRequestOptions{
				IncludeSuspensionsEmbed:      isGuildEnforcer,
				IncludePastSuspensions:       isGuildEnforcer,
				IncludeCurrentMatchesEmbed:   isGuildEnforcer,
				IncludeVRMLHistoryEmbed:      isGlobalOperator,
				IncludePastDisplayNamesEmbed: true,
				IncludeAlternatesEmbed:       isGlobalOperator,

				IncludeDiscordDisplayName:      isGuildEnforcer,
				IncludeSuspensionAuditorNotes:  isGuildEnforcer,
				IncludeInactiveSuspensions:     isGuildEnforcer,
				ErrorIfAccountDisabled:         !isGuildEnforcer,
				IncludePartyGroupName:          isGuildAuditor,
				IncludeDefaultMatchmakingGuild: isGuildAuditor,
				IncludeLinkedDevices:           isGuildAuditor,
				StripIPAddresses:               !isGlobalOperator,
				IncludeRecentLogins:            isGuildAuditor,
				IncludePasswordSetState:        isGuildAuditor,
				IncludeGuildRoles:              isGuildAuditor,
				IncludeAllGuilds:               isGlobalOperator,
				IncludeMatchmakingTier:         isGuildAuditor,
				ShowLoginsSince:                loginsSince,
				SendFileOnError:                isGlobalOperator,
			}

			return d.handleProfileRequest(ctx, logger, nk, s, i, target, opts)
		},
		"search": d.handleSearch,
		"create": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
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

			logger = logger.WithFields(map[string]any{
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

			serverLocation = label.GameServer.LocationRegionCode(true, true)

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

					// Update the list of playerListStr in the interaction response
					var playerListStr strings.Builder
					for _, p := range presences {
						if p.GetSessionId() == label.GameServer.SessionID.String() {
							continue
						}
						playerListStr.WriteString(fmt.Sprintf("<@!%s>\n", d.cache.UserIDToDiscordID(p.GetUserId())))
					}
					responseContent.Data.Embeds[0].Fields[4].Value = playerListStr.String()

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
		"allocate": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
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
		"kick-player": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, callerMember *discordgo.Member, userID string, groupID string) error {

			if user == nil {
				return nil
			}

			var (
				target                 *discordgo.User
				targetUserID           string
				userNotice             string
				notes                  string
				requireCommunityValues bool
				duration               string
				allowPrivateLobbies    bool
			)

			for _, o := range i.ApplicationCommandData().Options {
				switch o.Name {
				case "user":
					target = o.UserValue(s)
					targetUserID = d.cache.DiscordIDToUserID(target.ID)
					if targetUserID == "" {
						return errors.New("failed to get target user ID")
					}
				case "user_notice":
					userNotice = o.StringValue()
					if len(userNotice) > 48 {
						return errors.New("user notice must be less than 48 characters")
					}

				case "moderator_notes":
					notes = o.StringValue()
				case "require_community_values":
					requireCommunityValues = o.BoolValue()
				case "suspension_duration":
					duration = o.StringValue()
				case "allow_private_lobbies":
					allowPrivateLobbies = o.BoolValue()
				}
			}

			return d.kickPlayer(logger, i, callerMember, target, duration, userNotice, notes, requireCommunityValues, allowPrivateLobbies)
		},
		"join-player": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

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
		"set-roles": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
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
			metadata, err := GroupMetadataLoad(ctx, d.db, groupID)
			if err != nil {
				return errors.New("failed to get guild group metadata")
			}

			roles := metadata.RoleMap
			for _, o := range options {
				roleID := o.RoleValue(s, guild.ID).ID
				switch o.Name {
				case "moderator":
					roles.Enforcer = roleID
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

			gg, err := GuildGroupLoad(ctx, nk, groupID)
			if err != nil {
				return errors.New("failed to load guild group")
			}
			d.guildGroupRegistry.Add(gg)

			return simpleInteractionResponse(s, i, "roles set!")
		},

		"region-status": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
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
		"stream-list": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
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

		"party": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			if user == nil {
				return nil
			}

			options := i.ApplicationCommandData().Options
			switch options[0].Name {
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
				groupName := strings.ToLower(options[0].StringValue())

				// Validate the group is 1 to 12 characters long
				if !partyGroupIDPattern.MatchString(groupName) || len(groupName) < 1 || len(groupName) > 12 {
					return errors.New("invalid party group name. It must be 1-12 alphanumeric characters (a-z, A-Z, 0-9, _)")
				}

				// Validate the group is not a reserved group
				if lo.Contains([]string{"admin", "moderator", "verified"}, groupName) {
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
		"outfits": func(ctx context.Context, logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			if user == nil {
				return nil
			}

			options := i.ApplicationCommandData().Options

			if len(options) == 0 {
				return nil
			}

			wardrobe := &Wardrobe{}

			if err := StorableRead(ctx, d.nk, userID, wardrobe, true); err != nil {
				return fmt.Errorf("failed to read saved outfits: %w", err)
			}
			metadata, err := EVRProfileLoad(ctx, d.nk, userID)
			if err != nil {
				return fmt.Errorf("failed to get account metadata: %w", err)
			}

			switch options[0].Name {
			case "manage":
				if len(options[0].Options) < 2 {
					return errors.New("not enough options provided")
				}

				var (
					operation  = options[0].Options[0].StringValue()
					outfitName = options[0].Options[1].StringValue()
				)

				if len(outfitName) > MaximumOutfitNameLength {
					return fmt.Errorf("invalid profile name. Must be less than %d characters", MaximumOutfitNameLength+1)
				}

				switch operation {
				case "save":
					// limit set arbitrarily
					if len(wardrobe.Outfits) >= MaximumOutfitCount {
						return fmt.Errorf("cannot save more than %d outfits", MaximumOutfitCount)

					}

					wardrobe.SetOutfit(outfitName, metadata.LoadoutCosmetics)

					if err := StorableWrite(ctx, d.nk, userID, wardrobe); err != nil {
						return fmt.Errorf("failed to write saved outfits: %w", err)
					}

					return simpleInteractionResponse(s, i, fmt.Sprintf("Saved current outfit as `%s`", outfitName))

				case "load":
					outfit, ok := wardrobe.GetOutfit(outfitName)
					if !ok || outfit == nil {
						return simpleInteractionResponse(s, i, fmt.Sprintf("Outfit `%s` does not exist.", outfitName))
					}

					metadata.LoadoutCosmetics = *outfit

					if err := EVRProfileUpdate(ctx, d.nk, userID, metadata); err != nil {
						return fmt.Errorf("failed to set account metadata: %w", err)
					}

					return simpleInteractionResponse(s, i, fmt.Sprintf("Applied outfit `%s`. If the changes do not take effect in your next match, Please re-open your game.", outfitName))

				case "delete":
					if deleted := wardrobe.DeleteOutfit(outfitName); !deleted {
						simpleInteractionResponse(s, i, fmt.Sprintf("Outfit `%s` does not exist.", outfitName))
						return nil
					}

					if err := StorableWrite(ctx, d.nk, userID, wardrobe); err != nil {
						return fmt.Errorf("failed to write saved outfits: %w", err)
					}

					return simpleInteractionResponse(s, i, fmt.Sprintf("Deleted loadout profile `%s`", outfitName))
				}

			case "list":
				if len(wardrobe.Outfits) == 0 {
					return simpleInteractionResponse(s, i, "No saved outfits.")
				}

				responseString := "Available profiles: "

				for k := range wardrobe.Outfits {
					responseString += fmt.Sprintf("`%s`, ", k)
				}

				return simpleInteractionResponse(s, i, responseString[:len(responseString)-2])
			}

			return discordgo.ErrNilState
		},
		"vrml-verify": d.handleVRMLVerify,
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
				err := d.handleInteractionApplicationCommand(ctx, logger, s, i, appCommandName, handler)
				if err != nil {
					logger.WithField("err", err).Error("Failed to handle interaction")
					// Queue the user to be updated in the cache
					userID := d.cache.DiscordIDToUserID(user.ID)
					groupID := d.cache.GuildIDToGroupID(i.GuildID)
					if userID != "" && groupID != "" {
						d.cache.QueueSyncMember(i.GuildID, user.ID, false)
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

			err := d.handleInteractionMessageComponent(ctx, logger, s, i, commandName, value)
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
							devices = slices.Delete(devices, i, i+1)
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
						Flags:   discordgo.MessageFlagsEphemeral,
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
						Flags:   discordgo.MessageFlagsEphemeral,
						Choices: choices, // This is basically the whole purpose of autocomplete interaction - return custom options to the user.
					},
				}); err != nil {
					logger.Error("Failed to respond to interaction", zap.Error(err))
				}
			}

		case discordgo.InteractionModalSubmit:
			customID := i.ModalSubmitData().CustomID
			group, value, _ := strings.Cut(customID, ":")

			switch group {
			case "linkcode_modal":
				data := i.ModalSubmitData()
				member, err := d.dg.GuildMember(i.GuildID, i.Member.User.ID)
				if err != nil {
					logger.Error("Failed to get guild member", zap.Error(err))
					return
				}
				code := data.Components[0].(*discordgo.ActionsRow).Components[0].(*discordgo.TextInput).Value
				if err := d.linkHeadset(ctx, logger, member, code); err != nil {
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Content: "❌ Invalid code! Reopen your game and double check your code.",
							Flags:   discordgo.MessageFlagsEphemeral,
						},
					})
				} else {
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Content: "✅ Your are now linked. Restart your game.",
							Flags:   discordgo.MessageFlagsEphemeral,
						},
					})
				}
			case "igp":
				if err := d.handleModalSubmit(logger, i, value); err != nil {
					logger.Error("Failed to handle modal submit", zap.Error(err))
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Content: "Failed to handle modal submit: %s" + err.Error(),
							Flags:   discordgo.MessageFlagsEphemeral,
						},
					})
				}

			case "server_issue_modal":
				// Parse the value which contains issue type and server context
				// Format: "other:matchID:serverIP:regionCode" (from Report Other button)
				// or legacy format: "matchID" (old modal format)
				parts := strings.SplitN(value, ":", 2)
				issueType := ""
				serverContext := value
				if len(parts) >= 2 {
					issueType = parts[0]
					serverContext = parts[1]
				}
				// If issueType is empty, handleServerIssueModalSubmit will extract it from modal data
				if err := d.handleServerIssueModalSubmit(ctx, logger, s, i, issueType, serverContext); err != nil {
					logger.Error("Failed to handle server issue modal submit", zap.Error(err))
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Content: "Failed to submit server issue report: " + err.Error(),
							Flags:   discordgo.MessageFlagsEphemeral,
						},
					})
				}

			default:
				logger.WithField("custom_id", i.ModalSubmitData().CustomID).Info("Unhandled modal submit")
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

	_, err = s.ApplicationCommandBulkOverwrite(s.State.Application.ID, guildID, mainSlashCommands)
	if err != nil {
		commandNames := make([]string, 0, len(mainSlashCommands))
		for _, command := range mainSlashCommands {
			commandNames = append(commandNames, command.Name)
		}
		logger.WithFields(map[string]any{
			"guild_id":      guildID,
			"command_names": commandNames,
			"command_count": len(mainSlashCommands),
			"error":         err,
		}).Error("Failed to bulk overwrite application commands.")
	}

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
	var status string

	for _, state := range tracked {

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

func (d *DiscordAppBot) SendIPApprovalRequest(ctx context.Context, userID string, e *LoginHistoryEntry, ipInfo IPInfo) error {
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
	embeds, components := IPVerificationEmbed(e, ipInfo)
	_, err = d.dg.ChannelMessageSendComplex(channel.ID, &discordgo.MessageSend{
		Embeds:     embeds,
		Components: components,
	})

	return err
}

func IPVerificationEmbed(entry *LoginHistoryEntry, ipInfo IPInfo) ([]*discordgo.MessageEmbed, []discordgo.MessageComponent) {

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

	if ipInfo != nil {
		embed.Fields = append(embed.Fields, &discordgo.MessageEmbedField{
			Name:   "Location (may be inaccurate)",
			Value:  fmt.Sprintf("%s, %s, %s", ipInfo.City(), ipInfo.Region(), ipInfo.CountryCode()),
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
	sep := "="

	for _, opt := range options {
		strval := ""
		switch opt.Type {
		case discordgo.ApplicationCommandOptionSubCommand:
			strval = d.interactionToSignature(opt.Name, opt.Options)
		case discordgo.ApplicationCommandOptionSubCommandGroup:
			strval = d.interactionToSignature(opt.Name, opt.Options)
		case discordgo.ApplicationCommandOptionString:
			strval = fmt.Sprintf(`"%s"`, EscapeDiscordMarkdown(opt.StringValue()))
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
			args = append(args, fmt.Sprintf("*%s*%s%s", opt.Name, sep, strval))
		}
	}

	content := fmt.Sprintf("`/%s`", prefix)
	if len(args) > 0 {
		content += " { " + strings.Join(args, ", ") + " }"
	}

	return content
}

func (d *DiscordAppBot) LogInteractionToChannel(i *discordgo.InteractionCreate, channelID string) error {
	if channelID == "" {
		return nil
	}

	data := i.ApplicationCommandData()
	signature := d.interactionToSignature(data.Name, data.Options)

	content := fmt.Sprintf("%s (<@%s>) used %s", EscapeDiscordMarkdown(InGameName(i.Member)), i.Member.User.ID, signature)
	d.dg.ChannelMessageSendComplex(channelID, &discordgo.MessageSend{
		Content:         content,
		AllowedMentions: &discordgo.MessageAllowedMentions{},
	})
	return nil
}

func (d *DiscordAppBot) LogServiceAuditMessage(message string) error {
	return AuditLogSend(d.dg, ServiceSettings().ServiceAuditChannelID, message)
}

func (d *DiscordAppBot) LogServiceDebugMessage(message string) error {
	return AuditLogSend(d.dg, ServiceSettings().ServiceDebugChannelID, message)
}

func (d *DiscordAppBot) LogServiceUserErrorMessage(message string) error {
	return AuditLogSend(d.dg, ServiceSettings().ServiceDebugChannelID, message)
}

func (d *DiscordAppBot) LogAuditMessage(ctx context.Context, groupID string, message string, replaceMentions bool) (*discordgo.Message, error) {
	// replace all <@uuid> mentions with <@discordID>

	gg := d.guildGroupRegistry.Get(groupID)
	if gg == nil {
		return nil, fmt.Errorf("group not found")
	}

	if replaceMentions {
		message = d.cache.ReplaceMentions(message)
	}

	if err := d.LogServiceAuditMessage(fmt.Sprintf("[`%s/%s`] %s", gg.Name(), gg.GuildID, message)); err != nil {
		return nil, err
	}

	if gg.AuditChannelID != "" {
		return d.dg.ChannelMessageSendComplex(gg.AuditChannelID, &discordgo.MessageSend{
			Content:         message,
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		})
	}

	return nil, nil
}

func (d *DiscordAppBot) LogUserErrorMessage(ctx context.Context, groupID string, message string, replaceMentions bool) (*discordgo.Message, error) {
	if d == nil {
		return nil, fmt.Errorf("discord session is not initialized")
	}
	// replace all <@uuid> mentions with <@discordID>
	if replaceMentions {
		message = d.cache.ReplaceMentions(message)
	}

	if err := d.LogServiceUserErrorMessage(message); err != nil {
		return nil, err
	}

	gg := d.guildGroupRegistry.Get(groupID)
	if gg == nil {
		return nil, fmt.Errorf("group not found")
	}

	if gg.ErrorChannelID != "" {
		return d.dg.ChannelMessageSendComplex(gg.ErrorChannelID, &discordgo.MessageSend{
			Content:         message,
			AllowedMentions: &discordgo.MessageAllowedMentions{},
		})
	}

	return nil, nil
}

func (d *DiscordAppBot) createLookupSetIGNModal(currentDisplayName string, isLocked bool) *discordgo.InteractionResponse {
	allowPlayerToChangeIGN := !isLocked

	return &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseModal,
		Data: &discordgo.InteractionResponseData{
			CustomID: "lookup:set_ign_modal",
			Title:    "Override In-Game Name",
			Components: []discordgo.MessageComponent{
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "display_name_input",
							Label:       "In-Game Display Name",
							Value:       currentDisplayName,
							Style:       discordgo.TextInputShort,
							Required:    true,
							Placeholder: "Enter the desired In-Game Display Name",
						},
					},
				},
				// TODO this should be a true/false toggle or select menu, or set to "yes/no"
				discordgo.ActionsRow{
					Components: []discordgo.MessageComponent{
						discordgo.TextInput{
							CustomID:    "lock_input",
							Label:       "Allow player to change in-game display name? (true/false)",
							Value:       fmt.Sprintf("%t", allowPlayerToChangeIGN),
							Style:       discordgo.TextInputShort,
							Required:    true,
							Placeholder: "true or false",
						},
					},
				},
			},
		},
	}
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

func SendIPAuthorizationNotification(dg *discordgo.Session, discordID string, ip string) error {
	if dg == nil {
		return fmt.Errorf("discord session is not initialized")
	}

	channel, err := dg.UserChannelCreate(discordID)
	if err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	if _, err = dg.ChannelMessageSend(channel.ID, fmt.Sprintf("IP `%s` has been automatically authorized, because you used discordID/password authentication.", ip)); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}

	return nil
}

func IsDiscordErrorCode(err error, code int) bool {
	var restError *discordgo.RESTError
	if errors.As(err, &restError) && restError.Message != nil && restError.Message.Code == code {
		return true
	}
	return false
}
