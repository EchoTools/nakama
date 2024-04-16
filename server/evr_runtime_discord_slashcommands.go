package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/google/go-cmp/cmp"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/timestamppb"
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
	dg              *discordgo.Session
}

func NewDiscordAppBot(nk runtime.NakamaModule, logger runtime.Logger, metrics Metrics, pipeline *Pipeline, config Config, discordRegistry DiscordRegistry, dg *discordgo.Session) *DiscordAppBot {
	ctx, cancelFn := context.WithCancel(context.Background())
	return &DiscordAppBot{
		ctx:      ctx,
		cancelFn: cancelFn,

		logger:   logger,
		nk:       nk,
		pipeline: pipeline,
		metrics:  metrics,

		discordRegistry: discordRegistry,
		dg:              dg,
	}
}

type WhoAmI struct {
	Username     string        `json:"username"`
	NakamaId     string        `json:"nakama_id"`
	DiscordId    string        `json:"discord_id"`
	CreateTime   string        `json:"create_time,omitempty"`
	DisableTime  string        `json:"disable_time,omitempty"`
	VerifyTime   string        `json:"verify_time,omitempty"`
	DisplayName  string        `json:"display_name"`
	DevicesCount int           `json:"device_count"`
	DiscordLink  bool          `json:"discord_link,omitempty"`
	DeviceLinks  []string      `json:"device_links,omitempty"`
	HasPassword  bool          `json:"has_password"`
	EvrLogins    []EvrIdLogins `json:"last_logins"`
	Groups       []string      `json:"groups"`
	Online       bool          `json:"online"`
	Addresses    []string      `json:"addresses,omitempty"`
}
type EvrIdLogins struct {
	EvrId         string `json:"evr_id"`
	LastLoginTime string `json:"login_time"`
	DisplayName   string `json:"display_name,omitempty"`
}

