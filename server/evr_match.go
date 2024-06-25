package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/rand"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/ipinfo/go/v2/ipinfo"
	"github.com/samber/lo"
	"go.uber.org/zap"
)

const (
	VersionLock       uint64 = 0xc62f01d78f77910d // The game build version.
	MatchmakingModule        = "evr"              // The module used for matchmaking

	MatchMaxSize = 12 // The total max players (not including the broadcaster) for a EVR lobby.

	LevelSelectionFirst  MatchLevelSelection = "first"
	LevelSelectionRandom MatchLevelSelection = "random"

	StatGroupArena  MatchStatGroup = "arena"
	StatGroupCombat MatchStatGroup = "combat"
)

const (
	OpCodeBroadcasterDisconnected int64 = iota
	OpCodeEvrPacketData

	SignalPrepareSession
	SignalStartSession
	SignalGetEndpoint
	SignalGetPresences
	SignalPruneUnderutilized
	SignalTerminate
)

var (
	displayNameRegex   = regexp.MustCompile(`"displayname": "(\\"|[^"])*"`)
	updatetimeRegex    = regexp.MustCompile(`"updatetime": \d*`)
	StreamContextMatch = uuid.NewV5(uuid.Nil, "match")
)

type MatchStatGroup string
type MatchLevelSelection string

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
	EvrMatchmakerModule = "evrmatchmaker"
	EvrBackfillModule   = "evrbackfill"
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
	EvrID         evr.EvrId // The player's evr id.
	PlayerSession uuid.UUID // Match-scoped session id.
	TeamIndex     int       // the team index the player prefers/has been assigned to.
	PartyID       uuid.UUID // The party id the player is in.
	DiscordID     string
	ClientIP      string
	Query         string // Matchmaking query used to find this match.
}

func (p *EvrMatchPresence) String() string {

	data, err := json.Marshal(struct {
		UserID      string `json:"userid,omitempty"`
		DisplayName string `json:"displayname,omitempty"`
		EvrId       string `json:"evrid,omitempty"`
		TeamIndex   int    `json:"team,omitempty"`
	}{p.UserID.String(), p.DisplayName, p.EvrID.Token(), p.TeamIndex})
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
	return p.EvrID.Token()
}

func (p *EvrMatchPresence) GetPlayerSession() string {
	return p.PlayerSession.String()
}

type EvrMatchMeta struct {
	MatchBroadcaster
	Players []EvrMatchPresence `json:"players,omitempty"` // The displayNames of the players (by team name) in the match.
	// Stats
}
type PlayerInfo struct {
	UserID      string    `json:"user_id,omitempty"`
	Username    string    `json:"username,omitempty"`
	DisplayName string    `json:"display_name,omitempty"`
	EvrID       evr.EvrId `json:"evr_id,omitempty"`
	Team        TeamIndex `json:"team"`
	ClientIP    string    `json:"client_ip,omitempty"`
	DiscordID   string    `json:"discord_id,omitempty"`
	PartyID     string    `json:"party_id,omitempty"`
}

type MatchBroadcaster struct {
	SessionID     string       `json:"sid,omitempty"`            // The broadcaster's Session ID
	OperatorID    string       `json:"oper,omitempty"`           // The user id of the broadcaster.
	GroupIDs      []uuid.UUID  `json:"group_ids,omitempty"`      // The channels this broadcaster will host matches for.
	Endpoint      evr.Endpoint `json:"endpoint,omitempty"`       // The endpoint data used for connections.
	VersionLock   uint64       `json:"version_lock,omitempty"`   // The game build version. (EVR)
	AppId         string       `json:"app_id,omitempty"`         // The game app id. (EVR)
	Regions       []evr.Symbol `json:"regions,omitempty"`        // The region the match is hosted in. (Matching Only) (EVR)
	IPinfo        *ipinfo.Core `json:"ip_info,omitempty"`        // The IPinfo of the broadcaster.
	ServerID      uint64       `json:"server_id,omitempty"`      // The server id of the broadcaster. (EVR)
	PublisherLock bool         `json:"publisher_lock,omitempty"` // Publisher lock (EVR)
	Features      []string     `json:"features,omitempty"`       // The features of the broadcaster.
	Tags          []string     `json:"tags,omitempty"`           // The tags given on the urlparam for the match.
}

