package backend

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"slices"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/echotools/nakama/v3/service"
	"github.com/echotools/nevr-common/v3/rtapi"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

type (
	MatchID            = service.MatchID
	Role               = service.RoleIndex
	LobbyPresence      = service.LobbyPresence
	GameServerPresence = service.GameServerPresence
	MatchSettings      = service.MatchSettings
	Label              = service.MatchLabel
	PlayerInfo         = service.PlayerInfo
	LobbyType          = service.LobbyType
	State              = service.LobbySessionState
	Reservation        = service.LobbySlotReservation
	EntrantMetadata    = service.EntrantMetadata
	MatchPresence      = service.LobbyPresence
)

var (
	LobbySizeByMode = map[evr.Symbol]int{
		evr.ModeArenaPublic:   service.MatchLobbyMaxSize,
		evr.ModeArenaPrivate:  service.MatchLobbyMaxSize,
		evr.ModeCombatPublic:  service.MatchLobbyMaxSize,
		evr.ModeCombatPrivate: service.MatchLobbyMaxSize,
		evr.ModeSocialPublic:  service.SocialLobbyMaxSize,
		evr.ModeSocialPrivate: service.SocialLobbyMaxSize,
	}
)

const (
	AnyTeam                = service.AnyTeam
	BlueTeam               = service.BlueTeam
	OrangeTeam             = service.OrangeTeam
	SocialLobbyParticipant = service.SocialLobbyParticipant
	Spectator              = service.Spectator
	Moderator              = service.Moderator
)

func DefaultLobbySize(mode evr.Symbol) int {
	if size, ok := LobbySizeByMode[mode]; ok {
		return size
	}
	return service.MatchLobbyMaxSize
}

const (
	OpCodeBroadcasterDisconnected int64 = iota
	OpCodeEVRPacketData
	OpCodeMatchGameStateUpdate
	OpCodeGameServerLobbyStatus
)

// This is the match handler for all matches.
// There always is one per broadcaster.
// The match is spawned and managed directly by nakama.
// The match can only be communicated with through service.MatchSignal() and service.MatchData messages.
type NEVRMatch struct{}

// MatchInit is called when the match is created.
func (m *NEVRMatch) MatchInit(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, params map[string]interface{}) (interface{}, int, string) {

	var gameServer *service.GameServerPresence
	if b, ok := params["gameserver"].([]byte); ok {
		if err := json.Unmarshal(b, &gameServer); err != nil {
			logger.Error("Failed to unmarshal gameserver config: %v", err)
			return nil, 0, ""
		}
	}

	state := State{
		// TODO: The label should be generated/returned by matchState.Label()
		ID:           MatchIDFromContext(ctx),
		CreateTime:   time.Now().UTC(),
		Server:       gameServer,
		Presences:    make(map[uuid.UUID]*LobbyPresence, service.SocialLobbyMaxSize),
		Reservations: make(map[uuid.UUID]*Reservation, 2),
		Goals:        make([]*evr.MatchGoal, 0),
		EmptyTicks:   0,
		TickRate:     10,
	}

	labelJson, err := json.Marshal(state.Label())
	if err != nil {
		logger.WithField("err", err).Error("Match label marshal error.")
		return nil, 0, ""
	}
	if state.TickRate == 0 {
		state.TickRate = 10
	}

	return &state, int(state.TickRate), string(labelJson)
}

// MatchIDFromContext is a helper function to extract the match id from the context.
func MatchIDFromContext(ctx context.Context) service.MatchID {
	matchIDStr, ok := ctx.Value(runtime.RUNTIME_CTX_MATCH_ID).(string)
	if !ok {
		return service.MatchID{}
	}
	matchID := service.MatchIDFromStringOrNil(matchIDStr)
	return matchID
}