var (
	vrmlGroupChoices = []*discordgo.ApplicationCommandOptionChoice{
		{Name: "Preseason", Value: "Preseason"},
		{Name: "VRML S1 Champion", Value: "VRML S1 Champion"},
		{Name: "VRML S1 Finalist", Value: "VRML S1 Finalist"},
		{Name: "VRML S1", Value: "VRML S1"},
		{Name: "VRML S2 Champion", Value: "VRML S2 Champion"},
		{Name: "VRML S2 Finalist", Value: "VRML S2 Finalist"},
		{Name: "VRML S2", Value: "VRML S2"},
		{Name: "VRML S3 Champion", Value: "VRML S3 Champion"},
		{Name: "VRML S3 Finalist", Value: "VRML S3 Finalist"},
		{Name: "VRML S3", Value: "VRML S3"},
		{Name: "VRML S4", Value: "VRML S4"},
		{Name: "VRML S4 Finalist", Value: "VRML S4 Finalist"},
		{Name: "VRML S4 Champion", Value: "VRML S4 Champion"},
		{Name: "VRML S5", Value: "VRML S5"},
		{Name: "VRML S5 Finalist", Value: "VRML S5 Finalist"},
		{Name: "VRML S5 Champion", Value: "VRML S5 Champion"},
		{Name: "VRML S6", Value: "VRML S6"},
		{Name: "VRML S6 Finalist", Value: "VRML S6 Finalist"},
		{Name: "VRML S6 Champion", Value: "VRML S6 Champion"},
		{Name: "VRML S7", Value: "VRML S7"},
		{Name: "VRML S7 Finalist", Value: "VRML S7 Finalist"},
		{Name: "VRML S7 Champion", Value: "VRML S7 Champion"},
	}

	groupRegex = regexp.MustCompile("^[a-z0-9]+$")

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
			Name:        "badges",
			Description: "manage badge entitlements",
			Options: []*discordgo.ApplicationCommandOption{
				{
					Name:        "request",
					Description: "request badge(s)",
					Type:        discordgo.ApplicationCommandOptionSubCommand,
					Options: []*discordgo.ApplicationCommandOption{
						// Also, subcommand groups aren't capable of
						// containing options, by the name of them, you can see
						// they can only contain subcommands
						{
							Type:        discordgo.ApplicationCommandOptionString,
							Name:        "title",
							Description: "Badge to request",
							Required:    true,
							Choices:     vrmlGroupChoices,
						},
					},
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

	bot.AddHandler(func(s *discordgo.Session, m *discordgo.Ready) {
		// Create a user for the bot based on it's discord profile
		_, _, _, err := nk.AuthenticateCustom(ctx, m.User.ID, s.State.User.Username, true)
		if err != nil {
			logger.Error("Error creating discordbot user: %s", err)
		}

		// Synchronize the guilds with nakama groups
		logger.Info("Bot is in %d guilds", len(s.State.Guilds))
		for _, g := range m.Guilds {
			g, err := s.Guild(g.ID)
			if err != nil {
				logger.Error("Error getting guild: %w", err)
				return
			}

			if err := d.discordRegistry.SynchronizeGroup(ctx, g); err != nil {
				logger.Error("Error synchronizing group: %w", err)
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
				nk.GroupUsersKick(ctx, SystemUserId, groupId, []string{user.String()})
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
				_ = nk.GroupUserLeave(ctx, SystemUserId, groupId, user.String())
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

		// Update the user's guild group

		// TODO FIXME Make this only update what changed.
		userID, err := d.discordRegistry.GetUserIdByDiscordId(ctx, discordID, true)
		if err != nil {
			logger.Error("Error getting user id: %w", err)
			return
		}
		err = d.discordRegistry.UpdateGuildGroup(ctx, logger, userID, e.GuildID, discordID)
		if err != nil {
			logger.Error("Error updating guild group: %w", err)
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

	bot.AddHandler(func(s *discordgo.Session, e *discordgo.Ready) {
		if err := d.RegisterSlashCommands(); err != nil {
			logger.Error("Failed to register slash commands: %w", err)
		}
	})
	return nil
}
func (d *DiscordAppBot) UnregisterCommandsAll(ctx context.Context, logger runtime.Logger, dg *discordgo.Session) {
	guilds, err := dg.UserGuilds(100, "", "")
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
	logger := d.logger
	dg := d.dg
	discordRegistry := d.discordRegistry

	// Build a map of VRML group names to their group IDs
	vrmlGroups := make(map[string]string)
	for _, group := range vrmlGroupChoices {
		// Look up the group by name
		groups, _, err := nk.GroupsList(ctx, group.Name, "", nil, nil, 1, "")
		if err != nil {
			logger.Error("Error looking up group", zap.Error(err))
			continue
		}
		if len(groups) == 0 {
			logger.Error("Group not found", zap.String("name", group.Name))
			continue
		}
		vrmlGroups[group.Name] = groups[0].Id
	}

	commandHandlers := map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
		"evrsymbol": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			options := i.ApplicationCommandData().Options
			token := options[0].StringValue()
			symbol := evr.ToSymbol(token)
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
									Value:  symbol.Token().String(),
									Inline: false,
								},
								{
									Name:   "cached?",
									Value:  strconv.FormatBool(lo.Contains(lo.Keys(evr.SymbolCache), uint64(evr.Symbol(symbol)))),
									Inline: false,
								},
								{
									Name:   "hex byte array",
									Value:  fmt.Sprintf("%#v", []byte(symbol.Token())),
									Inline: false,
								},
							},
						},
					},
				},
			})
		},

		"link-headset": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
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
				return nk.LinkDevice(ctx, userId.String(), token)
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
					Content: "Your headset has been linked. Restart EchoVR.",
				},
			})
		},
		"unlink-headset": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
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
		"check-broadcaster": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
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
		"reset-password": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
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
		"badgesoff": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			options := i.ApplicationCommandData().Options
			content := ""
			switch options[0].Name {
			case "request":
				options = options[0].Options
				badgeNames := make([]string, len(options[1:]))
				for _, v := range options[1:] {
					badgeNames = append(badgeNames, v.StringValue())
				}

				// Get the user's discord ID
				user := getScopedUser(i)
				if user == nil {
					return
				}

				rows := make([]discordgo.MessageComponent, 0, len(badgeNames))

				for _, badgeName := range badgeNames {
					groupID, ok := vrmlGroups[badgeName]
					if !ok {
						continue
					}

					fdadd := strings.Join([]string{"fd_badge_add", user.ID, groupID}, ":")
					fdremove := strings.Join([]string{"fd_badge_remove", user.ID, groupID}, ":")

					row := discordgo.ActionsRow{
						Components: []discordgo.MessageComponent{
							discordgo.Button{
								Label:    badgeName,
								Style:    discordgo.SuccessButton,
								CustomID: fdadd,
							},
							discordgo.Button{
								Label:    "Remove",
								Style:    discordgo.DangerButton,
								CustomID: fdremove,
							},
						},
					}

					rows = append(rows, row)
				}

				badgeChannel := "1228721641375138018"
				s.ChannelMessageSendComplex(badgeChannel, &discordgo.MessageSend{
					Content:    fmt.Sprintf("<@%s> (`%s`) is requesting the following badges:", user.ID, user.Username),
					Components: rows,
				})

				// Message the User
				content = "Requesting badges for:" + strings.Join(badgeNames, ", ")
				// Respond to the user

			default:
				content = "Oops, something went wrong.\n" +
					"Hol' up, you aren't supposed to see this message."
			}
			if content == "" {
				return
			}

			s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
				Type: discordgo.InteractionResponseChannelMessageWithSource,
				Data: &discordgo.InteractionResponseData{
					Flags:   discordgo.MessageFlagsEphemeral,
					Content: content,
				},
			})
		},
		"whoami": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
			var user *discordgo.User
			switch {
			case i.User != nil:
				user = i.User
			case i.Member.User != nil:
				user = i.Member.User
			default:
				return
			}

			if err := d.handleProfileRequest(ctx, logger, nk, s, discordRegistry, i, user.ID, user.Username, true); err != nil {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: err.Error(),
					},
				})

			}
		},
		"lookup": func(s *discordgo.Session, i *discordgo.InteractionCreate) {

			if i.Type != discordgo.InteractionApplicationCommand {
				return
			}
			user := getScopedUser(i)

			// Check if the user is part of the Global Moderators group
			isModerator, isGlobal, err := d.discordRegistry.isModerator(ctx, i.GuildID, user.ID)
			if err != nil {
				logger.Error("Failed to check if user is a moderator", zap.Error(err))
				return
			}

			if !isModerator && !isGlobal {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: "You do not have permission to use this command.",
					},
				})
				return
			}

			options := i.ApplicationCommandData().Options
			target := options[0].UserValue(s)
			if err := d.handleProfileRequest(ctx, logger, nk, s, discordRegistry, i, target.ID, target.Username, isGlobal); err != nil {
				s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
					Type: discordgo.InteractionResponseChannelMessageWithSource,
					Data: &discordgo.InteractionResponseData{
						Flags:   discordgo.MessageFlagsEphemeral,
						Content: err.Error(),
					},
				})
			}
		},
		"party": func(s *discordgo.Session, i *discordgo.InteractionCreate) {

			if i.Type != discordgo.InteractionApplicationCommand {
				return
			}
			user := i.User
			if user == nil {
				if i.Member != nil && i.Member.User != nil {
					user = i.Member.User
				} else {
					return
				}

			}

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
				matchmakingConfig := &MatchmakingConfig{}
				if len(objs) != 0 {
					if err := json.Unmarshal([]byte(objs[0].Value), matchmakingConfig); err != nil {
						logger.Error("Failed to unmarshal matchmaking config", zap.Error(err))
						return
					}
				}
				logger = logger.WithField("group_id", matchmakingConfig.GroupID)

				// Query the storage index
				query := "+group_id:" + matchmakingConfig.GroupID
				var members []string

				idxobjs, err := nk.StorageIndexList(ctx, SystemUserId, ActivePartyGroupIndex, query, 1000)
				if err != nil {
					logger.Error("Failed to list party members", zap.Error(err))
					return
				}
				for _, obj := range idxobjs.GetObjects() {
					members = append(members, obj.UserId)
				}

				// Convert the members to discord user IDs
				discordIds := make([]string, 0, len(members))
				for _, member := range members {
					discordId, err := d.discordRegistry.GetDiscordIdByUserId(ctx, uuid.FromStringOrNil(member))
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
				if !groupRegex.MatchString(groupID) {
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
				matchmakingConfig := &MatchmakingConfig{}
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
		logger.Info("Received interaction: %s", i.ApplicationCommandData().Name)
		switch i.Type {
		case discordgo.InteractionApplicationCommand:
			if h, ok := commandHandlers[i.ApplicationCommandData().Name]; ok {
				h(s, i)
			} else {
				logger.Info("Unhandled command: %v", i.ApplicationCommandData().Name)
			}
		}
	})

	logger.Info("Registering slash commands.")
	// Register global guild commands
	d.updateSlashCommands(dg, logger, "")
	logger.Info("%d Slash commands registered/updated in %d guilds.", len(mainSlashCommands), len(dg.State.Guilds))

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
			logger.WithField("err", err).Error("Failed to create application command.")
		}
	}

	// Edit existing commands
	for _, command := range currentCommands {
		if registered, ok := registeredCommands[command.Name]; ok {
			command.ID = registered.ID
			if !cmp.Equal(registered, command) {
				logger.Debug("Updating %s command: %s", guildID, command.Name)
				if _, err := s.ApplicationCommandEdit(s.State.Application.ID, guildID, registered.ID, command); err != nil {
					logger.WithField("err", err).Error("Failed to edit application command.")
				}
			}
		}
	}
}

