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
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/jackc/pgtype"
	"go.uber.org/zap"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	_ "google.golang.org/protobuf/proto"
)

var (
	nakamaStartTime = time.Now()
	// This is a list of functions that are called to initialize the runtime module from runtime_go.go
	EvrRuntimeModuleFns = []func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) error{
		InitializeEvrRuntimeModule,
	}
)

const (
	GroupGlobalDevelopers        = "Global Developers"
	GroupGlobalModerators        = "Global Moderators"
	GroupGlobalTesters           = "Global Testers"
	GroupGlobalBots              = "Global Bots"
	GroupGlobalBadgeAdmins       = "Global Badge Admins"
	GroupGlobalPrivateDataAccess = "Global Private Data Access"
	GroupGlobalRequire2FA        = "Global Require 2FA"
	SystemGroupLangTag           = "system"
	GuildGroupLangTag            = "guild"

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
		"player/setnextmatch":           SetNextMatchRPC,
		"player/statistics":             PlayerStatisticsRPC,
		"link":                          LinkingAppRpc,
		"evr/servicestatus":             ServiceStatusRpc,
		"importloadouts":                ImportLoadoutsRpc,
		//"terminateMatch":                shutdownMatchRpc,
		"setmatchamakerstatus": setMatchmakingStatusRpc,

		//"/v1/storage/game/sourcedb/rad15/json/r14/loading_tips.json": StorageLoadingTipsRPC,
	}

	for name, rpc := range rpcs {
		if err = initializer.RegisterRpc(name, rpc); err != nil {
			return fmt.Errorf("unable to register %s: %v", name, err)
		}
	}

	if db != nil {
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
	/*
		// Register the matchmaking override
		sbmm := &SkillBasedMatchmaker{}
		if err := initializer.RegisterMatchmakerOverride(sbmm.EvrMatchmakerFn); err != nil {
			return fmt.Errorf("unable to register matchmaker override: %v", err)
		}
	*/
	// Update the metrics with match data
	go metricsUpdateLoop(ctx, logger, nk.(*RuntimeGoNakamaModule))

	logger.Info("Initialized runtime module.")
	return nil
}

type MatchLabelMeta struct {
	TickRate  int
	Presences []*rtapi.UserPresence
	State     MatchLabel
}

func listMatchStates(ctx context.Context, nk runtime.NakamaModule, query string) ([]*MatchLabelMeta, error) {
	if query == "" {
		query = "*"
	}
	// Get the list of active matches
	minSize := 1
	maxSize := MatchLobbyMaxSize

	matches, err := nk.MatchList(ctx, 1000, true, "", &minSize, &maxSize, query)
	if err != nil {
		return nil, err
	}

	var matchStates []*MatchLabelMeta
	for _, match := range matches {
		mt := MatchIDFromStringOrNil(match.MatchId)
		presences, tickRate, data, err := nk.(*RuntimeGoNakamaModule).matchRegistry.GetState(ctx, mt.UUID, mt.Node)
		if err != nil {
			return nil, err
		}

		state := MatchLabel{}
		if err := json.Unmarshal([]byte(data), &state); err != nil {
			return nil, err
		}

		if state.LobbyType == UnassignedLobby {
			continue
		}

		matchStates = append(matchStates, &MatchLabelMeta{
			State:     state,
			TickRate:  int(tickRate),
			Presences: presences,
		})
	}
	return matchStates, nil
}

type MatchStateTags struct {
	Type     string
	Mode     string
	Level    string
	Operator string
	Region   string
	Group    string
}

