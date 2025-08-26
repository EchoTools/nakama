package service

import (
	"fmt"
	"sort"
	"strings"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
)

type Role = RoleIndex

type LobbySlotReservation struct {
	*LobbyPresence    `json:"presence"`
	ReservationExpiry time.Time `json:"reservation_expiry"`
}

type LobbyMetadata struct {
	Visibility        LobbyType  `json:"visibility"`
	Mode              evr.Symbol `json:"mode"`
	Level             evr.Symbol `json:"level"`
	MaxSize           int        `json:"max_size"`
	PlayerLimit       int        `json:"player_limit"`
	TeamSize          int        `json:"team_size"`
	SpawnedBy         uuid.UUID  `json:"spawned_by"`
	GroupID           uuid.UUID  `json:"group_id"`
	RequiredFeatures  []string   `json:"required_features"`
	ReservationExpiry time.Time  `json:"reservation_expiry"`
	ScheduledTime     time.Time  `json:"scheduled_time"`
}

// LobbySessionState holds the state of a match.
type LobbySessionState struct {
	ID                 MatchID                             `json:"id"`                   // The match ID
	Metadata           *LobbyMetadata                      `json:"settings"`             // The match settings
	CreateTime         time.Time                           `json:"create_time"`          // The time the match was created
	StartTime          time.Time                           `json:"start_time"`           // The time the match started
	LockTime           time.Time                           `json:"lock_time"`            // The time the match was locked (no new players can join)
	IsLoaded           bool                                `json:"is_loaded"`            // Whether the server has loaded the level
	Server             *GameServerPresence                 `json:"server"`               // The game server's presence
	Presences          map[uuid.UUID]*LobbyPresence        `json:"presences"`            // map[sessionID]LobbyPresence
	Reservations       map[uuid.UUID]*LobbySlotReservation `json:"reservations"`         // map[sessionID]LobbySlotReservation
	Alignments         map[uuid.UUID]Role                  `json:"team_alignments"`      // map[sessionID]TeamIndex
	JoinTimes          map[uuid.UUID]time.Time             `json:"join_times"`           // map[sessionID]time.Time
	SessionStartExpiry int64                               `json:"session_start_expiry"` // The tick count at which the match will be shut down if it has not started.
	TickRate           int64                               `json:"handler_tick_rate"`    // The number of ticks per second.
	EmptyTicks         int64                               `json:"empty_ticks"`          // The number of ticks the match has been empty.
	TerminateTick      int64                               `json:"terminate_tick"`       // The tick count at which the match will be shut down.
	Goals              []*evr.MatchGoal                    `json:"goals"`                // The goals scored in the match.
}

func (s *LobbySessionState) Mode() evr.Symbol {
	return s.Metadata.Mode
}

func (s *LobbySessionState) Level() evr.Symbol {
	return s.Metadata.Level
}

func (s *LobbySessionState) Visibility() LobbyType {
	return s.Metadata.Visibility
}

func (s *LobbySessionState) RequiredFeatures() []string {
	return s.Metadata.RequiredFeatures
}
func (s *LobbySessionState) TeamAlignments() map[uuid.UUID]Role {
	return s.Alignments
}

func (s *LobbySessionState) GroupID() string {
	return s.Metadata.GroupID.String()
}

func (s *LobbySessionState) MaxSize() int {
	return s.Metadata.MaxSize
}

func (s *LobbySessionState) PlayerLimit() int {
	return s.Metadata.PlayerLimit
}

func (s *LobbySessionState) TeamSize() int {
	return s.Metadata.TeamSize
}

func (s *LobbySessionState) IsStarted() bool {
	return !s.StartTime.IsZero() && time.Now().After(s.StartTime)
}

func (s *LobbySessionState) IsOpen() bool {
	return s.LockTime.IsZero() || time.Now().Before(s.LockTime)
}

func (s *LobbySessionState) SetLocked() {
	s.LockTime = time.Now()
}

func (s *LobbySessionState) SlotPresences() []*LobbyPresence {
	presences := make([]*LobbyPresence, 0, len(s.Presences)+len(s.Reservations))
	for _, p := range s.Presences {
		presences = append(presences, p)
	}
	// Include the reservations
	for _, r := range s.Reservations {
		presences = append(presences, r.LobbyPresence)
	}
	return presences
}

// PlayerCount is the total number of players excluding spectators and moderators.
func (s *LobbySessionState) NonPlayers() []*LobbyPresence {
	nonPlayers := make([]*LobbyPresence, 0)
	for _, p := range s.Presences {
		if p.RoleAlignment == Spectator || p.RoleAlignment == Moderator {
			nonPlayers = append(nonPlayers, p)
		}
	}
	return nonPlayers
}

