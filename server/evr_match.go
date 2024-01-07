package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

const (
	VersionLock uint64 = 0xc62f01d78f77910d // The game build version.
	EvrModule          = "evr"              // The module used for matchmaking.

	MaxMatchSize = 15 // The total max players for a social lobby.

	LevelSelectionFirst  MatchLevelSelection = "first"
	LevelSelectionRandom MatchLevelSelection = "random"

	StatGroupArena  MatchStatGroup = "arena"
	StatGroupCombat MatchStatGroup = "combat"
)

const (
	OpCodeBroadcasterDisconnected int64 = iota
	OpCodeEvrPacketData

	SignalStartSession
	SignalGetEndpoint
	SignalShutdown
)

var (
	displayNameRegex = regexp.MustCompile(`"displayname": "(\\"|[^"])*"`)
	updatetimeRegex  = regexp.MustCompile(`"updatetime": \d*`)
)

type MatchStatGroup string
type MatchLevelSelection string

/*
		func NewMatchParams(logger *zap.Logger, session *sessionWS, serverId uint64, internalAddress net.IP, externalAddress net.IP, port uint16, regionSymbol uint64, versionLock uint64, public bool, open bool) (uuid.UUID, error) {

			matchId, err := r.runtimeModule.MatchCreate(ctx, EvrMatchModule, params)
			if err != nil {
				return "", fmt.Errorf("failed to create new lobby: %v", err)
			}


		// The actual matches "live" in a separate runtime.
		// We use the rpc to create the match in the other runtime.

			rpcFn := session.pipeline.runtime.Rpc("create_match_rpc")

			paramsJson, err := json.Marshal(params)
			if err != nil {
				return uuid.Nil, fmt.Errorf("failed to create new lobby: %v", err)
			}

			matchNode, err, _ := rpcFn(session.Context(), nil, nil, uuid.Nil.String(), "", nil, session.Expiry(), session.ID().String(), session.clientIP, session.clientPort, "", string(paramsJson))
			if err != nil {
				return uuid.Nil, fmt.Errorf("failed to create new lobby: %v", err)
			}

		matchNode, err := r.matchRegistry.CreateMatch(context.Background(), r.runtime.matchCreateFunction, "match", params)
		if err != nil {
			return uuid.Nil, fmt.Errorf("failed to create new lobby: %v", err)
		}
		logger.Debug("Created new lobby.", zap.String("result", matchNode))

		s := strings.Split(matchNode, ".")
		matchId, _ := s[0], s[1]
		return uuid.FromStringOrNil(matchId), nil
	}
*/

type EvrSignal struct {
	UserId string
	Signal int64
	Data   []byte
}

func (s EvrSignal) String() string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b)
}

const (
	EvrMatchModule = "evrmatch"
)

var _ runtime.Presence = &EvrMatchPresence{}

// Represents identity information for a single match participant.
type EvrMatchPresence struct {
	Node          string
	UserID        uuid.UUID
	SessionID     uuid.UUID
	Username      string
	DisplayName   string
	Reason        runtime.PresenceReason
	EvrId         evr.EvrId // The player's evr id.
	PlayerSession uuid.UUID // Match-scoped session id.
	TeamIndex     int       // the team index the player prefers/has been assigned to.
}

func (p *EvrMatchPresence) String() string {

	data, err := json.Marshal(struct {
		UserID      string `json:"userid,omitempty"`
		DisplayName string `json:"displayname,omitempty"`
		EvrId       string `json:"evrid,omitempty"`
		TeamIndex   int    `json:"team,omitempty"`
	}{p.UserID.String(), p.DisplayName, p.EvrId.Token(), p.TeamIndex})
	if err != nil {
		return ""
	}
	return string(data)
}

func (p *EvrMatchPresence) GetUserId() string {
	return p.UserID.String()
}
func (p *EvrMatchPresence) GetSessionId() string {
	return p.SessionID.String()
}
func (p *EvrMatchPresence) GetNodeId() string {
	return p.Node
}
func (p *EvrMatchPresence) GetHidden() bool {
	return false
}
func (p *EvrMatchPresence) GetPersistence() bool {
	return false
}
func (p *EvrMatchPresence) GetUsername() string {
	return p.Username
}
func (p *EvrMatchPresence) GetStatus() string {
	return ""
}
func (p *EvrMatchPresence) GetReason() runtime.PresenceReason {
	return p.Reason
}
func (p *EvrMatchPresence) GetEvrId() string {
	return p.EvrId.Token()
}

func (p *EvrMatchPresence) GetPlayerSession() string {
	return p.PlayerSession.String()
}

type JoinMeta struct {
	EvrId         evr.EvrId
	PlayerSession uuid.UUID
	TeamIndex     int16
	DisplayName   string
}

