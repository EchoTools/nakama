package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"

	_ "google.golang.org/protobuf/proto"
)

var (
	// This is a list of functions that are called to initialize the runtime module from runtime_go.go
	EvrRuntimeModuleFns = []func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error{
		InitializeEvrRuntimeModule,
		EchoTaxiRuntimeModule,
	}
)

const (
	GroupGlobalDevelopers        = "Global Developers"
	GroupGlobalModerators        = "Global Moderators"
	GroupGlobalTesters           = "Global Testers"
	GroupGlobalBots              = "Global Bots"
	GroupGlobalBadgeAdmins       = "Global Badge Admins"
	GroupGlobalPrivateDataAccess = "Global Private Data Access"
	SystemGroupLangTag           = "system"

	FlagGlobalDevelopers = 1 << iota
	FlagGlobalModerators
	FlagGlobalTesters
	FlagGlobalBots
	FlagGlobalBadgeAdmins
	FlagNoVR
	FlagGlobalPrivateDataAccess
)

var groupFlagMap = map[string]int{
	GroupGlobalDevelopers:        FlagGlobalDevelopers,
	GroupGlobalModerators:        FlagGlobalModerators,
	GroupGlobalTesters:           FlagGlobalTesters,
	GroupGlobalBots:              FlagGlobalBots,
	GroupGlobalBadgeAdmins:       FlagGlobalBadgeAdmins,
	GroupGlobalPrivateDataAccess: FlagGlobalPrivateDataAccess,
}

func InitializeEvrRuntimeModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) (err error) {

	// Register RPC's for device linking
	rpcs := map[string]func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error){
		"account/lookup":                AccountLookupRPC,
		"account/authenticate/password": AuthenticatePasswordRPC,
		"link/device":                   LinkDeviceRpc,
		"link/usernamedevice":           LinkUserIdDeviceRpc,
		"signin/discord":                DiscordSignInRpc,
		"match":                         MatchRPC,
		"match/prepare":                 PrepareMatchRPC,
		"link":                          LinkingAppRpc,
		"evr/servicestatus":             ServiceStatusRpc,
		"importloadouts":                ImportLoadoutsRpc,
		"terminateMatch":                terminateMatchRpc,
		"matchmaker":                    matchmakingStatusRpc,
		"setmatchamakerstatus":          setMatchmakingStatusRpc,
	}

	for name, rpc := range rpcs {
		if err = initializer.RegisterRpc(name, rpc); err != nil {
			return fmt.Errorf("unable to register %s: %v", name, err)
		}
	}

	if err := RegisterIndexes(initializer); err != nil {
		return fmt.Errorf("unable to register indexes: %v", err)
	}

	// Remove all LinkTickets
	objs, _, err := nk.StorageList(ctx, SystemUserID, SystemUserID, LinkTicketCollection, 1000, "")
	if err != nil {
		return fmt.Errorf("unable to list LinkTickets: %v", err)
	}

	deletes := make([]*runtime.StorageDelete, 0, len(objs))
	for _, obj := range objs {
		deletes = append(deletes, &runtime.StorageDelete{
			Collection: LinkTicketCollection,
			Key:        obj.Key,
			Version:    obj.Version,
		})
	}

	if err := nk.StorageDelete(ctx, deletes); err != nil {
		return fmt.Errorf("unable to delete LinkTickets: %v", err)
	}

	// Create the core groups
	if err := createCoreGroups(ctx, logger, db, nk, initializer); err != nil {
		return fmt.Errorf("unable to create core groups: %v", err)
	}

	// Register the "matchmaking" handler
	if err := initializer.RegisterMatch(EvrMatchmakerModule, func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) (runtime.Match, error) {
		return &EvrMatch{}, nil
	}); err != nil {
		return err
	}

	// Register RPC for /api service.
	if err := initializer.RegisterRpc("evr/api", EvrApiHttpHandler); err != nil {
		return fmt.Errorf("unable to register /evr/api service: %v", err)
	}

	// Update the metrics with match data
	go metricsUpdateLoop(ctx, logger, nk.(*RuntimeGoNakamaModule))

	logger.Info("Initialized runtime module.")
	return nil
}

type MatchState struct {
	TickRate  int
	Presences []*rtapi.UserPresence
	State     EvrMatchState
}

func listMatchStates(ctx context.Context, nk runtime.NakamaModule, query string) ([]*MatchState, error) {
	if query == "" {
		query = "*"
	}
	// Get the list of active matches
	minSize := 1
	maxSize := MatchMaxSize
	matches, err := nk.MatchList(ctx, 1000, true, "", &minSize, &maxSize, query)
	if err != nil {
		return nil, err
	}

	var matchStates []*MatchState
	for _, match := range matches {
		mt := MatchIDFromStringOrNil(match.MatchId)
		presences, tickRate, data, err := nk.(*RuntimeGoNakamaModule).matchRegistry.GetState(ctx, mt.UUID(), mt.Node())
		if err != nil {
			return nil, err
		}

		state := EvrMatchState{}
		if err := json.Unmarshal([]byte(data), &state); err != nil {
			return nil, err
		}

		if state.LobbyType == UnassignedLobby {
			continue
		}

		matchStates = append(matchStates, &MatchState{
			State:     state,
			TickRate:  int(tickRate),
			Presences: presences,
		})
	}
	return matchStates, nil
}