// The lobby state is used for the match label.
// Any changes to the lobby state should be reflected in the match label.
// This also makes it easier to update the match label, and query against it.
type EvrMatchState struct {
	ID          MatchID          `json:"id,omitempty"`          // The Session Id used by EVR (the same as match id)
	Open        bool             `json:"open,omitempty"`        // Whether the lobby is open to new players (Matching Only)
	LobbyType   LobbyType        `json:"lobby_type"`            // The type of lobby (Public, Private, Unassigned) (EVR)
	Broadcaster MatchBroadcaster `json:"broadcaster,omitempty"` // The broadcaster's data
	Started     bool             `json:"started"`               // Whether the match has started.
	StartTime   time.Time        `json:"start_time,omitempty"`  // The time the match was started.
	SpawnedBy   string           `json:"spawned_by,omitempty"`  // The userId of the player that spawned this match.
	GroupID     *uuid.UUID       `json:"group_id,omitempty"`    // The channel id of the broadcaster. (EVR)
	GuildID     string           `json:"guild_id,omitempty"`    // The guild id of the broadcaster. (EVR)
	GuildName   string           `json:"guild_name,omitempty"`  // The guild name of the broadcaster. (EVR)

	Mode             evr.Symbol           `json:"mode,omitempty"`              // The mode of the lobby (Arena, Combat, Social, etc.) (EVR)
	Level            evr.Symbol           `json:"level,omitempty"`             // The level to play on (EVR).
	SessionSettings  *evr.SessionSettings `json:"session_settings,omitempty"`  // The session settings for the match (EVR).
	RequiredFeatures []string             `json:"required_features,omitempty"` // The required features for the match.

	MaxSize     uint8     `json:"limit,omitempty"`        // The total lobby size limit (players + specs)
	Size        int       `json:"size,omitempty"`         // The number of players (including spectators) in the match.
	PlayerCount int       `json:"player_count,omitempty"` // The number of participants (not including spectators) in the match.
	PlayerLimit int       `json:"player_limit,omitempty"` // The number of players in the match (not including spectators).
	TeamSize    int       `json:"team_size,omitempty"`    // The size of each team in arena/combat (either 4 or 5)
	TeamIndex   TeamIndex `json:"team,omitempty"`         // What team index a player prefers (Used by Matching only)

	Players        []PlayerInfo                    `json:"players,omitempty"`         // The displayNames of the players (by team name) in the match.
	TeamAlignments map[uuid.UUID]int               `json:"team_alignments,omitempty"` // map[userID]TeamIndex
	presences      map[uuid.UUID]*EvrMatchPresence // [sessionId]EvrMatchPresence
	broadcaster    runtime.Presence                // The broadcaster's presence
	presenceCache  map[uuid.UUID]*EvrMatchPresence // [sessionId]PlayerMeta cache for all players that have attempted to join the match.

	emptyTicks            int64 // The number of ticks the match has been empty.
	sessionStartExpiry    int64 // The tick count at which the match will be shut down if it has not started.
	broadcasterJoinExpiry int64 // The tick count at which the match will be shut down if the broadcaster has not joined.
	tickRate              int64 // The number of ticks per second.
}

func (s *EvrMatchState) String() string {
	return s.GetLabel()
}

func (s *EvrMatchState) GetLabel() string {
	labelJson, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return ""
	}
	return string(labelJson)
}

