package server

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"

	_ "google.golang.org/protobuf/proto"
)

var (
	// This is a list of functions that are called to initialize the runtime module from runtime_go.go
	EvrRuntimeModuleFns = []func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error{
		InitializeEvrRuntimeModule,
		EchoTaxiRuntimeModule,
	}
)

// FIXME Figure out how to get this passed to the pipeline without having to do this.

type EvrRuntime struct{}

func NewEvrRuntime(logger *zap.Logger, config Config, db *sql.DB, matchRegistry MatchRegistry, router MessageRouter) *EvrRuntime {
	/*
		matchProvider := NewMatchProvider()

		matchProvider.RegisterCreateFn("go",
			func(ctx context.Context, logger *zap.Logger, id uuid.UUID, node string, stopped *atomic.Bool,
				name string) (RuntimeMatchCore, error) {
				match, err := newEvrLobby(context.Background(), NewRuntimeGoLogger(logger), nil, nil)
				if err != nil {
					return nil, err
				}

				rmc, err := NewRuntimeGoMatchCore(logger, "module", matchRegistry, router, id, "node", "",
					stopped, nil, map[string]string{}, nil, match)
				if err != nil {
					return nil, err
				}
				return rmc, nil
			},
		)
	*/

	// This is a hack around the issues with go plugins.
	// This mimics the loading of a nakama plugin.
	// TODO continue to move code out of the internals and into this runtime.

	//fn := f.(func(context.Context, runtime.Logger, *sql.DB, runtime.NakamaModule, runtime.Initializer) error)

	return &EvrRuntime{}
}

func InitializeEvrRuntimeModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) (err error) {
	// Setup the discord registry with the bot

	vars, _ := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)
	botToken := vars["DISCORD_BOT_TOKEN"]

	ctx = context.WithValue(ctx, ctxDiscordBotTokenKey{}, vars["DISCORD_BOT_TOKEN"])

	// TODO FIXME Make sure the system works without a bot. Add interfaces.
	if botToken != "" {
		// Start the bot
		dg, err := discordgo.New("Bot " + botToken)
		if err != nil {
			logger.Error("Unable to create bot")
		}
		discordRegistry := NewLocalDiscordRegistry(ctx, nk, logger, nil, nil, dg)
		if err != nil {
			logger.Error("Unable to create discord registry: %v", err)
			return err
		}
		if err = discordRegistry.InitializeDiscordBot(ctx, logger, nk, initializer); err != nil {
			logger.Error("Unable to initialize discord: %v", err)
			return err
		}
	}

	// Register RPC's for device linking
	rpcs := map[string]func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error){
		"link/device":          LinkDeviceRpc,
		"link/usernamedevice":  LinkUserIdDeviceRpc,
		"signin/discord":       DiscordSignInRpc,
		"match":                MatchRpc,
		"link":                 LinkingAppRpc,
		"evr/servicestatus":    ServiceStatusRpc,
		"importloadouts":       ImportLoadoutsRpc,
		"terminateMatch":       terminateMatchRpc,
		"matchmaker":           matchmakingStatusRpc,
		"setmatchamakerstatus": setMatchmakingStatusRpc,
	}

	for name, rpc := range rpcs {
		if err = initializer.RegisterRpc(name, rpc); err != nil {
			logger.Error("Unable to register: %v", err)
			return
		}
	}

	go RegisterIndexes(initializer)

	// Create the core groups
	if err := createCoreGroups(ctx, logger, db, nk, initializer); err != nil {
		logger.Error("Unable to create core groups: %v", err)
		return err
	}

	// Register the "matchmaking" handler
	if err := initializer.RegisterMatch(EvrMatchmakerModule, func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) (runtime.Match, error) {
		return &EvrMatch{}, nil
	}); err != nil {
		return err
	}

	// Register RPC for /api service.
	if err := initializer.RegisterRpc("evr/api", EvrApiHttpHandler); err != nil {
		logger.Error("Unable to register: %v", err)
		return err
	}

	logger.Info("Initialized runtime module.")
	return nil
}