// Size is the total number of players in the match, including reservations.
func (s *LobbySessionState) Size() int {
	return len(s.Presences) + len(s.Reservations)
}

func (s *LobbySessionState) Players() []PlayerInfo {
	presences := s.Presences
	ratingScores := s.caculateRatingWeights()

	players := make([]PlayerInfo, 0, len(presences))
	for _, p := range presences {
		// Do not include spectators or moderators in player count
		if p.RoleAlignment == Spectator {
			players = append(players, PlayerInfo{
				UserID:      p.UserID.String(),
				Username:    p.Username,
				DisplayName: p.DisplayName,
				XPID:        p.XPID,
				Role:        p.RoleAlignment,
				ClientIP:    p.ClientIP,
				DiscordID:   p.DiscordID,
				SessionID:   p.SessionID.String(),
				JoinTime:    p.JoinTime,
				GeoHash:     p.GeoHash,
			})
		} else {
			players = append(players, PlayerInfo{
				UserID:        p.UserID.String(),
				Username:      p.Username,
				DisplayName:   p.DisplayName,
				XPID:          p.XPID,
				Role:          p.RoleAlignment,
				ClientIP:      p.ClientIP,
				DiscordID:     p.DiscordID,
				PartyID:       p.PartyID.String(),
				RatingMu:      p.Rating.Mu,
				RatingSigma:   p.Rating.Sigma,
				RatingOrdinal: rating.Ordinal(p.Rating),
				RatingScore:   ratingScores[p.XPID],
				JoinTime:      p.JoinTime,
				SessionID:     p.SessionID.String(),
				IsReservation: s.Reservations[p.SessionID] != nil,
				GeoHash:       p.GeoHash,
				PingMillis:    p.PingMillis,
			})
		}
	}
	// Sort the players by team, party ID, and join time.
	sort.SliceStable(players, func(i, j int) bool {
		// by team
		if players[i].Role < players[j].Role {
			return true
		}
		if players[i].Role > players[j].Role {
			return false
		}

		// by party ID
		if players[i].PartyID < players[j].PartyID {
			return true
		}
		if players[i].PartyID > players[j].PartyID {
			return false
		}

		// by join time
		return players[i].JoinTime.Before(players[j].JoinTime)
	})
	return players
}

func (s *LobbySessionState) LoadAndDeleteReservation(sessionID uuid.UUID) (*LobbyPresence, bool) {
	r, ok := s.Reservations[sessionID]

	if !ok || r.ReservationExpiry.Before(time.Now()) {
		delete(s.Reservations, sessionID)
		return nil, false
	}
	delete(s.Reservations, sessionID)
	return r.LobbyPresence, true
}

func (l *LobbySessionState) caculateRatingWeights() map[evr.XPID]int {
	// Calculate the weight of each player's rating in the match
	byPlayer := make(map[evr.XPID]int)
	for _, g := range l.Goals {
		byPlayer[g.XPID] += g.PointsValue // Shooter gets the points
		if !g.PrevPlayerXPID.IsNil() && g.PrevPlayerXPID != g.XPID {
			byPlayer[g.PrevPlayerXPID] += g.PointsValue - 1 // Assist gets the points - 1
		}
	}
	return byPlayer
}

func (s *LobbySessionState) MetricsTags() map[string]string {

	tags := map[string]string{
		"mode":        s.Metadata.Mode.String(),
		"level":       s.Metadata.Level.String(),
		"type":        s.Metadata.Visibility.String(),
		"group_id":    s.Metadata.GroupID.String(),
		"operator_id": s.Server.GetUserId(),
	}

	if !s.Server.SessionID.IsNil() {
		tags["operator_username"] = strings.TrimPrefix("broadcaster:", s.Server.GetUsername())
	}
	return tags
}

func (s *LobbySessionState) GameState() *GameState {
	gameState := &GameState{}
	switch s.Metadata.Mode {
	case evr.ModeArenaPublic:
		teamRatings := make([]types.Team, 2)
		teams := make(map[Role]RatedTeam, 2)
		for _, p := range s.Presences {
			// Skip non-players
			if p.RoleAlignment != BlueTeam && p.RoleAlignment != OrangeTeam {
				continue
			}
			teams[p.RoleAlignment] = append(teams[p.RoleAlignment], p.Rating)
			teamRatings[p.RoleAlignment] = append(teamRatings[p.RoleAlignment], p.Rating)
		}

		winPredictions := rating.PredictWin(teamRatings, nil)
		meta := make(map[Role]TeamMetadata, 2)
		for _, t := range [...]Role{BlueTeam, OrangeTeam} {
			meta[t] = TeamMetadata{
				Strength:   teams[t].Strength(),
				PredictWin: winPredictions[t],
			}
		}
		gameState.Teams = meta
	}
	return gameState
}