// MatchJoinAttempt decides whether to accept or deny the player session.
func (m *NEVRMatch) MatchJoinAttempt(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, joinPresence runtime.Presence, metadata map[string]string) (interface{}, bool, string) {
	state, ok := state_.(*State)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil, false, ""
	}

	logger = logger.WithFields(map[string]any{
		"mid":      state.ID.UUID.String(),
		"uid":      joinPresence.GetUserId(),
		"username": joinPresence.GetUsername()})

	if joinPresence.GetSessionId() == state.Server.SessionID.String() {
		// This is the game server joining.
		logger.Debug("Server joining the match.")
		state.SetLocked()

		if err := m.updateLabel(logger, dispatcher, state); err != nil {
			logger.Error("Failed to update label: %v", err)
		}
		return state, true, ""
	}

	if state.Metadata.Visibility == service.UnassignedLobby {
		return state, false, service.ErrJoinRejectReasonUnassignedLobby.Error()
	}

	// This is a player joining.
	meta := &service.EntrantMetadata{}
	if err := meta.FromMatchMetadata(metadata); err != nil {
		return state, false, fmt.Sprintf("failed to unmarshal metadata: %v", err)
	}

	// Check if the match is locked.
	if !state.IsOpen() {

		// Reject if the match is terminating
		if state.IsStarted() && state.TerminateTick > 0 {
			return state, false, service.ErrJoinRejectReasonMatchTerminating.Error()
		}

		// Only allow spectators to join closed/ending matches
		if !meta.Presence.IsSpectator() {
			return state, false, service.ErrJoinRejectReasonMatchClosed.Error()
		}
	}

	// Remove any reservations of existing players (i.e. party members already in the match)
	for i := 0; i < len(meta.Reservations); i++ {
		sID := meta.Reservations[i].SessionID

		// Remove any existing reservations for this player.
		if _, found := state.Reservations[sID]; found {
			delete(state.Reservations, sID)
		}

		// Remove existing players from the reservation entrants
		if _, found := state.Presences[sID]; found {
			meta.Reservations = slices.Delete(meta.Reservations, i, i+1)
			i--
			continue
		}

		// Ensure the player has the required features
		for _, f := range state.Metadata.RequiredFeatures {
			if !slices.Contains(meta.Reservations[i].SupportedFeatures, f) {
				return state, false, service.ErrJoinRejectReasonFeatureMismatch.Error()
			}
		}
	}

	// Check both the match's presence and reservation map for a duplicate join with the same service.XPID
	for _, p := range meta.Presences() {
		for _, e := range state.Presences {
			if e.XPID.Equals(p.XPID) {
				logger.WithFields(map[string]interface{}{
					"evrid": p.XPID,
					"uid":   p.GetUserId(),
				}).Error("Duplicate service.EVR-ID join attempt.")
				return state, false, service.ErrJoinRejectDuplicateXPID.Error()
			}
		}
	}

	// Ensure the match has enough slots available
	if state.OpenSlots() < len(meta.Presences()) {
		return state, false, service.ErrJoinRejectReasonLobbyFull.Error()
	}

	// If this is a reservation, load the reservation
	if e, found := state.LoadAndDeleteReservation(meta.Presence.SessionID); found {
		meta.Presence.PartyID = e.PartyID
		meta.Presence.RoleAlignment = e.RoleAlignment
		logger = logger.WithField("has_reservation", true)
	}

	// If this player has a team alignment, load it
	if teamIndex, ok := state.Alignments[meta.Presence.UserID]; ok {
		// Do not try to load the alignment if the player is a spectator or moderator
		if teamIndex != service.Spectator && teamIndex != Moderator {
			meta.Presence.RoleAlignment = teamIndex
		}
	}

	// Public and social lobbies require a role alignment
	if meta.Presence.RoleAlignment == service.AnyTeam {
		switch state.Metadata.Mode {
		case evr.ModeSocialPublic, evr.ModeSocialPrivate:
			meta.Presence.RoleAlignment = service.SocialLobbyParticipant

		case evr.ModeArenaPublic, evr.ModeCombatPublic:
			// Select the team with the fewest players
			if state.RoleCount(service.BlueTeam) < state.RoleCount(service.OrangeTeam) {
				meta.Presence.RoleAlignment = service.BlueTeam
			} else {
				meta.Presence.RoleAlignment = service.OrangeTeam
			}
		}
	}

	// Ensure the player has a role alignment
	metricsTags := map[string]string{
		"mode":     state.Metadata.Mode.String(),
		"level":    state.Metadata.Level.String(),
		"type":     state.Metadata.Visibility.String(),
		"role":     fmt.Sprintf("%d", meta.Presence.RoleAlignment),
		"group_id": state.Metadata.GroupID.String(),
	}
	if nk != nil { // for testing
		nk.MetricsCounterAdd("match_entrant_join_count", metricsTags, 1)
	}

	// check the available slots
	if slots, err := state.OpenSlotsByRole(meta.Presence.RoleAlignment); err != nil {
		return state, false, service.ErrJoinRejectReasonFailedToAssignTeam.Error()
	} else if slots < len(meta.Presences()) {
		return state, false, service.ErrJoinRejectReasonLobbyFull.Error()
	}

	// Add reservations to the reservation map
	for _, p := range meta.Reservations {
		// Set the reservations roles to match the player
		p.RoleAlignment = meta.Presence.RoleAlignment

		// Add the reservation
		reservation := &Reservation{
			LobbyPresence:     p,
			ReservationExpiry: time.Now().Add(time.Second * 15),
		}
		if state.Metadata.Mode == evr.ModeSocialPublic {
			// Reserve spots for party members for 5 minutes
			reservation.ReservationExpiry = time.Now().Add(time.Minute * 5)
		}
		state.Reservations[p.SessionID] = reservation
		state.JoinTimes[p.SessionID] = time.Now()
	}

	// Add the player
	state.Presences[meta.Presence.SessionID] = meta.Presence
	state.JoinTimes[meta.Presence.SessionID] = time.Now()

	if err := m.updateLabel(logger, dispatcher, state); err != nil {
		logger.Error("Failed to update label: %v", err)
	}

	// Check team sizes
	if state.Mode() == evr.ModeArenaPublic {
		if state.RoleCount(BlueTeam) > state.TeamSize() || state.RoleCount(OrangeTeam) > state.TeamSize() {
			logger.WithFields(map[string]interface{}{
				"blue":   state.RoleCount(BlueTeam),
				"orange": state.RoleCount(OrangeTeam),
			}).Error("Oversized team.")
		}
	}

	return state, true, meta.Presence.String()
}

