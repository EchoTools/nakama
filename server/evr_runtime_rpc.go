package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"sort"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	jwt "github.com/golang-jwt/jwt/v4"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
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

type Cache struct {
	sync.RWMutex
	Store map[string]*CacheEntry
}

type CacheEntry struct {
	Value     any
	Timestamp time.Time
}

func NewCache() *Cache {
	return &Cache{
		Store: make(map[string]*CacheEntry),
	}
}

func (c *Cache) Set(key string, value any, ttl time.Duration) {
	c.Lock()
	defer c.Unlock()
	c.Store[key] = &CacheEntry{
		Value:     value,
		Timestamp: time.Now(),
	}

	time.AfterFunc(ttl, func() { c.Remove(key) })
}

func (c *Cache) Get(key string) (value any, timestamp time.Time, found bool) {
	c.RLock()
	defer c.RUnlock()
	if entry, found := c.Store[key]; found {
		return entry.Value, entry.Timestamp, true
	}
	return
}

// get if the timestamp is not expired
func (c *Cache) GetIfNotExpired(key string, expiry time.Time) (value any, found bool) {
	value, timestamp, found := c.Get(key)
	if !found || timestamp.After(expiry) {
		return nil, false
	}
	return value, true
}

func (c *Cache) Count() int {
	c.RLock()
	defer c.RUnlock()
	return len(c.Store)
}

func (c *Cache) Clear() {
	c.Lock()
	defer c.Unlock()
	c.Store = make(map[string]*CacheEntry)
}

func (c *Cache) Remove(key string) {
	c.Lock()
	defer c.Unlock()
	delete(c.Store, key)
}

func (c *Cache) Range(f func(key string, value any, timestamp time.Time) bool) {
	c.Lock()
	defer c.Unlock()
	for k, v := range c.Store {
		if !f(k, v.Value, v.Timestamp) {
			break
		}
	}
}

var rpcResponseCache *Cache

func init() {
	rpcResponseCache = NewCache()
}

func MatchListPublicRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// The public view is cached.
	cachedResponse, _, found := rpcResponseCache.Get("match:public")
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
		groups, cursor, err = nk.GroupsList(ctx, "", "guild", nil, nil, 100, "")
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
		presences, err := nk.StreamUserList(StreamModeMatchmaking, groupID, "", "", true, true)
		if err != nil {
			return "", runtime.NewError("Failed to list matchmaker tickets", StatusInternalError)
		}
		matchmakingTicketsByGroupID[groupID] = make(map[string]int)
		for _, presence := range presences {
			s := LobbySessionParameters{}
			if err := json.Unmarshal([]byte(presence.GetStatus()), &s); err != nil {
				return "", runtime.NewError("Failed to unmarshal matchmaker ticket", StatusInternalError)
			}
			matchmakingTicketsByGroupID[groupID][s.Mode.String()] += s.PartySize
		}
		if len(matchmakingTicketsByGroupID[groupID]) == 0 {
			delete(matchmakingTicketsByGroupID, groupID)
		}
	}

	playerCount := 0
	gameServers := make([]*MatchBroadcaster, 0, len(matches))
	labels := make([]*MatchLabel, 0, len(matches))
	for _, m := range matches {
		l := &MatchLabel{}
		if err := json.Unmarshal([]byte(m.GetLabel().GetValue()), l); err != nil {
			return "", runtime.NewError("Failed to unmarshal match label", StatusInternalError)
		}

		v := l.PublicView()

		playerCount += v.PlayerCount
		gameServers = append(gameServers, &v.Broadcaster)
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
		UpdateTime                  TimeRFC3339               `json:"update_time"`
		LobbySessionCount           int                       `json:"lobby_session_count"`
		GameServerCount             int                       `json:"gameserver_count"`
		PlayerCount                 int                       `json:"player_count"`
		MatchmakingTicketsByGroupID map[string]map[string]int `json:"active_matchmaking_counts"`
		Labels                      []*MatchLabel             `json:"labels"`
		GameServers                 []*MatchBroadcaster       `json:"gameservers"`
	}{
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
	rpcResponseCache.Set("match:public", payload, 3*time.Second)

	return payload, nil

}

