package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/url"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/internal/intents"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

const (
	// Websocket Error Codes
	StatusOK                 = 0  // StatusOK indicates a successful operation.
	StatusCanceled           = 1  // StatusCanceled indicates the operation was canceled.
	StatusUnknown            = 2  // StatusUnknown indicates an unknown error occurred.
	StatusInvalidArgument    = 3  // StatusInvalidArgument indicates an invalid argument was provided.
	StatusDeadlineExceeded   = 4  // StatusDeadlineExceeded indicates the operation exceeded the deadline.
	StatusNotFound           = 5  // StatusNotFound indicates the requested resource was not found.
	StatusAlreadyExists      = 6  // StatusAlreadyExists indicates the resource already exists.
	StatusPermissionDenied   = 7  // StatusPermissionDenied indicates the operation was denied due to insufficient permissions.
	StatusResourceExhausted  = 8  // StatusResourceExhausted indicates the resource has been exhausted.
	StatusFailedPrecondition = 9  // StatusFailedPrecondition indicates a precondition for the operation was not met.
	StatusAborted            = 10 // StatusAborted indicates the operation was aborted.
	StatusOutOfRange         = 11 // StatusOutOfRange indicates a value is out of range.
	StatusUnimplemented      = 12 // StatusUnimplemented indicates the operation is not implemented.
	StatusInternalError      = 13 // StatusInternal indicates an internal server error occurred.
	StatusUnavailable        = 14 // StatusUnavailable indicates the service is currently unavailable.
	StatusDataLoss           = 15 // StatusDataLoss indicates a loss of data occurred.
	StatusUnauthenticated    = 16 // StatusUnauthenticated indicates the request lacks valid authentication credentials.
)

type (
	TimeRFC3339 time.Time
)

func (t TimeRFC3339) MarshalText() ([]byte, error) {
	return []byte(time.Time(t).Format(time.RFC3339)), nil
}

func (t *TimeRFC3339) UnmarshalText(data []byte) error {
	parsed, err := time.Parse(time.RFC3339, string(data))
	if err != nil {
		return err
	}
	*t = TimeRFC3339(parsed)
	return nil
}

func (h *RPCHandler) MatchListPublicRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// The public view is cached.
	cachedResponse, _, found := h.responseCache.Get("match:public")
	if found {
		return cachedResponse.(string), nil
	}

	minSize := 1
	query := "*"
	matches, err := nk.MatchList(ctx, 1000, true, "", &minSize, nil, query)
	if err != nil {
		return "", runtime.NewError("Failed to list matches", StatusInternalError)
	}
	data, err := json.Marshal(matches)
	if err != nil {
		return "", runtime.NewError("Failed to marshal matches", StatusInternalError)
	}
	_ = data

	matchmakingTicketsByGroupID := make(map[string]map[string]int)

	cursor := ""
	var groups []*api.Group

	for {
		groups, cursor, err = nk.GroupsList(ctx, "", GuildGroupLangTag, nil, nil, 100, "")
		if err != nil {
			return "", runtime.NewError("Failed to list guild groups", StatusInternalError)
		}
		groups = append(groups, groups...)

		if cursor == "" {
			break
		}
	}

	for _, group := range groups {
		groupID := group.GetId()
		presences, err := nk.StreamUserList(StreamModeMatchmaking, groupID, "", "", false, true)
		if err != nil {
			return "", runtime.NewError("Failed to list matchmaker tickets", StatusInternalError)
		}
		matchmakingTicketsByGroupID[groupID] = make(map[string]int)
		for _, presence := range presences {
			s := LobbySessionParameters{}
			if err := json.Unmarshal([]byte(presence.GetStatus()), &s); err != nil {
				logger.WithFields(map[string]interface{}{
					"err":    err,
					"status": presence.GetStatus(),
				}).Warn("Failed to unmarshal lobby session parameters")
				continue
			}
			matchmakingTicketsByGroupID[groupID][s.Mode.String()] += s.GetPartySize()
		}
		if len(matchmakingTicketsByGroupID[groupID]) == 0 {
			delete(matchmakingTicketsByGroupID, groupID)
		}
	}

	playerCount := 0
	gameServers := make([]*GameServerPresence, 0, len(matches))
	labels := make([]*MatchLabel, 0, len(matches))
	playerSet := make(map[string]bool) // To avoid counting players multiple times
	for _, m := range matches {
		l := &MatchLabel{}
		if err := json.Unmarshal([]byte(m.GetLabel().GetValue()), l); err != nil {
			return "", fmt.Errorf("failed to unmarshal match label: %s", err.Error())
		}

		v := l.PublicView()

		for _, p := range v.Players {
			if _, exists := playerSet[p.UserID]; !exists {
				playerSet[p.UserID] = true
				playerCount++
			}
		}

		gameServers = append(gameServers, v.GameServer)
		if v.LobbyType != UnassignedLobby {
			labels = append(labels, v)
		}
	}

	// Sort matches by the latest pubs first, then the latest privates
	sort.SliceStable(labels, func(i, j int) bool {
		if labels[i].LobbyType == PublicLobby && labels[j].LobbyType != PublicLobby {
			return true
		} else if labels[i].LobbyType != PublicLobby && labels[j].LobbyType == PublicLobby {
			return false
		}
		return labels[i].StartTime.After(labels[j].StartTime)
	})

	response := struct {
		Uptime                      int64                     `json:"uptime_mins"`
		UpdateTime                  TimeRFC3339               `json:"update_time"`
		LobbySessionCount           int                       `json:"lobby_session_count"`
		GameServerCount             int                       `json:"gameserver_count"`
		PlayerCount                 int                       `json:"player_count"`
		MatchmakingTicketsByGroupID map[string]map[string]int `json:"active_matchmaking_counts"`
		Labels                      []*MatchLabel             `json:"labels"`
		GameServers                 []*GameServerPresence     `json:"gameservers"`
	}{
		Uptime:                      int64(time.Since(nakamaStartTime).Minutes()),
		UpdateTime:                  TimeRFC3339(time.Now().UTC()),
		Labels:                      labels,
		LobbySessionCount:           len(labels),
		GameServers:                 gameServers,
		GameServerCount:             len(gameServers),
		PlayerCount:                 playerCount,
		MatchmakingTicketsByGroupID: matchmakingTicketsByGroupID,
	}

	data, err = json.MarshalIndent(response, "", "  ")
	if err != nil {
		return "", runtime.NewError("Failed to marshal response", StatusInternalError)
	}

	payload = string(data)

	// Update the cache
	h.responseCache.Set("match:public", payload, 3*time.Second)

	return payload, nil

}

type MatchRpcRequest struct {
	MatchIDs []MatchID `json:"match_ids"`
	Query    string    `json:"query"`
}

type MatchRpcResponse struct {
	SystemStartTime string            `json:"system_start_time"`
	Timestamp       string            `json:"timestamp"`
	Labels          []json.RawMessage `json:"labels"`
}

func (r MatchRpcResponse) String() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

func MembershipsFromSessionVars(vars map[string]string) (map[string]guildGroupPermissions, error) {
	memberships := make(map[string]guildGroupPermissions)
	if data, ok := vars["memberships"]; ok {
		var bitsets map[string]uint64
		if err := json.Unmarshal([]byte(data), &bitsets); err != nil {
			return nil, err
		}

		for groupID, bitset := range bitsets {
			m := guildGroupPermissions{}
			m.FromUint64(bitset)
			memberships[groupID] = m
		}
	}
	return memberships, nil
}

func MatchRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var (
		err         error
		memberships map[string]guildGroupPermissions
		fullAccess  bool = false
	)

	if userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string); ok {
		if fullAccess, err = CheckSystemGroupMembership(ctx, db, userID, GroupGlobalPrivateDataAccess); err != nil {
			return "", fmt.Errorf("failed to check system group membership: %s", err.Error())
		} else {
			// Unpack the bitsets from the session token
			vars, ok := ctx.Value(runtime.RUNTIME_CTX_VARS).(map[string]string)
			if ok {
				memberships, err = MembershipsFromSessionVars(vars)
				if err != nil {
					return "", fmt.Errorf("failed to get memberships from session vars: %s", err.Error())
				}
			}
		}
	}

	request := &MatchRpcRequest{}
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), request); err != nil {
			return "", fmt.Errorf("failed to unmarshal payload: %s", err.Error())
		}
	}
	if request.Query == "" {
		request.Query = "*"
	}

	matches := make([]*api.Match, 0, len(request.MatchIDs))

	if request.MatchIDs != nil {

		// Get specific match(es)
		for _, id := range request.MatchIDs {
			match, err := nk.MatchGet(ctx, id.String())
			if err != nil {
				return "", fmt.Errorf("failed to get match: %s", err.Error())
			}
			matches = append(matches, match)
		}

	} else {

		// Get all the matches
		matches, err = nk.MatchList(ctx, 1000, true, "", nil, nil, request.Query)
		if err != nil {
			return "", fmt.Errorf("failed to list matches: %s", err.Error())
		}
	}

	labels := make([]json.RawMessage, 0, len(matches))
	var data json.RawMessage
	for _, match := range matches {
		if fullAccess {
			labels = append(labels, json.RawMessage(match.GetLabel().GetValue()))
			continue
		} else {
			label := &MatchLabel{}
			if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
				return "", fmt.Errorf("failed to unmarshal match label: %s", err.Error())
			}

			// Remove sensitive data
			label.GameServer.Latitude = 0
			label.GameServer.Longitude = 0
			label.GameServer.ServerID = 0
			label.GameServer.Endpoint = evr.Endpoint{}
			for i, p := range label.Players {
				p.ClientIP = ""
				label.Players[i] = p
			}

			if label.LobbyType == UnassignedLobby {
				for _, id := range label.GameServer.GroupIDs {
					if m, ok := memberships[id.String()]; ok {
						if !m.IsAPIAccess {
							continue
						}
					}
				}
			} else {
				if m, ok := memberships[label.GetGroupID().String()]; ok {
					if !m.IsAPIAccess {
						continue
					}
				}
			}
			data, err = json.Marshal(label.PublicView())
			if err != nil {
				return "", fmt.Errorf("failed to marshal match label: %s", err.Error())
			}
			labels = append(labels, data)
		}
	}
	response := MatchRpcResponse{
		SystemStartTime: nakamaStartTime.UTC().Format(time.RFC3339),
		Timestamp:       time.Now().UTC().Format(time.RFC3339),
		Labels:          labels,
	}

	return response.String(), nil
}

func KickPlayerRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	request := struct {
		UserID string `json:"user_id"`
	}{}

	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", err
	}

	callerID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("authentication required", StatusUnauthenticated)
	}

	guildGroups, err := GuildUserGroupsList(ctx, nk, nil, callerID)
	if err != nil {
		return "", err
	}

	// Get a slice of groupIDs that this user is a moderator for
	var groupIDs []string
	for groupID, g := range guildGroups {
		if g.IsEnforcer(callerID) {
			groupIDs = append(groupIDs, groupID)
		}
	}

	if len(groupIDs) == 0 {
		return "", runtime.NewError("unauthorized: not a moderator for any guilds.", StatusPermissionDenied)
	}

	// Get the match of the user
	presences, err := nk.StreamUserList(StreamModeService, request.UserID, "", StreamLabelMatchService, false, true)
	if err != nil {
		return "", err
	}

	if len(presences) == 0 {
		return "", fmt.Errorf("user not in a match")
	}

	sessionIDs := make([]string, 0, len(presences))
	for _, p := range presences {
		matchID := MatchIDFromStringOrNil(p.GetStatus())
		if matchID.IsNil() {
			continue
		}
		label, err := MatchLabelByID(ctx, nk, matchID)
		if err != nil {
			return "", err
		}
		if slices.Contains(groupIDs, label.GetGroupID().String()) {
			sessionIDs = append(sessionIDs, p.GetSessionId())
		}
	}

	if len(sessionIDs) == 0 {
		return "", fmt.Errorf("user not in a match that the caller is a moderator for")
	}

	for _, sessionID := range sessionIDs {
		if err := nk.SessionDisconnect(ctx, sessionID, runtime.PresenceReasonDisconnect); err != nil {
			return "", err
		}
	}

	response := struct {
		SessionIDs []string `json:"session_ids"`
	}{
		SessionIDs: sessionIDs,
	}

	data, err := json.Marshal(response)
	if err != nil {
		return "", err
	}

	return string(data), nil
}

/*
func MatchmakingStatusRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk_ runtime.NakamaModule, payload string) (string, error) {
	// The public view is cached for 5 seconds.
	cachedResponse, _, found := h.responseCache.Get("matchmaking:status")
	if found {
		return cachedResponse.(string), nil
	}

	nk := nk_.(*RuntimeGoNakamaModule)

	subcontext := uuid.NewV5(uuid.Nil, "matchmaking").String()
	presences, err := nk.StreamUserList(StreamModeService, "", subcontext, "", true, true)
	if err != nil {
		return "", err
	}

	tickets := make([]Ticket, len(presences))

	for _, presence := range presences {
		status := presence.GetStatus()
		ticketMeta := &TicketMeta{}
		if err := json.Unmarshal([]byte(status), ticketMeta); err != nil {
			return "", err
		}
		tickets = append(tickets, *ticketMeta)
	}

	response := &matchmakingStatusResponse{
		Tickets: tickets,
	}

	jsonData, err := json.Marshal(response)
	if err != nil {
		return "", err
	}

	// Update the cache
	h.responseCache.Set("matchmaking:status", string(jsonData), 5*time.Second)

	return string(jsonData), nil
}
*/
/*
func ExportAccountData(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

		// get the user id from the payload or the urlparam
		// if the payload is empty, the user id is in the urlparam

		// Get the RUNTIME_CTX_QUERY_PARAMS from the ctx
		queryParams, ok := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string)
		if !ok {
			return "", fmt.Errorf("failed to get RUNTIME_CTX_QUERY_PARAMS from context")
		}
		logger.Info("Query params: %v", queryParams)

		// Get the user's account data.
		account, err := nk.AccountGetId(ctx, runtime.DefaultSession, runtime.DefaultUserID)
		if err != nil {
			return "", err
		}

		// Convert the account data to a jsonN object.
		accountData, err := json.Marshal(account)
		if err != nil {
			return "", err
		}

		return string(accountData), nil
	}
*/

type DiscordSignInRpcRequest struct {
	Code             string `json:"code"`
	OAuthRedirectUrl string `json:"oauth_redirect_url"`
}

type DiscordSignInRpcResponse struct {
	SessionToken    string `json:"sessionToken"`
	DiscordUsername string `json:"discordUsername"`
}

