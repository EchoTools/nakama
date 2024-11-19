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

type slotReservation struct {
	Presence *EvrMatchPresence
	Expiry   time.Time
}

type MatchLabel struct {
	ID             MatchID      `json:"id"`                        // The Session Id used by EVR (the same as match id)
	Open           bool         `json:"open"`                      // Whether the lobby is open to new players (Matching Only)
	LobbyType      LobbyType    `json:"lobby_type"`                // The type of lobby (Public, Private, Unassigned) (EVR)
	Mode           evr.Symbol   `json:"mode,omitempty"`            // The mode of the lobby (Arena, Combat, Social, etc.) (EVR)
	Level          evr.Symbol   `json:"level,omitempty"`           // The level to play on (EVR).
	Size           int          `json:"size"`                      // The number of players (including spectators) in the match.
	PlayerCount    int          `json:"player_count"`              // The number of participants (not including spectators) in the match.
	Players        []PlayerInfo `json:"players,omitempty"`         // The displayNames of the players (by team name) in the match.
	RankPercentile float64      `json:"rank_percentile,omitempty"` // The average percentile rank of the players in the match.
	GameState      *GameState   `json:"game_state,omitempty"`      // The game state for the match.

	TeamSize         int      `json:"team_size,omitempty"`    // The size of each team in arena/combat (either 4 or 5)
	MaxSize          int      `json:"limit,omitempty"`        // The total lobby size limit (players + specs)
	PlayerLimit      int      `json:"player_limit,omitempty"` // The number of players in the match (not including spectators).
	RequiredFeatures []string `json:"features,omitempty"`     // The required features for the match. map[feature][hmdtype]isRequired

	GroupID         *uuid.UUID                `json:"group_id,omitempty"`         // The channel id of the broadcaster. (EVR)
	SpawnedBy       string                    `json:"spawned_by,omitempty"`       // The userId of the player that spawned this match.
	StartTime       time.Time                 `json:"start_time,omitempty"`       // The time the match was, or will be started.
	CreatedAt       time.Time                 `json:"created_at,omitempty"`       // The time the match was created.
	Broadcaster     MatchBroadcaster          `json:"broadcaster,omitempty"`      // The broadcaster's data
	SessionSettings *evr.LobbySessionSettings `json:"session_settings,omitempty"` // The session settings for the match (EVR).
	TeamAlignments  map[string]int            `json:"team_alignments,omitempty"`  // map[userID]TeamIndex

	server         runtime.Presence             // The broadcaster's presence
	levelLoaded    bool                         // Whether the server has been sent the start instruction.
	presenceMap    map[string]*EvrMatchPresence // [sessionId]EvrMatchPresence
	reservationMap map[string]*slotReservation  // map[sessionID]slotReservation

	joinTimestamps       map[string]time.Time // The timestamps of when players joined the match. map[sessionId]time.Time
	joinTimeMilliseconds map[string]int64     // The round clock time of when players joined the match. map[sessionId]time.Time
	sessionStartExpiry   int64                // The tick count at which the match will be shut down if it has not started.
	tickRate             int64                // The number of ticks per second.
	emptyTicks           int64                // The number of ticks the match has been empty.
	terminateTick        int64                // The tick count at which the match will be shut down.
}

func (s *MatchLabel) LoadAndDeleteReservation(sessionID string) (*EvrMatchPresence, bool) {
	r, ok := s.reservationMap[sessionID]

	if !ok || r.Expiry.Before(time.Now()) {
		delete(s.reservationMap, sessionID)
		return nil, false
	}

	delete(s.reservationMap, sessionID)
	s.rebuildCache()

	return r.Presence, true
}

func (s *MatchLabel) IsPublic() bool {
	return s.LobbyType == PublicLobby
}

func (s *MatchLabel) IsPrivate() bool {
	return s.LobbyType == PrivateLobby
}