type MatchRpcRequest struct {
	MatchIDs []MatchID `json:"match_ids"`
	Query    string    `json:"query"`
}

type MatchRpcResponse struct {
	SystemStartTime string        `json:"system_start_time"`
	Timestamp       string        `json:"timestamp"`
	Labels          []*MatchLabel `json:"labels"`
}

func (r MatchRpcResponse) String() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

func MembershipsFromSessionVars(vars map[string]string) (map[string]GuildGroupMembership, error) {
	memberships := make(map[string]GuildGroupMembership)
	if data, ok := vars["memberships"]; ok {
		var bitsets map[string]uint64
		if err := json.Unmarshal([]byte(data), &bitsets); err != nil {
			return nil, err
		}

		for groupID, bitset := range bitsets {
			m := GuildGroupMembership{}
			m.FromUint64(bitset)
			memberships[groupID] = m
		}
	}
	return memberships, nil
}

func MatchRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	var err error

	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("authentication required", StatusUnauthenticated)
	}
	// Unpack the bitsets from the session token
	vars, ok := ctx.Value(runtime.RUNTIME_CTX_VARS).(map[string]string)
	if !ok {
		return "", runtime.NewError("failed to get session vars", StatusInternalError)
	}

	memberships, err := MembershipsFromSessionVars(vars)
	if err != nil {
		return "", runtime.NewError("failed to get memberships", StatusInternalError)
	}

	fullAccess := false

	if fullAccess, err = CheckSystemGroupMembership(ctx, db, userID, GroupGlobalPrivateDataAccess); err != nil {
		return "", runtime.NewError("failed to check group membership", StatusInternalError)
	}

	request := &MatchRpcRequest{}
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), request); err != nil {
			return "", runtime.NewError("Failed to unmarshal match list request", StatusInternalError)
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
				return "", runtime.NewError("Failed to get match", StatusInternalError)
			}
			matches = append(matches, match)
		}

	} else {

		// Get all the matches
		matches, err = nk.MatchList(ctx, 1000, true, "", nil, nil, request.Query)
		if err != nil {
			return "", runtime.NewError("Failed to list matches", StatusInternalError)
		}
	}

	labels := make([]*MatchLabel, 0, len(matches))
	for _, match := range matches {
		// "any" avoids some reflection overhead
		label := &MatchLabel{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
			return "", runtime.NewError("Failed to unmarshal match label", StatusInternalError)
		}

		if fullAccess {
			labels = append(labels, label)
			continue
		} else {
			// Remove sensitive data
			label.Broadcaster.IPinfo = nil
			label.Broadcaster.Endpoint = evr.Endpoint{}
			for i, p := range label.Players {
				p.IPinfo = nil
				p.ClientIP = ""
				label.Players[i] = p
			}
		}

		if label.LobbyType == UnassignedLobby {
			for _, id := range label.Broadcaster.GroupIDs {
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

		labels = append(labels, label.PublicView())
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

	memberships, err := GetGuildGroupMemberships(ctx, nk, callerID)
	if err != nil {
		return "", err
	}

	// Get a slice of groupIDs that this user is a moderator for
	var groupIDs []string
	for groupID, m := range memberships {
		if m.IsModerator {
			groupIDs = append(groupIDs, groupID)
		}
	}

	if len(groupIDs) == 0 {
		return "", runtime.NewError("unauthorized: not a moderator for any guilds.", StatusPermissionDenied)
	}

	// Get the match of the user
	presences, err := nk.StreamUserList(StreamModeService, request.UserID, "", StreamLabelMatchService, true, true)
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
	cachedResponse, _, found := rpcResponseCache.Get("matchmaking:status")
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
	rpcResponseCache.Set("matchmaking:status", string(jsonData), 5*time.Second)

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
	authToken, err := ExchangeLinkCode(ctx, nk, logger, request.LinkCode)
	if err != nil {
		return "", err
	}

	// Link the device to the user account
	if err := nk.LinkDevice(ctx, uid, authToken.Token()); err != nil {
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
	authToken, err := ExchangeLinkCode(ctx, nk, logger, request.LinkCode)
	if err != nil {
		return "fail", err
	}

	// Link the device to the user account
	if err := nk.LinkDevice(ctx, userId, authToken.Token()); err != nil {
		logger.WithField("err", err).Error("Unable to link device")
		return "", runtime.NewError("Unable to link device", StatusInternalError)
	}

	// Return "success" and nil error on successful execution
	return `{"success": true}`, nil
}

func LinkingAppRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	return "Hello Sir.", nil
}

type ServiceStatusResponse struct {
	Services []ServiceStatusService `json:"14466474907882883491"`
	News     []string               `json:"-3980269165826668125"`
}

type ServiceStatusService struct {
	ServiceId int    `json:"serviceid"`
	Available bool   `json:"available"`
	Message   string `json:"message"`
}

func ServiceStatusRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	// /status/services,news?env=live&projectid=rad14
	// Get the serviceStatus object from storage

	payload = `{
		2731580513406714229: [
		  {
			"serviceid": 1,
			"available": true,
			"message": "Service 1 operational."
		  },
		  {
			"serviceid": 2,
			"available": false,
			"message": "Service 2 under maintenance."
		  }
		],
		-3980269165826668125: [
		  {
			"message": "New feature release next week."
		  },
		  {
			"message": "Scheduled downtime for maintenance."
		  }
		]
	  }`

	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: "serviceStatus",
			Key:        "services",
			UserID:     uuid.Nil.String(),
		},
	})
	if err != nil {
		return "", err
	}

	if len(objs) == 0 {
		return "", nil
	}

	return objs[0].Value, nil
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