// The lobby state is used for the match label.
// Any changes to the lobby state should be reflected in the match label.
// This also makes it easier to update the match label, and query against it.
type EvrMatchState struct {
	MatchId              uuid.UUID    `json:"match_id,omitempty"`               // The Session Id used by EVR (the same as match id)
	Open                 bool         `json:"open,omitempty"`                   // Whether the lobby is open to new players (Matching Only)
	LobbyType            LobbyType    `json:"lobby_type"`                       // The type of lobby (Public, Private, Unassigned) (EVR)
	BroadcasterSessionId string       `json:"broadcaster_session_id,omitempty"` // The broadcaster's Session ID
	BroadcasterUserId    string       `json:"broadcaster_user_id,omitempty"`    // The user id of the broadcaster.
	BroadcasterChannels  []uuid.UUID  `json:"broadcaster_channels,omitempty"`   // The channels this broadcaster will host matches for.
	Endpoint             evr.Endpoint `json:"endpoint,omitempty"`               // The endpoint data used for connections.

	Channel         uuid.UUID           `json:"channel,omitempty"`          // The channel id of the broadcaster. (EVR)
	ServerId        uint64              `json:"server_id,omitempty"`        // The server id of the broadcaster. (EVR)
	AppId           string              `json:"app_id,omitempty"`           // The game app id. (EVR)
	PublisherLock   bool                `json:"publisher_lock,omitempty"`   // Publisher lock (EVR)
	VersionLock     uint64              `json:"version_lock,omitempty"`     // The game build version. (EVR)
	Platform        evr.Symbol          `json:"platform,omitempty"`         // The platform the match is hosted on. (EVR)
	Region          evr.Symbol          `json:"region,omitempty"`           // The region the match is hosted in. (Matching Only) (EVR)
	Mode            evr.Symbol          `json:"mode,omitempty"`             // The mode of the lobby (Arena, Combat, Social, etc.) (EVR)
	Level           evr.Symbol          `json:"level,omitempty"`            // The level to play on (EVR).
	Levels          []Level             `json:"levels,omitempty"`           // The levels to choose from (EVR).
	LevelSelection  MatchLevelSelection `json:"level_selection,omitempty"`  // The level selection method (EVR).
	SessionSettings evr.SessionSettings `json:"session_settings,omitempty"` // The session settings for the match (EVR).

	Tags        []string  `json:"tags,omitempty"`          // The tags given on the urlparam for the match.
	MaxSize     uint8     `json:"max_size,omitempty"`      // The total lobby size limit (players + specs)
	Size        int       `json:"size,omitempty"`          // The number of players (not including spectators) in the match.
	MaxTeamSize int       `json:"max_team_size,omitempty"` // The size of each team in arena/combat (either 4 or 5)
	TeamIndex   TeamIndex `json:"team,omitempty"`          // What team index a player prefers (Used by Matching only)
	EvrIds      []string  `json:"evr_ids,omitempty"`       // []evrIdToken Matching only
	UserIds     []string  `json:"user_ids,omitempty"`      // The user ids of the players in the match.

	presences               map[string]*EvrMatchPresence // [userId]EvrMatchPresence
	broadcaster             runtime.Presence             // The broadcaster's presence
	presenceByEvrId         map[string]*EvrMatchPresence // lookup table for EchoVR ID
	presenceByPlayerSession map[string]*EvrMatchPresence // lookup table for match-scoped session id
	presenceCache           map[string]*EvrMatchPresence // [userId]PlayerMeta cache for all players that have attempted to join the match.
	emptyTicks              int                          // The number of ticks the match has been empty.
	tickRate                int                          // The number of ticks per second.
}

func (s *EvrMatchState) String() string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b)
}

func (s *EvrMatchState) GetLabel() string {
	labelJson, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return ""
	}
	return string(labelJson)
}

func MatchStateFromLabel(label string) (*EvrMatchState, error) {
	state := &EvrMatchState{}
	err := json.Unmarshal([]byte(label), state)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal match label: %v", err)
	}
	return state, nil
}

// rebuildCache is called after the presences map is updated.
func (s *EvrMatchState) rebuildCache() {
	// Rebuild the lookup tables.

	s.EvrIds = make([]string, 0)
	s.UserIds = make([]string, 0)
	s.presenceByEvrId = make(map[string]*EvrMatchPresence)
	s.presenceByPlayerSession = make(map[string]*EvrMatchPresence)

	s.Size = 0

	// Count players by team index
	for userId, presence := range s.presences {
		if presence.TeamIndex != evr.TeamSpectator {
			s.Size += 1
		}
		s.EvrIds = append(s.EvrIds, presence.GetEvrId())
		s.UserIds = append(s.UserIds, userId)
		s.presenceByPlayerSession[presence.GetPlayerSession()] = presence
		s.presenceByEvrId[presence.GetEvrId()] = presence
	}
}

// The match config is used internally to create a new match.
type evrMatchConfig struct {
	Endpoint     evr.Endpoint // TODO FIXME this si extraneous since it's now in the state.
	InitialState *EvrMatchState
}

// This is the match handler for all matches.
// There always is one per broadcaster.
// The match is spawned and managed directly by nakama.
// The match can only be communicated with through MatchSignal() and MatchData messages.
type EvrMatch struct{}

