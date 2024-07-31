package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"reflect"
	"regexp"
	"slices"
	"strconv"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/ipinfo/go/v2/ipinfo"
)

const (
	VersionLock       uint64 = 0xc62f01d78f77910d // The game build version.
	MatchmakingModule        = "evr"              // The module used for matchmaking

	MatchMaxSize                             = 12 // The total max players (not including the broadcaster) for a EVR lobby.
	LevelSelectionFirst  MatchLevelSelection = "first"
	LevelSelectionRandom MatchLevelSelection = "random"

	StatGroupArena  MatchStatGroup = "arena"
	StatGroupCombat MatchStatGroup = "combat"
)

const (
	OpCodeBroadcasterDisconnected int64 = iota
	OpCodeEVRPacketData

	SignalPrepareSession
	SignalStartSession
	SignalEndSession
	SignalLockSession
	SignalUnlockSession
	SignalGetEndpoint
	SignalGetPresences
	SignalPruneUnderutilized
	SignalTerminate
)

var (
	displayNameRegex = regexp.MustCompile(`"displayname": "(\\"|[^"])*"`)
	updatetimeRegex  = regexp.MustCompile(`"updatetime": \d*`)
)

type MatchStatGroup string
type MatchLevelSelection string

type EvrSignal struct {
	UserId  string
	Signal  int64
	Payload []byte
}

func NewEvrSignal(userId string, signal int64, data any) *EvrSignal {
	payload, err := json.Marshal(data)
	if err != nil {
		return nil
	}
	return &EvrSignal{
		UserId:  userId,
		Signal:  signal,
		Payload: payload,
	}
}

func (s EvrSignal) GetOpCode() int64 {
	return s.Signal
}

func (s EvrSignal) GetData() []byte {
	return s.Payload
}

func (s EvrSignal) String() string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b)
}

const (
	EvrMatchmakerModule = "evrmatchmaker"
	EvrBackfillModule   = "evrbackfill"
)

type EvrMatchMeta struct {
	MatchBroadcaster
	Players []EvrMatchPresence `json:"players,omitempty"` // The displayNames of the players (by team name) in the match.
	// Stats
}

type MatchGameMode struct {
	Mode       evr.Symbol `json:"mode"`
	Visibility LobbyType  `json:"visibility"`
}

type MatchBroadcaster struct {
	SessionID       string          `json:"sid,omitempty"`              // The broadcaster's Session ID
	OperatorID      string          `json:"oper,omitempty"`             // The user id of the broadcaster.
	GroupIDs        []uuid.UUID     `json:"group_ids,omitempty"`        // The channels this broadcaster will host matches for.
	Endpoint        evr.Endpoint    `json:"endpoint,omitempty"`         // The endpoint data used for connections.
	VersionLock     uint64          `json:"version_lock,omitempty"`     // The game build version. (EVR)
	AppId           string          `json:"app_id,omitempty"`           // The game app id. (EVR)
	Regions         []evr.Symbol    `json:"regions,omitempty"`          // The region the match is hosted in. (Matching Only) (EVR)
	IPinfo          *ipinfo.Core    `json:"ip_info,omitempty"`          // The IPinfo of the broadcaster.
	ServerID        uint64          `json:"server_id,omitempty"`        // The server id of the broadcaster. (EVR)
	PublisherLock   bool            `json:"publisher_lock,omitempty"`   // Publisher lock (EVR)
	Features        []string        `json:"features,omitempty"`         // The features of the broadcaster.
	Tags            []string        `json:"tags,omitempty"`             // The tags given on the urlparam for the match.
	DesignatedModes []MatchGameMode `json:"designated_modes,omitempty"` // The priority modes for the broadcaster.
}