// MatchJoin is called after the join attempt.
// MatchJoin updates the match data, and should not have any decision logic.
func (m *NEVRMatch) MatchJoin(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presences []runtime.Presence) interface{} {
	state, ok := state_.(*State)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	for _, p := range presences {
		// Game servers don't get added to the presence map.
		if p.GetSessionId() == state.Server.SessionID.String() {
			continue
		}
		userID := uuid.FromStringOrNil(p.GetUserId())
		sessionID := uuid.FromStringOrNil(p.GetSessionId())
		// Remove the player's team align map if they are joining a public match.
		switch state.Mode() {
		case evr.ModeArenaPublic, evr.ModeCombatPublic:
			delete(state.TeamAlignments(), userID)
		}

		if mp, ok := state.Presences[sessionID]; !ok {
			logger.WithFields(map[string]interface{}{
				"username": p.GetUsername(),
				"uid":      p.GetUserId(),
			}).Error("Presence not found. this should never happen.")
			return nil
		} else {
			logger.WithFields(map[string]interface{}{
				"username": p.GetUsername(),
				"uid":      p.GetUserId(),
				"sid":      p.GetSessionId(),
				"role":     fmt.Sprintf("%d", mp.RoleAlignment),
			}).Info("Player joining the match.")
			tags := map[string]string{
				"mode":     state.Mode().String(),
				"level":    state.Level().String(),
				"type":     state.Visibility().String(),
				"role":     fmt.Sprintf("%d", mp.RoleAlignment),
				"group_id": state.GroupID(),
			}
			nk.MetricsCounterAdd("match_entrant_join_count", tags, 1)
			nk.MetricsTimerRecord("match_player_join_duration", tags, time.Since(state.JoinTimes[sessionID]))
		}

		service.MatchDataEvent(ctx, nk, state.ID, service.MatchDataPlayerJoin{
			Presence: state.Presences[sessionID],
			State:    state,
		})

	}

	//m.updateLabel(logger, dispatcher, state)
	return state
}

var PresenceReasonKicked runtime.PresenceReason = 16

// MatchLeave is called after a player leaves the match.
func (m *NEVRMatch) MatchLeave(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presences []runtime.Presence) interface{} {
	state, ok := state_.(*State)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	node := ctx.Value(runtime.RUNTIME_CTX_NODE).(string)

	for _, p := range presences {
		sessionID := uuid.FromStringOrNil(p.GetSessionId())
		class := "Player"
		_, ok := state.Presences[sessionID]
		if !ok {
			logger.WithFields(map[string]interface{}{
				"username": p.GetUsername(),
				"uid":      p.GetUserId(),
				"sid":      sessionID,
			}).Warn("Presence not found in match state.")
			continue
		}

		joinTime := state.JoinTimes[sessionID]
		if joinTime.IsZero() {
			joinTime = state.CreateTime
			class = "Server"
		}
		logger.WithFields(map[string]interface{}{
			"username": p.GetUsername(),
			"uid":      p.GetUserId(),
			"sid":      p.GetSessionId(),
			"reason":   p.GetReason(),
			"duration": time.Since(joinTime),
		}).Debug(class + " leaving the match.")
	}

	if state.IsStarted() && len(state.Presences) == 0 {
		// If the match is empty, and the server has left, then shut down.
		logger.Debug("Match is empty. service.Shutting down.")
		return nil
	}

	// if the server is in the presences, then shut down.
	for _, p := range presences {
		if p.GetSessionId() == state.Server.SessionID.String() {
			logger.Debug("Server left the match. service.Shutting down.")
			state = nil
			return m.MatchShutdown(ctx, logger, db, nk, dispatcher, tick, state, 2)
		}
	}

	rejects := make([]uuid.UUID, 0)

	for _, p := range presences {
		logger := logger.WithFields(map[string]interface{}{
			"username": p.GetUsername(),
			"uid":      p.GetUserId(),
			"sid":      p.GetSessionId(),
		})
		sessionID := uuid.FromStringOrNil(p.GetSessionId())
		reason := ""
		switch p.GetReason() {
		case runtime.PresenceReasonUnknown:
			reason = "unknown"
		case runtime.PresenceReasonJoin:
			reason = "join"
		case runtime.PresenceReasonUpdate:
			reason = "update"
		case runtime.PresenceReasonLeave:
			reason = "leave"
		case runtime.PresenceReasonDisconnect:
			reason = "disconnect"
		default:
			reason = "unknown"
		}

		if mp, ok := state.Presences[sessionID]; ok {

			tags := map[string]string{
				"mode":     state.Mode().String(),
				"level":    state.Level().String(),
				"type":     state.Visibility().String(),
				"role":     fmt.Sprintf("%d", mp.RoleAlignment),
				"group_id": state.GroupID(),
				"reason":   reason,
			}

			// The entrant stream presence is only present when the player has not disconnect from the server yet.
			if userPresences, err := nk.StreamUserList(service.StreamModeEntrant, mp.EntrantID(state.ID).String(), "", node, false, true); err != nil {
				logger.Error("Failed to list user streams: %v", err)

			} else if len(userPresences) > 0 || p.GetReason() == runtime.PresenceReasonDisconnect {
				tags["reject_sent"] = "true"

				rejects = append(rejects, mp.EntrantID(state.ID))

				if err := nk.StreamUserLeave(service.StreamModeEntrant, mp.EntrantID(state.ID).String(), "", node, mp.GetUserId(), mp.GetSessionId()); err != nil {
					logger.Warn("Failed to leave user stream: %v", err)
				}

			} else {
				tags["reject_sent"] = "false"
				logger.Info("Player disconnected from game server. service.Removing from handler.")
			}

			nk.MetricsCounterAdd("match_entrant_leave_count", tags, 1)

			if err := service.MatchDataEvent(ctx, nk, state.ID, service.MatchDataPlayerLeave{
				Label:    state.Label(),
				Presence: mp,
				Reason:   reason,
			}); err != nil {
				logger.Error("Failed to send match data event: %v", err)
			}

			ts := state.JoinTimes[mp.SessionID]
			nk.MetricsTimerRecord("match_player_session_duration", tags, time.Since(ts))

			// Store the player's time in the match to a leaderboard

			if err := recordMatchTimeToLeaderboard(ctx, nk, mp.GetUserId(), mp.DisplayName, state.GroupID(), state.Mode(), int64(time.Since(ts).Seconds())); err != nil {
				logger.Warn("Failed to record match time to leaderboard: %v", err)
			}

			// If the round is not over, then add an early quit count to the player.
			if state.Mode() == evr.ModeArenaPublic && time.Since(state.StartTime) >= (time.Second*60) && state.GameState != nil && state.GameState().MatchOver == false {

				for _, p := range presences {
					sessionID := uuid.FromStringOrNil(p.GetSessionId())
					if mp, ok := state.Presences[sessionID]; ok {
						// Only players
						if !mp.IsPlayer() {
							continue
						}

						nk.MetricsCounterAdd("match_entrant_early_quit", tags, 1)

						logger.WithFields(map[string]interface{}{
							"uid":          mp.GetUserId(),
							"username":     mp.Username,
							"evrid":        mp.XPID,
							"display_name": mp.DisplayName,
						}).Debug("Incrementing early quit for player.")

						for _, r := range [...]evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime} {
							boardID := service.StatisticBoardID(state.GroupID(), state.Mode(), service.EarlyQuitStatisticID, r)

							if _, err := nk.LeaderboardRecordWrite(ctx, boardID, mp.UserID.String(), mp.DisplayName, 1, 0, nil, nil); err != nil {
								if errors.Is(err, server.ErrLeaderboardNotFound) {
									if err = nk.LeaderboardCreate(ctx, boardID, true, "desc", "incr", service.ResetScheduleToCron(r), nil, true); err != nil {
										logger.Warn("Failed to create early quit leaderboard: %v", err)
									}
								}
								logger.Warn("Failed to write early quit record: %v", err)
							}
						}
					}
				}
			}

			delete(state.Presences, sessionID)
			delete(state.JoinTimes, sessionID)

		}
	}

	if len(rejects) > 0 {
		code := evr.PlayerRejectionReasonDisconnected
		msgs := []evr.Message{
			evr.NewGameServerEntrantRejected(code, rejects...),
		}
		logger.WithFields(map[string]interface{}{
			"rejects": rejects,
			"code":    code,
		}).Debug("Sending reject message to game server.")

		if err := m.dispatchMessages(ctx, logger, dispatcher, msgs, []runtime.Presence{state.Server}, nil); err != nil {
			logger.Warn("Failed to dispatch message: %v", err)
		}
	}
	if len(state.Presences) == 0 {
		// Lock the match
		state.LockTime = time.Now()
		logger.Debug("Match is empty. service.Closing it.")
	}

	// Update the label that includes the new player list.
	if err := m.updateLabel(logger, dispatcher, state); err != nil {
		return nil
	}

	return state
}

