package server

import (
	"encoding/json"
	"fmt"
	"sort"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

type MatchLabel struct {
	ID          MatchID          `json:"id"`                    // The Session Id used by EVR (the same as match id)
	Open        bool             `json:"open"`                  // Whether the lobby is open to new players (Matching Only)
	LobbyType   LobbyType        `json:"lobby_type"`            // The type of lobby (Public, Private, Unassigned) (EVR)
	Broadcaster MatchBroadcaster `json:"broadcaster,omitempty"` // The broadcaster's data
	Started     bool             `json:"started"`               // Whether the match has started.
	StartTime   time.Time        `json:"start_time"`            // The time the match was started.
	SpawnedBy   string           `json:"spawned_by,omitempty"`  // The userId of the player that spawned this match.
	GroupID     *uuid.UUID       `json:"group_id,omitempty"`    // The channel id of the broadcaster. (EVR)
	GuildID     string           `json:"guild_id,omitempty"`    // The guild id of the broadcaster. (EVR)
	GuildName   string           `json:"guild_name,omitempty"`  // The guild name of the broadcaster. (EVR)

	Mode             evr.Symbol           `json:"mode,omitempty"`             // The mode of the lobby (Arena, Combat, Social, etc.) (EVR)
	Level            evr.Symbol           `json:"level,omitempty"`            // The level to play on (EVR).
	SessionSettings  *evr.SessionSettings `json:"session_settings,omitempty"` // The session settings for the match (EVR).
	RequiredFeatures []string             `json:"features,omitempty"`         // The required features for the match. map[feature][hmdtype]isRequired

	MaxSize     uint8     `json:"limit,omitempty"`        // The total lobby size limit (players + specs)
	Size        int       `json:"size"`                   // The number of players (including spectators) in the match.
	PlayerCount int       `json:"player_count"`           // The number of participants (not including spectators) in the match.
	PlayerLimit int       `json:"player_limit,omitempty"` // The number of players in the match (not including spectators).
	TeamSize    int       `json:"team_size,omitempty"`    // The size of each team in arena/combat (either 4 or 5)
	TeamIndex   TeamIndex `json:"team,omitempty"`         // What team index a player prefers (Used by Matching only)

	Players        []PlayerInfo                 `json:"players,omitempty"`         // The displayNames of the players (by team name) in the match.
	TeamAlignments map[string]int               `json:"team_alignments,omitempty"` // map[userID]TeamIndex
	presenceMap    map[string]*EvrMatchPresence // [sessionId]EvrMatchPresence
	broadcaster    runtime.Presence             // The broadcaster's presence

	emptyTicks            int64                // The number of ticks the match has been empty.
	sessionStartExpiry    int64                // The tick count at which the match will be shut down if it has not started.
	broadcasterJoinExpiry int64                // The tick count at which the match will be shut down if the broadcaster has not joined.
	tickRate              int64                // The number of ticks per second.
	joinTimestamps        map[string]time.Time // The timestamps of when players joined the match. map[sessionId]time.Time
	terminateTick         int64                // The tick count at which the match will be shut down.
}

// NewMatchLabel is a helper function to create a new match state. It returns the state, params, label json, and err.
func NewMatchLabel(endpoint evr.Endpoint, config *MatchBroadcaster) (state *MatchLabel, params map[string]interface{}, configPayload string, err error) {

	var tickRate int64 = 10 // 10 ticks per second

	initialState := MatchLabel{
		Broadcaster:      *config,
		Open:             false,
		LobbyType:        UnassignedLobby,
		Mode:             evr.ModeUnloaded,
		Level:            evr.LevelUnloaded,
		RequiredFeatures: make([]string, 0),
		Players:          make([]PlayerInfo, 0, MatchMaxSize),
		presenceMap:      make(map[string]*EvrMatchPresence, MatchMaxSize),

		TeamAlignments: make(map[string]int, MatchMaxSize),

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

func (s *MatchLabel) GetPlayerCount() int {
	return len(s.presenceMap)
}

func (s *MatchLabel) OpenPlayerSlots() int {
	return s.PlayerLimit - s.PlayerCount
}

func (s *MatchLabel) OpenNonPlayerSlots() int {
	return int(s.MaxSize) - s.PlayerLimit
}

func (s *MatchLabel) OpenSlots() int {
	return int(s.MaxSize) - s.Size
}

func (s *MatchLabel) String() string {
	return s.GetLabel()
}

func (s *MatchLabel) RoleCount(role int) int {
	count := 0
	for _, p := range s.presenceMap {
		if p.RoleAlignment == role {
			count++
		}
	}
	return count
}

func (s *MatchLabel) GetLabel() string {
	labelJson, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(labelJson)
}

func (s *MatchLabel) GetGroupID() uuid.UUID {
	if s.GroupID == nil {
		return uuid.Nil
	}
	return *s.GroupID
}

func (s *MatchLabel) GetEndpoint() evr.Endpoint {
	return s.Broadcaster.Endpoint
}

func (s *MatchLabel) IsPriorityForMode() bool {
	tag := "priority_mode_" + s.Mode.String()
	for _, t := range s.Broadcaster.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

func (s *MatchLabel) PublicView() *MatchLabel {
	ps := MatchLabel{
		LobbyType:        s.LobbyType,
		ID:               s.ID,
		Open:             s.Open,
		Started:          s.Started,
		StartTime:        s.StartTime,
		GroupID:          s.GroupID,
		GuildID:          s.GuildID,
		SpawnedBy:        s.SpawnedBy,
		Mode:             s.Mode,
		Level:            s.Level,
		RequiredFeatures: s.RequiredFeatures,
		MaxSize:          s.MaxSize,
		Size:             s.Size,
		PlayerCount:      s.PlayerCount,
		PlayerLimit:      s.PlayerLimit,
		TeamSize:         s.TeamSize,
		Broadcaster: MatchBroadcaster{
			OperatorID:  s.Broadcaster.OperatorID,
			GroupIDs:    s.Broadcaster.GroupIDs,
			VersionLock: s.Broadcaster.VersionLock,
			Regions:     s.Broadcaster.Regions,
			Tags:        s.Broadcaster.Tags,
		},
		Players: make([]PlayerInfo, 0),
	}
	if ps.LobbyType == PrivateLobby || ps.LobbyType == UnassignedLobby {
		ps.ID = MatchID{}
		ps.SpawnedBy = ""
	} else {
		for i := range s.Players {
			ps.Players = append(ps.Players, PlayerInfo{
				UserID:      s.Players[i].UserID,
				Username:    s.Players[i].Username,
				DisplayName: s.Players[i].DisplayName,
				EvrID:       s.Players[i].EvrID,
				Team:        s.Players[i].Team,
				DiscordID:   s.Players[i].DiscordID,
				PartyID:     s.Players[i].PartyID,
			})
		}

	}
	return &ps
}

func (s *MatchLabel) MetricsTags() map[string]string {
	return map[string]string{
		"mode":     s.Mode.String(),
		"level":    s.Level.String(),
		"type":     s.LobbyType.String(),
		"group_id": s.GetGroupID().String(),
	}
}

// rebuildCache is called after the presences map is updated.
func (s *MatchLabel) rebuildCache() {
	// Rebuild the lookup tables.

	s.Players = make([]PlayerInfo, 0, len(s.presenceMap))
	s.Size = s.GetPlayerCount()
	s.PlayerCount = 0
	// Construct Player list
	for _, presence := range s.presenceMap {
		// Do not include spectators or moderators in player count
		if presence.RoleAlignment != evr.TeamSpectator && presence.RoleAlignment != evr.TeamModerator {
			s.PlayerCount++
		}

		playerinfo := PlayerInfo{
			UserID:      presence.UserID.String(),
			Username:    presence.Username,
			DisplayName: presence.DisplayName,
			EvrID:       presence.EvrID,
			Team:        TeamIndex(presence.RoleAlignment),
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
