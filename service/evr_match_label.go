package service

import (
	"encoding/json"
	"errors"
	"fmt"

	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
)

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

type MatchLabel struct {
	ID             MatchID      `json:"id"`                   // The Session Id used by EVR (the same as match id)
	Open           bool         `json:"open"`                 // Whether the lobby is open to new players (Matching Only)
	LockedAt       time.Time    `json:"locked_at,omitempty"`  // The time the match was locked.
	LobbyType      LobbyType    `json:"lobby_type"`           // The type of lobby (Public, Private, Unassigned) (EVR)
	Mode           evr.Symbol   `json:"mode,omitempty"`       // The mode of the lobby (Arena, Combat, Social, etc.) (EVR)
	Level          evr.Symbol   `json:"level,omitempty"`      // The level to play on (EVR).
	Size           int          `json:"size"`                 // The number of players (including spectators) in the match.
	PlayerCount    int          `json:"player_count"`         // The number of participants (not including spectators) in the match.
	Players        []PlayerInfo `json:"players,omitempty"`    // The displayNames of the players (by team name) in the match.
	RankPercentile float64      `json:"rank_percentile"`      // The average percentile rank of the players in the match.
	RatingOrdinal  float64      `json:"rating_ordinal"`       // The average rating ordinal of the players in the match.
	GameState      *GameState   `json:"game_state,omitempty"` // The game state for the match.

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
	if !s.Open || !s.LockedAt.IsZero() {
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
	return evr.NewLobbySessionSuccess(s.Mode, s.ID.UUID, s.GetGroupID(), s.GameServer.Endpoint, int16(role), isPCVR, disableEncryption, disableMAC).Version5()
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
			OperatorID:  l.GameServer.OperatorID,
			GroupIDs:    l.GameServer.GroupIDs,
			VersionLock: l.GameServer.VersionLock,
			//RegionCodes: l.GameServer.RegionCodes,
			Tags:        l.GameServer.Tags,
			Features:    l.GameServer.Features,
			Region:      l.GameServer.Region,
			CountryCode: l.GameServer.CountryCode,
		},
		Players:        make([]PlayerInfo, 0),
		RankPercentile: l.RankPercentile,
		RatingOrdinal:  l.RatingOrdinal,
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
				Team:           l.Players[i].Team,
				DiscordID:      l.Players[i].DiscordID,
				PartyID:        l.Players[i].PartyID,
				JoinTime:       l.Players[i].JoinTime,
				RatingMu:       l.Players[i].RatingMu,
				RatingSigma:    l.Players[i].RatingSigma,
				RatingOrdinal:  l.Players[i].RatingOrdinal,
				RankPercentile: l.Players[i].RankPercentile,
				RatingScore:    l.Players[i].RatingScore,
			})
		}

	}
	return v
}