func recordGameServerTimeToLeaderboard(ctx context.Context, nk runtime.NakamaModule, userID, username, groupID string, mode evr.Symbol, matchTimeSecs int64) error {
	resetSchedules := []evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime}
	if matchTimeSecs <= 0 {
		return nil
	}

	for _, period := range resetSchedules {
		id := service.StatisticBoardID(groupID, mode, service.GameServerTimeStatisticsID, period)

		// Write the record
		if _, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, matchTimeSecs, 0, nil, nil); err != nil {

			// Try to create the leaderboard
			if err = nk.LeaderboardCreate(ctx, id, true, "desc", "incr", service.ResetScheduleToCron(period), nil, true); err != nil {
				return fmt.Errorf("Leaderboard create error: %w", err)
			} else if _, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, matchTimeSecs, 0, nil, nil); err != nil {
				return fmt.Errorf("Leaderboard record write error: %w", err)
			}
		}
	}
	return nil
}

func recordMatchTimeToLeaderboard(ctx context.Context, nk runtime.NakamaModule, userID, displayName, groupID string, mode evr.Symbol, matchTimeSecs int64) error {
	resetSchedules := []evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime}
	if matchTimeSecs <= 0 {
		return nil
	}

	for _, period := range resetSchedules {
		id := service.StatisticBoardID(groupID, mode, service.LobbyTimeStatisticID, period)

		// Write the record
		if _, err := nk.LeaderboardRecordWrite(ctx, id, userID, displayName, matchTimeSecs, 0, nil, nil); err != nil {

			// Try to create the leaderboard
			if err = nk.LeaderboardCreate(ctx, id, true, "desc", "incr", service.ResetScheduleToCron(period), nil, true); err != nil {
				return fmt.Errorf("Leaderboard create error: %w", err)
			} else if _, err := nk.LeaderboardRecordWrite(ctx, id, userID, displayName, matchTimeSecs, 0, nil, nil); err != nil {
				return fmt.Errorf("Leaderboard record write error: %w", err)
			}
		}
	}
	return nil
}

