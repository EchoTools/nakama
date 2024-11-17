package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
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

	SocialLobbyMaxSize                       = 12 // The total max players (not including the broadcaster) for a EVR lobby.
	MatchLobbyMaxSize                        = 16
	LevelSelectionFirst  MatchLevelSelection = "first"
	LevelSelectionRandom MatchLevelSelection = "random"

	StatGroupArena  MatchStatGroup = "arena"
	StatGroupCombat MatchStatGroup = "combat"

	DefaultPublicArenaTeamSize  = 4
	DefaultPublicCombatTeamSize = 5

	// Defaults for public arena matches
	RoundDuration              = 300 * time.Second
	AfterGoalDuration          = 15 * time.Second
	RespawnDuration            = 3 * time.Second
	RoundCatapultDelayDuration = 5 * time.Second
	CatapultDuration           = 15 * time.Second
	RoundWaitDuration          = 59 * time.Second
	PreMatchWaitTime           = 45 * time.Second
	PublicMatchWaitTime        = PreMatchWaitTime + CatapultDuration + RoundCatapultDelayDuration
)

var (
	LobbySizeByMode = map[evr.Symbol]int{
		evr.ModeArenaPublic:   MatchLobbyMaxSize,
		evr.ModeArenaPrivate:  MatchLobbyMaxSize,
		evr.ModeCombatPublic:  MatchLobbyMaxSize,
		evr.ModeCombatPrivate: MatchLobbyMaxSize,
		evr.ModeSocialPublic:  SocialLobbyMaxSize,
		evr.ModeSocialPrivate: SocialLobbyMaxSize,
	}
)

func DefaultLobbySize(mode evr.Symbol) int {
	if size, ok := LobbySizeByMode[mode]; ok {
		return size
	}
	return MatchLobbyMaxSize
}

const (
	OpCodeBroadcasterDisconnected int64 = iota
	OpCodeEVRPacketData
	OpCodeMatchGameStateUpdate
)

type MatchStatGroup string
type MatchLevelSelection string

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
	SessionID       string       `json:"sid,omitempty"`              // The broadcaster's Session ID
	OperatorID      string       `json:"oper,omitempty"`             // The user id of the broadcaster.
	GroupIDs        []uuid.UUID  `json:"group_ids,omitempty"`        // The channels this broadcaster will host matches for.
	Endpoint        evr.Endpoint `json:"endpoint,omitempty"`         // The endpoint data used for connections.
	VersionLock     evr.Symbol   `json:"version_lock,omitempty"`     // The game build version. (EVR)
	AppId           string       `json:"app_id,omitempty"`           // The game app id. (EVR)
	Regions         []evr.Symbol `json:"regions,omitempty"`          // The region the match is hosted in. (Matching Only) (EVR)
	IPinfo          *ipinfo.Core `json:"ip_info,omitempty"`          // The IPinfo of the broadcaster.
	ServerID        uint64       `json:"server_id,omitempty"`        // The server id of the broadcaster. (EVR)
	PublisherLock   bool         `json:"publisher_lock,omitempty"`   // Publisher lock (EVR)
	Features        []string     `json:"features,omitempty"`         // The features of the broadcaster.
	Tags            []string     `json:"tags,omitempty"`             // The tags given on the urlparam for the match.
	DesignatedModes []evr.Symbol `json:"designated_modes,omitempty"` // The priority modes for the broadcaster.
}

func (g *MatchBroadcaster) IsPriorityFor(mode evr.Symbol) bool {
	return slices.Contains(g.DesignatedModes, mode)
}