func (s *MatchLabel) IsSocial() bool {
	return s.Mode == evr.ModeSocialPublic || s.Mode == evr.ModeSocialPrivate
}

func (s *MatchLabel) IsArena() bool {
	return s.Mode == evr.ModeArenaPublic || s.Mode == evr.ModeArenaPrivate
}

func (s *MatchLabel) IsCombat() bool {
	return s.Mode == evr.ModeCombatPublic || s.Mode == evr.ModeCombatPrivate
}

func (s *MatchLabel) IsMatch() bool {
	return s.IsArena() || s.IsCombat()
}

func (s *MatchLabel) IsPrivateMatch() bool {
	return s.Mode == evr.ModeArenaPrivate || s.Mode == evr.ModeCombatPrivate
}

func (s *MatchLabel) IsPublicMatch() bool {
	return s.Mode == evr.ModeArenaPublic || s.Mode == evr.ModeCombatPublic
}

func (s *MatchLabel) GetPlayerCount() int {
	count := 0
	for _, p := range s.Players {
		if int(p.Team) != evr.TeamSpectator && int(p.Team) != evr.TeamModerator {
			count++
		}
	}
	return count
}

func (s *MatchLabel) GetNonPlayerCount() int {
	count := 0
	for _, p := range s.Players {
		if int(p.Team) == evr.TeamSpectator || int(p.Team) == evr.TeamModerator {
			count++
		}
	}
	return count
}

func (s *MatchLabel) GetPlayerByEvrID(evrID evr.EvrId) *PlayerInfo {
	for _, p := range s.Players {
		if p.EvrID == evrID {
			return &p
		}
	}
	return nil
}

func (s *MatchLabel) OpenPlayerSlots() int {
	return s.PlayerLimit - s.GetPlayerCount()
}

func (s *MatchLabel) OpenNonPlayerSlots() int {
	return int(s.MaxSize) - s.PlayerLimit - s.GetNonPlayerCount()
}

func (s *MatchLabel) OpenSlots() int {
	return int(s.MaxSize) - s.Size
}

func (s *MatchLabel) OpenSlotsByRole(role int) (int, error) {
	if evr.RolesByMode[s.Mode] == nil {
		return 0, fmt.Errorf("mode %s is not a valid mode", s.Mode)
	}

	return s.roleLimit(role) - s.RoleCount(role), nil
}

func (s *MatchLabel) String() string {
	return s.GetLabel()
}

func (s *MatchLabel) roleLimit(role int) int {

	switch s.Mode {

	case evr.ModeArenaPublic, evr.ModeCombatPublic:

		switch role {
		case evr.TeamBlue, evr.TeamOrange:

			return s.TeamSize

		case evr.TeamSpectator, evr.TeamModerator:

			return s.MaxSize - s.PlayerLimit
		}

	default:
		return s.PlayerLimit
	}

	return 0
}

func (s *MatchLabel) RoleCount(role int) int {
	count := 0
	for _, p := range s.Players {
		if p.Team == TeamIndex(role) {
			count++
		}
	}
	return count
}

func (s *MatchLabel) Started() bool {
	return !s.StartTime.IsZero() && time.Now().After(s.StartTime)
}

func (s *MatchLabel) GetLabel() string {
	labelJson, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(labelJson)
}