func (s *LobbySessionState) Label() *MatchLabel {
	players := s.Players()
	ratingOrdinal := calculateMedianRating(players)
	nonPlayerCount := len(s.NonPlayers())
	playerCount := s.Size() - nonPlayerCount
	return &MatchLabel{
		ID:               s.ID,
		Open:             s.IsOpen(),
		LockedAt:         s.LockTime,
		LobbyType:        s.Metadata.Visibility,
		Mode:             s.Metadata.Mode,
		Level:            s.Metadata.Level,
		Size:             playerCount + nonPlayerCount,
		PlayerCount:      playerCount,
		Players:          players,
		RatingOrdinal:    ratingOrdinal,
		GameState:        s.GameState(),
		TeamSize:         s.Metadata.TeamSize,
		MaxSize:          s.Metadata.MaxSize,
		PlayerLimit:      s.Metadata.PlayerLimit,
		RequiredFeatures: s.Metadata.RequiredFeatures,
		GroupID:          &s.Metadata.GroupID,
		SpawnedBy:        s.Metadata.SpawnedBy.String(),
		StartTime:        s.StartTime,
		CreatedAt:        s.CreateTime,
		GameServer:       s.Server,
		TeamAlignments:   s.Alignments,
	}
}

func (s *LobbySessionState) GetPlayerByXPID(evrID evr.XPID) *LobbyPresence {
	for _, p := range s.Presences {
		if p.XPID == evrID {
			return p
		}
	}
	return nil
}

func (s *LobbySessionState) GetPresenceByUserID(userID string) *LobbyPresence {
	for _, p := range s.Presences {
		if p.UserID.String() == userID {
			return p
		}
	}
	return nil
}

func (s *LobbySessionState) GetPlayerCount() int {
	count := 0
	for _, p := range s.Presences {
		if p.RoleAlignment != Spectator && p.RoleAlignment != Moderator {
			count++
		}
	}
	return count
}

func (s *LobbySessionState) GetNonPlayerCount() int {
	count := 0
	for _, p := range s.Presences {
		if p.RoleAlignment == Spectator || p.RoleAlignment == Moderator {
			count++
		}
	}
	return count
}
func (s *LobbySessionState) OpenPlayerSlots() int {
	return s.Metadata.PlayerLimit - s.GetPlayerCount()
}

func (s *LobbySessionState) OpenNonPlayerSlots() int {
	return s.Metadata.MaxSize - s.Metadata.PlayerLimit - s.GetNonPlayerCount()
}

func (s *LobbySessionState) OpenSlots() int {
	return s.Metadata.MaxSize - s.Size()
}

func (s *LobbySessionState) OpenSlotsByRole(role Role) (int, error) {
	if evr.RolesByMode[s.Metadata.Mode] == nil {
		return 0, fmt.Errorf("mode %s is not a valid mode", s.Metadata.Mode)
	}

	return s.roleLimit(role) - s.RoleCount(role), nil
}

func (s *LobbySessionState) roleLimit(role Role) int {
	switch s.Metadata.Mode {
	case evr.ModeArenaPublic, evr.ModeCombatPublic:
		switch role {
		case BlueTeam, OrangeTeam:
			return s.Metadata.TeamSize
		case Spectator, Moderator:
			return s.Metadata.MaxSize - s.Metadata.PlayerLimit
		}
	default:
		return s.Metadata.PlayerLimit
	}
	return 0
}

func (s *LobbySessionState) RoleCount(role Role) int {
	count := 0
	for _, p := range s.Presences {
		if p.RoleAlignment == role {
			count++
		}
	}
	return count
}

// calculateMedianRating calculates the median rating ordinal of all competitor players in the match.
func calculateMedianRating(players []PlayerInfo) float64 {
	var ordinals []float64
	for _, p := range players {
		if p.RatingOrdinal != 0 && p.IsCompetitor() {
			ordinals = append(ordinals, p.RatingOrdinal)
		}
	}
	if len(ordinals) == 0 {
		return 0
	}
	sort.Float64s(ordinals)
	mid := len(ordinals) / 2
	if len(ordinals)%2 == 0 {
		return (ordinals[mid-1] + ordinals[mid]) / 2
	}
	return ordinals[mid]
}