func (s *EvrMatchState) PublicView() *EvrMatchState {
	ps := *s
	ps.Broadcaster.SessionID = ""
	if ps.LobbyType == PrivateLobby || ps.LobbyType == UnassignedLobby {
		ps.ID = MatchID{}
		ps.SpawnedBy = ""
		ps.TeamAlignments = nil
		ps.Players = nil
	}

	for i := range ps.Players {
		ps.Players[i].ClientIP = ""
		ps.Players[i].PartyID = ""
	}
	return &ps
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

	s.Players = make([]PlayerInfo, 0, len(s.presences))
	s.Size = len(s.presences)
	s.PlayerCount = 0
	// Construct Player list
	for _, presence := range s.presences {
		// Do not include spectators or moderators in player count
		if presence.TeamIndex != evr.TeamSpectator && presence.TeamIndex != evr.TeamModerator {
			s.PlayerCount++
		}
		playerinfo := PlayerInfo{
			UserID:      presence.UserID.String(),
			Username:    presence.Username,
			DisplayName: presence.DisplayName,
			EvrID:       presence.EvrID,
			Team:        TeamIndex(presence.TeamIndex),
			ClientIP:    presence.ClientIP,
			DiscordID:   presence.DiscordID,
			PartyID:     presence.PartyID.String(),
		}

		s.Players = append(s.Players, playerinfo)
	}

	sort.SliceStable(s.Players, func(i, j int) bool {
		return s.Players[i].Team < s.Players[j].Team
	})
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
func NewEvrMatchState(endpoint evr.Endpoint, config *MatchBroadcaster) (state *EvrMatchState, params map[string]interface{}, configPayload string, err error) {

	var tickRate int64 = 10 // 10 ticks per second

	initialState := EvrMatchState{
		Broadcaster:      *config,
		Open:             false,
		LobbyType:        UnassignedLobby,
		Mode:             evr.ModeUnloaded,
		Level:            evr.LevelUnloaded,
		RequiredFeatures: make([]string, 0),
		Players:          make([]PlayerInfo, 0, MatchMaxSize),
		presences:        make(map[uuid.UUID]*EvrMatchPresence, MatchMaxSize),
		presenceCache:    make(map[uuid.UUID]*EvrMatchPresence, MatchMaxSize),
		TeamAlignments:   make(map[uuid.UUID]int, MatchMaxSize),

		emptyTicks: 0,
		tickRate:   tickRate,
	}

	stateJson, err := json.Marshal(initialState)
	if err != nil {
		return nil, nil, "", fmt.Errorf("failed to marshal match config: %v", err)
	}

	params = map[string]interface{}{
		"initialState": stateJson,
	}

	return &initialState, params, string(stateJson), nil
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
	state := EvrMatchState{}
	if err := json.Unmarshal(params["initialState"].([]byte), &state); err != nil {
		logger.Error("Failed to unmarshal match config. %s", err)
	}
	const (
		BroadcasterJoinTimeoutSecs = 45
	)

	state.ID = MatchIDFromContext(ctx)
	state.presenceCache = make(map[uuid.UUID]*EvrMatchPresence)
	state.presences = make(map[uuid.UUID]*EvrMatchPresence)

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

// selectTeamForPlayer decides which team to assign a player to.
func selectTeamForPlayer(logger runtime.Logger, presence *EvrMatchPresence, state *EvrMatchState) (int, bool) {
	t := presence.TeamIndex

	teams := lo.GroupBy(lo.Values(state.presences), func(p *EvrMatchPresence) int { return p.TeamIndex })

	blueTeam := teams[evr.TeamBlue]
	orangeTeam := teams[evr.TeamOrange]
	playerpop := len(blueTeam) + len(orangeTeam)
	spectators := len(teams[evr.TeamSpectator]) + len(teams[evr.TeamModerator])
	teamsFull := playerpop >= state.TeamSize*2
	specsFull := spectators >= int(state.MaxSize)-state.TeamSize*2

	// If the lobby is full, reject
	if len(state.presences) >= int(state.MaxSize) {
		return evr.TeamUnassigned, false
	}

	// If the player is a moderator and the spectators are not full, return
	if t == evr.TeamModerator && !specsFull {
		return t, true
	}

	// If the match has been running for less than 15 seconds check the presets for the team
	if time.Since(state.StartTime) < 15*time.Second {
		if teamIndex, ok := state.TeamAlignments[presence.UserID]; ok {
			// Make sure the team isn't already full
			if len(teams[teamIndex]) < state.TeamSize {
				return teamIndex, true
			}
		}
	}

	// If this is a social lobby, put the player on the social team.
	if state.Mode == evr.ModeSocialPublic || state.Mode == evr.ModeSocialPrivate {
		return evr.TeamSocial, true
	}

	// If the player is unassigned, assign them to a team.
	if t == evr.TeamUnassigned {
		t = evr.TeamBlue
	}

	// If this is a private lobby, and the teams are full, put them on spectator
	if t != evr.TeamSpectator && teamsFull {
		if state.LobbyType == PrivateLobby {
			t = evr.TeamSpectator
		} else {
			// The lobby is full, reject the player.
			return evr.TeamUnassigned, false
		}
	}

	// If this is a spectator, and the spectators are not full, return
	if t == evr.TeamSpectator {
		if specsFull {
			return evr.TeamUnassigned, false
		}
		return t, true
	}
	// If this is a private lobby, and their team is not full,  put the player on the team they requested
	if state.LobbyType == PrivateLobby && len(teams[t]) < state.TeamSize {
		return t, true
	}

	// If the players team is unbalanced, put them on the other team
	if len(teams[evr.TeamBlue]) != len(teams[evr.TeamOrange]) {
		if len(teams[evr.TeamBlue]) < len(teams[evr.TeamOrange]) {
			t = evr.TeamBlue
		} else {
			t = evr.TeamOrange
		}
	}

	logger.Debug("picked team", zap.Int("team", t))
	return t, true
}

const (
	ErrJoinRejectedUnassignedLobby = "unassigned lobby"
	ErrJoinRejectedDuplicateJoin   = "duplicate join"
	ErrJoinRejectedLobbyFull       = "lobby full"
	ErrJoinRejectedNotModerator    = "not a moderator"
)

// MatchJoinAttempt decides whether to accept or deny the player session.
func (m *EvrMatch) MatchJoinAttempt(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presence runtime.Presence, metadata map[string]string) (interface{}, bool, string) {
	state, ok := state_.(*EvrMatchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil, false, ""
	}

	logger = logger.WithField("mid", state.ID.String())
	logger = logger.WithField("username", presence.GetUsername())

	switch {
	case presence.GetSessionId() == state.Broadcaster.SessionID:
		logger.Debug("Broadcaster joining the match.")
		state.broadcaster = presence
		// This is the broadcaster joining, this completes the match init.
		return state, true, ""
	}

	// This is a player joining.
	mp := &EvrMatchPresence{}
	if err := json.Unmarshal([]byte(metadata["playermeta"]), &mp); err != nil {
		return state, false, fmt.Sprintf("failed to unmarshal metadata: %q", err)
	}

	// If this is a parking match, reject the player
	if state.LobbyType == UnassignedLobby {
		return state, false, ErrJoinRejectedUnassignedLobby
	}

	// If this session is already in the match, reject the player
	sessionID := uuid.FromStringOrNil(presence.GetSessionId())
	if _, ok := state.presences[sessionID]; ok {
		logger.Warn("Rejecting duplicate join attempt.")
		return state, false, ErrJoinRejectedDuplicateJoin
	}

	// If this EvrID is already in the match, reject the player
	for _, p := range state.presences {
		if p.EvrID.Equals(mp.EvrID) {
			logger.Warn("Rejecting join from existing EVR-ID: %s", metadata["evrid"])
			return state, false, ErrJoinRejectedDuplicateJoin
		}
	}

	// If the player is not allowed to be a moderator, reject them.
	if mp.TeamIndex == evr.TeamModerator {
		found, err := checkIfGlobalModerator(ctx, nk, uuid.FromStringOrNil(presence.GetUserId()))
		if err != nil {
			return state, false, fmt.Sprintf("failed to check if moderator: %v", err)
		}
		if !found {
			return state, false, ErrJoinRejectedNotModerator
		}
	}

	// If the lobby is full, reject them
	if len(state.presences) >= MatchMaxSize {
		return state, false, ErrJoinRejectedLobbyFull
	}

	// Only pick teams for public matches.
	if state.Mode == evr.ModeArenaPublic || state.Mode == evr.ModeCombatPublic {
		// If the entrant is joining as a spectator, do not look up their previous team.
		if mp.TeamIndex != evr.TeamSpectator && mp.TeamIndex != evr.TeamModerator {
			// If this is the player rejoining, prefer the same team.
			if matchPresence, ok := state.presenceCache[sessionID]; ok {
				mp.TeamIndex = matchPresence.TeamIndex
			}
		}
		if mp.TeamIndex, ok = selectTeamForPlayer(logger, mp, state); !ok {
			// The lobby is full, reject the player.
			return state, false, ErrJoinRejectedLobbyFull
		}
	}

	// Reserve this player's spot in the match.
	state.presences[sessionID] = mp

	// The player data will reused if the player rejoins the match.
	state.presenceCache[sessionID] = mp
	// Accept the player(s) into the session.

	state.rebuildCache()
	err := m.updateLabel(dispatcher, state)
	if err != nil {
		return state, false, fmt.Sprintf("failed to update label: %q", err)
	}

	// Tell teh broadcaster to load the match if it's not already started
	if !state.Started {

		// Instruct the server to load the level

		state, err := m.StartSession(ctx, logger, nk, dispatcher, state)
		if err != nil {
			return state, false, ""
		}
	}

	logger.Debug("Accepting player into match: %s (%s)", presence.GetUsername(), mp.GetPlayerSession())
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
		logger = logger.WithFields(map[string]interface{}{
			"username": p.GetUsername(),
			"uid":      p.GetUserId(),
		})
		logger.Debug("Joined a new player")

		if p.GetSessionId() == state.Broadcaster.SessionID {
			logger.Debug("Broadcaster joined the match.")
			state.broadcaster = p
			state.Open = true // Available

			if state.LobbyType == UnassignedLobby {
				// This is a parking match. do nothing.
				continue
			}
			if state.GroupID == nil {
				logger.Error("Channel is nil. This shouldn't happen.")
				state.GroupID = &uuid.Nil
			}

			state, err := m.StartSession(ctx, logger, nk, dispatcher, state)
			if err != nil {
				logger.Error("failed to start session: %v", err)
				return nil
			}
			// If there are already players in this match, then notify them to load.
			for _, presence := range state.presences {
				err := m.sendPlayerStart(ctx, logger, dispatcher, state, presence)
				if err != nil {
					logger.Error("failed to send player start: %v", err)
				}
			}

			continue
		}

		// This is a player joining
		sessionID := uuid.FromStringOrNil(p.GetSessionId())
		// the cache entry is kept even after a player leaves. This helps put them back on the same team.
		matchPresence, ok := state.presences[sessionID]
		if !ok {
			// TODO FIXME Kick the player from the match.
			logger.Error("Player not in cache. This shouldn't happen.")
			return errors.New("player not in cache")
		}

		// Update the player's status to include the match ID
		if err := nk.StreamUserUpdate(StreamModeEvr, p.GetUserId(), StreamContextMatch.String(), "", p.GetUserId(), p.GetSessionId(), false, false, state.ID.String()); err != nil {
			logger.Warn("Failed to update user status: %v", err)
		}

		// Send this after the function returns to ensure the match is ready to receive the player.
		err := m.sendPlayerStart(ctx, logger, dispatcher, state, matchPresence)
		if err != nil {
			logger.Error("failed to send player start: %v", err)
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

func (m *EvrMatch) sendPlayerStart(ctx context.Context, logger runtime.Logger, dispatcher runtime.MatchDispatcher, state *EvrMatchState, p *EvrMatchPresence) error {

	gameMode := state.Mode
	teamIndex := int16(p.TeamIndex)
	channel := state.GroupID
	matchSession := state.ID.UUID()
	endpoint := state.Broadcaster.Endpoint
	success := evr.NewLobbySessionSuccess(gameMode, matchSession, *channel, endpoint, teamIndex)
	successV4 := success.Version4()
	successV5 := success.Version5()
	messages := []evr.Message{
		successV4,
		successV5,
		evr.NewSTcpConnectionUnrequireEvent(),
	}

	go func() {
		// Delay to allow everything to be ready for the user to join.
		<-time.After(1 * time.Second)
		// Wait until 5 seconds after the match has started to send the player start message.
		<-time.After(time.Until(state.StartTime.Add(5 * time.Second)))

		// Dispatch the message for delivery.
		if err := m.dispatchMessages(ctx, logger, dispatcher, messages, []runtime.Presence{state.broadcaster, p}, nil); err != nil {
			logger.Error("failed to dispatch success message to broadcaster: %v", err)
		}
	}()
	return nil

}

// MatchLeave is called after a player leaves the match.
func (m *EvrMatch) MatchLeave(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, tick int64, state_ interface{}, presences []runtime.Presence) interface{} {
	state, ok := state_.(*EvrMatchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	// if the broadcaster is in the presences, then shut down.
	for _, p := range presences {
		if p.GetSessionId() == state.Broadcaster.SessionID {
			logger.Debug("Broadcaster left the match. Shutting down.")
			return nil
		}
	}

	// Create a list of sessions to remove
	rejects := lo.Map(presences, func(p runtime.Presence, _ int) uuid.UUID {
		sessionID := uuid.FromStringOrNil(p.GetSessionId())
		// Get the match presence for this user
		matchPresence, ok := state.presences[sessionID]
		if !ok || matchPresence == nil {
			return uuid.Nil
		}
		return uuid.FromStringOrNil(matchPresence.GetPlayerSession())
	})

	// Filter out the uuid.Nil's
	rejects = lo.Filter(rejects, func(u uuid.UUID, _ int) bool {
		return u != uuid.Nil
	})

	for _, p := range presences {
		// Update the player's status to remove the match ID
		// Check if the session exists.

		sessionID := uuid.FromStringOrNil(p.GetSessionId())
		matchPresence, ok := state.presences[sessionID]
		if !ok || matchPresence == nil {
			continue
		}
		delete(state.presences, sessionID)

		if _, err := nk.StreamUserGet(StreamModeEvr, p.GetUserId(), StreamContextMatch.String(), "", p.GetUserId(), p.GetSessionId()); err != nil {
			continue
		}
		if err := nk.StreamUserUpdate(StreamModeEvr, p.GetUserId(), StreamContextMatch.String(), "", p.GetUserId(), p.GetSessionId(), false, false, ""); err != nil {
			logger.Debug("Failed to update user status for %v: %v", p, err)
		}
	}
	// Delete the each user from the match.
	if len(rejects) > 0 {

		go func(rejects []uuid.UUID) {
			// Inform players (if they are still in the match) that the broadcaster has disconnected.
			messages := []evr.Message{
				evr.NewBroadcasterPlayersRejected(evr.PlayerRejectionReasonDisconnected, rejects...),
			}
			if err := m.dispatchMessages(ctx, logger, dispatcher, messages, []runtime.Presence{state.broadcaster}, nil); err != nil {
				logger.Error("failed to dispatch broadcaster disconnected message: %v", err)
			}
		}(rejects)
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
	state, ok := state_.(*EvrMatchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}

	var err error

	switch {
	case state.broadcaster == nil && tick > state.broadcasterJoinExpiry:
		// If the broadcaster has not joined within the timeout, shut down the match.
		logger.Debug("Broadcaster did not join before the expiry time. Shutting down.")
		return nil
	case state.LobbyType != UnassignedLobby && !state.Started && tick > state.sessionStartExpiry:
		logger.Debug("Match did not start before the expiry time. Shutting down.")
		return nil
	case state.LobbyType != UnassignedLobby && len(state.presences) == 0:
		// If the match is empty, check if it has been empty for too long.
		state.emptyTicks++
		if state.emptyTicks > 20*state.tickRate {
			logger.Debug("Match has been empty for too long. Shutting down.")
			return nil
		}
	default:
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

			logger.Debug("Received match message %T(%s) from %s (%s)", typ, string(in.GetData()), in.GetUsername(), in.GetSessionId())
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
			case *evr.BroadcasterPlayerSessionsLocked:
				// The server has locked the player sessions.
				messageFn = m.broadcasterPlayerSessionsLocked
			case *evr.BroadcasterPlayerSessionsUnlocked:
				// The server has locked the player sessions.
				messageFn = m.broadcasterPlayerSessionsUnlocked
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
	state, ok := state_.(*EvrMatchState)
	if !ok {
		logger.Error("state not a valid lobby state object")
		return nil
	}
	logger.Info("MatchTerminate called. %v", state)
	if state.broadcaster != nil {
		// Disconnect the players
		for _, presence := range state.presences {
			nk.SessionDisconnect(ctx, presence.GetPlayerSession(), runtime.PresenceReasonDisconnect)
		}
		// Disconnect the broadcasters session
		nk.SessionDisconnect(ctx, state.broadcaster.GetSessionId(), runtime.PresenceReasonDisconnect)

	}

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
	state, ok := state_.(*EvrMatchState)
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
		if len(state.presences) <= 3 {
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

		jsonData, err := json.Marshal(state.presences)
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

		var newState = EvrMatchState{}
		if err := json.Unmarshal(signal.Data, &newState); err != nil {
			return state, SignalResponse{Message: fmt.Sprintf("failed to unmarshal match label: %v", err)}.String()
		}

		state.Started = false
		state.Open = newState.Open
		state.Mode = newState.Mode
		state.Level = newState.Level
		state.SpawnedBy = newState.SpawnedBy
		state.GroupID = newState.GroupID
		state.MaxSize = newState.MaxSize
		state.SessionSettings = newState.SessionSettings
		state.RequiredFeatures = newState.RequiredFeatures
		state.TeamSize = newState.TeamSize
		state.StartTime = newState.StartTime

		if state.StartTime.IsZero() || state.StartTime.Before(time.Now()) {
			state.StartTime = time.Now()
		}
		state.sessionStartExpiry = tick + (15 * 60 * state.tickRate)

		state.TeamAlignments = make(map[uuid.UUID]int, MatchMaxSize)

		switch newState.Mode {
		case evr.ModeArenaPublic, evr.ModeSocialPublic, evr.ModeCombatPublic:
			state.LobbyType = PublicLobby
		default:
			state.LobbyType = PrivateLobby
		}

		if state.TeamSize == 0 {
			state.TeamSize = 5
		}

		if state.Mode == evr.ModeSocialPrivate || state.Mode == evr.ModeSocialPublic {
			state.PlayerLimit = int(state.MaxSize)
		} else {
			state.PlayerLimit = state.TeamSize * 2
		}

		if state.Level == 0xffffffffffffffff || state.Level == 0 {
			// The level is not set, set it to a random value
			if levels, ok := evr.LevelsByMode[state.Mode]; ok {
				state.Level = levels[rand.Intn(len(levels))]
			}
		}

		if state.SessionSettings == nil {
			settings := evr.NewSessionSettings(strconv.FormatUint(PcvrAppId, 10), state.Mode, state.Level, state.RequiredFeatures)
			state.SessionSettings = &settings
		}

		if newState.Players != nil {
			for _, player := range newState.Players {
				state.TeamAlignments[uuid.FromStringOrNil(player.UserID)] = int(player.Team)
			}
		}

		state.rebuildCache()

		if err := m.updateLabel(dispatcher, state); err != nil {
			logger.Error("failed to update label: %v", err)
			return state, SignalResponse{Message: fmt.Sprintf("failed to update label: %v", err)}.String()
		}

		return state, SignalResponse{Success: true, Payload: state.String()}.String()

	case SignalStartSession:

		state, err := m.StartSession(ctx, logger, nk, dispatcher, state)
		if err != nil {
			return state, SignalResponse{Message: fmt.Sprintf("failed to start session: %v", err)}.String()
		}
		logger.Debug("Session started. %v", state)
		return state, SignalResponse{Success: true}.String()

	default:
		logger.Warn("Unknown signal: %v", signal.Signal)
		return state, SignalResponse{Success: false, Message: "unknown signal"}.String()
	}

	return state, SignalResponse{Success: true, Payload: state.String()}.String()

}

func (m *EvrMatch) StartSession(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *EvrMatchState) (*EvrMatchState, error) {
	channel := uuid.Nil
	if state.GroupID != nil {
		channel = *state.GroupID
	}
	state.Started = true
	state.StartTime = time.Now()
	entrants := make([]evr.EvrId, 0)
	message := evr.NewBroadcasterStartSession(state.ID.UUID(), channel, state.MaxSize, uint8(state.LobbyType), state.Broadcaster.AppId, state.Mode, state.Level, state.RequiredFeatures, entrants)
	logger.Info("Starting session. %v", message)
	messages := []evr.Message{
		message,
	}

	state.rebuildCache()

	if err := m.updateLabel(dispatcher, state); err != nil {
		logger.Error("failed to update label: %v", err)
	}

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
		Signal: signalID,
		Data:   dataJson,
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
	if err := dispatcher.BroadcastMessageDeferred(OpCodeEvrPacketData, bytes, presences, sender, true); err != nil {
		return fmt.Errorf("could not broadcast message: %v", err)
	}
	return nil
}

func (m *EvrMatch) updateLabel(dispatcher runtime.MatchDispatcher, state *EvrMatchState) error {

	if err := dispatcher.MatchLabelUpdate(state.GetLabel()); err != nil {
		return fmt.Errorf("could not update label: %v", err)
	}
	return nil
}

func checkIfGlobalDeveloper(ctx context.Context, nk runtime.NakamaModule, userID uuid.UUID) (bool, error) {
	return checkGroupMembershipByName(ctx, nk, userID, GroupGlobalDevelopers, SystemGroupLangTag)
}

func checkIfGlobalBot(ctx context.Context, nk runtime.NakamaModule, userID uuid.UUID) (bool, error) {
	return checkGroupMembershipByName(ctx, nk, userID, GroupGlobalBots, SystemGroupLangTag)
}

func checkIfGlobalModerator(ctx context.Context, nk runtime.NakamaModule, userID uuid.UUID) (bool, error) {
	// Developers are moderators
	ok, err := checkGroupMembershipByName(ctx, nk, userID, GroupGlobalDevelopers, SystemGroupLangTag)
	if err != nil {
		return false, fmt.Errorf("error getting user groups: %w", err)
	}
	if ok {
		return true, nil
	}
	return checkGroupMembershipByName(ctx, nk, userID, GroupGlobalModerators, SystemGroupLangTag)
}

func checkGroupMembershipByName(ctx context.Context, nk runtime.NakamaModule, userID uuid.UUID, groupName, langtag string) (bool, error) {
	groups, _, err := nk.UserGroupsList(ctx, userID.String(), 100, nil, "")
	if err != nil {
		return false, fmt.Errorf("error getting user groups: %w", err)
	}
	for _, g := range groups {
		if g.Group.LangTag == langtag && g.Group.Name == groupName {
			return true, nil
		}
	}
	return false, nil
}

// lobbyPlayerSessionsRequest is called when a client requests the player sessions for a list of EchoVR IDs.
func (m *EvrMatch) lobbyPlayerSessionsRequest(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *EvrMatchState, in runtime.MatchData, msg evr.Message) (*EvrMatchState, error) {
	message := msg.(*evr.LobbyPlayerSessionsRequest)

	if message.MatchSession != state.ID.UUID() {
		logger.Warn("lobbyPlayerSessionsRequest: match session %s does not match this one: %s", message.MatchSession, state.ID)
	}

	playerSession := uuid.Must(uuid.NewV4())
	teamIndex := evr.TeamUnassigned

	// Get the playerSession of the sender
	sessionID := uuid.FromStringOrNil(in.GetSessionId())
	sender, ok := state.presences[sessionID]
	if ok {
		playerSession = sender.PlayerSession
		teamIndex = sender.TeamIndex
	} else {
		logger.Warn("lobbyPlayerSessionsRequest: session ID %s not found in match", in.GetSessionId())

	}

	playerSessions := make([]uuid.UUID, 0)
	for _, e := range message.PlayerEvrIDs {
		for _, p := range state.presences {
			if p.GetEvrId() == e.String() {
				playerSessions = append(playerSessions, p.PlayerSession)
				break
			}
		}
	}

	if len(playerSessions) == 0 {
		logger.Warn("lobbyPlayerSessionsRequest: no player sessions found for %v", message.PlayerEvrIDs)
	}

	success := evr.NewLobbyPlayerSessionsSuccess(message.EvrID(), state.ID.UUID(), playerSession, playerSessions, int16(teamIndex))
	messages := []evr.Message{
		success.VersionU(),
		success.Version2(),
		success.Version3(),
	}
	if err := m.dispatchMessages(ctx, logger, dispatcher, messages, []runtime.Presence{in}, nil); err != nil {
		logger.Error("lobbyPlayerSessionsRequest: failed to dispatch message: %v", err)
	}

	return state, nil
}

// broadcasterPlayersAccept is called when the broadcaster has accepted or rejected player sessions.
func (m *EvrMatch) broadcasterPlayersAccept(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *EvrMatchState, in runtime.MatchData, msg evr.Message) (*EvrMatchState, error) {
	message := msg.(*evr.BroadcasterPlayersAccept)
	// validate the player sessions.

	accepted := make([]uuid.UUID, 0)
	rejected := make([]uuid.UUID, 0)

	for _, p := range message.PlayerSessions {
		found := false
		for _, presence := range state.presences {
			if presence.PlayerSession == p {
				accepted = append(accepted, p)
				found = true
				break
			}
		}
		if !found {
			rejected = append(rejected, p)
		}
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

	matchID := MatchIDFromContext(ctx)

	// Remove the player from the match.
	for _, presence := range state.presences {
		if presence.PlayerSession == message.PlayerSession {
			delete(state.presences, presence.SessionID)
			// Kick the presence from the match. This will trigger the MatchLeave function.
			nk.StreamUserKick(StreamModeMatchAuthoritative, matchID.String(), "", matchID.Node(), presence)
			logger.Debug("broadcasterPlayerRemoved: removing player presence from match: %v", message.PlayerSession)
			break
		}
	}

	// Remove the player from the internal presences to avoid the handler sending a message to the player
	return state, nil
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

func (m *EvrMatch) broadcasterPlayerSessionsLocked(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *EvrMatchState, in runtime.MatchData, msg evr.Message) (*EvrMatchState, error) {
	_ = msg.(*evr.BroadcasterPlayerSessionsLocked)
	// Verify that the update is coming from the broadcaster.
	state.Open = false
	return state, nil
}

func (m *EvrMatch) broadcasterPlayerSessionsUnlocked(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, dispatcher runtime.MatchDispatcher, state *EvrMatchState, in runtime.MatchData, msg evr.Message) (*EvrMatchState, error) {
	_ = msg.(*evr.BroadcasterPlayerSessionsUnlocked)
	// Verify that the update is coming from the broadcaster.
	state.Open = true
	return state, nil
}