func (t MatchStateTags) AsMap() map[string]string {
	return map[string]string{
		"type":     t.Type,
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
	defer ticker.Stop()

	for {

		select {
		case <-ctx.Done():
			// Context has been cancelled, return
			return
		case <-ticker.C:
		}

		// Get the match states
		matchStates, err := listMatchStates(ctx, nk, "")
		if err != nil {
			logger.Error("Error listing match states: %v", err)
			continue
		}
		playercounts := make(map[MatchStateTags][]int)
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
				Type:     state.State.LobbyType.String(),
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
			nk.metrics.CustomGauge("match_active_gauge", tagMap, float64(len(matches)))
			nk.metrics.CustomGauge("player_active_gauge", tagMap, float64(playerCount))
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
		GroupGlobalRequire2FA,
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
	var dbCustomID sql.NullString
	var found = true
	if err = db.QueryRowContext(ctx, query, userID).Scan(&dbCustomID); err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return "", status.Error(codes.Internal, "error finding discord ID by user ID: "+err.Error())
		}
	}
	if !found {
		return "", status.Error(codes.NotFound, "discord ID not found")
	}

	return dbCustomID.String, nil
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

func GetGuildGroupMetadata(ctx context.Context, db *sql.DB, groupID string) (*GroupMetadata, error) {
	// Look for an existing account.
	query := "SELECT metadata FROM groups WHERE id = $1"
	var dbGuildMetadataJSON string
	var found = true
	var err error
	if err = db.QueryRowContext(ctx, query, groupID).Scan(&dbGuildMetadataJSON); err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return nil, status.Error(codes.Internal, "error finding guild ID by group ID")
		}
	}
	if !found {
		return nil, status.Error(codes.NotFound, "guild ID not found")
	}

	metadata := &GroupMetadata{}
	if err := json.Unmarshal([]byte(dbGuildMetadataJSON), metadata); err != nil {
		return nil, status.Error(codes.Internal, "error unmarshalling guild metadata")
	}
	return metadata, nil
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

func MatchLabelByID(ctx context.Context, nk runtime.NakamaModule, matchID MatchID) (*MatchLabel, error) {
	match, err := nk.MatchGet(ctx, matchID.String())
	if err != nil || match == nil {
		return nil, err
	}

	label := MatchLabel{}
	if err = json.Unmarshal([]byte(match.GetLabel().GetValue()), &label); err != nil {
		return nil, err
	}
	if label.GroupID == nil {
		label.GroupID = &uuid.Nil
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

/*
func CheckAllocationPermission(ctx context.Context, db *sql.DB, userID, groupID string) error {
	var err error
	var allowed bool
	// Validate that this userID has permission to signal this match

	if ok, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalDevelopers); err != nil {
		return runtime.NewError("Failed to check group membership", StatusInternalError)
	} else if ok {
		allowed = true
	} else if label.Broadcaster.OperatorID == userID {
		allowed = true
	} else {
		for _, groupID := range label.Broadcaster.GroupIDs {
			if _, md, err := GetGuildGroupMetadata(ctx, nk, groupID.String()); err != nil {
				returnruntime.NewError("Failed to get group metadata", StatusInternalError)
			} else if slices.Contains(md.AllocatorUserIDs, userID) {
				allowed = true
				break
			}
		}
	}

	if !allowed {
		return runtime.NewError("unauthorized to signal match", StatusPermissionDenied)
	}

	var groupID string
	if request.GuildID != "" {
		if groupID, err = GetGroupIDByGuildID(ctx, db, request.GuildID); err != nil {
			return runtime.NewError(err.Error(), StatusInternalError)
		} else if groupID == "" {
			return runtime.NewError("guild group not found", StatusNotFound)
		}
	}

}
*/

func CheckSystemGroupMembership(ctx context.Context, db *sql.DB, userID, groupName string) (bool, error) {
	return CheckGroupMembershipByName(ctx, db, userID, groupName, SystemGroupLangTag)
}

func CheckGroupMembershipByName(ctx context.Context, db *sql.DB, userID, groupName, groupType string) (bool, error) {
	query := `
SELECT ge.state FROM groups g, group_edge ge WHERE g.id = ge.destination_id AND g.lang_tag = $1 AND g.name = $2 
AND ge.source_id = $3 AND ge.state >= 0 AND ge.state <= $4;
`

	params := make([]interface{}, 0, 4)
	params = append(params, groupType)
	params = append(params, groupName)
	params = append(params, userID)
	params = append(params, int32(api.UserGroupList_UserGroup_MEMBER))
	rows, err := db.QueryContext(ctx, query, params...)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, nil
	}
	return true, nil
}

func CheckGroupMembershipByID(ctx context.Context, db *sql.DB, userID, groupID, groupType string) (bool, error) {
	query := `
SELECT ge.state FROM groups g, group_edge ge WHERE g.id = ge.destination_id AND g.lang_tag = $1 AND g.id = $2
AND ge.source_id = $3 AND ge.state >= 0 AND ge.state <= $4;
`

	params := make([]interface{}, 0, 4)
	params = append(params, groupType)
	params = append(params, groupID)
	params = append(params, userID)
	params = append(params, int32(api.UserGroupList_UserGroup_MEMBER))

	rows, err := db.QueryContext(ctx, query, params...)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, nil
	}
	return true, nil
}