// DiscordSignInRpc is a function that handles the Discord sign-in RPC.
// It takes in the context, logger, database connection, Nakama module, and payload as parameters.
// The function exchanges the provided code for an access token, creates a Discord client,
// retrieves the Discord user, checks if a user exists with the Discord ID as a Nakama username,
// creates a user if necessary, gets the account data, relinks the custom ID if necessary,
// writes the access token to storage, updates the account information, generates a session token,
// stores the JWT in the user's metadata, and returns the session token and Discord username as a jsonN response.
// If any error occurs during the process, an error message is returned.
func DiscordSignInRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	logger.WithField("payload", payload).Info("DiscordSignInRpc")

	vars, _ := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)
	clientId := vars["DISCORD_CLIENT_ID"]
	clientSecret := vars["DISCORD_CLIENT_SECRET"]
	nkUserId := ""

	// Parse the payload into a LoginRequest object
	var request DiscordSignInRpcRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		logger.WithField("err", err).WithField("payload", payload).Error("Unable to unmarshal payload")
		return "", runtime.NewError("Unable to unmarshal payload", StatusInvalidArgument)
	}
	if request.Code == "" {
		logger.Error("DiscordSignInRpc: Code is empty")
		return "", runtime.NewError("Code is empty", StatusInvalidArgument)
	}
	if request.OAuthRedirectUrl == "" {
		logger.Error("DiscordSignInRpc: OAuthRedirectUrl is empty")
		return "", runtime.NewError("OAuthRedirectUrl is empty", StatusInvalidArgument)
	}

	// Exchange the code for an access token
	accessToken, err := ExchangeCodeForAccessToken(logger, request.Code, clientId, clientSecret, request.OAuthRedirectUrl)
	if err != nil {
		logger.WithField("err", err).Error("Unable to exchange code for access token")
		return "", runtime.NewError("Unable to exchange code for access token", StatusInternalError)
	}

	// Create a Discord client
	discord, err := discordgo.New("Bearer " + accessToken.AccessToken)
	if err != nil {
		logger.WithField("err", err).Error("Unable to create Discord client")
		return "", runtime.NewError("Unable to create Discord client", StatusInternalError)
	}

	// Get the Discord user
	user, err := discord.User("@me")
	if err != nil {
		logger.WithField("err", err).Error("Unable to get Discord user")
		return "", runtime.NewError("Unable to get Discord user", StatusInternalError)
	}

	// Authenticate/create an account.
	nkUserId, _, _, err = nk.AuthenticateCustom(ctx, user.ID, user.Username, true)
	if err != nil {
		return "", runtime.NewError("Unable to create user", StatusInternalError)
	}

	// Store the discord token.
	err = WriteAccessTokenToStorage(ctx, logger, nk, nkUserId, accessToken)
	if err != nil {
		logger.WithField("err", err).Error("Unable to write access token to storage")
		return "", runtime.NewError("Unable to write access token to storage", StatusInternalError)
	}
	logger.WithField("user.Username", user.Username).Info("DiscordSignInRpc: Wrote access token to storage")

	expiry := time.Now().UTC().Unix() + 15*60 // 15 minutes
	// Generate a session token for the user to use to authenticate for device linking
	sessionToken, _, err := nk.AuthenticateTokenGenerate(nkUserId, user.Username, expiry, nil)
	if err != nil {
		logger.WithField("err", err).Error("Unable to generate session token")
		return "", runtime.NewError("Unable to generate session token", StatusInternalError)
	}

	// store the jwt in the user's metadata so we can verify it later

	response := DiscordSignInRpcResponse{
		SessionToken:    sessionToken,
		DiscordUsername: user.Username,
	}

	responsejson, err := json.Marshal(response)
	if err != nil {
		return "", runtime.NewError(fmt.Sprintf("error marshalling LoginSuccess response: %v", err), StatusInternalError)
	}

	return string(responsejson), nil
}

// LinkDeviceRpc is a function that handles the linking of a device to a user account.
// It takes in the context, logger, database connection, Nakama module, and payload as parameters.
// The payload should be a jsonN string containing the session token and link code.
// It returns an empty string and an error.
// The function performs the following steps:
// 1. Unmarshalls the payload to extract the session token and link code.
// 2. Validates the session token and retrieves the UID from it.
// 3. Retrieves the link ticket from storage using the link code.
// 4. Verifies the session token using the link ticket's device auth token.
// 5. Retrieves the user account using the UID.
// 6. Links the device to the user account.
// 7. Deletes the link ticket from storage.

type LinkDeviceRpcRequest struct {
	SessionToken string `json:"sessionToken"`
	LinkCode     string `json:"linkCode"`
}

// LinkDeviceRpc is a function that handles the linking of a device to a user account.
// It takes in the context, logger, database connection, Nakama module, and payload as parameters.
// The payload should be a jsonN string containing the session token and link code.
// It returns an empty string and an error.
func LinkDeviceRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Extract environment variables from the context
	vars, _ := ctx.Value(runtime.RUNTIME_CTX_ENV).(map[string]string)

	// Unmarshal the payload into a LinkDeviceRpcRequest struct
	var request LinkDeviceRpcRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		logger.WithField("err", err).WithField("payload", payload).Error("Unable to unmarshal payload")
		return "", runtime.NewError("Unable to unmarshal payload", StatusInvalidArgument)
	}

	// Validate the session token and link code
	if request.SessionToken == "" {
		logger.Error("linkDeviceRpc: SessionToken is empty")
		return "", runtime.NewError("SessionToken is empty", StatusInvalidArgument)
	}
	if request.LinkCode == "" {
		logger.Error("linkDeviceRpc: LinkCode is empty")
		return "", runtime.NewError("LinkCode is empty", StatusInvalidArgument)
	}

	// Verify the session token and extract the user ID
	token, err := verifySignedJWT(request.SessionToken, vars["SESSION_ENCRYPTION_KEY"])
	if err != nil {
		logger.WithField("err", err).Error("Unable to verify session token")
		return "", runtime.NewError("Unable to verify session token", StatusInternalError)
	}
	uid := token.Claims.(jwt.MapClaims)["uid"].(string)

	// Exchange the link code for an auth token and remove the link code
	ticket, err := ExchangeLinkCode(ctx, nk, logger, request.LinkCode)
	if err != nil {
		return "", err
	}

	// Link the device to the user account
	if err := nk.LinkDevice(ctx, uid, ticket.XPID.Token()); err != nil {
		logger.WithField("err", err).Error("Unable to link device")
		return "", runtime.NewError("Unable to link device", StatusInternalError)
	}

	// Return an empty string and nil error on successful execution
	return "", nil
}

type LinkUserIdDeviceRpcRequest struct {
	UserID   string `json:"userId"`
	LinkCode string `json:"code"`
}

// LinkUserIdDeviceRpc is a function that links a device to a user account using the username and link code.
// It takes in the context, logger, database connection, Nakama module, and payload as parameters.
// The payload should be a jsonN string containing the username and link code.
// It returns a string indicating the status of the operation and an error.
func LinkUserIdDeviceRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Unmarshal the payload into a LinkUserIdDeviceRpcRequest struct
	var request LinkUserIdDeviceRpcRequest
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		logger.WithField("err", err).WithField("payload", payload).Error("Unable to unmarshal payload")
		return "", runtime.NewError("Unable to unmarshal payload", StatusInvalidArgument)
	}

	// Validate the userId and link code
	if request.UserID == "" {
		logger.Error("LinkUserIdDeviceRpc: Username is empty")
		return "", runtime.NewError("Username is empty", StatusInvalidArgument)
	}
	if request.LinkCode == "" {
		logger.Error("LinkUserIdDeviceRpc: LinkCode is empty")
		return "", runtime.NewError("LinkCode is empty", StatusInvalidArgument)
	}

	// Verify the userId and extract the Nakama user ID
	logger.WithField("Username", request.UserID).Info("Verifying userId")
	userIds, err := nk.UsersGetId(ctx, []string{request.UserID}, nil)
	if err != nil || len(userIds) == 0 {
		logger.WithField("err", err).Error("Unable to verify userId")
		return "fail", runtime.NewError("Unable to verify userId", StatusNotFound)
	}
	userId := userIds[0].GetId()

	// Exchange the link code for an auth token and remove the link code
	ticket, err := ExchangeLinkCode(ctx, nk, logger, request.LinkCode)
	if err != nil {
		return "fail", err
	}

	// Link the device to the user account
	if err := nk.LinkDevice(ctx, userId, ticket.XPID.Token()); err != nil {
		logger.WithField("err", err).Error("Unable to link device")
		return "", runtime.NewError("Unable to link device", StatusInternalError)
	}

	// Return "success" and nil error on successful execution
	return `{"success": true}`, nil
}

func LinkingAppRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	return "Hello Sir.", nil
}

type ServiceStatusData struct {
	IsActive bool                   `json:"is_active"`
	Statuses []ServiceStatusService `json:"statuses"`
}

func (s ServiceStatusData) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection: "Service",
		Key:        "status",
	}
}

func (s ServiceStatusData) SetStorageMeta(meta StorableMetadata) {
	// ServiceStatusData doesn't track version, so nothing to set
}

func (s ServiceStatusData) String() string {
	data, _ := json.Marshal(s.Statuses)
	return string(data)
}

type ServiceStatusService struct {
	ServiceID string `json:"serviceid"` // "services" or "news"
	Available bool   `json:"available"`
	Message   string `json:"message"`
}

// the this format is for the `/status/services,news?env=live&projectid=rad14` request
func (h *RPCHandler) ServiceStatusRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	if cachedResponse, _, found := h.responseCache.Get("server-status"); found {
		return cachedResponse.(string), nil
	}

	statusData := &ServiceStatusData{}

	if err := StorableRead(ctx, nk, SystemUserID, statusData, true); err != nil {
		return "", err
	}
	response := ""
	if statusData.IsActive {
		response = statusData.String()
	} else {
		response = ServiceSettings().serviceStatusMessage
	}

	h.responseCache.Set("server-status", response, 60*time.Second)

	return response, nil
}

type ImportLoadoutRpcRequest struct {
	Loadouts []evr.CosmeticLoadout `json:"loadouts"`
}

type ImportLoadoutRpcResponse struct {
	LoadoutIDs []string `json:"loadout_ids"`
}

func (r *ImportLoadoutRpcResponse) String() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

func ImportLoadoutsRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Import communinty generated outfits (loadouts)

	request := &ImportLoadoutRpcRequest{}
	if err := json.Unmarshal([]byte(payload), request); err != nil {
		return "", err
	}

	response := &ImportLoadoutRpcResponse{
		LoadoutIDs: make([]string, 0),
	}
	// Loop through the loadouts and write them to storage
	for _, loadout := range request.Loadouts {
		value, err := json.Marshal(loadout)
		if err != nil {
			return "", err
		}

		// Create a hash of the loadout to use as the key
		hash := fnv.New64a()
		hash.Write(value)
		key := fmt.Sprintf("%d", hash.Sum64())

		response.LoadoutIDs = append(response.LoadoutIDs, key)

		data := &StoredCosmeticLoadout{
			LoadoutID: key,
			Loadout:   loadout,
			UserID:    uuid.Nil.String(),
		}

		value, err = json.Marshal(data)
		if err != nil {
			return "", err
		}

		if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			PermissionRead:  0,
			PermissionWrite: 0,
			Collection:      CosmeticLoadoutCollection,
			Key:             key,
			Value:           string(value),
			UserID:          uuid.Nil.String(),
		}}); err != nil {
			return "", err
		}
	}

	return response.String(), nil

}

/*
type matchmakingStatusRequest struct {
}

	type matchmakingStatusResponse struct {
		Tickets []TicketMeta `json:"tickets"`
	}

	func matchmakingStatusRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
		request := &matchmakingStatusRequest{}
		if payload != "" {
			if err := json.Unmarshal([]byte(payload), request); err != nil {
				return "", err
			}
		}
		subcontext := uuid.NewV5(uuid.Nil, "matchmaking").String()
		presences, err := nk.StreamUserList(StreamModeService, "", subcontext, "", true, true)
		if err != nil {
			return "", err
		}

		tickets := make([]TicketMeta, len(presences))

		for _, presence := range presences {
			status := presence.GetStatus()
			ticketMeta := &TicketMeta{}
			if err := json.Unmarshal([]byte(status), ticketMeta); err != nil {
				return "", err
			}
			tickets = append(tickets, *ticketMeta)
		}

		response := &matchmakingStatusResponse{
			Tickets: tickets,
		}

		jsonData, err := json.Marshal(response)
		if err != nil {
			return "", err
		}

		return string(jsonData), nil
	}
*/
type setMatchmakingStatusRequest struct {
	Enabled bool `json:"enabled"`
}

type BanUserPayload struct {
	UserId string `json:"userId"`
}

func BanUserRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// Check the user calling the RPC has permissions depending on your criteria
	hasPermission := true
	if !hasPermission {
		logger.Error("unprivileged user attempted to use the BanUser RPC")
		return "", runtime.NewError("unauthorized", 7)
	}

	// Extract the payload
	var data BanUserPayload
	if err := json.Unmarshal([]byte(payload), &data); err != nil {
		logger.Error("unable to deserialize payload")
		return "", runtime.NewError("invalid payload", 3)
	}

	// Ban the user
	if err := nk.UsersBanId(ctx, []string{data.UserId}); err != nil {
		logger.Error("unable to ban user")
		return "", runtime.NewError("unable to ban user", 13)
	}

	// Log the user out
	if err := nk.SessionLogout(data.UserId, "", ""); err != nil {
		logger.Error("unable to logout user")
		return "", runtime.NewError("unable to logout user", 13)
	}

	// Get any existing connections by inspecting the notifications stream
	if presences, err := nk.StreamUserList(0, data.UserId, "", "", true, true); err != nil {
		logger.Debug("no active connections found for user")
	} else {
		// For each active connection, disconnect them
		for _, presence := range presences {
			nk.SessionDisconnect(ctx, presence.GetSessionId(), runtime.PresenceReasonDisconnect)
		}
	}

	return "{}", nil
}

type PrepareMatchRPCRequest struct {
	MatchID          MatchID              `json:"id"`                          // Parking match to signal
	Mode             evr.SymbolToken      `json:"mode"`                        // Mode to set the match to
	Level            evr.SymbolToken      `json:"level,omitempty"`             // Level to set the match to
	RequiredFeatures []string             `json:"required_features,omitempty"` // Required features of the broadcaster/clients
	TeamSize         int                  `json:"team_size,omitempty"`         // Team size to set the match to
	Alignments       map[string]TeamIndex `json:"role_alignments,omitempty"`   // Team alignments to set the match to (discord username -> team index))
	GuildID          string               `json:"guild_id,omitempty"`          // Guild ID to set the match to
	StartTime        time.Time            `json:"start_time,omitempty"`        // The time to start the match
	SpawnedBy        string               `json:"spawned_by,omitempty"`        // The discord ID of the user who spawned the match
	MatchLabel       *MatchLabel          `json:"label,omitempty"`             // an EvrMatchState to send (unmodified) as the signal payload
}