// MatchLoop is called every tick of the match and handles state, plus messages from the client.
func (m *NEVRMatch) MatchLoop(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, messages []runtime.MatchData) interface{} {
	state, ok := state_.(*State)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	var err error
	var updateLabel bool

	// Handle the messages, one by one
	for _, in := range messages {
		switch in.GetOpCode() {
		case service.OpCodeMatchGameStateUpdate:

			update := service.MatchGameStateUpdate{}
			if err := json.Unmarshal(in.GetData(), &update); err != nil {
				logger.Error("Failed to unmarshal match update: %v", err)
				continue
			}

			if state.GameState != nil {
				logger.WithField("update", update).Debug("Received match update message.")
				gs := state.GameState()
				u := update

				if len(u.Goals) > 0 {
					state.Goals = append(state.Goals, u.Goals...)
				}

				if update.MatchOver {
					state.GameState().MatchOver = true
				}

				if state.GameState().RoundClock != nil {
					if u.CurrentGameClock != 0 {
						if u.PauseDuration != 0 {
							gs.RoundClock.UpdateWithPause(u.CurrentGameClock, u.PauseDuration)
						} else {
							gs.RoundClock.Update(u.CurrentGameClock)
						}
					}
				}
				updateLabel = true
			}
		case service.OpCodeGameServerLobbyStatus:
			/*
				lobbyStatus := evr.NEVRLobbyStatusV1{}
				if err := json.Unmarshal(in.GetData(), &lobbyStatus); err != nil {
					logger.Error("Failed to unmarshal lobby status: %v", err)
					continue
				}
				for _, s := range lobbyStatus.Slots {
					if s == nil {
						continue
					}

					// Find the player in the match
					presence := state.presenceByXPID[s.XPlatformId]
					if presence == nil {
						logger.Warn("Player not found in match: %s", s.XPlatformId)
						continue
					}
					presence.PingMillis = int(s.Ping)
					presence.RoleAlignment = int(s.TeamIndex)
				}
			*/
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

			var messageFn func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *State, in runtime.MatchData, msg evr.Message) (*State, error)

			// Switch on the message type. service.This is where the match logic is handled.
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

	if state == nil {
		state.EmptyTicks++
		if state.EmptyTicks > 60*state.TickRate {
			logger.Warn("Match has been empty for too long. service.Shutting down.")
			return m.MatchShutdown(ctx, logger, db, nk, dispatcher, tick, state, 20)
		}
	} else if state.EmptyTicks > 0 {
		state.EmptyTicks = 0
	}

	// If the match is terminating, terminate on the tick.
	if state.TerminateTick != 0 {
		if tick >= state.TerminateTick {
			logger.Debug("Match termination tick reached.")
			return m.MatchTerminate(ctx, logger, db, nk, dispatcher, tick, state, 0)
		}
		return state
	}

	if state.Visibility() == service.UnassignedLobby {
		return state
	}

	// Expire any slot reservations
	for id, r := range state.Reservations {
		if time.Now().After(r.ReservationExpiry) {
			delete(state.Reservations, id)
			updateLabel = true
		}
	}

	// If the match is prepared and the start time has been reached, start it.
	if !state.IsLoaded && (len(state.Presences) != 0 || state.IsStarted()) {
		if state, err = m.MatchStart(ctx, logger, nk, dispatcher, state); err != nil {
			logger.Error("failed to start session: %v", err)
			return nil
		}
		if err := m.updateLabel(logger, dispatcher, state); err != nil {
			logger.Error("failed to update label: %v", err)
			return nil
		}
		return state
	}

	// If the match is empty, and the match has been empty for too long, then terminate the match.
	if state.IsStarted() && len(state.Presences) == 0 {
		state.EmptyTicks++
		if state.EmptyTicks > 60*state.TickRate {
			logger.Warn("Started match has been empty for too long. service.Shutting down.")
			return m.MatchShutdown(ctx, logger, db, nk, dispatcher, tick, state, 20)
		}
	} else {
		state.EmptyTicks = 0
	}

	// Update the game clock every three seconds
	if tick%(state.TickRate*3) == 0 && state.GameState != nil {
		state.GameState().Update(state.Goals)
		updateLabel = true
	}

	if updateLabel {
		if err := m.updateLabel(logger, dispatcher, state); err != nil {
			logger.Error("failed to update label: %v", err)
			return nil
		}
	}

	return state
}

// MatchTerminate is called when the match is being terminated.
func (m *NEVRMatch) MatchTerminate(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, graceSeconds int) interface{} {
	state, ok := state_.(*State)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}
	state.SetLocked()
	logger.Debug("MatchTerminate called.")
	nk.MetricsCounterAdd("match_terminate_count", state.MetricsTags(), 1)
	if state != nil {
		// Disconnect the players
		for _, presence := range state.Presences {
			logger.WithFields(map[string]any{
				"uid": presence.GetUserId(),
				"sid": presence.GetSessionId(),
			}).Warn("Match terminating, disconnecting player.")
			nk.SessionDisconnect(ctx, presence.EntrantID(state.ID).String(), runtime.PresenceReasonDisconnect)
		}
		// Disconnect the broadcasters session
		logger.WithFields(map[string]any{
			"uid": state.Server.GetUserId(),
			"sid": state.Server.GetSessionId(),
		}).Warn("Match terminating, disconnecting broadcaster.")

		nk.SessionDisconnect(ctx, state.Server.GetSessionId(), runtime.PresenceReasonDisconnect)
	}

	// Cleanup service.Discord session message if it exists
	if appBot := service.GlobalAppBot.Load(); appBot != nil && appBot.SessionsChannelManager != nil {
		appBot.SessionsChannelManager.RemoveSessionMessage(state.ID.String())
	}

	return nil
}