// NewEvrMatch is called by the match handler when creating the match.
func NewEvrMatch(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) (m runtime.Match, err error) {
	return &EvrMatch{}, nil
}

// NewEvrMatchState is a helper function to create a new match state. It returns the state, params, label json, and err.
func NewEvrMatchState(endpoint evr.Endpoint, config *EvrMatchState, sessionId string) (*EvrMatchState, map[string]interface{}, string, error) {
	// TODO It might be better to have the broadcaster just waiting on a stream, and be constantly matchmaking along with users,
	// Then when the matchmaker decides spawning a new server is nominal approach, the broadcaster joins the match along with
	// the players. It would have to join first though.  - @thesprockee
	initState := *config

	initState.Open = false
	initState.LobbyType = UnassignedLobby
	initState.Mode = evr.ModeUnloaded
	initState.Level = evr.LevelUnloaded
	initState.AppId = strconv.FormatUint(PcvrAppId, 10)
	initState.EvrIds = make([]string, 0)
	initState.UserIds = make([]string, 0)
	evrMatchConfig := evrMatchConfig{
		Endpoint:     endpoint,
		InitialState: &initState,
	}

	configJson, err := json.Marshal(evrMatchConfig)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to marshal match config: %v", err)
	}

	params := map[string]interface{}{
		"config": configJson,
	}

	return &initState, params, string(configJson), nil
}

// MatchIdFromContext is a helper function to extract the match id from the context.
func MatchIdFromContext(ctx context.Context) (uuid.UUID, string) {
	matchId, ok := ctx.Value(runtime.RUNTIME_CTX_MATCH_ID).(string)
	if !ok {
		return uuid.Nil, ""
	}
	matchIdComponents := strings.SplitN(matchId, ".", 2)
	if len(matchIdComponents) != 2 {
		return uuid.Nil, ""
	}
	u, err := uuid.FromString(matchIdComponents[0])
	if err != nil {
		return uuid.Nil, ""
	}
	return u, matchIdComponents[1]
}

// MatchInit is called when the match is created.
func (m *EvrMatch) MatchInit(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, params map[string]interface{}) (interface{}, int, string) {
	config := evrMatchConfig{}
	if err := json.Unmarshal(params["config"].([]byte), &config); err != nil {
		logger.Error("Failed to unmarshal match config. %s", err)
	}
	matchId, _ := MatchIdFromContext(ctx)

	state := config.InitialState
	state.MatchId = matchId
	state.Endpoint = config.Endpoint
	state.presenceCache = make(map[string]*EvrMatchPresence)
	state.presences = make(map[string]*EvrMatchPresence)

	state.rebuildCache()

	labelJson, err := json.Marshal(state)
	if err != nil {
		logger.WithField("err", err).Error("Match label marshal error.")
	}

	state.tickRate = 4
	return state, state.tickRate, string(labelJson)
}

// selectTeamForPlayer decides which team to assign a player to.
func selectTeamForPlayer(t int, mode evr.Symbol, presences map[string]*EvrMatchPresence) (int, bool) {

	if len(presences) >= MaxMatchSize {
		// Lobby full, reject.
		return t, false
	}

	// If the player is unassigned, assign them to a team.
	if t == evr.TeamUnassigned {
		t = evr.TeamBlue
	}

	teams := lo.GroupBy(lo.Values(presences), func(p *EvrMatchPresence) int { return p.TeamIndex })

	blueTeam := teams[evr.TeamBlue]
	orangeTeam := teams[evr.TeamOrange]
	playerpop := len(blueTeam) + len(orangeTeam)
	spectators := len(teams[evr.TeamSpectator]) + len(teams[evr.TeamModerator])

	// Private Arena and Combat lobbies have a 10 player limit.
	if t == evr.TeamBlue || t == evr.TeamOrange {
		if mode == evr.ModeArenaPrivate || mode == evr.ModeCombatPrivate && playerpop >= 10 {
			// Teams are full, send to spectator
			t = evr.TeamSpectator
		} else {
			// Public Match
			if playerpop >= 8 {
				// Teams are full, reject.
				return t, false
			}
			// Assign to the smallest team.
			if len(blueTeam) < len(orangeTeam) {
				t = evr.TeamBlue
			} else {
				t = evr.TeamOrange
			}
		}
	} else {
		// Spectator or Moderator
		if spectators >= 6 {
			// Spectator population is full, reject.
			return t, false
		}
	}
	return t, true
}

