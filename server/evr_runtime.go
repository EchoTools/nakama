package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/go-redis/redis"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	apiAuth "github.com/heroiclabs/nakama/v3/server/socialauth"
	"github.com/jackc/pgtype"
	"go.mongodb.org/mongo-driver/mongo"
	"go.mongodb.org/mongo-driver/mongo/options"
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
	GroupGlobalOperators         = "Global Operators"
	GroupGlobalTesters           = "Global Testers"
	GroupGlobalBots              = "Global Bots"
	GroupGlobalBadgeAdmins       = "Global Badge Admins"
	GroupGlobalPrivateDataAccess = "Global Private Data Access"
	GroupGlobalRequire2FA        = "Global Require 2FA"
	SystemGroupLangTag           = "system"
	GuildGroupLangTag            = "guild"
)

func InitializeEvrRuntimeModule(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) (err error) {
	// Add the environment variables to the context

	if err := registerAPIGuards(initializer); err != nil {
		return fmt.Errorf("unable to register API guards: %w", err)
	}

	var (
		vars = nk.(*RuntimeGoNakamaModule).config.GetRuntime().Environment
		sbmm = NewSkillBasedMatchmaker()
	)

	// Store skill-based matchmaker globally so it can be connected to the lobby builder
	globalSkillBasedMatchmaker.Store(sbmm)

	// Register hooks
	//if err = initializer.RegisterBeforeReadStorageObjects(BeforeReadStorageObjectsHook); err != nil {
	//		return fmt.Errorf("unable to register AfterReadStorageObjects hook: %w", err)
	//	}
	if err = initializer.RegisterAfterReadStorageObjects(AfterReadStorageObjectsHook); err != nil {
		return fmt.Errorf("unable to register AfterReadStorageObjects hook: %w", err)
	}
	if err = initializer.RegisterBeforeWriteStorageObjects(BeforeWriteStorageObjectsHook); err != nil {
		return fmt.Errorf("unable to register BeforeWriteStorageObjects hook: %w", err)
	}
	if err = initializer.RegisterBeforeDeleteStorageObjects(BeforeDeleteStorageObjectsHook); err != nil {
		return fmt.Errorf("unable to register BeforeDeleteStorageObjects hook: %w", err)
	}

	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_ENV, vars) // ignore lint
	// Initialize the discord bot if the token is set
	if appBotToken, ok := vars["DISCORD_BOT_TOKEN"]; !ok || appBotToken == "" {
		logger.Warn("DISCORD_BOT_TOKEN is not set, Discord bot will not be used")
		panic("Bot token is not set in context.") // TODO: remove bot dependency
	} else {
		if dg, err = discordgo.New("Bot " + appBotToken); err != nil {
			logger.Error("Unable to create bot", zap.Error(err))
			return fmt.Errorf("unable to create discord bot: %w", err)
		}
		dg.StateEnabled = true
	}

	// Register RPC's for device linking
	rpcHandler := NewRPCHandler(ctx, db, dg)
	rpcs := map[string]func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error){
		"account/search":                AccountSearchRPC,
		"account/lookup":                rpcHandler.AccountLookupRPC,
		"account/authenticate/password": AuthenticatePasswordRPC,
		"leaderboard/haystack":          rpcHandler.LeaderboardHaystackRPC,
		"leaderboard/records":           rpcHandler.LeaderboardRecordsListRPC,
		"link/device":                   LinkDeviceRpc,
		"link/usernamedevice":           LinkUserIdDeviceRpc,
		"signin/discord":                DiscordSignInRpc,
		"match/public":                  rpcHandler.MatchListPublicRPC,
		"match":                         MatchRPC,
		"match/prepare":                 PrepareMatchRPC,
		"match/allocate":                AllocateMatchRPC,
		"match/terminate":               shutdownMatchRpc,
		"match/build":                   BuildMatchRPC,
		"player/setnextmatch":           SetNextMatchRPC,
		"player/statistics":             PlayerStatisticsRPC,
		"player/kick":                   KickPlayerRPC,
		"player/profile":                UserServerProfileRPC,
		"player/matchlock":              MatchLockRPC,
		"player/matchlock/status":       GetMatchLockStatusRPC,
		"link":                          LinkingAppRpc,
		"evr/servicestatus":             rpcHandler.ServiceStatusRPC,
		"matchmaking/settings":          MatchmakingSettingsRPC,
		"importloadouts":                ImportLoadoutsRpc,
		"matchmaker/stream":             MatchmakerStreamRPC,
		"matchmaker/state":              MatchmakerStateRPC,
		"matchmaker/candidates":         MatchmakerCandidatesRPCFactory(sbmm),
		"matchmaker/config":             MatchmakerConfigRPC,
		"stream/join":                   StreamJoinRPC,
		"server/score":                  ServerScoreRPC,
		"server/scores":                 ServerScoresRPC,
		"forcecheck":                    CheckForceUserRPC,
		"guildgroup":                    GuildGroupGetRPC,
		//"/v1/storage/game/sourcedb/rad15/json/r14/loading_tips.json": StorageLoadingTipsRPC,
	}
	for name, rpc := range rpcs {
		if err = initializer.RegisterRpc(name, rpc); err != nil {
			return fmt.Errorf("unable to register %s: %w", name, err)
		}
	}

	if db != nil && nk != nil { // Avoid panic's during some automated tests
		go func() {
			if err := RegisterIndexes(initializer); err != nil {
				panic(fmt.Errorf("unable to register indexes: %v", err))
			}
		}()

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
	if err := initializer.RegisterMatch(EVRLobbySessionMatchModule, func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) (runtime.Match, error) {
		return &EvrMatch{}, nil
	}); err != nil {
		return err
	}

	// Register HTTP Handler for the evr/api service
	appAcceptorFn := NewAppAPIAcceptor(ctx, logger, db, nk, initializer)
	if err := initializer.RegisterHttp("/apievr/v1/{id:.*}", appAcceptorFn, http.MethodGet, http.MethodPost); err != nil {
		return fmt.Errorf("unable to register /evr/api service: %w", err)
	}

	// The statistics queue handles inserting match statistics into the leaderboard records
	statisticsQueue := NewStatisticsQueue(logger, db, nk)

	// Initialize the VRML scan queue if the configuration is set
	var vrmlScanQueue *VRMLScanQueue
	if vars["VRML_REDIS_URI"] == "" || vars["VRML_OAUTH_REDIRECT_URL"] == "" || vars["VRML_OAUTH_CLIENT_ID"] == "" {
		logger.Warn("VRML OAuth configuration is not set, VRML verification will not be available.")
	} else {
		redisURI := vars["VRML_REDIS_URI"]
		vrmlOAuthRedirectURL := vars["VRML_OAUTH_REDIRECT_URL"]
		vrmlOAuthClientID := vars["VRML_OAUTH_CLIENT_ID"]
		redisClient, err := connectRedis(ctx, redisURI)
		if err != nil {
			return fmt.Errorf("failed to connect to Redis for VRML scan queue: %w", err)
		}
		if vrmlScanQueue, err = NewVRMLScanQueue(ctx, logger, db, nk, initializer, dg, redisClient, vrmlOAuthRedirectURL, vrmlOAuthClientID); err != nil {
			return fmt.Errorf("failed to create VRML scan queue: %w", err)
		}
	}

	// Register the event dispatch
	eventDispatch, err := NewEventDispatch(ctx, logger, db, nk, initializer, nil, dg, statisticsQueue, vrmlScanQueue)
	if err != nil {
		return fmt.Errorf("unable to create event dispatch: %w", err)
	}

	// Register the event handler
	if err := initializer.RegisterEvent(eventDispatch.eventFn); err != nil {
		return err
	}

	// Register the matchmaking override
	if err := initializer.RegisterMatchmakerOverride(sbmm.EvrMatchmakerFn); err != nil {
		return fmt.Errorf("unable to register matchmaker override: %w", err)
	}

	// Migrate any system level data
	go MigrateSystem(ctx, logger, db, nk)

	// Update the metrics with match data
	go func() {
		<-time.After(15 * time.Second)
		metricsUpdateLoop(ctx, logger, nk.(*RuntimeGoNakamaModule), db)
	}()

	if err := apiAuth.InitModule(ctx, logger, db, nk, initializer); err != nil {
		return fmt.Errorf("unable to initialize social auth module: %w", err)
	}

	// If running in a dev environment, start a simple file server for static content
	if vars["ENABLE_STATIC_HTTP_SERVER"] == "true" {
		fs := http.FileServer(http.Dir("./data/static"))
		if err := initializer.RegisterHttp("/static", http.StripPrefix("/static", fs).ServeHTTP, http.MethodGet); err != nil {
			return fmt.Errorf("unable to register /static/ file server: %w", err)
		}
		logger.Info("Registered /static/ file server for development environment.")
	}

	// Add telemetry stream RPCs
	if err := RegisterTelemetryStreamRPCs(ctx, logger, db, nk, initializer); err != nil {
		return fmt.Errorf("unable to register telemetry stream RPCs: %w", err)
	}

	// Initialize MongoDB client if configured
	var mongoClient *mongo.Client
	if mongoURI, ok := vars["MONGO_URI"]; ok && mongoURI != "" {
		mongoClient, err = connectMongo(ctx, mongoURI)
		if err != nil {
			logger.Warn("Failed to connect to MongoDB, session events will not be available.", zap.Error(err))
		} else {
			logger.Info("Connected to MongoDB for session events")

			// Register session event RPCs
			if err := RegisterSessionEventRPCs(ctx, logger, db, nk, initializer, mongoClient); err != nil {
				return fmt.Errorf("unable to register session event RPCs: %w", err)
			}
		}
	} else {
		logger.Warn("MONGO_URI is not set, session event RPCs will not be available.")
	}

	logger.Info("Initialized runtime module.")
	return nil
}

