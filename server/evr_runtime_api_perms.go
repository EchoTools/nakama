package server

import (
	"context"
	"database/sql"
	"encoding/json"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/internal/intents"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// AfterReadStorageObjectsHook is a hook that runs after reading storage objects.
// It checks if the intent includes storage objects access and retries the request as the system user if any objects are missing from the response.
func AfterReadStorageObjectsHook(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.StorageObjects, in *api.ReadStorageObjectsRequest) error {
	if out == nil || in == nil {
		return nil
	}

	vars, err := intents.SessionVarsFromRuntimeContext(ctx)
	if err != nil {
		logger.Error("Failed to parse session variables from context", zap.Error(err))
		return err
	}

	type objCompact struct {
		Collection string
		Key        string
		UserID     string
	}
	isAuthoritative := false

	if vars != nil && (vars.Intents.StorageObjects || vars.Intents.IsGlobalOperator) {
		isAuthoritative = true
	}

	if isAuthoritative {
		// If the returned value was empty, retry the request as the system user.
		returnedMap := make(map[objCompact]struct{})
		for _, obj := range out.Objects {
			returnedMap[objCompact{
				Collection: obj.Collection,
				Key:        obj.Key,
				UserID:     obj.UserId,
			}] = struct{}{}
		}

		// Find any missing objects in the response.
		ops := make([]*runtime.StorageRead, 0)
		for _, obj := range in.ObjectIds {
			if _, exists := returnedMap[objCompact{
				Collection: obj.Collection,
				Key:        obj.Key,
				UserID:     obj.UserId,
			}]; !exists {
				ops = append(ops, &runtime.StorageRead{
					Collection: obj.Collection,
					Key:        obj.Key,
					UserID:     obj.UserId,
				})
			}
		}
		if len(ops) > 0 {
			// retry as the system user
			objs, err := nk.StorageRead(ctx, ops)
			if err != nil {
				logger.Error("Failed to read storage objects as system user", zap.Error(err))
				return err
			}
			for _, obj := range objs {
				out.Objects = append(out.Objects, &api.StorageObject{
					Collection:      obj.Collection,
					Key:             obj.Key,
					UserId:          obj.UserId,
					Value:           obj.Value,
					Version:         obj.Version,
					PermissionRead:  obj.PermissionRead,
					PermissionWrite: obj.PermissionWrite,
				})
			}
		}
	}

	return nil
}

// checkStorageObjectAuthorization checks if the context has authorization to access storage objects.
// Returns true if authorized (has StorageObjects or IsGlobalOperator intent), false otherwise.

func checkStorageObjectAuthorization(ctx context.Context, logger runtime.Logger) (bool, error) {
	vars, err := intents.SessionVarsFromRuntimeContext(ctx)
	if err != nil {
		logger.Error("Failed to parse session variables from context", zap.Error(err))
		return false, err
	}

	if vars != nil && (vars.Intents.StorageObjects || vars.Intents.IsGlobalOperator || vars.Intents.IsGlobalDeveloper) {
		return true, nil
	}

	return false, nil
}

// BeforeWriteStorageObjectsHook is a hook that runs before writing storage objects.
// It checks if the intent includes storage objects access and blocks the request if not authorized.
// It also validates that matchmaking config writes include a version to prevent overwriting with stale data.
func BeforeWriteStorageObjectsHook(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.WriteStorageObjectsRequest) (*api.WriteStorageObjectsRequest, error) {
	if in == nil {
		return nil, nil
	}

	isAuthorized, err := checkStorageObjectAuthorization(ctx, logger)
	if err != nil {
		return nil, err
	}

	if !isAuthorized {
		// Block the request by returning nil
		return nil, nil
	}

	// Validate matchmaking config writes require a version field
	for _, obj := range in.Objects {
		if obj.Collection == MatchmakerStorageCollection && obj.Key == MatchmakingConfigStorageKey {
			if obj.Version == "" {
				logger.WithFields(map[string]interface{}{
					"collection": obj.Collection,
					"key":        obj.Key,
				}).Warn("Matchmaking config write rejected: version is required to prevent overwriting with stale data")
				return nil, runtime.NewError("Matchmaking config write requires a version. Read the current object first and include the version to prevent overwriting with stale data.", StatusFailedPrecondition)
			}
		}
	}

	return in, nil
}

// BeforeDeleteStorageObjectsHook is a hook that runs before deleting storage objects.
// It checks if the intent includes storage objects access and blocks the request if not authorized.
func BeforeDeleteStorageObjectsHook(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.DeleteStorageObjectsRequest) (*api.DeleteStorageObjectsRequest, error) {
	if in == nil {
		return nil, nil
	}

	isAuthorized, err := checkStorageObjectAuthorization(ctx, logger)
	if err != nil {
		return nil, err
	}

	if !isAuthorized {
		// Block the request by returning nil
		return nil, nil
	}

	return in, nil
}

// BeforeListMatchesHook is a hook that runs before listing matches.
func BeforeListMatchesHook(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.ListMatchesRequest) (*api.ListMatchesRequest, error) {
	if in == nil {
		return nil, nil
	}

	// TODO: Probably better to just restrict the matches that are returned.
	in.Limit = wrapperspb.Int32(min(in.GetLimit().GetValue(), 100)) // Enforce a maximum limit of 100 matches.
	// Set the default values for the request.
	in.Label = nil                           // Clear the label to prevent any filtering by label.
	in.Authoritative = wrapperspb.Bool(true) // Set authoritative to false to allow listing all matches.

	vars, err := intents.SessionVarsFromRuntimeContext(ctx)
	if err != nil {
		logger.Error("Failed to parse session variables from context", zap.Error(err))
		return nil, err
	}

	// Append restrictions to the query
	var query string
	if in.GetQuery() != nil {
		query = in.GetQuery().GetValue()
	}

	if !vars.Intents.Matches {
		// No limits
	} else if vars.Intents.GuildMatches {
		// Limit to guild matches only (including private matches).
		// TODO: Implement filtering logic to limit results to guild matches only, including private matches.
	} else {
		// Limit to public matches only.
		query = query + ` +label.mode:public`
	}

	in.Query = &wrapperspb.StringValue{Value: query}
	// Otherwise, we return an empty response.
	return in, nil
}

func RestrictAPIFunctionAccess[T any](beforeFn func(fn func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in T) (T, error)) error) error {

	noopFn := func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in T) (v T, err error) {
		return v, nil
	}

	if err := beforeFn(noopFn); err != nil {
		return err
	}
	return nil
}