// PrepareMatchRPC is a function that prepares a match from a given match ID.
// Global Developers, the operator of the target server, allocators of the target server's specified guilds can prepare matches.
func PrepareMatchRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("authentication required", StatusUnauthenticated)
	}

	request := &PrepareMatchRPCRequest{}
	if err := json.Unmarshal([]byte(payload), request); err != nil {
		return "", runtime.NewError("Failed to unmarshal match request", StatusInvalidArgument)
	}

	if request.GuildID == "" {
		return "", runtime.NewError("guild ID must be specified", StatusInvalidArgument)
	}

	groupID, err := GetGroupIDByGuildID(ctx, db, request.GuildID)
	if err != nil {
		return "", runtime.NewError(err.Error(), StatusInternalError)
	} else if groupID == "" {
		return "", runtime.NewError("guild group not found", StatusNotFound)
	}

	// Validate that this userID has permission to signal this match
	gg, err := GuildGroupLoad(ctx, nk, groupID)
	if err != nil {
		return "", runtime.NewError(err.Error(), StatusInternalError)
	}

	// Validate that the user has permissions to allocate for the guild
	if !gg.HasRole(userID, gg.RoleMap.Allocator) {
		return "", runtime.NewError("user must have the `allocator` in the guild.", StatusPermissionDenied)
	}

	label, err := MatchLabelByID(ctx, nk, request.MatchID)
	if err != nil || label == nil {
		return "", runtime.NewError("Failed to get match label", StatusNotFound)
	}

	// Operators may signal their own game servers.
	// Otherwise, the game server must host for the guild.
	if label.GameServer.OperatorID.String() != userID && !slices.Contains(label.GameServer.GroupIDs, uuid.FromStringOrNil(groupID)) {
		return "", runtime.NewError("game server does not host for that guild.", StatusPermissionDenied)
	}

	if request.SpawnedBy != "" {
		if userID, err := GetUserIDByDiscordID(ctx, db, request.SpawnedBy); err != nil {
			return "", runtime.NewError("Failed to get spawned by userID: "+err.Error(), StatusNotFound)
		} else {
			request.SpawnedBy = userID
		}
	}

	if request.StartTime.Before(time.Now().Add(10 * time.Minute)) {
		request.StartTime = time.Now().Add(10 * time.Minute)
	}

	label = request.MatchLabel

	var settings *MatchSettings
	if label != nil {
		settings = &MatchSettings{
			Mode:             label.Mode,
			TeamSize:         label.TeamSize,
			Level:            label.Level,
			RequiredFeatures: label.RequiredFeatures,
			StartTime:        label.StartTime.UTC(),
			SpawnedBy:        label.SpawnedBy,
			GroupID:          uuid.FromStringOrNil(groupID),
			TeamAlignments:   label.TeamAlignments,
		}
	} else {

		settings = &MatchSettings{
			Mode:             request.Mode.Symbol(),
			TeamSize:         request.TeamSize,
			Level:            request.Level.Symbol(),
			RequiredFeatures: request.RequiredFeatures,
			StartTime:        request.StartTime.UTC(),
			SpawnedBy:        request.SpawnedBy,
			GroupID:          uuid.FromStringOrNil(groupID),
			TeamAlignments:   make(map[string]int, len(request.Alignments)),
		}

		// Translate the discord ID to the nakama ID for the team Alignments
		settings.TeamAlignments = make(map[string]int, len(request.Alignments))
		for discordID, teamIndex := range request.Alignments {
			// Get the nakama ID from the discord ID
			userID, err := GetUserIDByDiscordID(ctx, db, discordID)
			if err != nil {
				return "", runtime.NewError(err.Error(), StatusNotFound)
			}
			settings.TeamAlignments[userID] = int(teamIndex)
		}
	}

	label, err = LobbyPrepareSession(ctx, nk, request.MatchID, settings)
	if err != nil || label == nil {
		return err.Error(), runtime.NewError(err.Error(), StatusInvalidArgument)
	}

	data, err := json.MarshalIndent(label, "", "  ")
	if err != nil {
		return "", runtime.NewError(err.Error(), StatusInternalError)
	}

	logger.WithFields(map[string]any{
		"mid":      request.MatchID,
		"preparer": userID,
		"state":    string(data),
	}).Info("Match prepared")

	return string(data), nil
}

type AuthenticatePasswordRequest struct {
	UserID       string `json:"user_id"`
	DiscordID    string `json:"discord_id"`
	Username     string `json:"username"`
	Password     string `json:"password"`
	RefreshToken string `json:"refresh_token"`
	IntentStr    string `json:"intents"`
}

var ErrAuthenticateFailed = runtime.NewError("authentication failed", StatusUnauthenticated)
var RPCErrInvalidRequest = runtime.NewError("invalid request", StatusInvalidArgument)

func AuthenticatePasswordRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, _nk runtime.NakamaModule, payload string) (string, error) {
	nk := _nk.(*RuntimeGoNakamaModule)

	if payload == "" {
		return "", RPCErrInvalidRequest
	}

	request := AuthenticatePasswordRequest{}
	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Failed to unmarshal request: %v", err), StatusInvalidArgument)
	}

	var err error
	var userID, username, tokenID string
	var tokenIssuedAt int64 = time.Now().UTC().Unix()
	var vars map[string]string
	if request.RefreshToken != "" {
		var userUUID uuid.UUID
		userUUID, username, vars, tokenID, tokenIssuedAt, err = SessionRefresh(ctx, nk.logger, db, nk.config, nk.sessionCache, request.RefreshToken)
		if err != nil {
			return "", err
		}
		userID = userUUID.String()
	} else {

		switch {

		case request.UserID != "":
			userID = request.UserID

		case request.DiscordID != "":
			userID, err = GetUserIDByDiscordID(ctx, db, request.DiscordID)
			if err != nil {
				return "", err
			}
		case request.Username != "":
			users, err := nk.UsersGetUsername(ctx, []string{request.Username})
			if err != nil {
				return "", err
			}
			if len(users) != 1 {
				return "", ErrAuthenticateFailed
			}
			userID = users[0].Id

		default:
			return "", RPCErrInvalidRequest
		}

		sessionVars := intents.SessionVars{}

		// If the user requests intents, they must be a global developer
		if request.IntentStr != "" {
			// Check if they have the required intents
			if isMember, err := CheckGroupMembershipByName(ctx, db, userID, GroupGlobalDevelopers, "system"); err != nil {
				return "", err
			} else if isMember {
				sessionVars.Intents.UnmarshalText([]byte(request.IntentStr))
			}
		}
		vars = sessionVars.MarshalVars()

		var account *api.Account
		if account, err = nk.AccountGetId(ctx, userID); err != nil || account == nil {
			return "", ErrAccountNotFound
		}
		if userID, username, _, err = nk.AuthenticateEmail(ctx, account.Email, request.Password, "", false); err != nil {
			return "", err
		}
		tokenID = uuid.Must(uuid.NewV4()).String()

		guildGroups, err := GuildUserGroupsList(ctx, nk, nil, userID)
		if err != nil {
			return "", err
		}

		varMemberships := make(map[string]uint64)

		for id, gg := range guildGroups {
			varMemberships[id] = gg.MembershipBitSet(userID)
		}

		data, _ := json.Marshal(varMemberships)
		vars["memberships"] = string(data)
	}

	token, exp := generateToken(nk.config, tokenID, tokenIssuedAt, userID, username, vars)
	refreshToken, refreshExp := generateRefreshToken(nk.config, tokenID, tokenIssuedAt, userID, username, vars)
	nk.sessionCache.Add(uuid.FromStringOrNil(userID), exp, tokenID, refreshExp, tokenID)
	session := &api.Session{Token: token, RefreshToken: refreshToken}

	response, err := json.Marshal(session)
	if err != nil {
		return "", runtime.NewError(fmt.Errorf("error marshalling response: %w", err).Error(), StatusInvalidArgument)
	}
	return string(response), nil
}

type AccountLookupRPCResponse struct {
	ID          uuid.UUID     `json:"id"`
	DiscordID   string        `json:"discord_id"`
	Username    string        `json:"username"`
	DisplayName string        `json:"display_name"`
	AvatarURL   string        `json:"avatar_url"`
	IPQSData    *IPQSResponse `json:"ipqs_data,omitempty"`
}