func createCoreGroups(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	// Create user for use by the discord bot (and core group ownership)
	userId, _, _, err := nk.AuthenticateDevice(ctx, SystemUserId, "discordbot", true)
	if err != nil {
		logger.WithField("err", err).Error("Error creating discordbot user: %v", err)
	}

	coreGroups := []string{
		"Global Developers",
		"Global Moderators",
		"Global Testers",
	}

	for _, name := range coreGroups {
		// Search for group first
		groups, _, err := nk.GroupsList(ctx, name, "", nil, nil, 1, "")
		if err != nil {
			logger.WithField("err", err).Error("Group list error: %v", err)
		}
		if len(groups) == 0 {
			// Create a nakama group for developers
			_, err = nk.GroupCreate(ctx, userId, name, userId, "en", name, "", false, map[string]interface{}{}, 1000)
			if err != nil {
				logger.WithField("err", err).Error("Group create error: %v", err)
			}
		}
	}

	// Create a VRML group for each season
	vrmlgroups := []string{
		"VRML Season Preseason",
		"VRML Season 1",
		"VRML Season 1 Finalist",
		"VRML Season 1 Champion",
		"VRML Season 2",
		"VRML Season 2 Finalist",
		"VRML Season 2 Champion",
		"VRML Season 3",
		"VRML Season 3 Finalist",
		"VRML Season 3 Champion",
		"VRML Season 4",
		"VRML Season 4 Finalist",
		"VRML Season 4 Champion",
		"VRML Season 5",
		"VRML Season 5 Finalist",
		"VRML Season 5 Champion",
		"VRML Season 6",
		"VRML Season 6 Finalist",
		"VRML Season 6 Champion",
		"VRML Season 7",
		"VRML Season 7 Finalist",
		"VRML Season 7 Champion",
	}
	// Create the VRML groups
	for _, name := range vrmlgroups {
		groups, _, err := nk.GroupsList(ctx, name, "", nil, nil, 1, "")
		if err != nil {
			logger.WithField("err", err).Error("Group list error: %v", err)
		}
		if len(groups) == 0 {
			_, err = nk.GroupCreate(ctx, userId, name, userId, "en", "VRML Badge Entitlement", "", false, map[string]interface{}{}, 1000)
			if err != nil {
				logger.WithField("err", err).Error("Group create error: %v", err)
			}
		}
	}
	return nil
}

// Register Indexes for the login service
func RegisterIndexes(initializer runtime.Initializer) error {
	// Register the LinkTicket Index that prevents multiple LinkTickets with the same device_id_str
	name := LinkTicketIndex
	collection := LinkTicketCollection
	key := ""                                                 // Set to empty string to match all keys instead
	fields := []string{"evrid_token", "nk_device_auth_token"} // index on these fields
	maxEntries := 10000
	indexOnly := false

	if err := initializer.RegisterStorageIndex(name, collection, key, fields, maxEntries, indexOnly); err != nil {
		return err
	}

	// Register the IP Address index for looking up user's by IP Address
	// FIXME this needs to be updated for the new login system
	name = IpAddressIndex
	collection = EvrLoginStorageCollection
	key = ""                                           // Set to empty string to match all keys instead
	fields = []string{"client_ip_address,displayname"} // index on these fields
	maxEntries = 1000000
	indexOnly = false
	if err := initializer.RegisterStorageIndex(name, collection, key, fields, maxEntries, indexOnly); err != nil {
		return err
	}
	name = EvrIDStorageIndex
	collection = GameProfileStorageCollection
	key = GameProfileStorageKey             // Set to empty string to match all keys instead
	fields = []string{"server.xplatformid"} // index on these fields
	maxEntries = 100000
	indexOnly = false
	if err := initializer.RegisterStorageIndex(name, collection, key, fields, maxEntries, indexOnly); err != nil {
		return err
	}
	// Register the DisplayName index for avoiding name collisions
	// FIXME this needs to be updated for the new login system
	name = DisplayNameIndex
	collection = EvrLoginStorageCollection
	key = ""                          // Set to empty string to match all keys instead
	fields = []string{"display_name"} // index on these fields
	maxEntries = 100000
	if err := initializer.RegisterStorageIndex(name, collection, key, fields, maxEntries, indexOnly); err != nil {
		return err
	}

	name = GhostedUsersIndex
	collection = GameProfileStorageCollection
	key = GameProfileStorageKey             // Set to empty string to match all keys instead
	fields = []string{"client.ghost.users"} // index on these fields
	maxEntries = 1000000
	if err := initializer.RegisterStorageIndex(name, collection, key, fields, maxEntries, indexOnly); err != nil {
		return err
	}

	name = ActiveSocialGroupIndex
	collection = GameProfileStorageCollection
	key = GameProfileStorageKey              // Set to empty string to match all keys instead
	fields = []string{"client.social.group"} // index on these fields
	maxEntries = 100000
	if err := initializer.RegisterStorageIndex(name, collection, key, fields, maxEntries, indexOnly); err != nil {
		return err
	}

	return nil
}

func EvrApiHttpHandler(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var message interface{}
	if err := json.Unmarshal([]byte(payload), &message); err != nil {
		return "", err
	}

	logger.Info("API Service Message: %v", message)

	response, err := json.Marshal(map[string]interface{}{"message": message})
	if err != nil {
		return "", err
	}

	return string(response), nil
}