func registerAPIGuards(initializer runtime.Initializer) error {
	rtMessages := []string{
		"ChannelJoin",
		"ChannelLeave",
		"ChannelMessageRemove",
		"ChannelMessageSend",
		"ChannelMessageUpdate",
		"MatchCreate",
		"MatchDataSend",
		"MatchJoin",
		"MatchLeave",
		"MatchmakerAdd",
		"MatchmakerRemove",
		"PartyAccept",
		"PartyClose",
		"PartyCreate",
		"PartyDataSend",
		"PartyJoin",
		"PartyJoinRequestList",
		"PartyLeave",
		"PartyMatchmakerAdd",
		"PartyMatchmakerRemove",
		"PartyPromote",
		"PartyRemove",
		"StatusFollow",
		"StatusUnfollow",
		"StatusUpdate",
		//"Ping",
		//"Pong",
		//"Rpc",
	}

	RestrictAPIFunctionAccess(initializer.RegisterBeforeAddGroupUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateApple)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateCustom)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateDevice)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateEmail)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateFacebook)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateFacebookInstantGame)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateGameCenter)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateGoogle)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeAuthenticateSteam)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeBanGroupUsers)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeBlockFriends)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeCreateGroup)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteFriends)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteGroup)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteLeaderboardRecord)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteNotifications)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteStorageObjects)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDeleteTournamentRecord)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDemoteGroupUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeDemoteGroupUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeGetSubscription)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeGetUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeImportFacebookFriends)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeImportSteamFriends)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeJoinGroup)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeJoinTournament)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeKickGroupUsers)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLeaveGroup)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkApple)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkCustom)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkDevice)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkEmail)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkFacebook)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkFacebookInstantGame)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkGameCenter)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkGoogle)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeLinkSteam)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListChannelMessages)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListFriends)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListGroups)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListGroupUsers)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListLeaderboardRecords)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListLeaderboardRecordsAroundOwner)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListMatches)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListNotifications)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListStorageObjects)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListSubscriptions)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeListTournamentRecords)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListTournamentRecordsAroundOwner)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListTournaments)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeListUserGroups)
	RestrictAPIFunctionAccess(initializer.RegisterBeforePromoteGroupUsers)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeReadStorageObjects)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeSessionLogout)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeSessionRefresh)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkApple)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkCustom)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkDevice)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkEmail)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkFacebook)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkFacebookInstantGame)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkGameCenter)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkGoogle)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUnlinkSteam)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUpdateAccount)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeUpdateGroup)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeValidatePurchaseApple)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeValidatePurchaseFacebookInstant)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeValidatePurchaseGoogle)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeValidatePurchaseHuawei)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeValidateSubscriptionApple)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeValidateSubscriptionGoogle)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeWriteLeaderboardRecord)
	//RestrictAPIFunctionAccess(initializer.RegisterBeforeWriteStorageObjects)
	RestrictAPIFunctionAccess(initializer.RegisterBeforeWriteTournamentRecord)

	for _, rtMessage := range rtMessages {
		if err := initializer.RegisterBeforeRt(rtMessage, func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, envelope *rtapi.Envelope) (*rtapi.Envelope, error) {
			return nil, nil
		}); err != nil {
			return err
		}
	}

	return nil
}

