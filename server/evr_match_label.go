package server

import (
	"encoding/json"
	"fmt"
	"sort"

	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
)

type slotReservation struct {
	Presence *EvrMatchPresence
	Expiry   time.Time
}

type MatchLabel struct {
	ID          MatchID      `json:"id"`                   // The Session Id used by EVR (the same as match id)
	Open        bool         `json:"open"`                 // Whether the lobby is open to new players (Matching Only)
	LockedAt    *time.Time   `json:"locked_at,omitempty"`  // The time the match was locked.
	LobbyType   LobbyType    `json:"lobby_type"`           // The type of lobby (Public, Private, Unassigned) (EVR)
	Mode        evr.Symbol   `json:"mode,omitempty"`       // The mode of the lobby (Arena, Combat, Social, etc.) (EVR)
	Level       evr.Symbol   `json:"level,omitempty"`      // The level to play on (EVR).
	Size        int          `json:"size"`                 // The number of players (including spectators) in the match.
	PlayerCount int          `json:"player_count"`         // The number of participants (not including spectators) in the match.
	Players     []PlayerInfo `json:"players,omitempty"`    // The displayNames of the players (by team name) in the match.
	RatingMu    float64      `json:"rating_mu"`            // The average rating mu of the players in the match.
	GameState   *GameState   `json:"game_state,omitempty"` // The game state for the match.

	TeamSize         int      `json:"team_size,omitempty"`    // The size of each team in arena/combat (either 4 or 5)
	MaxSize          int      `json:"limit,omitempty"`        // The total lobby size limit (players + specs)
	PlayerLimit      int      `json:"player_limit,omitempty"` // The number of players in the match (not including spectators).
	RequiredFeatures []string `json:"features,omitempty"`     // The required features for the match.

	GroupID         *uuid.UUID                `json:"group_id,omitempty"`         // The channel id of the broadcaster. (EVR)
	SpawnedBy       string                    `json:"spawned_by,omitempty"`       // The userId of the player that spawned this match.
	StartTime       time.Time                 `json:"start_time,omitempty"`       // The time the match was, or will be started.
	CreatedAt       time.Time                 `json:"created_at,omitempty"`       // The time the match was created.
	GameServer      *GameServerPresence       `json:"broadcaster,omitempty"`      // The broadcaster's data
	SessionSettings *evr.LobbySessionSettings `json:"session_settings,omitempty"` // The session settings for the match (EVR).
	TeamAlignments  map[string]int            `json:"team_alignments,omitempty"`  // map[userID]TeamIndex
	OriginSocialID  *uuid.UUID                `json:"origin_social_id,omitempty"` // The social lobby ID this match originated from (for auto-rejoin)

	server          runtime.Presence                // The broadcaster's presence
	levelLoaded     bool                            // Whether the server has been sent the start instruction.
	presenceMap     map[string]*EvrMatchPresence    // [sessionId]EvrMatchPresence
	reservationMap  map[string]*slotReservation     // map[sessionID]slotReservation
	presenceByEvrID map[evr.EvrId]*EvrMatchPresence // map[evrID]EvrMatchPresence

	joinTimestamps       map[string]time.Time            // The timestamps of when players joined the match. map[sessionId]time.Time
	joinTimeMilliseconds map[string]int64                // The round clock time of when players joined the match. map[sessionId]time.Time
	participations       map[string]*PlayerParticipation // map[userID]*PlayerParticipation - tracks all players who ever joined
	tickRate             int64                           // The number of ticks per second.
	emptyTicks           int64                           // The number of ticks the match has been empty.
	terminateTick        int64                           // The tick count at which the match will be shut down.
	goals                []*evr.MatchGoal                // The goals scored in the match.
	matchSummarySent     bool                            // Whether the match summary has been sent.
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
	return s.Mode == evr.ModeArenaPrivate || s.Mode == evr.ModeCombatPrivate || s.Mode == evr.ModeSocialPrivate
}

func (s *MatchLabel) IsPublicMatch() bool {
	return s.Mode == evr.ModeArenaPublic || s.Mode == evr.ModeCombatPublic || s.Mode == evr.ModeSocialPublic
}