func PresenceByEntrantID(nk runtime.NakamaModule, matchID MatchID, entrantID uuid.UUID) (presence *EvrMatchPresence, err error) {

	presences, err := nk.StreamUserList(StreamModeEntrant, entrantID.String(), "", matchID.Node, true, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get stream presences for entrant %s: %w", entrantID.String(), err)
	}

	if len(presences) == 0 {
		return nil, ErrEntrantNotFound
	}

	if len(presences) > 1 {
		return nil, ErrMultipleEntrantsFound
	}

	mp := &EvrMatchPresence{}
	if err := json.Unmarshal([]byte(presences[0].GetStatus()), mp); err != nil {
		return nil, fmt.Errorf("failed to unmarshal presence: %w", err)
	}

	return mp, nil
}

func GetMatchBySessionID(nk runtime.NakamaModule, sessionID uuid.UUID) (matchID MatchID, presence runtime.Presence, err error) {

	presences, err := nk.StreamUserList(StreamModeService, sessionID.String(), "", StreamLabelMatchService, true, true)
	if err != nil {
		return MatchID{}, nil, fmt.Errorf("failed to get stream presences: %w", err)
	}

	for _, presence := range presences {
		matchID := MatchIDFromStringOrNil(presence.GetStatus())
		if !matchID.IsNil() {
			// Verify that the user is actually in the match
			if meta, err := nk.StreamUserGet(StreamModeMatchAuthoritative, matchID.UUID.String(), "", matchID.Node, presence.GetUserId(), presence.GetSessionId()); err != nil || meta == nil {
				return MatchID{}, nil, ErrMatchNotFound
			}
			return matchID, presence, nil
		}
	}

	return MatchID{}, nil, ErrMatchNotFound
}

func GetLobbyGroupID(ctx context.Context, db *sql.DB, userID string) (string, uuid.UUID, error) {
	query := "SELECT value->>'group_id' FROM storage WHERE collection = $1 AND key = $2 and user_id = $3"
	var dbPartyGroupName string
	var found = true
	err := db.QueryRowContext(ctx, query, MatchmakingConfigStorageCollection, MatchmakingConfigStorageKey, userID).Scan(&dbPartyGroupName)
	if err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return "", uuid.Nil, status.Error(codes.Internal, "error finding user account")
		}
	}
	if !found {
		return "", uuid.Nil, status.Error(codes.NotFound, "user account not found")
	}
	if dbPartyGroupName == "" {
		return "", uuid.Nil, nil
	}
	return dbPartyGroupName, uuid.NewV5(uuid.Nil, dbPartyGroupName), nil
}

// returns map[guildID]groupID
func GetGuildGroupIDsByUser(ctx context.Context, db *sql.DB, userID string) (map[string]string, error) {
	if userID == "" {
		return nil, status.Error(codes.InvalidArgument, "user ID is required")
	}
	query := `
	SELECT g.id, g.metadata->>'guild_id' 
	FROM group_edge ge, groups g 
	WHERE g.id = ge.source_id AND ge.destination_id = $1 AND g.lang_tag = 'guild' AND ge.state <= 2
						  `

	var dbGroupID string
	var dbGuildID string
	rows, err := db.QueryContext(ctx, query, userID)
	if err != nil {
		return nil, status.Error(codes.Internal, "An error occurred while trying to list group IDs.")
	}

	groups := make(map[string]string, 0)

	for rows.Next() {
		if err := rows.Scan(&dbGroupID, &dbGuildID); err != nil {
			return nil, err
		}
		groups[dbGuildID] = dbGroupID
	}
	_ = rows.Close()
	return groups, nil
}