func setMatchmakingStatusRpc(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	request := &setMatchmakingStatusRequest{}
	if err := json.Unmarshal([]byte(payload), request); err != nil {
		return "", err
	}

	g := GlobalConfig
	g.Lock()
	defer g.Unlock()
	g.rejectMatchmaking = !request.Enabled

	statusJson, err := json.Marshal(request)
	if err != nil {
		return "", err
	}

	return string(statusJson), nil
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

type PrepareMatchRPCResponse struct {
	MatchID       MatchID    `json:"id"`
	MatchLabel    MatchLabel `json:"label"`
	SignalPayload string     `json:"signal_payload"`
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
	matchID := request.MatchID

	if request.GuildID == "" {
		return "", runtime.NewError("guild ID must be specified", StatusInvalidArgument)
	}

	groupID, err := GetGroupIDByGuildID(ctx, db, request.GuildID)
	if err != nil {
		return "", runtime.NewError(err.Error(), StatusInternalError)
	} else if groupID == "" {
		return "", runtime.NewError("guild group not found", StatusNotFound)
	}

	label, err := MatchLabelByID(ctx, nk, matchID)
	if err != nil || label == nil {
		return "", runtime.NewError("Failed to get match label", StatusNotFound)
	}

	// Validate that this userID has permission to signal this match
	md, err := GetGuildGroupMetadata(ctx, db, groupID)
	if err != nil {
		return "", runtime.NewError("Failed to get group metadata: "+err.Error(), StatusInternalError)
	}

	// Operators may signal their own game servers.
	// Otherwise, the game server must host for the guild.
	if label.Broadcaster.OperatorID != userID && !slices.Contains(label.Broadcaster.GroupIDs, uuid.FromStringOrNil(groupID)) {
		return "", runtime.NewError("game server does not host for that guild.", StatusPermissionDenied)
	}

	// Validate that the user has permission to signal the match
	if !slices.Contains(md.RoleCache[md.Roles.Allocator], userID) {
		return "", runtime.NewError("user must have the `allocator` in the guild.", StatusPermissionDenied)
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

	gid := uuid.FromStringOrNil(groupID)
	label = request.MatchLabel
	maxSize := SocialLobbyMaxSize
	if l, ok := LobbySizeByMode[request.Mode.Symbol()]; ok {
		maxSize = l
	}

	if label == nil {
		label = &MatchLabel{
			Mode:             request.Mode.Symbol(),
			TeamSize:         request.TeamSize,
			Level:            request.Level.Symbol(),
			RequiredFeatures: request.RequiredFeatures,
			StartTime:        request.StartTime.UTC(),
			SpawnedBy:        request.SpawnedBy,
			MaxSize:          uint8(maxSize),
			GroupID:          &gid,
		}

		// Translate the discord ID to the nakama ID for the team Alignments
		label.TeamAlignments = make(map[string]int, len(request.Alignments))
		for discordID, teamIndex := range request.Alignments {
			// Get the nakama ID from the discord ID
			userID, err := GetUserIDByDiscordID(ctx, db, discordID)
			if err != nil {
				return "", runtime.NewError(err.Error(), StatusNotFound)
			}
			label.TeamAlignments[userID] = int(teamIndex)
		}
	}

	signalPayload := NewEvrSignal(userID, SignalPrepareSession, label).String()
	errResponse := func(err error) (string, error) {
		response := PrepareMatchRPCResponse{
			MatchID:       matchID,
			SignalPayload: signalPayload,
		}
		data, _ := json.MarshalIndent(response, "", "  ")
		return string(data), runtime.NewError(err.Error(), StatusInvalidArgument)
	}

	// Send the signal
	signalResponsePayload, err := nk.MatchSignal(ctx, matchID.String(), signalPayload)
	if err != nil {
		return errResponse(err)
	}
	signalResponse := SignalResponse{}
	if err := json.Unmarshal([]byte(signalResponsePayload), &signalResponse); err != nil {
		return errResponse(err)
	}
	if !signalResponse.Success {
		return errResponse(fmt.Errorf("failed to signal match: %s", signalResponse.Message))
	}

	// Get the match label
	match, err := nk.MatchGet(ctx, matchID.String())
	if err != nil {
		return errResponse(err)
	}

	state := &MatchLabel{}
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), state); err != nil {
		return errResponse(err)
	}

	data, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return errResponse(err)
	}

	logger.WithFields(map[string]any{
		"mid":      matchID,
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
		return "", err
	}

	var err error
	var userID, username, tokenID string
	var vars map[string]string

	if request.RefreshToken != "" {
		var userUUID uuid.UUID
		userUUID, username, vars, tokenID, err = SessionRefresh(ctx, nk.logger, db, nk.config, nk.sessionCache, request.RefreshToken)
		if err != nil {
			return "", err
		}
		userID = userUUID.String()
	} else {
		vars = make(map[string]string)
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

		var account *api.Account
		if account, err = nk.AccountGetId(ctx, userID); err != nil || account == nil {
			return "", ErrAccountNotFound
		}
		if userID, username, _, err = nk.AuthenticateEmail(ctx, account.Email, request.Password, "", false); err != nil {
			return "", err
		}
		tokenID = uuid.Must(uuid.NewV4()).String()

		memberships, err := GetGuildGroupMemberships(ctx, nk, userID)
		if err != nil {
			return "", err
		}

		varMemberships := make(map[string]uint64)

		for id, membership := range memberships {
			varMemberships[id] = membership.ToUint64()
		}

		data, _ := json.Marshal(varMemberships)
		vars["memberships"] = string(data)
	}

	token, exp := generateToken(nk.config, tokenID, userID, username, vars)
	refreshToken, refreshExp := generateRefreshToken(nk.config, tokenID, userID, username, vars)
	nk.sessionCache.Add(uuid.FromStringOrNil(userID), exp, tokenID, refreshExp, tokenID)
	session := &api.Session{Token: token, RefreshToken: refreshToken}

	response, err := json.Marshal(session)
	if err != nil {
		return "", runtime.NewError(fmt.Errorf("error marshalling response: %w", err).Error(), StatusInvalidArgument)
	}
	return string(response), nil
}

