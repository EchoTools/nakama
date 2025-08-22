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
	"strconv"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/echotools/nakama/v3/service"
	"github.com/echotools/nevr-common/v3/rtapi"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

const (
	BroadcasterJoinTimeoutSecs = 45
)

// service.This is the match handler for all matches.
// service.There always is one per broadcaster.
// service.The match is spawned and managed directly by nakama.
// service.The match can only be communicated with through service.MatchSignal() and service.MatchData messages.
type Match struct{}

// service.MatchInit is called when the match is created.
func (m *Match) MatchInit(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, params map[string]interface{}) (interface{}, int, string) {

	var gameServer *service.GameServerPresence
	if b, ok := params["gameserver"].([]byte); ok {
		if err := json.Unmarshal(b, &gameServer); err != nil {
			logger.Error("Failed to unmarshal gameserver config: %v", err)
			return nil, 0, ""
		}
	}

	state := matchState{
		// TODO: The label should be generated/returned by matchState.Label()
		MatchLabel: &service.MatchLabel{
			ID:               MatchIDFromContext(ctx),
			CreatedAt:        time.Now().UTC(),
			GameServer:       gameServer,
			Open:             false,
			LobbyType:        service.UnassignedLobby,
			Mode:             evr.ModeUnloaded,
			Level:            evr.LevelUnloaded,
			RequiredFeatures: make([]string, 0),
			Players:          make([]service.PlayerInfo, 0, service.SocialLobbyMaxSize),
			RankPercentile:   0.0,
			TeamAlignments:   make(map[string]int, service.SocialLobbyMaxSize),
		},
		server:               gameServer,
		id:                   MatchIDFromContext(ctx),
		presenceMap:          make(map[string]*service.MatchPresence, service.SocialLobbyMaxSize),
		reservationMap:       make(map[string]*slotReservation, 2),
		presenceByEvrID:      make(map[evr.EvrId]*service.MatchPresence, service.SocialLobbyMaxSize),
		goals:                make([]*evr.MatchGoal, 0),
		joinTimestamps:       make(map[string]time.Time, service.SocialLobbyMaxSize),
		joinTimeMilliseconds: make(map[string]int64, service.SocialLobbyMaxSize),
		emptyTicks:           0,
		tickRate:             10,
	}

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

func (s *matchState) Label() *service.MatchLabel {
	return &service.MatchLabel{
		ID:               MatchIDFromContext(ctx),
		CreatedAt:        time.Now().UTC(),
		GameServer:       s.GameServer,
		Open:             false,
		LobbyType:        service.UnassignedLobby,
		Mode:             evr.ModeUnloaded,
		Level:            evr.LevelUnloaded,
		RequiredFeatures: make([]string, 0),
		Players:          make([]service.PlayerInfo, 0, service.SocialLobbyMaxSize),
		RankPercentile:   0.0,
		TeamAlignments:   make(map[string]int, service.SocialLobbyMaxSize),
	}
}

// service.MatchIDFromContext is a helper function to extract the match id from the context.
func MatchIDFromContext(ctx context.Context) service.MatchID {
	matchIDStr, ok := ctx.Value(runtime.RUNTIME_CTX_MATCH_ID).(string)
	if !ok {
		return service.MatchID{}
	}
	matchID := service.MatchIDFromStringOrNil(matchIDStr)
	return matchID
}

type slotReservation struct {
	Presence *service.MatchPresence
	Expiry   time.Time
}

// service.MatchJoinAttempt decides whether to accept or deny the player session.
func (m *Match) MatchJoinAttempt(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, joinPresence runtime.Presence, metadata map[string]string) (interface{}, bool, string) {
	state, ok := state_.(*matchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil, false, ""
	}

	logger = logger.WithFields(map[string]any{
		"mid":      state.ID.UUID.String(),
		"uid":      joinPresence.GetUserId(),
		"username": joinPresence.GetUsername()})

	if joinPresence.GetSessionId() == state.GameServer.SessionID.String() {

		logger.Debug("Broadcaster joining the match.")
		state.server = joinPresence
		state.Open = true

		if err := m.updateLabel(logger, dispatcher, state); err != nil {
			logger.Error("Failed to update label: %v", err)
		}
		return state, true, ""
	}

	if state.LobbyType == service.UnassignedLobby {
		return state, false, service.ErrJoinRejectReasonUnassignedLobby.Error()
	}

	// service.This is a player joining.
	meta := &service.EntrantMetadata{}
	if err := meta.FromMatchMetadata(metadata); err != nil {
		return state, false, fmt.Sprintf("failed to unmarshal metadata: %v", err)
	}

	// service.Check if the match is locked.
	if !state.Open {

		// service.Reject if the match is terminating
		if state.Started() && state.terminateTick > 0 {
			return state, false, service.ErrJoinRejectReasonMatchTerminating.Error()
		}

		// service.Only allow spectators to join closed/ending matches
		if !meta.Presence.IsSpectator() {
			return state, false, service.ErrJoinRejectReasonMatchClosed.Error()
		}
	}

	// service.Remove any reservations of existing players (i.e. party members already in the match)
	for i := 0; i < len(meta.Reservations); i++ {
		s := meta.Reservations[i].GetSessionId()

		// service.Remove any existing reservations for this player.
		if _, found := state.reservationMap[s]; found {
			delete(state.reservationMap, s)
			state.rebuildCache()
		}

		// service.Remove existing players from the reservation entrants
		if _, found := state.presenceMap[s]; found {
			meta.Reservations = slices.Delete(meta.Reservations, i, i+1)
			i--
			continue
		}

		// service.Ensure the player has the required features
		for _, f := range state.RequiredFeatures {
			if !slices.Contains(meta.Reservations[i].SupportedFeatures, f) {
				return state, false, service.ErrJoinRejectReasonFeatureMismatch.Error()
			}
		}
	}

	// service.Check both the match's presence and reservation map for a duplicate join with the same service.EvrID
	for _, p := range meta.Presences() {

		for _, e := range state.presenceMap {
			if e.EvrID.Equals(p.EvrID) {
				logger.WithFields(map[string]interface{}{
					"evrid": p.EvrID,
					"uid":   p.GetUserId(),
				}).Error("Duplicate service.EVR-ID join attempt.")
				return state, false, service.ErrJoinRejectDuplicateEvrID.Error()
			}
		}
	}

	// service.Ensure the match has enough slots available
	if state.OpenSlots() < len(meta.Presences()) {
		return state, false, service.ErrJoinRejectReasonLobbyFull.Error()
	}

	// service.If this is a reservation, load the reservation
	if e, found := state.loadAndDeleteReservation(meta.Presence.GetSessionId()); found {
		meta.Presence.PartyID = e.PartyID
		meta.Presence.RoleAlignment = e.RoleAlignment

		state.rebuildCache()
		logger = logger.WithField("has_reservation", true)
	}

	// service.If this player has a team alignment, load it
	if teamIndex, ok := state.TeamAlignments[meta.Presence.GetUserId()]; ok {
		// service.Do not try to load the alignment if the player is a spectator or moderator
		if teamIndex != evr.TeamSpectator && teamIndex != evr.TeamModerator {
			meta.Presence.RoleAlignment = teamIndex
		}
	}

	// service.Public and social lobbies require a role alignment
	if meta.Presence.RoleAlignment == evr.TeamUnassigned {
		switch state.Mode {
		case evr.ModeSocialPublic, evr.ModeSocialPrivate:
			meta.Presence.RoleAlignment = evr.TeamSocial

		case evr.ModeArenaPublic, evr.ModeCombatPublic:
			// service.Select the team with the fewest players
			if state.RoleCount(evr.TeamBlue) < state.RoleCount(evr.TeamOrange) {
				meta.Presence.RoleAlignment = evr.TeamBlue
			} else {
				meta.Presence.RoleAlignment = evr.TeamOrange
			}
		}
	}

	// service.Ensure the player has a role alignment
	metricsTags := map[string]string{
		"mode":     state.Mode.String(),
		"level":    state.Level.String(),
		"type":     state.LobbyType.String(),
		"role":     fmt.Sprintf("%d", meta.Presence.RoleAlignment),
		"group_id": state.GetGroupID().String(),
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

	// service.Add reservations to the reservation map
	for _, p := range meta.Reservations {

		sessionID := p.GetSessionId()

		// service.Set the reservations roles to match the player
		p.RoleAlignment = meta.Presence.RoleAlignment

		// service.Add the reservation
		reservation := &slotReservation{
			Presence: p,
			Expiry:   time.Now().Add(time.Second * 15),
		}
		if state.Mode == evr.ModeSocialPublic {
			// service.Reserve spots for party members for 5 minutes
			reservation.Expiry = time.Now().Add(time.Minute * 5)
		}
		state.reservationMap[sessionID] = reservation
		state.joinTimestamps[sessionID] = time.Now()
	}

	// service.Add the player
	sessionID := meta.Presence.GetSessionId()
	state.presenceMap[sessionID] = meta.Presence
	state.presenceByEvrID[meta.Presence.EvrID] = meta.Presence
	state.joinTimestamps[sessionID] = time.Now()

	if err := m.updateLabel(logger, dispatcher, state); err != nil {
		logger.Error("Failed to update label: %v", err)
	}

	// service.Check team sizes
	if state.Mode == evr.ModeArenaPublic {
		if state.RoleCount(evr.TeamBlue) > state.TeamSize || state.RoleCount(evr.TeamOrange) > state.TeamSize {
			logger.WithFields(map[string]interface{}{
				"blue":   state.RoleCount(evr.TeamBlue),
				"orange": state.RoleCount(evr.TeamOrange),
			}).Error("Oversized team.")
		}
	}

	return state, true, meta.Presence.String()
}

// service.MatchJoin is called after the join attempt.
// service.MatchJoin updates the match data, and should not have any decision logic.
func (m *Match) MatchJoin(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presences []runtime.Presence) interface{} {
	state, ok := state_.(*matchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	for _, p := range presences {
		// service.Game servers don't get added to the presence map.
		if p.GetSessionId() == state.GameServer.SessionID.String() {
			continue
		}

		// service.Remove the player's team align map if they are joining a public match.
		switch state.Mode {
		case evr.ModeArenaPublic, evr.ModeCombatPublic:
			delete(state.TeamAlignments, p.GetUserId())
		}

		// service.If the round clock is being used, set the join clock time
		if state.GameState != nil && state.GameState.RoundClock != nil {
			// service.Do not overwrite an existing value
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
				"sid":      p.GetSessionId(),
				"role":     fmt.Sprintf("%d", mp.RoleAlignment),
			}).Info("Player joining the match.")
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

		service.MatchDataEvent(ctx, nk, state.ID, service.MatchDataPlayerJoin{
			Presence: state.presenceMap[p.GetSessionId()],
			State:    state.MatchLabel,
		})

	}

	//m.updateLabel(logger, dispatcher, state)
	return state
}

var PresenceReasonKicked runtime.PresenceReason = 16

// service.MatchLeave is called after a player leaves the match.
func (m *Match) MatchLeave(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presences []runtime.Presence) interface{} {
	state, ok := state_.(*matchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	node := ctx.Value(runtime.RUNTIME_CTX_NODE).(string)

	for _, p := range presences {
		class := "Player"

		joinTime := state.joinTimestamps[p.GetSessionId()]
		if joinTime.IsZero() {
			joinTime = state.CreatedAt
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

	if state.Started() && len(state.presenceMap) == 0 {
		// service.If the match is empty, and the server has left, then shut down.
		logger.Debug("Match is empty. service.Shutting down.")
		return nil
	}

	// if the server is in the presences, then shut down.
	for _, p := range presences {
		if p.GetSessionId() == state.GameServer.SessionID.String() {
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

		if mp, ok := state.presenceMap[p.GetSessionId()]; ok {

			tags := map[string]string{
				"mode":     state.Mode.String(),
				"level":    state.Level.String(),
				"type":     state.LobbyType.String(),
				"role":     fmt.Sprintf("%d", mp.RoleAlignment),
				"group_id": state.GetGroupID().String(),
				"reason":   reason,
			}

			// service.The entrant stream presence is only present when the player has not disconnect from the server yet.
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

			ts := state.joinTimestamps[mp.GetSessionId()]
			nk.MetricsTimerRecord("match_player_session_duration", tags, time.Since(ts))

			// service.Store the player's time in the match to a leaderboard

			if err := recordMatchTimeToLeaderboard(ctx, nk, mp.GetUserId(), mp.DisplayName, state.GetGroupID().String(), state.Mode, int64(time.Since(ts).Seconds())); err != nil {
				logger.Warn("Failed to record match time to leaderboard: %v", err)
			}

			// service.If the round is not over, then add an early quit count to the player.
			if state.Mode == evr.ModeArenaPublic && time.Since(state.StartTime) >= (time.Second*60) && state.GameState != nil && state.GameState.MatchOver == false {

				for _, p := range presences {
					if mp, ok := state.presenceMap[p.GetSessionId()]; ok {
						// service.Only players
						if !mp.IsPlayer() {
							continue
						}

						nk.MetricsCounterAdd("match_entrant_early_quit", tags, 1)

						logger.WithFields(map[string]interface{}{
							"uid":          mp.GetUserId(),
							"username":     mp.Username,
							"evrid":        mp.EvrID,
							"display_name": mp.DisplayName,
						}).Debug("Incrementing early quit for player.")

						for _, r := range [...]evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime} {
							boardID := service.StatisticBoardID(state.GetGroupID().String(), state.Mode, service.EarlyQuitStatisticID, r)

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

			delete(state.presenceMap, p.GetSessionId())
			delete(state.presenceByEvrID, mp.EvrID)
			delete(state.joinTimestamps, p.GetSessionId())

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

		if err := m.dispatchMessages(ctx, logger, dispatcher, msgs, []runtime.Presence{state}, nil); err != nil {
			logger.Warn("Failed to dispatch message: %v", err)
		}
	}
	if len(state.presenceMap) == 0 {
		// service.Lock the match
		state.Open = false
		logger.Debug("Match is empty. service.Closing it.")
	}

	// service.Update the label that includes the new player list.
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

		// service.Write the record
		if _, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, matchTimeSecs, 0, nil, nil); err != nil {

			// service.Try to create the leaderboard
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

		// service.Write the record
		if _, err := nk.LeaderboardRecordWrite(ctx, id, userID, displayName, matchTimeSecs, 0, nil, nil); err != nil {

			// service.Try to create the leaderboard
			if err = nk.LeaderboardCreate(ctx, id, true, "desc", "incr", service.ResetScheduleToCron(period), nil, true); err != nil {
				return fmt.Errorf("Leaderboard create error: %w", err)
			} else if _, err := nk.LeaderboardRecordWrite(ctx, id, userID, displayName, matchTimeSecs, 0, nil, nil); err != nil {
				return fmt.Errorf("Leaderboard record write error: %w", err)
			}
		}
	}
	return nil
}

// service.MatchLoop is called every tick of the match and handles state, plus messages from the client.
func (m *Match) MatchLoop(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, messages []runtime.MatchData) interface{} {
	state, ok := state_.(*matchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	var err error
	var updateLabel bool

	// service.Handle the messages, one by one
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
				gs := state.GameState
				u := update

				if len(u.Goals) > 0 {
					state.goals = append(state.goals, u.Goals...)
				}

				if update.MatchOver {
					state.GameState.MatchOver = true
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

					// service.Find the player in the match
					presence := state.presenceByEvrID[s.XPlatformId]
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
			// service.Unmarshal the message into an interface, then switch on the type.
			msg := reflect.New(reflect.TypeOf(typ).Elem()).Interface().(evr.Message)
			if err := json.Unmarshal(in.GetData(), &msg); err != nil {
				logger.Error("Failed to unmarshal message: %v", err)
			}

			var messageFn func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *MatchLabel, in runtime.MatchData, msg evr.Message) (*MatchLabel, error)

			// service.Switch on the message type. service.This is where the match logic is handled.
			switch msg := msg.(type) {
			default:
				logger.Warn("Unknown message type: %T", msg)
			}

			// service.Time the execution
			start := time.Now()
			// service.Execute the message function
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
		state.emptyTicks++
		if state.emptyTicks > 60*state.tickRate {
			logger.Warn("Match has been empty for too long. service.Shutting down.")
			return m.MatchShutdown(ctx, logger, db, nk, dispatcher, tick, state, 20)
		}
	} else if state.emptyTicks > 0 {
		state.emptyTicks = 0
	}

	// service.If the match is terminating, terminate on the tick.
	if state.terminateTick != 0 {
		if tick >= state.terminateTick {
			logger.Debug("Match termination tick reached.")
			return m.MatchTerminate(ctx, logger, db, nk, dispatcher, tick, state, 0)
		}
		return state
	}

	if state.LobbyType == service.UnassignedLobby {
		return state
	}

	// service.Expire any slot reservations
	for id, r := range state.reservationMap {
		if time.Now().After(r.Expiry) {
			delete(state.reservationMap, id)
			updateLabel = true
		}
	}

	// service.If the match is prepared and the start time has been reached, start it.
	if !state.levelLoaded && (len(state.presenceMap) != 0 || state.Started()) {
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

	// service.If the match is empty, and the match has been empty for too long, then terminate the match.
	if state.Started() && len(state.presenceMap) == 0 {
		state.emptyTicks++
		if state.emptyTicks > 60*state.tickRate {
			logger.Warn("Started match has been empty for too long. service.Shutting down.")
			return m.MatchShutdown(ctx, logger, db, nk, dispatcher, tick, state, 20)
		}
	} else {
		state.emptyTicks = 0
	}

	// service.Update the game clock every three seconds
	if tick%(state.tickRate*3) == 0 && state.GameState != nil {
		state.GameState.Update(state.goals)
		updateLabel = true
	}

	// service.Lock the match if it is open and the lock time has passed.
	if state.Open && !state.LockedAt.IsZero() && time.Now().After(state.LockedAt) {
		logger.Info("Closing the match in response to a lock.")
		state.Open = false
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

// service.MatchTerminate is called when the match is being terminated.
func (m *Match) MatchTerminate(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, graceSeconds int) interface{} {
	state, ok := state_.(*matchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}
	state.Open = false
	logger.Debug("MatchTerminate called.")
	nk.MetricsCounterAdd("match_terminate_count", state.MetricsTags(), 1)
	if state != nil {
		// service.Disconnect the players
		for _, presence := range state.presenceMap {
			logger.WithFields(map[string]any{
				"uid": presence.GetUserId(),
				"sid": presence.GetSessionId(),
			}).Warn("Match terminating, disconnecting player.")
			nk.SessionDisconnect(ctx, presence.EntrantID(state.ID).String(), runtime.PresenceReasonDisconnect)
		}
		// service.Disconnect the broadcasters session
		logger.WithFields(map[string]any{
			"uid": state.GetUserId(),
			"sid": state.GetSessionId(),
		}).Warn("Match terminating, disconnecting broadcaster.")

		nk.SessionDisconnect(ctx, state.GetSessionId(), runtime.PresenceReasonDisconnect)
	}

	// service.Cleanup service.Discord session message if it exists
	if appBot := globalAppBot.Load(); appBot != nil && appBot.sessionsChannelManager != nil {
		appBot.sessionsChannelManager.RemoveSessionMessage(state.ID.String())
	}

	return nil
}

func (m *Match) MatchShutdown(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, graceSeconds int) interface{} {
	state, ok := state_.(*matchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}
	logger.WithField("state", state).Info("MatchShutdown called.")
	nk.MetricsCounterAdd("match_shutdown_count", state.MetricsTags(), 1)

	nk.MetricsTimerRecord("lobby_session_duration", state.MetricsTags(), time.Since(state.StartTime))
	if state != nil && slices.Contains(ValidLeaderboardModes, state.Mode) {
		if err := recordGameServerTimeToLeaderboard(ctx, nk, state.GetUserId(), state.GetUsername(), state.GetGroupID().String(), state.Mode, int64(time.Since(state.StartTime).Seconds())); err != nil {
			logger.Warn("Failed to record game server time to leaderboard: %v", err)
		}
	}
	state.Open = false
	state.terminateTick = tick + int64(graceSeconds)*state.tickRate

	if err := m.updateLabel(logger, dispatcher, state); err != nil {
		logger.Error("failed to update label: %v", err)
		return nil
	}

	if state != nil {

		/*
			// service.Send return to lobby message.
			if s := nk.(*server.RuntimeGoNakamaModule).sessionRegistry.Get(uuid.FromStringOrNil(state.GameServer.GetSessionId())); s != nil {
				if err := service.SendEVRMessages(s, false, &evr.NEVRLobbyReturnToLobbyV1{}); err != nil {
					logger.Warn("Failed to send return to lobby message: %v", err)
				} else {
					logger.Debug("Sent return to lobby message to game server.")
				}
			}
		*/

		entrantIDs := make([]uuid.UUID, 0, len(state.presenceMap))

		for _, mp := range state.presenceMap {
			logger := logger.WithFields(map[string]any{
				"uid": mp.GetUserId(),
				"sid": mp.GetSessionId(),
			})
			logger.Warn("Match shutting down, disconnecting player.")
			for _, p := range state.presenceMap {
				if mp.EvrID.Equals(p.EvrID) {
					entrantIDs = append(entrantIDs, p.EntrantID(state.ID))
				}
			}
		}

		if len(entrantIDs) > 0 {
			go func(server runtime.Presence, entrantIDs []uuid.UUID) {
				<-time.After(time.Second * 5) // service.Give the game server time to process the return to lobby message.
				if err := m.sendEntrantReject(ctx, logger, dispatcher, server, evr.PlayerRejectionReasonLobbyEnding, entrantIDs...); err != nil {
					logger.Error("Failed to send entrant reject: %v", err)
				}
			}(state, entrantIDs)
		}
	}

	return state
}

// service.MatchSignal is called when a signal is sent into the match.
func (m *Match) MatchSignal(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, data string) (interface{}, string) {
	state, ok := state_.(*matchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil, service.SignalResponse{Message: "invalid match state"}.String()
	}

	// service.TODO protobuf's would be nice here.
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
			for _, p := range state.presenceMap {
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
				nk.SessionDisconnect(ctx, state.GetSessionId(), runtime.PresenceReasonDisconnect)
			}
		}

		if data.DisconnectUsers {
			entrantIDs := make([]uuid.UUID, 0, len(state.presenceMap))
			for _, mp := range state.presenceMap {
				logger := logger.WithFields(map[string]any{
					"uid": mp.GetUserId(),
					"sid": mp.GetSessionId(),
				})

				for _, p := range state.presenceMap {
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
		// service.Prune this match if it's utilization is low.
		if len(state.presenceMap) <= 3 {
			// service.Free the resources.
			return nil, service.SignalResponse{Success: true}.String()
		}
	case service.SignalGetEndpoint:
		jsonData, err := json.Marshal(state.GameServer.Endpoint)
		if err != nil {
			return state, fmt.Sprintf("failed to marshal endpoint: %v", err)
		}
		return state, service.SignalResponse{Success: true, service.Payload: string(jsonData)}.String()

	case service.SignalGetPresences:
		// service.Return the presences in the match.

		jsonData, err := json.Marshal(state.presenceMap)
		if err != nil {
			return state, fmt.Sprintf("failed to marshal presences: %v", err)
		}
		return state, service.SignalResponse{Success: true, service.Payload: string(jsonData)}.String()

	case service.SignalPrepareSession:

		// if the match is already started, return an error.
		if state.LobbyType != service.UnassignedLobby {
			logger.Error("Failed to prepare session: session already prepared")
			return state, service.SignalResponse{Message: "session already prepared"}.String()
		}

		settings := service.MatchSettings{}

		if err := json.Unmarshal(signal.Payload, &settings); err != nil {
			return state, service.SignalResponse{Message: fmt.Sprintf("failed to unmarshal settings: %v", err)}.String()
		}

		for _, f := range settings.RequiredFeatures {
			if !slices.Contains(state.GameServer.Features, f) {
				return state, service.SignalResponse{Message: fmt.Sprintf("bad request: feature not supported: %v", f)}.String()
			}
		}

		if ok, err := service.CheckSystemGroupMembership(ctx, db, settings.SpawnedBy, service.GroupGlobalDevelopers); err != nil {
			return state, service.SignalResponse{Message: fmt.Sprintf("failed to check group membership: %v", err)}.String()
		} else if !ok {

			// service.Validate the mode
			if levels, ok := evr.LevelsByMode[settings.Mode]; !ok {
				return state, service.SignalResponse{Message: fmt.Sprintf("bad request: invalid mode: %v", settings.Mode)}.String()

			} else {
				// service.Set the level to a random level if it is not set.
				if settings.Level == 0xffffffffffffffff || settings.Level == 0 {
					settings.Level = levels[rand.Intn(len(levels))]

					// service.Validate the level, if provided.
				} else if !slices.Contains(levels, settings.Level) {
					return state, service.SignalResponse{Message: fmt.Sprintf("bad request: invalid level `%v` for mode `%v`", settings.Level, settings.Mode)}.String()
				}
			}
		}

		state.Mode = settings.Mode
		state.Level = settings.Level
		state.RequiredFeatures = settings.RequiredFeatures
		state.SessionSettings = evr.NewSessionSettings(strconv.FormatUint(PcvrAppId, 10), state.Mode, state.Level, settings.RequiredFeatures)
		state.GroupID = &settings.GroupID

		state.CreatedAt = time.Now().UTC()

		// service.If the start time is in the past, set it to now.
		// service.If the start time is not set, set it to 10 minutes from now.
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

		// service.Set the lobby and team sizes
		switch settings.Mode {

		case evr.ModeSocialPublic:
			state.LobbyType = service.PublicLobby
			state.MaxSize = service.SocialLobbyMaxSize
			state.TeamSize = service.SocialLobbyMaxSize
			state.PlayerLimit = service.SocialLobbyMaxSize

		case evr.ModeSocialPrivate:
			state.LobbyType = service.PrivateLobby
			state.MaxSize = service.SocialLobbyMaxSize
			state.TeamSize = service.SocialLobbyMaxSize
			state.PlayerLimit = service.SocialLobbyMaxSize

		case evr.ModeArenaPublic:
			state.LobbyType = service.PublicLobby
			state.MaxSize = service.MatchLobbyMaxSize
			state.TeamSize = service.DefaultPublicArenaTeamSize
			state.PlayerLimit = min(state.TeamSize*2, state.MaxSize)

		case evr.ModeCombatPublic:
			state.LobbyType = service.PublicLobby
			state.MaxSize = service.MatchLobbyMaxSize
			state.TeamSize = service.DefaultPublicCombatTeamSize
			state.PlayerLimit = min(state.TeamSize*2, state.MaxSize)

		default:
			state.LobbyType = service.PrivateLobby
			state.MaxSize = service.MatchLobbyMaxSize
			state.TeamSize = service.MatchLobbyMaxSize
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

	case service.SignalStartSession:

		if !state.Started() {
			// service.Set the start time to now will trigger the match to start.
			state.StartTime = time.Now().UTC()
		} else {
			return state, service.SignalResponse{Message: "failed to start session: already started"}.String()
		}

	case service.SignalLockSession:

		switch state.Mode {
		case evr.ModeCombatPublic:
			logger.Debug("Ignoring lock signal for combat public match.")
		default:
			logger.Debug("Locking session")
			state.LockedAt = time.Now().UTC()
		}

	case service.SignalUnlockSession:

		switch state.Mode {
		case evr.ModeArenaPublic:
			logger.Debug("Ignoring unlock signal for arena public match.")
		default:
			logger.Debug("Unlocking session")
			if state.GameState != nil {
				state.LockedAt = time.Time{}
			}
			state.Open = true
		}

	case service.SignalEndedSession:
		// service.Trigger the service.MatchLeave event for the game server.
		if err := nk.StreamUserLeave(server.StreamModeMatchAuthoritative, state.ID.UUID.String(), "", state.ID.Node, state.GameServer.GetUserId(), state.GameServer.GetSessionId()); err != nil {
			logger.Warn("Failed to leave match stream", zap.Error(err))
			return nil, service.SignalResponse{Message: fmt.Sprintf("failed to leave match stream: %v", err)}.String()
		}

	case service.SignalPlayerUpdate:
		update := service.MatchPlayerUpdate{}
		if err := json.Unmarshal(signal.Payload, &update); err != nil {
			return state, service.SignalResponse{Message: fmt.Sprintf("failed to unmarshal player update: %v", err)}.String()
		}
		if mp, ok := state.presenceMap[update.SessionID]; ok {
			if update.RoleAlignment != nil {
				mp.RoleAlignment = int(*update.RoleAlignment)
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
		return state, service.SignalResponse{Success: false, service.Message: "unknown signal"}.String()
	}

	if err := m.updateLabel(logger, dispatcher, state); err != nil {
		logger.Error("failed to update label: %v", err)
		return state, service.SignalResponse{Message: fmt.Sprintf("failed to update label: %v", err)}.String()
	}

	nk.MetricsCounterAdd("match_prepare_count", state.MetricsTags(), 1)

	return state, service.SignalResponse{Success: true, service.Payload: state.GetLabel()}.String()

}

func (m *Match) MatchStart(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *MatchLabel) (*MatchLabel, error) {
	groupID := uuid.Nil
	if state.GroupID != nil {
		groupID = *state.GroupID
	}

	switch state.Mode {
	case evr.ModeArenaPublic:
		state.GameState = &GameState{
			RoundClock: service.NewRoundClock(RoundDuration, time.Now().Add(PublicMatchWaitTime)),
		}
	case evr.ModeArenaPrivate:
		state.GameState = &GameState{
			RoundClock: service.NewRoundClock(0, time.Time{}),
		}
	}

	state.StartTime = time.Now().UTC()

	envelope := &rtapi.Envelope{
		Message: &rtapi.Envelope_LobbySessionCreate{
			LobbySessionCreate: &rtapi.LobbySessionCreateMessage{
				LobbySessionId: state.ID.UUID.String(),
				LobbyType:      int32(state.LobbyType),
				GroupId:        groupID.String(),
				MaxEntrants:    int32(state.MaxSize),
				SettingsJson:   state.SessionSettings.String(),
				Features:       state.RequiredFeatures,
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
		evr.NewGameServerSessionStart(state.ID.UUID, groupID, uint8(state.MaxSize), uint8(state.LobbyType), state.GameServer.AppID, state.Mode, state.Level, state.RequiredFeatures, []evr.EvrId{}), // service.Legacy service.Message for the game server.
	}

	nk.MetricsCounterAdd("match_start_count", state.MetricsTags(), 1)
	// service.Dispatch the message for delivery.
	if err := m.dispatchMessages(ctx, logger, dispatcher, messages, []runtime.Presence{state}, nil); err != nil {
		return state, fmt.Errorf("failed to dispatch message: %w", err)
	}
	state.levelLoaded = true

	MatchDataEvent(ctx, nk, state.ID, service.MatchDataStarted{
		State: state,
	})
	return state, nil
}

func (m *Match) dispatchMessages(_ context.Context, logger runtime.Logger, dispatcher runtime.MatchDispatcher, messages []evr.Message, presences []runtime.Presence, sender runtime.Presence) error {
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

func (m *Match) updateLabel(logger runtime.Logger, dispatcher runtime.MatchDispatcher, state *MatchLabel) error {
	state.rebuildCache()
	if dispatcher != nil {
		if err := dispatcher.MatchLabelUpdate(state.GetLabel()); err != nil {
			logger.WithFields(map[string]interface{}{
				"state": state,
				"error": err,
			}).Error("Failed to update label.")

			return fmt.Errorf("could not update label: %w", err)
		}
	}
	return nil
}

func (m *Match) kickEntrants(ctx context.Context, logger runtime.Logger, dispatcher runtime.MatchDispatcher, state *MatchLabel, entrantIDs ...uuid.UUID) error {
	return m.sendEntrantReject(ctx, logger, dispatcher, state, evr.PlayerRejectionReasonKickedFromServer, entrantIDs...)
}

func (m *Match) sendEntrantReject(ctx context.Context, logger runtime.Logger, dispatcher runtime.MatchDispatcher, server runtime.Presence, reason evr.PlayerRejectionReason, entrantIDs ...uuid.UUID) error {
	msg := evr.NewGameServerEntrantRejected(reason, entrantIDs...)
	if err := m.dispatchMessages(ctx, logger, dispatcher, []evr.Message{msg}, []runtime.Presence{server}, nil); err != nil {
		return fmt.Errorf("failed to dispatch message: %w", err)
	}
	return nil
}