func (s *MatchLabel) IsLocked() bool {
	if !s.Open || (s.LockedAt != nil && !s.LockedAt.IsZero()) {
		return true
	}
	return false
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

func (s *MatchLabel) GetPlayerByUserID(userID string) *PlayerInfo {
	for _, p := range s.Players {
		if p.UserID == userID {
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
	return s.GetLabelIndented()
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
	return s.GameServer.Endpoint
}

func (s *MatchLabel) GetEntrantConnectMessage(role int, isPCVR bool, disableEncryption bool, disableMAC bool) *evr.LobbySessionSuccessv5 {
	return evr.NewLobbySessionSuccess(s.Mode, s.ID.UUID, s.GetGroupID(), s.GameServer.Endpoint, int16(role), disableEncryption, disableMAC).Version5()
}

func (s *MatchLabel) MetricsTags() map[string]string {

	tags := map[string]string{
		"mode":        s.Mode.String(),
		"level":       s.Level.String(),
		"type":        s.LobbyType.String(),
		"group_id":    s.GetGroupID().String(),
		"operator_id": s.GameServer.OperatorID.String(),
	}

	if s.server != nil {
		tags["operator_username"] = strings.TrimPrefix("broadcaster:", s.server.GetUsername())
	}
	return tags
}

func (s *MatchLabel) ratingMu() float64 {
	mus := make([]float64, 0, len(s.Players))
	for _, p := range s.Players {
		if p.RatingMu == 0 || !p.IsCompetitor() {
			continue
		}
		mus = append(mus, p.RatingMu)
	}
	if len(mus) == 0 {
		return 0.0
	}
	var sum float64
	for _, v := range mus {
		sum += v
	}
	return sum / float64(len(mus))

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

		if p.RoleAlignment == evr.TeamSpectator {
			s.Players = append(s.Players, PlayerInfo{
				UserID:      p.UserID.String(),
				Username:    p.Username,
				DisplayName: p.DisplayName,
				EvrID:       p.EvrID,
				Team:        TeamIndex(p.RoleAlignment),
				ClientIP:    p.ClientIP,
				DiscordID:   p.DiscordID,
				SessionID:   p.SessionID.String(),
				JoinTime:    s.joinTimeMilliseconds[p.SessionID.String()],
				GeoHash:     p.GeoHash,
			})
		} else {
			switch s.Mode {
			case evr.ModeArenaPublic:
				s.Players = append(s.Players, PlayerInfo{
					UserID:        p.UserID.String(),
					Username:      p.Username,
					DisplayName:   p.DisplayName,
					EvrID:         p.EvrID,
					Team:          TeamIndex(p.RoleAlignment),
					ClientIP:      p.ClientIP,
					DiscordID:     p.DiscordID,
					PartyID:       p.PartyID.String(),
					RatingMu:      p.Rating.Mu,
					RatingSigma:   p.Rating.Sigma,
					JoinTime:      s.joinTimeMilliseconds[p.SessionID.String()],
					SessionID:     p.SessionID.String(),
					IsReservation: s.reservationMap[p.SessionID.String()] != nil,
					GeoHash:       p.GeoHash,
					PingMillis:    p.PingMillis,
				})
			case evr.ModeCombatPublic, evr.ModeSocialPublic:
				s.Players = append(s.Players, PlayerInfo{
					UserID:        p.UserID.String(),
					Username:      p.Username,
					DisplayName:   p.DisplayName,
					EvrID:         p.EvrID,
					Team:          TeamIndex(p.RoleAlignment),
					ClientIP:      p.ClientIP,
					DiscordID:     p.DiscordID,
					PartyID:       p.PartyID.String(),
					JoinTime:      s.joinTimeMilliseconds[p.SessionID.String()],
					RatingMu:      p.Rating.Mu,
					RatingSigma:   p.Rating.Sigma,
					SessionID:     p.SessionID.String(),
					IsReservation: s.reservationMap[p.SessionID.String()] != nil,
					GeoHash:       p.GeoHash,
					PingMillis:    p.PingMillis,
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
					SessionID:     p.SessionID.String(),
					IsReservation: s.reservationMap[p.SessionID.String()] != nil,
					GeoHash:       p.GeoHash,
					PingMillis:    p.PingMillis,
				})
			case evr.ModeSocialPrivate:
				s.Players = append(s.Players, PlayerInfo{
					UserID:        p.UserID.String(),
					Username:      p.Username,
					DisplayName:   p.DisplayName,
					EvrID:         p.EvrID,
					Team:          TeamIndex(p.RoleAlignment),
					ClientIP:      p.ClientIP,
					DiscordID:     p.DiscordID,
					PartyID:       p.PartyID.String(),
					SessionID:     p.SessionID.String(),
					IsReservation: s.reservationMap[p.SessionID.String()] != nil,
					GeoHash:       p.GeoHash,
					PingMillis:    p.PingMillis,
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
					SessionID:     p.SessionID.String(),
					IsReservation: s.reservationMap[p.SessionID.String()] != nil,
					GeoHash:       p.GeoHash,
					PingMillis:    p.PingMillis,
				})
			}
		}
		if p.MatchmakingAt != nil {
			s.joinTimestamps[p.SessionID.String()] = *p.MatchmakingAt
		}
		switch s.Mode {
		case evr.ModeArenaPublic:
			teamRatings := make([]types.Team, 2)

			teams := make(map[TeamIndex]RatedTeam, 2)
			for _, p := range s.Players {
				if p.Team != BlueTeam && p.Team != OrangeTeam {
					continue
				}
				teams[p.Team] = append(teams[p.Team], p.Rating())
				teamRatings[p.Team] = append(teamRatings[p.Team], p.Rating())
			}

			winPredictions := rating.PredictWin(teamRatings, nil)
			meta := make(map[TeamIndex]TeamMetadata, 2)
			for _, t := range [...]TeamIndex{BlueTeam, OrangeTeam} {
				meta[t] = TeamMetadata{
					Strength:   teams[t].Strength(),
					PredictWin: winPredictions[t],
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

	s.RatingMu = s.ratingMu()
}

func (l *MatchLabel) CalculateRatingWeights() map[evr.EvrId]int {
	// Calculate the weight of each player's rating in the match
	byPlayer := make(map[evr.EvrId]int)
	byTeam := make(map[TeamIndex]int)
	for _, g := range l.goals {
		byPlayer[g.XPID] += g.PointsValue            // Shooter gets the points
		byTeam[TeamIndex(g.TeamID)] += g.PointsValue // Team gets the points
		if !g.PrevPlayerXPID.IsNil() && g.PrevPlayerXPID != g.XPID {
			byPlayer[g.PrevPlayerXPID] += g.PointsValue - 1 // Assist gets the points - 1
		}
	}

	winningTeam := BlueTeam
	if byTeam[BlueTeam] < byTeam[OrangeTeam] {
		winningTeam = OrangeTeam
	}

	for _, p := range l.Players {
		if p.Team == winningTeam {
			byPlayer[p.EvrID] += 4
		}
	}
	return byPlayer
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
		if l.GameState.SessionScoreboard != nil {
			gs.SessionScoreboard = l.GameState.SessionScoreboard.LatestAsNewScoreboard()
		}
	}

	v := &MatchLabel{
		LobbyType:        l.LobbyType,
		ID:               l.ID,
		Open:             l.Open,
		LockedAt:         l.LockedAt,
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
		GameServer: &GameServerPresence{
			OperatorID:    l.GameServer.OperatorID,
			GroupIDs:      l.GameServer.GroupIDs,
			VersionLock:   l.GameServer.VersionLock,
			DefaultRegion: l.GameServer.DefaultRegion,
			Tags:          l.GameServer.Tags,
			Features:      l.GameServer.Features,
			Region:        l.GameServer.Region,
			CountryCode:   l.GameServer.CountryCode,
		},
		Players:  make([]PlayerInfo, 0),
		RatingMu: l.RatingMu,
	}
	if l.LobbyType == PrivateLobby || l.LobbyType == UnassignedLobby {
		// Set the last bytes to FF to hide the ID
		for i := 12; i < 16; i++ {
			v.ID.UUID[i] = 0xFF
		}
	} else {
		for i := range l.Players {
			v.Players = append(v.Players, PlayerInfo{
				IsReservation: l.Players[i].IsReservation,
				UserID:        l.Players[i].UserID,
				Username:      l.Players[i].Username,
				DisplayName:   l.Players[i].DisplayName,
				//EvrID:          l.Players[i].EvrID,
				Team:        l.Players[i].Team,
				DiscordID:   l.Players[i].DiscordID,
				PartyID:     l.Players[i].PartyID,
				JoinTime:    l.Players[i].JoinTime,
				RatingMu:    l.Players[i].RatingMu,
				RatingSigma: l.Players[i].RatingSigma,
			})
		}

	}
	return v
}
