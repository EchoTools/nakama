package service

import (
	"encoding/json"
	"math/rand"
	"time"
)

const (
	OpCodeBroadcasterDisconnected int64 = iota
	OpCodeEVRPacketData
	OpCodeMatchGameStateUpdate
	OpCodeGameServerLobbyStatus
)

const (
	EVRLobbySessionMatchModule = "nevrlobby"

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

const (
	ModeUnloaded             Mode = ""                       // Unloaded Lobby
	ModeSocialPublic         Mode = "social_2.0"             // Public Social Lobby
	ModeSocialPrivate        Mode = "social_2.0_private"     // Private Social Lobby
	ModeSocialNPE            Mode = "social_2.0_npe"         // Social Lobby NPE
	ModeArenaPublic          Mode = "echo_arena"             // Public Echo Arena
	ModeArenaPrivate         Mode = "echo_arena_private"     // Private Echo Arena
	ModeArenaTournment       Mode = "echo_arena_tournament"  // Echo Arena Tournament
	ModeArenaPublicAI        Mode = "echo_arena_public_ai"   // Public Echo Arena AI
	ModeArenaPracticeAI      Mode = "echo_arena_practice"    // Echo Arena Practice
	ModeEchoCombatTournament Mode = "echo_combat_tournament" // Echo Combat Tournament
	ModeCombatPublic         Mode = "echo_combat"            // Echo Combat
	ModeCombatPrivate        Mode = "echo_combat_private"    // Private Echo Combat

	LevelUnspecified  Level = ""                       // Unloaded Lobby
	LevelSocial       Level = "mpl_lobby_b2"           // Social Lobby
	LevelArena        Level = "mpl_arena_a"            // Echo Arena
	ModeArenaTutorial Level = "mpl_tutorial_arena"     // Echo Arena Tutorial
	LevelFission      Level = "mpl_combat_fission"     // Echo Combat
	LevelCombustion   Level = "mpl_combat_combustion"  // Echo Combat
	LevelDyson        Level = "mpl_combat_dyson"       // Echo Combat
	LevelGauss        Level = "mpl_combat_gauss"       // Echo Combat
	LevelPebbles      Level = "mpl_combat_pebbles"     // Echo Combat
	LevelPtyPebbles   Level = "pty_mpl_combat_pebbles" // Echo Combat

	TeamBlue       RoleName = "blue"
	TeamOrange     RoleName = "orange"
	TeamSpectator  RoleName = "spectator"
	TeamSocial     RoleName = "social"
	TeamModerator  RoleName = "moderator"
	TeamUnassigned RoleName = "unassigned"

	PublicSession  Visibility = "public"
	PrivateSession Visibility = "private"
)

type (
	Mode                string
	Level               string
	RoleName            string
	Visibility          string
	MatchStatGroup      string
	MatchLevelSelection string
)

type ModeFilter struct {
	Levels          []Level
	MaxSlots        int32
	MaxTeamSize     int32
	Roles           []RoleName
	DefaultRole     RoleName
	DefaultTeamSize int32
}

var (
	GameModeConfigurations = map[Mode]ModeFilter{
		ModeCombatPublic: {
			MaxSlots:        MatchLobbyMaxSize,
			MaxTeamSize:     5,
			Roles:           []RoleName{TeamBlue, TeamOrange, TeamSpectator, TeamModerator},
			Levels:          []Level{LevelCombustion, LevelDyson, LevelFission, LevelGauss},
			DefaultRole:     TeamUnassigned,
			DefaultTeamSize: 5,
		},
		ModeCombatPrivate: {
			MaxSlots:        MatchLobbyMaxSize,
			MaxTeamSize:     5,
			Levels:          []Level{LevelCombustion, LevelDyson, LevelFission, LevelGauss},
			Roles:           []RoleName{TeamBlue, TeamOrange, TeamSpectator, TeamModerator},
			DefaultTeamSize: 5,
			DefaultRole:     TeamUnassigned,
		},
		ModeArenaPublic: {
			MaxSlots:        MatchLobbyMaxSize,
			MaxTeamSize:     5,
			Roles:           []RoleName{TeamBlue, TeamOrange, TeamSpectator, TeamModerator},
			Levels:          []Level{LevelArena},
			DefaultTeamSize: 4,
			DefaultRole:     TeamUnassigned,
		},
		ModeArenaPrivate: {
			Levels:      []Level{LevelArena},
			MaxSlots:    MatchLobbyMaxSize,
			MaxTeamSize: 4,
			Roles:       []RoleName{TeamBlue, TeamOrange, TeamSpectator, TeamModerator},
			DefaultRole: TeamUnassigned,
		},
		ModeSocialPublic: {
			Levels:      []Level{LevelSocial},
			MaxSlots:    SocialLobbyMaxSize,
			MaxTeamSize: SocialLobbyMaxSize,
			Roles:       []RoleName{TeamSocial, TeamModerator},
			DefaultRole: TeamSocial,
		},
		ModeSocialPrivate: {
			Levels:      []Level{LevelSocial},
			MaxSlots:    SocialLobbyMaxSize,
			MaxTeamSize: SocialLobbyMaxSize,
			Roles:       []RoleName{TeamSocial, TeamModerator},
			DefaultRole: TeamSocial,
		},
	}
)

func RandomLevelByMode(mode Mode) Level {
	valid, ok := GameModeConfigurations[mode]
	if !ok {
		return LevelUnspecified
	}
	levels := valid.Levels
	return levels[rand.Intn(len(levels))]
}

func DefaultLobbySize(mode Mode) int32 {
	valid, ok := GameModeConfigurations[mode]
	if !ok {
		return 0
	}
	return valid.MaxSlots
}

// TODO: move this to the protobuf definitions
type LobbySessionSettings struct {
	// The game mode, e.g. "combat_private"
	Mode Mode `json:"mode"`
	// The level to play on, e.g. "mpl_arena"
	Level Level `json:"level"`
	// The number of players per team (1-5)
	TeamSize int32 `json:"team_size"`
	// The user/Discord ID of the lobby creator/allocator
	CreatorID string `json:"creator_id"`
	// The user/Discord ID of the lobby owner/admin.
	OwnerID string `json:"owner_id"`
	// The group/Guild ID to which the lobby belongs.
	GroupID string `json:"group_id"`
	// Any required features that must be supported by the game server/clients.
	RequiredFeatures []string `json:"required_features"`
	// The team assignments for each user/Discord ID in the lobby.
	TeamAlignments map[string]RoleIndex `json:"team_alignments"`
	// The list of users who have reserved a spot in the lobby.
	Reservations []*LobbyPresence `json:"reservations"`
	// The expiry time for the reservations.
	ReservationExpiry time.Time `json:"reservation_expiry"`
	// The time when the match will expire if no players join.
	MatchExpiry time.Time `json:"match_expiry"`
	// Extra metadata for the lobby session.
	Metadata map[string]any `json:"metadata,omitempty"`
}

func (s LobbySessionSettings) String() string {
	data, _ := json.Marshal(s)
	return string(data)
}