func (g *MatchBroadcaster) IsPriorityFor(mode evr.Symbol, visibility LobbyType) bool {
	v := MatchGameMode{
		Mode:       mode,
		Visibility: visibility,
	}
	return slices.Contains(g.DesignatedModes, v)
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

// MatchIDFromContext is a helper function to extract the match id from the context.
func MatchIDFromContext(ctx context.Context) MatchID {
	matchIDStr, ok := ctx.Value(runtime.RUNTIME_CTX_MATCH_ID).(string)
	if !ok {
		return MatchID{}
	}
	matchID := MatchIDFromStringOrNil(matchIDStr)
	return matchID
}

// MatchInit is called when the match is created.
func (m *EvrMatch) MatchInit(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, params map[string]interface{}) (interface{}, int, string) {
	state := MatchLabel{}
	if err := json.Unmarshal(params["initialState"].([]byte), &state); err != nil {
		logger.Error("Failed to unmarshal match config. %s", err)
	}
	const (
		BroadcasterJoinTimeoutSecs = 45
	)

	state.ID = MatchIDFromContext(ctx)
	state.presenceMap = make(map[string]*EvrMatchPresence)
	state.joinTimestamps = make(map[string]time.Time)

	state.broadcasterJoinExpiry = state.tickRate * BroadcasterJoinTimeoutSecs

	state.rebuildCache()

	labelJson, err := json.Marshal(state)
	if err != nil {
		logger.WithField("err", err).Error("Match label marshal error.")
		return nil, 0, ""
	}
	if state.tickRate == 0 {
		state.tickRate = 10
	}

	return &state, int(state.tickRate), string(labelJson)
}

var (
	JoinRejectReasonUnassignedLobby    = "unassigned lobby"
	JoinRejectReasonDuplicateJoin      = "duplicate join"
	JoinRejectReasonLobbyFull          = "lobby full"
	JoinRejectReasonFailedToAssignTeam = "failed to assign team"
)

// MatchJoinAttempt decides whether to accept or deny the player session.
func (m *EvrMatch) MatchJoinAttempt(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presence runtime.Presence, metadata map[string]string) (interface{}, bool, string) {
	state, ok := state_.(*MatchLabel)

	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil, false, ""
	}

	logger = logger.WithField("mid", state.ID.UUID().String())
	logger = logger.WithField("uid", presence.GetUserId())
	logger = logger.WithField("username", presence.GetUsername())

	if presence.GetSessionId() == state.Broadcaster.SessionID {

		logger.Debug("Broadcaster joining the match.")
		state.broadcaster = presence
		state.Open = true // Available

		if err := m.updateLabel(dispatcher, state); err != nil {
			return state, false, fmt.Sprintf("failed to update label: %q", err)
		}

		return state, true, ""
	}

	// This is a player joining.
	md := JoinMetadata{}
	if err := md.UnmarshalMap(metadata); err != nil {
		return state, false, fmt.Sprintf("failed to unmarshal metadata: %v", err)
	}

	mp, reason := m.playerJoinAttempt(state, md.Presence)
	if reason != "" {
		return state, false, reason
	}

	state.presenceMap[mp.GetSessionId()] = mp
	state.joinTimestamps[mp.GetSessionId()] = time.Now()

	logger.WithFields(map[string]interface{}{
		"evrid": mp.EvrID.Token(),
		"role":  mp.RoleAlignment,
		"sid":   mp.GetSessionId(),
		"uid":   mp.GetUserId(),
		"eid":   mp.EntrantID.String()}).Info("Player joining the match.")

	// Tell the broadcaster to load the match if it's not already started
	if !state.Started {
		var err error
		if state, err = m.MatchStart(ctx, logger, nk, dispatcher, state); err != nil {
			return state, false, fmt.Sprintf("failed to start session: %v", err)
		}
	}

	if err := m.updateLabel(dispatcher, state); err != nil {
		return state, false, fmt.Sprintf("failed to update label: %v", err)
	}

	return state, true, mp.String()
}

func (m *EvrMatch) playerJoinAttempt(state *MatchLabel, mp EvrMatchPresence) (*EvrMatchPresence, string) {

	if state.LobbyType == UnassignedLobby {
		return &mp, JoinRejectReasonUnassignedLobby
	}

	// If the lobby is full, reject
	if state.OpenSlots() < 1 {
		return &mp, JoinRejectReasonLobbyFull
	}

	// If this EvrID is already in the match, reject the player
	for _, p := range state.presenceMap {
		if p.GetSessionId() == mp.GetSessionId() || p.EvrID.Equals(mp.EvrID) {
			return &mp, JoinRejectReasonDuplicateJoin
		}
	}

	if teamIndex, ok := state.TeamAlignments[mp.GetUserId()]; ok {
		if mp.RoleAlignment != evr.TeamModerator && mp.RoleAlignment != evr.TeamSpectator {
			mp.RoleAlignment = teamIndex
		}
	}

	// If it's a private match, do not assign teams or check for open slots.
	if state.LobbyType == PrivateLobby {
		return &mp, ""
	}

	if mp.RoleAlignment == evr.TeamModerator || mp.RoleAlignment == evr.TeamSpectator {
		if state.OpenNonPlayerSlots() < 1 {
			return &mp, JoinRejectReasonLobbyFull
		}
		return &mp, ""
	}

	if state.OpenPlayerSlots() < 1 {
		return &mp, JoinRejectReasonLobbyFull
	}

	if state.Mode == evr.ModeSocialPublic || state.Mode == evr.ModeSocialPrivate {
		mp.RoleAlignment = evr.TeamSocial
		return &mp, ""
	}

	if state.RoleCount(evr.TeamBlue) == state.RoleCount(evr.TeamOrange) {
		mp.RoleAlignment = evr.TeamOrange
		return &mp, ""
	}

	if state.RoleCount(evr.TeamBlue) < state.RoleCount(evr.TeamOrange) {
		mp.RoleAlignment = evr.TeamBlue
		return &mp, ""
	}

	mp.RoleAlignment = evr.TeamOrange
	return &mp, ""
}