type MatchSettings struct {
	Mode                evr.Symbol
	Level               evr.Symbol
	TeamSize            int
	StartTime           time.Time
	SpawnedBy           string
	GroupID             uuid.UUID
	RequiredFeatures    []string
	TeamAlignments      map[string]int
	Reservations        []*EvrMatchPresence
	ReservationLifetime time.Duration
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

const (
	BroadcasterJoinTimeoutSecs = 45
)

// MatchInit is called when the match is created.
func (m *EvrMatch) MatchInit(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, params map[string]interface{}) (interface{}, int, string) {

	gameserverConfig := MatchBroadcaster{}
	if err := json.Unmarshal([]byte(params["gameserver"].(string)), &gameserverConfig); err != nil {
		logger.Error("Failed to unmarshal gameserver config: %v", err)
		return nil, 0, ""
	}

	state := MatchLabel{
		CreatedAt:        time.Now().UTC(),
		Broadcaster:      gameserverConfig,
		Open:             false,
		LobbyType:        UnassignedLobby,
		Mode:             evr.ModeUnloaded,
		Level:            evr.LevelUnloaded,
		RequiredFeatures: make([]string, 0),
		Players:          make([]PlayerInfo, 0, SocialLobbyMaxSize),
		presenceMap:      make(map[string]*EvrMatchPresence, SocialLobbyMaxSize),
		reservationMap:   make(map[string]*slotReservation, 2),

		TeamAlignments:       make(map[string]int, SocialLobbyMaxSize),
		joinTimestamps:       make(map[string]time.Time, SocialLobbyMaxSize),
		joinTimeMilliseconds: make(map[string]int64, SocialLobbyMaxSize),
		emptyTicks:           0,
		tickRate:             10,
		RankPercentile:       0.0,
	}

	state.ID = MatchIDFromContext(ctx)

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
	ErrJoinRejectReasonUnassignedLobby           = errors.New("unassigned lobby")
	ErrJoinRejectReasonDuplicateJoin             = errors.New("duplicate join")
	ErrJoinRejectDuplicateEvrID                  = errors.New("duplicate evr id")
	ErrJoinRejectReasonLobbyFull                 = errors.New("lobby full")
	ErrJoinRejectReasonFailedToAssignTeam        = errors.New("failed to assign team")
	ErrJoinRejectReasonPartyMembersMustHaveRoles = errors.New("party members must have roles")
	ErrJoinRejectReasonMatchTerminating          = errors.New("match terminating")
	ErrJoinRejectReasonFeatureMismatch           = errors.New("feature mismatch")
)

type EntrantMetadata struct {
	Presence     *EvrMatchPresence
	Reservations []*EvrMatchPresence
}

func NewJoinMetadata(p *EvrMatchPresence) *EntrantMetadata {
	return &EntrantMetadata{Presence: p}
}

func (m EntrantMetadata) Presences() []*EvrMatchPresence {
	return append([]*EvrMatchPresence{m.Presence}, m.Reservations...)
}

func (m EntrantMetadata) ToMatchMetadata() map[string]string {

	bytes, err := json.Marshal(m)
	if err != nil {
		panic(err)
	}

	return map[string]string{
		"entrants": string(bytes),
	}
}

func (m *EntrantMetadata) FromMatchMetadata(md map[string]string) error {
	if v, ok := md["entrants"]; ok {
		return json.Unmarshal([]byte(v), m)
	}
	return fmt.Errorf("`entrants` key not found")
}

// MatchJoinAttempt decides whether to accept or deny the player session.
func (m *EvrMatch) MatchJoinAttempt(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, joinPresence runtime.Presence, metadata map[string]string) (interface{}, bool, string) {
	state, ok := state_.(*MatchLabel)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil, false, ""
	}

	logger = logger.WithFields(map[string]any{
		"mid":      state.ID.UUID.String(),
		"uid":      joinPresence.GetUserId(),
		"username": joinPresence.GetUsername()})

	if joinPresence.GetSessionId() == state.Broadcaster.SessionID {

		logger.Debug("Broadcaster joining the match.")
		state.server = joinPresence
		state.Open = true
		if err := m.updateLabel(dispatcher, state); err != nil {
			return state, false, fmt.Sprintf("failed to update label: %v", err)
		}
		return state, true, ""
	}

	if state.Started() && !state.Open {
		return state, false, ErrJoinRejectReasonMatchTerminating.Error()
	}

	if state.LobbyType == UnassignedLobby {
		return state, false, ErrJoinRejectReasonUnassignedLobby.Error()
	}

	// This is a player joining.
	meta := &EntrantMetadata{}
	if err := meta.FromMatchMetadata(metadata); err != nil {
		return state, false, fmt.Sprintf("failed to unmarshal metadata: %v", err)
	}

	// Remove any reservations of existing players (i.e. party members already in the match)
	for i := 0; i < len(meta.Reservations); i++ {
		s := meta.Reservations[i].GetSessionId()

		// Remove existing players from the reservation entrants
		if _, found := state.presenceMap[s]; found {
			meta.Reservations = append(meta.Reservations[:i], meta.Reservations[i+1:]...)
			i--
		}

		// Remove any duplicate reservations
		if _, found := state.reservationMap[s]; found {
			delete(state.reservationMap, s)
			state.rebuildCache()
		}

		// Ensure the player has the required features
		for _, f := range state.RequiredFeatures {
			if !slices.Contains(meta.Reservations[i].SupportedFeatures, f) {
				return state, false, ErrJoinRejectReasonFeatureMismatch.Error()
			}
		}
	}

	// If any entrants with the same EvrID are already in the match, reject the player
	for _, p := range state.presenceMap {

		// If the player is already in the match, reject the player
		if p.EvrID.Equals(meta.Presence.EvrID) {
			return state, false, ErrJoinRejectDuplicateEvrID.Error()
		}

		// If a reservation with the same EvrID is already in the match, reject the player
		for _, r := range meta.Reservations {
			if r.EvrID.Equals(p.EvrID) {
				return state, false, ErrJoinRejectDuplicateEvrID.Error()
			}
		}
	}

	// Ensure the match has enough slots available
	if state.OpenSlots() < len(meta.Presences()) {
		return state, false, ErrJoinRejectReasonLobbyFull.Error()
	}

	// If this is a reservation, load the reservation
	if e, found := state.LoadAndDeleteReservation(meta.Presence.GetSessionId()); found {
		meta.Presence = e
		state.rebuildCache()
		logger = logger.WithField("has_reservation", true)
	}

	// If this player has a team alignment, load it
	if teamIndex, ok := state.TeamAlignments[meta.Presence.GetUserId()]; ok {
		// Do not try to load the alignment if the player is a spectator or moderator
		if teamIndex != evr.TeamSpectator && teamIndex != evr.TeamModerator {
			meta.Presence.RoleAlignment = teamIndex
		}
	}

	// Public and social lobbies require a role alignment
	if meta.Presence.RoleAlignment == evr.TeamUnassigned {
		switch state.Mode {
		case evr.ModeSocialPublic, evr.ModeSocialPrivate:
			meta.Presence.RoleAlignment = evr.TeamSocial

		case evr.ModeArenaPublic, evr.ModeCombatPublic:
			// Select the team with the fewest players
			if state.RoleCount(evr.TeamBlue) < state.RoleCount(evr.TeamOrange) {
				meta.Presence.RoleAlignment = evr.TeamBlue
			} else {
				meta.Presence.RoleAlignment = evr.TeamOrange
			}
		}
	}

	// check the available slots
	if slots, err := state.OpenSlotsByRole(meta.Presence.RoleAlignment); err != nil {
		return state, false, ErrJoinRejectReasonFailedToAssignTeam.Error()
	} else if slots < len(meta.Presences()) {
		return state, false, ErrJoinRejectReasonLobbyFull.Error()
	}

	// Add reservations to the reservation map
	for _, p := range meta.Reservations {

		sessionID := p.GetSessionId()

		// Set the reservations roles to match the player
		p.RoleAlignment = meta.Presence.RoleAlignment

		// Add the reservation
		state.reservationMap[sessionID] = &slotReservation{
			Presence: p,
			Expiry:   time.Now().Add(time.Second * 15),
		}
		state.joinTimestamps[sessionID] = time.Now()
	}

	// Add the player
	sessionID := meta.Presence.GetSessionId()
	state.presenceMap[sessionID] = meta.Presence
	state.joinTimestamps[sessionID] = time.Now()

	if err := m.updateLabel(dispatcher, state); err != nil {
		logger.Error("Failed to update label: %v", err)
	}

	return state, true, meta.Presence.String()
}

