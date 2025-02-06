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
	"go.uber.org/zap"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	_ "google.golang.org/protobuf/proto"
)

var (
	nakamaStartTime = time.Now().UTC()
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
)

func InitializeEvrRuntimeModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) (err error) {

	/*
		if err := registerAPIGuards(initializer); err != nil {
			return fmt.Errorf("unable to register API guards: %w", err)
		}
	*/

	sbmm := NewSkillBasedMatchmaker()

	// Register RPC's for device linking
	rpcs := map[string]func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error){
		"account/search":                AccountSearchRPC,
		"account/lookup":                AccountLookupRPC,
		"account/authenticate/password": AuthenticatePasswordRPC,
		"leaderboard/records":           UserLeaderboardRecordsRPC,
		"link/device":                   LinkDeviceRpc,
		"link/usernamedevice":           LinkUserIdDeviceRpc,
		"signin/discord":                DiscordSignInRpc,
		"match/public":                  MatchListPublicRPC,
		"match":                         MatchRPC,
		"match/prepare":                 PrepareMatchRPC,
		"match/terminate":               shutdownMatchRpc,
		"player/setnextmatch":           SetNextMatchRPC,
		"player/statistics":             PlayerStatisticsRPC,
		"player/kick":                   KickPlayerRPC,
		"player/profile":                UserServerProfileRPC,
		"link":                          LinkingAppRpc,
		"evr/servicestatus":             ServiceStatusRpc,
		"importloadouts":                ImportLoadoutsRpc,
		"matchmaker/stream":             MatchmakerStreamRPC,
		"matchmaker/candidates":         MatchmakerCandidatesRPCFactory(sbmm),
		"stream/join":                   StreamJoinRPC,
		"server/score":                  ServerScoreRPC,
		"server/scores":                 ServerScoresRPC,
		"forcecheck":                    CheckForceUserRPC,
		//"/v1/storage/game/sourcedb/rad15/json/r14/loading_tips.json": StorageLoadingTipsRPC,
	}

	for name, rpc := range rpcs {
		if err = initializer.RegisterRpc(name, rpc); err != nil {
			return fmt.Errorf("unable to register %s: %w", name, err)
		}
	}

	if db != nil {
		if err := RegisterIndexes(initializer); err != nil {
			return fmt.Errorf("unable to register indexes: %w", err)
		}

		// Remove all LinkTickets
		if err := nk.StorageDelete(ctx, []*runtime.StorageDelete{{
			Collection: AuthorizationCollection,
			Key:        LinkTicketKey,
		}}); err != nil {
			return fmt.Errorf("unable to delete LinkTickets: %w", err)
		}

		// Create the core groups
		if err := createCoreGroups(ctx, logger, db, nk, initializer); err != nil {
			return fmt.Errorf("unable to create core groups: %w", err)
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
		return fmt.Errorf("unable to register /evr/api service: %w", err)
	}

	// Register the event

	eventDispatch, err := NewEventDispatch(ctx, logger, db, nk, initializer)
	if err != nil {
		return fmt.Errorf("unable to create event dispatch: %w", err)
	}

	if err := initializer.RegisterEvent(eventDispatch.eventFn); err != nil {
		return err
	}

	// Register the matchmaking override
	if err := initializer.RegisterMatchmakerOverride(sbmm.EvrMatchmakerFn); err != nil {
		return fmt.Errorf("unable to register matchmaker override: %w", err)
	}

	go MigrateSystem(ctx, logger, db, nk)

	// Update the metrics with match data
	go func() {
		<-time.After(15 * time.Second)
		metricsUpdateLoop(ctx, logger, nk.(*RuntimeGoNakamaModule))
	}()

	logger.Info("Initialized runtime module.")
	return nil
}

type MatchLabelMeta struct {
	TickRate  int
	Presences []*rtapi.UserPresence
	State     *MatchLabel
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

	return nil
}