// MatchJoin is called after the join attempt.
// MatchJoin updates the match data, and should not have any decision logic.
func (m *EvrMatch) MatchJoin(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presences []runtime.Presence) interface{} {
	state, ok := state_.(*MatchLabel)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	for _, p := range presences {
		if p.GetSessionId() == state.Broadcaster.SessionID {
			continue
		}

		if _, ok := state.presenceMap[p.GetSessionId()]; !ok {
			logger.WithFields(map[string]interface{}{
				"username": p.GetUsername(),
				"uid":      p.GetUserId(),
			}).Error("Presence not found. this should never happen.")
			return nil
		} else {
			logger.WithFields(map[string]interface{}{
				"username": p.GetUsername(),
				"uid":      p.GetUserId(),
			}).Info("Join complete.")
		}
	}

	return state
}

var PresenceReasonKicked uint8 = 16

// MatchLeave is called after a player leaves the match.
func (m *EvrMatch) MatchLeave(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presences []runtime.Presence) interface{} {
	state, ok := state_.(*MatchLabel)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	// if the broadcaster is in the presences, then shut down.
	for _, p := range presences {
		if p.GetSessionId() == state.Broadcaster.SessionID {
			logger.Debug("Broadcaster left the match. Shutting down.")
			m.MatchShutdown(ctx, logger, db, nk, dispatcher, tick, state, 10)
		}
	}

	rejects := make([]uuid.UUID, 0)

	for _, p := range presences {
		logger.WithField("presence", p).Debug("Player leaving the match.")

		if mp, ok := state.presenceMap[p.GetSessionId()]; ok {
			tags := map[string]string{
				"mode":     state.Mode.String(),
				"level":    state.Level.String(),
				"type":     state.LobbyType.String(),
				"role":     fmt.Sprintf("%d", mp.RoleAlignment),
				"group_id": state.GetGroupID().String(),
			}
			msg := "Player removed from game server."
			// If the presence still has the entrant stream, then this was from nakama, not the server. inform the server.
			if userPresences, err := nk.StreamUserList(StreamModeEntrant, mp.EntrantID.String(), "", mp.GetNodeId(), true, true); err != nil {
				logger.Error("Failed to list user streams: %v", err)
			} else if len(userPresences) > 0 {
				rejects = append(rejects, mp.EntrantID)
				msg = "Removing player from game server."
				nk.MetricsCounterAdd("match_entrant_kick_count", tags, 1)
			}
			nk.MetricsCounterAdd("match_entrant_leave_count", tags, 1)
			logger.WithFields(map[string]interface{}{
				"username": mp.GetUsername(),
				"uid":      mp.GetUserId(),
			}).Info(msg)

			ts := state.joinTimestamps[mp.GetSessionId()]
			nk.MetricsTimerRecord("match_player_session_duration", tags, time.Since(ts))

			delete(state.presenceMap, p.GetSessionId())
			delete(state.joinTimestamps, p.GetSessionId())

		}
	}

	if len(rejects) > 0 {
		msg := evr.NewBroadcasterPlayersRejected(evr.PlayerRejectionReasonLobbyEnding, rejects...)
		m.dispatchMessages(ctx, logger, dispatcher, []evr.Message{msg}, []runtime.Presence{state.broadcaster}, nil)
	}
	if len(state.presenceMap) == 0 {
		// Lock the match
		state.Open = false
		logger.Debug("Match is empty. Closing it.")
	}

	// Update the label that includes the new player list.
	if err := m.updateLabel(dispatcher, state); err != nil {
		return nil
	}

	return state
}