type AccountLookupRPCResponse struct {
	ID          string        `json:"id"`
	DiscordID   string        `json:"discord_id"`
	Username    string        `json:"username"`
	DisplayName string        `json:"display_name"`
	AvatarURL   string        `json:"avatar_url"`
	IPQSData    *IPQSResponse `json:"ipqs_data,omitempty"`
}

func AccountLookupRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	request := struct {
		Username  string `json:"username"`
		UserID    string `json:"user_id"`
		DiscordID string `json:"discord_id"`
	}{}

	if err := json.Unmarshal([]byte(payload), &request); err != nil {
		return "", err
	}

	includePrivate := false

	callerID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("authentication required", StatusUnauthenticated)
	}

	if ok, err := CheckSystemGroupMembership(ctx, db, callerID, GroupGlobalPrivateDataAccess); err != nil {
		includePrivate = false
	} else {
		includePrivate = ok
	}

	var err error
	userID := ""
	switch {
	case request.UserID != "":
		uid, err := uuid.FromString(request.UserID)
		if err != nil {
			return "", runtime.NewError("invalid user id", StatusInvalidArgument)
		}
		userID = uid.String()
	case request.Username != "":
		users, err := nk.UsersGetUsername(ctx, []string{request.Username})
		if err != nil {
			return "", err
		}
		if len(users) != 1 {
			return "", nil
		}
		userID = users[0].Id

	case request.DiscordID != "":
		userID, err = GetUserIDByDiscordID(ctx, db, request.DiscordID)
		if err != nil {
			return "", err
		}
	}

	// Get the user's account data.
	account, err := nk.AccountGetId(ctx, userID)
	if err != nil {
		return "", err
	}

	response := AccountLookupRPCResponse{
		ID:          account.User.Id,
		Username:    account.User.Username,
		DiscordID:   account.CustomId,
		DisplayName: account.User.DisplayName,
		AvatarURL:   account.User.AvatarUrl,
	}

	if includePrivate {
		// Check if the player is online
		if presences, err := nk.StreamUserList(StreamModeNotifications, userID, "", "", true, true); err != nil {
			logger.Error("Failed to get user presence: %v", err)
		} else if len(presences) > 0 {

			nkgo := nk.(*RuntimeGoNakamaModule)
			sessionID := uuid.FromStringOrNil(presences[0].GetSessionId())
			session := nkgo.sessionRegistry.Get(sessionID)
			if session != nil {
				response.IPQSData, _ = ipqsCache.Load(session.ClientIP())
			}
		}
	}

	// Convert the account data to a json object.
	jsonResponse, err := json.Marshal(response)
	if err != nil {
		return "", err
	}

	return string(jsonResponse), nil
}