// MatchJoinAttempt decides whether to accept or deny the player session.
func (m *EvrMatch) MatchJoinAttempt(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presence runtime.Presence, metadata map[string]string) (interface{}, bool, string) {
	state, ok := state_.(*EvrMatchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil, false, ""
	}

	if presence.GetSessionId() == state.BroadcasterSessionId {
		logger.Debug("Broadcaster joining the match.")
		// This is the broadcaster joining, this completes the match init.
		return state, true, ""
	}

	// This is a player joining.
	if state.LobbyType == UnassignedLobby {
		// This is a parking match. reject the player.
		return state, false, "unassigned lobby"
	}

	// Verify this isn't a duplicate. It will crash the server if they are allowed to join.
	if _, ok := state.presences[presence.GetUserId()]; ok {
		return state, false, "duplicate join"
	}

	playermeta := JoinMeta{}
	if err := json.Unmarshal([]byte(metadata["playermeta"]), &playermeta); err != nil {
		logger.Error("Failed to unmarshal metadata", zap.Error(err))
		return state, false, fmt.Sprintf("failed to unmarshal metadata: %q", err)
	}
	mp := EvrMatchPresence{
		Node:          presence.GetNodeId(),
		UserID:        uuid.Must(uuid.FromString(presence.GetUserId())),
		SessionID:     uuid.Must(uuid.FromString(presence.GetSessionId())),
		Username:      presence.GetUsername(),
		DisplayName:   playermeta.DisplayName,
		Reason:        presence.GetReason(),
		EvrId:         playermeta.EvrId,
		PlayerSession: playermeta.PlayerSession,
		TeamIndex:     int(playermeta.TeamIndex),
	}

	// Check if they are a moderator
	if mp.TeamIndex == evr.TeamModerator {
		found, err := checkIfModerator(ctx, nk, presence.GetUserId(), state.Channel.String())
		if err != nil {
			return state, false, fmt.Sprintf("failed to check if moderator: %q", err)
		}
		if !found {
			return state, false, "not a moderator"
		}
	}

	if mp.TeamIndex, ok = selectTeamForPlayer(mp.TeamIndex, state.Mode, state.presences); !ok {
		// The lobby is full, reject the player.
		return state, false, "lobby full"
	}
	// The player data will be looked up by MatchJoin()
	state.presenceCache[presence.GetUserId()] = &mp
	// Accept the player(s) into the session.
	logger.Debug("Accepting player into match: %s", presence.GetUsername())
	return state, true, ""
}

