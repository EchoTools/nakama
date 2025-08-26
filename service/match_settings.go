package service

import (
	"time"

	evr "github.com/echotools/nakama/v3/protocol"

	"github.com/gofrs/uuid/v5"
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
	EVRLobbySessionMatchModule = "nevrlobby"
)

type MatchSettings struct {
	Visibility        LobbyType               `json:"visibility"`
	Mode              evr.Symbol              `json:"mode"`
	Level             evr.Symbol              `json:"level"`
	TeamSize          int                     `json:"team_size"`
	MaxSize           int                     `json:"max_size"`
	PlayerLimit       int                     `json:"player_limit"`
	SpawnedBy         uuid.UUID               `json:"spawned_by"`
	GroupID           uuid.UUID               `json:"group_id"`
	RequiredFeatures  []string                `json:"required_features"`
	TeamAlignments    map[uuid.UUID]RoleIndex `json:"team_alignments"`
	Reservations      []*LobbyPresence        `json:"reservations"`
	ReservationExpiry time.Time               `json:"reservation_expiry"`
	ScheduledTime     time.Time               `json:"scheduled_time"`
}