/*
// TODO FIXME put this as part of a bot.

	func (d *DiscordAppBot) RegisterPartySlashCommands() error {
		bot := d.dg
		logger := d.logger
		nk := d.nk
		ctx := d.ctx
		pipeline := d.pipeline
		partyCommandHandlers := map[string]func(s *discordgo.Session, i *discordgo.InteractionCreate){
			"party": func(s *discordgo.Session, i *discordgo.InteractionCreate) {
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				if i.Type != discordgo.InteractionApplicationCommand {
					return
				}
				var user *discordgo.User
				switch {
				case i.User != nil:
					user = i.User
				case i.Member.User != nil:
					user = i.Member.User
				default:
					return
				}
				options := i.ApplicationCommandData().Options
				switch options[0].Name {
				case "invite":
					options := options[0].Options
					inviter := user
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
					matchmakingConfig := &MatchmakingConfig{}
					if len(objs) != 0 {
						if err := json.Unmarshal([]byte(objs[0].Value), matchmakingConfig); err != nil {
							logger.Error("Failed to unmarshal matchmaking config", zap.Error(err))
							return
						}
					}
					logger = logger.WithField("group_id", matchmakingConfig.GroupID)

					// Query the storage index
					query := "+group_id:" + matchmakingConfig.GroupID
					var members []string

					idxobjs, err := nk.StorageIndexList(ctx, SystemUserId, ActivePartyGroupIndex, query, 1000)
					if err != nil {
						logger.Error("Failed to list party members", zap.Error(err))
						return
					}
					for _, obj := range idxobjs.GetObjects() {
						members = append(members, obj.UserId)
					}

					// Convert the members to discord user IDs
					discordIds := make([]string, 0, len(members))
					for _, member := range members {
						discordId, err := d.discordRegistry.GetDiscordIdByUserId(ctx, uuid.FromStringOrNil(member))
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

					// Send the messge to the user
					s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
						Type: discordgo.InteractionResponseChannelMessageWithSource,
						Data: &discordgo.InteractionResponseData{
							Flags:   discordgo.MessageFlagsEphemeral,
							Content: content,
						},
					})

				case "group":
					var user *discordgo.User
					switch {
					case i.User != nil:
						user = i.User
					case i.Member.User != nil:
						user = i.Member.User
					default:
						return
					}

					options := i.ApplicationCommandData().Options
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
					if !groupRegex.MatchString(groupID) {
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
					userID, err := d.discordRegistry.GetUserIdByDiscordId(ctx, user.ID, true)
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
					matchmakingConfig := &MatchmakingConfig{}
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

		bot.AddHandler(func(s *discordgo.Session, i *discordgo.InteractionCreate) {

			switch i.Type {
			case discordgo.InteractionApplicationCommand:
				if h, ok := partyCommandHandlers[i.ApplicationCommandData().Name]; ok {
					h(s, i)
				}
			case discordgo.InteractionMessageComponent:
				// Handle the button press
				parts := strings.Split(i.MessageComponentData().CustomID, ":")
				switch parts[0] {
				case "fd_cancel_invite":
					// Cancel the invite
					// Get the partyID and inviteeSessionID
					partyID := uuid.FromStringOrNil(parts[1])
					sessionID := parts[2]

					// Loop over the invitations and remove the invitee
					// Get the party handler
					ph, found := pipeline.partyRegistry.(*LocalPartyRegistry).parties.Load(partyID)
					if !found {
						pipeline.logger.Error("Failed to find party.")
						return
					}
					ph.Lock()
					defer ph.Unlock()
					// Check if the invitee has a join request
					var presence *rtapi.UserPresence
					for _, p := range ph.joinRequests {
						if p.UserPresence.SessionId == sessionID {
							presence = p.UserPresence
							break
						}
					}
					if presence != nil {
						// Remove the invitee
						err := d.pipeline.partyRegistry.PartyRemove(ctx, partyID, pipeline.node, presence.SessionId, pipeline.node, presence)
						if err != nil {
							d.pipeline.logger.Error("Failed to remove party invite.")
							return
						}
						// Send an emphemeral message to the inviter
						s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
							Type: discordgo.InteractionResponseChannelMessageWithSource,
							Data: &discordgo.InteractionResponseData{
								Flags:   discordgo.MessageFlagsEphemeral,
								Content: "Invite cancelled.",
							},
						})
					} else {
						// Check if the invitee is already in the party
						for _, p := range ph.members.presences {
							if p.UserPresence.SessionId == sessionID {
								presence := p.UserPresence
								// Send an emphemeral message to the inviter
								err := pipeline.partyRegistry.PartyRemove(ctx, partyID, pipeline.node, presence.SessionId, pipeline.node, presence)
								if err != nil {
									pipeline.logger.Error("Failed to remove party invite.")
									return
								}
								// Send an emphemeral message to the inviter
								s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
									Type: discordgo.InteractionResponseChannelMessageWithSource,
									Data: &discordgo.InteractionResponseData{
										Flags:   discordgo.MessageFlagsEphemeral,
										Content: "Player removed from party.",
									},
								})
								// Send message to the invitee
								channel, err := s.UserChannelCreate(presence.UserId)
								if err != nil {
									pipeline.logger.Error("Failed to create user channel.")
									return
								}
								s.ChannelMessageSendComplex(channel.ID, &discordgo.MessageSend{
									Content: "You are no longer in a party.",
								})
								return
							}
						}
					}
				case "fd_accept_invite":
					// Accept the invite
					// Get the partyId and the inviteeSessionID

					partyID := uuid.FromStringOrNil(parts[1])
					inviteeSessionID := uuid.FromStringOrNil(parts[2])

					// Verify that the user has a join request.
					// Get the party
					ph, found := pipeline.partyRegistry.(*LocalPartyRegistry).parties.Load(partyID)
					if !found {
						pipeline.logger.Error("Failed to find party.")
						return
					}
					ph.Lock()
					defer ph.Unlock()
					// Check if the invitee is already in the party

					for _, p := range ph.members.presences {
						if p.UserPresence.SessionId == inviteeSessionID.String() {
							// Send an emphemeral message to the inviter
							s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
								Type: discordgo.InteractionResponseChannelMessageWithSource,
								Data: &discordgo.InteractionResponseData{
									Flags:   discordgo.MessageFlagsEphemeral,
									Content: "You are already in the party.",
								},
							})
							return
						}
					}
					// Find the join request
					var presence *rtapi.UserPresence
					for _, p := range ph.joinRequests {
						if p.UserPresence.SessionId == inviteeSessionID.String() {
							presence = p.UserPresence
							break
						}
					}
					if presence == nil {
						// Send an regular channel message to the invitee
						s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
							Type: discordgo.InteractionResponseChannelMessageWithSource,
							Data: &discordgo.InteractionResponseData{
								Flags:   discordgo.MessageFlagsEphemeral,
								Content: "Failed to find join request.",
							},
						})
						return
					}
					if err := pipeline.partyRegistry.PartyAccept(ctx, partyID, pipeline.node, inviteeSessionID.String(), pipeline.node, presence); err != nil {
						pipeline.logger.Error("Failed to accept party invite.")
						return
					}
					// Send a message to the entire party
					idMap, err := d.getPartyDiscordIds(ctx, d.discordRegistry, ph)
					if err != nil {
						pipeline.logger.Error("Failed to get party discord ids.")
						return
					}
					// make a string of comma seperated discord ids
					allMembers := lo.Map(lo.Values(idMap), func(v string, i int) string {
						return fmt.Sprintf("<@%s>", v)
					})
					joinee, err := d.discordRegistry.GetDiscordIdByUserId(ctx, uuid.FromStringOrNil(presence.UserId))
					if err != nil {
						pipeline.logger.Error("Failed to get discord id by user id.")
						return
					}

					memberList := strings.Join(allMembers, ", ")
					for userID, discordId := range idMap {
						channel, err := s.UserChannelCreate(discordId)
						if err != nil {
							pipeline.logger.Error("Failed to create user channel.")
							return
						}
						s.ChannelMessageSendComplex(channel.ID, &discordgo.MessageSend{
							Content: fmt.Sprintf("<@%s> has joined. The party is now: %s", joinee, memberList),
							Components: []discordgo.MessageComponent{
								discordgo.ActionsRow{
									Components: []discordgo.MessageComponent{

										discordgo.Button{
											Label:    "Leave Party",
											Style:    discordgo.DangerButton,
											Disabled: false,
											CustomID: fmt.Sprintf("fd_leave:%s:%s", partyID, userID),
										},
									},
								},
							},
						})
					}
				}
			default:
				pipeline.logger.Warn("Unhandled interaction type", zap.Any("type", i.Type))
				return
			}
		})

		return nil
	}
*/
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