func DisconnectUserID(ctx context.Context, nk runtime.NakamaModule, userID string) (int, error) {
	// Get the user's presences

	presences, err := nk.StreamUserList(StreamModeService, userID, StreamContextLogin.String(), "", true, true)
	if err != nil {
		return 0, fmt.Errorf("failed to get stream presences: %w", err)
	}

	cnt := 0
	for _, presence := range presences {
		if err = nk.SessionDisconnect(ctx, presence.GetSessionId(), runtime.PresenceReasonDisconnect); err != nil {
			return cnt, fmt.Errorf("failed to disconnect session `%s`: %w", presence.GetSessionId(), err)
		}
		cnt++
	}

	return cnt, nil
}

func GetAccountMetadata(ctx context.Context, db *sql.DB, userID string) (*AccountMetadata, error) {
	query := "SELECT metadata FROM users WHERE id = $1"
	var metadata string
	if err := db.QueryRowContext(ctx, query, userID).Scan(&metadata); err != nil {
		return nil, err
	}

	md := &AccountMetadata{}
	if err := json.Unmarshal([]byte(metadata), md); err != nil {
		return nil, err
	}

	return md, nil
}

func GetUserIDByEvrID(ctx context.Context, db *sql.DB, evrID string) (string, error) {
	query := "SELECT user_id FROM storage WHERE collection = $1 AND key = $2 ORDER BY update_time DESC LIMIT 1"
	var dbUserID string
	var found = true
	err := db.QueryRowContext(ctx, query, EvrLoginStorageCollection, evrID).Scan(&dbUserID)
	if err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return "", status.Error(codes.Internal, "error finding user account")
		}
	}
	if !found {
		return "", status.Error(codes.NotFound, "user account not found")
	}
	if dbUserID == "" {
		return "", nil
	}
	return dbUserID, nil
}

func GetPlayerStats(ctx context.Context, db *sql.DB, userID string) (*evr.PlayerStatistics, error) {
	query := "SELECT value->'server'->>'stats' FROM storage WHERE user_id = $1 AND collection = $2 AND key = $3"
	var dbStatsJSON string
	var found = true
	err := db.QueryRowContext(ctx, query, userID, GameProfileStorageCollection, GameProfileStorageKey).Scan(&dbStatsJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return nil, status.Error(codes.Internal, "error finding user account")
		}
	}
	if !found {
		return nil, status.Error(codes.NotFound, "user account not found")
	}
	if dbStatsJSON == "" {
		return nil, nil
	}

	playerStats := &evr.PlayerStatistics{}
	if err := json.Unmarshal([]byte(dbStatsJSON), playerStats); err != nil {
		return nil, status.Error(codes.Internal, "error unmarshalling player statistics")
	}

	return playerStats, nil
}

func GetPartyGroupMembers(ctx context.Context, db *sql.DB, userID string) (*evr.PlayerStatistics, error) {
	query := "SELECT user_id FROM storage WHERE collection = $1 AND key = $2 AND value->>'group_id' = $3"
	var dbStatsJSON string
	var found = true
	err := db.QueryRowContext(ctx, query, userID, MatchmakingConfigStorageCollection, MatchmakingConfigStorageKey).Scan(&dbStatsJSON)
	if err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return nil, status.Error(codes.Internal, "error finding user account")
		}
	}
	if !found {
		return nil, status.Error(codes.NotFound, "user account not found")
	}
	if dbStatsJSON == "" {
		return nil, nil
	}

	playerStats := &evr.PlayerStatistics{}
	if err := json.Unmarshal([]byte(dbStatsJSON), playerStats); err != nil {
		return nil, status.Error(codes.Internal, "error unmarshalling player statistics")
	}

	return playerStats, nil
}

func RuntimeLoggerToZapLogger(logger runtime.Logger) *zap.Logger {
	return logger.(*RuntimeGoLogger).logger
}
