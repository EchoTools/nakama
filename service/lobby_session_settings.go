package service

import (
	"encoding/json"
	"math/rand"
	"slices"
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

	DefaultRegionCode = "default"
)

type Mode string

func (m Mode) IsPrivate() bool {
	return !slices.Contains([]Mode{ModeSocialPublic, ModeArenaPublic, ModeCombatPublic, ModeArenaPublicAI}, m)
}

func (m Mode) IsSocial() bool {
	return m == ModeSocialPublic || m == ModeSocialPrivate || m == ModeSocialNPE
}

func (m Mode) IsArena() bool {
	return slices.Contains([]Mode{ModeArenaPublic, ModeArenaPrivate, ModeArenaTournment, ModeArenaPracticeAI, ModeArenaPublicAI}, m)
}

func (m Mode) IsCombat() bool {
	return slices.Contains([]Mode{ModeCombatPublic, ModeCombatPrivate, ModeEchoCombatTournament}, m)
}

func (m Mode) String() string {
	return string(m)
}

type Level string

func (l Level) String() string {
	return string(l)
}

func (l Level) IsValid() bool {
	for _, levels := range LobbyModeSettings {
		for _, level := range levels.Levels {
			if l == level {
				return true
			}
		}
	}
	return false
}

type RoleName string

func (r RoleName) String() string {
	return string(r)
}

func (r RoleName) IsValid() bool {
	if r == TeamUnassigned {
		return true
	}
	for _, roles := range LobbyModeSettings {
		for _, role := range roles.Roles {
			if r == role {
				return true
			}
		}
	}
	return false
}

type (
	Visibility           string
	MatchStatGroup       string
	MatchLevelSelection  string
	LobbySettingsOptions struct {
		Levels          []Level
		MaxSlots        int32
		MaxTeamSize     int32
		Roles           []RoleName
		DefaultRole     RoleName
		DefaultTeamSize int32
	}
)

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
	ReservationsExpiry time.Time `json:"reservations_expiry"`
	// The time when the match will expire if no players join.
	MatchExpiry time.Time `json:"match_expiry"`
	// Extra metadata for the lobby session.
	Metadata map[string]any `json:"metadata,omitempty"`
}

func (s LobbySessionSettings) String() string {
	data, _ := json.Marshal(s)
	return string(data)
}

type validGameSettings map[Mode]LobbySettingsOptions

// IsValidMode checks if the given mode is valid.
func (v validGameSettings) IsValidMode(mode Mode) bool {
	_, ok := v[mode]
	return ok
}

// IsValidLevel checks if the given level is valid for the given mode.
func (v validGameSettings) IsValidLevel(mode Mode, level Level) bool {
	if !v.IsValidMode(mode) {
		return false
	}
	for _, l := range v[mode].Levels {
		if l == level {
			return true
		}
	}
	return false
}

// IsValidRole checks if the given role is valid for the given mode.
func (v validGameSettings) IsValidRole(mode Mode, role RoleName) bool {
	if !v.IsValidMode(mode) {
		return false
	}
	for _, r := range v[mode].Roles {
		if r == role {
			return true
		}
	}
	return false
}

// IsValidMaxSlots checks if the given size is valid for the given mode.
func (v validGameSettings) IsValidTeamSize(mode Mode, size int32) bool {
	if !v.IsValidMode(mode) {
		return false
	}
	return size > 0 && size <= v[mode].MaxTeamSize
}

// IsValidMaxSlots checks if the given size is a valid max slots for the given mode.
func (v validGameSettings) IsValidMaxSlots(mode Mode, size int32) bool {
	if !v.IsValidMode(mode) {
		return false
	}
	return size > 0 && size <= v[mode].MaxSlots
}

// DefaultRole returns a default role for the given mode. If the mode is invalid, it returns TeamUnassigned.
func (v validGameSettings) DefaultRole(mode Mode) RoleName {
	if !v.IsValidMode(mode) {
		return TeamUnassigned
	}
	return v[mode].DefaultRole
}

// DefaultTeamSize returns a default team size for the given mode. If the mode is invalid, it returns 0.
func (v validGameSettings) DefaultTeamSize(mode Mode) int32 {
	if !v.IsValidMode(mode) {
		return 0
	}
	return v[mode].DefaultTeamSize
}

// DefaultLevel returns a default level for the given mode. If there's only one level, it returns that level.
// If there are multiple levels, it returns a random level from the list.
func (v validGameSettings) RandomLevel(mode Mode) Level {
	if !v.IsValidMode(mode) || len(v[mode].Levels) == 0 {
		return LevelUnspecified
	}
	// If there's only one level, return it
	if len(v[mode].Levels) == 1 {
		return v[mode].Levels[0]
	}
	// Return a random level instead of the first
	levels := v[mode].Levels[:]
	return levels[rand.Intn(len(levels))]
}

// ValidModes returns a list of all valid modes.
func (v validGameSettings) ValidModes() []Mode {
	modes := make([]Mode, 0, len(v))
	for mode := range v {
		modes = append(modes, mode)
	}
	return modes
}

// MaxSlots returns the max slots for the given mode. If the mode is invalid, it returns 0.
func (v validGameSettings) MaxSlots(mode Mode) int32 {
	if !v.IsValidMode(mode) {
		return 0
	}
	return v[mode].MaxSlots
}

// MaxTeamSize returns the max team size for the given mode. If the mode is invalid, it returns 0.
func (v validGameSettings) MaxTeamSize(mode Mode) int32 {
	if !v.IsValidMode(mode) {
		return 0
	}
	return v[mode].MaxTeamSize
}

// ValidRoles returns a list of all valid roles for the given mode.
func (v validGameSettings) ValidRoles(mode Mode) []RoleName {
	if !v.IsValidMode(mode) {
		return nil
	}
	return v[mode].Roles[:]
}

// ValidLevels returns a list of all valid levels for the given mode.
func (v validGameSettings) ValidLevels(mode Mode) []Level {
	if !v.IsValidMode(mode) {
		return nil
	}
	return v[mode].Levels[:]
}

// ValidGameSettings is a global variable/functions for all valid game settings.
var ValidGameSettings = validGameSettings(LobbyModeSettings)

var (
	LobbyModeSettings = map[Mode]LobbySettingsOptions{
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
