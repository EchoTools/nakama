package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/jackc/pgtype"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
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
		"match/public":                  MatchListPublicRPC,
		"match":                         MatchRPC,
		"match/prepare":                 PrepareMatchRPC,
		"match/setnext":                 RESTfulRPCHandlerFactory[SetNextMatchRPCRequest](SetNextMatchRPC),
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
		if obj.GetCreateTime().AsTime().Before(time.Now().Add(-time.Hour * 24)) {
			deletes = append(deletes, &runtime.StorageDelete{
				Collection: LinkTicketCollection,
				Key:        obj.Key,
				Version:    obj.Version,
			})
		}
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
func GetUserIDByDiscordID(ctx context.Context, db *sql.DB, customID string) (userID string, err error) {
	// Look for an existing account.
	query := "SELECT id, disable_time FROM users WHERE custom_id = $1"
	var dbUserID string
	var dbDisableTime pgtype.Timestamptz
	var found = true
	err = db.QueryRowContext(ctx, query, customID).Scan(&dbUserID, &dbDisableTime)
	if err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return uuid.Nil.String(), status.Error(codes.Internal, "error finding user account")
		}
	}
	if !found {
		return uuid.Nil.String(), status.Error(codes.NotFound, "user account not found")
	}

	// Check if it's disabled.
	if dbDisableTime.Status == pgtype.Present && dbDisableTime.Time.Unix() != 0 {
		return dbUserID, status.Error(codes.PermissionDenied, "account banned")
	}

	return dbUserID, nil
}

func GetGroupIDByGuildID(ctx context.Context, db *sql.DB, guildID string) (groupID string, err error) {
	// Look for an existing account.
	query := "SELECT id FROM groups WHERE lang_tag = 'guild' AND metadata->>'guild_id' = $1"
	var dbGroupID string
	var found = true
	if err = db.QueryRowContext(ctx, query, guildID).Scan(&dbGroupID); err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return uuid.Nil.String(), status.Error(codes.Internal, "error finding group by guild ID")
		}
	}
	if !found {
		return uuid.Nil.String(), status.Error(codes.NotFound, "guild ID not found")
	}

	return dbGroupID, nil
}

func GetDiscordIDByUserID(ctx context.Context, db *sql.DB, userID string) (discordID string, err error) {
	// Look for an existing account.
	query := "SELECT custom_id FROM users WHERE id = $1"
	var dbCustomID string
	var found = true
	if err = db.QueryRowContext(ctx, query, userID).Scan(&dbCustomID); err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return "", status.Error(codes.Internal, "error finding discord ID by user ID")
		}
	}
	if !found {
		return "", status.Error(codes.NotFound, "discord ID not found")
	}

	return dbCustomID, nil
}

func GetGuildIDByGroupID(ctx context.Context, db *sql.DB, groupID string) (guildID string, err error) {
	// Look for an existing account.
	query := "SELECT metadata->>'guild_id' FROM groups WHERE id = $1"
	var dbGuildID string
	var found = true
	if err = db.QueryRowContext(ctx, query, groupID).Scan(&dbGuildID); err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return "", status.Error(codes.Internal, "error finding guild ID by group ID")
		}
	}
	if !found {
		return "", status.Error(codes.NotFound, "guild ID not found")
	}

	return dbGuildID, nil
}