// MatchJoin is called after the join attempt.
// MatchJoin updates the match data, and should not have any decision logic.
func (m *EvrMatch) MatchJoin(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presences []runtime.Presence) interface{} {
	state, ok := state_.(*EvrMatchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	for _, p := range presences {
		logger.Debug("Joined a new player: %s", presences[0].GetUsername())

		if p.GetSessionId() == state.BroadcasterSessionId {
			logger.Debug("Broadcaster joined the match.")
			state.broadcaster = p
			state.Open = true // Available

			if state.LobbyType == UnassignedLobby {
				// This is a parking match. do nothing.
				continue
			}

			// Tell the broadcaster to load the level.
			messages := []evr.Message{
				evr.NewBroadcasterStartSession(state.MatchId, state.Channel, state.MaxSize, uint8(state.LobbyType), state.AppId, state.Mode, state.Level, []evr.EvrId{}),
			}

			// Dispatch the message for delivery.
			if err := m.dispatchMessages(ctx, logger, dispatcher, messages, []runtime.Presence{state.broadcaster}, nil); err != nil {
				logger.Error("failed to dispatch load message to broadcaster: %v", err)
				return nil
			}
			continue
		}

		// the cache entry is kept even after a player leaves. This helps put them back on the same team.
		matchPresence, ok := state.presenceCache[p.GetUserId()]
		if !ok {
			// TODO FIXME Kick the player from the match.
			logger.Error("Player not in cache. This shouldn't happen.")
			return state
		}

		state.presences[p.GetUserId()] = matchPresence
		// NOTE TODO Should multiple players be treated as a group?

		// TODO FIXME: Validate access to the moderator team (in the calling function).
		// TODO FIXME: Determine what team the player will be on. (by balance, limit, etc.)

		// Add the player to a direct match presence stream.

		// Send the success messages to the client and server.
		// The server still technically has to accept the player session, but
		// it's assumed that will be successful. If the server rejects them, they will
		// be removed from the match.
		gameMode := state.Mode
		teamIndex := int16(matchPresence.TeamIndex)
		channel := state.Channel
		matchSession := uuid.UUID(state.MatchId)
		endpoint := state.Endpoint
		success := evr.NewLobbySessionSuccess(gameMode, matchSession, channel, endpoint, teamIndex)
		successV4 := success.Version4()
		successV5 := success.Version5()
		messages := []evr.Message{
			successV4,
			successV5,
			evr.NewSTcpConnectionUnrequireEvent(),
		}

		// Dispatch the message for delivery.
		if err := m.dispatchMessages(ctx, logger, dispatcher, messages, []runtime.Presence{state.broadcaster, p}, nil); err != nil {
			logger.Error("failed to dispatch success message to broadcaster: %v", err)
		}
	}

	state.rebuildCache()
	// Update the label that includes the new player list.
	err := m.updateLabel(dispatcher, state)
	if err != nil {
		logger.Error("failed to update label: %v", err)
	}
	return state
}

// MatchLeave is called after a player leaves the match.
func (m *EvrMatch) MatchLeave(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presences []runtime.Presence) interface{} {
	state, ok := state_.(*EvrMatchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}
	// Remove the player from the match.
	for _, p := range presences {
		logger.Debug("Removing player from match: %s", p.GetUsername())
		sessionId := p.GetSessionId()
		if sessionId == state.BroadcasterSessionId {
			logger.Debug("Broadcaster left the match. Shutting down.")
			// TODO maybe send the users back to the lobby? if possible.
			return nil
		}
		delete(state.presences, p.GetUserId())
	}

	state.rebuildCache()
	// Update the label that includes the new player list.
	err := m.updateLabel(dispatcher, state)
	if err != nil {
		logger.Error("failed to update label: %v", err)
	}

	return state
}

// MatchLoop is called every tick of the match and handles state, plus messages from the client.
func (m *EvrMatch) MatchLoop(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, messages []runtime.MatchData) interface{} {
	var err error
	state, ok := state_.(*EvrMatchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	// Keep track of how many ticks the match has been empty.
	if len(state.presences) == 0 {
		state.emptyTicks++
	} else {
		state.emptyTicks = 0
	}

	// Handle the messages, one by one
	for _, in := range messages {
		switch in.GetOpCode() {
		default:
			typ, found := evr.SymbolTypes[uint64(in.GetOpCode())]
			if !found {
				logger.Error("Unknown opcode: %v", in.GetOpCode())
				continue
			}

			logger.Debug("Received match message %T(%s) from %v", typ, string(in.GetData()), in.GetUserId())
			// Unmarshal the message into an interface, then switch on the type.
			msg := reflect.New(reflect.TypeOf(typ).Elem()).Interface().(evr.Message)
			if err := json.Unmarshal(in.GetData(), &msg); err != nil {
				logger.Error("Failed to unmarshal message: %v", err)
			}

			var messageFn func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *EvrMatchState, in runtime.MatchData, msg evr.Message) (*EvrMatchState, error)

			// Switch on the message type. This is where the match logic is handled.
			switch msg := msg.(type) {
			// TODO consider using a go routine for any/all of these that do not modify state.
			// FIXME modify the state only here in the main loop, do not pass it to the functions.
			case *evr.LobbyPlayerSessionsRequest:
				// The client is requesting player sessions.
				// TODO consider pushing this into a go routine.
				messageFn = m.lobbyPlayerSessionsRequest
			case *evr.BroadcasterPlayersAccept:
				// The client has connected to the broadcaster, and the broadcaster has accepted the connection.
				messageFn = m.broadcasterPlayersAccept
			case *evr.BroadcasterPlayerRemoved:
				// The client has disconnected from the broadcaster.
				messageFn = m.broadcasterPlayerRemoved
			case *evr.UserServerProfileUpdateRequest:
				// The broadcaster is updating the user's profile.
				// TODO consider pushing this into a go routine.
				messageFn = m.userServerProfileUpdateRequest
			case *evr.OtherUserProfileRequest:
				// The broadcaster is requesting the profile of joining user.
				messageFn = m.otherUserProfileRequest
			default:
				logger.Warn("Unknown message type: %T", msg)
			}
			if messageFn != nil {
				state, err = messageFn(ctx, logger, db, nk, dispatcher, state, in, msg)
				if err != nil {
					logger.Error("lobbyPlayerSessionsRequest: %v", err)
				}
			}
		}
	}
	return state
}

// MatchTerminate is called when the match is being terminated.
func (m *EvrMatch) MatchTerminate(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state interface{}, graceSeconds int) interface{} {
	//message := "Server shutting down in " + strconv.Itoa(graceSeconds) + " seconds."
	//dispatcher.BroadcastMessage(OpCodeBroadcasterDisconnected, []byte(message), []runtime.Presence{}, nil, false)
	// TODO FIXME send disconnect messages to clients.
	return state
}

// MatchSignal is called when a signal is sent into the match.
func (m *EvrMatch) MatchSignal(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, data string) (interface{}, string) {
	state, ok := state_.(*EvrMatchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil, "state not a valid lobby state object"
	}

	// TODO protobuf's would be nice here.
	signal := &EvrSignal{}
	err := json.Unmarshal([]byte(data), signal)
	if err != nil {
		return state, fmt.Sprintf("failed to unmarshal signal: %v", err)
	}

	switch signal.Signal {
	case SignalShutdown:
		return nil, "shutdown signal"
	case SignalGetEndpoint:
		jsonData, err := json.Marshal(state.Endpoint)
		if err != nil {
			return state, fmt.Sprintf("failed to marshal endpoint: %v", err)
		}
		return state, string(jsonData)
	case SignalStartSession:
		// Start a new session by loading a level
		newState := &EvrMatchState{}
		err := json.Unmarshal(signal.Data, newState)
		if err != nil {
			return state, fmt.Sprintf("failed to unmarshal match label: %v", err)
		}
		matchId, _ := MatchIdFromContext(ctx)
		state.MatchId = matchId
		state.Channel = newState.Channel
		state.MaxSize = newState.MaxSize
		state.LobbyType = newState.LobbyType
		state.Mode = newState.Mode
		state.MaxTeamSize = newState.MaxTeamSize
		state.Level = newState.Level
		state.Open = newState.Open
		state.SessionSettings = newState.SessionSettings
		state.LevelSelection = newState.LevelSelection
		if state.Level == 0xffffffffffffffff {
			// The level is not set, set it to zero
			state.Level = 0
		}
		// Tell the broadcaster to start the session.
		//entrants := lo.Map(lo.Values(state.presences), func(presence *EvrMatchPresence, _ int) evr.EvrId { return presence.EvrId })
		entrants := make([]evr.EvrId, 0)
		message := evr.NewBroadcasterStartSession(state.MatchId, state.Channel, state.MaxSize, uint8(state.LobbyType), state.AppId, state.Mode, state.Level, entrants)
		messages := []evr.Message{
			message,
		}

		// Dispatch the message for delivery.
		if err := m.dispatchMessages(ctx, logger, dispatcher, messages, []runtime.Presence{state.broadcaster}, nil); err != nil {
			return state, fmt.Sprintf("failed to dispatch message: %v", err)
		}
		if err := m.updateLabel(dispatcher, state); err != nil {
			logger.Error("failed to update label: %v", err)
		}
		return state, "session started"

	default:
		logger.Warn("Unknown signal: %v", signal.Signal)
		return state, "unknown signal"
	}
}

// SignalMatch is a helper function to send a signal to a match.
func SignalMatch(ctx context.Context, matchRegistry MatchRegistry, matchId string, signalId int64, data interface{}) (string, error) {
	dataJson, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal match label: %v", err)
	}
	signal := EvrSignal{
		Signal: signalId,
		Data:   dataJson,
	}
	signalJson, err := json.Marshal(signal)
	if err != nil {
		return "", fmt.Errorf("failed to marshal match signal: %v", err)
	}
	return matchRegistry.Signal(ctx, matchId, string(signalJson))
}
func (m *EvrMatch) dispatchMessages(_ context.Context, logger runtime.Logger, dispatcher runtime.MatchDispatcher, messages []evr.Message, presences []runtime.Presence, sender runtime.Presence) error {
	bytes := []byte{}
	for _, message := range messages {

		logger.Debug("Sending message from match: %v", message)
		payload, err := evr.Marshal(message)
		if err != nil {
			return fmt.Errorf("could not marshal message: %v", err)
		}
		bytes = append(bytes, payload...)
	}
	if err := dispatcher.BroadcastMessageDeferred(OpCodeEvrPacketData, bytes, presences, sender, true); err != nil {
		return fmt.Errorf("could not broadcast message: %v", err)
	}
	return nil
}