func (m *NEVRMatch) MatchShutdown(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, graceSeconds int) interface{} {
	state, ok := state_.(*State)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}
	logger.WithField("state", state).Info("MatchShutdown called.")
	nk.MetricsCounterAdd("match_shutdown_count", state.MetricsTags(), 1)

	nk.MetricsTimerRecord("lobby_session_duration", state.MetricsTags(), time.Since(state.StartTime))
	if state != nil && slices.Contains(service.ValidLeaderboardModes, state.Mode()) {
		if err := recordGameServerTimeToLeaderboard(ctx, nk, state.Server.GetUserId(), state.Server.GetUsername(), state.GroupID(), state.Mode(), int64(time.Since(state.StartTime).Seconds())); err != nil {
			logger.Warn("Failed to record game server time to leaderboard: %v", err)
		}
	}

	state.SetLocked()

	state.TerminateTick = tick + int64(graceSeconds)*state.TickRate

	if err := m.updateLabel(logger, dispatcher, state); err != nil {
		logger.Error("failed to update label: %v", err)
		return nil
	}

	if state != nil {

		entrantIDs := make([]uuid.UUID, 0, len(state.Presences))

		for _, mp := range state.Presences {
			logger := logger.WithFields(map[string]any{
				"uid": mp.GetUserId(),
				"sid": mp.GetSessionId(),
			})
			logger.Warn("Match shutting down, disconnecting player.")
			for _, p := range state.Presences {
				if mp.XPID.Equals(p.XPID) {
					entrantIDs = append(entrantIDs, p.EntrantID(state.ID))
				}
			}
		}

		if len(entrantIDs) > 0 {
			go func(server runtime.Presence, entrantIDs []uuid.UUID) {
				<-time.After(time.Second * 5) // Give the game server time to process the return to lobby message.
				if err := m.sendEntrantReject(ctx, logger, dispatcher, server, evr.PlayerRejectionReasonLobbyEnding, entrantIDs...); err != nil {
					logger.Error("Failed to send entrant reject: %v", err)
				}
			}(state.Server, entrantIDs)
		}
	}

	return state
}

