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
	"net/http"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type DiscordAppBot struct {
	sync.Mutex

	ctx      context.Context
	cancelFn context.CancelFunc

	nk       runtime.NakamaModule
	logger   runtime.Logger
	metrics  Metrics
	pipeline *Pipeline
	config   Config

	discordRegistry DiscordRegistry
	profileRegistry *ProfileRegistry
	dg              *discordgo.Session

	debugChannels map[string]string // map[groupID]channelID
	userID        string            // Nakama UserID of the bot
}

func NewDiscordAppBot(nk runtime.NakamaModule, logger runtime.Logger, metrics Metrics, pipeline *Pipeline, config Config, discordRegistry DiscordRegistry, profileRegistry *ProfileRegistry, dg *discordgo.Session) *DiscordAppBot {
	ctx, cancelFn := context.WithCancel(context.Background())
	logger = logger.WithField("system", "discordAppBot")
	return &DiscordAppBot{
		ctx:      ctx,
		cancelFn: cancelFn,

		logger:   logger,
		nk:       nk,
		pipeline: pipeline,
		metrics:  metrics,

		discordRegistry: discordRegistry,
		profileRegistry: profileRegistry,
		dg:              dg,
	}
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

	partyGroupIDPattern = regexp.MustCompile("^[a-z0-9]+$")
	vrmlIDPattern       = regexp.MustCompile("^[-a-zA-Z0-9]{24}$")

	mainSlashCommands = []*discordgo.ApplicationCommand{
		{
			Name:        "evrsymbol",
			Description: "Generate the symbol value for a token.",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Type:        discordgo.ApplicationCommandOptionString,
					Name:        "token",
					Description: "String to convert to symbol.",
					Required:    true,
				},
			},
		},
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
					Name:        "region",
					Description: "Region to allocate the session in",
					Required:    true,
				},
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
					},
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

	bot.AddHandler(func(session *discordgo.Session, ready *discordgo.Ready) {
		logger.Info("Discord bot is ready.")
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.RateLimit) {
		logger.WithField("rate_limit", m).Warn("Discord rate limit")
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.Ready) {
		// Create a user for the bot based on it's discord profile
		userID, _, _, err := nk.AuthenticateCustom(ctx, m.User.ID, s.State.User.Username, true)
		if err != nil {
			logger.Error("Error creating discordbot user: %s", err)
		}
		d.userID = userID
		// Synchronize the guilds with nakama groups
		logger.Info("Bot is in %d guilds", len(s.State.Guilds))
		for _, g := range m.Guilds {
			g, err := s.Guild(g.ID)
			if err != nil {
				logger.Error("Error getting guild: %w", err)
				return
			}

			if err := d.discordRegistry.SynchronizeGroup(ctx, g); err != nil {
				logger.Error("Error synchronizing group: %s", err.Error())
				return
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
				groupID, found := d.discordRegistry.Get(m.GuildID)
				if !found {
					return
				}
				// Remove the guild group from the system.
				err := d.nk.GroupDelete(ctx, groupID)
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
							groupID, found := d.discordRegistry.Get(m.GuildID)
							if !found {
								return
							}
							// Remove the guild group from the system.
							err := d.nk.GroupDelete(ctx, groupID)
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
				userId, err := d.discordRegistry.GetUserIdByDiscordId(ctx, suspensionStatus.UserDiscordId, true)
				if err != nil {
					logger.Error("Error getting user id: %w", err)
					return
				}
				suspensionStatus.UserId = userId.String()
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

		if groupId, found := d.discordRegistry.Get(m.GuildID); found {
			if user, err := d.discordRegistry.GetUserIdByDiscordId(ctx, m.User.ID, true); err == nil {
				nk.GroupUsersKick(ctx, SystemUserID, groupId, []string{user.String()})
			}
		}
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.GuildBanRemove) {
		if s == nil || m == nil {
			return
		}

		_, _ = d.discordRegistry.GetUserIdByDiscordId(ctx, m.User.ID, true)
	})

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.GuildMemberRemove) {
		if s == nil || m == nil {
			return
		}

		if groupId, found := d.discordRegistry.Get(m.GuildID); found {
			if user, err := d.discordRegistry.GetUserIdByDiscordId(ctx, m.User.ID, true); err == nil {
				_ = nk.GroupUserLeave(ctx, SystemUserID, groupId, user.String())
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

		discordID := e.User.ID

		// Get the guild group, or sync the group
		_, found := d.discordRegistry.Get(e.GuildID)
		if !found {
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
			_, found = d.discordRegistry.Get(e.GuildID)
			if !found {
				logger.Error("Group not found after synchronization")
				return
			}
		}

		userID, err := d.discordRegistry.GetUserIdByDiscordId(ctx, discordID, true)
		if err != nil {
			if status.Code(err) == codes.NotFound {
				return
			}
			logger.Error("Error getting user id: %s", err.Error())
			return
		}
		err = d.discordRegistry.UpdateGuildGroup(ctx, logger, userID, e.GuildID)
		if err != nil {
			logger.Error("Error updating guild group: %s", err.Error())
			return
		}

		go d.discordRegistry.UpdateAccount(context.Background(), userID)
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
		for {
			select {
			case <-updateTicker.C:
				// Get all the matches
				minSize := 2
				maxSize := MatchMaxSize + 1
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

func (d *DiscordAppBot) RegisterSlashCommands() error {
	ctx := d.ctx
	nk := d.nk

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

	commandHandlers := map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger){

		"evrsymbol": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {
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
		},

		"link-headset": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {
			options := i.ApplicationCommandData().Options
			linkCode := options[0].StringValue()
			// Validate the link code as a 4 character string
			if len(linkCode) != 4 {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: "Invalid link code. It must be four (4) characters long.",
					},
				})
			}
			disableIPVerification := false
			if len(options) > 1 {
				disableIPVerification = options[1].BoolValue()
			}

			discordId := ""
			switch {
			case i.User != nil:
				discordId = i.User.ID
			case i.Member.User != nil:
				discordId = i.Member.User.ID
			default:
				return
			}

			if err := func() error {

				// Authenticate/create an account.
				userId, err := discordRegistry.GetUserIdByDiscordId(ctx, discordId, true)
				if err != nil {
					return fmt.Errorf("failed to authenticate (or create) user %s: %w", discordId, err)
				}

				// Update the accounts roles, etc.
				go discordRegistry.UpdateAccount(context.Background(), userId)

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

				return nk.LinkDevice(ctx, userId.String(), token.Token())
			}(); err != nil {
				logger.WithFields(map[string]interface{}{
					"discord_id": discordId,
					"link_code":  linkCode,
					"error":      err,
				}).Error("Failed to link headset")
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
					Content: "Your headset has been linked. Restart EchoVR.",
				},
			})
		},
		"unlink-headset": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {
			options := i.ApplicationCommandData().Options
			deviceId := options[0].StringValue()
			// Validate the link code as a 4 character string
			var user *discordgo.User
			switch {
			case i.User != nil:
				user = i.User
			case i.Member.User != nil:
				user = i.Member.User
			default:
				return
			}

			if err := func() error {
				// Get the userid by username
				userId, err := discordRegistry.GetUserIdByDiscordId(ctx, user.ID, false)
				if err != nil {
					return fmt.Errorf("failed to authenticate (or create) user %s: %w", user.ID, err)
				}

				return nk.UnlinkDevice(ctx, userId.String(), deviceId)

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
		},
		"check-broadcaster": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {
			errFn := func(err error) {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: fmt.Sprintf("check failed: %v", err.Error()),
					},
				})
			}

			options := i.ApplicationCommandData().Options
			if len(options) == 0 {
				errFn(errors.New("no options provided"))
				return
			}
			target := options[0].StringValue()

			// 1.1.1.1[:6792[-6820]]
			parts := strings.SplitN(target, ":", 2)
			if len(parts) == 0 {
				errFn(errors.New("no address provided"))
				return
			}
			if parts[0] == "" {
				errFn(errors.New("invalid address"))
				return
			}
			// Parse the address
			remoteIP := net.ParseIP(parts[0])
			if remoteIP == nil {
				// Try resolving the hostname
				ips, err := net.LookupIP(parts[0])
				if err != nil {
					errFn(fmt.Errorf("failed to resolve address: %v", err))
					return
				}
				// Select the ipv4 address
				for _, remoteIP = range ips {
					if remoteIP.To4() != nil {
						break
					}
				}
				if remoteIP == nil {
					errFn(errors.New("failed to resolve address to an ipv4 address"))
					return

				}
			}

			// Parse the (optional) port range
			var startPort, endPort int
			var err error
			if len(parts) > 1 {
				// If a port range is specified, scan the specified range
				portRange := strings.SplitN(parts[1], "-", 2)
				if startPort, err = strconv.Atoi(portRange[0]); err != nil {
					errFn(fmt.Errorf("invalid start port: %v", err))
					return
				}
				if len(portRange) == 1 {
					// If a single port is specified, do not scan
					endPort = startPort
				} else {
					// If a port range is specified, scan the specified range
					if endPort, err = strconv.Atoi(portRange[1]); err != nil {
						errFn(fmt.Errorf("invalid end port: %v", err))
						return
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
				errFn(errors.New("invalid IP address"))
				return
			case startPort < 0:
				errFn(errors.New("start port must be greater than or equal to 0"))
				return
			case startPort > endPort:
				errFn(errors.New("start port must be less than or equal to end port"))
				return
			case endPort-startPort > 100:
				errFn(errors.New("port range must be less than or equal to 100"))
				return
			case startPort < 1024:
				errFn(errors.New("start port must be greater than or equal to 1024"))
				return
			case endPort > 65535:
				errFn(errors.New("end port must be less than or equal to 65535"))
				return
			}
			localIP, err := DetermineLocalIPAddress()
			if startPort == endPort {
				count := 5
				interval := 100 * time.Millisecond
				timeout := 500 * time.Millisecond

				if err != nil {
					errFn(fmt.Errorf("failed to determine local IP address: %v", err))
					return
				}

				// If a single port is specified, do not scan
				rtts, err := BroadcasterRTTcheck(localIP, remoteIP, startPort, count, interval, timeout)
				if err != nil {
					errFn(fmt.Errorf("failed to healthcheck broadcaster: %v", err))
					return
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
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,

						Data: &discordgo.InteractionResponseData{
							Flags:   discordgo.MessageFlagsEphemeral,
							Content: fmt.Sprintf("Broadcaster %s:%d RTTs (AVG: %.0f): %s", remoteIP, startPort, avgrtt.Seconds()*1000, rttMessage),
						},
					})
					return
				} else {
					errFn(errors.New("no response from broadcaster"))
					return
				}
			} else {

				// Scan the address for responding broadcasters and then return the results as a newline-delimited list of ip:port
				responses, _ := BroadcasterPortScan(localIP, remoteIP, 6792, 6820, 500*time.Millisecond)
				if len(responses) == 0 {
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Flags:   discordgo.MessageFlagsEphemeral,
							Content: "No broadcasters are responding.",
						},
					})
					return
				}

				// Craft a message that contains the newline-delimited list of the responding broadcasters
				var b strings.Builder
				for port, r := range responses {
					b.WriteString(fmt.Sprintf("%s:%-5d %3.0fms\n", remoteIP, port, r.Seconds()*1000))
				}

				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: fmt.Sprintf("```%s```", b.String()),
					},
				})
				return
			}
		},
		"reset-password": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {
			var user *discordgo.User
			switch {
			case i.User != nil:
				user = i.User
			case i.Member.User != nil:
				user = i.Member.User
			default:
				return
			}
			if err := func() error {
				userId, err := discordRegistry.GetUserIdByDiscordId(ctx, user.ID, true)
				if err != nil {
					return err
				}
				// Get the account
				account, err := nk.AccountGetId(ctx, userId.String())
				if err != nil {
					return err
				}
				// Clear the password
				return nk.UnlinkEmail(ctx, userId.String(), account.GetEmail())

			}(); err != nil {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: err.Error(),
					},
				})
			} else {
				// Send the response
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: "Your password has been cleared.",
					},
				})
			}
		},
		"badges": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {
			options := i.ApplicationCommandData().Options
			var err error

			user := getScopedUser(i)
			if user == nil {
				return
			}

			errFn := func(err error) {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: err.Error(),
					},
				})
			}

			switch options[0].Name {
			case "assign":
				options = options[0].Options
				// Check that the user is a developer

				var userID uuid.UUID
				userID, err = discordRegistry.GetUserIdByDiscordId(ctx, user.ID, false)
				if err != nil {
					errFn(status.Error(codes.PermissionDenied, "you do not have permission to use this command"))
					break
				}
				var member bool
				member, err = checkGroupMembershipByName(ctx, nk, userID.String(), GroupGlobalBadgeAdmins, SystemGroupLangTag)
				if err != nil {
					errFn(status.Error(codes.Internal, "failed to check group membership"))
					break
				}
				if !member {
					errFn(status.Error(codes.PermissionDenied, "you do not have permission to use this command"))
					break
				}
				if len(options) < 2 {
					errFn(status.Error(codes.InvalidArgument, "you must specify a user and a badge"))
				}
				// Get the target user's discord ID
				target := options[0].UserValue(s)
				if target == nil {
					errFn(status.Error(codes.InvalidArgument, "you must specify a user"))
					break
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
						errFn(status.Errorf(codes.InvalidArgument, fmt.Sprintf("badge `%s` not found", c)))
						break
					}

					groupID, ok := vrmlGroups[groupName]
					if !ok {
						errFn(status.Error(codes.Internal, fmt.Sprintf("badge `%s` not found (this shouldn't happen)", c)))
						break // This shouldn't happen
					}
					badgeGroups = append(badgeGroups, groupID)
				}

				userID, err := discordRegistry.GetUserIdByDiscordId(ctx, target.ID, false)
				if err != nil {
					errFn(status.Error(codes.Internal, fmt.Errorf("failed to get user `%s`: %w", target.Username, err).Error()))
					return
				}

				for _, groupID := range badgeGroups {
					// Add the user to the group

					if err = nk.GroupUsersAdd(ctx, SystemUserID, groupID, []string{userID.String()}); err != nil {
						errFn(status.Error(codes.Internal, fmt.Errorf("failed to assign badge `%s` to user `%s`: %w", groupID, target.Username, err).Error()))
						continue
					}
				}

				// Log the action
				logger.Info("assign badges", zap.String("badges", badgeCodestr), zap.String("user", target.Username), zap.String("discord_id", target.ID), zap.String("assigner", user.ID))

				// Send a message to the channel
				channel := "1232462244797874247"
				_, err = s.ChannelMessageSend(channel, fmt.Sprintf("`%s` assigned VRML cosmetics `%s` to user `%s`", user.ID, badgeCodestr, target.Username))
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
					return
				}
				vrmlUsername := options[0].StringValue()

				// Check the vlaue against vrmlIDPattern
				if !vrmlIDPattern.MatchString(vrmlUsername) {
					errFn(fmt.Errorf("invalid VRML username: `%s`", vrmlUsername))
					return
				}

				// Access the VRML HTTP API
				url := fmt.Sprintf("https://api.vrmasterleague.com/EchoArena/Players/Search?name=%s", vrmlUsername)
				var req *http.Request
				req, err = http.NewRequest("GET", url, nil)
				if err != nil {
					errFn(status.Error(codes.Internal, "failed to create request"))
					break
				}

				req.Header.Set("User-Agent", "EchoVRCE Discord Bot (contact: @sprockee)")

				// Make the request
				var resp *http.Response
				resp, err = http.DefaultClient.Do(req)
				if err != nil {
					errFn(status.Error(codes.Internal, "failed to make request"))
					break
				}

				// Parse the response as JSON...
				// [{"id":"4rPCIjBhKhGpG4uDnfHlfg2","name":"sprockee","image":"/images/logos/users/25d45af7-f6a8-40ef-a035-879a61869c8f.png"}]
				var players []struct {
					ID   string `json:"id"`
					Name string `json:"name"`
				}

				if err = json.NewDecoder(resp.Body).Decode(&players); err != nil {
					errFn(status.Error(codes.Internal, "failed to decode response: "+err.Error()))
					break
				}

				// Check if the player was found
				if len(players) == 0 {
					errFn(status.Error(codes.NotFound, "player not found"))
					break
				}

				// Ensure that only one was returned
				if len(players) > 1 {
					errFn(status.Error(codes.Internal, "multiple players found"))
					break
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
					errFn(status.Error(codes.Internal, "failed to marshal player data: "+err.Error()))
					break
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
					errFn(status.Error(codes.Internal, "failed to set VRML ID: "+err.Error()))
					break
				}

				logger.Info("set vrml id", zap.String("discord_id", user.ID), zap.String("discord_username", user.Username), zap.String("vrml_id", playerID))

				err = simpleInteractionResponse(s, i, fmt.Sprintf("set VRML username `%s` for user `%s`", vrmlUsername, user.Username))
				if err != nil {
					errFn(status.Error(codes.Internal, "failed to send response: "+err.Error()))
					break
				}
			}
		},
		"whoami": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {

			user, _ := getScopedUserMember(i)
			if user == nil {
				return
			}

			if err := d.handleProfileRequest(ctx, logger, nk, s, discordRegistry, i, user.ID, user.Username, "", true); err != nil {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: err.Error(),
					},
				})

			}
		},
		"set-lobby": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {
			if i.Type != discordgo.InteractionApplicationCommand {
				return
			}

			if i.Member == nil || i.Member.User.ID == "" || i.GuildID == "" {
				return
			}

			guildID := i.GuildID

			errFn := func(err error) {
				logger.Debug("set-lobby failed: %s", err.Error())
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: fmt.Sprintf("set failed: %v", err.Error()),
					},
				})
			}

			userID, err := d.discordRegistry.GetUserIdByDiscordId(ctx, i.Member.User.ID, true)
			if err != nil {
				logger.Error("Failed to get user ID", zap.Error(err))
				errFn(err)
				return
			}

			groupID, found := d.discordRegistry.Get(guildID)
			if !found {
				errFn(errors.New("guild group not found"))
				return
			}

			// Try to find it by searching
			memberships, err := d.discordRegistry.GetGuildGroupMemberships(ctx, userID, []uuid.UUID{uuid.FromStringOrNil(groupID)})
			if err != nil {
				errFn(err)
				return
			}
			if len(memberships) == 0 {
				err := d.discordRegistry.UpdateGuildGroup(ctx, logger, userID, guildID)
				if err != nil {
					errFn(err)
				}
				errFn(errors.New("guild data stale, please try again in a few seconds"))
				return
			}
			membership := memberships[0]

			profile, _ := d.profileRegistry.Load(userID, evr.EvrIdNil)
			profile.SetChannel(evr.GUID(membership.GuildGroup.ID()))
			if err = d.profileRegistry.Store(userID, profile); err != nil {
				errFn(err)
				return
			}

			guild, err := s.Guild(i.GuildID)
			if err != nil {
				logger.Error("Failed to get guild", zap.Error(err))
				errFn(err)
				return
			}

			d.profileRegistry.Save(userID)
			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
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
		"lookup": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {

			if i.Type != discordgo.InteractionApplicationCommand {
				return
			}
			if i.GuildID == "" {
				return
			}

			// Determine if the call is from a guild.
			user, member := getScopedUserMember(i)

			if user == nil {
				return
			}

			errFn := func(errs ...error) {
				err := errors.Join(errs...)
				if err := simpleInteractionResponse(s, i, err.Error()); err != nil {
					logger.Warn("Failed to send interaction response", zap.Error(err))
					return
				}
			}

			// Get the caller's nakama user ID
			userID, err := d.discordRegistry.GetUserIdByDiscordId(ctx, user.ID, false)
			if err != nil {
				errFn(errors.New("failed to get user ID"), err)
				return
			}

			guildID := ""
			if member != nil {
				memberships, err := d.discordRegistry.GetGuildGroupMemberships(ctx, userID, nil)
				if err != nil {
					errFn(errors.New("failed to get user ID"), err)
					return
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
						errFn(errors.New("Error updating guild group data"), err)
						return
					}
					errFn(errors.New("guild data stale, please try again in a few seconds"))
					return
				}
				if membership.isModerator {
					guildID = i.GuildID
				}
			}

			isGlobalModerator, err := d.discordRegistry.IsGlobalModerator(ctx, userID)
			if err != nil {
				errFn(errors.New("Error checking global moderator status"), err)
				return
			}

			options := i.ApplicationCommandData().Options
			target := options[0].UserValue(s)

			if err := d.handleProfileRequest(ctx, logger, nk, s, discordRegistry, i, target.ID, target.Username, guildID, isGlobalModerator); err != nil {
				errFn(errors.New("Error handling profile request"), err)
			}
		},
		"allocate": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {
			options := i.ApplicationCommandData().Options
			user, member := getScopedUserMember(i)
			if user == nil {
				return
			}
			if member == nil {
				return
			}
			if member.User == nil {
				return
			}
			region := evr.ToSymbol(options[0].StringValue())
			mode := evr.ToSymbol(options[1].StringValue())

			// validate the mode
			if _, ok := evr.LevelsByMode[mode]; !ok {
				err := fmt.Errorf("invalid mode `%s`", mode)
				simpleInteractionResponse(s, i, err.Error())
				return
			}

			var level evr.Symbol
			if len(options) >= 3 {
				level = evr.ToSymbol(options[2].StringValue())

				// validate the level
				if !slices.Contains(evr.LevelsByMode[mode], level) {
					err := fmt.Errorf("invalid level `%s` for mode `%s`", level, mode)
					simpleInteractionResponse(s, i, err.Error())
					return
				}
			}

			startTime := time.Now()

			label, err := d.handlePrepareMatch(ctx, logger, member.User.ID, i.GuildID, region, mode, level, startTime)
			if err != nil {
				simpleInteractionResponse(s, i, err.Error())
				return
			}

			simpleInteractionResponse(s, i, fmt.Sprintf("Match prepared with label ```json\n%s\n```\nhttps://echo.taxi/spark://c/%s", label.String(), strings.ToUpper(label.ID.UUID().String())))
		},
		"trigger-cv": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {

			options := i.ApplicationCommandData().Options

			user := getScopedUser(i)
			if user == nil {
				return
			}
			errFn := func(errs ...error) {
				err := errors.Join(errs...)
				logger.Debug("trigger-cv failed: %s", err.Error())
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: fmt.Sprintf("error setting community values:\n%v", err.Error()),
					},
				})
			}
			userID, err := d.discordRegistry.GetUserIdByDiscordId(ctx, user.ID, false)
			if err != nil {
				errFn(errors.New("failed to get user ID"), err)
				return
			}

			// Require the user to be a global moderator
			if isGlobalModerator, err := d.discordRegistry.IsGlobalModerator(ctx, userID); err != nil {
				errFn(errors.New("failed to check global moderator status"), err)
				return
			} else if !isGlobalModerator {
				if err := simpleInteractionResponse(s, i, "You must be a global moderator to use this command."); err != nil {
					logger.Warn("Failed to send interaction response", zap.Error(err))
				}
				return
			}

			target := options[0].UserValue(s)
			targetUserID, err := d.discordRegistry.GetUserIdByDiscordId(ctx, target.ID, false)
			if err != nil {
				errFn(errors.New("failed to get user ID"), err)
				return
			}

			profile, _ := d.profileRegistry.Load(targetUserID, evr.EvrIdNil)
			profile.TriggerCommunityValues()
			if err = d.profileRegistry.Store(targetUserID, profile); err != nil {
				errFn(err)
				return
			}

			presences, err := d.nk.StreamUserList(StreamModeEvr, targetUserID.String(), matchContext.String(), "", true, true)
			if err != nil {
				errFn(err)
				return
			}

			cnt := 0
			for _, presence := range presences {
				if err = d.nk.SessionDisconnect(ctx, presence.GetSessionId(), runtime.PresenceReasonDisconnect); err != nil {
					errFn(err)
					continue
				}
				cnt++
			}
			if err := simpleInteractionResponse(s, i, fmt.Sprintf("Disconnected %d sessions. Player is required to complete *Community Values* when entering the next social lobby.", cnt)); err != nil {
				logger.Warn("Failed to send interaction response", zap.Error(err))
			}
		},
		"set-roles": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {
			options := i.ApplicationCommandData().Options
			user := getScopedUser(i)
			if user == nil {
				return
			}
			// Ensure the user is the owner of the guild
			if i.Member == nil || i.Member.User.ID == "" || i.GuildID == "" {
				return
			}

			errFn := func(err error) {
				simpleInteractionResponse(s, i, err.Error())
			}

			guild, err := s.Guild(i.GuildID)
			if err != nil {
				errFn(errors.New("failed to get guild"))
			}

			if guild == nil {
				errFn(errors.New("failed to get guild"))
				return
			}

			if guild.OwnerID != user.ID {
				// Check if the user is a global developer
				userID, err := d.discordRegistry.GetUserIdByDiscordId(ctx, user.ID, false)
				if err != nil {
					errFn(errors.New("failed to get user ID"))
					return
				}
				member, err := checkGroupMembershipByName(ctx, nk, userID.String(), GroupGlobalDevelopers, SystemGroupLangTag)
				if err != nil {
					errFn(errors.New("failed to check group membership"))
					return
				}
				if !member {
					errFn(errors.New("you do not have permission to use this command"))
					return
				}
			}

			groupID, found := d.discordRegistry.Get(i.GuildID)
			if !found {
				errFn(errors.New("guild group not found"))
				return
			}
			// Get the metadata
			metadata, err := d.discordRegistry.GetGuildGroupMetadata(ctx, groupID)
			if err != nil {
				errFn(errors.New("failed to get guild group metadata"))
				return
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
				}
			}

			// Write the metadata to the group
			if err = d.discordRegistry.SetGuildGroupMetadata(ctx, groupID, metadata); err != nil {
				errFn(errors.New("failed to set guild group metadata"))
				return
			}
			simpleInteractionResponse(s, i, "roles set!")
		},

		"region-status": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {
			options := i.ApplicationCommandData().Options

			user := getScopedUser(i)
			if user == nil {
				return
			}
			errFn := func(err error) {
				simpleInteractionResponse(s, i, err.Error())
			}

			regionStr := options[0].StringValue()
			if regionStr == "" {
				errFn(errors.New("no region provided"))
			}

			if err := d.createRegionStatusEmbed(ctx, logger, regionStr, i.Interaction.ChannelID, nil); err != nil {
				errFn(err)
			}

		},
		"party": func(s *discordgo.Session, i *discordgo.InteractionCreate, logger runtime.Logger) {

			if i.Type != discordgo.InteractionApplicationCommand {
				return
			}
			user, _ := getScopedUserMember(i)

			options := i.ApplicationCommandData().Options
			switch options[0].Name {
			case "invite":
				options := options[0].Options
				inviter := i.User
				invitee := options[0].UserValue(s)

				if err := d.sendPartyInvite(ctx, s, i, inviter, invitee); err != nil {
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Flags:   discordgo.MessageFlagsEphemeral,
							Content: err.Error(),
						},
					})
				}
			case "members":
				logger := logger.WithField("discord_id", user.ID)
				// List the other players in this party group
				user := getScopedUser(i)
				if user == nil {
					return
				}
				// Get the user's party group
				// Get the userID
				userID, err := d.discordRegistry.GetUserIdByDiscordId(ctx, user.ID, false)
				if err != nil {
					logger.Error("Failed to get user ID", zap.Error(err))
					return
				}

				// Make sure the user is online
				count, err := nk.StreamCount(StreamModeEvr, userID.String(), StreamContextMatch.String(), "")
				if err != nil {
					logger.Error("Failed to get user presence", zap.Error(err))
					return
				}
				if count == 0 {
					if err := simpleInteractionResponse(s, i, "You must be online (in a social lobby or match) to use this command."); err != nil {
						logger.Warn("Failed to send interaction response", zap.Error(err))
					}
				}

				objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
					{
						Collection: MatchmakingConfigStorageCollection,
						Key:        MatchmakingConfigStorageKey,
						UserID:     userID.String(),
					},
				})
				if err != nil {
					logger.Error("Failed to read matchmaking config", zap.Error(err))
				}
				matchmakingConfig := &MatchmakingSettings{}
				if len(objs) != 0 {
					if err := json.Unmarshal([]byte(objs[0].Value), matchmakingConfig); err != nil {
						logger.Error("Failed to unmarshal matchmaking config", zap.Error(err))
						return
					}
				}
				if matchmakingConfig.GroupID == "" {
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Flags:   discordgo.MessageFlagsEphemeral,
							Content: "You do not have a party groupID set.",
						},
					})
					return
				}

				logger = logger.WithField("group_id", matchmakingConfig.GroupID)

				// Look for presences
				partyID := uuid.NewV5(uuid.Nil, matchmakingConfig.GroupID).String()
				streamUsers, err := nk.StreamUserList(StreamModeParty, partyID, "", d.pipeline.node, true, true)
				if err != nil {
					logger.Error("Failed to list party members", zap.Error(err))
					return
				}

				// Convert the members to discord user IDs
				discordIds := make([]string, 0, len(streamUsers))
				for _, streamUser := range streamUsers {
					discordId, err := d.discordRegistry.GetDiscordIdByUserId(ctx, uuid.FromStringOrNil(streamUser.GetUserId()))
					if err != nil {
						logger.Error("Failed to get discord ID", zap.Error(err))
						return
					}
					discordIds = append(discordIds, fmt.Sprintf("<@%s>", discordId))
				}

				// Create a list of the members
				var content string
				if len(discordIds) == 0 {
					content = "No members in your party group."
				} else {
					content = "Members in your party group:\n" + strings.Join(discordIds, ", ")
				}

				// Send the message to the user
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: content,
					},
				})

			case "group":
				user := i.User
				if i.Member != nil && i.Member.User != nil {
					user = i.Member.User
				}
				options := options[0].Options
				groupID := options[0].StringValue()
				// Validate the group is 1 to 12 characters long
				if len(groupID) < 1 || len(groupID) > 12 {
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Flags:   discordgo.MessageFlagsEphemeral,
							Content: "Invalid group ID. It must be between one (1) and eight (8) characters long.",
						},
					})
				}
				// Validate the group is alphanumeric
				if !partyGroupIDPattern.MatchString(groupID) {
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Flags:   discordgo.MessageFlagsEphemeral,
							Content: "Invalid group ID. It must be alphanumeric.",
						},
					})
				}
				// Validate the group is not a reserved group
				if lo.Contains([]string{"admin", "moderator", "verified", "broadcaster"}, groupID) {
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Flags:   discordgo.MessageFlagsEphemeral,
							Content: "Invalid group ID. It is a reserved group.",
						},
					})
				}
				// lowercase the group
				groupID = strings.ToLower(groupID)

				// Get the userID
				userID, err := discordRegistry.GetUserIdByDiscordId(ctx, user.ID, true)
				if err != nil {
					logger.Error("Failed to get user ID", zap.Error(err))
					return
				}

				objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
					{
						Collection: MatchmakingConfigStorageCollection,
						Key:        MatchmakingConfigStorageKey,
						UserID:     userID.String(),
					},
				})
				if err != nil {
					logger.Error("Failed to read matchmaking config", zap.Error(err))
				}
				matchmakingConfig := &MatchmakingSettings{}
				if len(objs) != 0 {
					if err := json.Unmarshal([]byte(objs[0].Value), matchmakingConfig); err != nil {
						logger.Error("Failed to unmarshal matchmaking config", zap.Error(err))
						return
					}
				}
				matchmakingConfig.GroupID = groupID
				// Store it back

				data, err := json.Marshal(matchmakingConfig)
				if err != nil {
					logger.Error("Failed to marshal matchmaking config", zap.Error(err))
					return
				}

				if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{
					{
						Collection:      MatchmakingConfigStorageCollection,
						Key:             MatchmakingConfigStorageKey,
						UserID:          userID.String(),
						Value:           string(data),
						PermissionRead:  1,
						PermissionWrite: 0,
					},
				}); err != nil {
					logger.Error("Failed to write matchmaking config", zap.Error(err))
					return
				}

				// Inform the user of the groupid
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: fmt.Sprintf("Your group ID has been set to `%s`. Everyone must matchmake at the same time (~15-30 seconds)", groupID),
					},
				})
			}
		},
	}

	dg.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {
		user, _ := getScopedUserMember(i)
		logFields := make(map[string]any, 0)
		if i.GuildID != "" {
			logFields["guild_id"] = i.GuildID
		}
		if i.ChannelID != "" {
			logFields["channel_id"] = i.ChannelID
		}
		if user != nil && user.ID != "" {
			logFields["discord_id"] = user.ID
		}

		appCommandName := i.ApplicationCommandData().Name
		logger := d.logger

		if len(logFields) > 0 {
			logger = logger.WithFields(logFields)
		}

		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			if h, ok := commandHandlers[appCommandName]; ok {
				h(s, i, logger)
			} else {
				logger.Info("Unhandled command: %v", appCommandName)
			}
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
	leaderID, err := discordRegistry.GetDiscordIdByUserId(ctx, uuid.FromStringOrNil(partyHandler.leader.UserPresence.GetUserId()))
	if err != nil {
		return nil, err
	}
	memberMap[leaderID] = partyHandler.leader.UserPresence.GetUserId()

	for _, presence := range partyHandler.members.presences {
		if presence.UserPresence.GetUserId() == partyHandler.leader.UserPresence.GetUserId() {
			continue
		}
		discordId, err := discordRegistry.GetDiscordIdByUserId(ctx, uuid.FromStringOrNil(presence.UserPresence.UserId))
		if err != nil {
			return nil, err
		}
		memberMap[discordId] = presence.UserPresence.UserId
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

func (d *DiscordAppBot) handleProfileRequest(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, s *discordgo.Session, discordRegistry DiscordRegistry, i *discordgo.InteractionCreate, discordID string, username string, guildID string, includePrivate bool) error {
	whoami := &WhoAmI{
		DiscordID:             discordID,
		EVRIDLogins:           make(map[string]time.Time),
		DisplayNames:          make([]string, 0),
		ClientAddresses:       make([]string, 0),
		DeviceLinks:           make([]string, 0),
		GuildGroupMemberships: make([]GuildGroupMembership, 0),
		MatchIDs:              make([]string, 0),
	}
	// Get the user's ID
	userID, err := discordRegistry.GetUserIdByDiscordId(ctx, discordID, true)
	if err != nil {
		return fmt.Errorf("failed to authenticate (or create) user %s: %w", discordID, err)
	}

	if userID == uuid.Nil { // assertion
		logger.Error("Failed to get or create an account.")
		return fmt.Errorf("failed to get or create an account for %s (%s)", discordID, username)
	}

	if includePrivate {
		// Do some profile checks and cleanups

		// Remove the cache entries for this user
		discordRegistry.ClearCache(discordID)

		// Synchronize the user's guilds with nakama groups
		err = d.discordRegistry.UpdateGuildGroup(ctx, logger, userID, i.GuildID)
		if err != nil {
			return fmt.Errorf("error updating guild groups: %v", err)
		}
	}

	// Basic account details
	whoami.NakamaID = userID

	account, err := nk.AccountGetId(ctx, whoami.NakamaID.String())
	if err != nil {
		return err
	}

	whoami.Username = account.GetUser().GetUsername()

	if account.GetUser().GetCreateTime() != nil {
		whoami.CreateTime = account.GetUser().GetCreateTime().AsTime().UTC()
	}

	// Get the device links from the account
	whoami.DeviceLinks = make([]string, 0, len(account.GetDevices()))
	for _, device := range account.GetDevices() {
		whoami.DeviceLinks = append(whoami.DeviceLinks, fmt.Sprintf("`%s`", device.GetId()))
	}

	whoami.HasPassword = account.GetEmail() != ""

	var groupIDs []uuid.UUID
	if guildID != "" {
		groupID, found := d.discordRegistry.Get(guildID)
		if !found {
			return fmt.Errorf("guild group not found")
		}
		groupIDs = []uuid.UUID{uuid.FromStringOrNil(groupID)}
	}

	whoami.GuildGroupMemberships, err = d.discordRegistry.GetGuildGroupMemberships(ctx, userID, groupIDs)
	if err != nil {
		return err
	}

	evrIDRecords, err := GetEVRRecords(ctx, logger, nk, userID.String())
	if err != nil {
		return err
	}

	whoami.EVRIDLogins = make(map[string]time.Time, len(evrIDRecords))

	for evrID, record := range evrIDRecords {
		whoami.EVRIDLogins[evrID.String()] = record.UpdateTime.UTC()
	}

	// Get the past displayNames
	displayNameObjs, err := GetDisplayNameRecords(ctx, logger, nk, userID.String())
	if err != nil {
		return err
	}
	// Sort displayNames by age
	sort.SliceStable(displayNameObjs, func(i, j int) bool {
		return displayNameObjs[i].GetUpdateTime().AsTime().After(displayNameObjs[j].GetUpdateTime().AsTime())
	})

	whoami.DisplayNames = make([]string, 0, len(displayNameObjs))
	for _, dn := range displayNameObjs {
		whoami.DisplayNames = append(whoami.DisplayNames, dn.Key)
	}

	// Get the MatchIDs for the user from it's presence
	presences, err := nk.StreamUserList(StreamModeEvr, userID.String(), StreamContextMatch.String(), "", true, true)
	if err != nil {
		return err
	}
	whoami.MatchIDs = make([]string, 0, len(presences))
	for _, p := range presences {
		if p.GetStatus() != "" {
			m := p.GetStatus()
			mid := MatchIDFromStringOrNil(m)
			if mid.IsNil() {
				continue
			}
			whoami.MatchIDs = append(whoami.MatchIDs, mid.UUID().String())
		}
	}

	// Get the suspensions
	dr := d.discordRegistry.(*LocalDiscordRegistry)
	suspensions, err := dr.GetAllSuspensions(ctx, userID)
	if err != nil {
		return err
	}

	suspensionLines := make([]string, 0, len(suspensions))
	for _, suspension := range suspensions {
		suspensionLines = append(suspensionLines, fmt.Sprintf("%s: %s", suspension.GuildName, suspension.RoleName))
	}
	if !includePrivate {
		whoami.HasPassword = false
		whoami.ClientAddresses = nil
		whoami.DeviceLinks = nil
		whoami.EVRIDLogins = nil
		if guildID == "" {
			whoami.GuildGroupMemberships = nil
		}
		whoami.MatchIDs = nil
	}
	fields := []*discordgo.MessageEmbedField{
		{Name: "Nakama ID", Value: whoami.NakamaID.String(), Inline: true},
		{Name: "Create Time", Value: fmt.Sprintf("<t:%d:R>", whoami.CreateTime.Unix()), Inline: false},
		{Name: "Password Protected", Value: func() string {
			if whoami.HasPassword {
				return "Yes"
			}
			return ""
		}(), Inline: true},
		{Name: "Discord ID", Value: whoami.DiscordID, Inline: false},
		{Name: "Username", Value: whoami.Username, Inline: true},
		{Name: "Display Names", Value: strings.Join(whoami.DisplayNames, "\n"), Inline: false},
		{Name: "Linked Devices", Value: strings.Join(whoami.DeviceLinks, "\n"), Inline: false},
		{Name: "Logins", Value: func() string {
			lines := lo.MapToSlice(whoami.EVRIDLogins, func(k string, v time.Time) string {
				return fmt.Sprintf("<t:%d:R> - %s", v.Unix(), k)
			})
			slices.Sort(lines)
			slices.Reverse(lines)
			return strings.Join(lines, "\n")
		}(), Inline: false},
		{Name: "Guild Memberships", Value: strings.Join(lo.Map(whoami.GuildGroupMemberships, func(m GuildGroupMembership, index int) string {
			s := m.GuildGroup.Name()
			roles := make([]string, 0)
			if m.isMember {
				roles = append(roles, "member")
			}
			if m.isModerator {
				roles = append(roles, "moderator")
			}
			if m.isServerHost {
				roles = append(roles, "server-host")
			}
			if m.canAllocate {
				roles = append(roles, "allocator")
			}
			if len(roles) > 0 {
				s += fmt.Sprintf(" (%s)", strings.Join(roles, ", "))
			}
			return s
		}), "\n"), Inline: false},
		{Name: "Suspensions", Value: strings.Join(suspensionLines, "\n"), Inline: false},
		{Name: "Current Match(es)", Value: strings.Join(lo.Map(whoami.MatchIDs, func(m string, index int) string {
			return fmt.Sprintf("https://echo.taxi/spark://c/%s", strings.ToUpper(m))
		}), "\n"), Inline: false},
	}

	// Remove any blank fields
	fields = lo.Filter(fields, func(f *discordgo.MessageEmbedField, _ int) bool {
		return f.Value != ""
	})

	// Send the response
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
			Embeds: []*discordgo.MessageEmbed{
				{
					Title:  "EchoVRCE Account",
					Color:  0xCCCCCC,
					Fields: fields,
				},
			},
		},
	})
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