func (m *EvrMatch) logJoin(logger runtime.Logger, nk runtime.NakamaModule, presence *EvrMatchPresence, state *MatchLabel) {

	metricsTags := map[string]string{
		"mode":     state.Mode.String(),
		"level":    state.Level.String(),
		"type":     state.LobbyType.String(),
		"role":     fmt.Sprintf("%d", presence.RoleAlignment),
		"group_id": state.GetGroupID().String(),
	}
	nk.MetricsCounterAdd("match_entrant_join_count", metricsTags, 1)
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

		// Remove the player's team align map if they are joining a public match.
		switch state.Mode {
		case evr.ModeArenaPublic, evr.ModeCombatPublic:
			delete(state.TeamAlignments, p.GetUserId())
		}

		// If the round clock is being used, set the join clock time
		if state.GameState != nil && state.GameState.RoundClock != nil {
			// Do not overwrite an existing value
			if _, ok := state.joinTimeMilliseconds[p.GetSessionId()]; !ok {
				state.joinTimeMilliseconds[p.GetSessionId()] = state.GameState.RoundClock.Current().Milliseconds()
			}
		}

		if mp, ok := state.presenceMap[p.GetSessionId()]; !ok {
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
			tags := map[string]string{
				"mode":     state.Mode.String(),
				"level":    state.Level.String(),
				"type":     state.LobbyType.String(),
				"role":     fmt.Sprintf("%d", mp.RoleAlignment),
				"group_id": state.GetGroupID().String(),
			}
			nk.MetricsCounterAdd("match_entrant_join_count", tags, 1)
			nk.MetricsTimerRecord("match_player_join_duration", tags, time.Since(state.joinTimestamps[p.GetSessionId()]))
		}
	}

	for _, p := range presences {
		delete(state.reservationMap, p.GetSessionId())
	}

	return state
}