/*
// GetGuildGroupMemberships looks up the guild groups by the user ID
func GetGuildGroupMemberships(ctx context.Context, nk runtime.NakamaModule, db *sql.DB, discordRegistry DiscordRegistry, userID uuid.UUID, groupIDs []uuid.UUID) ([]GuildGroupMembership, error) {
	// Check if userId is provided

	account, err := nk.AccountGetId(ctx, userID.String())
	if err != nil {
		return nil, fmt.Errorf("error getting account: %w", err)
	}
	displayName := account.User.GetDisplayName()
	username := account.User.GetUsername()
	// Get the override if it exists
	amd := AccountUserMetadata{}
	if err = json.Unmarshal([]byte(account.User.GetMetadata()), &amd); err != nil {
		return nil, fmt.Errorf("error unmarshalling account metadata: %w", err)
	}
	displayNameOverride := amd.DisplayNameOverride
	if amd.DisplayNameOverride != "" && account.User.GetDisplayName() != amd.DisplayNameOverride {
		if err = nk.AccountUpdateId(ctx, account.User.Id, "", nil, displayNameOverride, "", "", "", ""); err != nil {
			return nil, fmt.Errorf("error updating account: %w", err)
		}
	}

	memberships := make([]GuildGroupMembership, 0)
	cursor := ""
	for {
		// Fetch the groups using the provided userId
		userGroups, _, err := nk.UserGroupsList(ctx, userID.String(), 100, nil, cursor)
		if err != nil {
			return nil, fmt.Errorf("error getting user groups: %w", err)
		}
		for _, ug := range userGroups {
			g := ug.GetGroup()
			if g.GetLangTag() != "guild" {
				continue
			}
			if len(groupIDs) > 0 && !slices.Contains(groupIDs, uuid.FromStringOrNil(g.GetId())) {
				continue
			}
			_, md, err := GetGuildGroupMetadata(ctx, nk, g.GetId())
			if err != nil {
				return nil, fmt.Errorf("error getting guild group metadata: %w", err)
			}

			membership := NewGuildGroupMembership(g, userID, api.UserGroupList_UserGroup_State(ug.GetState().GetValue()))

			if lo.Contains(md.SuspendedUserIDs, userID.String()) {
				membership.isMember = false
				membership.isSuspended = true
			}

			// If the account has an override, use that
			if displayNameOverride != "" {
				membership.DisplayName = displayNameOverride
			} else {
				// Determine the display name
				var err error
				membership.DisplayName, err = SetDisplayNameByChannelBySession(ctx, nk, db, discordRegistry, displayName, username, membership.GuildGroup.ID().String())
				if err != nil {
					return nil, fmt.Errorf("error setting display name: %w", err)
				}
			}

			memberships = append(memberships, membership)
		}
		if cursor == "" {
			break
		}
	}
	return memberships, nil
}
*/

func GetGuildGroupMetadata(ctx context.Context, nk runtime.NakamaModule, groupId string) (*api.Group, *GroupMetadata, error) {

	groups, err := nk.GroupsGetId(ctx, []string{groupId})
	if err != nil {
		return nil, nil, status.Error(codes.Internal, fmt.Sprintf("error getting group: %v", err))
	}

	if len(groups) == 0 {
		return nil, nil, status.Error(codes.NotFound, fmt.Sprintf("group not found: %v", groupId))
	}
	group := groups[0]
	if group.LangTag != "guild" {
		return nil, nil, ErrGroupIsNotaGuild
	}

	// Extract the metadata from the group
	data := groups[0].GetMetadata()

	// Unmarshal the metadata into a GroupMetadata struct
	md := &GroupMetadata{}
	if err := json.Unmarshal([]byte(data), md); err != nil {
		return nil, nil, status.Error(codes.Internal, fmt.Sprintf("error unmarshalling group metadata: %v", err))
	}

	return group, md, nil
}

func SetGuildGroupMetadata(ctx context.Context, nk runtime.NakamaModule, groupId string, metadata *GroupMetadata) error {
	// Check if groupId is provided
	if groupId == "" {
		return fmt.Errorf("groupId is required")
	}

	// Marshal the metadata
	mdMap, err := metadata.MarshalToMap()
	if err != nil {
		return fmt.Errorf("error marshalling group metadata: %w", err)
	}

	// Get the group
	groups, err := nk.GroupsGetId(ctx, []string{groupId})
	if err != nil {
		return fmt.Errorf("error getting group: %w", err)
	}
	g := groups[0]

	// Update the group
	if err := nk.GroupUpdate(ctx, g.Id, SystemUserID, g.Name, g.CreatorId, g.LangTag, g.Description, g.AvatarUrl, g.Open.Value, mdMap, int(g.MaxCount)); err != nil {
		return fmt.Errorf("error updating group: %w", err)
	}

	return nil
}

func MatchLabelByID(ctx context.Context, nk runtime.NakamaModule, matchID MatchID) (*EvrMatchState, error) {
	match, err := nk.MatchGet(ctx, matchID.String())
	if err != nil || match == nil {
		return nil, err
	}

	label := EvrMatchState{}
	if err = json.Unmarshal([]byte(match.GetLabel().GetValue()), &label); err != nil {
		return nil, err
	}

	return &label, nil
}

func PartyMemberList(ctx context.Context, nk runtime.NakamaModule, partyID uuid.UUID) ([]runtime.Presence, error) {
	node, ok := ctx.Value(runtime.RUNTIME_CTX_NODE).(string)
	if !ok {
		return nil, status.Error(codes.Internal, "error getting node from context")
	}
	// Get the MatchIDs for the user from it's presence
	presences, err := nk.StreamUserList(StreamModeParty, partyID.String(), "", node, true, true)
	if err != nil {
		return nil, err
	}
	return presences, nil
}