func (d *DiscordAppBot) getLoginSessionForUser(ctx context.Context, discordId string, discordRegistry DiscordRegistry, pipeline *Pipeline) (uid uuid.UUID, sessionID uuid.UUID, err error) {
	// Get the userId for this inviter
	uid, err = discordRegistry.GetUserIdByDiscordId(ctx, discordId, true)
	if err != nil {
		return uuid.Nil, uuid.Nil, fmt.Errorf("failed to get user id from discord id: %w", err)
	}

	// Get existing login session for the user
	presenceIDs := pipeline.tracker.ListPresenceIDByStream(PresenceStream{
		Mode:       StreamModeEvr,
		Subject:    uid,
		Subcontext: svcLoginID,
	})
	if len(presenceIDs) == 0 {
		return uid, uuid.Nil, fmt.Errorf("<@%s> must be logged into EchoVR to party up", discordId)
	}
	// Get the invitee's session id
	sessionID = presenceIDs[0].SessionID

	return uid, sessionID, nil
}

func (d *DiscordAppBot) getOrCreateParty(ctx context.Context, pipeline *Pipeline, discordRegistry DiscordRegistry, userID uuid.UUID, username string, sessionID uuid.UUID, leaderID string) (partyHandler *PartyHandler, err error) {
	partyRegistry := pipeline.partyRegistry.(*LocalPartyRegistry)

	// Check if this user is already in a party
	presences := pipeline.tracker.ListByStream(PresenceStream{
		Mode:  StreamModeParty,
		Label: pipeline.node,
	}, true, true)

	// Loop over the presences to find the partyId
	// TODO FIXME This is really inefficient
	for _, presence := range presences {
		// Check if this is the user and if so, get the party ID
		if presence.UserID == userID {
			partyID := presence.Stream.Subject
			ph, found := partyRegistry.parties.Load(partyID)
			if !found {
				return nil, fmt.Errorf("failed to find party")
			}
			// Party found
			return ph, nil
		}
	}

	// Create a new party
	presence := &rtapi.UserPresence{
		UserId:    userID.String(),
		SessionId: sessionID.String(),
		Username:  username,
	}
	ph := partyRegistry.Create(false, 15, presence)
	return ph, nil
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

func (d *DiscordAppBot) handlePrepareMatch(ctx context.Context, logger runtime.Logger, discordID, guildID string, region, mode, level evr.Symbol, startTime time.Time) (*EvrMatchState, error) {
	userID, err := d.discordRegistry.GetUserIdByDiscordId(ctx, discordID, false)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "user not found: %s", discordID)
	}

	// Find a parking match to prepare
	minSize := 1
	maxSize := 1

	groupID, found := d.discordRegistry.Get(guildID)
	if !found {
		return nil, status.Errorf(codes.NotFound, "guild not found: %s", guildID)
	}

	if region.IsNil() {
		region = evr.ToSymbol("default")
	}
	// Get a list of the groups that this user has moderator access to
	memberships, err := d.discordRegistry.GetGuildGroupMemberships(ctx, userID, nil)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to get guild group memberships: %v", err)
	}

	groupIDs := make([]string, 0, len(memberships))
	for _, membership := range memberships {
		if !membership.canAllocate {
			continue
		}
		groupIDs = append(groupIDs, membership.GuildGroup.ID().String())
	}

	query := fmt.Sprintf("+label.lobby_type:unassigned label.broadcaster.group_ids:/(%s)/^10 +label.broadcaster.group_ids:/(%s)/ +label.broadcaster.regions:%s", groupID, strings.Join(groupIDs, "|"), region.Token().String())
	matches, err := d.nk.MatchList(ctx, 100, true, "", &minSize, &maxSize, query)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list matches: %v", err)
	}

	if len(matches) == 0 {
		return nil, status.Error(codes.NotFound, "no matches found")
	}

	// Pick a random result
	match := matches[rand.Intn(len(matches))]
	matchID := MatchIDFromStringOrNil(match.GetMatchId())
	gid := uuid.FromStringOrNil(groupID)
	// Prepare the session for the match.
	state := &EvrMatchState{}
	state.SpawnedBy = userID.String()
	state.StartTime = startTime
	state.MaxSize = MatchMaxSize
	state.Mode = mode
	state.Open = true
	state.GroupID = &gid
	if !level.IsNil() {
		state.Level = level
	}
	nk_ := d.nk.(*RuntimeGoNakamaModule)
	response, err := SignalMatch(ctx, nk_.matchRegistry, matchID, SignalPrepareSession, state)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to signal match: %v", err)
	}

	label := EvrMatchState{}
	if err := json.Unmarshal([]byte(response), &label); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to unmarshal match label: %v", err)
	}

	return &label, nil

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

	tracked := make([]*EvrMatchState, 0, len(matches))

	for _, match := range matches {

		state := &EvrMatchState{}
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
			if !state.Started {
				spawnedBy := "unknown"
				if state.SpawnedBy != "" {
					spawnedBy, err = d.discordRegistry.GetDiscordIdByUserId(ctx, uuid.FromStringOrNil(state.SpawnedBy))
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
