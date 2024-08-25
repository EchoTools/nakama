package server

import (
	"context"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"golang.org/x/time/rate"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type DiscordAppBot struct {
	sync.Mutex

	ctx      context.Context
	cancelFn context.CancelFunc
	logger   runtime.Logger

	config          Config
	metrics         Metrics
	pipeline        *Pipeline
	discordRegistry DiscordRegistry
	profileRegistry *ProfileRegistry

	nk runtime.NakamaModule
	db *sql.DB
	dg *discordgo.Session

	debugChannels map[string]string // map[groupID]channelID
	userID        string            // Nakama UserID of the bot

	prepareMatchRatePerMinute rate.Limit
	prepareMatchBurst         int
	prepareMatchRateLimiters  *MapOf[string, *rate.Limiter]
}

func NewDiscordAppBot(logger runtime.Logger, nk runtime.NakamaModule, db *sql.DB, metrics Metrics, pipeline *Pipeline, config Config, discordRegistry DiscordRegistry, profileRegistry *ProfileRegistry, dg *discordgo.Session) (*DiscordAppBot, error) {
	ctx, cancelFn := context.WithCancel(context.Background())
	logger = logger.WithField("system", "discordAppBot")

	return &DiscordAppBot{
		ctx:      ctx,
		cancelFn: cancelFn,

		logger:   logger,
		nk:       nk,
		db:       db,
		pipeline: pipeline,
		metrics:  metrics,
		config:   config,

		dg: dg,

		discordRegistry: discordRegistry,
		profileRegistry: profileRegistry,

		prepareMatchRatePerMinute: 1,
		prepareMatchBurst:         1,
		prepareMatchRateLimiters:  &MapOf[string, *rate.Limiter]{},
		debugChannels:             make(map[string]string),
	}, nil
}

func (e *DiscordAppBot) loadPrepareMatchRateLimiter(userID, groupID string) *rate.Limiter {
	key := strings.Join([]string{userID, groupID}, ":")
	limiter, _ := e.prepareMatchRateLimiters.LoadOrStore(key, rate.NewLimiter(e.prepareMatchRatePerMinute, e.prepareMatchBurst))
	return limiter
}

type WhoAmI struct {
	NakamaID              uuid.UUID              `json:"nakama_id"`
	Username              string                 `json:"username"`
	DiscordID             string                 `json:"discord_id"`
	CreateTime            time.Time              `json:"create_time,omitempty"`
	DisplayNames          []string               `json:"display_names"`
	DeviceLinks           []string               `json:"device_links,omitempty"`
	HasPassword           bool                   `json:"has_password"`
	EVRIDLogins           map[string]time.Time   `json:"evr_id_logins"`
	GuildGroupMemberships []GuildGroupMembership `json:"guild_memberships"`
	VRMLSeasons           []string               `json:"vrml_seasons"`
	MatchIDs              []string               `json:"match_ids"`

	ClientAddresses []string `json:"addresses,omitempty"`
}

type EvrIdLogins struct {
	EvrId         string `json:"evr_id"`
	LastLoginTime string `json:"login_time"`
	DisplayName   string `json:"display_name,omitempty"`
}

var (
	vrmlGroupChoices = []*discordgo.ApplicationCommandOptionChoice{
		{Name: "VRML Preseason", Value: "VRML Season Preseason"},
		{Name: "VRML S1", Value: "VRML Season 1"},
		{Name: "VRML S1 Champion", Value: "VRML Season 1 Champion"},
		{Name: "VRML S1 Finalist", Value: "VRML Season 1 Finalist"},
		{Name: "VRML S2", Value: "VRML Season 2"},
		{Name: "VRML S2 Champion", Value: "VRML Season 2 Champion"},
		{Name: "VRML S2 Finalist", Value: "VRML Season 2 Finalist"},
		{Name: "VRML S3", Value: "VRML Season 3"},
		{Name: "VRML S3 Champion", Value: "VRML Season 3 Champion"},
		{Name: "VRML S3 Finalist", Value: "VRML Season 3 Finalist"},
		{Name: "VRML S4", Value: "VRML Season 4"},
		{Name: "VRML S4 Finalist", Value: "VRML Season 4 Finalist"},
		{Name: "VRML S4 Champion", Value: "VRML Season 4 Champion"},
		{Name: "VRML S5", Value: "VRML Season 5"},
		{Name: "VRML S5 Finalist", Value: "VRML Season 5 Finalist"},
		{Name: "VRML S5 Champion", Value: "VRML Season 5 Champion"},
		{Name: "VRML S6", Value: "VRML Season 6"},
		{Name: "VRML S6 Finalist", Value: "VRML Season 6 Finalist"},
		{Name: "VRML S6 Champion", Value: "VRML Season 6 Champion"},
		{Name: "VRML S7", Value: "VRML Season 7"},
		{Name: "VRML S7 Finalist", Value: "VRML Season 7 Finalist"},
		{Name: "VRML S7 Champion", Value: "VRML Season 7 Champion"},
	}

	vrmlGroupShortMap = map[string]string{
		"p":  "VRML Season Preseason",
		"1":  "VRML Season 1",
		"1f": "VRML Season 1 Finalist",
		"1c": "VRML Season 1 Champion",
		"2":  "VRML Season 2",
		"2f": "VRML Season 2 Finalist",
		"2c": "VRML Season 2 Champion",
		"3":  "VRML Season 3",
		"3f": "VRML Season 3 Finalist",
		"3c": "VRML Season 3 Champion",
		"4":  "VRML Season 4",
		"4f": "VRML Season 4 Finalist",
		"4c": "VRML Season 4 Champion",
		"5":  "VRML Season 5",
		"5f": "VRML Season 5 Finalist",
		"5c": "VRML Season 5 Champion",
		"6":  "VRML Season 6",
		"6f": "VRML Season 6 Finalist",
		"6c": "VRML Season 6 Champion",
		"7":  "VRML Season 7",
		"7f": "VRML Season 7 Finalist",
		"7c": "VRML Season 7 Champion",
	}

	partyGroupIDPattern   = regexp.MustCompile("^[a-z0-9]+$")
	vrmlIDPattern         = regexp.MustCompile("^[-a-zA-Z0-9]{24}$")
	cosmeticPresetPattern = regexp.MustCompile("^[a-zA-Z0-9-_]+$")

	mainSlashCommands = []*discordgo.ApplicationCommand{

		{
			Name:        "link-headset",
			Description: "Link your device to your discord account.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "link-code",
					Description: "Your four character link code.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionBoolean,
					Name:        "disable-ip-verification",
					Description: "Disable IP verification",
					Required:    false,
				},
			},
		},
		{
			Name:        "unlink-headset",
			Description: "Unlink a device from your discord account.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "device-link",
					Description: "device link from /whoami",
					Required:    true,
				},
			},
		},
		{
			Name:        "check-broadcaster",
			Description: "Check if an EchoVR broadcaster is actively responding on a port.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "address",
					Description: "host:port of the broadcaster",
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
			Description: "Receive your echo account information (privately).",
		},
		{
			Name:        "fixit",
			Description: "Fix your echo account",
		},
		{
			Name:        "set-lobby",
			Description: "Set your EchoVR lobby to this Discord server/guild.",
		},
		{
			Name:        "lookup",
			Description: "Lookup information about echo users.",
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
			Name:        "hash",
			Description: "Convert a string into an EVR symbol hash.",
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
			Name:        "trigger-cv",
			Description: "Force user to go through community values in social lobby.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Target user",
					Required:    true,
				},
			},
		},
		{
			Name:        "kick-player",
			Description: "Kick a player's sessions.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionUser,
					Name:        "user",
					Description: "Target user",
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
					Name:        "is-linked",
					Description: "Assigned/Removed by Nakama denoting if an account is linked to a headset.",
					Required:    true,
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
					Name:        "allocator",
					Description: "Allowed to reserve game servers.",
					Required:    true,
				},
				{
					Type:        discordgo.ApplicationCommandOptionRole,
					Name:        "suspension",
					Description: "Disallowed from joining any guild matches.",
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
							Name:  "Echo Arena Private",
							Value: "echo_arena_private",
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
							Name:  "Echo Arena Public",
							Value: "echo_arena",
						},
						{
							Name:  "Echo Combat Public",
							Value: "echo_combat",
						},
					},
				},
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "region",
					Description: "Region to allocate the session in (leave blank to use the best server for you)",
					Required:    false,
					Choices: []*discordgo.ApplicationCommandOptionChoice{
						{
							Name:  "US Central North (Chicago)",
							Value: "us-central-north",
						},
						{
							Name:  "US Central South (Texas)",
							Value: "us-central-south",
						},
						{
							Name:  "US Central South",
							Value: "us-east",
						},
						{
							Name:  "US West",
							Value: "us-west",
						},
						{
							Name:  "EU West",
							Value: "eu-west",
						},
						{
							Name:  "Japan",
							Value: "jp",
						},
						{
							Name:  "Singapore",
							Value: "sin",
						},
						{
							Name:  "Vibinator",
							Value: "82be4f8d-7504-4b67-8411-ce80c17bdf65",
						},
					},
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
			Name:        "cosmetic-loadout",
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
	//bot.LogLevel = discordgo.LogDebug

	ctx := d.ctx
	logger := d.logger
	nk := d.nk

	bot.Identify.Intents |= discordgo.IntentAutoModerationExecution
	bot.Identify.Intents |= discordgo.IntentMessageContent
	bot.Identify.Intents |= discordgo.IntentGuilds
	bot.Identify.Intents |= discordgo.IntentGuildMembers
	bot.Identify.Intents |= discordgo.IntentGuildBans
	bot.Identify.Intents |= discordgo.IntentGuildEmojis
	bot.Identify.Intents |= discordgo.IntentGuildWebhooks
	bot.Identify.Intents |= discordgo.IntentGuildInvites
	//bot.Identify.Intents |= discordgo.IntentGuildPresences
	bot.Identify.Intents |= discordgo.IntentGuildMessages
	bot.Identify.Intents |= discordgo.IntentGuildMessageReactions
	bot.Identify.Intents |= discordgo.IntentDirectMessages
	bot.Identify.Intents |= discordgo.IntentDirectMessageReactions
	bot.Identify.Intents |= discordgo.IntentMessageContent
	bot.Identify.Intents |= discordgo.IntentAutoModerationConfiguration
	bot.Identify.Intents |= discordgo.IntentAutoModerationExecution

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.Ready) {

		// Create a user for the bot based on it's discord profile
		userID, _, _, err := nk.AuthenticateCustom(ctx, m.User.ID, s.State.User.Username, true)
		if err != nil {
			logger.Error("Error creating discordbot user: %s", err)
		}
		d.userID = userID
		// Synchronize the guilds with nakama groups

		displayName := bot.State.User.GlobalName
		if displayName == "" {
			displayName = bot.State.User.Username
		}
		logger.Info("Bot `%s` ready in %d guilds", displayName, len(bot.State.Guilds))
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.RateLimit) {
		logger.WithField("rate_limit", m).Warn("Discord rate limit")
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMemberAdd) {
		if s == nil || m == nil {
			return
		}
		if m.Member == nil || m.Member.User == nil {
			return
		}
		if m.Member.User.ID == s.State.User.ID {
			guild, err := s.Guild(m.GuildID)
			if err != nil {
				logger.Error("Error getting guild: %s", err.Error())
			}

			if err := d.discordRegistry.SynchronizeGroup(ctx, guild); err != nil {
				logger.Error("Error synchronizing group: %s", err.Error())
				return
			}
		}
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMemberRemove) {
		if s == nil || m == nil {
			return
		}
		if m.Member == nil || m.Member.User == nil {
			return
		}
		if m.Member.User.ID == s.State.User.ID {
			guild, err := s.Guild(m.GuildID)
			if err != nil {
				logger.Error("Error getting guild: %w", err)
			}

			if guild == nil {
				groupID, err := GetGroupIDByGuildID(ctx, d.db, m.GuildID)
				if err != nil {
					return
				}
				// Remove the guild group from the system.
				err = d.nk.GroupDelete(ctx, groupID)
				if err != nil {
					logger.Error("Error deleting group: %w", err)
				}
			}
		}
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMemberAdd) {
		if s == nil || m == nil {
			return
		}
		if m.Member == nil || m.Member.User == nil {
			return
		}
		if m.Member.User.ID == s.State.User.ID {
			guild, err := s.Guild(m.GuildID)
			if err != nil {
				logger.Error("Error getting guild: %w", err)
			}
			if err := d.discordRegistry.SynchronizeGroup(ctx, guild); err != nil {
				logger.Error("Error synchronizing group: %s", err.Error())
				return
			}
		}
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMemberRemove) {
		if s == nil || m == nil {
			return
		}
		if m.Member == nil || m.Member.User == nil {
			return
		}
		if m.Member.User.ID == s.State.User.ID {
			guild, err := s.Guild(m.GuildID)
			if err != nil {
				if err, ok := err.(*discordgo.RESTError); ok {
					switch err.Message.Code {
					case discordgo.ErrCodeUnknownGuild:
						logger.Info("Guild not found, removing group")
						if guild == nil {
							groupID, err := GetGroupIDByGuildID(ctx, d.db, m.GuildID)
							if err != nil {
								return
							}
							// Remove the guild group from the system.
							err = d.nk.GroupDelete(ctx, groupID)
							if err != nil {
								logger.Error("Error deleting group: %w", err)
							}
						}
						return
					default:
						logger.Error("Error getting guild: %w", err)
					}
				}
				return
			}
		}
	})

	bot.AddHandler(func(se *discordgo.Session, m *discordgo.MessageCreate) {
		if se == nil || m == nil || m.Author == nil {
			return
		}

		if m.Author.ID == se.State.User.ID {
			return
		}

		if m.Author.ID != "155149108183695360" { // Dyno bot
			return
		}
		if m.Embeds == nil || len(m.Embeds) == 0 {
			return
		}
		guild, err := se.Guild(m.ID)
		if err != nil {
			logger.Error("Error getting guild: %w", err)
			return
		}

		suspensionStatus := &SuspensionStatus{
			GuildName: guild.Name,
			GuildId:   guild.ID,
		}
		e := m.Embeds[0]
		for _, f := range e.Fields {
			switch f.Name {
			case "User":
				suspensionStatus.UserDiscordId = strings.Trim(strings.Replace(strings.Replace(f.Value, "\\u003c", "<", -1), "\\u003e", ">", -1), "<@!>")
				userID, err := GetUserIDByDiscordID(ctx, d.db, suspensionStatus.UserDiscordId)
				if err != nil {
					logger.Error("Error getting suspended user id: %w", err)
					return
				}
				suspensionStatus.UserId = userID
			case "Moderator":
				suspensionStatus.ModeratorDiscordId = d.discordRegistry.ReplaceMentions(m.GuildID, f.Value)
			case "Length":
				suspensionStatus.Duration, err = parseDuration(f.Value)
				if err != nil || suspensionStatus.Duration <= 0 {
					logger.Error("Error parsing duration: %w", err)
					return
				}
				suspensionStatus.Expiry = m.Timestamp.Add(suspensionStatus.Duration)
			case "Role":
				roles, err := se.GuildRoles(m.GuildID)
				if err != nil {
					logger.Error("Error getting guild roles: %w", err)
					return
				}
				for _, role := range roles {
					if role.Name == f.Value {
						suspensionStatus.RoleId = role.ID
						suspensionStatus.RoleName = role.Name
						break
					}
				}
			case "Reason":
				suspensionStatus.Reason = d.discordRegistry.ReplaceMentions(m.GuildID, f.Value)
			}
		}

		if !suspensionStatus.Valid() {
			return
		}

		// Marshal it
		suspensionStatusBytes, err := json.Marshal(suspensionStatus)
		if err != nil {
			logger.Error("Error marshalling suspension status: %w", err)
			return
		}

		// Save the storage object.

		_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
			{
				Collection:      SuspensionStatusCollection,
				Key:             m.GuildID,
				UserID:          suspensionStatus.UserId,
				Value:           string(suspensionStatusBytes),
				PermissionRead:  0,
				PermissionWrite: 0,
			},
		})
		if err != nil {
			logger.Error("Error writing suspension status: %w", err)
			return
		}

	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.GuildBanAdd) {
		if s == nil || m == nil {
			return
		}

		if groupID, err := GetGroupIDByGuildID(ctx, d.db, m.GuildID); err == nil {
			if userID, err := GetUserIDByDiscordID(ctx, d.db, m.User.ID); err == nil {
				nk.GroupUsersKick(ctx, SystemUserID, groupID, []string{userID})
			}
		}
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.GuildBanRemove) {
		if s == nil || m == nil {
			return
		}
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMemberRemove) {
		if s == nil || m == nil {
			return
		}

		if groupID, err := GetGroupIDByGuildID(ctx, d.db, m.GuildID); err == nil {
			if userID, err := GetUserIDByDiscordID(ctx, d.db, m.User.ID); err == nil {
				nk.GroupUserLeave(ctx, groupID, userID, m.User.Username)
			}
		}
	})

	bot.AddHandler(func(s *discordgo.Session, e *discordgo.GuildMemberUpdate) {
		if s == nil || e == nil || e.BeforeUpdate == nil {
			return
		}

		// Only concerned with role changes
		if slices.Equal(e.BeforeUpdate.Roles, e.Roles) {
			return
		}
		userID, err := GetUserIDByDiscordID(ctx, d.db, e.User.ID)
		if err != nil {
			return
		}

		// Get the guild group, or sync the group
		_, err = GetGroupIDByGuildID(ctx, d.db, e.GuildID)
		if err != nil {
			// Update the user's guild group
			guild, err := s.State.Guild(e.GuildID)
			if err != nil {
				logger.Error("Error getting guild: %w", err)
				return
			}
			if guild == nil {
				logger.Error("Guild not found")
				return
			}
			if err := d.discordRegistry.SynchronizeGroup(ctx, guild); err != nil {
				logger.Error("Error synchronizing group: %s", err.Error())
				return
			}
		}

		if err != nil {
			if status.Code(err) == codes.NotFound {
				return
			}
			logger.Error("Error getting user id: %s", err.Error())
			return
		}
		err = d.discordRegistry.UpdateGuildGroup(ctx, logger, uuid.FromStringOrNil(userID), e.GuildID)
		if err != nil {
			logger.Error("Error updating guild group: %s", err.Error())
			return
		}

		go d.discordRegistry.UpdateAccount(context.Background(), uuid.FromStringOrNil(userID))
	})

	/*
		bot.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMembersChunk) {
			if err := OnGuildMembersChunk(ctx, s, m); err != nil {
				logger.Error("Error calling OnGuildMembersChunk: %w", err)
			}
		})
	*/

	bot.AddHandlerOnce(func(s *discordgo.Session, e *discordgo.Ready) {
		if err := d.RegisterSlashCommands(); err != nil {
			logger.Error("Failed to register slash commands: %w", err)
		}
		logger.Info("Registered slash commands")
	})

	// Update the status with the number of matches and players
	go func() {
		updateTicker := time.NewTicker(1 * time.Minute)
		defer updateTicker.Stop()
		for {
			select {
			case <-updateTicker.C:
				// Get all the matches
				minSize := 2
				maxSize := MatchLobbyMaxSize + 1
				matches, err := nk.MatchList(ctx, 1000, true, "", &minSize, &maxSize, "")
				if err != nil {
					logger.Error("Error fetching matches: %w", err)
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
					logger.Error("Error updating status: %w", err)
				}

			case <-ctx.Done():
				updateTicker.Stop()
				return
			}
		}
	}()

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
	discordRegistry := d.discordRegistry

	// Build a map of VRML group names to their group IDs
	vrmlGroups := make(map[string]string)
	for _, choice := range vrmlGroupChoices {
		// Look up the group by name
		groupName := choice.Value.(string)
		groups, _, err := nk.GroupsList(ctx, groupName, "", nil, nil, 1, "")
		if err != nil {
			d.logger.Error("Error looking up group: %s", err.Error())
			continue
		}
		if len(groups) == 0 {
			d.logger.Error("Group not found: %s", groupName)
			continue
		}
		vrmlGroups[groupName] = groups[0].Id
	}

	commandHandlers := map[string]DiscordCommandHandlerFn{

		"hash": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options
			token := options[0].StringValue()
			symbol := evr.ToSymbol(token)
			bytes := binary.LittleEndian.AppendUint64([]byte{}, uint64(symbol))

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
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
			return nil
		},

		"link-headset": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options
			linkCode := options[0].StringValue()

			if user == nil {
				return nil
			}

			// Validate the link code as a 4 character string
			if len(linkCode) != 4 {
				return errors.New("invalid link code: link code must be (4) characters long")
			}
			disableIPVerification := false
			if len(options) > 1 {
				disableIPVerification = options[1].BoolValue()
			}

			if err := func() error {

				if linkCode == "0000" {
					return errors.New("sychronized Discord<->Nakama")
				}

				// Exchange the link code for a device auth.
				token, err := ExchangeLinkCode(ctx, nk, logger, linkCode)
				if err != nil {
					return err
				}

				if disableIPVerification {
					token.ClientIP = "*"
				}

				return nk.LinkDevice(ctx, userID, token.Token())
			}(); err != nil {
				logger.WithFields(map[string]interface{}{
					"discord_id": user.ID,
					"link_code":  linkCode,
					"error":      err,
				}).Error("Failed to link headset")
				return err
			}
			go func() {
				// Update the accounts roles, etc.
				if err := d.discordRegistry.UpdateGuildGroup(ctx, logger, uuid.FromStringOrNil(userID), i.GuildID); err != nil {
					logger.Error("Error updating guild group: %s", err.Error())
				}
				if err := discordRegistry.UpdateAccount(context.Background(), uuid.FromStringOrNil(userID)); err != nil {
					logger.Error("Error updating account: %w", err)
				}
			}()
			// Send the response
			return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral,
					Content: "Your headset has been linked. Restart EchoVR.",
				},
			})
		},
		"unlink-headset": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options
			deviceId := options[0].StringValue()
			// Validate the link code as a 4 character string

			if user == nil {
				return nil
			}

			if err := func() error {
				// Get the userid by username
				userID, err := GetUserIDByDiscordID(ctx, db, user.ID)
				if err != nil {
					return fmt.Errorf("failed to authenticate user %s: %w", user.ID, err)
				}

				return nk.UnlinkDevice(ctx, userID, deviceId)

			}(); err != nil {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: err.Error(),
					},
				})
			}

			// Send the response
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral,
					Content: "Your headset has been unlinked. Restart EchoVR.",
				},
			})
			return nil
		},
		"check-broadcaster": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

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
					return fmt.Errorf("failed to resolve address: %v", err)
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
					return fmt.Errorf("invalid start port: %v", err)
				}
				if len(portRange) == 1 {
					// If a single port is specified, do not scan
					endPort = startPort
				} else {
					// If a port range is specified, scan the specified range
					if endPort, err = strconv.Atoi(portRange[1]); err != nil {
						return fmt.Errorf("invalid end port: %v", err)
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
					return fmt.Errorf("failed to determine local IP address: %v", err)
				}

				// If a single port is specified, do not scan
				rtts, err := BroadcasterRTTcheck(localIP, remoteIP, startPort, count, interval, timeout)
				if err != nil {
					return fmt.Errorf("failed to healthcheck broadcaster: %v", err)
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
							Content: fmt.Sprintf("Broadcaster %s:%d RTTs (AVG: %.0f): %s", remoteIP, startPort, avgrtt.Seconds()*1000, rttMessage),
						},
					})
				} else {
					return errors.New("no response from broadcaster")

				}
			} else {

				// Scan the address for responding broadcasters and then return the results as a newline-delimited list of ip:port
				responses, _ := BroadcasterPortScan(localIP, remoteIP, 6792, 6820, 500*time.Millisecond)
				if len(responses) == 0 {
					return errors.New("no broadcasters are responding")
				}

				// Craft a message that contains the newline-delimited list of the responding broadcasters
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
				userID, err := GetUserIDByDiscordID(ctx, d.db, user.ID)
				if err != nil {
					return err
				}
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

				userID, err := GetUserIDByDiscordID(ctx, db, user.ID)
				if err != nil {
					return status.Error(codes.PermissionDenied, "you do not have permission to use this command")
				}
				var member bool
				member, err = CheckSystemGroupMembership(ctx, db, userID, GroupGlobalBadgeAdmins)
				if err != nil {
					return status.Error(codes.Internal, "failed to check group membership")
				}
				if !member {
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

				// Get the badge name
				badgeCodestr := options[1].StringValue()
				badgeCodes := strings.Split(strings.ToLower(badgeCodestr), ",")
				badgeGroups := make([]string, 0, len(badgeCodes))
				for _, c := range badgeCodes {

					c := strings.TrimSpace(c)
					if c == "" {
						continue
					}
					groupName, ok := vrmlGroupShortMap[c]
					if !ok {
						return status.Errorf(codes.InvalidArgument, fmt.Sprintf("badge `%s` not found", c))
					}

					groupID, ok := vrmlGroups[groupName]
					if !ok {
						return status.Error(codes.Internal, fmt.Sprintf("badge `%s` not found (this shouldn't happen)", c)) // This shouldn't happen
					}
					badgeGroups = append(badgeGroups, groupID)
				}

				targetUserID, err := GetUserIDByDiscordID(ctx, db, target.ID)
				if err != nil {
					return status.Errorf(codes.Internal, "failed to get user `%s`: %s", target.Username, err)
				}

				for _, groupID := range badgeGroups {
					// Add the user to the group

					if err = nk.GroupUsersAdd(ctx, SystemUserID, groupID, []string{targetUserID}); err != nil {
						return status.Error(codes.Internal, fmt.Errorf("failed to assign badge `%s` to user `%s`: %w", groupID, target.Username, err).Error())
					}
				}

				// Log the action
				logger.Info("assign badges", zap.String("badges", badgeCodestr), zap.String("user", target.Username), zap.String("discord_id", target.ID), zap.String("assigner", user.ID))

				// Send a message to the channel
				channel := "1232462244797874247"
				_, err = s.ChannelMessageSend(channel, fmt.Sprintf("<@%s> assigned VRML cosmetics `%s` to user `%s`", user.ID, badgeCodestr, target.Username))
				if err != nil {
					logger.Warn("failed to send message", zap.Error(err))
					break
				}
				simpleInteractionResponse(s, i, fmt.Sprintf("Assigned VRML cosmetics `%s` to user `%s`", badgeCodestr, target.Username))

			case "set-vrml-username":
				options = options[0].Options
				// Get the user's discord ID
				user := getScopedUser(i)
				if user == nil {
					return nil
				}
				vrmlUsername := options[0].StringValue()

				// Check the vlaue against vrmlIDPattern
				if !vrmlIDPattern.MatchString(vrmlUsername) {
					return fmt.Errorf("invalid VRML username: `%s`", vrmlUsername)
				}

				// Access the VRML HTTP API
				url := fmt.Sprintf("https://api.vrmasterleague.com/EchoArena/Players/Search?name=%s", vrmlUsername)
				var req *http.Request
				req, err = http.NewRequest("GET", url, nil)
				if err != nil {
					return status.Error(codes.Internal, "failed to create request")
				}

				req.Header.Set("User-Agent", "EchoVRCE Discord Bot (contact: @sprockee)")

				// Make the request
				var resp *http.Response
				resp, err = http.DefaultClient.Do(req)
				if err != nil {
					return status.Error(codes.Internal, "failed to make request")
				}

				// Parse the response as JSON...
				// [{"id":"4rPCIjBhKhGpG4uDnfHlfg2","name":"sprockee","image":"/images/logos/users/25d45af7-f6a8-40ef-a035-879a61869c8f.png"}]
				var players []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				}

				if err = json.NewDecoder(resp.Body).Decode(&players); err != nil {
					return status.Error(codes.Internal, "failed to decode response: "+err.Error())
				}

				// Check if the player was found
				if len(players) == 0 {
					return status.Error(codes.NotFound, "player not found")
				}

				// Ensure that only one was returned
				if len(players) > 1 {
					return status.Error(codes.Internal, "multiple players found")
				}

				// Get the player's ID
				playerID := players[0].ID

				type VRMLUser struct {
					UserID        string      `json:"userID"`
					UserName      string      `json:"userName"`
					UserLogo      string      `json:"userLogo"`
					Country       string      `json:"country"`
					Nationality   string      `json:"nationality"`
					DateJoinedUTC string      `json:"dateJoinedUTC"`
					StreamURL     interface{} `json:"streamUrl"`
					DiscordID     float64     `json:"discordID"`
					DiscordTag    string      `json:"discordTag"`
					SteamID       interface{} `json:"steamID"`
					IsTerminated  bool        `json:"isTerminated"`
				}
				type Game struct {
					GameID         string `json:"gameID"`
					GameName       string `json:"gameName"`
					TeamMode       string `json:"teamMode"`
					MatchMode      string `json:"matchMode"`
					URL            string `json:"url"`
					URLShort       string `json:"urlShort"`
					URLComplete    string `json:"urlComplete"`
					HasSubstitutes bool   `json:"hasSubstitutes"`
					HasTies        bool   `json:"hasTies"`
					HasCasters     bool   `json:"hasCasters"`
					HasCameraman   bool   `json:"hasCameraman"`
				}

				type ThisGame struct {
					PlayerID   string `json:"playerID"`
					PlayerName string `json:"playerName"`
					UserLogo   string `json:"userLogo"`
					Game       Game   `json:"game"`
				}

				type playerDetailed struct {
					User     VRMLUser `json:"user"`
					ThisGame ThisGame `json:"thisGame"`
				}
				jsonData, err := json.Marshal(players[0])
				if err != nil {
					return status.Error(codes.Internal, "failed to marshal player data: "+err.Error())
				}

				// Set the VRML ID for the user in their profile as a storage object
				_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
					{
						Collection: VRMLStorageCollection,
						Key:        playerID,
						UserID:     user.ID,
						Value:      string(jsonData),
						Version:    "*",
					},
				})
				if err != nil {
					return status.Error(codes.Internal, "failed to set VRML ID: "+err.Error())
				}

				logger.Info("set vrml id", zap.String("discord_id", user.ID), zap.String("discord_username", user.Username), zap.String("vrml_id", playerID))

				return simpleInteractionResponse(s, i, fmt.Sprintf("set VRML username `%s` for user `%s`", vrmlUsername, user.Username))
			}
			return nil
		},
		"whoami": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			if user == nil {
				return nil
			}

			return d.handleProfileRequest(ctx, logger, nk, s, discordRegistry, i, user.ID, user.Username, "", true)
		},

		"set-lobby": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userIDStr string, groupID string) error {

			if member == nil {
				return fmt.Errorf("this command must be used from a guild")
			}

			guildID := i.GuildID

			userID := uuid.FromStringOrNil(userIDStr)

			// Try to find it by searching
			memberships, err := d.discordRegistry.GetGuildGroupMemberships(ctx, userID, []uuid.UUID{uuid.FromStringOrNil(groupID)})
			if err != nil {
				return err
			}

			if len(memberships) == 0 {
				err := d.discordRegistry.UpdateGuildGroup(ctx, logger, userID, guildID)
				if err != nil {
					return err
				}
				return errors.New("guild data stale, please try again in a few seconds")

			}
			membership := memberships[0]

			profile, err := d.profileRegistry.Load(ctx, userID)
			if err != nil {
				return err
			}
			profile.SetChannel(evr.GUID(membership.GuildGroup.ID()))

			if err = d.profileRegistry.SaveAndCache(ctx, userID, profile); err != nil {
				return err
			}

			guild, err := s.Guild(i.GuildID)
			if err != nil {
				logger.Error("Failed to get guild", zap.Error(err))
				return err
			}

			d.profileRegistry.SaveAndCache(ctx, userID, profile)
			return s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags: discordgo.MessageFlagsEphemeral,
					Content: strings.Join([]string{
						fmt.Sprintf("EchoVR lobby changed to **%s**.", guild.Name),
						"- Matchmaking will prioritize members",
						"- Social lobbies will contain only members",
						"- Private matches that you create will prioritize guild's broadcasters/servers.",
					}, "\n"),
				},
			})
		},
		"lookup": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userIDStr string, groupID string) error {

			if user == nil {
				return nil
			}

			// Get the caller's nakama user ID
			userID := uuid.FromStringOrNil(userIDStr)

			guildID := ""
			if member != nil {
				memberships, err := d.discordRegistry.GetGuildGroupMemberships(ctx, userID, nil)
				if err != nil {
					return errors.Join(errors.New("failed to get user ID"), err)
				}

				var membership *GuildGroupMembership

				// Get the caller's nakama guild group membership
				for _, m := range memberships {
					if m.GuildGroup.GuildID() == i.GuildID {
						membership = &m
						break
					}
				}

				if membership == nil {
					err := d.discordRegistry.UpdateGuildGroup(ctx, logger, userID, i.GuildID)
					if err != nil {
						return errors.New("Error updating guild group data")
					}
					return errors.New("guild data stale, please try again in a few seconds")

				}
				if membership.isModerator {
					guildID = i.GuildID
				}
			}

			isGlobalModerator, err := CheckSystemGroupMembership(ctx, db, userIDStr, GroupGlobalModerators)
			if err != nil {
				return errors.New("Error checking global moderator status")
			}

			options := i.ApplicationCommandData().Options
			target := options[0].UserValue(s)

			return d.handleProfileRequest(ctx, logger, nk, s, discordRegistry, i, target.ID, target.Username, guildID, isGlobalModerator)
		},
		"create": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options

			if member == nil {
				return simpleInteractionResponse(s, i, "this command must be used from a guild")

			}
			mode := evr.ModeArenaPrivate
			region := evr.DefaultRegion
			level := evr.LevelUnspecified
			for _, o := range options {
				switch o.Name {
				case "region":
					region = evr.ToSymbol(o.StringValue())
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

			userID, err := GetUserIDByDiscordID(ctx, db, user.ID)
			if err != nil {
				return errors.New("failed to get user ID")
			}

			logger = logger.WithFields(map[string]interface{}{
				"userID":    userID,
				"guildID":   i.GuildID,
				"region":    region.String(),
				"mode":      mode.String(),
				"level":     level.String(),
				"startTime": startTime,
			})

			label, rtt, err := d.handlePrepareMatch(ctx, logger, userID, member.User.ID, i.GuildID, region, mode, level, startTime)
			if err != nil {
				return err
			}
			rttMs := int(rtt / 1000000)
			logger.WithField("label", label).Info("Match created.")
			content := fmt.Sprintf("Reserved server (%dms ping) for `%s` session. Reservation will timeout in %d minute.\n\nhttps://echo.taxi/spark://c/%s", rttMs, label.Mode.String(), 1, strings.ToUpper(label.ID.UUID.String()))
			return simpleInteractionResponse(s, i, content)
		},
		"allocate": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options

			if member == nil {
				return simpleInteractionResponse(s, i, "this command must be used from a guild")

			}
			mode := evr.ModeArenaPrivate
			region := evr.DefaultRegion
			level := evr.LevelUnspecified
			for _, o := range options {
				switch o.Name {
				case "region":
					region = evr.ToSymbol(o.StringValue())
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

			userID, err := GetUserIDByDiscordID(ctx, db, user.ID)
			if err != nil {
				return errors.New("failed to get user ID")
			}

			logger = logger.WithFields(map[string]interface{}{
				"userID":    userID,
				"guildID":   i.GuildID,
				"region":    region.String(),
				"mode":      mode.String(),
				"level":     level.String(),
				"startTime": startTime,
			})

			label, _, err := d.handlePrepareMatch(ctx, logger, userID, member.User.ID, i.GuildID, region, mode, level, startTime)
			if err != nil {
				return err
			}

			logger.WithField("label", label).Info("Match prepared")
			return simpleInteractionResponse(s, i, fmt.Sprintf("Match prepared with label ```json\n%s\n```\nhttps://echo.taxi/spark://c/%s", label.GetLabelIndented(), strings.ToUpper(label.ID.UUID.String())))
		},
		"trigger-cv": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {

			options := i.ApplicationCommandData().Options

			if user == nil {
				return nil
			}

			target := options[0].UserValue(s)
			targetUserIDStr, err := GetUserIDByDiscordID(ctx, db, target.ID)
			if err != nil {
				return errors.New("failed to get user ID")
			}
			targetUserID := uuid.FromStringOrNil(targetUserIDStr)

			profile, err := d.profileRegistry.Load(ctx, targetUserID)
			if err != nil {
				return err
			}

			profile.TriggerCommunityValues()

			if err := d.profileRegistry.SaveAndCache(ctx, targetUserID, profile); err != nil {
				return err
			}

			cnt, err := DisconnectUserID(ctx, d.nk, targetUserIDStr)
			if err != nil {
				return err
			}

			return simpleInteractionResponse(s, i, fmt.Sprintf("Disconnected %d sessions.", cnt))
		},
		"kick-player": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			options := i.ApplicationCommandData().Options

			if user == nil {
				return nil
			}

			target := options[0].UserValue(s)
			targetUserIDStr, err := GetUserIDByDiscordID(ctx, db, target.ID)
			if err != nil {
				return errors.New("failed to get user ID")
			}

			cnt, err := DisconnectUserID(ctx, d.nk, targetUserIDStr)
			if err != nil {
				return err
			}

			return simpleInteractionResponse(s, i, fmt.Sprintf("Disconnected %d sessions. Player is required to complete *Community Values* when entering the next social lobby.", cnt))
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
				userID, err := GetUserIDByDiscordID(ctx, db, user.ID)
				if err != nil {
					return errors.New("failed to get user ID")

				}
				if ok, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalDevelopers); err != nil {
					return errors.New("failed to check group membership")
				} else if !ok {
					return errors.New("you do not have permission to use this command")
				}
			}

			// Get the metadata
			metadata, err := d.discordRegistry.GetGuildGroupMetadata(ctx, groupID)
			if err != nil {
				return errors.New("failed to get guild group metadata")

			}

			for _, o := range options {
				roleID := o.RoleValue(s, guild.ID).ID
				switch o.Name {
				case "moderator":
					metadata.ModeratorRole = roleID
				case "serverhost":
					metadata.ServerHostRole = roleID
				case "suspension":
					metadata.SuspensionRole = roleID
				case "member":
					metadata.MemberRole = roleID
				case "allocator":
					metadata.AllocatorRole = roleID
				case "is_linked":
					metadata.IsLinkedRole = roleID
				}
			}

			// Write the metadata to the group
			if err = d.discordRegistry.SetGuildGroupMetadata(ctx, groupID, metadata); err != nil {
				return errors.New("failed to set guild group metadata")

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
						Collection: MatchmakingConfigStorageCollection,
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
				if matchmakingConfig.LobbyGroupID == "" {
					return errors.New("set a group ID first with `/party group`")
				}

				//logger = logger.WithField("group_id", matchmakingConfig.GroupID)
				_, subject, err := GetLobbyGroupID(ctx, d.db, userID)
				if err != nil {
					return fmt.Errorf("failed to get party group ID: %w", err)
				}
				// Look for presences

				partyMembers, err := nk.StreamUserList(StreamModeParty, subject.String(), "", d.pipeline.node, false, true)
				if err != nil {
					return fmt.Errorf("failed to list stream users: %w", err)
				}

				// Convert the members to discord user IDs
				discordIDs := make([]string, 0, len(partyMembers))
				for _, streamUser := range partyMembers {
					discordID, err := GetDiscordIDByUserID(ctx, d.db, streamUser.GetUserId())
					if err != nil {
						return fmt.Errorf("failed to get discord ID: %w", err)
					}
					discordIDs = append(discordIDs, fmt.Sprintf("<@%s>", discordID))
				}

				// Create a list of the members
				var content string
				if len(discordIDs) == 0 {
					content = "No members in your party group."
				} else {
					content = "Members in your party group:\n" + strings.Join(discordIDs, ", ")
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

				if i.Member != nil && i.Member.User != nil {
					user = i.Member.User
				}
				options := options[0].Options
				groupName := options[0].StringValue()
				// Validate the group is 1 to 12 characters long
				if len(groupName) < 1 || len(groupName) > 12 {
					return errors.New("invalid group ID. It must be between one (1) and eight (8) characters long.")
				}
				// Validate the group is alphanumeric
				if !partyGroupIDPattern.MatchString(groupName) {
					return errors.New("invalid group ID. It must be alphanumeric.")
				}
				// Validate the group is not a reserved group
				if lo.Contains([]string{"admin", "moderator", "verified", "broadcaster"}, groupName) {
					return errors.New("invalid group ID. It is a reserved group.")
				}
				// lowercase the group
				groupName = strings.ToLower(groupName)

				objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
					{
						Collection: MatchmakingConfigStorageCollection,
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
				matchmakingConfig.LobbyGroupID = groupName
				// Store it back

				data, err := json.Marshal(matchmakingConfig)
				if err != nil {
					return fmt.Errorf("failed to marshal matchmaking config: %w", err)
				}

				if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
					{
						Collection:      MatchmakingConfigStorageCollection,
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
		"cosmetic-loadout": func(logger runtime.Logger, s *discordgo.Session, i *discordgo.InteractionCreate, user *discordgo.User, member *discordgo.Member, userID string, groupID string) error {
			if user == nil {
				return nil
			}

			options := i.ApplicationCommandData().Options

			if len(options) == 0 {
				return nil
			}

			profile, err := d.profileRegistry.Load(ctx, uuid.FromStringOrNil(userID))
			if err != nil {
				return err
			}

			switch options[0].Name {
			case "manage":
				profileName := options[0].Options[1].StringValue()
				if len(profileName) < 3 || len(profileName) > 32 {
					simpleInteractionResponse(s, i, "Invalid profile name. It must be between three (3) and thirty two (32) characters long.")
					return nil
				}
				if !cosmeticPresetPattern.MatchString(profileName) {
					simpleInteractionResponse(s, i, "Invalid profile name. It must contain only characters a-z, A-Z, 0-9, underscores, and hyphens.")
					return nil
				}

				switch options[0].Options[0].StringValue() {
				case "save":
					// limit set arbitrarily
					if len(profile.CosmeticPresets) >= 5 {
						simpleInteractionResponse(s, i, "Cannot save more than 5 loadouts.")
						return nil
					}

					profile.saveCosmeticPreset(profileName)

					if err := d.profileRegistry.SaveAndCache(ctx, uuid.FromStringOrNil(userID), profile); err != nil {
						return err
					}

					simpleInteractionResponse(s, i, fmt.Sprintf("Saved current loadout to profile `%s`", profileName))
					return nil

				case "load":
					if _, ok := profile.CosmeticPresets[profileName]; !ok {
						simpleInteractionResponse(s, i, fmt.Sprintf("Profile `%s` does not exist.", profileName))
						return nil
					}

					profile.loadCosmeticPreset(profileName)

					if err := d.profileRegistry.SaveAndCache(ctx, uuid.FromStringOrNil(userID), profile); err != nil {
						return err
					}

					simpleInteractionResponse(s, i, fmt.Sprintf("Applied loadout from profile `%s`. Please relog for changes to take effect.", profileName))
					return nil

				case "delete":
					if _, ok := profile.CosmeticPresets[profileName]; !ok {
						simpleInteractionResponse(s, i, fmt.Sprintf("Profile `%s` does not exist.", profileName))
						return nil
					}

					profile.deleteCosmeticPreset(profileName)

					if err := d.profileRegistry.SaveAndCache(ctx, uuid.FromStringOrNil(userID), profile); err != nil {
						return err
					}

					simpleInteractionResponse(s, i, fmt.Sprintf("Deleted loadout profile `%s`", profileName))
					return nil
				}

			case "list":
				if len(profile.CosmeticPresets) == 0 {
					simpleInteractionResponse(s, i, "No saved profiles.")
					return nil
				}

				responseString := "Available profiles: "

				for k := range profile.CosmeticPresets {
					responseString += fmt.Sprintf("`%s`, ", k)
				}
				responseString = responseString[:len(responseString)-2]
				simpleInteractionResponse(s, i, responseString)
				return nil
			}
			return discordgo.ErrNilState
		},
	}

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		user, _ := getScopedUserMember(i)

		appCommandName := i.ApplicationCommandData().Name

		logger := d.logger.WithFields(map[string]any{
			"discord_id":       user.ID,
			"discord_username": user.Username,
			"app_command":      appCommandName,
			"guild_id":         i.GuildID,
			"channel_id":       i.ChannelID,
		})

		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			if handler, ok := commandHandlers[appCommandName]; ok {
				err := d.handleInteractionCreate(logger, s, i, appCommandName, handler)
				if err != nil {
					logger.WithField("error", err).Error("Failed to handle interaction")
					if err := simpleInteractionResponse(s, i, err.Error()); err != nil {
						return
					}
				}
			} else {
				logger.Info("Unhandled command: %v", appCommandName)
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

	// Create a map for comparison
	registeredCommands := make(map[string]*discordgo.ApplicationCommand, 0)
	for _, command := range commands {
		registeredCommands[command.Name] = command
	}

	// Create an add and remove list
	add, remove := lo.Difference(lo.Keys(currentCommands), lo.Keys(registeredCommands))

	// Remove any commands that are not in the mainSlashCommands
	for _, name := range remove {
		command := registeredCommands[name]
		logger.Debug("Deleting %s command: %s", guildID, command.Name)
		if err := s.ApplicationCommandDelete(s.State.Application.ID, guildID, command.ID); err != nil {
			logger.WithField("err", err).Error("Failed to delete application command.")
		}
	}

	// Add any commands that are in the mainSlashCommands
	for _, name := range add {
		command := currentCommands[name]
		logger.Debug("Creating %s command: %s", guildID, command.Name)
		if _, err := s.ApplicationCommandCreate(s.State.Application.ID, guildID, command); err != nil {
			logger.WithField("err", err).Error("Failed to create application command: %s", command.Name)
		}
	}

	// Edit existing commands
	for _, command := range currentCommands {
		if registered, ok := registeredCommands[command.Name]; ok {
			command.ID = registered.ID
			if !cmp.Equal(registered, command) {
				logger.Debug("Updating %s command: %s", guildID, command.Name)
				if _, err := s.ApplicationCommandEdit(s.State.Application.ID, guildID, registered.ID, command); err != nil {
					logger.WithField("err", err).Error("Failed to edit application command: %s", command.Name)
				}
			}
		}
	}
}

func (d *DiscordAppBot) getPartyDiscordIds(ctx context.Context, discordRegistry DiscordRegistry, partyHandler *PartyHandler) (map[string]string, error) {
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
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
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
	})

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

// Function to extract the timestamp from a Discord snowflake ID
func getTimestampFromID(snowflakeID string) (time.Time, error) {
	// Convert the snowflake ID to an integer
	id, err := strconv.ParseInt(snowflakeID, 10, 64)
	if err != nil {
		return time.Time{}, err
	}

	// Extract the timestamp part of the snowflake ID
	// Discord epoch in milliseconds (January 1, 2015 00:00:00 UTC)
	timestamp := (id >> 22) + 1420070400000

	// Convert the timestamp to time.Time
	return time.Unix(0, timestamp*int64(time.Millisecond)), nil
}

func (d *DiscordAppBot) createRegionStatusEmbed(ctx context.Context, logger runtime.Logger, regionStr string, channelID string, existingMessage *discordgo.Message) error {
	// list all the matches

	matches, err := d.nk.MatchList(ctx, 100, true, "", nil, nil, "")
	if err != nil {
		return err
	}

	regionSymbol := evr.ToSymbol(regionStr)

	tracked := make([]*MatchLabel, 0, len(matches))

	for _, match := range matches {

		state := &MatchLabel{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), state); err != nil {
			logger.Error("Failed to unmarshal match label", zap.Error(err))
			continue
		}

		for _, r := range state.Broadcaster.Regions {
			if regionSymbol == r {
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
			Name:   strconv.FormatUint(state.Broadcaster.ServerID, 10),
			Value:  status,
			Inline: false,
		})
	}

	if existingMessage != nil {
		ts, err := getTimestampFromID(existingMessage.ID)
		if err != nil {
			return err
		}

		embed.Footer = &discordgo.MessageEmbedFooter{
			Text: fmt.Sprintf("Expires %s", ts.Format(time.RFC1123)),
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