var PresenceReasonKicked runtime.PresenceReason = 16

// MatchLeave is called after a player leaves the match.
func (m *EvrMatch) MatchLeave(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presences []runtime.Presence) interface{} {
	state, ok := state_.(*MatchLabel)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	for _, p := range presences {
		logger.WithField("presence", p).Debug("Player leaving the match.")
	}

	if state.Started() && state.server == nil && len(state.presenceMap) == 0 {
		// If the match is empty, and the broadcaster has left, then shut down.
		logger.Debug("Match is empty. Shutting down.")
		return nil
	}

	// if the broadcaster is in the presences, then shut down.
	for _, p := range presences {
		if p.GetSessionId() == state.Broadcaster.SessionID {
			state.server = nil

			logger.Debug("Broadcaster left the match. Shutting down.")
			return m.MatchShutdown(ctx, logger, db, nk, dispatcher, tick, state, 2)
		}
	}

	rejects := make([]uuid.UUID, 0)

	for _, p := range presences {

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
			if userPresences, err := nk.StreamUserList(StreamModeEntrant, mp.EntrantID(state.ID).String(), "", mp.GetNodeId(), true, true); err != nil {
				logger.Warn("Failed to list user streams: %v", err)

			} else if len(userPresences) > 0 {

				rejects = append(rejects, mp.EntrantID(state.ID))
				msg = "Removing player from game server."
				nk.MetricsCounterAdd("match_entrant_kick_count", tags, 1)
				if err := nk.StreamUserLeave(StreamModeEntrant, mp.EntrantID(state.ID).String(), "", mp.GetNodeId(), mp.GetUserId(), mp.GetSessionId()); err != nil {
					logger.Warn("Failed to leave user stream: %v", err)
				}
			}
			nk.MetricsCounterAdd("match_entrant_leave_count", tags, 1)
			logger.WithFields(map[string]interface{}{
				"username": mp.GetUsername(),
				"uid":      mp.GetUserId(),
			}).Info(msg)

			ts := state.joinTimestamps[mp.GetSessionId()]
			nk.MetricsTimerRecord("match_player_session_duration", tags, time.Since(ts))

			// Store the player's time in the match to a leaderboard

			if err := recordMatchTimeToLeaderboard(ctx, nk, mp.GetUserId(), mp.GetUsername(), state.Mode, int64(time.Since(ts).Seconds())); err != nil {
				logger.Warn("Failed to record match time to leaderboard: %v", err)
			}

			delete(state.presenceMap, p.GetSessionId())
			delete(state.joinTimestamps, p.GetSessionId())

		}
	}

	if len(rejects) > 0 {

		code := evr.PlayerRejectionReasonKickedFromServer
		msgs := []evr.Message{
			evr.NewGameServerEntrantRejected(code, rejects...),
			unrequireMessage,
		}
		if err := m.dispatchMessages(ctx, logger, dispatcher, msgs, []runtime.Presence{state.server}, nil); err != nil {
			logger.Warn("Failed to dispatch message: %v", err)
		}
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

func recordMatchTimeToLeaderboard(ctx context.Context, nk runtime.NakamaModule, userID, username string, mode evr.Symbol, matchTimeSecs int64) error {
	periods := []string{"alltime", "daily", "weekly"}

	for _, period := range periods {
		id := fmt.Sprintf("%s:%s:%s", mode.String(), "LobbyTime", period)

		// Write the record
		_, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, matchTimeSecs, 0, nil, nil)

		if err != nil {
			// Try to create the leaderboard
			err = nk.LeaderboardCreate(ctx, id, true, "desc", "incr", PeriodicityToSchedule(period), nil)

			if err != nil {
				return fmt.Errorf("Leaderboard create error: %v", err)
			} else {
				// Retry the write
				_, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, matchTimeSecs, 0, nil, nil)
				if err != nil {
					return fmt.Errorf("Leaderboard record write error: %v", err)
				}
			}
		}
	}

	return nil
}