func (m *EvrMatch) updateLabel(dispatcher runtime.MatchDispatcher, state *EvrMatchState) error {
	labelJson, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal match label: %v", err)
	}
	if err := dispatcher.MatchLabelUpdate(string(labelJson)); err != nil {
		return fmt.Errorf("could not update label: %v", err)
	}
	return nil
}

func checkIfModerator(ctx context.Context, nk runtime.NakamaModule, userId string, channelId string) (bool, error) {
	result, _, err := nk.GroupsList(ctx, "Global Moderators", "", nil, nil, 1, "")
	if err != nil {
		return false, fmt.Errorf("failed to list groups: %q", err)
	}
	modgroups := []string{}
	for _, group := range result {
		modgroups = append(modgroups, group.GetId())
	}
	// Pull the channel's group
	result, err = nk.GroupsGetId(ctx, []string{channelId})
	if err != nil {
		return false, fmt.Errorf("failed to get group: %q", err)
	}
	if len(result) == 0 {
		// No group found for this channel.
		return false, fmt.Errorf("no group found for channel: %q", channelId)
	}
	cgroup := result[0]
	// Extract the metadata from the group
	metadata := GroupMetadata{}
	if err := json.Unmarshal([]byte(cgroup.GetMetadata()), &metadata); err != nil {
		return false, fmt.Errorf("failed to unmarshal group metadata: %q", err)
	}

	// Get the moderator group from the channel's metadata.
	moderatorGroup := metadata.ModeratorGroupId
	if moderatorGroup == "" {
		return false, fmt.Errorf("no moderator group found for channel: %q", channelId)
	} else {
		modgroups = append(modgroups, moderatorGroup)
	}

	groups, _, err := nk.UserGroupsList(ctx, userId, 1, lo.ToPtr(2), "")
	if err != nil {
		return false, fmt.Errorf("failed to list user groups: %q", err)
	}
	for _, group := range groups {
		if lo.Contains(modgroups, group.GetGroup().GetId()) {
			return true, nil
		}
	}
	return false, nil
}