func (s *MatchLabel) GetLabelIndented() string {
	labelJson, err := json.MarshalIndent(s, "", "  ")
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

func (s *MatchLabel) GetEntrantConnectMessage(role int, isPCVR bool, disableEncryption bool, disableMAC bool) *evr.LobbySessionSuccessv5 {
	return evr.NewLobbySessionSuccess(s.Mode, s.ID.UUID, s.GetGroupID(), s.Broadcaster.Endpoint, int16(role), isPCVR, disableEncryption, disableMAC).Version5()
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
	presences := make([]*EvrMatchPresence, 0, len(s.presenceMap))
	for _, p := range s.presenceMap {
		presences = append(presences, p)
	}

	// Include the reservations in the cache.
	for id, r := range s.reservationMap {
		if r.Expiry.Before(time.Now()) {
			delete(s.reservationMap, id)
			continue
		}
		// Include the reservation in the cache.
		presences = append(presences, r.Presence)
	}

	// Rebuild the lookup tables.
	s.Size = len(presences)
	s.Players = make([]PlayerInfo, 0, s.Size)
	s.PlayerCount = 0

	for _, p := range presences {
		// Do not include spectators or moderators in player count
		if p.RoleAlignment != evr.TeamSpectator && p.RoleAlignment != evr.TeamModerator {
			s.PlayerCount++
		}
		p.RankPercentile = 0.0

		if p.RoleAlignment == evr.TeamSpectator {
			s.Players = append(s.Players, PlayerInfo{
				UserID:      p.UserID.String(),
				Username:    p.Username,
				DisplayName: p.DisplayName,
				EvrID:       p.EvrID,
				Team:        TeamIndex(p.RoleAlignment),
				ClientIP:    p.ClientIP,
				DiscordID:   p.DiscordID,
				JoinTime:    s.joinTimeMilliseconds[p.SessionID.String()],
			})
		} else {

			switch s.Mode {
			case evr.ModeArenaPublic, evr.ModeCombatPublic:
				s.Players = append(s.Players, PlayerInfo{
					UserID:         p.UserID.String(),
					Username:       p.Username,
					DisplayName:    p.DisplayName,
					EvrID:          p.EvrID,
					Team:           TeamIndex(p.RoleAlignment),
					ClientIP:       p.ClientIP,
					DiscordID:      p.DiscordID,
					PartyID:        p.PartyID.String(),
					JoinTime:       s.joinTimeMilliseconds[p.SessionID.String()],
					RatingMu:       p.Rating.Mu,
					RatingSigma:    p.Rating.Sigma,
					RankPercentile: p.RankPercentile,
					Query:          p.Query,
					IsReservation:  s.reservationMap[p.SessionID.String()] != nil,
				})

			case evr.ModeArenaPrivate, evr.ModeCombatPrivate:
				s.Players = append(s.Players, PlayerInfo{
					UserID:        p.UserID.String(),
					Username:      p.Username,
					DisplayName:   p.DisplayName,
					EvrID:         p.EvrID,
					Team:          AnyTeam, // Roles are not tracked in private matches.
					ClientIP:      p.ClientIP,
					DiscordID:     p.DiscordID,
					PartyID:       p.PartyID.String(),
					IsReservation: s.reservationMap[p.SessionID.String()] != nil,
				})
			case evr.ModeSocialPublic, evr.ModeSocialPrivate:
				s.Players = append(s.Players, PlayerInfo{
					UserID:        p.UserID.String(),
					Username:      p.Username,
					DisplayName:   p.DisplayName,
					EvrID:         p.EvrID,
					Team:          TeamIndex(p.RoleAlignment),
					ClientIP:      p.ClientIP,
					DiscordID:     p.DiscordID,
					PartyID:       p.PartyID.String(),
					IsReservation: s.reservationMap[p.SessionID.String()] != nil,
				})
			default:
				s.Players = append(s.Players, PlayerInfo{
					UserID:        p.UserID.String(),
					Username:      p.Username,
					DisplayName:   p.DisplayName,
					EvrID:         p.EvrID,
					Team:          TeamIndex(p.RoleAlignment),
					ClientIP:      p.ClientIP,
					DiscordID:     p.DiscordID,
					PartyID:       p.PartyID.String(),
					IsReservation: s.reservationMap[p.SessionID.String()] != nil,
				})
			}
		}

		switch s.Mode {
		case evr.ModeArenaPublic:

			teams := make(map[TeamIndex]RatedTeam, 2)
			for _, p := range s.Players {
				teams[p.Team] = append(teams[p.Team], p.Rating())
			}

			meta := make(map[TeamIndex]TeamMetadata, 2)
			for _, t := range []TeamIndex{BlueTeam, OrangeTeam} {
				meta[t] = TeamMetadata{
					Strength: teams[t].Strength(),
				}
			}

			if s.GameState == nil {
				s.GameState = &GameState{}
			}
			s.GameState.Teams = meta
		}
	}
	// Sort the players by team, party ID, and join time.
	sort.SliceStable(s.Players, func(i, j int) bool {
		// by team
		if s.Players[i].Team < s.Players[j].Team {
			return true
		}
		if s.Players[i].Team > s.Players[j].Team {
			return false
		}

		// by party ID
		if s.Players[i].PartyID < s.Players[j].PartyID {
			return true
		}
		if s.Players[i].PartyID > s.Players[j].PartyID {
			return false
		}

		// by join time
		return s.Players[i].JoinTime < s.Players[j].JoinTime
	})

	// Recalculate the match's aggregate rank percentile
	s.RankPercentile = 0.0

	count := 0
	if len(s.Players) > 0 {
		for _, p := range s.Players {
			if (p.Team != BlueTeam && p.Team != OrangeTeam) || p.RankPercentile > 0 {
				continue
			}
			count++
			s.RankPercentile += p.RankPercentile
		}
		s.RankPercentile = s.RankPercentile / float64(count)
	}
}

func (l *MatchLabel) PublicView() *MatchLabel {
	// Remove private data
	var gs *GameState
	if l.GameState != nil {
		gs = &GameState{
			BlueScore:   l.GameState.BlueScore,
			OrangeScore: l.GameState.OrangeScore,
			Teams:       l.GameState.Teams,
		}
		if l.GameState.RoundClock != nil {
			gs.RoundClock = l.GameState.RoundClock.LatestAsNewClock()
		}
	}
	v := &MatchLabel{
		LobbyType:        l.LobbyType,
		ID:               l.ID,
		Open:             l.Open,
		GameState:        gs,
		StartTime:        l.StartTime,
		CreatedAt:        l.CreatedAt,
		GroupID:          l.GroupID,
		SpawnedBy:        l.SpawnedBy,
		Mode:             l.Mode,
		Level:            l.Level,
		RequiredFeatures: l.RequiredFeatures,
		MaxSize:          l.MaxSize,
		Size:             l.Size,
		PlayerCount:      l.PlayerCount,
		PlayerLimit:      l.PlayerLimit,
		TeamSize:         l.TeamSize,
		Broadcaster: MatchBroadcaster{
			OperatorID:  l.Broadcaster.OperatorID,
			GroupIDs:    l.Broadcaster.GroupIDs,
			VersionLock: l.Broadcaster.VersionLock,
			Regions:     l.Broadcaster.Regions,
			Tags:        l.Broadcaster.Tags,
			Features:    l.Broadcaster.Features,
		},
		Players:        make([]PlayerInfo, 0),
		RankPercentile: l.RankPercentile,
	}
	if l.LobbyType == PrivateLobby || l.LobbyType == UnassignedLobby {
		// Set the last bytes to FF to hide the ID
		for i := 12; i < 16; i++ {
			v.ID.UUID[i] = 0xFF
		}
	} else {
		for i := range l.Players {
			v.Players = append(v.Players, PlayerInfo{
				IsReservation:  l.Players[i].IsReservation,
				UserID:         l.Players[i].UserID,
				Username:       l.Players[i].Username,
				DisplayName:    l.Players[i].DisplayName,
				EvrID:          l.Players[i].EvrID,
				Team:           l.Players[i].Team,
				DiscordID:      l.Players[i].DiscordID,
				PartyID:        l.Players[i].PartyID,
				JoinTime:       l.Players[i].JoinTime,
				RatingMu:       l.Players[i].RatingMu,
				RatingSigma:    l.Players[i].RatingSigma,
				RankPercentile: l.Players[i].RankPercentile,
				Query:          l.Players[i].Query,
			})
		}

	}
	return v
}