// MatchSignal is called when a signal is sent into the match.
func (m *NEVRMatch) MatchSignal(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, data string) (interface{}, string) {
	state, ok := state_.(*State)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil, service.SignalResponse{Message: "invalid match state"}.String()
	}

	// TODO protobuf's would be nice here.
	signal := &service.SignalEnvelope{}
	err := json.Unmarshal([]byte(data), signal)
	if err != nil {
		return state, service.SignalResponse{Message: fmt.Sprintf("failed to unmarshal signal: %v", err)}.String()
	}

	switch signal.OpCode {
	case service.SignalKickEntrants:
		var data service.SignalKickEntrantsPayload

		if err := json.Unmarshal(signal.Payload, &data); err != nil {
			return state, service.SignalResponse{Message: fmt.Sprintf("failed to unmarshal shutdown payload: %v", err)}.String()
		}
		entrantIDs := make([]uuid.UUID, 0, len(data.UserIDs))
		for _, e := range data.UserIDs {
			for _, p := range state.Presences {
				if p.UserID == e {
					entrantIDs = append(entrantIDs, p.EntrantID(state.ID))
				}
			}
		}

		if len(entrantIDs) > 0 {
			if err := m.kickEntrants(ctx, logger, dispatcher, state, entrantIDs...); err != nil {
				return state, service.SignalResponse{Message: fmt.Sprintf("failed to kick player: %v", err)}.String()
			}
		}

		logger.WithFields(map[string]any{
			"requested_user_ids": data.UserIDs,
			"entrant_ids":        entrantIDs,
		}).Debug("Kicking players from the match.")

	case service.SignalShutdown:

		var data service.SignalShutdownPayload

		if err := json.Unmarshal(signal.Payload, &data); err != nil {
			return state, service.SignalResponse{Message: fmt.Sprintf("failed to unmarshal shutdown payload: %v", err)}.String()
		}

		if data.DisconnectGameServer {
			logger.Warn("Match shutting down, disconnecting game server.")
			if state != nil {
				nk.SessionDisconnect(ctx, state.Server.GetSessionId(), runtime.PresenceReasonDisconnect)
			}
		}

		if data.DisconnectUsers {
			entrantIDs := make([]uuid.UUID, 0, len(state.Presences))
			for _, mp := range state.Presences {
				logger := logger.WithFields(map[string]any{
					"uid": mp.GetUserId(),
					"sid": mp.GetSessionId(),
				})

				for _, p := range state.Presences {
					entrantIDs = append(entrantIDs, p.EntrantID(state.ID))
				}

				if len(entrantIDs) > 0 {
					if err := m.kickEntrants(ctx, logger, dispatcher, state, entrantIDs...); err != nil {
						return state, service.SignalResponse{Message: fmt.Sprintf("failed to kick player: %v", err)}.String()
					}
				}

				logger.Warn("Match shutting down, disconnecting players.")
			}
		}

		return m.MatchShutdown(ctx, logger, db, nk, dispatcher, tick, state, data.GraceSeconds), service.SignalResponse{Success: true}.String()

	case service.SignalPruneUnderutilized:
		// Prune this match if it's utilization is low.
		if len(state.Presences) <= 3 {
			// Free the resources.
			return nil, service.SignalResponse{Success: true}.String()
		}
	case service.SignalGetEndpoint:
		jsonData, err := json.Marshal(state.Server.Endpoint)
		if err != nil {
			return state, fmt.Sprintf("failed to marshal endpoint: %v", err)
		}
		return state, service.SignalResponse{Success: true, Payload: string(jsonData)}.String()

	case service.SignalGetPresences:
		// Return the presences in the match.

		jsonData, err := json.Marshal(state.Presences)
		if err != nil {
			return state, fmt.Sprintf("failed to marshal presences: %v", err)
		}
		return state, service.SignalResponse{Success: true, Payload: string(jsonData)}.String()

	case service.SignalPrepareSession:

		// if the match is already started, return an error.
		if state.Visibility() != service.UnassignedLobby {
			logger.Error("Failed to prepare session: session already prepared")
			return state, service.SignalResponse{Message: "session already prepared"}.String()
		}

		settings := service.MatchSettings{}

		if err := json.Unmarshal(signal.Payload, &settings); err != nil {
			return state, service.SignalResponse{Message: fmt.Sprintf("failed to unmarshal settings: %v", err)}.String()
		}

		for _, f := range settings.RequiredFeatures {
			if !slices.Contains(state.Server.Features, f) {
				return state, service.SignalResponse{Message: fmt.Sprintf("bad request: feature not supported: %v", f)}.String()
			}
		}

		if ok, err := service.CheckSystemGroupMembership(ctx, db, settings.SpawnedBy.String(), service.GroupGlobalDevelopers); err != nil {
			return state, service.SignalResponse{Message: fmt.Sprintf("failed to check group membership: %v", err)}.String()
		} else if !ok {

			// Validate the mode
			if levels, ok := evr.LevelsByMode[settings.Mode]; !ok {
				return state, service.SignalResponse{Message: fmt.Sprintf("bad request: invalid mode: %v", settings.Mode)}.String()

			} else {
				// Set the level to a random level if it is not set.
				if settings.Level == 0xffffffffffffffff || settings.Level == 0 {
					settings.Level = levels[rand.Intn(len(levels))]

					// Validate the level, if provided.
				} else if !slices.Contains(levels, settings.Level) {
					return state, service.SignalResponse{Message: fmt.Sprintf("bad request: invalid level `%v` for mode `%v`", settings.Level, settings.Mode)}.String()
				}
			}
		}

		state.Metadata.Mode = settings.Mode
		state.Metadata.Level = settings.Level
		state.Metadata.RequiredFeatures = settings.RequiredFeatures
		state.Metadata.GroupID = settings.GroupID

		state.CreateTime = time.Now().UTC()

		// If the start time is in the past, set it to now.
		// If the start time is not set, set it to 10 minutes from now.
		if settings.ScheduledTime.IsZero() {
			state.StartTime = time.Now().UTC().Add(10 * time.Minute)
		} else if settings.ScheduledTime.Before(time.Now()) {
			state.StartTime = time.Now().UTC()
		} else {
			state.StartTime = settings.ScheduledTime.UTC()
		}

		if !settings.SpawnedBy.IsNil() {
			state.Metadata.SpawnedBy = settings.SpawnedBy
		} else {
			state.Metadata.SpawnedBy = uuid.FromStringOrNil(signal.UserID)
		}

		// Set the lobby and team sizes
		switch settings.Mode {

		case evr.ModeSocialPublic:
			state.Metadata.Visibility = service.PublicLobby
			state.Metadata.MaxSize = service.SocialLobbyMaxSize
			state.Metadata.TeamSize = service.SocialLobbyMaxSize
			state.Metadata.PlayerLimit = service.SocialLobbyMaxSize

		case evr.ModeSocialPrivate:
			state.Metadata.Visibility = service.PrivateLobby
			state.Metadata.MaxSize = service.SocialLobbyMaxSize
			state.Metadata.TeamSize = service.SocialLobbyMaxSize
			state.Metadata.PlayerLimit = service.SocialLobbyMaxSize

		case evr.ModeArenaPublic:
			state.Metadata.Visibility = service.PublicLobby
			state.Metadata.MaxSize = service.MatchLobbyMaxSize
			state.Metadata.TeamSize = service.DefaultPublicArenaTeamSize
			state.Metadata.PlayerLimit = min(state.Metadata.TeamSize*2, state.Metadata.MaxSize)

		case evr.ModeCombatPublic:
			state.Metadata.Visibility = service.PublicLobby
			state.Metadata.MaxSize = service.MatchLobbyMaxSize
			state.Metadata.TeamSize = service.DefaultPublicCombatTeamSize
			state.Metadata.PlayerLimit = min(state.TeamSize()*2, state.Metadata.MaxSize)

		default:
			state.Metadata.Visibility = service.PrivateLobby
			state.Metadata.MaxSize = service.MatchLobbyMaxSize
			state.Metadata.TeamSize = service.MatchLobbyMaxSize
			state.Metadata.PlayerLimit = state.Metadata.MaxSize
		}

		if settings.TeamSize > 0 && settings.TeamSize <= 5 {
			state.Metadata.TeamSize = settings.TeamSize
			state.Metadata.PlayerLimit = min(state.TeamSize()*2, state.Metadata.MaxSize)
		}

		state.Alignments = make(map[uuid.UUID]Role, state.Metadata.MaxSize)

		for userID, role := range settings.TeamAlignments {
			if !userID.IsNil() {
				state.Alignments[userID] = role
			}
		}

		for _, e := range settings.Reservations {
			state.Reservations[e.SessionID] = &Reservation{
				LobbyPresence:     e,
				ReservationExpiry: settings.ReservationExpiry,
			}
		}

	case service.SignalStartSession:

		if !state.IsStarted() {
			// Set the start time to now will trigger the match to start.
			state.StartTime = time.Now().UTC()
		} else {
			return state, service.SignalResponse{Message: "failed to start session: already started"}.String()
		}

	case service.SignalLockSession:

		switch state.Mode() {
		case evr.ModeCombatPublic:
			logger.Debug("Ignoring lock signal for combat public match.")
		default:
			logger.Debug("Locking session")
			state.LockTime = time.Now().UTC()
		}

	case service.SignalUnlockSession:

		switch state.Mode() {
		case evr.ModeArenaPublic:
			logger.Debug("Ignoring unlock signal for arena public match.")
		default:
			logger.Debug("Unlocking session")
			if state.GameState != nil {
				state.LockTime = time.Time{}
			}
			state.LockTime = time.Time{}
		}

	case service.SignalEndedSession:
		// Trigger the service.MatchLeave event for the game server.
		if err := nk.StreamUserLeave(server.StreamModeMatchAuthoritative, state.ID.UUID.String(), "", state.ID.Node, state.Server.GetUserId(), state.Server.GetSessionId()); err != nil {
			logger.Warn("Failed to leave match stream", zap.Error(err))
			return nil, service.SignalResponse{Message: fmt.Sprintf("failed to leave match stream: %v", err)}.String()
		}

	case service.SignalPlayerUpdate:
		update := service.MatchPlayerUpdate{}
		if err := json.Unmarshal(signal.Payload, &update); err != nil {
			return state, service.SignalResponse{Message: fmt.Sprintf("failed to unmarshal player update: %v", err)}.String()
		}
		sessionID := uuid.FromStringOrNil(update.SessionID)
		if mp, ok := state.Presences[sessionID]; ok {
			if update.RoleAlignment != nil {
				mp.RoleAlignment = Role(*update.RoleAlignment)
			}
			if update.IsMatchmaking != nil {
				if *update.IsMatchmaking {
					t := time.Now().UTC()
					mp.MatchmakingAt = &t
				} else {
					mp.MatchmakingAt = nil
				}
			}
		}

	default:
		logger.Warn("Unknown signal: %v", signal.OpCode)
		return state, service.SignalResponse{Success: false, Message: "unknown signal"}.String()
	}

	if err := m.updateLabel(logger, dispatcher, state); err != nil {
		logger.Error("failed to update label: %v", err)
		return state, service.SignalResponse{Message: fmt.Sprintf("failed to update label: %v", err)}.String()
	}

	nk.MetricsCounterAdd("match_prepare_count", state.MetricsTags(), 1)

	return state, service.SignalResponse{Success: true, Payload: state.Label().String()}.String()

}