// AfterListMatchesHook filters match data based on user permissions.
// - Complete match data is shown only for guilds where user has audit role OR is the server host
// - Global operators can see client IPs
// - Public match label data is available for any guild the user is a member of
// - Suspended users see no match data except for servers they run
func AfterListMatchesHook(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, out *api.MatchList, in *api.ListMatchesRequest) error {
	if out == nil || len(out.Matches) == 0 {
		return nil
	}

	// Get user ID from context
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		// No user context - return minimal data
		return filterMatchesForAnonymous(out)
	}

	// Get session vars for permission checks
	vars, err := intents.SessionVarsFromRuntimeContext(ctx)
	if err != nil {
		logger.Error("Failed to parse session variables", zap.Error(err))
		return err
	}

	isGlobalOperatorIntent := vars != nil && vars.Intents.IsGlobalOperator

	// Fetch user's guild memberships and roles
	userGroups, _, err := nk.UserGroupsList(ctx, userID, 100, nil, "")
	if err != nil {
		logger.Error("Failed to fetch user groups", zap.Error(err))
		return err
	}

	// Build a map of group IDs the user is a member of
	memberGroupIDs := make(map[string]bool)
	isGlobalOperatorMember := false
	for _, ug := range userGroups {
		if ug.State != nil && ug.State.Value <= 2 { // Member, Admin, or Superadmin
			memberGroupIDs[ug.Group.Id] = true
			if ug.Group != nil && ug.Group.Name == GroupGlobalOperators {
				isGlobalOperatorMember = true
			}
		}
	}

	isGlobalOperator := isGlobalOperatorIntent || isGlobalOperatorMember

	// Fetch guild groups for role checking
	groupIDs := make([]string, 0, len(memberGroupIDs))
	for gid := range memberGroupIDs {
		groupIDs = append(groupIDs, gid)
	}

	guildGroups := make(map[string]*GuildGroup)
	suspendedGroupIDs := make(map[string]bool)

	if len(groupIDs) > 0 {
		ggs, err := GuildGroupsLoad(ctx, nk, groupIDs)
		if err != nil {
			logger.Warn("Failed to load guild groups", zap.Error(err))
		} else {
			for _, gg := range ggs {
				guildGroups[gg.ID().String()] = gg
				if gg.IsSuspended(userID, nil) {
					suspendedGroupIDs[gg.ID().String()] = true
				}
			}
		}
	}

	// Filter each match based on permissions
	filteredMatches := make([]*api.Match, 0, len(out.Matches))
	for _, match := range out.Matches {
		filteredMatch := filterMatchForUser(match, userID, isGlobalOperator, guildGroups, suspendedGroupIDs, memberGroupIDs)
		if filteredMatch != nil {
			filteredMatches = append(filteredMatches, filteredMatch)
		}
	}

	out.Matches = filteredMatches
	return nil
}

func filterMatchesForAnonymous(out *api.MatchList) error {
	// For anonymous users, only show public match data with minimal info
	filteredMatches := make([]*api.Match, 0, len(out.Matches))
	for _, match := range out.Matches {
		// Parse the label
		label := &MatchLabel{}
		if err := json.Unmarshal([]byte(match.Label.Value), label); err != nil {
			continue
		}

		// Only show public matches
		if !label.IsPublic() {
			continue
		}

		// Create a sanitized public label
		publicLabel := label.PublicView()
		labelJSON, _ := json.Marshal(publicLabel)

		filteredMatches = append(filteredMatches, &api.Match{
			MatchId:       match.MatchId,
			Authoritative: match.Authoritative,
			Label:         &wrapperspb.StringValue{Value: string(labelJSON)},
			Size:          match.Size,
			TickRate:      match.TickRate,
			HandlerName:   match.HandlerName,
		})
	}
	out.Matches = filteredMatches
	return nil
}