// MatchLoop is called every tick of the match and handles state, plus messages from the client.
func (m *EvrMatch) MatchLoop(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, messages []runtime.MatchData) interface{} {
	state, ok := state_.(*MatchLabel)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	if state.terminateTick > 0 && tick >= state.terminateTick {
		logger.Debug("Match termination tick reached.")
		return m.MatchTerminate(ctx, logger, db, nk, dispatcher, tick, state, 0)
	}

	var err error

	if state.broadcaster == nil {
		if state.LobbyType != UnassignedLobby {
			logger.Error("Parking match has a lobby type. Shutting down.")
			return nil
		}
		if tick > 15*state.tickRate {
			logger.Error("Parking match join timeout expired. Shutting down.")
			return nil
		}
	}
	if state.Started {
		if len(state.presenceMap) == 0 {
			state.emptyTicks++

			if state.emptyTicks > 20*state.tickRate {
				if state.Open {
					logger.Warn("Started match has been empty for too long. Shutting down.")
				}
				return nil
			}
		}
	} else if state.StartTime.Before(time.Now().Add(-10 * time.Minute)) {
		if state.LobbyType != UnassignedLobby {
			logger.Error("Match has not started on time. Shutting down.")
			// Tell the broadcaster to load the level, which it will immediately shut down.
			if state, err = m.MatchStart(ctx, logger, nk, dispatcher, state); err != nil {
				logger.Error("failed to start session: %v", err)
				return nil
			}
			if err := m.updateLabel(dispatcher, state); err != nil {
				return nil
			}

		}
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

			logger.Debug("Received match message %T(%s) from %s (%s)", typ, string(in.GetData()), in.GetUsername(), in.GetSessionId())
			// Unmarshal the message into an interface, then switch on the type.
			msg := reflect.New(reflect.TypeOf(typ).Elem()).Interface().(evr.Message)
			if err := json.Unmarshal(in.GetData(), &msg); err != nil {
				logger.Error("Failed to unmarshal message: %v", err)
			}

			var messageFn func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *MatchLabel, in runtime.MatchData, msg evr.Message) (*MatchLabel, error)

			// Switch on the message type. This is where the match logic is handled.
			switch msg := msg.(type) {
			default:
				logger.Warn("Unknown message type: %T", msg)
			}
			// Time the execution
			start := time.Now()
			// Execute the message function
			if messageFn != nil {
				state, err = messageFn(ctx, logger, db, nk, dispatcher, state, in, msg)
				if err != nil {
					logger.Error("match pipeline: %v", err)
				}
			}
			logger.Debug("Message %T took %dms", msg, time.Since(start)/time.Millisecond)
		}
	}
	return state
}

// MatchTerminate is called when the match is being terminated.
func (m *EvrMatch) MatchTerminate(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, graceSeconds int) interface{} {
	state, ok := state_.(*MatchLabel)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}
	logger.Info("MatchTerminate called. %v", state)
	nk.MetricsCounterAdd("match_terminate_count", state.MetricsTags(), 1)
	if state.broadcaster != nil {
		// Disconnect the players
		for _, presence := range state.presenceMap {
			logger.WithFields(map[string]any{
				"uid": presence.GetUserId(),
				"sid": presence.GetSessionId(),
			}).Warn("Match terminating, disconnecting player.")
			nk.SessionDisconnect(ctx, presence.GetPlayerSession(), runtime.PresenceReasonDisconnect)
		}
		// Disconnect the broadcasters session
		logger.WithFields(map[string]any{
			"uid": state.broadcaster.GetUserId(),
			"sid": state.broadcaster.GetSessionId(),
		}).Warn("Match terminating, disconnecting broadcaster.")

		nk.SessionDisconnect(ctx, state.broadcaster.GetSessionId(), runtime.PresenceReasonDisconnect)
	}

	return nil
}

func (m *EvrMatch) MatchShutdown(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, graceSeconds int) interface{} {
	state, ok := state_.(*MatchLabel)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}
	logger.Info("MatchShutdown called.")
	nk.MetricsCounterAdd("match_shutdown_count", state.MetricsTags(), 1)

	state.Open = false
	if err := m.updateLabel(dispatcher, state); err != nil {
		logger.Error("failed to update label: %v", err)
		return nil
	}

	if state.broadcaster != nil {

		for _, mp := range state.presenceMap {

			if err := nk.StreamUserKick(StreamModeMatchAuthoritative, mp.EntrantID.String(), "", mp.GetNodeId(), mp); err != nil {
				logger.WithFields(map[string]any{
					"uid": mp.GetUserId(),
					"sid": mp.GetSessionId(),
				}).Error("Failed to kick user from stream.")
			}

		}

	}

	state.terminateTick = tick + int64(graceSeconds)*state.tickRate

	return state
}