func (m *NEVRMatch) MatchStart(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *State) (*State, error) {
	groupID := uuid.Nil
	if state.GroupID != nil {
		groupID = state.Metadata.GroupID
	}

	state.StartTime = time.Now().UTC()
	sessionSettings := evr.NewSessionSettings(state.Server.AppID, state.Mode(), state.Level(), state.RequiredFeatures())
	envelope := &rtapi.Envelope{
		Message: &rtapi.Envelope_LobbySessionCreate{
			LobbySessionCreate: &rtapi.LobbySessionCreateMessage{
				LobbySessionId: state.ID.UUID.String(),
				LobbyType:      int32(state.Visibility()),
				GroupId:        groupID.String(),
				MaxEntrants:    int32(state.MaxSize()),
				SettingsJson:   sessionSettings.String(),
				Features:       state.RequiredFeatures(),
			},
		},
	}
	logger.WithField("message", envelope.Message).Info("Starting session.")

	message, err := evr.NewNEVRProtobufMessageV1(envelope)
	if err != nil {
		return nil, fmt.Errorf("failed to create protobuf message: %w", err)
	}

	messages := []evr.Message{
		message,
		evr.NewGameServerSessionStart(state.ID.UUID, groupID, uint8(state.MaxSize()), uint8(state.Visibility()), state.Server.AppID, state.Mode(), state.Level(), state.RequiredFeatures(), []evr.XPID{}), // Legacy service.Message for the game server.
	}

	nk.MetricsCounterAdd("match_start_count", state.MetricsTags(), 1)
	// Dispatch the message for delivery.
	if err := m.dispatchMessages(ctx, logger, dispatcher, messages, []runtime.Presence{state.Server}, nil); err != nil {
		return state, fmt.Errorf("failed to dispatch message: %w", err)
	}
	state.IsLoaded = true

	service.MatchDataEvent(ctx, nk, state.ID, service.MatchDataStarted{
		State: state.Label(),
	})
	return state, nil
}

func (m *NEVRMatch) dispatchMessages(_ context.Context, logger runtime.Logger, dispatcher runtime.MatchDispatcher, messages []evr.Message, presences []runtime.Presence, sender runtime.Presence) error {
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

func (m *NEVRMatch) updateLabel(logger runtime.Logger, dispatcher runtime.MatchDispatcher, state *State) error {
	if dispatcher != nil {
		if err := dispatcher.MatchLabelUpdate(state.Label().String()); err != nil {
			logger.WithFields(map[string]interface{}{
				"state": state,
				"error": err,
			}).Error("Failed to update label.")

			return fmt.Errorf("could not update label: %w", err)
		}
	}
	return nil
}

func (m *NEVRMatch) kickEntrants(ctx context.Context, logger runtime.Logger, dispatcher runtime.MatchDispatcher, state *State, entrantIDs ...uuid.UUID) error {
	return m.sendEntrantReject(ctx, logger, dispatcher, state.Server, evr.PlayerRejectionReasonKickedFromServer, entrantIDs...)
}

func (m *NEVRMatch) sendEntrantReject(ctx context.Context, logger runtime.Logger, dispatcher runtime.MatchDispatcher, server runtime.Presence, reason evr.PlayerRejectionReason, entrantIDs ...uuid.UUID) error {
	msg := evr.NewGameServerEntrantRejected(reason, entrantIDs...)
	if err := m.dispatchMessages(ctx, logger, dispatcher, []evr.Message{msg}, []runtime.Presence{server}, nil); err != nil {
		return fmt.Errorf("failed to dispatch message: %w", err)
	}
	return nil
}