type SetNextMatchRPCRequestPayload struct {
	UserID    string   `json:"user_id"`
	DiscordID string   `json:"discord_id"`
	MatchID   *MatchID `json:"match_id"`
}

type SetNextMatchRPCResponsePayload struct {
	Label *MatchLabel `json:"label,omitempty"`
}

func (r *SetNextMatchRPCResponsePayload) String() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

type PlayerStatsRPCRequest struct {
	UserID    string `json:"user_id"`
	DiscordID string `json:"discord_id"`
}

type PlayerStatsRPCResponse struct {
	Stats *evr.PlayerStatistics `json:"stats"`
}

func (r *PlayerStatsRPCResponse) String() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

func PlayerStatisticsRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	request := &PlayerStatsRPCRequest{}
	if payload != "" {
		if err := json.Unmarshal([]byte(payload), request); err != nil {
			return "", err
		}
	}

	queryParameters := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string)

	if len(queryParameters) > 0 {
		// extract the discordID from the query string
		if discordID, ok := queryParameters["discord_id"]; ok {
			request.DiscordID = discordID[0]
		}
		// extract the userID from the query string
		if userID, ok := queryParameters["user_id"]; ok {
			request.UserID = userID[0]
		}
	}

	var userID string
	if request.UserID != "" {
		userID = request.UserID
	} else if request.DiscordID != "" {
		var err error
		if userID, err = GetUserIDByDiscordID(ctx, db, request.DiscordID); err != nil {
			return "", err
		}
	}

	if userID == "" {
		return "", runtime.NewError("user not found", StatusNotFound)
	}

	stats, err := GetPlayerStats(ctx, db, userID)
	if err != nil {
		return "", err
	}

	response := &PlayerStatsRPCResponse{
		Stats: stats,
	}

	return response.String(), nil
}