// connectRedis connects to the Redis server using the provided URL and returns a redis.Client.
func connectRedis(ctx context.Context, redisURL string) (*redis.Client, error) {
	redisOptions, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse Redis URI: %v", err)
	}
	redisClient := redis.NewClient(redisOptions)
	if err := redisClient.WithContext(ctx).Ping().Err(); err != nil {
		// If the connection fails, return an error
		redisClient.Close()
		return nil, fmt.Errorf("failed to connect to Redis: %v", err)
	}
	return redisClient, nil
}

// connectMongo connects to MongoDB using the provided URI and returns a mongo.Client.
func connectMongo(ctx context.Context, mongoURI string) (*mongo.Client, error) {
	clientOptions := options.Client().ApplyURI(mongoURI)

	// Set a timeout for the connection
	connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	client, err := mongo.Connect(connectCtx, clientOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to connect to MongoDB: %w", err)
	}

	// Verify the connection
	if err := client.Ping(connectCtx, nil); err != nil {
		client.Disconnect(ctx)
		return nil, fmt.Errorf("failed to ping MongoDB: %w", err)
	}

	return client, nil
}

func createCoreGroups(ctx context.Context, logger runtime.Logger, _ *sql.DB, nk runtime.NakamaModule, _ runtime.Initializer) error {
	// Create user for use by the discord bot (and core group ownership)
	userId, _, _, err := nk.AuthenticateDevice(ctx, SystemUserID, "discordbot", true)
	if err != nil {
		logger.WithField("err", err).Error("Error creating discordbot user: %v", err)
	}

	coreGroups := []string{
		GroupGlobalDevelopers,
		GroupGlobalOperators,
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

	// Register storage indexes for any Storables
	storables := []StorableIndexer{
		&DisplayNameHistory{},
		&DeveloperApplications{},
		&MatchmakingSettings{},
		&VRMLPlayerSummary{},
		&LoginHistory{},
	}
	for _, s := range storables {
		for _, idx := range s.StorageIndexes() {
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
		if presence.GetSessionId() == label.GameServer.SessionID.String() {
			// Do not kick the game server
			continue
		}

		signal := SignalKickEntrantsPayload{
			UserIDs: []uuid.UUID{uuid.FromStringOrNil(userID)},
		}

		data := NewSignalEnvelope(userID, SignalKickEntrants, signal).String()

		// Signal the match to kick the entrants
		if _, err := nk.MatchSignal(ctx, matchID.String(), data); err != nil {
			return fmt.Errorf("failed to signal match: %w", err)
		}
	}

	return nil
}

func DisconnectUserID(ctx context.Context, nk runtime.NakamaModule, userID string, kickFirst bool, includeLogin bool, includeGameserver bool) (int, error) {

	if kickFirst {
		// Kick the user from any matches they are in
		if matchID, _, err := GetMatchIDBySessionID(nk, uuid.FromStringOrNil(userID)); err == nil && !matchID.IsNil() {
			if err := KickPlayerFromMatch(ctx, nk, matchID, userID); err != nil {
				return 0, fmt.Errorf("failed to kick player from match: %w", err)
			}
		}
	}

	// Get the user's presences
	labels := []string{StreamLabelMatchService}
	if includeLogin {
		labels = append(labels, StreamLabelLoginService)
	} else if includeGameserver {
		labels = append(labels, StreamLabelGameServerService)
	}

	cnt := 0
	for _, l := range labels {

		presences, err := nk.StreamUserList(StreamModeService, userID, "", l, false, true)
		if err != nil {
			return 0, fmt.Errorf("failed to get stream presences: %w", err)
		}

		for _, presence := range presences {

			// Add a delay to allow the match to process the kick
			go func() {
				if kickFirst {
					<-time.After(5 * time.Second)
				}
				if err := nk.SessionDisconnect(ctx, presence.GetSessionId(), runtime.PresenceReasonDisconnect); err != nil {
					// Ignore the error
					return
				}
			}()

			cnt++
		}
	}
	return cnt, nil
}

func GetUserIDByDeviceID(ctx context.Context, db *sql.DB, deviceID string) (string, error) {
	query := `
	SELECT ud.user_id FROM user_device ud WHERE ud.id = $1`
	var dbUserID string
	var found = true
	err := db.QueryRowContext(ctx, query, deviceID).Scan(&dbUserID)
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