type SignalResponse struct {
	Success bool   `json:"success"`
	Message string `json:"message"`
	Payload string `json:"payload"`
}

func (r SignalResponse) String() string {
	b, err := json.Marshal(r)
	if err != nil {
		return ""
	}
	return string(b)
}

// MatchSignal is called when a signal is sent into the match.
func (m *EvrMatch) MatchSignal(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, data string) (interface{}, string) {
	state, ok := state_.(*MatchLabel)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil, SignalResponse{Message: "invalid match state"}.String()
	}

	// TODO protobuf's would be nice here.
	signal := &EvrSignal{}
	err := json.Unmarshal([]byte(data), signal)
	if err != nil {
		return state, SignalResponse{Message: fmt.Sprintf("failed to unmarshal signal: %v", err)}.String()
	}

	switch signal.Signal {
	case SignalTerminate:

		return m.MatchTerminate(ctx, logger, db, nk, dispatcher, tick, state, 10), SignalResponse{Success: true}.String()

	case SignalPruneUnderutilized:
		// Prune this match if it's utilization is low.
		if len(state.presenceMap) <= 3 {
			// Free the resources.
			return nil, SignalResponse{Success: true}.String()
		}
	case SignalGetEndpoint:
		jsonData, err := json.Marshal(state.Broadcaster.Endpoint)
		if err != nil {
			return state, fmt.Sprintf("failed to marshal endpoint: %v", err)
		}
		return state, SignalResponse{Success: true, Payload: string(jsonData)}.String()

	case SignalGetPresences:
		// Return the presences in the match.

		jsonData, err := json.Marshal(state.presenceMap)
		if err != nil {
			return state, fmt.Sprintf("failed to marshal presences: %v", err)
		}
		return state, SignalResponse{Success: true, Payload: string(jsonData)}.String()

	case SignalPrepareSession:

		// if the match is already started, return an error.
		if state.LobbyType != UnassignedLobby {
			logger.Error("Failed to prepare session: session already prepared")
			return state, SignalResponse{Message: "session already prepared"}.String()
		}

		var newState = MatchLabel{}
		if err := json.Unmarshal(signal.Payload, &newState); err != nil {
			return state, SignalResponse{Message: fmt.Sprintf("failed to unmarshal match label: %v", err)}.String()
		}

		state.Started = false
		state.Open = true

		state.Mode = newState.Mode
		switch newState.Mode {
		case evr.ModeArenaPublic, evr.ModeSocialPublic, evr.ModeCombatPublic:
			state.LobbyType = PublicLobby
		default:
			state.LobbyType = PrivateLobby
		}

		state.Level = newState.Level
		// validate the mode
		if levels, ok := evr.LevelsByMode[state.Mode]; !ok {
			return state, SignalResponse{Message: fmt.Sprintf("invalid mode: %v", state.Mode)}.String()
		} else {
			if state.Level == 0xffffffffffffffff || state.Level == 0 {
				state.Level = levels[rand.Intn(len(levels))]
			}
		}

		if newState.SpawnedBy != "" {
			state.SpawnedBy = newState.SpawnedBy
		} else {
			state.SpawnedBy = signal.UserId
		}

		state.GroupID = newState.GroupID
		if state.GroupID == nil {
			state.GroupID = &uuid.Nil
		}

		state.RequiredFeatures = newState.RequiredFeatures
		for _, f := range state.RequiredFeatures {
			if !slices.Contains(state.Broadcaster.Features, f) {
				return state, SignalResponse{Message: fmt.Sprintf("feature not supported: %v", f)}.String()
			}
		}

		settings := evr.NewSessionSettings(strconv.FormatUint(PcvrAppId, 10), state.Mode, state.Level, state.RequiredFeatures)
		state.SessionSettings = &settings

		state.MaxSize = newState.MaxSize
		if state.MaxSize < 2 || state.MaxSize > MatchMaxSize {
			state.MaxSize = MatchMaxSize
		}

		state.TeamSize = newState.TeamSize
		if state.TeamSize <= 0 || state.TeamSize > 5 {
			state.TeamSize = 5
		}

		state.StartTime = newState.StartTime
		if state.StartTime.IsZero() || state.StartTime.Before(time.Now()) {
			state.StartTime = time.Now()
		}
		state.sessionStartExpiry = tick + (15 * 60 * state.tickRate)

		if state.Mode == evr.ModeSocialPrivate || state.Mode == evr.ModeSocialPublic {
			state.PlayerLimit = int(state.MaxSize)
		} else {
			state.PlayerLimit = state.TeamSize * 2
		}

		state.TeamAlignments = make(map[string]int, MatchMaxSize)
		if newState.TeamAlignments != nil {
			for userID, role := range newState.TeamAlignments {
				if userID != "" {
					state.TeamAlignments[userID] = int(role)
				}
			}
		}

	case SignalStartSession:

		state, err := m.MatchStart(ctx, logger, nk, dispatcher, state)
		if err != nil {
			return state, SignalResponse{Message: fmt.Sprintf("failed to start session: %v", err)}.String()
		}

	case SignalEndSession:
		state.Open = false

	case SignalLockSession:
		state.Open = false

	case SignalUnlockSession:
		logger.Debug("ignoring unlock session request")
		//state.Open = true

	default:
		logger.Warn("Unknown signal: %v", signal.Signal)
		return state, SignalResponse{Success: false, Message: "unknown signal"}.String()
	}

	if err := m.updateLabel(dispatcher, state); err != nil {
		logger.Error("failed to update label: %v", err)
		return state, SignalResponse{Message: fmt.Sprintf("failed to update label: %v", err)}.String()
	}

	return state, SignalResponse{Success: true, Payload: state.String()}.String()

}