type MatchStateTags struct {
	Mode     string
	Level    string
	Operator string
	Region   string
	Group    string
}

func (t MatchStateTags) AsMap() map[string]string {
	return map[string]string{
		"mode":     t.Mode,
		"level":    t.Level,
		"operator": t.Operator,
		"region":   t.Region,
		"group":    t.Group,
	}
}

func metricsUpdateLoop(ctx context.Context, logger runtime.Logger, nk *RuntimeGoNakamaModule) {

	// Create a ticker to update the metrics every 5 minutes
	ticker := time.NewTicker(60 * time.Second)
	playercounts := make(map[MatchStateTags][]int)
	for {
		select {
		case <-ctx.Done():
			// Context has been cancelled, return
			return
		case <-ticker.C:
		}

		// Remove zero'd out entries
		for tags, matches := range playercounts {
			// sum the slices
			count := 0
			for _, c := range matches {
				if c == 0 {
					matches = append(matches[:c], matches[c+1:]...)
				}
				count += c
			}
			if count == 0 {
				delete(playercounts, tags)
			}
		}

		// Get the match states
		matchStates, err := listMatchStates(ctx, nk, "")
		if err != nil {
			logger.Error("Error listing match states: %v", err)
			continue
		}

		for _, state := range matchStates {
			groupID := state.State.GroupID
			if groupID == nil {
				groupID = &uuid.Nil
			}

			region := "default"
			if len(state.State.Broadcaster.Regions) > 0 {
				region = state.State.Broadcaster.Regions[0].String()
			}

			stateTags := MatchStateTags{
				Mode:     state.State.Mode.String(),
				Level:    state.State.Level.String(),
				Operator: state.State.Broadcaster.OperatorID,
				Region:   region,
				Group:    groupID.String(),
			}

			playercounts[stateTags] = append(playercounts[stateTags], len(state.State.Players))
		}
		// Update the metrics

		for tags, matches := range playercounts {
			playerCount := 0
			for _, match := range matches {
				playerCount += match
			}
			tagMap := tags.AsMap()
			nk.metrics.CustomGauge("match_count_gauge", tagMap, float64(len(matches)))
			nk.metrics.CustomGauge("match_player_counts_gauge", tagMap, float64(playerCount))
		}
	}
}

func createCoreGroups(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error {
	// Create user for use by the discord bot (and core group ownership)
	userId, _, _, err := nk.AuthenticateDevice(ctx, SystemUserID, "discordbot", true)
	if err != nil {
		logger.WithField("err", err).Error("Error creating discordbot user: %v", err)
	}

	coreGroups := []string{
		GroupGlobalDevelopers,
		GroupGlobalModerators,
		GroupGlobalTesters,
		GroupGlobalBadgeAdmins,
		GroupGlobalBots,
		GroupGlobalPrivateDataAccess,
	}

	for _, name := range coreGroups {
		// Search for group first
		groups, _, err := nk.GroupsList(ctx, name, "", nil, nil, 1, "")
		if err != nil {
			logger.WithField("err", err).Error("Group list error: %v", err)
		}
		// remove groups that are not lang tag of 'system'
		for i, group := range groups {
			if group.LangTag != SystemGroupLangTag {
				groups = append(groups[:i], groups[i+1:]...)
			}
		}

		if len(groups) == 0 {
			// Create a nakama core group
			_, err = nk.GroupCreate(ctx, userId, name, userId, SystemGroupLangTag, name, "", false, map[string]interface{}{}, 1000)
			if err != nil {
				logger.WithField("err", err).Warn("Group `%s` create error: %v", name, err)
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
			_, err = nk.GroupCreate(ctx, userId, name, userId, "entitlement", "VRML Badge Entitlement", "", false, map[string]interface{}{}, 1000)
			if err != nil {
				logger.WithField("err", err).Error("Group create error: %v", err)
			}
			continue
		}
		group := groups[0]
		if err := nk.GroupUpdate(ctx, group.Id, userId, name, userId, "entitlement", "VRML Badge Entitlement", "", false, map[string]interface{}{}, 1000); err != nil {
			logger.WithField("err", err).Error("Group update error: %v", err)
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

	name = ActivePartyGroupIndex
	collection = MatchmakingStorageCollection
	key = MatchmakingConfigStorageKey // Set to empty string to match all keys instead
	fields = []string{"group_id"}     // index on these fields
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