func filterMatchForUser(match *api.Match, userID string, isGlobalOperator bool, guildGroups map[string]*GuildGroup, suspendedGroupIDs, memberGroupIDs map[string]bool) *api.Match {
	// Parse the label
	label := &MatchLabel{}
	if err := json.Unmarshal([]byte(match.Label.Value), label); err != nil {
		return nil
	}

	// Check if user is the server host
	isServerHost := label.GameServer != nil && label.GameServer.OperatorID.String() == userID

	// Get the guild ID for this match
	var matchGroupID string
	if label.GroupID != nil {
		matchGroupID = label.GroupID.String()
	}

	// Check if user is suspended from this guild
	isSuspended := suspendedGroupIDs[matchGroupID]

	// If suspended and not the server host, hide the match
	if isSuspended && !isServerHost {
		return nil
	}

	// Check if user is a member of this guild
	isMember := memberGroupIDs[matchGroupID]

	// Check if user has audit role in this guild
	isAuditor := false
	if gg, ok := guildGroups[matchGroupID]; ok {
		isAuditor = gg.IsAuditor(userID) || gg.IsEnforcer(userID)
	}

	// Global operators can see all match data
	canSeeFullData := isGlobalOperator || isServerHost || isAuditor

	// Can see client IPs only if global operator
	canSeeClientIPs := isGlobalOperator

	// Create filtered label based on permissions
	var labelJSON []byte
	var err error
	if canSeeFullData {
		filteredLabel := createFullMatchLabel(label, canSeeClientIPs)
		labelJSON, err = json.Marshal(filteredLabel)
	} else if isMember || label.IsPublic() {
		publicLabel := createPublicMatchLabel(label)
		labelJSON, err = json.Marshal(publicLabel)
	} else {
		// User can't see this match at all
		return nil
	}

	if err != nil {
		return nil
	}

	return &api.Match{
		MatchId:       match.MatchId,
		Authoritative: match.Authoritative,
		Label:         &wrapperspb.StringValue{Value: string(labelJSON)},
		Size:          match.Size,
		TickRate:      match.TickRate,
		HandlerName:   match.HandlerName,
	}
}

// PublicMatchLabel contains only publicly visible match information
type PublicMatchLabel struct {
	ID          MatchID   `json:"id"`
	Open        bool      `json:"open"`
	LobbyType   LobbyType `json:"lobby_type"`
	Mode        string    `json:"mode,omitempty"`
	Level       string    `json:"level,omitempty"`
	Size        int       `json:"size"`
	PlayerCount int       `json:"player_count"`
	TeamSize    int       `json:"team_size,omitempty"`
	MaxSize     int       `json:"limit,omitempty"`
	PlayerLimit int       `json:"player_limit,omitempty"`
	GroupID     string    `json:"group_id,omitempty"`
	StartTime   string    `json:"start_time,omitempty"`
	CreatedAt   string    `json:"created_at,omitempty"`
	RatingMu    float64   `json:"rating_mu,omitempty"`
	// Server location info (no IPs)
	ServerRegion  string `json:"server_region,omitempty"`
	ServerCity    string `json:"server_city,omitempty"`
	ServerCountry string `json:"server_country,omitempty"`
}

func createPublicMatchLabel(label *MatchLabel) *PublicMatchLabel {
	public := &PublicMatchLabel{
		ID:          label.ID,
		Open:        label.Open,
		LobbyType:   label.LobbyType,
		Mode:        label.Mode.String(),
		Level:       label.Level.String(),
		Size:        label.Size,
		PlayerCount: label.PlayerCount,
		TeamSize:    label.TeamSize,
		MaxSize:     label.MaxSize,
		PlayerLimit: label.PlayerLimit,
		RatingMu:    label.RatingMu,
	}

	if label.GroupID != nil {
		public.GroupID = label.GroupID.String()
	}

	if !label.StartTime.IsZero() {
		public.StartTime = label.StartTime.Format("2006-01-02T15:04:05Z07:00")
	}
	if !label.CreatedAt.IsZero() {
		public.CreatedAt = label.CreatedAt.Format("2006-01-02T15:04:05Z07:00")
	}

	// Add server location but not IP
	if label.GameServer != nil {
		public.ServerRegion = label.GameServer.DefaultRegion
		public.ServerCity = label.GameServer.City
		public.ServerCountry = label.GameServer.CountryCode
	}

	return public
}

func createFullMatchLabel(label *MatchLabel, includeClientIPs bool) *MatchLabel {
	// Return the full label, but potentially redact IPs if not authorized
	if !includeClientIPs && label.GameServer != nil {
		// Create a copy with redacted endpoint
		labelCopy := *label
		serverCopy := *label.GameServer
		serverCopy.Endpoint = evr.Endpoint{} // Redact the endpoint (contains IPs)
		labelCopy.GameServer = &serverCopy
		return &labelCopy
	}
	return label
}