func (m *EvrMatch) MatchStart(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *MatchLabel) (*MatchLabel, error) {
	channel := uuid.Nil
	if state.GroupID != nil {
		channel = *state.GroupID
	}
	state.Started = true
	state.StartTime = time.Now()
	entrants := make([]evr.EvrId, 0)
	message := evr.NewGameServerSessionStart(state.ID.UUID(), channel, state.MaxSize, uint8(state.LobbyType), state.Broadcaster.AppId, state.Mode, state.Level, state.RequiredFeatures, entrants)
	logger.WithField("message", message).Info("Starting session.")
	messages := []evr.Message{
		message,
	}
	nk.MetricsCounterAdd("match_start_count", state.MetricsTags(), 1)
	// Dispatch the message for delivery.
	if err := m.dispatchMessages(ctx, logger, dispatcher, messages, []runtime.Presence{state.broadcaster}, nil); err != nil {
		return state, fmt.Errorf("failed to dispatch message: %v", err)
	}
	return state, nil
}

// SignalMatch is a helper function to send a signal to a match.
func SignalMatch(ctx context.Context, matchRegistry MatchRegistry, matchID MatchID, signalID int64, data interface{}) (string, error) {
	dataJson, err := json.Marshal(data)
	if err != nil {
		return "", fmt.Errorf("failed to marshal match label: %v", err)
	}
	signal := EvrSignal{
		Signal:  signalID,
		Payload: dataJson,
	}
	signalJson, err := json.Marshal(signal)
	if err != nil {
		return "", fmt.Errorf("failed to marshal match signal: %v", err)
	}
	responseJSON, err := matchRegistry.Signal(ctx, matchID.String(), string(signalJson))
	if err != nil {
		return "", fmt.Errorf("failed to signal match: %v", err)
	}
	response := SignalResponse{}
	if err := json.Unmarshal([]byte(responseJSON), &response); err != nil {
		return "", fmt.Errorf("failed to unmarshal response: %v", err)
	}
	if !response.Success {
		return "", fmt.Errorf("match signal response: %v", response.Message)
	}
	return response.Payload, nil
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
	if err := dispatcher.BroadcastMessageDeferred(OpCodeEVRPacketData, bytes, presences, sender, true); err != nil {
		return fmt.Errorf("could not broadcast message: %v", err)
	}
	return nil
}

func (m *EvrMatch) updateLabel(dispatcher runtime.MatchDispatcher, state *MatchLabel) error {
	state.rebuildCache()
	if err := dispatcher.MatchLabelUpdate(state.GetLabel()); err != nil {
		return fmt.Errorf("could not update label: %v", err)
	}
	return nil
}