func (d *DiscordAppBot) handleProfileRequest(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, s *discordgo.Session, discordRegistry DiscordRegistry, i *discordgo.InteractionCreate, discordId string, username string, fullProfile bool) error {
	if i.GuildID == "" {
		return fmt.Errorf("guild id is required")
	}
	userId, err := discordRegistry.GetUserIdByDiscordId(ctx, discordId, true)
	if err != nil {
		return fmt.Errorf("failed to authenticate (or create) user %s: %w", discordId, err)
	}

	if userId == uuid.Nil { // assertion
		logger.Error("Failed to get or create an account.")
		return fmt.Errorf("failed to get or create an account for %s (%s)", discordId, username)
	}
	// Synchronize the user's guilds with nakama groups
	err = d.discordRegistry.UpdateGuildGroup(ctx, logger, userId, i.GuildID, discordId)
	if err != nil {
		return fmt.Errorf("error updating guild groups: %v", err)
	}
	// Get the account.
	account, err := nk.AccountGetId(ctx, userId.String())
	if err != nil {
		return err
	}
	// Get the nakama groups
	groups, _, err := nk.UserGroupsList(ctx, userId.String(), 100, nil, "")
	if err != nil {
		return err
	}

	// Get the EvrUserId
	evrUserIds, err := GetEvrRecords(ctx, logger, nk, userId.String())
	if err != nil {
		return err
	}

	// Extract the evrUserInfo
	// extract a mapping of the evr ids and hte alst time they were logged into
	evrIdMap := make([]EvrIdLogins, 0, len(evrUserIds))
	for _, evrUserId := range evrUserIds {
		loginData := &evr.LoginProfile{}
		if err := json.Unmarshal([]byte(evrUserId.Value), loginData); err != nil {
			logger.WithField("err", err).Error("Failed to unmarshal login data.")
			continue
		}
		evrIdMap = append(evrIdMap, EvrIdLogins{
			EvrId:         evrUserId.GetKey(),
			LastLoginTime: evrUserId.GetUpdateTime().AsTime().Format(time.RFC3339),
			DisplayName:   loginData.DisplayName,
		})
	}

	// Get the EvrUserId
	result, err := GetAddressRecords(ctx, logger, nk, userId.String())
	if err != nil {
		return err
	}
	addresses := lo.Map(result, func(a *api.StorageObject, index int) string {
		return a.GetKey()
	})

	tsZeroAsBlank := func(t *timestamppb.Timestamp) string {
		if t == nil {
			return ""
		} else {
			return t.AsTime().Format(time.RFC3339)
		}
	}

	// Get the MatchId for the user from it's presence
	presences, err := nk.StreamUserList(StreamModeStatus, userId.String(), "", "", true, true)
	if err != nil {
		return err
	}
	matchId := ""
	if len(presences) != 0 {
		p := presences[0]
		matchId = p.GetStatus()
	}

	// Get the suspensions
	dr := d.discordRegistry.(*LocalDiscordRegistry)
	suspensions, err := dr.GetAllSuspensions(ctx, userId)
	if err != nil {
		return err
	}

	suspensionLines := make([]string, 0, len(suspensions))
	for _, suspension := range suspensions {
		suspensionLines = append(suspensionLines, fmt.Sprintf("%s: %s", suspension.GuildName, suspension.RoleName))
	}

	whoami := &WhoAmI{
		NakamaId:     userId.String(),
		Username:     account.GetUser().GetUsername(),
		DiscordId:    discordId,
		DisplayName:  account.GetUser().GetDisplayName(),
		CreateTime:   tsZeroAsBlank(account.GetUser().GetCreateTime()),
		DisableTime:  tsZeroAsBlank(account.DisableTime),
		DevicesCount: len(account.GetDevices()),
		DiscordLink:  account.GetCustomId() != "",
		HasPassword:  account.GetEmail() != "",
		Groups: lo.Map(groups, func(g *api.UserGroupList_UserGroup, index int) string {
			return g.GetGroup().GetName()
		}),
		DeviceLinks: lo.Map(account.GetDevices(), func(d *api.AccountDevice, index int) string {
			return d.GetId()
		}),
		EvrLogins: evrIdMap,
		Addresses: addresses,
		Online:    account.GetUser().GetOnline(),
	}
	if !fullProfile {
		whoami.Addresses = nil
		whoami.DeviceLinks = nil
		whoami.EvrLogins = nil
		whoami.Groups = nil
	}

	fields := []*discordgo.MessageEmbedField{
		{Name: "Nakama ID", Value: whoami.NakamaId, Inline: true},
		{Name: "Online", Value: fmt.Sprintf("%v", whoami.Online), Inline: true},
		{Name: "Create Time", Value: whoami.CreateTime, Inline: false},
		{Name: "Username", Value: whoami.Username, Inline: true},
		{Name: "Display Name", Value: whoami.DisplayName, Inline: true},
		{Name: "Discord ID", Value: whoami.DiscordId, Inline: true},
		{Name: "Has Password", Value: fmt.Sprintf("%v", whoami.HasPassword), Inline: true},
		{Name: "Device Count", Value: fmt.Sprintf("%d", whoami.DevicesCount), Inline: true},
		{Name: "IP Addresses", Value: strings.Join(whoami.Addresses, "\n"), Inline: false},
		{Name: "Device Links", Value: strings.Join(whoami.DeviceLinks, "\n"), Inline: false},
		{Name: "Evr Logins", Value: strings.Join(lo.Map(whoami.EvrLogins, func(l EvrIdLogins, index int) string {
			return fmt.Sprintf("%16s (%16s) - %16s", l.LastLoginTime, l.EvrId, l.DisplayName)
		}), "\n"), Inline: false},
		{Name: "Groups", Value: strings.Join(whoami.Groups, "\n"), Inline: false},
	}

	if whoami.DisableTime != "" {
		fields = append(fields, &discordgo.MessageEmbedField{Name: "Disable Time", Value: whoami.DisableTime, Inline: true})
	}

	if len(suspensionLines) > 0 {
		fields = append(fields, &discordgo.MessageEmbedField{Name: "Suspensions", Value: strings.Join(suspensionLines, "\n"), Inline: false})
	}

	if matchId != "" {
		m, _, _ := strings.Cut(matchId, ".")
		fields = append(fields, &discordgo.MessageEmbedField{Name: "Current Match", Value: fmt.Sprintf("https://echo.taxi/spark://c/%s", m), Inline: true})
	}

	// Send the response
	s.InteractionRespond(i.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{
			Flags: discordgo.MessageFlagsEphemeral,
			Embeds: []*discordgo.MessageEmbed{
				{
					Title:  "Your Echo VR Account",
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
				return nil, fmt.Errorf("failed to find party.")
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