// Register Indexes for the login service
func RegisterIndexes(initializer runtime.Initializer) error {

	if err := initializer.RegisterStorageIndex(
		ActivePartyGroupIndex,
		MatchmakerStorageCollection,
		MatchmakingConfigStorageKey,
		[]string{"group_id"},
		nil,
		100000,
		false,
	); err != nil {
		return err
	}

	// Register storage indexes for any Storables
	storables := []Storable{
		&DisplayNameHistory{},
		&LoginHistory{},
		&DeveloperApplications{},
	}
	for _, s := range storables {
		if idx := s.StorageIndex(); idx != nil {
			if err := initializer.RegisterStorageIndex(
				idx.Name,
				idx.Collection,
				idx.Key,
				idx.Fields,
				idx.SortableFields,
				idx.MaxEntries,
				idx.IndexOnly,
			); err != nil {
				return err
			}
		}
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
		return "", fmt.Errorf("error marshalling response: %w", err)
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
			return uuid.Nil.String(), fmt.Errorf("error finding user by discord ID: %w", err)
		}
	}
	if !found {
		return uuid.Nil.String(), ErrAccountNotFound
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
			return uuid.Nil.String(), fmt.Errorf("error finding guild ID: %w", err)
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
			return "", fmt.Errorf("error finding discord ID: %w", err)
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
			return "", fmt.Errorf("error finding guild ID: %w", err)
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
			return nil, fmt.Errorf("error finding guild metadata: %w", err)
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

func MatchLabelByID(ctx context.Context, nk runtime.NakamaModule, matchID MatchID) (*MatchLabel, error) {
	match, err := nk.MatchGet(ctx, matchID.String())
	if err != nil {
		return nil, err
	} else if match == nil {
		return nil, ErrMatchNotFound
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

func CheckSystemGroupMembership(ctx context.Context, db *sql.DB, userID, groupName string) (bool, error) {
	return CheckGroupMembershipByName(ctx, db, userID, groupName, SystemGroupLangTag)
}

func CheckGroupMembershipByName(ctx context.Context, db *sql.DB, userID, groupName, groupType string) (bool, error) {
	query := `
SELECT ge.state FROM groups g, group_edge ge WHERE g.id = ge.destination_id AND g.lang_tag = $1 AND g.name = $2 
AND ge.source_id = $3 AND ge.state >= 0 AND ge.state <= 2;
`

	params := make([]interface{}, 0, 4)
	params = append(params, groupType)
	params = append(params, groupName)
	params = append(params, userID)
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

	presences, err := nk.StreamUserList(StreamModeEntrant, entrantID.String(), "", matchID.Node, false, true)
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

func GetMatchIDBySessionID(nk runtime.NakamaModule, sessionID uuid.UUID) (matchID MatchID, presence runtime.Presence, err error) {

	presences, err := nk.StreamUserList(StreamModeService, sessionID.String(), "", StreamLabelMatchService, false, true)
	if err != nil {
		return MatchID{}, nil, fmt.Errorf("failed to get stream presences: %w", err)
	}
	if len(presences) == 0 {
		return MatchID{}, nil, ErrMatchNotFound
	}
	presence = presences[0]
	matchID = MatchIDFromStringOrNil(presences[0].GetStatus())
	if !matchID.IsNil() {
		// Verify that the user is actually in the match
		if meta, err := nk.StreamUserGet(StreamModeMatchAuthoritative, matchID.UUID.String(), "", matchID.Node, presence.GetUserId(), presence.GetSessionId()); err != nil || meta == nil {
			return MatchID{}, nil, ErrMatchNotFound
		}
		return matchID, presence, nil
	}

	return MatchID{}, nil, ErrMatchNotFound
}

func GetLobbyGroupID(ctx context.Context, db *sql.DB, userID string) (string, uuid.UUID, error) {
	query := "SELECT value->>'group_id' FROM storage WHERE collection = $1 AND key = $2 and user_id = $3"
	var dbPartyGroupName string
	var found = true
	err := db.QueryRowContext(ctx, query, MatchmakerStorageCollection, MatchmakingConfigStorageKey, userID).Scan(&dbPartyGroupName)
	if err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return "", uuid.Nil, fmt.Errorf("error finding lobby group id: %w", err)
		}
	}
	if !found {
		return "", uuid.Nil, status.Error(codes.NotFound, "lobby group id not found")
	}
	if dbPartyGroupName == "" {
		return "", uuid.Nil, nil
	}
	return dbPartyGroupName, uuid.NewV5(EntrantIDSalt, dbPartyGroupName), nil
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
		return nil, fmt.Errorf("error finding guild groups: %w", err)
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

func KickPlayerFromMatch(ctx context.Context, nk runtime.NakamaModule, matchID MatchID, userID string) error {
	// Get the user's presences

	label, err := MatchLabelByID(ctx, nk, matchID)
	if err != nil {
		return fmt.Errorf("failed to get match label: %w", err)
	}

	presences, err := nk.StreamUserList(StreamModeMatchAuthoritative, matchID.UUID.String(), "", matchID.Node, false, true)
	if err != nil {
		return fmt.Errorf("failed to get stream presences: %w", err)
	}

	for _, presence := range presences {
		if presence.GetUserId() != userID {
			continue
		}
		if presence.GetSessionId() == label.Broadcaster.SessionID.String() {
			// Do not kick the game server
			continue
		}

		if err = nk.StreamUserKick(StreamModeMatchAuthoritative, matchID.UUID.String(), "", matchID.Node, presence); err != nil {
			return fmt.Errorf("failed to disconnect session `%s`: %w", presence.GetSessionId(), err)
		}

	}

	return nil
}

func DisconnectUserID(ctx context.Context, nk runtime.NakamaModule, userID string, includeGameServers bool) (int, error) {
	// Get the user's presences

	labels := []string{StreamLabelMatchService}
	if includeGameServers {
		labels = append(labels, StreamLabelGameServerService, StreamLabelLoginService)
	}

	cnt := 0
	for _, l := range labels {

		presences, err := nk.StreamUserList(StreamModeService, userID, "", l, false, true)
		if err != nil {
			return 0, fmt.Errorf("failed to get stream presences: %w", err)
		}

		for _, presence := range presences {
			if err = nk.SessionDisconnect(ctx, presence.GetSessionId(), runtime.PresenceReasonDisconnect); err != nil {
				return cnt, fmt.Errorf("failed to disconnect session `%s`: %w", presence.GetSessionId(), err)
			}
			cnt++
		}
	}
	return cnt, nil
}

func GetUserIDByEvrID(ctx context.Context, db *sql.DB, evrID string) (string, error) {
	query := `
	SELECT ud.user_id FROM user_device ud WHERE ud.id = $1`
	var dbUserID string
	var found = true
	err := db.QueryRowContext(ctx, query, evrID).Scan(&dbUserID)
	if err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return "", fmt.Errorf("error finding user ID By Evr ID: %w", err)
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

func GetPartyGroupUserIDs(ctx context.Context, nk runtime.NakamaModule, groupName string) ([]string, error) {
	if groupName == "" {
		return nil, status.Error(codes.InvalidArgument, "user ID is required")
	}

	objs, _, err := nk.StorageIndexList(ctx, SystemUserID, ActivePartyGroupIndex, fmt.Sprintf("+value.group_id:%s", groupName), 100, nil, "")
	if err != nil {
		return nil, fmt.Errorf("error listing party group users: %w", err)
	}

	if len(objs.Objects) == 0 {
		return nil, status.Error(codes.NotFound, "party group not found")
	}

	userIDs := make([]string, 0, len(objs.Objects))

	for _, obj := range objs.Objects {
		if obj.GetUserId() == SystemUserID {
			continue
		}
		userIDs = append(userIDs, obj.GetUserId())
	}

	return userIDs, nil
}

func RuntimeLoggerToZapLogger(logger runtime.Logger) *zap.Logger {
	return logger.(*RuntimeGoLogger).logger
}

func SetNextMatchID(ctx context.Context, nk runtime.NakamaModule, userID string, matchID MatchID, role TeamIndex, hostDiscordID string) error {
	settings, err := LoadMatchmakingSettings(ctx, nk, userID)
	if err != nil {
		return fmt.Errorf("Error loading matchmaking settings: %w", err)
	}

	settings.NextMatchID = matchID
	settings.NextMatchRole = role.String()
	settings.NextMatchDiscordID = hostDiscordID

	if err = StoreMatchmakingSettings(ctx, nk, userID, settings); err != nil {
		return fmt.Errorf("Error storing matchmaking settings: %w", err)
	}

	return nil
}