type AccountLookupRequest struct {
	Username    string    `json:"username"`     // discord/nakama username
	UserID      uuid.UUID `json:"user_id"`      // nakama user uuid
	DiscordID   string    `json:"discord_id"`   // discord ID (snowflake)
	XPID        string    `json:"xp_id"`        // OVR-ORG-123412341234
	DisplayName string    `json:"display_name"` // active display name (probably unreliable)
}

func (r *AccountLookupRequest) CacheKey() string {
	return fmt.Sprintf("%s:%s:%s:%s:%s", r.Username, r.UserID, r.DiscordID, r.XPID, r.DisplayName)
}

// parseAccountLookupQueryParams extracts and validates query parameters into an AccountLookupRequest
func parseAccountLookupQueryParams(queryParams map[string][]string) (*AccountLookupRequest, error) {
	request := &AccountLookupRequest{}

	// extract the username from the query string
	if username, ok := queryParams["username"]; ok && len(username) > 0 {
		request.Username = username[0]
	}

	// extract the discordID from the query string
	if discordID, ok := queryParams["discord_id"]; ok && len(discordID) > 0 {
		request.DiscordID = discordID[0]
	}

	// extract the userID from the query string
	if userID, ok := queryParams["user_id"]; ok && len(userID) > 0 {
		uid, err := uuid.FromString(userID[0])
		if err != nil {
			return nil, runtime.NewError("invalid user id", StatusInvalidArgument)
		}
		request.UserID = uid
	}

	// extract the display_name from the query string
	if displayName, ok := queryParams["display_name"]; ok && len(displayName) > 0 {
		request.DisplayName = displayName[0]
	}

	// extract the xp_id from the query string
	if xpID, ok := queryParams["xp_id"]; ok && len(xpID) > 0 {
		if parsedXPID, err := evr.ParseEvrId(xpID[0]); err != nil {
			return nil, runtime.NewError("invalid xp_id", StatusInvalidArgument)
		} else {
			request.XPID = parsedXPID.String()
		}
	}

	return request, nil
}

// lookupUserID performs the actual user lookup based on the request parameters
func lookupUserID(ctx context.Context, db *sql.DB, nk runtime.NakamaModule, request *AccountLookupRequest) (string, error) {
	switch {
	case !request.UserID.IsNil():
		return request.UserID.String(), nil

	case request.XPID != "":
		return GetUserIDByDeviceID(ctx, db, request.XPID)

	case request.Username != "":
		users, err := nk.UsersGetUsername(ctx, []string{request.Username})
		if err != nil {
			return "", err
		}
		if len(users) != 1 {
			return "", runtime.NewError("user not found", StatusNotFound)
		}
		return users[0].Id, nil

	case request.DiscordID != "":
		return GetUserIDByDiscordID(ctx, db, request.DiscordID)

	case request.DisplayName != "":
		ownerMap, err := DisplayNameOwnerSearch(ctx, nk, []string{request.DisplayName})
		if err != nil {
			return "", err
		}
		if len(ownerMap) == 0 || len(ownerMap[request.DisplayName]) == 0 {
			return "", runtime.NewError("user not found", StatusNotFound)
		}
		userIDs := ownerMap[request.DisplayName]
		if len(userIDs) > 1 {
			return "", runtime.NewError(fmt.Sprintf("multiple users found with display name %s", request.DisplayName), StatusInvalidArgument)
		}
		return userIDs[0], nil

	default:
		return "", runtime.NewError("no valid lookup parameter provided", StatusInvalidArgument)
	}
}

func (h *RPCHandler) AccountLookupRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var request *AccountLookupRequest
	var err error

	// Parse request from JSON payload or query parameters
	if payload != "" {
		request = &AccountLookupRequest{}
		if err := json.Unmarshal([]byte(payload), request); err != nil {
			return "", err
		}
	} else {
		queryParameters, ok := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string)
		if !ok || len(queryParameters) == 0 {
			return "", runtime.NewError("no lookup parameters provided", StatusInvalidArgument)
		}

		request, err = parseAccountLookupQueryParams(queryParameters)
		if err != nil {
			return "", err
		}
	}

	includePrivate := false

	callerID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if ok {
		if ok, err := CheckSystemGroupMembership(ctx, db, callerID, GroupGlobalPrivateDataAccess); err != nil {
			logger.Warn("Error checking system group membership for private data access:", err)
		} else {
			includePrivate = ok
		}
	}

	if !includePrivate {
		// check the cache
		if cachedResponse, _, found := h.responseCache.Get(request.CacheKey()); found {
			return cachedResponse.(string), nil
		}
	}

	// Perform user lookup
	userID, err := lookupUserID(ctx, db, nk, request)
	if err != nil {
		return "", err
	}

	// Get the user's account data.
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return "", err
	}

	response := AccountLookupRPCResponse{
		ID:          uuid.FromStringOrNil(account.User.Id),
		Username:    account.User.Username,
		DiscordID:   account.CustomId,
		DisplayName: account.User.DisplayName,
		AvatarURL:   account.User.AvatarUrl,
	}

	// Convert the account data to a json object.
	jsonResponse, err := json.Marshal(response)
	if err != nil {
		return "", err
	}

	if !includePrivate {
		// Cache the response
		h.responseCache.Set(request.CacheKey(), string(jsonResponse), 5*time.Minute)
	}

	return string(jsonResponse), nil
}

func parseURLParams(params url.Values, target any) error {
	val := reflect.ValueOf(target).Elem()
	typ := val.Type()

	for i := range typ.NumField() {
		field := typ.Field(i)

		// Use the json tag if it exists, otherwise use the field name

		paramName := field.Tag.Get("json")
		paramName, _, _ = strings.Cut(paramName, ",")
		if paramName == "" {
			paramName = field.Name
		}
		paramValue := params.Get(paramName)

		if paramValue == "" {
			continue
		}

		fieldValue := val.Field(i)
		switch fieldValue.Kind() {
		case reflect.String:
			fieldValue.SetString(paramValue)
		case reflect.Int, reflect.Int64:
			intValue, err := strconv.ParseInt(paramValue, 10, 64)
			if err != nil {
				return fmt.Errorf("failed to parse int field %s: %w", field.Name, err)
			}
			fieldValue.SetInt(intValue)
		case reflect.Int32:
			intValue, err := strconv.ParseInt(paramValue, 10, 32)
			if err != nil {
				return fmt.Errorf("failed to parse int field %s: %w", field.Name, err)
			}
			fieldValue.SetInt(intValue)
		case reflect.Float64:
			floatValue, err := strconv.ParseFloat(paramValue, 64)
			if err != nil {
				return fmt.Errorf("failed to parse float field %s: %w", field.Name, err)
			}
			fieldValue.SetFloat(floatValue)
		case reflect.Bool:
			boolValue, err := strconv.ParseBool(paramValue)
			if err != nil {
				return fmt.Errorf("failed to parse bool field %s: %w", field.Name, err)
			}
			fieldValue.SetBool(boolValue)
		default:
			// Get the underlying type of the field
			fieldType := fieldValue.Type()
			if fieldType.Kind() == reflect.Ptr {
				fieldType = fieldType.Elem()
			}

			switch fieldValue.Interface().(type) {
			case time.Time:
				// Parse the field as a string and then convert it to a time.Time
				timeValue, err := time.Parse(time.RFC3339, paramValue)
				if err != nil {
					return fmt.Errorf("failed to parse time field %s: %w", field.Name, err)
				}
				fieldValue.Set(reflect.ValueOf(timeValue))
			case evr.Symbol:
				// Parse the field as a string and then convert it to a Symbol
				symbolValue := evr.ToSymbol(paramValue)
				if symbolValue.IsNil() {
					return fmt.Errorf("failed to parse symbol field %s", field.Name)
				}
				// Set the field value
				fieldValue.Set(reflect.ValueOf(symbolValue))

			case evr.EvrId:
				// Parse the field as a string and then convert it to an EvrId
				evrIdValue, err := evr.ParseEvrId(paramValue)
				if err != nil {
					return fmt.Errorf("failed to parse evr id field %s: %w", field.Name, err)
				}

				fieldValue.Set(reflect.ValueOf(evrIdValue))

			default:
				return fmt.Errorf("unsupported field type %s", fieldType.Name())
			}

		}
	}

	return nil
}