// lobbyPlayerSessionsRequest is called when a client requests the player sessions for a list of EchoVR IDs.
func (m *EvrMatch) lobbyPlayerSessionsRequest(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *EvrMatchState, in runtime.MatchData, msg evr.Message) (*EvrMatchState, error) {
	message := msg.(*evr.LobbyPlayerSessionsRequest)
	// Prefer the match data over the message data message data, use the match data.
	sender, ok := state.presences[in.GetUserId()]
	if !ok {
		logger.Warn("lobbyPlayerSessionsRequest: player not in match: %v", in.GetUserId())
	}
	// build the player sessions list.
	playerSessions := make([]uuid.UUID, 0, len(message.PlayerEvrIds))
	for _, e := range message.PlayerEvrIds {
		p, ok := state.presenceByEvrId[e.Token()]
		if !ok {
			logger.Warn("lobbyPlayerSessionsRequest: player not in match: %v", e)
			continue
		}
		playerSessions = append(playerSessions, p.PlayerSession)
	}

	teamIndex := int16(sender.TeamIndex)
	go func(sender runtime.Presence, evrId evr.EvrId, matchSession uuid.UUID, playerSession uuid.UUID, playerSessions []uuid.UUID, teamIndex int16) {
		success := evr.NewLobbyPlayerSessionsSuccess(evrId, state.MatchId, playerSession, playerSessions, teamIndex)
		messages := []evr.Message{
			success.VersionU(),
			success.Version2(),
			success.Version3(),
		}
		if err := m.dispatchMessages(ctx, logger, dispatcher, messages, []runtime.Presence{sender}, nil); err != nil {
			logger.Error("lobbyPlayerSessionsRequest: failed to dispatch message: %v", err)
		}
	}(sender, sender.EvrId, state.MatchId, sender.PlayerSession, playerSessions, teamIndex)
	return state, nil
}

// broadcasterPlayersAccept is called when the broadcaster has accepted or rejected player sessions.
func (m *EvrMatch) broadcasterPlayersAccept(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *EvrMatchState, in runtime.MatchData, msg evr.Message) (*EvrMatchState, error) {
	message := msg.(*evr.BroadcasterPlayersAccept)
	// validate the player sessions.
	accepted := make([]uuid.UUID, 0)
	rejected := make([]uuid.UUID, 0)
	for _, playerSession := range message.PlayerSessions {
		if _, ok := state.presenceByPlayerSession[playerSession.String()]; !ok {
			logger.Warn("rejecting %v: player not in this match.", playerSession)
			rejected = append(rejected, playerSession)
			continue
		}
		accepted = append(accepted, playerSession)
	}
	// Only include the message if there are players to accept or reject.
	messages := []evr.Message{}
	if len(accepted) > 0 {
		messages = append(messages, evr.NewBroadcasterPlayersAccepted(accepted...))
	}
	if len(rejected) > 0 {
		messages = append(messages, evr.NewBroadcasterPlayersRejected(evr.PlayerRejectionReasonBadRequest, rejected...))
	}
	// Dispatch the message for delivery.
	if err := m.dispatchMessages(ctx, logger, dispatcher, messages, []runtime.Presence{state.broadcaster}, nil); err != nil {
		return nil, fmt.Errorf("failed to dispatch message: %v", err)
	}
	return state, nil
}

// broadcasterPlayerRemoved is called when a player has been removed from the match.
func (m *EvrMatch) broadcasterPlayerRemoved(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *EvrMatchState, in runtime.MatchData, msg evr.Message) (*EvrMatchState, error) {
	message := msg.(*evr.BroadcasterPlayerRemoved)
	// Remove the player from the match.
	matchId, node := MatchIdFromContext(ctx)
	presence, found := state.presenceByPlayerSession[message.PlayerSession.String()]
	if !found {
		logger.Debug("broadcasterPlayerRemoved: player not in match: %v", message.PlayerSession)
		return state, nil
	}
	// Kick the presence from the match. This will trigger the MatchLeave function.
	nk.StreamUserKick(StreamModeMatchAuthoritative, matchId.String(), "", node, presence)
	return state, nil
}

// UpdateUnlocks updates the unlocked cosmetic fields in the dst with the src.
func UpdateUnlocks(dst, src interface{}) {
	dVal := reflect.ValueOf(dst)
	sVal := reflect.ValueOf(src)

	if dVal.Kind() == reflect.Ptr {
		dVal = dVal.Elem()
		sVal = sVal.Elem()
	}

	for i := 0; i < sVal.NumField(); i++ {
		sField := sVal.Field(i)
		dField := dVal.Field(i)

		// Check if the field is a boolean
		if sField.Kind() == reflect.Bool {
			dField.SetBool(sField.Bool())
		}
	}
}

