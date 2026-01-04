package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"strconv"
	"time"

	"github.com/echotools/nevr-common/v4/gen/go/rtapi"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
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
	OpCodeGameServerLobbyStatus
)

type MatchStatGroup string
type MatchLevelSelection string

const (
	EVRLobbySessionMatchModule = "evrmatch"
	EvrBackfillModule          = "evrbackfill"
)

type EvrMatchMeta struct {
	GameServerPresence
	Players []EvrMatchPresence `json:"players,omitempty"` // The displayNames of the players (by team name) in the match.
	// Stats
}

type MatchGameMode struct {
	Mode       evr.Symbol `json:"mode"`
	Visibility LobbyType  `json:"visibility"`
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

	gameserverConfig := GameServerPresence{}
	if err := json.Unmarshal([]byte(params["gameserver"].(string)), &gameserverConfig); err != nil {
		logger.Error("Failed to unmarshal gameserver config: %v", err)
		return nil, 0, ""
	}

	state := MatchLabel{
		CreatedAt: time.Now().UTC(),

		GameServer:       &gameserverConfig,
		Open:             false,
		LobbyType:        UnassignedLobby,
		Mode:             evr.ModeUnloaded,
		Level:            evr.LevelUnloaded,
		RequiredFeatures: make([]string, 0),
		Players:          make([]PlayerInfo, 0, SocialLobbyMaxSize),
		presenceMap:      make(map[string]*EvrMatchPresence, SocialLobbyMaxSize),
		reservationMap:   make(map[string]*slotReservation, 2),
		presenceByEvrID:  make(map[evr.EvrId]*EvrMatchPresence, SocialLobbyMaxSize),
		goals:            make([]*evr.MatchGoal, 0),

		TeamAlignments:       make(map[string]int, SocialLobbyMaxSize),
		joinTimestamps:       make(map[string]time.Time, SocialLobbyMaxSize),
		joinTimeMilliseconds: make(map[string]int64, SocialLobbyMaxSize),
		participations:       make(map[string]*PlayerParticipation, SocialLobbyMaxSize),
		emptyTicks:           0,
		tickRate:             10,
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
	ErrJoinInvalidRoleForLevel                   = errors.New("invalid role for level")
	ErrJoinRejectReasonPartyMembersMustHaveRoles = errors.New("party members must have roles")
	ErrJoinRejectReasonMatchTerminating          = errors.New("match terminating")
	ErrJoinRejectReasonMatchClosed               = errors.New("match closed to new entrants")
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

	if joinPresence.GetSessionId() == state.GameServer.SessionID.String() {

		logger.Debug("Broadcaster joining the match.")
		state.server = joinPresence
		state.Open = true

		if err := m.updateLabel(logger, dispatcher, state); err != nil {
			logger.Error("Failed to update label: %v", err)
		}
		return state, true, ""
	}

	if state.LobbyType == UnassignedLobby {
		return state, false, ErrJoinRejectReasonUnassignedLobby.Error()
	}

	// This is a player joining.
	meta := &EntrantMetadata{}
	if err := meta.FromMatchMetadata(metadata); err != nil {
		return state, false, fmt.Sprintf("failed to unmarshal metadata: %v", err)
	}

	// Check if the match is locked.
	if !state.Open {

		// Reject if the match is terminating
		if state.Started() && state.terminateTick > 0 {
			return state, false, ErrJoinRejectReasonMatchTerminating.Error()
		}

		// Only allow spectators to join closed/ending matches
		if !meta.Presence.IsSpectator() {
			return state, false, ErrJoinRejectReasonMatchClosed.Error()
		}
	}

	// Check if the main presence is already in the match (idempotent join or reconnection)
	if existing, found := state.presenceMap[meta.Presence.GetSessionId()]; found {
		// If the session is already in the match, treat this as a successful idempotent join
		logger.WithFields(map[string]interface{}{
			"evrid": existing.EvrID,
			"uid":   existing.GetUserId(),
			"sid":   existing.GetSessionId(),
		}).Debug("Session already in match, allowing idempotent join.")
		return state, true, ""
	}

	// Remove any reservations of existing players (i.e. party members already in the match)
	for i := 0; i < len(meta.Reservations); i++ {
		s := meta.Reservations[i].GetSessionId()

		// Remove any existing reservations for this player.
		if _, found := state.reservationMap[s]; found {
			delete(state.reservationMap, s)
			state.rebuildCache()
		}

		// Remove existing players from the reservation entrants
		if _, found := state.presenceMap[s]; found {
			meta.Reservations = slices.Delete(meta.Reservations, i, i+1)
			i--
			continue
		}

		// Ensure the player has the required features
		for _, f := range state.RequiredFeatures {
			if !slices.Contains(meta.Reservations[i].SupportedFeatures, f) {
				return state, false, ErrJoinRejectReasonFeatureMismatch.Error()
			}
		}
	}

	// Check both the match's presence and reservation map for a duplicate join with the same EvrID
	for _, p := range meta.Presences() {

		for _, e := range state.presenceMap {
			// Reject any duplicate EVR-ID join attempt (regardless of session)
			if e.EvrID.Equals(p.EvrID) {
				logger.WithFields(map[string]interface{}{
					"evrid":            p.EvrID,
					"uid":              p.GetUserId(),
					"existing_session": e.GetSessionId(),
					"new_session":      p.GetSessionId(),
				}).Error("Duplicate EVR-ID join attempt.")
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
		meta.Presence.PartyID = e.PartyID
		meta.Presence.RoleAlignment = e.RoleAlignment

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

	// Ensure the player has a role alignment
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
		reservation := &slotReservation{
			Presence: p,
			Expiry:   time.Now().Add(time.Second * 15),
		}
		if state.Mode == evr.ModeSocialPublic {
			// Reserve spots for party members for 5 minutes
			reservation.Expiry = time.Now().Add(time.Minute * 5)
		}
		state.reservationMap[sessionID] = reservation
		state.joinTimestamps[sessionID] = time.Now()
	}

	// Add the player
	sessionID := meta.Presence.GetSessionId()
	state.presenceMap[sessionID] = meta.Presence
	state.presenceByEvrID[meta.Presence.EvrID] = meta.Presence
	state.joinTimestamps[sessionID] = time.Now()

	if err := m.updateLabel(logger, dispatcher, state); err != nil {
		logger.Error("Failed to update label: %v", err)
	}

	// Check team sizes
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

// MatchJoin is called after the join attempt.
// MatchJoin updates the match data, and should not have any decision logic.
func (m *EvrMatch) MatchJoin(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presences []runtime.Presence) interface{} {
	state, ok := state_.(*MatchLabel)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	for _, p := range presences {
		// Game servers don't get added to the presence map.
		if p.GetSessionId() == state.GameServer.SessionID.String() {
			continue
		}

		// Remove the player's team align map if they are joining a public match.
		switch state.Mode {
		case evr.ModeArenaPublic, evr.ModeCombatPublic:
			delete(state.TeamAlignments, p.GetUserId())
		}

		// If the session scoreboard is being used, set the join clock time
		if state.GameState != nil && state.GameState.SessionScoreboard != nil {
			// Do not overwrite an existing value
			if _, ok := state.joinTimeMilliseconds[p.GetSessionId()]; !ok {
				state.joinTimeMilliseconds[p.GetSessionId()] = state.GameState.SessionScoreboard.Elapsed().Milliseconds()
			}
		}
		isBackfill := time.Now().After(state.StartTime.Add(PublicMatchWaitTime))

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
				"backfill": strconv.FormatBool(isBackfill),
			}
			nk.MetricsCounterAdd("match_entrant_join_count", tags, 1)
			nk.MetricsTimerRecord("match_player_join_duration", tags, time.Since(state.joinTimestamps[p.GetSessionId()]))
		}

		MatchDataEvent(ctx, nk, state.ID, MatchDataPlayerJoin{
			Presence: state.presenceMap[p.GetSessionId()],
			State:    state,
		})

		if participation, ok := state.participations[p.GetUserId()]; ok && participation != nil {
			// If the player has participation info, clear leave time (they rejoined)
			participation.LeaveTime = time.Time{}
			participation.WasPresentAtEnd = false
		} else {
			// If the player has no participation info, create it now
			if participation := NewPlayerParticipation(ctx, logger, db, nk, state, p.GetUserId()); participation != nil {
				state.participations[p.GetUserId()] = participation
			}
		}
	}

	//m.updateLabel(logger, dispatcher, state)
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
		// If the match is empty, and the server has left, then shut down.
		logger.Debug("Match is empty. Shutting down.")
		return nil
	}

	// if the server is in the presences, then shut down.
	for _, p := range presences {
		if p.GetSessionId() == state.GameServer.SessionID.String() {
			logger.Debug("Server left the match. Shutting down.")
			state.server = nil
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

			// The entrant stream presence is only present when the player has not disconnect from the server yet.
			if userPresences, err := nk.StreamUserList(StreamModeEntrant, mp.EntrantID(state.ID).String(), "", node, false, true); err != nil {
				logger.Error("Failed to list user streams: %v", err)

			} else if len(userPresences) > 0 || p.GetReason() == runtime.PresenceReasonDisconnect {
				tags["reject_sent"] = "true"

				rejects = append(rejects, mp.EntrantID(state.ID))

				if err := nk.StreamUserLeave(StreamModeEntrant, mp.EntrantID(state.ID).String(), "", node, mp.GetUserId(), mp.GetSessionId()); err != nil {
					logger.Warn("Failed to leave user stream: %v", err)
				}

			} else {
				tags["reject_sent"] = "false"
				logger.Info("Player disconnected from game server. Removing from handler.")
			}

			nk.MetricsCounterAdd("match_entrant_leave_count", tags, 1)

			if err := MatchDataEvent(ctx, nk, state.ID, MatchDataPlayerLeave{
				State:    state,
				Presence: mp,
				Reason:   reason,
			}); err != nil {
				logger.Error("Failed to send match data event: %v", err)
			}

			ts := state.joinTimestamps[mp.GetSessionId()]
			nk.MetricsTimerRecord("match_player_session_duration", tags, time.Since(ts))

			// Store the player's time in the match to a leaderboard

			if err := AccumulateLeaderboardStat(ctx, nk, mp.GetUserId(), mp.DisplayName, state.GetGroupID().String(), state.Mode, LobbyTimeStatisticID, time.Since(ts).Seconds()); err != nil {
				logger.Warn("Failed to record match time to leaderboard: %v", err)
			}

			if participation, ok := state.participations[p.GetUserId()]; ok {
				// Record the leave event for the participation
				participation.RecordLeaveEvent(state)
			}
			// If the round is not over, then add an early quit count to the player.
			if state.Mode == evr.ModeArenaPublic && time.Now().After(state.StartTime.Add(time.Second*60)) && state.GameState != nil && !state.GameState.MatchOver {
				for _, p := range presences {
					if mp, ok := state.presenceMap[p.GetSessionId()]; ok {
						// Only players
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

						if err := AccumulateLeaderboardStat(ctx, nk, mp.GetUserId(), mp.DisplayName, state.GetGroupID().String(), state.Mode, EarlyQuitStatisticID, 1); err != nil {
							logger.Warn("Failed to record early quit to leaderboard: %v", err)
						}

						eqconfig := NewEarlyQuitConfig()
						_nk := nk.(*RuntimeGoNakamaModule)
						if err := StorableRead(ctx, nk, mp.GetUserId(), eqconfig, true); err != nil {
							logger.WithField("error", err).Warn("Failed to load early quitter config")
						} else {

							eqconfig.IncrementEarlyQuit()

							// Check for tier change after early quit
							serviceSettings := ServiceSettings()
							oldTier, newTier, tierChanged := eqconfig.UpdateTier(serviceSettings.Matchmaking.EarlyQuitTier1Threshold)

							logger.WithFields(map[string]interface{}{
								"old_tier":     oldTier,
								"new_tier":     newTier,
								"tier_changed": tierChanged,
								"eqconfig":     eqconfig,
							}).Debug("Early quitter tier update.")

							if err := StorableWrite(ctx, nk, mp.GetUserId(), eqconfig); err != nil {
								logger.Warn("Failed to write early quitter config", zap.Error(err))
							} else {
								if s := _nk.sessionRegistry.Get(uuid.FromStringOrNil(mp.GetSessionId())); s != nil {
									if params, ok := LoadParams(s.Context()); ok {
										params.earlyQuitConfig.Store(eqconfig)
									}
								}

								// Launch goroutine to check if player logs out and remove early quit if they do
								// Use a 5-minute grace period before checking logout status
								// Use background context to ensure goroutine isn't cancelled when match ends
								go func(userID, sessionID string) {
									bgCtx := context.Background()
									CheckAndStrikeEarlyQuitIfLoggedOut(bgCtx, logger, nk, db, _nk.sessionRegistry, userID, sessionID, 5*time.Minute)
								}(mp.GetUserId(), mp.GetSessionId())

								// Send Discord DM if tier changed
								if tierChanged {
									discordID, err := GetDiscordIDByUserID(ctx, db, mp.GetUserId())
									if err != nil {
										logger.Warn("Failed to get Discord ID for tier notification", zap.Error(err))
									} else if appBot := globalAppBot.Load(); appBot != nil && appBot.dg != nil {
										var message string
										if oldTier > newTier {
											// Degraded to Tier 2+
											message = TierDegradedMessage
										} else {
											// Recovered to Tier 1
											message = TierRestoredMessage
										}
										if _, err := SendUserMessage(ctx, appBot.dg, discordID, message); err != nil {
											logger.Warn("Failed to send tier change DM", zap.Error(err))
										}
									}
								}
							}
						}
					}
				}
			}

			delete(state.presenceMap, p.GetSessionId())
			delete(state.presenceByEvrID, mp.EvrID)
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
	if err := m.updateLabel(logger, dispatcher, state); err != nil {
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

			isPublicMatch := state.Mode == evr.ModeArenaPublic || state.Mode == evr.ModeCombatPublic

			if isPublicMatch {
				// Close public matches on round over
				if update.MatchOver && state.Open {
					logger.Info("Received round over for public match, locking match.")

					state.Open = false

					if state.LockedAt == nil {
						now := time.Now().UTC()
						state.LockedAt = &now
						logger.Info("Locking public match on round over")
					}
				}
			}

			if state.GameState != nil {

				logger.WithField("update", update).Debug("Received match update message.")

				state.GameState.MatchOver = true

				if len(update.Goals) > 0 {
					state.goals = append(state.goals, update.Goals...)
				}

				if state.GameState.SessionScoreboard != nil {
					if update.CurrentGameClock != 0 {
						if update.PauseDuration != 0 {
							state.GameState.SessionScoreboard.UpdateWithPause(update.CurrentGameClock, update.PauseDuration)
						} else {
							state.GameState.SessionScoreboard.Update(update.CurrentGameClock)
						}
					}
				}
				updateLabel = true
			}
		case OpCodeGameServerLobbyStatus:
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
			logger.Warn("Unknown match message type: %T", in)
			/*
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
				if messageFn == nil {
					logger.Warn("No handler for message type: %T", msg)
				} else {
					state, err = messageFn(ctx, logger, db, nk, dispatcher, state, in, msg)
					if err != nil {
						logger.Error("match pipeline: %v", err)
					}
				}
				logger.Debug("Message %T took %dms", msg, time.Since(start)/time.Millisecond)
			*/
		}
	}

	// If the match is terminating, terminate on the tick.
	if state.terminateTick != 0 {
		if tick >= state.terminateTick {
			logger.Debug("Match termination tick reached.")
			return m.MatchTerminate(ctx, logger, db, nk, dispatcher, tick, state, 0)
		}
		return state
	} else if state.server == nil {
		state.emptyTicks++
		if state.emptyTicks > 10*state.tickRate {
			logger.Warn("Match has been empty for too long. Shutting down.")
			return m.MatchShutdown(ctx, logger, db, nk, dispatcher, tick, state, 20)
		}
	} else if state.emptyTicks > 0 {
		state.emptyTicks = 0
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
	// Ensure the game server presence exists to avoid nil dispatch crashes.
	if !state.levelLoaded && state.server != nil && (len(state.presenceMap) != 0 || state.Started()) {
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
	if state.Started() && len(state.presenceMap) == 0 {
		state.emptyTicks++
		if state.emptyTicks > 60*state.tickRate {
			logger.Warn("Started match has been empty for too long. Shutting down.")
			return m.MatchShutdown(ctx, logger, db, nk, dispatcher, tick, state, 20)
		}
	} else {
		state.emptyTicks = 0
	}

	// Update the game clock every three seconds
	if tick%(state.tickRate*3) == 0 && state.GameState != nil {
		state.GameState.Update(state.goals)
		updateLabel = true
	}

	// Lock the match if it is open and the lock time has passed.
	if state.Open && state.LockedAt != nil && !state.LockedAt.IsZero() && time.Now().After(*state.LockedAt) {
		logger.Info("Closing the match in response to a lock.")
		state.Open = false
		updateLabel = true
	}

	// Every 2 seconds, update the participation durations for players that are still connected to the game server.
	if state.Mode == evr.ModeArenaPublic && tick%(2*state.tickRate) == 0 {
		delta := 2 * time.Second
		UpdateParticipationDurations(state, delta)

		// If the match is over, send the match summary once
		if state.GameState != nil && state.GameState.MatchOver && !state.matchSummarySent {
			state.matchSummarySent = true
			if err := SendMatchSummary(ctx, logger, nk, state); err != nil {
				logger.WithField("error", err).Error("failed to send match summary")
			}

			// Increment completed matches for players who stayed until the end
			_nk := nk.(*RuntimeGoNakamaModule)
			serviceSettings := ServiceSettings()
			for _, presence := range state.presenceMap {
				// Skip non-players (spectators, moderators)
				if presence.RoleAlignment == evr.TeamSpectator || presence.RoleAlignment == evr.TeamModerator {
					continue
				}

				eqconfig := NewEarlyQuitConfig()
				if err := StorableRead(ctx, nk, presence.GetUserId(), eqconfig, true); err != nil {
					logger.WithField("error", err).Warn("Failed to load early quitter config for completed match")
					continue
				}

				eqconfig.IncrementCompletedMatches()

				// Check for tier change after completing match
				oldTier, newTier, tierChanged := eqconfig.UpdateTier(serviceSettings.Matchmaking.EarlyQuitTier1Threshold)

				logger.WithFields(map[string]interface{}{
					"user_id":      presence.GetUserId(),
					"old_tier":     oldTier,
					"new_tier":     newTier,
					"tier_changed": tierChanged,
					"eqconfig":     eqconfig,
				}).Debug("Player completed match tier update.")

				if err := StorableWrite(ctx, nk, presence.GetUserId(), eqconfig); err != nil {
					logger.Warn("Failed to write early quitter config after completed match", zap.Error(err))
				} else {
					if s := _nk.sessionRegistry.Get(uuid.FromStringOrNil(presence.GetSessionId())); s != nil {
						if params, ok := LoadParams(s.Context()); ok {
							params.earlyQuitConfig.Store(eqconfig)
						}
					}

					// Send Discord DM if tier changed (restored from penalty tier), unless silent mode is enabled
					if tierChanged && newTier == MatchmakingTier1 && !serviceSettings.Matchmaking.SilentEarlyQuitSystem {
						discordID, err := GetDiscordIDByUserID(ctx, db, presence.GetUserId())
						if err != nil {
							logger.Warn("Failed to get Discord ID for tier notification", zap.Error(err))
						} else if appBot := globalAppBot.Load(); appBot != nil && appBot.dg != nil {
							message := TierRestoredMessage
							if _, err := SendUserMessage(ctx, appBot.dg, discordID, message); err != nil {
								logger.Warn("Failed to send tier restored Discord DM", zap.Error(err))
							}
						}
					}
				}
			}
		}
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
func (m *EvrMatch) MatchTerminate(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, graceSeconds int) interface{} {
	state, ok := state_.(*MatchLabel)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}
	state.Open = false
	logger.Debug("MatchTerminate called.")
	nk.MetricsCounterAdd("match_terminate_count", state.MetricsTags(), 1)

	// Send match summary if not already sent
	if !state.matchSummarySent && len(state.participations) > 0 {
		state.matchSummarySent = true
		if err := SendMatchSummary(ctx, logger, nk, state); err != nil {
			logger.WithField("error", err).Error("failed to send match summary on terminate")
		}
	}

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

	nk.MetricsTimerRecord("lobby_session_duration", state.MetricsTags(), time.Since(state.StartTime))
	if state.server != nil && slices.Contains(ValidLeaderboardModes, state.Mode) {
		if err := AccumulateLeaderboardStat(ctx, nk, state.server.GetUserId(), state.server.GetUsername(), state.GetGroupID().String(), state.Mode, GameServerTimeStatisticsID, time.Since(state.StartTime).Seconds()); err != nil {
			logger.Warn("Failed to record game server time to leaderboard: %v", err)
		}
	}
	state.Open = false
	state.terminateTick = tick + int64(graceSeconds)*state.tickRate

	if err := m.updateLabel(logger, dispatcher, state); err != nil {
		logger.Error("failed to update label: %v", err)
		return nil
	}

	if state.server != nil {

		/*
			// Send return to lobby message.
			if s := nk.(*RuntimeGoNakamaModule).sessionRegistry.Get(uuid.FromStringOrNil(state.GameServer.GetSessionId())); s != nil {
				if err := SendEVRMessages(s, false, &evr.NEVRLobbyReturnToLobbyV1{}); err != nil {
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
				<-time.After(time.Second * 5) // Give the game server time to process the return to lobby message.
				if err := m.sendEntrantReject(ctx, logger, dispatcher, server, evr.PlayerRejectionReasonLobbyEnding, entrantIDs...); err != nil {
					logger.Error("Failed to send entrant reject: %v", err)
				}
			}(state.server, entrantIDs)
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
	case SignalKickEntrants:
		var data SignalKickEntrantsPayload

		if err := json.Unmarshal(signal.Payload, &data); err != nil {
			return state, SignalResponse{Message: fmt.Sprintf("failed to unmarshal shutdown payload: %v", err)}.String()
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
				return state, SignalResponse{Message: fmt.Sprintf("failed to kick player: %v", err)}.String()
			}
		}

		logger.WithFields(map[string]any{
			"requested_user_ids": data.UserIDs,
			"entrant_ids":        entrantIDs,
		}).Debug("Kicking players from the match.")

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
						return state, SignalResponse{Message: fmt.Sprintf("failed to kick player: %v", err)}.String()
					}
				}

				logger.Warn("Match shutting down, disconnecting players.")
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
		jsonData, err := json.Marshal(state.GameServer.Endpoint)
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
			if !slices.Contains(state.GameServer.Features, f) {
				return state, SignalResponse{Message: fmt.Sprintf("bad request: feature not supported: %v", f)}.String()
			}
		}

		if ok, err := CheckSystemGroupMembership(ctx, db, settings.SpawnedBy, GroupGlobalDevelopers); err != nil {
			return state, SignalResponse{Message: fmt.Sprintf("failed to check group membership: %v", err)}.String()
		} else if !ok {

			// Validate the mode
			if levels, ok := evr.LevelsByMode[settings.Mode]; !ok {
				return state, SignalResponse{Message: fmt.Sprintf("bad request: invalid mode: %v", settings.Mode)}.String()

			} else {
				// Set the level to a random level if it is not set.
				if settings.Level == 0xffffffffffffffff || settings.Level == 0 {
					settings.Level = levels[rand.Intn(len(levels))]

					// Validate the level, if provided.
				} else if !slices.Contains(levels, settings.Level) {
					return state, SignalResponse{Message: fmt.Sprintf("bad request: invalid level `%v` for mode `%v`", settings.Level, settings.Mode)}.String()
				}
			}
		}

		state.Mode = settings.Mode
		state.Level = settings.Level
		state.RequiredFeatures = settings.RequiredFeatures
		state.SessionSettings = evr.NewSessionSettings(strconv.FormatUint(PcvrAppId, 10), state.Mode, state.Level, settings.RequiredFeatures)
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
			// Set the start time to now will trigger the match to start.
			state.StartTime = time.Now().UTC()
		} else {
			return state, SignalResponse{Message: "failed to start session: already started"}.String()
		}

	case SignalLockSession:
		// Lock signals are logged but have no effect - matches are locked via round over events
		logger.Info("Lock signal received (no effect)")

	case SignalUnlockSession:

		switch state.Mode {
		case evr.ModeArenaPublic:
			logger.Debug("Ignoring unlock signal for arena public match.")
		default:
			logger.Debug("Unlocking session")
			if state.GameState != nil {
				state.LockedAt = nil
			}
			state.Open = true
		}

	case SignalEndedSession:
		// Trigger the MatchLeave event for the game server.
		if err := nk.StreamUserLeave(StreamModeMatchAuthoritative, state.ID.UUID.String(), "", state.ID.Node, state.GameServer.GetUserId(), state.GameServer.GetSessionId()); err != nil {
			logger.Warn("Failed to leave match stream", zap.Error(err))
			return nil, SignalResponse{Message: fmt.Sprintf("failed to leave match stream: %v", err)}.String()
		}

	case SignalPlayerUpdate:
		update := MatchPlayerUpdate{}
		if err := json.Unmarshal(signal.Payload, &update); err != nil {
			return state, SignalResponse{Message: fmt.Sprintf("failed to unmarshal player update: %v", err)}.String()
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
		return state, SignalResponse{Success: false, Message: "unknown signal"}.String()
	}

	if err := m.updateLabel(logger, dispatcher, state); err != nil {
		logger.Error("failed to update label: %v", err)
		return state, SignalResponse{Message: fmt.Sprintf("failed to update label: %v", err)}.String()
	}

	nk.MetricsCounterAdd("match_prepare_count", state.MetricsTags(), 1)

	return state, SignalResponse{Success: true, Payload: state.GetLabel()}.String()

}

func (m *EvrMatch) MatchStart(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *MatchLabel) (*MatchLabel, error) {
	// Do not attempt to start without a game server presence; avoids nil dispatch.
	if state.server == nil {
		return state, fmt.Errorf("cannot start match: server presence is nil")
	}
	groupID := uuid.Nil
	if state.GroupID != nil {
		groupID = *state.GroupID
	}

	switch state.Mode {
	case evr.ModeArenaPublic:
		state.GameState = &GameState{
			SessionScoreboard: NewSessionScoreboard(RoundDuration, time.Now().Add(PublicMatchWaitTime)),
		}
	case evr.ModeArenaPrivate:
		state.GameState = &GameState{
			SessionScoreboard: NewSessionScoreboard(0, time.Time{}),
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
		evr.NewGameServerSessionStart(state.ID.UUID, groupID, uint8(state.MaxSize), uint8(state.LobbyType), state.GameServer.AppID, state.Mode, state.Level, state.RequiredFeatures, []evr.EvrId{}), // Legacy Message for the game server.
	}

	nk.MetricsCounterAdd("match_start_count", state.MetricsTags(), 1)
	// Dispatch the message for delivery.
	if err := m.dispatchMessages(ctx, logger, dispatcher, messages, []runtime.Presence{state.server}, nil); err != nil {
		return state, fmt.Errorf("failed to dispatch message: %w", err)
	}
	state.levelLoaded = true

	MatchDataEvent(ctx, nk, state.ID, MatchDataStarted{
		State: state,
	})
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

func (m *EvrMatch) updateLabel(logger runtime.Logger, dispatcher runtime.MatchDispatcher, state *MatchLabel) error {
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

func (m *EvrMatch) kickEntrants(ctx context.Context, logger runtime.Logger, dispatcher runtime.MatchDispatcher, state *MatchLabel, entrantIDs ...uuid.UUID) error {
	return m.sendEntrantReject(ctx, logger, dispatcher, state.server, evr.PlayerRejectionReasonKickedFromServer, entrantIDs...)
}

func (m *EvrMatch) sendEntrantReject(ctx context.Context, logger runtime.Logger, dispatcher runtime.MatchDispatcher, server runtime.Presence, reason evr.PlayerRejectionReason, entrantIDs ...uuid.UUID) error {
	msg := evr.NewGameServerEntrantRejected(reason, entrantIDs...)
	if err := m.dispatchMessages(ctx, logger, dispatcher, []evr.Message{msg}, []runtime.Presence{server}, nil); err != nil {
		return fmt.Errorf("failed to dispatch message: %w", err)
	}
	return nil
}