func parseRequest(ctx context.Context, payload string, request any) error {

	// Parse Payload
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), request); err != nil {
			return err
		}

		// Parse Query Parameters
	} else if params := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string); len(params) > 0 {

		if err := parseURLParams(params, request); err != nil {
			return fmt.Errorf("error parsing query parameters: %w", err)
		}
	}

	return nil
}

type AccountSearchRequest struct {
	DisplayNamePattern string `json:"display_name"`
	Limit              int    `json:"limit"`
}

type AccountSearchResponse struct {
	Cursor               *string                `json:"cursor,omitempty"`
	DisplayNameMatchList []DisplayNameMatchItem `json:"display_name_matches"`
}

type DisplayNameMatchItem struct {
	DisplayName string    `json:"display_name"`
	UserID      string    `json:"user_id"`
	GroupID     string    `json:"group_id"`
	UpdatedAt   time.Time `json:"updated_at"`
}

func AccountSearchRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	request := &AccountSearchRequest{}
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), &request); err != nil {
			return "", err
		}
	} else {

		queryParameters := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string)

		if len(queryParameters) > 0 {

			for k, v := range queryParameters {
				switch k {
				case "display_name":
					request.DisplayNamePattern = v[0]
				case "limit":
					limit, err := strconv.ParseUint(v[0], 10, 64)
					if err != nil {
						return "", runtime.NewError("invalid limit", StatusInvalidArgument)
					}
					request.Limit = int(limit)
				}
			}
		}
	}

	if request.Limit == 0 {
		request.Limit = 10
	}

	if request.DisplayNamePattern == "" {
		return "", runtime.NewError("Search pattern name is empty", StatusInvalidArgument)
	}

	limit := min(request.Limit, 1000)

	matches, err := DisplayNameCacheRegexSearch(ctx, nk, request.DisplayNamePattern, limit)
	if err != nil {
		return "", runtime.NewError(err.Error(), StatusInternalError)
	}

	results := make([]DisplayNameMatchItem, 0, len(matches))
	for matchUserID, byGroup := range matches {

		for groupID, entries := range byGroup {
			for n, t := range entries {
				displayNameL := strings.ToLower(n)
				if strings.Contains(displayNameL, request.DisplayNamePattern) {
					results = append(results, DisplayNameMatchItem{
						DisplayName: n,
						UserID:      matchUserID,
						GroupID:     groupID,
						UpdatedAt:   t,
					})
				}
			}
		}
	}

	sort.Slice(results, func(i, j int) bool {
		return results[i].UpdatedAt.After(results[j].UpdatedAt)
	})

	if len(results) > limit {
		// Sort by updated at

		results = results[:limit]
	}

	responseData, err := json.Marshal(&AccountSearchResponse{
		DisplayNameMatchList: results,
	})
	if err != nil {
		return "", runtime.NewError("Failed to marshal response", StatusInternalError)
	}

	return string(responseData), nil
}

type StreamJoinRequest struct {
	Mode        uint8  `json:"mode"`
	Subject     string `json:"subject"`
	Subcontext  string `json:"subcontext"`
	UserID      string `json:"user_id"`
	SessionID   string `json:"session_id"`
	Label       string `json:"label"`
	Hidden      bool   `json:"hidden"`
	Persistance bool   `json:"persistance"`
	Status      string `json:"status"`
}

type StreamJoinResponse struct {
	Success   bool        `json:"success"`
	Presences []*Presence `json:"presences"`
}

func (r *StreamJoinResponse) String() string {
	b, _ := json.Marshal(r)
	return string(b)
}

func StreamJoinRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	request := &StreamJoinRequest{}
	if err := json.Unmarshal([]byte(payload), request); err != nil {
		return "", err
	}

	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("No user ID in context", StatusUnauthenticated)
	}
	if request.UserID == "" {
		request.UserID = userID
	}

	sessionID, ok := ctx.Value(runtime.RUNTIME_CTX_SESSION_ID).(string)
	if !ok {
		return "", runtime.NewError("No session ID in context", StatusUnauthenticated)
	}

	if request.SessionID == "" {
		request.SessionID = sessionID
	}

	success, err := nk.StreamUserJoin(request.Mode, request.Subject, request.Subcontext, request.Label, request.UserID, request.SessionID, request.Hidden, request.Persistance, request.Status)
	if err != nil {
		return "", err
	}

	if !success {
		return "", runtime.NewError("Failed to join matchmaker stream", StatusInternalError)
	}

	presences, err := nk.StreamUserList(request.Mode, request.Subject, request.Subcontext, request.Label, false, true)
	if err != nil {
		return "", err
	}

	responsePresences := make([]*Presence, 0, len(presences))

	for _, p := range presences {
		var parameters LobbySessionParameters
		if err := json.Unmarshal([]byte(p.GetStatus()), &parameters); err != nil {
			return "", err
		}

		if presence, ok := p.(*Presence); ok {
			responsePresences = append(responsePresences, presence)
		}

	}

	response := &StreamJoinResponse{
		Success:   true,
		Presences: responsePresences,
	}

	return response.String(), nil
}

func MatchmakerCandidatesRPCFactory(sbmm *SkillBasedMatchmaker) func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	return func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

		/*
			if data, _, found := h.responseCache.Get("matchmaker_candidates"); found {
				return data.(string), nil
			}
		*/

		candidates, matches := sbmm.GetLatestResult()
		if len(candidates) > 10000 {

			logger.WithFields(map[string]any{
				"count": len(candidates),
			}).Warn("Matchmaker candidates list is too long, truncating to 1000 entries")

			candidates = candidates[:10000]

		}
		response := map[string][][]runtime.MatchmakerEntry{
			"candidates": candidates,
			"matches":    matches,
		}

		data, err := json.Marshal(response)
		if err != nil {
			return "", err
		}
		// Cache the result

		/*
			h.responseCache.Set("matchmaker_candidates", data, 30*time.Second)
		*/
		return string(data), nil
	}

}

type ServerScoreRPCRequest struct {
	PlayerRTTs   []float64 `json:"rtts"`
	MinRTT       int       `json:"min_rtt"`
	MaxRTT       int       `json:"max_rtt"`
	ThresholdRTT int       `json:"threshold_rtt"`
}

type ServerScoreRPCResponse struct {
	Score float64 `json:"score"`
}

func (r *ServerScoreRPCResponse) String() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

func ServerScoreRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	request := &ServerScoreRPCRequest{}

	// Get the pings from the query string
	queryParameters := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string)

	if payload != "" {
		if err := json.Unmarshal([]byte(payload), request); err != nil {
			return "", err
		}

	} else if len(queryParameters) == 0 {
		return "", runtime.NewError("No query parameters", StatusInvalidArgument)
	}
	var err error
	// extract the p from the query string
	if p, ok := queryParameters["rtts"]; ok {
		// Split by comma
		s := strings.Split(p[0], ",")
		rttstrs := make([]string, 0, len(s))
		for _, v := range s {
			rttstrs = append(rttstrs, strings.TrimSpace(v))
		}

		request.PlayerRTTs = make([]float64, 0, len(rttstrs))
		for _, rttstr := range rttstrs {
			rtt, err := strconv.ParseFloat(rttstr, 64)
			if err != nil {
				return "", err
			}
			request.PlayerRTTs = append(request.PlayerRTTs, rtt)
		}

		if p, ok := queryParameters["min_rtt"]; ok {
			request.MinRTT, err = strconv.Atoi(p[0])
			if err != nil {
				return "", fmt.Errorf("Failed to parse min_rtt: %w", err)
			}
		}

		if p, ok := queryParameters["max_rtt"]; ok {
			request.MaxRTT, err = strconv.Atoi(p[0])
			if err != nil {
				return "", fmt.Errorf("Failed to parse max_rtt: %w", err)
			}
		}

		if p, ok := queryParameters["threshold_rtt"]; ok {
			request.ThresholdRTT, err = strconv.Atoi(p[0])
			if err != nil {
				return "", fmt.Errorf("Failed to parse threshold_rtt: %w", err)
			}
		}

	}
	// Split the rtts in two groups
	teams := make([][]int, 2)
	for i, rtt := range request.PlayerRTTs {
		teams[i%2] = append(teams[i%2], int(rtt))
	}

	score, err := CalculateServerScore(teams, request.MinRTT, request.MaxRTT, request.ThresholdRTT)
	if err != nil {
		return "", fmt.Errorf("Failed to calculate server score: %w", err)
	}

	response := &ServerScoreRPCResponse{
		Score: score,
	}

	return response.String(), nil
}

type ServerScoresRPCRequest struct {
	DiscordIDs   []string `json:"discord_ids"`
	MinRTT       int      `json:"min_rtt"`
	MaxRTT       int      `json:"max_rtt"`
	ThresholdRTT int      `json:"threshold_rtt"`
}

type ServerScoresRPCResponse struct {
	Scores map[string]float64 `json:"scores"`
}

func (r *ServerScoresRPCResponse) String() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

func ServerScoresRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	request := &ServerScoresRPCRequest{}

	// Get the pings from the query string
	queryParameters := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string)

	if payload != "" {
		if err := json.Unmarshal([]byte(payload), request); err != nil {
			return "", err
		}

	} else if len(queryParameters) == 0 {
		return "", runtime.NewError("No query parameters", StatusInvalidArgument)
	}
	var err error
	// extract the p from the query string
	if p, ok := queryParameters["discord_ids"]; ok {

		s := strings.Split(p[0], ",")
		for _, v := range s {
			request.DiscordIDs = append(request.DiscordIDs, strings.TrimSpace(v))
		}
	}
	if p, ok := queryParameters["min_rtt"]; ok {
		request.MinRTT, err = strconv.Atoi(p[0])
		if err != nil {
			return "", fmt.Errorf("failed to parse min_rtt: %w", err)
		}
	}

	if p, ok := queryParameters["max_rtt"]; ok {
		request.MaxRTT, err = strconv.Atoi(p[0])
		if err != nil {
			return "", fmt.Errorf("failed to parse max_rtt: %w", err)
		}
	}

	if p, ok := queryParameters["threshold_rtt"]; ok {
		request.ThresholdRTT, err = strconv.Atoi(p[0])
		if err != nil {
			return "", fmt.Errorf("failed to parse threshold_rtt: %w", err)
		}
	}

	latencies := make(map[string][]int, 2)

	for _, discordID := range request.DiscordIDs {
		userID, err := GetUserIDByDiscordID(ctx, db, discordID)
		if err != nil {
			return "", err
		}

		latencyHistory := &LatencyHistory{}
		if err := StorableRead(ctx, nk, userID, latencyHistory, false); err != nil {
			return "", fmt.Errorf("failed to read latency history: %w", err)
		}

		for ip, latency := range latencyHistory.AverageRTTs(false) {
			latencies[ip] = append(latencies[ip], latency)
		}
	}

	scoresByServer := make(map[string]float64, len(latencies))
	for server, latencies := range latencies {
		if len(latencies) != len(request.DiscordIDs) {
			scoresByServer[server] = 999
		}

		teams := make([][]int, 2)
		for i, rtt := range latencies {
			teams[i%2] = append(teams[i%2], rtt)
		}

		score, err := CalculateServerScore(teams, request.MinRTT, request.MaxRTT, request.ThresholdRTT)
		if err != nil {
			return "", fmt.Errorf("failed to calculate server score: %w", err)
		}

		scoresByServer[server] = score
	}

	response := &ServerScoresRPCResponse{
		Scores: scoresByServer,
	}

	return response.String(), nil
}

type UserServerProfileRPCRequest struct {
	UserID    uuid.UUID `json:"user_id"`
	XPID      evr.EvrId `json:"xp_id"`
	DiscordID string    `json:"discord_id"`
	GuildID   string    `json:"guild_id"`
	GroupID   uuid.UUID `json:"group_id"`
}

func UserServerProfileRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	request := &UserServerProfileRPCRequest{}
	if err := parseRequest(ctx, payload, request); err != nil {
		return "", err
	}

	var (
		evrProfile *EVRProfile
		err        error
	)

	switch {
	case !request.UserID.IsNil():

	case !request.XPID.IsNil():
		if userID, err := GetUserIDByDeviceID(ctx, db, request.XPID.String()); err != nil {
			return "", fmt.Errorf("failed to get user ID by xp_id: %w", err)
		} else {
			request.UserID = uuid.FromStringOrNil(userID)
		}
	case request.DiscordID != "":
		if userID, err := GetUserIDByDiscordID(ctx, db, request.DiscordID); err != nil {
			return "", fmt.Errorf("failed to get user ID by discord ID: %w", err)
		} else {
			request.UserID = uuid.FromStringOrNil(userID)
		}
	default:
		return "", runtime.NewError("No user ID specified", StatusInvalidArgument)
	}

	evrProfile, err = EVRProfileLoad(ctx, nk, request.UserID.String())
	if err != nil {
		return "", fmt.Errorf("failed to load EVR profile: %w", err)
	}

	if request.XPID.IsNil() && len(evrProfile.account.Devices) == 0 {
		return "", runtime.NewError("No devices found for user", StatusNotFound)
	} else {
		if xpid, err := evr.ParseEvrId(evrProfile.account.Devices[0].Id); err != nil {
			return "", fmt.Errorf("failed to parse xp_id `%s`: %w", evrProfile.account.Devices[0].Id, err)
		} else {
			request.XPID = *xpid
		}
	}

	switch {
	case !request.GroupID.IsNil():

	case request.GuildID != "":
		if groupID, err := GetGroupIDByGuildID(ctx, db, request.GuildID); err != nil {
			return "", err
		} else {
			request.GroupID = uuid.FromStringOrNil(groupID)
		}
	}

	modes := []evr.Symbol{
		evr.ModeArenaPublic,
		evr.ModeCombatPublic,
		evr.ModeSocialPublic,
		evr.ModeCombatPrivate,
		evr.ModeArenaPrivate,
		evr.ModeSocialPrivate,
	}

	serverProfile, err := NewUserServerProfile(ctx, RuntimeLoggerToZapLogger(logger), db, nk, evrProfile, request.XPID, request.GroupID.String(), modes, 0, evrProfile.GetActiveGroupDisplayName())
	if err != nil {
		return "", fmt.Errorf("failed to get server profile: %w", err)
	}

	data, err := json.MarshalIndent(serverProfile, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal server profile: %w", err)
	}

	return string(data), nil
}