// userServerProfileUpdateRequest is called when the broadcaster is updating the user's server profile.
func (m *EvrMatch) userServerProfileUpdateRequest(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *EvrMatchState, in runtime.MatchData, msg evr.Message) (*EvrMatchState, error) {
	message := msg.(*evr.UserServerProfileUpdateRequest)

	// Verify that the update is coming from the broadcaster.
	if in.GetUserId() != state.broadcaster.GetUserId() {
		logger.Warn("unauthorized server profile update of %s by %v", message.EvrId, in.GetUserId())
		return state, fmt.Errorf("unauthorized")
	}

	presence, found := state.presenceByEvrId[message.EvrId.Token()]
	if !found {
		return state, fmt.Errorf("user not in match")
	}
	// Retrieve the server profile
	ops := []*runtime.StorageRead{
		{
			Collection: GameProfileStorageCollection,
			Key:        ServerGameProfileStorageKey,
			UserID:     presence.GetUserId(),
		},
	}
	objs, err := nk.StorageRead(ctx, ops)
	if err != nil {
		return state, fmt.Errorf("error writing server profile: %w", err)
	}
	if len(objs) == 0 {
		return state, fmt.Errorf("server profile not found")
	}

	// Unmarshal the server profile.
	profile := evr.ServerProfile{}
	if err := json.Unmarshal([]byte(objs[0].Value), &profile); err != nil {
		return state, fmt.Errorf("error unmarshalling server profile: %w", err)
	}

	unlocks := message.UpdateInfo.Update.Unlocks
	UpdateUnlocks(profile.UnlockedCosmetics, unlocks)

	// Write the profile to storage.
	profileJson, err := json.Marshal(profile)
	if err != nil {
		return state, fmt.Errorf("error marshalling server profile: %w", err)
	}

	objectIDs := []*runtime.StorageWrite{
		{
			Collection:      GameProfileStorageCollection,
			Key:             ServerGameProfileStorageKey,
			UserID:          presence.GetUserId(),
			Value:           string(profileJson),
			PermissionRead:  1,
			PermissionWrite: 0,
		},
	}

	if _, err := nk.StorageWrite(ctx, objectIDs); err != nil {
		return state, fmt.Errorf("error writing server profile: %w", err)
	}
	if err := sendMessagesToStream(ctx, nk, in.GetSessionId(), svcLoginID.String(), evr.NewUserServerProfileUpdateSuccess(message.EvrId)); err != nil {
		return state, fmt.Errorf("failed to send message: %w", err)
	}
	return state, nil
}

// A request from a broadcaster (over its _unrelated_ login connection) for a profile of a joining user.
func (m *EvrMatch) otherUserProfileRequest(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *EvrMatchState, in runtime.MatchData, msg evr.Message) (*EvrMatchState, error) {
	request := msg.(*evr.OtherUserProfileRequest)

	errFailure := func(e error, code int) error {
		if err := sendMessagesToStream(ctx, nk, in.GetSessionId(), svcLoginID.String(), evr.NewOtherUserProfileFailure(request.EvrId, uint64(code), e.Error())); err != nil {
			return fmt.Errorf("failed to send message: %w", err)
		}
		return e
	}
	presence, ok := state.presenceByEvrId[request.EvrId.Token()]
	if !ok {
		return state, errFailure(fmt.Errorf("user not in match"), 404)
	}

	// Retrieve the server profile from storage.
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: GameProfileStorageCollection,
			Key:        ServerGameProfileStorageKey,
			UserID:     presence.UserID.String(),
		},
	})
	if err != nil {
		return state, errFailure(fmt.Errorf("failed to read game profile: %w", err), 500)
	}

	if len(objs) == 0 {
		return state, errFailure(fmt.Errorf("server profile not found"), 500)
	}

	profileJson := []byte(objs[0].GetValue())

	// Parse the profile
	profile := evr.ServerProfile{}
	if err := json.Unmarshal(profileJson, &profile); err != nil {
		return state, errFailure(fmt.Errorf("failed to unmarshal game profile: %w", err), 500)
	}
	// Update the display name and times
	profile.DisplayName = presence.DisplayName
	profile.UpdateTime = time.Now().Unix()

	// Send the server profile to the requesting user over it's login connection.
	return state, sendMessagesToStream(ctx, nk, in.GetSessionId(), svcLoginID.String(), evr.NewOtherUserProfileSuccess(request.EvrId, &profile))
}

func injectDisplayName(profileJson []byte, displayName string) []byte {
	replacement := fmt.Sprintf(`"displayname": "%s"`, displayName)
	profileJson = displayNameRegex.ReplaceAll(profileJson, []byte(replacement))
	profileJson = updatetimeRegex.ReplaceAll(profileJson, []byte(fmt.Sprintf(`"updatetime": %d`, time.Now().Unix())))
	return profileJson
}

func sendMessagesToStream(_ context.Context, nk runtime.NakamaModule, sessionId string, serviceId string, messages ...evr.Message) error {
	data, err := evr.Marshal(messages...)
	if err != nil {
		return fmt.Errorf("failed to marshal message: %w", err)
	}
	presences, err := nk.StreamUserList(StreamModeEvr, sessionId, serviceId, "", true, true)
	if err != nil {
		return fmt.Errorf("failed to list users: %w", err)
	}
	for _, presence := range presences {
		log.Printf("Sending message to %s on session ID %s", presence.GetUserId(), presence.GetSessionId())
	}
	if err := nk.StreamSend(StreamModeEvr, sessionId, serviceId, "", string(data), nil, true); err != nil {
		return fmt.Errorf("failed to send message: %w", err)
	}
	return nil
}