// MatchLoop is called every tick of the match and handles state, plus messages from the client.
func (m *EvrMatch) MatchLoop(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, messages []runtime.MatchData) interface{} {
	state, ok := state_.(*MatchLabel)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	var err error
	var updateLabel bool

	// Handle the messages, one by one
	for _, in := range messages {
		switch in.GetOpCode() {
		case OpCodeMatchGameStateUpdate:
			update := MatchGameStateUpdate{}
			if err := json.Unmarshal(in.GetData(), &update); err != nil {
				logger.Error("Failed to unmarshal match update: %v", err)
				continue
			}

			if state.GameState != nil {
				logger.WithField("update", update).Debug("Received match update message.")
				gs := state.GameState
				u := update

				if len(u.Goals) > 0 {
					gs.Goals = append(gs.Goals, u.Goals...)
				}
				if state.GameState.RoundClock != nil {
					if u.CurrentGameClock != 0 {
						if u.PauseDuration != 0 {
							gs.RoundClock.UpdateWithPause(u.CurrentGameClock, u.PauseDuration)
						} else {
							gs.RoundClock.Update(u.CurrentGameClock)
						}
					}
				}

			}
			updateLabel = true
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

	if state.server == nil {
		state.emptyTicks++
		if state.emptyTicks > 60*state.tickRate {
			logger.Warn("Match has been empty for too long. Shutting down.")
			return m.MatchShutdown(ctx, logger, db, nk, dispatcher, tick, state, 20)
		}
	} else if state.emptyTicks > 0 {
		state.emptyTicks = 0
	}

	// If the match is terminating, terminate on the tick.
	if state.terminateTick != 0 {
		if tick >= state.terminateTick {
			logger.Debug("Match termination tick reached.")
			return m.MatchTerminate(ctx, logger, db, nk, dispatcher, tick, state, 0)
		}
		return state
	}

	if state.LobbyType == UnassignedLobby {
		return state
	}

	// Expire any slot reservations
	for id, r := range state.reservationMap {
		if time.Now().After(r.Expiry) {
			delete(state.reservationMap, id)
			updateLabel = true
		}
	}

	// If the match is prepared and the start time has been reached, start it.
	if !state.levelLoaded && (len(state.presenceMap) != 0 || state.Started()) {
		if state, err = m.MatchStart(ctx, logger, nk, dispatcher, state); err != nil {
			logger.Error("failed to start session: %v", err)
			return nil
		}
		if err := m.updateLabel(dispatcher, state); err != nil {
			return nil
		}
		return state
	}

	// If the match is empty, and the match has been empty for too long, then terminate the match.
	if state.Started() && len(state.presenceMap) == 0 {
		state.emptyTicks++
		if state.emptyTicks > 60*state.tickRate {
			logger.Warn("Started match has been empty for too long. Shutting down.")
			return m.MatchShutdown(ctx, logger, db, nk, dispatcher, tick, state, 20)
		}
	} else {
		state.emptyTicks = 0
	}

	// Update the game clock every second
	if tick%state.tickRate == 0 && state.GameState != nil {
		state.GameState.Update()
		updateLabel = true
	}

	if updateLabel {
		if err := m.updateLabel(dispatcher, state); err != nil {
			return nil
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
	state.Open = false
	logger.WithField("state", state).Info("MatchTerminate called.")
	nk.MetricsCounterAdd("match_terminate_count", state.MetricsTags(), 1)
	if state.server != nil {
		// Disconnect the players
		for _, presence := range state.presenceMap {
			logger.WithFields(map[string]any{
				"uid": presence.GetUserId(),
				"sid": presence.GetSessionId(),
			}).Warn("Match terminating, disconnecting player.")
			nk.SessionDisconnect(ctx, presence.EntrantID(state.ID).String(), runtime.PresenceReasonDisconnect)
		}
		// Disconnect the broadcasters session
		logger.WithFields(map[string]any{
			"uid": state.server.GetUserId(),
			"sid": state.server.GetSessionId(),
		}).Warn("Match terminating, disconnecting broadcaster.")

		nk.SessionDisconnect(ctx, state.server.GetSessionId(), runtime.PresenceReasonDisconnect)
	}

	return nil
}

func (m *EvrMatch) MatchShutdown(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, graceSeconds int) interface{} {
	state, ok := state_.(*MatchLabel)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}
	logger.WithField("state", state).Info("MatchShutdown called.")
	nk.MetricsCounterAdd("match_shutdown_count", state.MetricsTags(), 1)
	state.Open = false
	state.terminateTick = tick + int64(graceSeconds)*state.tickRate

	if err := m.updateLabel(dispatcher, state); err != nil {
		logger.Error("failed to update label: %v", err)
		return nil
	}

	if state.server != nil {
		for _, mp := range state.presenceMap {
			logger := logger.WithFields(map[string]any{
				"uid": mp.GetUserId(),
				"sid": mp.GetSessionId(),
			})
			logger.Warn("Match shutting down, disconnecting player.")
			if err := nk.StreamUserKick(StreamModeMatchAuthoritative, mp.EntrantID(state.ID).String(), "", mp.GetNodeId(), mp); err != nil {
				logger.Error("Failed to kick user from stream.")
			}
		}
	}

	return state
}

// MatchSignal is called when a signal is sent into the match.
func (m *EvrMatch) MatchSignal(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, data string) (interface{}, string) {
	state, ok := state_.(*MatchLabel)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil, SignalResponse{Message: "invalid match state"}.String()
	}

	// TODO protobuf's would be nice here.
	signal := &SignalEnvelope{}
	err := json.Unmarshal([]byte(data), signal)
	if err != nil {
		return state, SignalResponse{Message: fmt.Sprintf("failed to unmarshal signal: %v", err)}.String()
	}

	switch signal.OpCode {
	case SignalShutdown:

		var data SignalShutdownPayload

		if err := json.Unmarshal(signal.Payload, &data); err != nil {
			return state, SignalResponse{Message: fmt.Sprintf("failed to unmarshal shutdown payload: %v", err)}.String()
		}

		if data.DisconnectGameServer {
			logger.Warn("Match shutting down, disconnecting game server.")
			if state.server != nil {
				nk.SessionDisconnect(ctx, state.server.GetSessionId(), runtime.PresenceReasonDisconnect)
			}
		}

		if data.DisconnectUsers {
			for _, mp := range state.presenceMap {
				logger := logger.WithFields(map[string]any{
					"uid": mp.GetUserId(),
					"sid": mp.GetSessionId(),
				})
				logger.Warn("Match shutting down, disconnecting player.")
				if err := nk.StreamUserKick(StreamModeMatchAuthoritative, mp.EntrantID(state.ID).String(), "", mp.GetNodeId(), mp); err != nil {
					logger.Error("Failed to kick user from stream.")
				}
			}
		}

		return m.MatchShutdown(ctx, logger, db, nk, dispatcher, tick, state, data.GraceSeconds), SignalResponse{Success: true}.String()

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

		settings := MatchSettings{}

		if err := json.Unmarshal(signal.Payload, &settings); err != nil {
			return state, SignalResponse{Message: fmt.Sprintf("failed to unmarshal settings: %v", err)}.String()
		}

		for _, f := range settings.RequiredFeatures {
			if !slices.Contains(state.Broadcaster.Features, f) {
				return state, SignalResponse{Message: fmt.Sprintf("feature not supported: %v", f)}.String()
			}
		}

		// validate the mode
		if levels, ok := evr.LevelsByMode[settings.Mode]; !ok {
			return state, SignalResponse{Message: fmt.Sprintf("invalid mode: %v", state.Mode)}.String()
		} else {
			if settings.Level == 0xffffffffffffffff || settings.Level == 0 {
				settings.Level = levels[rand.Intn(len(levels))]
			} else {
				if !slices.Contains(levels, settings.Level) {
					return state, SignalResponse{Message: fmt.Sprintf("invalid level: %v", settings.Level)}.String()
				}

			}
		}

		state.Mode = settings.Mode
		state.Level = settings.Level
		state.RequiredFeatures = settings.RequiredFeatures
		state.SessionSettings = evr.NewSessionSettings(strconv.FormatUint(PcvrAppId, 10), state.Mode, state.Level, state.RequiredFeatures)
		state.GroupID = &settings.GroupID

		state.CreatedAt = time.Now().UTC()

		// If the start time is in the past, set it to now.
		// If the start time is not set, set it to 10 minutes from now.
		if settings.StartTime.IsZero() {
			state.StartTime = time.Now().UTC().Add(10 * time.Minute)
		} else if settings.StartTime.Before(time.Now()) {
			state.StartTime = time.Now().UTC()
		} else {
			state.StartTime = settings.StartTime.UTC()
		}

		if settings.SpawnedBy != "" {
			state.SpawnedBy = settings.SpawnedBy
		} else {
			state.SpawnedBy = signal.UserID
		}

		// Set the lobby and team sizes
		switch settings.Mode {

		case evr.ModeSocialPublic:
			state.LobbyType = PublicLobby
			state.MaxSize = SocialLobbyMaxSize
			state.TeamSize = SocialLobbyMaxSize
			state.PlayerLimit = SocialLobbyMaxSize

		case evr.ModeSocialPrivate:
			state.LobbyType = PrivateLobby
			state.MaxSize = SocialLobbyMaxSize
			state.TeamSize = SocialLobbyMaxSize
			state.PlayerLimit = SocialLobbyMaxSize

		case evr.ModeArenaPublic:
			state.LobbyType = PublicLobby
			state.MaxSize = MatchLobbyMaxSize
			state.TeamSize = DefaultPublicArenaTeamSize
			state.PlayerLimit = min(state.TeamSize*2, state.MaxSize)

		case evr.ModeCombatPublic:
			state.LobbyType = PublicLobby
			state.MaxSize = MatchLobbyMaxSize
			state.TeamSize = DefaultPublicCombatTeamSize
			state.PlayerLimit = min(state.TeamSize*2, state.MaxSize)

		default:
			state.LobbyType = PrivateLobby
			state.MaxSize = MatchLobbyMaxSize
			state.TeamSize = MatchLobbyMaxSize
			state.PlayerLimit = state.MaxSize
		}

		if settings.TeamSize > 0 && settings.TeamSize <= 5 {
			state.TeamSize = settings.TeamSize
			state.PlayerLimit = min(state.TeamSize*2, state.MaxSize)
		}

		state.TeamAlignments = make(map[string]int, state.MaxSize)

		for userID, role := range settings.TeamAlignments {
			if userID != "" {
				state.TeamAlignments[userID] = int(role)
			}
		}

		for _, e := range settings.Reservations {
			expiry := time.Now().Add(settings.ReservationLifetime)
			state.reservationMap[e.GetSessionId()] = &slotReservation{
				Presence: e,
				Expiry:   expiry,
			}
		}

	case SignalStartSession:

		if !state.Started() {
			state.StartTime = time.Now().UTC()
		} else {
			return state, SignalResponse{Message: "failed to start session: already started"}.String()
		}

	case SignalLockSession:
		logger.Debug("Locking session")
		state.Open = false

	case SignalUnlockSession:
		logger.Debug("Unlocking session")
		state.Open = true
	default:
		logger.Warn("Unknown signal: %v", signal.OpCode)
		return state, SignalResponse{Success: false, Message: "unknown signal"}.String()
	}

	if err := m.updateLabel(dispatcher, state); err != nil {
		logger.Error("failed to update label: %v", err)
		return state, SignalResponse{Message: fmt.Sprintf("failed to update label: %v", err)}.String()
	}

	return state, SignalResponse{Success: true, Payload: state.GetLabel()}.String()

}

func (m *EvrMatch) MatchStart(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *MatchLabel) (*MatchLabel, error) {
	groupID := uuid.Nil
	if state.GroupID != nil {
		groupID = *state.GroupID
	}

	switch state.Mode {
	case evr.ModeArenaPublic:
		state.GameState = &GameState{
			RoundClock: NewRoundClock(RoundDuration, time.Now().Add(PublicMatchWaitTime)),
			Goals:      make([]*MatchGoal, 0),
		}
	case evr.ModeArenaPrivate:
		state.GameState = &GameState{
			RoundClock: NewRoundClock(0, time.Time{}),
			Goals:      make([]*MatchGoal, 0),
		}
	}

	state.StartTime = time.Now().UTC()
	entrants := make([]evr.EvrId, 0)
	message := evr.NewGameServerSessionStart(state.ID.UUID, groupID, uint8(state.MaxSize), uint8(state.LobbyType), state.Broadcaster.AppId, state.Mode, state.Level, state.RequiredFeatures, entrants)
	logger.WithField("message", message).Info("Starting session.")
	messages := []evr.Message{
		message,
	}

	nk.MetricsCounterAdd("match_start_count", state.MetricsTags(), 1)
	// Dispatch the message for delivery.
	if err := m.dispatchMessages(ctx, logger, dispatcher, messages, []runtime.Presence{state.server}, nil); err != nil {
		return state, fmt.Errorf("failed to dispatch message: %w", err)
	}
	state.levelLoaded = true
	return state, nil
}

func (m *EvrMatch) dispatchMessages(_ context.Context, logger runtime.Logger, dispatcher runtime.MatchDispatcher, messages []evr.Message, presences []runtime.Presence, sender runtime.Presence) error {
	bytes := []byte{}
	for _, message := range messages {

		logger.Debug("Sending message from match: %v", message)
		payload, err := evr.Marshal(message)
		if err != nil {
			return fmt.Errorf("could not marshal message: %w", err)
		}
		bytes = append(bytes, payload...)
	}
	if err := dispatcher.BroadcastMessageDeferred(OpCodeEVRPacketData, bytes, presences, sender, true); err != nil {
		return fmt.Errorf("could not broadcast message: %w", err)
	}
	return nil
}

func (m *EvrMatch) updateLabel(dispatcher runtime.MatchDispatcher, state *MatchLabel) error {
	state.rebuildCache()
	if err := dispatcher.MatchLabelUpdate(state.GetLabel()); err != nil {
		return fmt.Errorf("could not update label: %w", err)
	}
	return nil
}

func (m *EvrMatch) kickEntrants(ctx context.Context, logger runtime.Logger, dispatcher runtime.MatchDispatcher, state *MatchLabel, entrantIDs ...uuid.UUID) error {
	return m.sendEntrantReject(ctx, logger, dispatcher, state, evr.PlayerRejectionReasonKickedFromServer, entrantIDs...)
}

func (m *EvrMatch) sendEntrantReject(ctx context.Context, logger runtime.Logger, dispatcher runtime.MatchDispatcher, state *MatchLabel, reason evr.PlayerRejectionReason, entrantIDs ...uuid.UUID) error {
	msg := evr.NewGameServerEntrantRejected(reason, entrantIDs...)
	if err := m.dispatchMessages(ctx, logger, dispatcher, []evr.Message{msg}, []runtime.Presence{state.server}, nil); err != nil {
		return fmt.Errorf("failed to dispatch message: %w", err)
	}
	return nil
}
