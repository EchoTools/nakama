// This file was generated from JSON Schema using quicktype, do not modify it directly.
// To parse and unparse this JSON data, add this code to your project and do:
//
//    gameProfiles, err := UnmarshalGameProfiles(bytes)
//    bytes, err = gameProfiles.Marshal()

package evr

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/go-playground/validator/v10"
	"github.com/gofrs/uuid/v5"
)

var (
	validate *validator.Validate

	EvrIdNil = EvrId{}
)

type GUID uuid.UUID

// GUID is a wrapper around uuid.UUID to provide custom JSON marshaling and unmarshaling. (i.e. uppercase it)

func (g GUID) String() string {
	return strings.ToUpper(uuid.UUID(g).String())
}

func (g GUID) MarshalJSON() ([]byte, error) {
	return json.Marshal(g.String())
}

func (g *GUID) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return err
	}
	u, err := uuid.FromString(s)
	if err != nil {
		return err
	}
	*g = GUID(u)
	return nil
}

func (g GUID) MarshalBytes() []byte {
	b := uuid.UUID(g).Bytes()
	b[0], b[1], b[2], b[3] = b[3], b[2], b[1], b[0]
	b[4], b[5] = b[5], b[4]
	b[6], b[7] = b[7], b[6]
	return b
}

func (g *GUID) UnmarshalBytes(b []byte) error {
	if len(b) != 16 {
		return fmt.Errorf("GUID: UUID must be exactly 16 bytes long, got %d bytes", len(b))
	}
	b[0], b[1], b[2], b[3] = b[3], b[2], b[1], b[0]
	b[4], b[5] = b[5], b[4]
	b[6], b[7] = b[7], b[6]
	u, err := uuid.FromBytes(b)
	if err != nil {
		return err
	}
	*g = GUID(u)
	return nil
}

func init() {
	validate = validator.New()
	validate.RegisterValidation("restricted", func(fl validator.FieldLevel) bool { return true })
	validate.RegisterValidation("blocked", forceFalse)
	validate.RegisterValidation("evrid", func(fl validator.FieldLevel) bool {
		evrId, err := ParseEvrId(fl.Field().String())
		if err != nil || evrId.Equals(&EvrIdNil) {
			return false
		}
		return true
	})
}

func forceFalse(fl validator.FieldLevel) bool {
	fl.Field().Set(reflect.ValueOf(false))
	return true
}

// Profiles represents the 'profile' field in the JSON data
type GameProfiles struct {
	Client *ClientProfile `json:"client"`
	Server *ServerProfile `json:"server"`
}

func UnmarshalGameProfiles(data []byte) (GameProfiles, error) {
	var r GameProfiles
	err := json.Unmarshal(data, &r)
	return r, err
}

func (r *GameProfiles) Marshal() ([]byte, error) {
	return json.Marshal(r)
}

type ClientProfile struct {
	// WARNING: EchoVR dictates this struct/schema.
	DisplayName     string `json:"displayname,omitempty"` // Ignored and set by nakama
	EchoUserIdToken string `json:"xplatformid,omitempty"` // Ignored and set by nakama

	// The team name shown on the spectator scoreboard overlay
	TeamName           string            `json:"teamname,omitempty" validate:"omitempty,ascii"`
	CombatWeapon       string            `json:"weapon" validate:"omitnil,oneof=assault blaster rocket scout magnum smg chain rifle"`
	CombatGrenade      string            `json:"grenade" validate:"omitnil,oneof=arc burst det stun loc"`
	CombatDominantHand uint8             `json:"weaponarm" validate:"eq=0|eq=1"`
	ModifyTime         int64             `json:"modifytime" validate:"gte=0"` // Ignored and set by nakama
	CombatAbility      string            `json:"ability" validate:"oneof=buff heal sensor shield wraith"`
	LegalConsents      LegalConsents     `json:"legal" validate:"required"`
	MutedPlayers       Players           `json:"mute,omitempty"`
	GhostedPlayers     Players           `json:"ghost,omitempty"`
	NewPlayerProgress  NewPlayerProgress `json:"npe,omitempty"`
	Customization      Customization     `json:"customization,omitempty"`
	Social             Social            `json:"social,omitempty"`
	NewUnlocks         []int64           `json:"newunlocks,omitempty"`
}

// MergePlayerData merges a partial PlayerData with a template PlayerData.
func (base *ClientProfile) Merge(partial *ClientProfile) (*ClientProfile, error) {

	partialJSON, err := json.Marshal(partial)
	if err != nil {
		return nil, fmt.Errorf("error marshaling partial data: %v", err)
	}

	err = json.Unmarshal(partialJSON, &base)
	if err != nil {
		return nil, fmt.Errorf("error unmarshaling partial data: %v", err)
	}

	return base, nil
}

func (c *ClientProfile) SetDefaults() error {
	err := validate.Struct(c)
	if err != nil {
		return fmt.Errorf("error validating profile: %v", err)
	}

	return nil
}

func (c *ClientProfile) String() string {
	return fmt.Sprintf("ClientProfile{DisplayName: %s, EchoUserIdToken: %s, TeamName: %s, CombatWeapon: %s, CombatGrenade: %s, CombatDominantHand: %d, ModifyTime: %d, CombatAbility: %s, LegalConsents: %v, MutedPlayers: %v, GhostedPlayers: %v, NewPlayerProgress: %v, Customization: %v, Social: %v, NewUnlocks: %v}",
		c.DisplayName, c.EchoUserIdToken, c.TeamName, c.CombatWeapon, c.CombatGrenade, c.CombatDominantHand, c.ModifyTime, c.CombatAbility, c.LegalConsents, c.MutedPlayers, c.GhostedPlayers, c.NewPlayerProgress, c.Customization, c.Social, c.NewUnlocks)
}

type Customization struct {
	// WARNING: EchoVR dictates this struct/schema.
	BattlePassSeasonPoiVersion uint16 `json:"battlepass_season_poi_version,omitempty"` // Battle pass season point of interest version (manually set to 3246)
	NewUnlocksPoiVersion       uint16 `json:"new_unlocks_poi_version,omitempty"`       // New unlocks point of interest version
	StoreEntryPoiVersion       uint16 `json:"store_entry_poi_version,omitempty"`       // Store entry point of interest version
	ClearNewUnlocksVersion     uint16 `json:"clear_new_unlocks_version,omitempty"`     // Clear new unlocks version
}

type Players struct {
	// WARNING: EchoVR dictates this struct/schema.
	UserIds []string `json:"users"` // List of user IDs
}

type LegalConsents struct {
	// WARNING: EchoVR dictates this struct/schema.
	PointsPolicyVersion int64 `json:"points_policy_version,omitempty"`
	EulaVersion         int64 `json:"eula_version,omitempty"`
	GameAdminVersion    int64 `json:"game_admin_version,omitempty"`
	SplashScreenVersion int64 `json:"splash_screen_version,omitempty"`
	GroupsLegalVersion  int64 `json:"groups_legal_version,omitempty"`
}

type NewPlayerProgress struct {
	// WARNING: EchoVR dictates this struct/schema.
	Lobby             NpeMilestone `json:"lobby,omitempty"`                // User has completed the tutorial
	FirstMatch        NpeMilestone `json:"firstmatch,omitempty"`           // User has completed their first match
	Movement          NpeMilestone `json:"movement,omitempty"`             // User has completed the movement tutorial
	ArenaBasics       NpeMilestone `json:"arenabasics,omitempty"`          // User has completed the arena basics tutorial
	SocialTabSeen     Versioned    `json:"social_tab_seen,omitempty"`      // User has seen the social tab
	Pointer           Versioned    `json:"pointer,omitempty"`              // User has seen the pointer ?
	BlueTintTabSeen   Versioned    `json:"blue_tint_tab_seen,omitempty"`   // User has seen the blue tint tab in the character room
	HeraldryTabSeen   Versioned    `json:"heraldry_tab_seen,omitempty"`    // User has seen the heraldry tab in the character room
	OrangeTintTabSeen Versioned    `json:"orange_tint_tab_seen,omitempty"` // User has seen the orange tint tab in the character room
}

type NpeMilestone struct {
	// WARNING: EchoVR dictates this struct/schema.
	Completed bool `json:"completed,omitempty" validate:"boolean"` // User has completed the milestone
}

type Versioned struct {
	// WARNING: EchoVR dictates this struct/schema.
	Version int `json:"version,omitempty" validate:"gte=0"` // A version number, 1 is seen, 0 is not seen ?
}

type Social struct {
	// WARNING: EchoVR dictates this struct/schema.
	CommunityValuesVersion int64 `json:"community_values_version,omitempty" validate:"gte=0"`
	SetupVersion           int64 `json:"setup_version,omitempty" validate:"gte=0"`
	Group                  GUID  `json:"group,omitempty" validate:"uuid_rfc4122"` // The channel. It is a GUID, uppercase.
}

type ServerProfile struct {
	// WARNING: EchoVR dictates this struct/schema.
	// TODO Add comments for what these are
	DisplayName       string             `json:"displayname"`                                    // Overridden by nakama
	EchoUserIdToken   string             `json:"xplatformid"`                                    // Overridden by nakama
	SchemaVersion     int16              `json:"_version,omitempty" validate:"gte=0"`            // Version of the schema(?)
	PublisherLock     string             `json:"publisher_lock,omitempty"`                       // unused atm
	PurchasedCombat   int8               `json:"purchasedcombat,omitempty" validate:"eq=0|eq=1"` // unused (combat was made free)
	LobbyVersion      uint64             `json:"lobbyversion" validate:"gte=0"`                  // set from the login request
	LoginTime         int64              `json:"logintime" validate:"gte=0"`                     // When the player logged in
	UpdateTime        int64              `json:"updatetime" validate:"gte=0"`                    // When the profile was last stored.
	CreateTime        int64              `json:"createtime" validate:"gte=0"`                    // When the player's nakama account was created.
	Statistics        PlayerStatistics   `json:"stats,omitempty"`                                // Player statistics
	MaybeStale        *bool              `json:"maybestale,omitempty" validate:"boolean"`        // If the profile is stale
	UnlockedCosmetics UnlockedCosmetics  `json:"unlocks,omitempty"`                              // Unlocked cosmetics
	EquippedCosmetics EquippedCosmetics  `json:"loadout,omitempty"`                              // Equipped cosmetics
	Social            Social             `json:"social,omitempty"`                               // Social settings
	Achievements      interface{}        `json:"achievements,omitempty"`                         // Achievements
	RewardState       interface{}        `json:"reward_state,omitempty"`                         // Reward state?
	DeveloperFeatures *DeveloperFeatures `json:"dev,omitempty"`                                  // Developer features
}

type DeveloperFeatures struct {
	// WARNING: EchoVR dictates this struct/schema.
	DisableAfkTimeout bool   `json:"disable_afk_timeout,omitempty"`
	EchoUserIdToken   string `json:"xplatformid,omitempty"`
}

type PlayerStatistics struct {
	Arena  ArenaStatistics  `json:"arena,omitempty"`
	Combat CombatStatistics `json:"combat,omitempty"`
}

type ArenaStatistics struct {
	Level                        LevelStatistic       `json:"Level"`
	Stuns                        *DiscreteStatistic   `json:"Stuns,omitempty"`
	TopSpeedsTotal               *ContinuousStatistic `json:"TopSpeedsTotal,omitempty"`
	HighestArenaWinStreak        *DiscreteStatistic   `json:"HighestArenaWinStreak,omitempty"`
	ArenaWinPercentage           *ContinuousStatistic `json:"ArenaWinPercentage,omitempty"`
	ArenaWins                    *DiscreteStatistic   `json:"ArenaWins,omitempty"`
	ShotsOnGoalAgainst           *DiscreteStatistic   `json:"ShotsOnGoalAgainst,omitempty"`
	Clears                       *DiscreteStatistic   `json:"Clears,omitempty"`
	AssistsPerGame               *ContinuousStatistic `json:"AssistsPerGame,omitempty"`
	Passes                       *DiscreteStatistic   `json:"Passes,omitempty"`
	AveragePossessionTimePerGame *ContinuousStatistic `json:"AveragePossessionTimePerGame,omitempty"`
	Catches                      *DiscreteStatistic   `json:"Catches,omitempty"`
	PossessionTime               *ContinuousStatistic `json:"PossessionTime,omitempty"`
	StunsPerGame                 *ContinuousStatistic `json:"StunsPerGame,omitempty"`
	ShotsOnGoal                  *DiscreteStatistic   `json:"ShotsOnGoal,omitempty"`
	PunchesReceived              *DiscreteStatistic   `json:"PunchesReceived,omitempty"`
	CurrentArenaWinStreak        *DiscreteStatistic   `json:"CurrentArenaWinStreak,omitempty"`
	Assists                      *DiscreteStatistic   `json:"Assists,omitempty"`
	Interceptions                *DiscreteStatistic   `json:"Interceptions,omitempty"`
	HighestStuns                 *DiscreteStatistic   `json:"HighestStuns,omitempty"`
	AverageTopSpeedPerGame       *ContinuousStatistic `json:"AverageTopSpeedPerGame,omitempty"`
	XP                           *DiscreteStatistic   `json:"XP,omitempty"`
	ArenaLosses                  *DiscreteStatistic   `json:"ArenaLosses,omitempty"`
	SavesPerGame                 *ContinuousStatistic `json:"SavesPerGame,omitempty"`
	Blocks                       *DiscreteStatistic   `json:"Blocks,omitempty"`
	Saves                        *DiscreteStatistic   `json:"Saves,omitempty"`
	HighestSaves                 *DiscreteStatistic   `json:"HighestSaves,omitempty"`
	GoalSavePercentage           *ContinuousStatistic `json:"GoalSavePercentage,omitempty"`
	BlockPercentage              *ContinuousStatistic `json:"BlockPercentage,omitempty"`
	GoalsPerGame                 *ContinuousStatistic `json:"GoalsPerGame,omitempty"`
	Points                       *DiscreteStatistic   `json:"Points,omitempty"`
	Goals                        *DiscreteStatistic   `json:"Goals,omitempty"`
	Steals                       *DiscreteStatistic   `json:"Steals,omitempty"`
	TwoPointGoals                *DiscreteStatistic   `json:"TwoPointGoals,omitempty"`
	HighestPoints                *DiscreteStatistic   `json:"HighestPoints,omitempty"`
	GoalScorePercentage          *ContinuousStatistic `json:"GoalScorePercentage,omitempty"`
	AveragePointsPerGame         *ContinuousStatistic `json:"AveragePointsPerGame,omitempty"`
	ThreePointGoals              *DiscreteStatistic   `json:"ThreePointGoals,omitempty"`
	BounceGoals                  *DiscreteStatistic   `json:"BounceGoals,omitempty"`
	ArenaMVPPercentage           *ContinuousStatistic `json:"ArenaMVPPercentage,omitempty"`
	ArenaMVPS                    *DiscreteStatistic   `json:"ArenaMVPs,omitempty"`
	CurrentArenaMVPStreak        *DiscreteStatistic   `json:"CurrentArenaMVPStreak,omitempty"`
	HighestArenaMVPStreak        *DiscreteStatistic   `json:"HighestArenaMVPStreak,omitempty"`
	HeadbuttGoals                *DiscreteStatistic   `json:"HeadbuttGoals,omitempty"`
	HatTricks                    *DiscreteStatistic   `json:"HatTricks,omitempty"`
}
type CombatStatistics struct {
	Level                              LevelStatistic              `json:"Level"`
	CombatAssists                      *CountedDiscreteStatistic   `json:"CombatAssists,omitempty"`
	CombatObjectiveDamage              *CountedContinuousStatistic `json:"CombatObjectiveDamage,omitempty"`
	CombatEliminations                 *CountedDiscreteStatistic   `json:"CombatEliminations,omitempty"`
	CombatDamageAbsorbed               *ContinuousStatistic        `json:"CombatDamageAbsorbed,omitempty"`
	CombatWINS                         *CountedDiscreteStatistic   `json:"CombatWins,omitempty"`
	CombatDamageTaken                  *CountedContinuousStatistic `json:"CombatDamageTaken,omitempty"`
	CombatWinPercentage                *CountedDiscreteStatistic   `json:"CombatWinPercentage,omitempty"`
	CombatStuns                        *CountedDiscreteStatistic   `json:"CombatStuns,omitempty"`
	CombatKills                        *CountedDiscreteStatistic   `json:"CombatKills,omitempty"`
	CombatPointCaptureGamesPlayed      *DiscreteStatistic          `json:"CombatPointCaptureGamesPlayed,omitempty"`
	CombatPointCaptureWINS             *DiscreteStatistic          `json:"CombatPointCaptureWins,omitempty"`
	CombatObjectiveTime                *CountedContinuousStatistic `json:"CombatObjectiveTime,omitempty"`
	CombatAverageEliminationDeathRatio *CountedContinuousStatistic `json:"CombatAverageEliminationDeathRatio,omitempty"`
	CombatPointCaptureWinPercentage    *ContinuousStatistic        `json:"CombatPointCaptureWinPercentage,omitempty"`
	CombatDeaths                       *CountedDiscreteStatistic   `json:"CombatDeaths,omitempty"`
	CombatDamage                       *CountedContinuousStatistic `json:"CombatDamage,omitempty"`
	CombatObjectiveEliminations        *CountedDiscreteStatistic   `json:"CombatObjectiveEliminations,omitempty"`
	CombatBestEliminationStreak        *CountedDiscreteStatistic   `json:"CombatBestEliminationStreak,omitempty"`
	CombatSoloKills                    *CountedDiscreteStatistic   `json:"CombatSoloKills,omitempty"`
	CombatHeadshotKills                *CountedDiscreteStatistic   `json:"CombatHeadshotKills,omitempty"`
	XP                                 *CountedDiscreteStatistic   `json:"XP,omitempty"`
	CombatMVPS                         *DiscreteStatistic          `json:"CombatMVPs,omitempty"`
	CombatHillDefends                  *DiscreteStatistic          `json:"CombatHillDefends,omitempty"`
	CombatPayloadWINS                  *CountedDiscreteStatistic   `json:"CombatPayloadWins,omitempty"`
	CombatPayloadGamesPlayed           *CountedDiscreteStatistic   `json:"CombatPayloadGamesPlayed,omitempty"`
	CombatPayloadWinPercentage         *CountedDiscreteStatistic   `json:"CombatPayloadWinPercentage,omitempty"`
	CombatHillCaptures                 *DiscreteStatistic          `json:"CombatHillCaptures,omitempty"`
	CombatHealing                      *CountedContinuousStatistic `json:"CombatHealing,omitempty"`
	CombatTeammateHealing              *CountedContinuousStatistic `json:"CombatTeammateHealing,omitempty"`
	CombatLosses                       *CountedDiscreteStatistic   `json:"CombatLosses,omitempty"`
}

type CountedDiscreteStatistic struct {
	Count   int64  `json:"cnt,omitempty" validate:"gte=0,required_with=Operand Value"`
	Operand string `json:"op,omitempty" validate:"oneof=add rep max, required_with=Count Value"`
	Value   uint64 `json:"val,omitempty" validate:"gte=0,required_with=Operand Count"`
}

type CountedContinuousStatistic struct {
	Operand string  `json:"op,omitempty" validate:"oneof=add rep max,required_with=Count Value"`
	Value   float64 `json:"val,omitempty" validate:"gte=0,required_with=Operand Count"`
	Count   uint64  `json:"cnt,omitempty" validate:"gte=0,required_with=Operand Value"`
}

type DiscreteStatistic struct {
	Operand string `json:"op,omitempty" validate:"oneof=add rep max,required_with=Value"`
	Value   uint64 `json:"val,omitempty" validate:"gte=0,required_with=Operand"`
}

type ContinuousStatistic struct {
	Operand string  `json:"op,omitempty" validate:"oneof=add rep max,required_with=Value"`
	Value   float64 `json:"val,omitempty" validate:"gte=0,required_with=Operand"`
}

type LevelStatistic struct {
	Count   uint8  `json:"cnt,omitempty" validate:"gte=0,required"`
	Operand string `json:"op,omitempty" validate:"oneof=add,required"`
	Value   uint8  `json:"val,omitempty" validate:"gte=1,required"`
}

type DailyStats struct {
	Stuns                        *DiscreteStatistic   `json:"Stuns,omitempty"`
	XP                           *DiscreteStatistic   `json:"XP,omitempty"`
	TopSpeedsTotal               *ContinuousStatistic `json:"TopSpeedsTotal,omitempty"`
	HighestArenaWinStreak        *DiscreteStatistic   `json:"HighestArenaWinStreak,omitempty"`
	ArenaWinPercentage           *DiscreteStatistic   `json:"ArenaWinPercentage,omitempty"`
	ArenaWins                    *DiscreteStatistic   `json:"ArenaWins,omitempty"`
	ShotsOnGoalAgainst           *DiscreteStatistic   `json:"ShotsOnGoalAgainst,omitempty"`
	Clears                       *DiscreteStatistic   `json:"Clears,omitempty"`
	AssistsPerGame               *ContinuousStatistic `json:"AssistsPerGame,omitempty"`
	Passes                       *DiscreteStatistic   `json:"Passes,omitempty"`
	AveragePossessionTimePerGame *ContinuousStatistic `json:"AveragePossessionTimePerGame,omitempty"`
	Catches                      *DiscreteStatistic   `json:"Catches,omitempty"`
	PossessionTime               *ContinuousStatistic `json:"PossessionTime,omitempty"`
	StunsPerGame                 *DiscreteStatistic   `json:"StunsPerGame,omitempty"`
	ShotsOnGoal                  *DiscreteStatistic   `json:"ShotsOnGoal,omitempty"`
	PunchesReceived              *DiscreteStatistic   `json:"PunchesReceived,omitempty"`
	CurrentArenaWinStreak        *DiscreteStatistic   `json:"CurrentArenaWinStreak,omitempty"`
	Assists                      *DiscreteStatistic   `json:"Assists,omitempty"`
	Interceptions                *DiscreteStatistic   `json:"Interceptions,omitempty"`
	HighestStuns                 *DiscreteStatistic   `json:"HighestStuns,omitempty"`
	AverageTopSpeedPerGame       *ContinuousStatistic `json:"AverageTopSpeedPerGame,omitempty"`
	ArenaLosses                  *DiscreteStatistic   `json:"ArenaLosses,omitempty"`
	SavesPerGame                 *ContinuousStatistic `json:"SavesPerGame,omitempty"`
	Blocks                       *DiscreteStatistic   `json:"Blocks,omitempty"`
	Saves                        *DiscreteStatistic   `json:"Saves,omitempty"`
	HighestSaves                 *DiscreteStatistic   `json:"HighestSaves,omitempty"`
	GoalSavePercentage           *ContinuousStatistic `json:"GoalSavePercentage,omitempty"`
	BlockPercentage              *ContinuousStatistic `json:"BlockPercentage,omitempty"`
}

type WeelkyStats struct {
	Stuns                        *DiscreteStatistic   `json:"Stuns,omitempty"`
	XP                           *DiscreteStatistic   `json:"XP,omitempty"`
	TopSpeedsTotal               *ContinuousStatistic `json:"TopSpeedsTotal,omitempty"`
	HighestArenaWinStreak        *DiscreteStatistic   `json:"HighestArenaWinStreak,omitempty"`
	ArenaWinPercentage           *DiscreteStatistic   `json:"ArenaWinPercentage,omitempty"`
	ArenaWINS                    *DiscreteStatistic   `json:"ArenaWins,omitempty"`
	ShotsOnGoalAgainst           *DiscreteStatistic   `json:"ShotsOnGoalAgainst,omitempty"`
	Clears                       *DiscreteStatistic   `json:"Clears,omitempty"`
	AssistsPerGame               *ContinuousStatistic `json:"AssistsPerGame,omitempty"`
	Passes                       *DiscreteStatistic   `json:"Passes,omitempty"`
	AveragePossessionTimePerGame *ContinuousStatistic `json:"AveragePossessionTimePerGame,omitempty"`
	Catches                      *DiscreteStatistic   `json:"Catches,omitempty"`
	PossessionTime               *ContinuousStatistic `json:"PossessionTime,omitempty"`
	StunsPerGame                 *ContinuousStatistic `json:"StunsPerGame,omitempty"`
	ShotsOnGoal                  *DiscreteStatistic   `json:"ShotsOnGoal,omitempty"`
	PunchesReceived              *DiscreteStatistic   `json:"PunchesReceived,omitempty"`
	CurrentArenaWinStreak        *DiscreteStatistic   `json:"CurrentArenaWinStreak,omitempty"`
	Assists                      *DiscreteStatistic   `json:"Assists,omitempty"`
	Interceptions                *DiscreteStatistic   `json:"Interceptions,omitempty"`
	HighestStuns                 *DiscreteStatistic   `json:"HighestStuns,omitempty"`
	AverageTopSpeedPerGame       *ContinuousStatistic `json:"AverageTopSpeedPerGame,omitempty"`
	ArenaLosses                  *DiscreteStatistic   `json:"ArenaLosses,omitempty"`
	SavesPerGame                 *ContinuousStatistic `json:"SavesPerGame,omitempty"`
	Blocks                       *DiscreteStatistic   `json:"Blocks,omitempty"`
	Saves                        *DiscreteStatistic   `json:"Saves,omitempty"`
	HighestSaves                 *DiscreteStatistic   `json:"HighestSaves,omitempty"`
	GoalSavePercentage           *ContinuousStatistic `json:"GoalSavePercentage,omitempty"`
	BlockPercentage              *ContinuousStatistic `json:"BlockPercentage,omitempty"`
	GoalsPerGame                 *ContinuousStatistic `json:"GoalsPerGame,omitempty"`
	Points                       *DiscreteStatistic   `json:"Points,omitempty"`
	Goals                        *DiscreteStatistic   `json:"Goals,omitempty"`
	Steals                       *DiscreteStatistic   `json:"Steals,omitempty"`
	TwoPointGoals                *DiscreteStatistic   `json:"TwoPointGoals,omitempty"`
	HighestPoints                *DiscreteStatistic   `json:"HighestPoints,omitempty"`
	GoalScorePercentage          *ContinuousStatistic `json:"GoalScorePercentage,omitempty"`
	AveragePointsPerGame         *ContinuousStatistic `json:"AveragePointsPerGame,omitempty"`
}

type EquippedCosmetics struct {
	Instances CosmeticInstances `json:"instances"`
	Number    int64             `json:"number"`
}

type CosmeticInstances struct {
	Unified UnifiedCosmeticInstance `json:"unified"`
}

type UnifiedCosmeticInstance struct {
	Slots CosmeticLoadout `json:"slots"`
}

type CosmeticLoadout struct {
	Decal          string `json:"decal"`
	DecalBody      string `json:"decal_body"`
	Emote          string `json:"emote"`
	SecondEmote    string `json:"secondemote"`
	Tint           string `json:"tint"`
	TintBody       string `json:"tint_body"`
	TintAlignmentA string `json:"tint_alignment_a"`
	TintAlignmentB string `json:"tint_alignment_b"`
	Pattern        string `json:"pattern"`
	PatternBody    string `json:"pattern_body"`
	Pip            string `json:"pip"`
	Chassis        string `json:"chassis"`
	Bracer         string `json:"bracer"`
	Booster        string `json:"booster"`
	Title          string `json:"title"`
	Tag            string `json:"tag"`
	Banner         string `json:"banner"`
	Medal          string `json:"medal"`
	GoalFx         string `json:"goal_fx"`
	Emissive       string `json:"emissive"`
}

type UnlockedCosmetics struct {
	Arena  ArenaUnlocks  `json:"arena"`
	Combat CombatUnlocks `json:"combat"`
}

type Banner bool
type Booster bool
type Bracer bool
type Decal bool
type Emissive bool
type Emote bool
type Pattern bool
type Pip bool
type Title bool
type Chassis bool
type Currency bool
type Tag bool
type Tint bool
type Heraldry bool
type Goal bool
type Medal bool
type Loadout bool
type XP bool

type ArenaUnlocks struct {
	// WARNING: EchoVR dictates this struct/schema.
	// If an unlock needs to be filtered out, use the  validate field tag.
	// If an unlock must not be allowed ever, use the "isdefault" validator tag.

	/*
		Decals
	*/

	Banner0000                  Banner   `json:"rwd_banner_0000,omitempty"`
	Banner0001                  Banner   `json:"rwd_banner_0001,omitempty"`
	Banner0002                  Banner   `json:"rwd_banner_0002,omitempty"`
	Banner0003                  Banner   `json:"rwd_banner_0003,omitempty"`
	Banner0008                  Banner   `json:"rwd_banner_0008,omitempty"`
	Banner0009                  Banner   `json:"rwd_banner_0009,omitempty"`
	Banner0010                  Banner   `json:"rwd_banner_0010,omitempty"`
	Banner0011                  Banner   `json:"rwd_banner_0011,omitempty"`
	Banner0014                  Banner   `json:"rwd_banner_0014,omitempty"`
	Banner0015                  Banner   `json:"rwd_banner_0015,omitempty"`
	Banner0016                  Banner   `json:"rwd_banner_0016,omitempty"`
	Banner0017                  Banner   `json:"rwd_banner_0017,omitempty"`
	Banner0021                  Banner   `json:"rwd_banner_0021,omitempty"`
	Banner0022                  Banner   `json:"rwd_banner_0022,omitempty"`
	Banner0023                  Banner   `json:"rwd_banner_0023,omitempty"`
	Banner0024                  Banner   `json:"rwd_banner_0024,omitempty"`
	Banner0025                  Banner   `json:"rwd_banner_0025,omitempty"`
	Banner0030                  Banner   `json:"rwd_banner_0030,omitempty" validate:"blocked"`
	Banner0031                  Banner   `json:"rwd_banner_0031,omitempty" validate:"blocked"`
	BannerLoneEcho2_A           Banner   `json:"rwd_banner_lone_echo_2_a,omitempty"`
	BannerS1Basic               Banner   `json:"rwd_banner_s1_basic,omitempty"`
	BannerS1BoldStripe          Banner   `json:"rwd_banner_s1_bold_stripe,omitempty"`
	BannerS1Chevrons            Banner   `json:"rwd_banner_s1_chevrons,omitempty"`
	BannerS1Default             Banner   `json:"rwd_banner_s1_default,omitempty"`
	BannerS1Digi                Banner   `json:"rwd_banner_s1_digi,omitempty"`
	BannerS1Flames              Banner   `json:"rwd_banner_s1_flames,omitempty"`
	BannerS1Hourglass           Banner   `json:"rwd_banner_s1_hourglass,omitempty"`
	BannerS1Squish              Banner   `json:"rwd_banner_s1_squish,omitempty"`
	BannerS1Tattered            Banner   `json:"rwd_banner_s1_tattered,omitempty"`
	BannerS1Trex                Banner   `json:"rwd_banner_s1_trex,omitempty"`
	BannerS1Tritip              Banner   `json:"rwd_banner_s1_tritip,omitempty"`
	BannerS1Wings               Banner   `json:"rwd_banner_s1_wings,omitempty"`
	BannerS2Deco                Banner   `json:"rwd_banner_s2_deco,omitempty"`
	BannerS2Gears               Banner   `json:"rwd_banner_s2_gears,omitempty"`
	BannerS2Ladybug             Banner   `json:"rwd_banner_s2_ladybug,omitempty"`
	BannerS2Pyramids            Banner   `json:"rwd_banner_s2_pyramids,omitempty"`
	BannerS2Squares             Banner   `json:"rwd_banner_s2_squares,omitempty"`
	BannerSashimonoA            Banner   `json:"rwd_banner_sashimono_a,omitempty"`
	BannerSpartanShieldA        Banner   `json:"rwd_banner_spartan_shield_a,omitempty"`
	BannerTrianglesA            Banner   `json:"rwd_banner_triangles_a,omitempty"`
	BoosterAnubisAHorus         Booster  `json:"rwd_booster_anubis_a_horus,omitempty"`
	BoosterAnubisS2A            Booster  `json:"rwd_booster_anubis_s2_a,omitempty"`
	BoosterArcadeS1A            Booster  `json:"rwd_booster_arcade_s1_a,omitempty"`
	BoosterArcadeVarS1A         Booster  `json:"rwd_booster_arcade_var_s1_a,omitempty"`
	BoosterAurumA               Booster  `json:"rwd_booster_aurum_a,omitempty"`
	BoosterAutomatonS2A         Booster  `json:"rwd_booster_automaton_s2_a,omitempty"`
	BoosterBeeS2A               Booster  `json:"rwd_booster_bee_s2_a,omitempty"`
	BoosterCovenantA            Booster  `json:"rwd_booster_covenant_a,omitempty"`
	BoosterCovenantAFlame       Booster  `json:"rwd_booster_covenant_a_flame,omitempty"`
	BoosterDefault              Booster  `json:"rwd_booster_default,omitempty"`
	BoosterFumeA                Booster  `json:"rwd_booster_fume_a,omitempty"`
	BoosterFumeADaydream        Booster  `json:"rwd_booster_fume_a_daydream,omitempty"`
	BoosterFunkA                Booster  `json:"rwd_booster_funk_a,omitempty"`
	BoosterHerosuitA            Booster  `json:"rwd_booster_herosuit_a,omitempty"`
	BoosterJunkyardA            Booster  `json:"rwd_booster_junkyard_a,omitempty"`
	BoosterLadybugS2A           Booster  `json:"rwd_booster_ladybug_s2_a,omitempty"`
	BoosterLazurliteA           Booster  `json:"rwd_booster_lazurlite_a,omitempty"`
	BoosterMakoS1A              Booster  `json:"rwd_booster_mako_s1_a,omitempty"`
	BoosterNobleA               Booster  `json:"rwd_booster_noble_a,omitempty"`
	BoosterNuclearA             Booster  `json:"rwd_booster_nuclear_a,omitempty"`
	BoosterNuclearAHydro        Booster  `json:"rwd_booster_nuclear_a_hydro,omitempty"`
	BoosterPlagueknightA        Booster  `json:"rwd_booster_plagueknight_a,omitempty"`
	BoosterRoverA               Booster  `json:"rwd_booster_rover_a,omitempty"`
	BoosterRoverADeco           Booster  `json:"rwd_booster_rover_a_deco,omitempty"`
	BoosterS11S1AFire           Booster  `json:"rwd_booster_s11_s1_a_fire,omitempty"`
	BoosterS11S1ARetro          Booster  `json:"rwd_booster_s11_s1_a_retro,omitempty"`
	BoosterSamuraiA             Booster  `json:"rwd_booster_samurai_a,omitempty"`
	BoosterScubaA               Booster  `json:"rwd_booster_scuba_a,omitempty"`
	BoosterSharkA               Booster  `json:"rwd_booster_shark_a,omitempty"`
	BoosterSharkATropical       Booster  `json:"rwd_booster_shark_a_tropical,omitempty"`
	BoosterSpartanA             Booster  `json:"rwd_booster_spartan_a,omitempty"`
	BoosterSpartanAHero         Booster  `json:"rwd_booster_spartan_a_hero,omitempty"`
	BoosterStreetwearA          Booster  `json:"rwd_booster_streetwear_a,omitempty"`
	BoosterTrexS1A              Booster  `json:"rwd_booster_trex_s1_a,omitempty"`
	BoosterVintageA             Booster  `json:"rwd_booster_vintage_a,omitempty"`
	BoosterWastelandA           Booster  `json:"rwd_booster_wasteland_a,omitempty"`
	BracerAnubisAHorus          Bracer   `json:"rwd_bracer_anubis_a_horus,omitempty"`
	BracerAnubisS2A             Bracer   `json:"rwd_bracer_anubis_s2_a,omitempty"`
	BracerArcadeS1A             Bracer   `json:"rwd_bracer_arcade_s1_a,omitempty"`
	BracerArcadeVarS1A          Bracer   `json:"rwd_bracer_arcade_var_s1_a,omitempty"`
	BracerAurumA                Bracer   `json:"rwd_bracer_aurum_a,omitempty"`
	BracerAutomatonS2A          Bracer   `json:"rwd_bracer_automaton_s2_a,omitempty"`
	BracerBeeS2A                Bracer   `json:"rwd_bracer_bee_s2_a,omitempty"`
	BracerCovenantA             Bracer   `json:"rwd_bracer_covenant_a,omitempty"`
	BracerCovenantAFlame        Bracer   `json:"rwd_bracer_covenant_a_flame,omitempty"`
	BracerDefault               Bracer   `json:"rwd_bracer_default,omitempty"`
	BracerFumeA                 Bracer   `json:"rwd_bracer_fume_a,omitempty"`
	BracerFumeADaydream         Bracer   `json:"rwd_bracer_fume_a_daydream,omitempty"`
	BracerFunkA                 Bracer   `json:"rwd_bracer_funk_a,omitempty"`
	BracerJunkyardA             Bracer   `json:"rwd_bracer_junkyard_a,omitempty"`
	BracerLadybugS2A            Bracer   `json:"rwd_bracer_ladybug_s2_a,omitempty"`
	BracerLazurliteA            Bracer   `json:"rwd_bracer_lazurlite_a,omitempty"`
	BracerMakoS1A               Bracer   `json:"rwd_bracer_mako_s1_a,omitempty"`
	BracerNobleA                Bracer   `json:"rwd_bracer_noble_a,omitempty"`
	BracerNuclearA              Bracer   `json:"rwd_bracer_nuclear_a,omitempty"`
	BracerNuclearAHydro         Bracer   `json:"rwd_bracer_nuclear_a_hydro,omitempty"`
	BracerPlagueknightA         Bracer   `json:"rwd_bracer_plagueknight_a,omitempty"`
	BracerRoverA                Bracer   `json:"rwd_bracer_rover_a,omitempty"`
	BracerRoverADeco            Bracer   `json:"rwd_bracer_rover_a_deco,omitempty"`
	BracerSamuraiA              Bracer   `json:"rwd_bracer_samurai_a,omitempty"`
	BracerScubaA                Bracer   `json:"rwd_bracer_scuba_a,omitempty"`
	BracerSharkA                Bracer   `json:"rwd_bracer_shark_a,omitempty"`
	BracerSharkATropical        Bracer   `json:"rwd_bracer_shark_a_tropical,omitempty"`
	BracerSnacktimeA            Bracer   `json:"rwd_bracer_snacktime_a,omitempty"`
	BracerSpartanA              Bracer   `json:"rwd_bracer_spartan_a,omitempty"`
	BracerSpartanAHero          Bracer   `json:"rwd_bracer_spartan_a_hero,omitempty"`
	BracerStreetwearA           Bracer   `json:"rwd_bracer_streetwear_a,omitempty"`
	BracerTrexS1A               Bracer   `json:"rwd_bracer_trex_s1_a,omitempty"`
	BracerVintageA              Bracer   `json:"rwd_bracer_vintage_a,omitempty"`
	BracerWastelandA            Bracer   `json:"rwd_bracer_wasteland_a,omitempty"`
	ChassisAnubisAHorus         Chassis  `json:"rwd_chassis_anubis_a_horus,omitempty"`
	ChassisAnubisS2A            Chassis  `json:"rwd_chassis_anubis_s2_a,omitempty"`
	ChassisAutomatonS2A         Chassis  `json:"rwd_chassis_automaton_s2_a,omitempty"`
	ChassisBodyS11A             Chassis  `json:"rwd_chassis_body_s11_a,omitempty"`
	ChassisFunkA                Chassis  `json:"rwd_chassis_funk_a,omitempty"`
	ChassisHerosuitA            Chassis  `json:"rwd_chassis_herosuit_a,omitempty"`
	ChassisJunkyardA            Chassis  `json:"rwd_chassis_junkyard_a,omitempty"`
	ChassisMakoS1A              Chassis  `json:"rwd_chassis_mako_s1_a,omitempty"`
	ChassisNobleA               Chassis  `json:"rwd_chassis_noble_a,omitempty"`
	ChassisPlagueknightA        Chassis  `json:"rwd_chassis_plagueknight_a,omitempty"`
	ChassisS11FlameA            Chassis  `json:"rwd_chassis_s11_flame_a,omitempty"`
	ChassisS11RetroA            Chassis  `json:"rwd_chassis_s11_retro_a,omitempty"`
	ChassisS8BA                 Chassis  `json:"rwd_chassis_s8b_a,omitempty"`
	ChassisSamuraiA             Chassis  `json:"rwd_chassis_samurai_a,omitempty"`
	ChassisScubaA               Chassis  `json:"rwd_chassis_scuba_a,omitempty"`
	ChassisSharkA               Chassis  `json:"rwd_chassis_shark_a,omitempty"`
	ChassisSharkATropical       Chassis  `json:"rwd_chassis_shark_a_tropical,omitempty"`
	ChassisSpartanA             Chassis  `json:"rwd_chassis_spartan_a,omitempty"`
	ChassisSpartanAHero         Chassis  `json:"rwd_chassis_spartan_a_hero,omitempty"`
	ChassisStreetwearA          Chassis  `json:"rwd_chassis_streetwear_a,omitempty"`
	ChassisTrexS1A              Chassis  `json:"rwd_chassis_trex_s1_a,omitempty"`
	ChassisWastelandA           Chassis  `json:"rwd_chassis_wasteland_a,omitempty"`
	CurrencyS0101               Currency `json:"rwd_currency_s01_01,omitempty"`
	CurrencyS0102               Currency `json:"rwd_currency_s01_02,omitempty"`
	CurrencyS0103               Currency `json:"rwd_currency_s01_03,omitempty"`
	CurrencyS0104               Currency `json:"rwd_currency_s01_04,omitempty"`
	CurrencyS0201               Currency `json:"rwd_currency_s02_01,omitempty"`
	CurrencyS0202               Currency `json:"rwd_currency_s02_02,omitempty"`
	CurrencyS0203               Currency `json:"rwd_currency_s02_03,omitempty"`
	CurrencyS0204               Currency `json:"rwd_currency_s02_04,omitempty"`
	CurrencyS0301               Currency `json:"rwd_currency_s03_01,omitempty"`
	CurrencyS0302               Currency `json:"rwd_currency_s03_02,omitempty"`
	CurrencyS0303               Currency `json:"rwd_currency_s03_03,omitempty"`
	CurrencyS0304               Currency `json:"rwd_currency_s03_04,omitempty"`
	CurrencyS0401               Currency `json:"rwd_currency_s04_01,omitempty"`
	CurrencyS0402               Currency `json:"rwd_currency_s04_02,omitempty"`
	CurrencyS0403               Currency `json:"rwd_currency_s04_03,omitempty"`
	CurrencyS0404               Currency `json:"rwd_currency_s04_04,omitempty"`
	CurrencyS0501               Currency `json:"rwd_currency_s05_01,omitempty"`
	CurrencyS0502               Currency `json:"rwd_currency_s05_02,omitempty"`
	CurrencyS0503               Currency `json:"rwd_currency_s05_03,omitempty"`
	CurrencyS0504               Currency `json:"rwd_currency_s05_04,omitempty"`
	CurrencyS0601               Currency `json:"rwd_currency_s06_01,omitempty"`
	CurrencyS0602               Currency `json:"rwd_currency_s06_02,omitempty"`
	CurrencyS0603               Currency `json:"rwd_currency_s06_03,omitempty"`
	CurrencyS0604               Currency `json:"rwd_currency_s06_04,omitempty"`
	CurrencyS0701               Currency `json:"rwd_currency_s07_01,omitempty"`
	CurrencyS0702               Currency `json:"rwd_currency_s07_02,omitempty"`
	CurrencyS0703               Currency `json:"rwd_currency_s07_03,omitempty"`
	CurrencyS0704               Currency `json:"rwd_currency_s07_04,omitempty"`
	Decal0000                   Decal    `json:"rwd_decal_0000,omitempty"`
	Decal0001                   Decal    `json:"rwd_decal_0001,omitempty"`
	Decal0002                   Decal    `json:"rwd_decal_0002,omitempty"`
	Decal0005                   Decal    `json:"rwd_decal_0005,omitempty"`
	Decal0006                   Decal    `json:"rwd_decal_0006,omitempty"`
	Decal0007                   Decal    `json:"rwd_decal_0007,omitempty"`
	Decal0010                   Decal    `json:"rwd_decal_0010,omitempty"`
	Decal0011                   Decal    `json:"rwd_decal_0011,omitempty"`
	Decal0012                   Decal    `json:"rwd_decal_0012,omitempty"`
	Decal0015                   Decal    `json:"rwd_decal_0015,omitempty"`
	Decal0016                   Decal    `json:"rwd_decal_0016,omitempty"`
	Decal0017                   Decal    `json:"rwd_decal_0017,omitempty"`
	Decal0018                   Decal    `json:"rwd_decal_0018,omitempty"`
	Decal0019                   Decal    `json:"rwd_decal_0019,omitempty"`
	Decal0020                   Decal    `json:"rwd_decal_0020,omitempty"`
	Decal0023                   Decal    `json:"rwd_decal_0023,omitempty" validate:"blocked"`
	Decal0024                   Decal    `json:"rwd_decal_0024,omitempty" validate:"blocked"`
	DecalAlienHeadA             Decal    `json:"decal_alien_head_a,omitempty"`
	DecalAnniversaryCupcakeA    Decal    `json:"decal_anniversary_cupcake_a,omitempty"`
	DecalAxolotlS2A             Decal    `json:"rwd_decal_axolotl_s2_a,omitempty"`
	DecalbackDefault            Decal    `json:"rwd_decalback_default,omitempty"`
	DecalBearPawA               Decal    `json:"decal_bear_paw_a,omitempty"`
	DecalBombA                  Decal    `json:"decal_bomb_a,omitempty"`
	DecalborderDefault          Decal    `json:"rwd_decalborder_default,omitempty"`
	DecalBowA                   Decal    `json:"decal_bow_a,omitempty"`
	DecalBullseyeA              Decal    `json:"decal_bullseye_a,omitempty"`
	DecalCatA                   Decal    `json:"decal_cat_a,omitempty"`
	DecalCherryBlossomA         Decal    `json:"rwd_decal_cherry_blossom_a,omitempty"`
	DecalCombatAnniversaryA     Decal    `json:"decal_combat_anniversary_a,omitempty"`
	DecalCombatCometA           Decal    `json:"decal_combat_comet_a,omitempty"`
	DecalCombatDemonA           Decal    `json:"decal_combat_demon_a,omitempty"`
	DecalCombatFlamingoA        Decal    `json:"decal_combat_flamingo_a,omitempty"`
	DecalCombatFlyingSaucerA    Decal    `json:"decal_combat_flying_saucer_a,omitempty"`
	DecalCombatIceCreamA        Decal    `json:"decal_combat_ice_cream_a,omitempty"`
	DecalCombatLionA            Decal    `json:"decal_combat_lion_a,omitempty"`
	DecalCombatLogoA            Decal    `json:"decal_combat_logo_a,omitempty"`
	DecalCombatMedicA           Decal    `json:"decal_combat_medic_a,omitempty"`
	DecalCombatMeteorA          Decal    `json:"decal_combat_meteor_a,omitempty"`
	DecalCombatMilitaryBadgeA   Decal    `json:"decal_combat_military_badge_a,omitempty"`
	DecalCombatNovaA            Decal    `json:"decal_combat_nova_a,omitempty"`
	DecalCombatOctopusA         Decal    `json:"decal_combat_octopus_a,omitempty"`
	DecalCombatPigA             Decal    `json:"decal_combat_pig_a,omitempty"`
	DecalCombatPizzaA           Decal    `json:"decal_combat_pizza_a,omitempty"`
	DecalCombatPulsarA          Decal    `json:"decal_combat_pulsar_a,omitempty"`
	DecalCombatPuppyA           Decal    `json:"decal_combat_puppy_a,omitempty"`
	DecalCombatRageBearA        Decal    `json:"decal_combat_rage_bear_a,omitempty"`
	DecalCombatScratchA         Decal    `json:"decal_combat_scratch_a,omitempty"`
	DecalCombatSkullCrossbonesA Decal    `json:"decal_combat_skull_crossbones_a,omitempty"`
	DecalCombatTrexSkullA       Decal    `json:"decal_combat_trex_skull_a,omitempty"`
	DecalCrosshairA             Decal    `json:"decal_crosshair_a,omitempty"`
	DecalCrownA                 Decal    `json:"decal_crown_a,omitempty"`
	DecalCupcakeA               Decal    `json:"decal_cupcake_a,omitempty"`
	DecalDefault                Decal    `json:"decal_default,omitempty"`
	DecalDinosaurA              Decal    `json:"decal_dinosaur_a,omitempty"`
	DecalDiscA                  Decal    `json:"decal_disc_a,omitempty"`
	DecalEagleA                 Decal    `json:"decal_eagle_a,omitempty"`
	DecalFangsA                 Decal    `json:"decal_fangs_a,omitempty"`
	DecalFireballA              Decal    `json:"decal_fireball_a,omitempty"`
	DecalGearsS2A               Decal    `json:"rwd_decal_gears_s2_a,omitempty"`
	DecalGgA                    Decal    `json:"rwd_decal_gg_a,omitempty"`
	DecalGingerbreadA           Decal    `json:"decal_gingerbread_a,omitempty"`
	DecalHalloweenBatA          Decal    `json:"decal_halloween_bat_a,omitempty"`
	DecalHalloweenCatA          Decal    `json:"decal_halloween_cat_a,omitempty"`
	DecalHalloweenCauldronA     Decal    `json:"decal_halloween_cauldron_a,omitempty"`
	DecalHalloweenGhostA        Decal    `json:"decal_halloween_ghost_a,omitempty"`
	DecalHalloweenPumpkinA      Decal    `json:"decal_halloween_pumpkin_a,omitempty"`
	DecalHalloweenScytheA       Decal    `json:"decal_halloween_scythe_a,omitempty"`
	DecalHalloweenSkullA        Decal    `json:"decal_halloween_skull_a,omitempty"`
	DecalHalloweenZombieA       Decal    `json:"decal_halloween_zombie_a,omitempty"`
	DecalHamburgerA             Decal    `json:"decal_hamburger_a,omitempty"`
	DecalKoiFishA               Decal    `json:"decal_koi_fish_a,omitempty"`
	DecalKronosA                Decal    `json:"decal_kronos_a,omitempty"`
	DecalLightningBoltA         Decal    `json:"decal_lightning_bolt_a,omitempty"`
	DecalLoneEcho2_A            Decal    `json:"rwd_decal_lone_echo_2_a,omitempty"`
	DecalMusicNoteA             Decal    `json:"decal_music_note_a,omitempty"`
	DecalNarwhalA               Decal    `json:"rwd_decal_narwhal_a,omitempty"`
	DecalOculusA                Decal    `json:"decal_oculus_a,omitempty"  validate:"restricted"`
	DecalOneYearA               Decal    `json:"decal_one_year_a,omitempty" validate:"restricted"`
	DecalOniA                   Decal    `json:"rwd_decal_oni_a,omitempty"`
	DecalPenguinA               Decal    `json:"decal_penguin_a,omitempty"`
	DecalPepperA                Decal    `json:"rwd_decal_pepper_a,omitempty"`
	DecalPresentA               Decal    `json:"decal_present_a,omitempty"`
	DecalProfileWolfA           Decal    `json:"decal_profile_wolf_a,omitempty"`
	DecalQuestLaunchA           Decal    `json:"decal_quest_launch_a,omitempty" validate:"restricted"`
	DecalRadioactiveA           Decal    `json:"decal_radioactive_a,omitempty"`
	DecalRadioactiveBioA        Decal    `json:"decal_radioactive_bio_a,omitempty"`
	DecalRadLogo                Decal    `json:"decal_radlogo_a,omitempty" validate:"restricted"`
	DecalRageWolfA              Decal    `json:"decal_rage_wolf_a,omitempty"`
	DecalRamenA                 Decal    `json:"rwd_decal_ramen_a,omitempty"`
	DecalRayGunA                Decal    `json:"decal_ray_gun_a,omitempty"`
	DecalReindeerA              Decal    `json:"decal_reindeer_a,omitempty"`
	DecalRocketA                Decal    `json:"decal_rocket_a,omitempty"`
	DecalRoseA                  Decal    `json:"decal_rose_a,omitempty"`
	DecalSaltShakerA            Decal    `json:"decal_salt_shaker_a,omitempty"`
	DecalSantaCubesatA          Decal    `json:"decal_santa_cubesat_a,omitempty"`
	DecalSaturnA                Decal    `json:"decal_saturn_a,omitempty"`
	DecalScarabS2A              Decal    `json:"rwd_decal_scarab_s2_a,omitempty"`
	DecalSheldonA               Decal    `json:"decal_sheldon_a,omitempty"`
	DecalSkullA                 Decal    `json:"decal_skull_a,omitempty"`
	DecalSnowflakeA             Decal    `json:"decal_snowflake_a,omitempty"`
	DecalSnowmanA               Decal    `json:"decal_snowman_a,omitempty"`
	DecalSpartanA               Decal    `json:"rwd_decal_spartan_a,omitempty"`
	DecalSpiderA                Decal    `json:"decal_spider_a,omitempty"`
	DecalSummerPirateA          Decal    `json:"decal_summer_pirate_a,omitempty"`
	DecalSummerSharkA           Decal    `json:"decal_summer_shark_a,omitempty"`
	DecalSummerSubmarineA       Decal    `json:"decal_summer_submarine_a,omitempty"`
	DecalSummerWhaleA           Decal    `json:"decal_summer_whale_a,omitempty"`
	DecalSwordsA                Decal    `json:"decal_swords_a,omitempty"`
	DecalVRML                   Decal    `json:"decal_vrml_a,omitempty" validate:"restricted"`
	DecalWreathA                Decal    `json:"decal_wreath_a,omitempty"`
	Emissive0001                Emissive `json:"rwd_emissive_0001,omitempty"`
	Emissive0002                Emissive `json:"rwd_emissive_0002,omitempty"`
	Emissive0003                Emissive `json:"rwd_emissive_0003,omitempty"`
	Emissive0004                Emissive `json:"rwd_emissive_0004,omitempty"`
	Emissive0005                Emissive `json:"rwd_emissive_0005,omitempty"`
	Emissive0006                Emissive `json:"rwd_emissive_0006,omitempty"`
	Emissive0007                Emissive `json:"rwd_emissive_0007,omitempty"`
	Emissive0008                Emissive `json:"rwd_emissive_0008,omitempty"`
	Emissive0009                Emissive `json:"rwd_emissive_0009,omitempty"`
	Emissive0010                Emissive `json:"rwd_emissive_0010,omitempty"`
	Emissive0011                Emissive `json:"rwd_emissive_0011,omitempty"`
	Emissive0012                Emissive `json:"rwd_emissive_0012,omitempty"`
	Emissive0013                Emissive `json:"rwd_emissive_0013,omitempty"`
	Emissive0014                Emissive `json:"rwd_emissive_0014,omitempty"`
	Emissive0016                Emissive `json:"rwd_emissive_0016,omitempty"`
	Emissive0017                Emissive `json:"rwd_emissive_0017,omitempty" validate:"blocked"`
	Emissive0023                Emissive `json:"rwd_emissive_0023,omitempty"`
	Emissive0024                Emissive `json:"rwd_emissive_0024,omitempty"`
	Emissive0025                Emissive `json:"rwd_emissive_0025,omitempty"`
	Emissive0026                Emissive `json:"rwd_emissive_0026,omitempty"`
	Emissive0027                Emissive `json:"rwd_emissive_0027,omitempty" validate:"blocked"`
	Emissive0028                Emissive `json:"rwd_emissive_0028,omitempty"`
	Emissive0029                Emissive `json:"rwd_emissive_0029,omitempty"`
	Emissive0031                Emissive `json:"rwd_emissive_0031,omitempty" validate:"blocked"`
	Emissive0033                Emissive `json:"rwd_emissive_0033,omitempty" validate:"blocked"`
	Emissive0034                Emissive `json:"rwd_emissive_0034,omitempty" validate:"blocked"`
	Emissive0035                Emissive `json:"rwd_emissive_0035,omitempty" validate:"blocked"`
	Emissive0036                Emissive `json:"rwd_emissive_0036,omitempty" validate:"blocked"`
	Emissive0037                Emissive `json:"rwd_emissive_0037,omitempty" validate:"blocked"`
	Emissive0038                Emissive `json:"rwd_emissive_0038,omitempty" validate:"blocked"`
	Emissive0040                Emissive `json:"rwd_emissive_0040,omitempty" validate:"blocked"`
	EmissiveDefault             Emissive `json:"emissive_default,omitempty"`
	Emote0000                   Emote    `json:"rwd_emote_0000,omitempty"`
	Emote0001                   Emote    `json:"rwd_emote_0001,omitempty"`
	Emote0002                   Emote    `json:"rwd_emote_0002,omitempty"`
	Emote0005                   Emote    `json:"rwd_emote_0005,omitempty"`
	Emote0006                   Emote    `json:"rwd_emote_0006,omitempty"`
	Emote0007                   Emote    `json:"rwd_emote_0007,omitempty"`
	Emote0010                   Emote    `json:"rwd_emote_0010,omitempty"`
	Emote0011                   Emote    `json:"rwd_emote_0011,omitempty"`
	Emote0012                   Emote    `json:"rwd_emote_0012,omitempty"`
	Emote0016                   Emote    `json:"rwd_emote_0016,omitempty"`
	Emote0017                   Emote    `json:"rwd_emote_0017,omitempty"`
	Emote0018                   Emote    `json:"rwd_emote_0018,omitempty"`
	Emote0019                   Emote    `json:"rwd_emote_0019,omitempty"`
	Emote0020                   Emote    `json:"rwd_emote_0020,omitempty"`
	EmoteAngryFaceA             Emote    `json:"emote_angry_face_a,omitempty"`
	EmoteBatsA                  Emote    `json:"emote_bats_a,omitempty"`
	EmoteBatteryS1A             Emote    `json:"rwd_emote_battery_s1_a,omitempty"`
	EmoteBattleCryA             Emote    `json:"rwd_emote_battle_cry_a,omitempty"`
	EmoteBlinkSmileyA           Emote    `json:"emote_blink_smiley_a,omitempty"`
	EmoteBrokenHeartA           Emote    `json:"emote_broken_heart_a,omitempty"`
	EmoteClockA                 Emote    `json:"emote_clock_a,omitempty"`
	EmoteCoffeeS1A              Emote    `json:"rwd_emote_coffee_s1_a,omitempty"`
	EmoteCombatAnniversaryA     Emote    `json:"emote_combat_anniversary_a,omitempty"`
	EmoteCryingFaceA            Emote    `json:"emote_crying_face_a,omitempty"`
	EmoteDancingOctopusA        Emote    `json:"emote_dancing_octopus_a,omitempty"`
	EmoteDeadFaceA              Emote    `json:"emote_dead_face_a,omitempty"`
	EmoteDealGlassesA           Emote    `json:"emote_deal_glasses_a,omitempty"`
	EmoteDefault                Emote    `json:"emote_default,omitempty"`
	EmoteDing                   Emote    `json:"emote_ding,omitempty"`
	EmoteDizzyEyesA             Emote    `json:"emote_dizzy_eyes_a,omitempty"`
	EmoteDollarEyesA            Emote    `json:"emote_dollar_eyes_a,omitempty"`
	EmoteExclamationPointA      Emote    `json:"emote_exclamation_point_a,omitempty"`
	EmoteEyeRollA               Emote    `json:"emote_eye_roll_a,omitempty"`
	EmoteFireA                  Emote    `json:"emote_fire_a,omitempty"`
	EmoteFlyingHeartsA          Emote    `json:"emote_flying_hearts_a,omitempty"`
	EmoteGgA                    Emote    `json:"emote_gg_a,omitempty"`
	EmoteGingerbreadManA        Emote    `json:"emote_gingerbread_man_a,omitempty"`
	EmoteHeartEyesA             Emote    `json:"emote_heart_eyes_a,omitempty"`
	EmoteHourglassA             Emote    `json:"emote_hourglass_a,omitempty"`
	EmoteKissyLipsA             Emote    `json:"emote_kissy_lips_a,omitempty"`
	EmoteLightbulbA             Emote    `json:"emote_lightbulb_a,omitempty"`
	EmoteLightningA             Emote    `json:"rwd_emote_lightning_a,omitempty"`
	EmoteLoadingA               Emote    `json:"emote_loading_a,omitempty"`
	EmoteMeteorS1A              Emote    `json:"rwd_emote_meteor_s1_a,omitempty"`
	EmoteMoneyBagA              Emote    `json:"emote_money_bag_a,omitempty"`
	EmoteMoustacheA             Emote    `json:"emote_moustache_a,omitempty"`
	EmoteOneA                   Emote    `json:"emote_one_a,omitempty"`
	EmotePizzaDance             Emote    `json:"emote_pizza_dance,omitempty"`
	EmotePresentA               Emote    `json:"emote_present_a,omitempty"`
	EmotePumpkinFaceA           Emote    `json:"emote_pumpkin_face_a,omitempty"`
	EmoteQuestionMarkA          Emote    `json:"emote_question_mark_a,omitempty"`
	EmoteReticleA               Emote    `json:"emote_reticle_a,omitempty"`
	EmoteRIPA                   Emote    `json:"emote_rip_a,omitempty"`
	EmoteSamuraiMaskA           Emote    `json:"rwd_emote_samurai_mask_a,omitempty"`
	EmoteScaredA                Emote    `json:"emote_scared_a,omitempty"`
	EmoteShiftyEyesS2A          Emote    `json:"emote_shifty_eyes_s2_a,omitempty"`
	EmoteSickFaceA              Emote    `json:"emote_sick_face_a,omitempty"`
	EmoteSkullCrossbonesA       Emote    `json:"emote_skull_crossbones_a,omitempty"`
	EmoteSleepyZzzA             Emote    `json:"emote_sleepy_zzz_a,omitempty"`
	EmoteSmirkFaceA             Emote    `json:"emote_smirk_face_a,omitempty"`
	EmoteSnowGlobeA             Emote    `json:"emote_snow_globe_a,omitempty"`
	EmoteSnowmanA               Emote    `json:"emote_snowman_a,omitempty"`
	EmoteSoundWaveS2A           Emote    `json:"emote_sound_wave_s2_a,omitempty"`
	EmoteSpiderA                Emote    `json:"emote_spider_a,omitempty"`
	EmoteStarEyesA              Emote    `json:"emote_star_eyes_a,omitempty"`
	EmoteStarSparklesA          Emote    `json:"emote_star_sparkles_a,omitempty"`
	EmoteStinkyPoopA            Emote    `json:"emote_stinky_poop_a,omitempty"`
	EmoteTearDropA              Emote    `json:"emote_tear_drop_a,omitempty"`
	EmoteUwuS2A                 Emote    `json:"emote_uwu_s2_a,omitempty"`
	EmoteVRMLA                  Emote    `json:"rwd_emote_vrml_a,omitempty" validate:"blocked"`
	EmoteWifiSymbolA            Emote    `json:"emote_wifi_symbol_a,omitempty"`
	EmoteWinkyTongueA           Emote    `json:"emote_winky_tongue_a,omitempty"`
	GoalFx0002                  Goal     `json:"rwd_goal_fx_0002,omitempty"`
	GoalFx0003                  Goal     `json:"rwd_goal_fx_0003,omitempty"`
	GoalFx0004                  Goal     `json:"rwd_goal_fx_0004,omitempty"`
	GoalFx0005                  Goal     `json:"rwd_goal_fx_0005,omitempty"`
	GoalFx0008                  Goal     `json:"rwd_goal_fx_0008,omitempty"`
	GoalFx0009                  Goal     `json:"rwd_goal_fx_0009,omitempty" validate:"blocked"`
	GoalFx0010                  Goal     `json:"rwd_goal_fx_0010,omitempty"`
	GoalFx0011                  Goal     `json:"rwd_goal_fx_0011,omitempty"`
	GoalFx0012                  Goal     `json:"rwd_goal_fx_0012,omitempty"`
	GoalFx0013                  Goal     `json:"rwd_goal_fx_0013,omitempty"`
	GoalFx0015                  Goal     `json:"rwd_goal_fx_0015,omitempty"`
	GoalFxDefault               Goal     `json:"rwd_goal_fx_default,omitempty"`
	HeraldryDefault             Heraldry `json:"heraldry_default,omitempty"`
	LoadoutNumber               Loadout  `json:"loadout_number,omitempty"`
	Medal0000                   Medal    `json:"rwd_medal_0000,omitempty"`
	Medal0001                   Medal    `json:"rwd_medal_0001,omitempty"`
	Medal0002                   Medal    `json:"rwd_medal_0002,omitempty"`
	Medal0003                   Medal    `json:"rwd_medal_0003,omitempty"`
	Medal0004                   Medal    `json:"rwd_medal_0004,omitempty"`
	Medal0005                   Medal    `json:"rwd_medal_0005,omitempty"`
	Medal0006                   Medal    `json:"rwd_medal_0006,omitempty" validate:"blocked"`
	Medal0007                   Medal    `json:"rwd_medal_0007,omitempty" validate:"blocked"`
	Medal0008                   Medal    `json:"rwd_medal_0008,omitempty" validate:"blocked"`
	Medal0009                   Medal    `json:"rwd_medal_0009,omitempty"`
	Medal0010                   Medal    `json:"rwd_medal_0010,omitempty"`
	Medal0011                   Medal    `json:"rwd_medal_0011,omitempty"`
	Medal0012                   Medal    `json:"rwd_medal_0012,omitempty"`
	Medal0013                   Medal    `json:"rwd_medal_0013,omitempty"`
	Medal0014                   Medal    `json:"rwd_medal_0014,omitempty"`
	Medal0015                   Medal    `json:"rwd_medal_0015,omitempty"`
	Medal0016                   Medal    `json:"rwd_medal_0016,omitempty"`
	Medal0017                   Medal    `json:"rwd_medal_0017,omitempty" validate:"blocked"`
	MedalDefault                Medal    `json:"rwd_medal_default,omitempty"`
	MedalLoneEcho2_A            Medal    `json:"rwd_medal_lone_echo_2_a,omitempty"`
	MedalS1ArenaBronze          Medal    `json:"rwd_medal_s1_arena_bronze,omitempty"`
	MedalS1ArenaGold            Medal    `json:"rwd_medal_s1_arena_gold,omitempty" `
	MedalS1ArenaSilver          Medal    `json:"rwd_medal_s1_arena_silver,omitempty" `
	MedalS1EchoPassBronze       Medal    `json:"rwd_medal_s1_echo_pass_bronze,omitempty" `
	MedalS1EchoPassGold         Medal    `json:"rwd_medal_s1_echo_pass_gold,omitempty" `
	MedalS1EchoPassSilver       Medal    `json:"rwd_medal_s1_echo_pass_silver,omitempty" `
	MedalS1QuestLaunch          Medal    `json:"rwd_medal_s1_quest_launch,omitempty" `
	MedalS2EchoPassBronze       Medal    `json:"rwd_medal_s2_echo_pass_bronze,omitempty" `
	MedalS2EchoPassGold         Medal    `json:"rwd_medal_s2_echo_pass_gold,omitempty" `
	MedalS2EchoPassSilver       Medal    `json:"rwd_medal_s2_echo_pass_silver,omitempty" `
	MedalS3EchoPassBronzeA      Medal    `json:"rwd_medal_s3_echo_pass_bronze_a,omitempty" `
	MedalS3EchoPassGoldA        Medal    `json:"rwd_medal_s3_echo_pass_gold_a,omitempty" `
	MedalS3EchoPassSilverA      Medal    `json:"rwd_medal_s3_echo_pass_silver_a,omitempty" `
	MedalVrmlPreseason          Medal    `json:"rwd_medal_s1_vrml_preseason,omitempty" validate:"restricted"`
	MedalVrmlS1                 Medal    `json:"rwd_medal_s1_vrml_s1_user,omitempty" validate:"restricted"`
	MedalVrmlS1Champion         Medal    `json:"rwd_medal_s1_vrml_s1_champion,omitempty" validate:"restricted"`
	MedalVrmlS1Finalist         Medal    `json:"rwd_medal_s1_vrml_s1_finalist,omitempty" validate:"restricted"`
	MedalVrmlS2                 Medal    `json:"rwd_medal_s1_vrml_s2,omitempty" validate:"restricted"`
	MedalVrmlS2Champion         Medal    `json:"rwd_medal_s1_vrml_s2_champion,omitempty" validate:"restricted"`
	MedalVrmlS2Finalist         Medal    `json:"rwd_medal_s1_vrml_s2_finalist,omitempty" validate:"restricted"`
	MedalVrmlS3                 Medal    `json:"rwd_medal_s1_vrml_s3,omitempty" validate:"restricted"`
	MedalVrmlS3Champion         Medal    `json:"rwd_medal_s1_vrml_s3_champion,omitempty" validate:"restricted"`
	MedalVrmlS3Finalist         Medal    `json:"rwd_medal_s1_vrml_s3_finalist,omitempty" validate:"restricted"`
	Pattern0000                 Pattern  `json:"rwd_pattern_0000,omitempty"`
	Pattern0001                 Pattern  `json:"rwd_pattern_0001,omitempty"`
	Pattern0002                 Pattern  `json:"rwd_pattern_0002,omitempty"`
	Pattern0005                 Pattern  `json:"rwd_pattern_0005,omitempty"`
	Pattern0006                 Pattern  `json:"rwd_pattern_0006,omitempty"`
	Pattern0008                 Pattern  `json:"rwd_pattern_0008,omitempty"`
	Pattern0009                 Pattern  `json:"rwd_pattern_0009,omitempty"`
	Pattern0010                 Pattern  `json:"rwd_pattern_0010,omitempty"`
	Pattern0011                 Pattern  `json:"rwd_pattern_0011,omitempty"`
	Pattern0012                 Pattern  `json:"rwd_pattern_0012,omitempty"`
	Pattern0016                 Pattern  `json:"rwd_pattern_0016,omitempty"`
	Pattern0017                 Pattern  `json:"rwd_pattern_0017,omitempty"`
	Pattern0018                 Pattern  `json:"rwd_pattern_0018,omitempty"`
	Pattern0019                 Pattern  `json:"rwd_pattern_0019,omitempty"`
	Pattern0020                 Pattern  `json:"rwd_pattern_0020,omitempty"`
	Pattern0021                 Pattern  `json:"rwd_pattern_0021,omitempty"`
	PatternAlienA               Pattern  `json:"rwd_pattern_alien_a,omitempty"`
	PatternAnglesA              Pattern  `json:"pattern_angles_a,omitempty"`
	PatternArrowheadsA          Pattern  `json:"pattern_arrowheads_a,omitempty"`
	PatternBananasA             Pattern  `json:"pattern_bananas_a,omitempty"`
	PatternCatsA                Pattern  `json:"pattern_cats_a,omitempty"`
	PatternChevronA             Pattern  `json:"pattern_chevron_a,omitempty"`
	PatternCircuitBoardA        Pattern  `json:"rwd_pattern_circuit_board_a,omitempty"`
	PatternCubesA               Pattern  `json:"pattern_cubes_a,omitempty"`
	PatternCupcakeA             Pattern  `json:"rwd_pattern_cupcake_a,omitempty"`
	PatternDefault              Pattern  `json:"pattern_default,omitempty"`
	PatternDiamondPlateA        Pattern  `json:"pattern_diamond_plate_a,omitempty"`
	PatternDiamondsA            Pattern  `json:"pattern_diamonds_a,omitempty"`
	PatternDigitalCamoA         Pattern  `json:"pattern_digital_camo_a,omitempty"`
	PatternDotsA                Pattern  `json:"pattern_dots_a,omitempty"`
	PatternDumbbellsA           Pattern  `json:"pattern_dumbbells_a,omitempty"`
	PatternGearsA               Pattern  `json:"pattern_gears_a,omitempty"`
	PatternHamburgerA           Pattern  `json:"rwd_pattern_hamburger_a,omitempty"`
	PatternHawaiianA            Pattern  `json:"pattern_hawaiian_a,omitempty"`
	PatternHoneycombTripleA     Pattern  `json:"pattern_honeycomb_triple_a,omitempty"`
	PatternInsetCubesA          Pattern  `json:"pattern_inset_cubes_a,omitempty"`
	PatternLeopardA             Pattern  `json:"pattern_leopard_a,omitempty"`
	PatternLightningA           Pattern  `json:"pattern_lightning_a,omitempty"`
	PatternPawsA                Pattern  `json:"pattern_paws_a,omitempty"`
	PatternPineappleA           Pattern  `json:"pattern_pineapple_a,omitempty"`
	PatternPizzaA               Pattern  `json:"rwd_pattern_pizza_a,omitempty"`
	PatternQuestA               Pattern  `json:"rwd_pattern_quest_a,omitempty"`
	PatternRageWolfA            Pattern  `json:"rwd_pattern_rage_wolf_a,omitempty"`
	PatternRocketA              Pattern  `json:"rwd_pattern_rocket_a,omitempty"`
	PatternS1A                  Pattern  `json:"rwd_pattern_s1_a,omitempty"`
	PatternS1B                  Pattern  `json:"rwd_pattern_s1_b,omitempty"`
	PatternS1C                  Pattern  `json:"rwd_pattern_s1_c,omitempty"`
	PatternS1D                  Pattern  `json:"rwd_pattern_s1_d,omitempty"`
	PatternS2A                  Pattern  `json:"rwd_pattern_s2_a,omitempty"`
	PatternS2B                  Pattern  `json:"rwd_pattern_s2_b,omitempty"`
	PatternS2C                  Pattern  `json:"rwd_pattern_s2_c,omitempty"`
	PatternSaltA                Pattern  `json:"rwd_pattern_salt_a,omitempty"`
	PatternScalesA              Pattern  `json:"pattern_scales_a,omitempty"`
	PatternSeigaihaA            Pattern  `json:"rwd_pattern_seigaiha_a,omitempty"`
	PatternSkullA               Pattern  `json:"rwd_pattern_skull_a,omitempty"`
	PatternSpearShieldA         Pattern  `json:"rwd_pattern_spear_shield_a,omitempty"`
	PatternSpookyBandagesA      Pattern  `json:"pattern_spooky_bandages_a,omitempty"`
	PatternSpookyBatsA          Pattern  `json:"pattern_spooky_bats_a,omitempty"`
	PatternSpookyCobwebA        Pattern  `json:"pattern_spooky_cobweb_a,omitempty"`
	PatternSpookyPumpkinsA      Pattern  `json:"pattern_spooky_pumpkins_a,omitempty"`
	PatternSpookySkullsA        Pattern  `json:"pattern_spooky_skulls_a,omitempty"`
	PatternSpookyStitchesA      Pattern  `json:"pattern_spooky_stitches_a,omitempty"`
	PatternSquigglesA           Pattern  `json:"pattern_squiggles_a,omitempty"`
	PatternStarsA               Pattern  `json:"pattern_stars_a,omitempty"`
	PatternStreaksA             Pattern  `json:"pattern_streaks_a,omitempty"`
	PatternStringsA             Pattern  `json:"pattern_strings_a,omitempty"`
	PatternSummerHawaiianA      Pattern  `json:"pattern_summer_hawaiian_a,omitempty"`
	PatternSwirlA               Pattern  `json:"pattern_swirl_a,omitempty"`
	PatternTableclothA          Pattern  `json:"pattern_tablecloth_a,omitempty"`
	PatternTigerA               Pattern  `json:"pattern_tiger_a,omitempty"`
	PatternTreadsA              Pattern  `json:"pattern_treads_a,omitempty"`
	PatternTrexSkullA           Pattern  `json:"rwd_pattern_trex_skull_a,omitempty"`
	PatternTrianglesA           Pattern  `json:"pattern_triangles_a,omitempty"`
	PatternWeaveA               Pattern  `json:"pattern_weave_a,omitempty"`
	PatternXmasFlourishA        Pattern  `json:"pattern_xmas_flourish_a,omitempty"`
	PatternXmasKnitA            Pattern  `json:"pattern_xmas_knit_a,omitempty"`
	PatternXmasKnitFlowersA     Pattern  `json:"pattern_xmas_knit_flowers_a,omitempty"`
	PatternXmasLightsA          Pattern  `json:"pattern_xmas_lights_a,omitempty"`
	PatternXmasMistletoeA       Pattern  `json:"pattern_xmas_mistletoe_a,omitempty"`
	PatternXmasSnowflakesA      Pattern  `json:"pattern_xmas_snowflakes_a,omitempty"`
	Pip0001                     Pip      `json:"rwd_pip_0001,omitempty"`
	Pip0005                     Pip      `json:"rwd_pip_0005,omitempty"`
	Pip0006                     Pip      `json:"rwd_pip_0006,omitempty"`
	Pip0007                     Pip      `json:"rwd_pip_0007,omitempty"`
	Pip0008                     Pip      `json:"rwd_pip_0008,omitempty"`
	Pip0009                     Pip      `json:"rwd_pip_0009,omitempty"`
	Pip0010                     Pip      `json:"rwd_pip_0010,omitempty"`
	Pip0011                     Pip      `json:"rwd_pip_0011,omitempty"`
	Pip0013                     Pip      `json:"rwd_pip_0013,omitempty"`
	Pip0014                     Pip      `json:"rwd_pip_0014,omitempty"`
	Pip0015                     Pip      `json:"rwd_pip_0015,omitempty"`
	Pip0017                     Pip      `json:"rwd_pip_0017,omitempty"`
	Pip0018                     Pip      `json:"rwd_pip_0018,omitempty"`
	Pip0019                     Pip      `json:"rwd_pip_0019,omitempty"`
	Pip0020                     Pip      `json:"rwd_pip_0020,omitempty"`
	Pip0021                     Pip      `json:"rwd_pip_0021,omitempty"`
	Pip0022                     Pip      `json:"rwd_pip_0022,omitempty"`
	Pip0023                     Pip      `json:"rwd_pip_0023,omitempty"`
	Pip0024                     Pip      `json:"rwd_pip_0024,omitempty"`
	Pip0025                     Pip      `json:"rwd_pip_0025,omitempty"`
	PipStubUi                   Pip      `json:"rwd_pip_0026,omitempty" validate:"blocked"`
	PlaceholderBooster          Booster  `json:"rwd_booster_ranger_a,omitempty" validate:"blocked"`
	PlaceholderBracer           Bracer   `json:"rwd_bracer_ranger_a,omitempty" validate:"blocked"`
	PlaceholderChassis          Chassis  `json:"rwd_chassis_ranger_a,omitempty" validate:"blocked"`
	RWDBanner0004               Banner   `json:"rwd_banner_0004,omitempty"`
	RWDBanner0005               Banner   `json:"rwd_banner_0005,omitempty"`
	RWDBanner0006               Banner   `json:"rwd_banner_0006,omitempty"`
	RWDBanner0007               Banner   `json:"rwd_banner_0007,omitempty"`
	RWDBanner0012               Banner   `json:"rwd_banner_0012,omitempty"`
	RWDBanner0013               Banner   `json:"rwd_banner_0013,omitempty"`
	RWDBanner0018               Banner   `json:"rwd_banner_0018,omitempty"`
	RWDBanner0019               Banner   `json:"rwd_banner_0019,omitempty"`
	RWDBanner0020               Banner   `json:"rwd_banner_0020,omitempty"`
	RWDBanner0026               Banner   `json:"rwd_banner_0026,omitempty"`
	RWDBanner0027               Banner   `json:"rwd_banner_0027,omitempty"`
	RWDBanner0028               Banner   `json:"rwd_banner_0028,omitempty"`
	RWDBanner0029               Banner   `json:"rwd_banner_0029,omitempty"`
	RWDBannerS2Lines            Banner   `json:"rwd_banner_s2_lines,omitempty"`
	RWDBoosterAvianA            Booster  `json:"rwd_booster_avian_a,omitempty"`
	RWDBoosterBaroqueA          Booster  `json:"rwd_booster_baroque_a,omitempty"`
	RWDBoosterExoA              Booster  `json:"rwd_booster_exo_a,omitempty"`
	RWDBoosterFlamingoA         Booster  `json:"rwd_booster_flamingo_a,omitempty"`
	RWDBoosterFragmentA         Booster  `json:"rwd_booster_fragment_a,omitempty"`
	RWDBoosterFrostA            Booster  `json:"rwd_booster_frost_a,omitempty"`
	RWDBoosterHalloweenA        Booster  `json:"rwd_booster_halloween_a,omitempty"`
	RWDBoosterHeartbreakA       Booster  `json:"rwd_booster_heartbreak_a,omitempty"`
	RWDBoosterLavaA             Booster  `json:"rwd_booster_lava_a,omitempty"`
	RWDBoosterMechA             Booster  `json:"rwd_booster_mech_a,omitempty"`
	RWDBoosterNinjaA            Booster  `json:"rwd_booster_ninja_a,omitempty"`
	RWDBoosterOrganicA          Booster  `json:"rwd_booster_organic_a,omitempty"`
	RWDBoosterOvergrownA        Booster  `json:"rwd_booster_overgrown_a,omitempty"`
	RWDBoosterPaladinA          Booster  `json:"rwd_booster_paladin_a,omitempty"`
	RWDBoosterReptileA          Booster  `json:"rwd_booster_reptile_a,omitempty"`
	RWDBoosterRetroA            Booster  `json:"rwd_booster_retro_a,omitempty"`
	RWDBoosterSamuraiAOni       Booster  `json:"rwd_booster_samurai_a_oni,omitempty"`
	RWDBoosterSnacktimeA        Booster  `json:"rwd_booster_snacktime_a,omitempty"`
	RWDBoosterSpeedformA        Booster  `json:"rwd_booster_speedform_a,omitempty"`
	RWDBoosterTrexASkelerex     Booster  `json:"rwd_booster_trex_a_skelerex,omitempty"`
	RWDBoosterVroomA            Booster  `json:"rwd_booster_vroom_a,omitempty"`
	RWDBoosterWolfA             Booster  `json:"rwd_booster_wolf_a,omitempty"`
	RWDBracerAvianA             Bracer   `json:"rwd_bracer_avian_a,omitempty"`
	RWDBracerBaroqueA           Bracer   `json:"rwd_bracer_baroque_a,omitempty"`
	RWDBracerExoA               Bracer   `json:"rwd_bracer_exo_a,omitempty"`
	RWDBracerFlamingoA          Bracer   `json:"rwd_bracer_flamingo_a,omitempty"`
	RWDBracerFragmentA          Bracer   `json:"rwd_bracer_fragment_a,omitempty"`
	RWDBracerFrostA             Bracer   `json:"rwd_bracer_frost_a,omitempty"`
	RWDBracerHalloweenA         Bracer   `json:"rwd_bracer_halloween_a,omitempty"`
	RWDBracerHeartbreakA        Bracer   `json:"rwd_bracer_heartbreak_a,omitempty"`
	RWDBracerLavaA              Bracer   `json:"rwd_bracer_lava_a,omitempty"`
	RWDBracerMechA              Bracer   `json:"rwd_bracer_mech_a,omitempty"`
	RWDBracerNinjaA             Bracer   `json:"rwd_bracer_ninja_a,omitempty"`
	RWDBracerOrganicA           Bracer   `json:"rwd_bracer_organic_a,omitempty"`
	RWDBracerOvergrownA         Bracer   `json:"rwd_bracer_overgrown_a,omitempty"`
	RWDBracerPaladinA           Bracer   `json:"rwd_bracer_paladin_a,omitempty"`
	RWDBracerReptileA           Bracer   `json:"rwd_bracer_reptile_a,omitempty"`
	RWDBracerRetroA             Bracer   `json:"rwd_bracer_retro_a,omitempty"`
	RWDBracerSamuraiAOni        Bracer   `json:"rwd_bracer_samurai_a_oni,omitempty"`
	RWDBracerSpeedformA         Bracer   `json:"rwd_bracer_speedform_a,omitempty"`
	RWDBracerTrexASkelerex      Bracer   `json:"rwd_bracer_trex_a_skelerex,omitempty"`
	RWDBracerVroomA             Bracer   `json:"rwd_bracer_vroom_a,omitempty"`
	RWDBracerWolfA              Bracer   `json:"rwd_bracer_wolf_a,omitempty"`
	RWDChassisExoA              Chassis  `json:"rwd_chassis_exo_a,omitempty"`
	RWDChassisFrostA            Chassis  `json:"rwd_chassis_frost_a,omitempty"`
	RWDChassisNinjaA            Chassis  `json:"rwd_chassis_ninja_a,omitempty"`
	RWDChassisOvergrownA        Chassis  `json:"rwd_chassis_overgrown_a,omitempty"`
	RWDChassisSamuraiAOni       Chassis  `json:"rwd_chassis_samurai_a_oni,omitempty"`
	RWDChassisSportyA           Chassis  `json:"rwd_chassis_sporty_a,omitempty"`
	RWDChassisTrexASkelerex     Chassis  `json:"rwd_chassis_trex_a_skelerex,omitempty"`
	RWDChassisWolfA             Chassis  `json:"rwd_chassis_wolf_a,omitempty"`
	RWDCurrencyStarterPack01    Currency `json:"rwd_currency_starter_pack_01,omitempty"`
	RWDDecal0003                Decal    `json:"rwd_decal_0003,omitempty"`
	RWDDecal0004                Decal    `json:"rwd_decal_0004,omitempty"`
	RWDDecal0008                Decal    `json:"rwd_decal_0008,omitempty"`
	RWDDecal0009                Decal    `json:"rwd_decal_0009,omitempty"`
	RWDDecal0013                Decal    `json:"rwd_decal_0013,omitempty"`
	RWDDecal0014                Decal    `json:"rwd_decal_0014,omitempty"`
	RWDDecal0021                Decal    `json:"rwd_decal_0021,omitempty"`
	RWDDecal0022                Decal    `json:"rwd_decal_0022,omitempty"`
	RWDEmissive0015             Emissive `json:"rwd_emissive_0015,omitempty"`
	RWDEmissive0018             Emissive `json:"rwd_emissive_0018,omitempty"`
	RWDEmissive0019             Emissive `json:"rwd_emissive_0019,omitempty"`
	RWDEmissive0020             Emissive `json:"rwd_emissive_0020,omitempty"`
	RWDEmissive0021             Emissive `json:"rwd_emissive_0021,omitempty"`
	RWDEmissive0022             Emissive `json:"rwd_emissive_0022,omitempty"`
	RWDEmissive0030             Emissive `json:"rwd_emissive_0030,omitempty"`
	RWDEmissive0032             Emissive `json:"rwd_emissive_0032,omitempty"`
	RWDEmissive0039             Emissive `json:"rwd_emissive_0039,omitempty"`
	RWDEmote0003                Emote    `json:"rwd_emote_0003,omitempty"`
	RWDEmote0004                Emote    `json:"rwd_emote_0004,omitempty"`
	RWDEmote0008                Emote    `json:"rwd_emote_0008,omitempty"`
	RWDEmote0009                Emote    `json:"rwd_emote_0009,omitempty"`
	RWDEmote0013                Emote    `json:"rwd_emote_0013,omitempty"`
	RWDEmote0014                Emote    `json:"rwd_emote_0014,omitempty"`
	RWDEmoteGhost               Emote    `json:"rwd_emote_0015,omitempty" validate:"restricted"`
	RWDEmote0021                Emote    `json:"rwd_emote_0021,omitempty"`
	RWDEmote0022                Emote    `json:"rwd_emote_0022,omitempty"`
	RWDEmote0023                Emote    `json:"rwd_emote_0023,omitempty"`
	RWDEmote0024                Emote    `json:"rwd_emote_0024,omitempty"`
	RWDGoalFx0001               Goal     `json:"rwd_goal_fx_0001,omitempty"`
	RWDGoalFx0006               Goal     `json:"rwd_goal_fx_0006,omitempty"`
	RWDGoalFx0007               Goal     `json:"rwd_goal_fx_0007,omitempty"`
	RWDGoalFx0014               Goal     `json:"rwd_goal_fx_0014,omitempty"`
	RWDPattern0003              Pattern  `json:"rwd_pattern_0003,omitempty"`
	RWDPattern0004              Pattern  `json:"rwd_pattern_0004,omitempty"`
	RWDPattern0007              Pattern  `json:"rwd_pattern_0007,omitempty"`
	RWDPip0002                  Pip      `json:"rwd_pip_0002,omitempty"`
	RWDPip0003                  Pip      `json:"rwd_pip_0003,omitempty"`
	RWDPip0004                  Pip      `json:"rwd_pip_0004,omitempty"`
	RWDPip0012                  Pip      `json:"rwd_pip_0012,omitempty"`
	RWDPip0016                  Pip      `json:"rwd_pip_0016,omitempty"`
	RWDTag0016                  Tag      `json:"rwd_tag_0016,omitempty"`
	RWDTag0017                  Tag      `json:"rwd_tag_0017,omitempty"`
	RWDTagS1RSecondary          Tag      `json:"rwd_tag_s1_r_secondary,omitempty"`
	RWDTint0003                 Tint     `json:"rwd_tint_0003,omitempty"`
	RWDTint0004                 Tint     `json:"rwd_tint_0004,omitempty"`
	RWDTint0005                 Tint     `json:"rwd_tint_0005,omitempty"`
	RWDTint0006                 Tint     `json:"rwd_tint_0006,omitempty"`
	RWDTint0010                 Tint     `json:"rwd_tint_0010,omitempty"`
	RWDTint0011                 Tint     `json:"rwd_tint_0011,omitempty"`
	RWDTint0012                 Tint     `json:"rwd_tint_0012,omitempty"`
	RWDTint0016                 Tint     `json:"rwd_tint_0016,omitempty"`
	RWDTint0017                 Tint     `json:"rwd_tint_0017,omitempty"`
	RWDTint0018                 Tint     `json:"rwd_tint_0018,omitempty"`
	RWDTint0028                 Tint     `json:"rwd_tint_0028,omitempty"`
	RWDTint0029                 Tint     `json:"rwd_tint_0029,omitempty"`
	RWDTintS1BDefault           Tint     `json:"rwd_tint_s1_b_default,omitempty"`
	RWDTintS2ADefault           Tint     `json:"rwd_tint_s2_a_default,omitempty"`
	RWDTitle0003                Title    `json:"rwd_title_0003,omitempty"`
	RWDTitle0016                Title    `json:"rwd_title_0016,omitempty"`
	RWDTitle0017                Title    `json:"rwd_title_0017,omitempty"`
	RWDTitle0018                Title    `json:"rwd_title_0018,omitempty"`
	RWDTitle0019                Title    `json:"rwd_title_0019,omitempty"`
	StubMedal0018               Medal    `json:"rwd_medal_0018,omitempty" validate:"blocked"`
	StubMedal0019               Medal    `json:"rwd_medal_0019,omitempty" validate:"blocked"`
	StubMedal0026               Medal    `json:"rwd_medal_0026,omitempty" validate:"blocked"`
	StubPattern0013             Pattern  `json:"rwd_pattern_0013,omitempty" validate:"blocked"`
	StubPattern0014             Pattern  `json:"rwd_pattern_0014,omitempty" validate:"blocked"`
	StubPattern0015             Pattern  `json:"rwd_pattern_0015,omitempty" validate:"blocked"`
	StubPattern0022             Pattern  `json:"rwd_pattern_0022,omitempty"  validate:"blocked"`
	Tag0000                     Tag      `json:"rwd_tag_0000,omitempty" `
	Tag0001                     Tag      `json:"rwd_tag_0001,omitempty"`
	Tag0003                     Tag      `json:"rwd_tag_0003,omitempty"`
	Tag0004                     Tag      `json:"rwd_tag_0004,omitempty"`
	Tag0005                     Tag      `json:"rwd_tag_0005,omitempty"`
	Tag0006                     Tag      `json:"rwd_tag_0006,omitempty"`
	Tag0007                     Tag      `json:"rwd_tag_0007,omitempty"`
	Tag0012                     Tag      `json:"rwd_tag_0012,omitempty" validate:"blocked"`
	Tag0013                     Tag      `json:"rwd_tag_0013,omitempty" validate:"blocked"`
	Tag0014                     Tag      `json:"rwd_tag_0014,omitempty" validate:"blocked"`
	Tag0015                     Tag      `json:"rwd_tag_0015,omitempty" validate:"blocked"`
	Tag0018                     Tag      `json:"rwd_tag_0018,omitempty" validate:"blocked"`
	Tag0019                     Tag      `json:"rwd_tag_0019,omitempty" validate:"blocked"`
	Tag0020                     Tag      `json:"rwd_tag_0020,omitempty" validate:"blocked"`
	Tag0021                     Tag      `json:"rwd_tag_0021,omitempty" validate:"blocked"`
	Tag0023                     Tag      `json:"rwd_tag_0023,omitempty" validate:"blocked"`
	Tag0025                     Tag      `json:"rwd_tag_0025,omitempty" validate:"blocked"`
	Tag0026                     Tag      `json:"rwd_tag_0026,omitempty" validate:"blocked"`
	Tag0027                     Tag      `json:"rwd_tag_0027,omitempty" validate:"blocked"`
	Tag0028                     Tag      `json:"rwd_tag_0028,omitempty" validate:"blocked"`
	Tag0029                     Tag      `json:"rwd_tag_0029,omitempty" validate:"blocked"`
	Tag0030                     Tag      `json:"rwd_tag_0030,omitempty" validate:"blocked"`
	Tag0031                     Tag      `json:"rwd_tag_0031,omitempty" validate:"blocked"`
	Tag0033                     Tag      `json:"rwd_tag_0033,omitempty" validate:"blocked"`
	Tag0034                     Tag      `json:"rwd_tag_0034,omitempty" validate:"blocked"`
	Tag0038                     Tag      `json:"rwd_tag_0038,omitempty" validate:"blocked"`
	Tag0039                     Tag      `json:"rwd_tag_0039,omitempty" validate:"blocked"`
	TagDefault                  Tag      `json:"rwd_tag_default,omitempty"`
	TagDeveloper                Tag      `json:"rwd_tag_s1_developer,omitempty" validate:"restricted"`
	TagDiamonds                 Tag      `json:"rwd_tag_diamonds_a,omitempty"`
	TagGameAdmin                Tag      `json:"rwd_tag_0011,omitempty" validate:"restricted"`
	TagLoneEcho2                Tag      `json:"rwd_tag_lone_echo_2_a,omitempty"`
	TagS1ASecondary             Tag      `json:"rwd_tag_s1_a_secondary,omitempty"`
	TagS1BSecondary             Tag      `json:"rwd_tag_s1_b_secondary,omitempty"`
	TagS1CSecondary             Tag      `json:"rwd_tag_s1_c_secondary,omitempty"`
	TagS1DSecondary             Tag      `json:"rwd_tag_s1_d_secondary,omitempty"`
	TagS1ESecondary             Tag      `json:"rwd_tag_s1_e_secondary,omitempty"`
	TagS1FSecondary             Tag      `json:"rwd_tag_s1_f_secondary,omitempty"`
	TagS1GSecondary             Tag      `json:"rwd_tag_s1_g_secondary,omitempty"`
	TagS1HSecondary             Tag      `json:"rwd_tag_s1_h_secondary,omitempty"`
	TagS1ISecondary             Tag      `json:"rwd_tag_s1_i_secondary,omitempty"`
	TagS1JSecondary             Tag      `json:"rwd_tag_s1_j_secondary,omitempty"`
	TagS1KSecondary             Tag      `json:"rwd_tag_s1_k_secondary,omitempty"`
	TagS1MSecondary             Tag      `json:"rwd_tag_s1_m_secondary,omitempty"`
	TagS1OSecondary             Tag      `json:"rwd_tag_s1_o_secondary,omitempty"`
	TagS1QSecondary             Tag      `json:"rwd_tag_s1_q_secondary,omitempty"`
	TagS1TSecondary             Tag      `json:"rwd_tag_s1_t_secondary,omitempty"`
	TagS1VSecondary             Tag      `json:"rwd_tag_s1_v_secondary,omitempty"`
	TagS2BSecondary             Tag      `json:"rwd_tag_s2_b_secondary,omitempty"`
	TagS2C                      Tag      `json:"rwd_tag_s2_c,omitempty"`
	TagS2GSecondary             Tag      `json:"rwd_tag_s2_g_secondary,omitempty"`
	TagS2HSecondary             Tag      `json:"rwd_tag_s2_h_secondary,omitempty"`
	TagSpearA                   Tag      `json:"rwd_tag_spear_a,omitempty"`
	TagStub0002                 Tag      `json:"rwd_tag_0002,omitempty" validate:"blocked"`
	TagStub0032                 Tag      `json:"rwd_tag_0032,omitempty" validate:"blocked"`
	TagStub0046                 Tag      `json:"rwd_tag_0046,omitempty" validate:"blocked"`
	TagStub0047                 Tag      `json:"rwd_tag_0047,omitempty" validate:"blocked"`
	TagStub0048                 Tag      `json:"rwd_tag_0048,omitempty" validate:"blocked"`
	TagStub0049                 Tag      `json:"rwd_tag_0049,omitempty" validate:"blocked"`
	TagStub0050                 Tag      `json:"rwd_tag_0050,omitempty" validate:"blocked"`
	TagStub0051                 Tag      `json:"rwd_tag_0051,omitempty" validate:"blocked"`
	TagStub0052                 Tag      `json:"rwd_tag_0052,omitempty" validate:"blocked"`
	TagStub0053                 Tag      `json:"rwd_tag_0053,omitempty" validate:"blocked"`
	TagStub0054                 Tag      `json:"rwd_tag_0054,omitempty" validate:"blocked"`
	TagTagUnreleased0024        Tag      `json:"rwd_tag_0024,omitempty" validate:"blocked"`
	TagToriA                    Tag      `json:"rwd_tag_tori_a,omitempty"`
	TagUnreleased0022           Tag      `json:"rwd_tag_0022,omitempty" validate:"blocked"`
	TagVrmlPreseason            Tag      `json:"rwd_tag_s1_vrml_preseason,omitempty" validate:"restricted"`
	TagVrmlS1                   Tag      `json:"rwd_tag_s1_vrml_s1,omitempty" validate:"restricted"`
	TagVrmlS1Champion           Tag      `json:"rwd_tag_s1_vrml_s1_champion,omitempty" validate:"restricted"`
	TagVrmlS1Finalist           Tag      `json:"rwd_tag_s1_vrml_s1_finalist,omitempty" validate:"restricted"`
	TagVrmlS2                   Tag      `json:"rwd_tag_s1_vrml_s2,omitempty" validate:"restricted"`
	TagVrmlS2Champion           Tag      `json:"rwd_tag_s1_vrml_s2_champion,omitempty" validate:"restricted"`
	TagVrmlS2Finalist           Tag      `json:"rwd_tag_s1_vrml_s2_finalist,omitempty" validate:"restricted"`
	TagVrmlS3                   Tag      `json:"rwd_tag_s1_vrml_s3,omitempty" validate:"restricted"`
	TagVrmlS3Champion           Tag      `json:"rwd_tag_s1_vrml_s3_champion,omitempty" validate:"restricted"`
	TagVrmlS3Finalist           Tag      `json:"rwd_tag_s1_vrml_s3_finalist,omitempty" validate:"restricted"`
	TagVrmlS4                   Tag      `json:"rwd_tag_0008,omitempty" validate:"restricted"`
	TagVrmlS4Champion           Tag      `json:"rwd_tag_0010,omitempty" validate:"restricted"`
	TagVrmlS4Finalist           Tag      `json:"rwd_tag_0009,omitempty" validate:"restricted"`
	TagVrmlS5                   Tag      `json:"rwd_tag_0035,omitempty" validate:"restricted"`
	TagVrmlS5Champion           Tag      `json:"rwd_tag_0037,omitempty" validate:"restricted"`
	TagVrmlS5Finalist           Tag      `json:"rwd_tag_0036,omitempty" validate:"restricted"`
	TagVrmlS6                   Tag      `json:"rwd_tag_0040,omitempty" validate:"restricted"`
	TagVrmlS6Champion           Tag      `json:"rwd_tag_0042,omitempty" validate:"restricted"`
	TagVrmlS6Finalist           Tag      `json:"rwd_tag_0041,omitempty" validate:"restricted"`
	TagVrmlS7                   Tag      `json:"rwd_tag_0043,omitempty" validate:"restricted"`
	TagVrmlS7Champion           Tag      `json:"rwd_tag_0045,omitempty" validate:"restricted"`
	TagVrmlS7Finalist           Tag      `json:"rwd_tag_0044,omitempty" validate:"restricted"`
	Tint0000                    Tint     `json:"rwd_tint_0000,omitempty"`
	Tint0001                    Tint     `json:"rwd_tint_0001,omitempty"`
	Tint0002                    Tint     `json:"rwd_tint_0002,omitempty"`
	Tint0007                    Tint     `json:"rwd_tint_0007,omitempty"`
	Tint0008                    Tint     `json:"rwd_tint_0008,omitempty"`
	Tint0009                    Tint     `json:"rwd_tint_0009,omitempty"`
	Tint0013                    Tint     `json:"rwd_tint_0013,omitempty"`
	Tint0014                    Tint     `json:"rwd_tint_0014,omitempty"`
	Tint0015                    Tint     `json:"rwd_tint_0015,omitempty"`
	Tint0019                    Tint     `json:"rwd_tint_0019,omitempty" validate:"blocked"`
	Tint0020                    Tint     `json:"rwd_tint_0020,omitempty"`
	Tint0021                    Tint     `json:"rwd_tint_0021,omitempty"`
	Tint0022                    Tint     `json:"rwd_tint_0022,omitempty"`
	Tint0023                    Tint     `json:"rwd_tint_0023,omitempty"`
	Tint0024                    Tint     `json:"rwd_tint_0024,omitempty"`
	Tint0025                    Tint     `json:"rwd_tint_0025,omitempty"`
	Tint0026                    Tint     `json:"rwd_tint_0026,omitempty" validate:"blocked"`
	Tint0027                    Tint     `json:"rwd_tint_0027,omitempty" validate:"blocked"`
	TintBlueADefault            Tint     `json:"tint_blue_a_default,omitempty"`
	TintBlueBDefault            Tint     `json:"tint_blue_b_default,omitempty"`
	TintBlueCDefault            Tint     `json:"tint_blue_c_default,omitempty"`
	TintBlueDDefault            Tint     `json:"tint_blue_d_default,omitempty"`
	TintBlueEDefault            Tint     `json:"tint_blue_e_default,omitempty"`
	TintBlueFDefault            Tint     `json:"tint_blue_f_default,omitempty"`
	TintBlueGDefault            Tint     `json:"tint_blue_g_default,omitempty"`
	TintBlueHDefault            Tint     `json:"tint_blue_h_default,omitempty"`
	TintBlueIDefault            Tint     `json:"tint_blue_i_default,omitempty"`
	TintBlueJDefault            Tint     `json:"tint_blue_j_default,omitempty"`
	TintBlueKDefault            Tint     `json:"tint_blue_k_default,omitempty"`
	TintChassisDefault          Tint     `json:"tint_chassis_default,omitempty"`
	TintNeutralADefault         Tint     `json:"tint_neutral_a_default,omitempty"`
	TintNeutralAS10Default      Tint     `json:"tint_neutral_a_s10_default,omitempty"`
	TintNeutralBDefault         Tint     `json:"tint_neutral_b_default,omitempty"`
	TintNeutralCDefault         Tint     `json:"tint_neutral_c_default,omitempty"`
	TintNeutralDDefault         Tint     `json:"tint_neutral_d_default,omitempty"`
	TintNeutralEDefault         Tint     `json:"tint_neutral_e_default,omitempty"`
	TintNeutralFDefault         Tint     `json:"tint_neutral_f_default,omitempty"`
	TintNeutralGDefault         Tint     `json:"tint_neutral_g_default,omitempty"`
	TintNeutralHDefault         Tint     `json:"tint_neutral_h_default,omitempty"`
	TintNeutralIDefault         Tint     `json:"tint_neutral_i_default,omitempty"`
	TintNeutralJDefault         Tint     `json:"tint_neutral_j_default,omitempty"`
	TintNeutralKDefault         Tint     `json:"tint_neutral_k_default,omitempty"`
	TintNeutralLDefault         Tint     `json:"tint_neutral_l_default,omitempty"`
	TintNeutralMDefault         Tint     `json:"tint_neutral_m_default,omitempty"`
	TintNeutralNDefault         Tint     `json:"tint_neutral_n_default,omitempty"`
	TintNeutralSpookyADefault   Tint     `json:"tint_neutral_spooky_a_default,omitempty"`
	TintNeutralSpookyBDefault   Tint     `json:"tint_neutral_spooky_b_default,omitempty"`
	TintNeutralSpookyCDefault   Tint     `json:"tint_neutral_spooky_c_default,omitempty"`
	TintNeutralSpookyDDefault   Tint     `json:"tint_neutral_spooky_d_default,omitempty"`
	TintNeutralSpookyEDefault   Tint     `json:"tint_neutral_spooky_e_default,omitempty"`
	TintNeutralSummerADefault   Tint     `json:"tint_neutral_summer_a_default,omitempty"`
	TintNeutralXmasADefault     Tint     `json:"tint_neutral_xmas_a_default,omitempty"`
	TintNeutralXmasBDefault     Tint     `json:"tint_neutral_xmas_b_default,omitempty"`
	TintNeutralXmasCDefault     Tint     `json:"tint_neutral_xmas_c_default,omitempty"`
	TintNeutralXmasDDefault     Tint     `json:"tint_neutral_xmas_d_default,omitempty"`
	TintNeutralXmasEDefault     Tint     `json:"tint_neutral_xmas_e_default,omitempty"`
	TintOrangeADefault          Tint     `json:"tint_orange_a_default,omitempty"`
	TintOrangeBDefault          Tint     `json:"tint_orange_b_default,omitempty"`
	TintOrangeCDefault          Tint     `json:"tint_orange_c_default,omitempty"`
	TintOrangeDDefault          Tint     `json:"tint_orange_d_default,omitempty"`
	TintOrangeEDefault          Tint     `json:"tint_orange_e_default,omitempty"`
	TintOrangeFDefault          Tint     `json:"tint_orange_f_default,omitempty"`
	TintOrangeGDefault          Tint     `json:"tint_orange_g_default,omitempty"`
	TintOrangeHDefault          Tint     `json:"tint_orange_h_default,omitempty"`
	TintOrangeIDefault          Tint     `json:"tint_orange_i_default,omitempty"`
	TintOrangeJDefault          Tint     `json:"tint_orange_j_default,omitempty"`
	TintOrangeKDefault          Tint     `json:"tint_orange_k_default,omitempty"`
	TintS1ADefault              Tint     `json:"rwd_tint_s1_a_default,omitempty"`
	TintS1CDefault              Tint     `json:"rwd_tint_s1_c_default,omitempty"`
	TintS1DDefault              Tint     `json:"rwd_tint_s1_d_default,omitempty"`
	TintS2BDefault              Tint     `json:"rwd_tint_s2_b_default,omitempty"`
	TintS2CDefault              Tint     `json:"rwd_tint_s2_c_default,omitempty"`
	TintS3TintA                 Tint     `json:"rwd_tint_s3_tint_a,omitempty"`
	TintS3TintB                 Tint     `json:"rwd_tint_s3_tint_b,omitempty"`
	TintS3TintC                 Tint     `json:"rwd_tint_s3_tint_c,omitempty"`
	TintS3TintD                 Tint     `json:"rwd_tint_s3_tint_d,omitempty"`
	TintS3TintE                 Tint     `json:"rwd_tint_s3_tint_e,omitempty"`
	Title0000                   Title    `json:"rwd_title_0000,omitempty"`
	Title0001                   Title    `json:"rwd_title_0001,omitempty"`
	Title0002                   Title    `json:"rwd_title_0002,omitempty"`
	Title0004                   Title    `json:"rwd_title_0004,omitempty"`
	Title0005                   Title    `json:"rwd_title_0005,omitempty"`
	Title0006                   Title    `json:"rwd_title_0006,omitempty"`
	Title0007                   Title    `json:"rwd_title_0007,omitempty"`
	Title0008                   Title    `json:"rwd_title_0008,omitempty"`
	Title0009                   Title    `json:"rwd_title_0009,omitempty"`
	Title0010                   Title    `json:"rwd_title_0010,omitempty"`
	Title0011                   Title    `json:"rwd_title_0011,omitempty"`
	Title0012                   Title    `json:"rwd_title_0012,omitempty"`
	Title0013                   Title    `json:"rwd_title_0013,omitempty"`
	Title0014                   Title    `json:"rwd_title_0014,omitempty"`
	Title0015                   Title    `json:"rwd_title_0015,omitempty"`
	TitleGuardianA              Title    `json:"rwd_title_guardian_a,omitempty"`
	TitleRoninA                 Title    `json:"rwd_title_ronin_a,omitempty"`
	TitleS1A                    Title    `json:"rwd_title_s1_a,omitempty"`
	TitleS1B                    Title    `json:"rwd_title_s1_b,omitempty"`
	TitleS1C                    Title    `json:"rwd_title_s1_c,omitempty"`
	TitleS2A                    Title    `json:"rwd_title_s2_a,omitempty"`
	TitleS2B                    Title    `json:"rwd_title_s2_b,omitempty"`
	TitleS2C                    Title    `json:"rwd_title_s2_c,omitempty"`
	TitleShieldBearerA          Title    `json:"rwd_title_shield_bearer_a,omitempty"`
	TitleTitleA                 Title    `json:"rwd_title_title_a,omitempty"`
	TitleTitleC                 Title    `json:"rwd_title_title_c,omitempty"`
	TitleTitleD                 Title    `json:"rwd_title_title_d,omitempty"`
	TitleTitleDefault           Title    `json:"rwd_title_title_default,omitempty"`
	TitleTitleE                 Title    `json:"rwd_title_title_e,omitempty"`
	XPBoostGroupS0101           XP       `json:"rwd_xp_boost_group_s01_01,omitempty" validate:"restricted"`
	XPBoostGroupS0102           XP       `json:"rwd_xp_boost_group_s01_02,omitempty"`
	XPBoostGroupS0103           XP       `json:"rwd_xp_boost_group_s01_03,omitempty"`
	XPBoostGroupS0104           XP       `json:"rwd_xp_boost_group_s01_04,omitempty"`
	XPBoostGroupS0105           XP       `json:"rwd_xp_boost_group_s01_05,omitempty"`
	XPBoostGroupS0201           XP       `json:"rwd_xp_boost_group_s02_01,omitempty"`
	XPBoostGroupS0202           XP       `json:"rwd_xp_boost_group_s02_02,omitempty"`
	XPBoostGroupS0203           XP       `json:"rwd_xp_boost_group_s02_03,omitempty"`
	XPBoostGroupS0204           XP       `json:"rwd_xp_boost_group_s02_04,omitempty"`
	XPBoostGroupS0205           XP       `json:"rwd_xp_boost_group_s02_05,omitempty"`
	XPBoostGroupS0301           XP       `json:"rwd_xp_boost_group_s03_01,omitempty"`
	XPBoostGroupS0302           XP       `json:"rwd_xp_boost_group_s03_02,omitempty"`
	XPBoostGroupS0303           XP       `json:"rwd_xp_boost_group_s03_03,omitempty"`
	XPBoostGroupS0304           XP       `json:"rwd_xp_boost_group_s03_04,omitempty"`
	XPBoostGroupS0305           XP       `json:"rwd_xp_boost_group_s03_05,omitempty"`
	XPBoostGroupS0401           XP       `json:"rwd_xp_boost_group_s04_01,omitempty"`
	XPBoostGroupS0402           XP       `json:"rwd_xp_boost_group_s04_02,omitempty"`
	XPBoostGroupS0403           XP       `json:"rwd_xp_boost_group_s04_03,omitempty"`
	XPBoostGroupS0404           XP       `json:"rwd_xp_boost_group_s04_04,omitempty"`
	XPBoostGroupS0405           XP       `json:"rwd_xp_boost_group_s04_05,omitempty"`
	XPBoostGroupS0501           XP       `json:"rwd_xp_boost_group_s05_01,omitempty"`
	XPBoostGroupS0502           XP       `json:"rwd_xp_boost_group_s05_02,omitempty"`
	XPBoostGroupS0503           XP       `json:"rwd_xp_boost_group_s05_03,omitempty"`
	XPBoostGroupS0504           XP       `json:"rwd_xp_boost_group_s05_04,omitempty"`
	XPBoostGroupS0505           XP       `json:"rwd_xp_boost_group_s05_05,omitempty"`
	XPBoostGroupS0601           XP       `json:"rwd_xp_boost_group_s06_01,omitempty"`
	XPBoostGroupS0602           XP       `json:"rwd_xp_boost_group_s06_02,omitempty"`
	XPBoostGroupS0603           XP       `json:"rwd_xp_boost_group_s06_03,omitempty"`
	XPBoostGroupS0604           XP       `json:"rwd_xp_boost_group_s06_04,omitempty"`
	XPBoostGroupS0605           XP       `json:"rwd_xp_boost_group_s06_05,omitempty"`
	XPBoostGroupS0701           XP       `json:"rwd_xp_boost_group_s07_01,omitempty"`
	XPBoostGroupS0702           XP       `json:"rwd_xp_boost_group_s07_02,omitempty"`
	XPBoostGroupS0703           XP       `json:"rwd_xp_boost_group_s07_03,omitempty"`
	XPBoostGroupS0704           XP       `json:"rwd_xp_boost_group_s07_04,omitempty"`
	XPBoostGroupS0705           XP       `json:"rwd_xp_boost_group_s07_05,omitempty"`
	XPBoostIndividualS0101      XP       `json:"rwd_xp_boost_individual_s01_01,omitempty"`
	XPBoostIndividualS0102      XP       `json:"rwd_xp_boost_individual_s01_02,omitempty"`
	XPBoostIndividualS0103      XP       `json:"rwd_xp_boost_individual_s01_03,omitempty"`
	XPBoostIndividualS0104      XP       `json:"rwd_xp_boost_individual_s01_04,omitempty"`
	XPBoostIndividualS0105      XP       `json:"rwd_xp_boost_individual_s01_05,omitempty"`
	XPBoostIndividualS0201      XP       `json:"rwd_xp_boost_individual_s02_01,omitempty"`
	XPBoostIndividualS0202      XP       `json:"rwd_xp_boost_individual_s02_02,omitempty"`
	XPBoostIndividualS0203      XP       `json:"rwd_xp_boost_individual_s02_03,omitempty"`
	XPBoostIndividualS0204      XP       `json:"rwd_xp_boost_individual_s02_04,omitempty"`
	XPBoostIndividualS0205      XP       `json:"rwd_xp_boost_individual_s02_05,omitempty"`
	XPBoostIndividualS0301      XP       `json:"rwd_xp_boost_individual_s03_01,omitempty"`
	XPBoostIndividualS0302      XP       `json:"rwd_xp_boost_individual_s03_02,omitempty"`
	XPBoostIndividualS0303      XP       `json:"rwd_xp_boost_individual_s03_03,omitempty"`
	XPBoostIndividualS0304      XP       `json:"rwd_xp_boost_individual_s03_04,omitempty"`
	XPBoostIndividualS0305      XP       `json:"rwd_xp_boost_individual_s03_05,omitempty"`
	XPBoostIndividualS0401      XP       `json:"rwd_xp_boost_individual_s04_01,omitempty"`
	XPBoostIndividualS0402      XP       `json:"rwd_xp_boost_individual_s04_02,omitempty"`
	XPBoostIndividualS0403      XP       `json:"rwd_xp_boost_individual_s04_03,omitempty"`
	XPBoostIndividualS0404      XP       `json:"rwd_xp_boost_individual_s04_04,omitempty"`
	XPBoostIndividualS0405      XP       `json:"rwd_xp_boost_individual_s04_05,omitempty"`
	XPBoostIndividualS0501      XP       `json:"rwd_xp_boost_individual_s05_01,omitempty"`
	XPBoostIndividualS0502      XP       `json:"rwd_xp_boost_individual_s05_02,omitempty"`
	XPBoostIndividualS0503      XP       `json:"rwd_xp_boost_individual_s05_03,omitempty"`
	XPBoostIndividualS0504      XP       `json:"rwd_xp_boost_individual_s05_04,omitempty"`
	XPBoostIndividualS0505      XP       `json:"rwd_xp_boost_individual_s05_05,omitempty"`
	XPBoostIndividualS0601      XP       `json:"rwd_xp_boost_individual_s06_01,omitempty"`
	XPBoostIndividualS0602      XP       `json:"rwd_xp_boost_individual_s06_02,omitempty"`
	XPBoostIndividualS0603      XP       `json:"rwd_xp_boost_individual_s06_03,omitempty"`
	XPBoostIndividualS0604      XP       `json:"rwd_xp_boost_individual_s06_04,omitempty"`
	XPBoostIndividualS0605      XP       `json:"rwd_xp_boost_individual_s06_05,omitempty"`
	XPBoostIndividualS0701      XP       `json:"rwd_xp_boost_individual_s07_01,omitempty"`
	XPBoostIndividualS0702      XP       `json:"rwd_xp_boost_individual_s07_02,omitempty"`
	XPBoostIndividualS0703      XP       `json:"rwd_xp_boost_individual_s07_03,omitempty"`
	XPBoostIndividualS0704      XP       `json:"rwd_xp_boost_individual_s07_04,omitempty"`
	XPBoostIndividualS0705      XP       `json:"rwd_xp_boost_individual_s07_05,omitempty"`
}

type CombatUnlocks struct {
	DecalCombatFlamingoA Decal   `json:"decal_combat_flamingo_a,omitempty"`
	DecalCombatLogoA     Decal   `json:"decal_combat_logo_a,omitempty"`
	EmoteDizzyEyesA      Emote   `json:"emote_dizzy_eyes_a,omitempty"`
	PatternLightningA    Pattern `json:"pattern_lightning_a,omitempty"`
	BoosterS10           Booster `json:"rwd_booster_s10,omitempty"`
	ChassisBodyS10A      Chassis `json:"rwd_chassis_body_s10_a,omitempty"`
	MedalS1CombatBronze  Medal   `json:"rwd_medal_s1_combat_bronze,omitempty"`
	MedalS1CombatGold    Medal   `json:"rwd_medal_s1_combat_gold,omitempty"`
	MedalS1CombatSilver  Medal   `json:"rwd_medal_s1_combat_silver,omitempty"`
	TitleTitleB          Title   `json:"rwd_title_title_b,omitempty"`
}

func NewServerProfile() *ServerProfile {
	// This is the default server profile that EchoVR shipped with.
	return &ServerProfile{
		PurchasedCombat: 1,
		SchemaVersion:   4,
		EquippedCosmetics: EquippedCosmetics{
			Instances: CosmeticInstances{
				Unified: UnifiedCosmeticInstance{
					Slots: CosmeticLoadout{
						Emote:          "emote_blink_smiley_a",
						Decal:          "decal_default",
						Tint:           "tint_neutral_a_default",
						TintAlignmentA: "tint_blue_a_default",
						TintAlignmentB: "tint_orange_a_default",
						Pattern:        "pattern_default",
						Pip:            "rwd_decalback_default",
						Chassis:        "rwd_chassis_body_s11_a",
						Bracer:         "rwd_bracer_default",
						Booster:        "rwd_booster_default",
						Title:          "rwd_title_title_default",
						Tag:            "rwd_tag_s1_a_secondary",
						Banner:         "rwd_banner_s1_default",
						Medal:          "rwd_medal_default",
						GoalFx:         "rwd_goal_fx_default",
						SecondEmote:    "emote_blink_smiley_a",
						Emissive:       "emissive_default",
						TintBody:       "tint_neutral_a_default",
						PatternBody:    "pattern_default",
						DecalBody:      "decal_default",
					},
				},
			},
			Number: 1,
		},
		Statistics: PlayerStatistics{
			Arena: ArenaStatistics{
				Level: LevelStatistic{
					Count:   1,
					Operand: "add",
					Value:   1,
				},
			},
			Combat: CombatStatistics{
				Level: LevelStatistic{
					Count:   1,
					Operand: "add",
					Value:   1,
				},
			},
		},
		UnlockedCosmetics: UnlockedCosmetics{
			Arena: ArenaUnlocks{
				DecalCombatFlamingoA:   true,
				DecalCombatLogoA:       true,
				DecalDefault:           true,
				DecalSheldonA:          true,
				EmoteBlinkSmileyA:      true,
				EmoteDefault:           true,
				EmoteDizzyEyesA:        true,
				LoadoutNumber:          true,
				PatternDefault:         true,
				PatternLightningA:      true,
				BannerS1Default:        true,
				BoosterDefault:         true,
				BracerDefault:          true,
				ChassisBodyS11A:        true,
				DecalbackDefault:       true,
				DecalborderDefault:     true,
				MedalDefault:           true,
				TagDefault:             true,
				TagS1ASecondary:        true,
				TitleTitleDefault:      true,
				TintBlueADefault:       true,
				TintNeutralADefault:    true,
				TintNeutralAS10Default: true,
				TintOrangeADefault:     true,
				GoalFxDefault:          true,
				EmissiveDefault:        true,
			},
			Combat: CombatUnlocks{
				BoosterS10:      true,
				ChassisBodyS10A: true,
			},
		},
		EchoUserIdToken: "default_battlepass",
	}
}

// DisableRestrictedCosmetics sets all the restricted cosmetics to false.
func (s *ServerProfile) DisableRestrictedCosmetics() error {
	// Set all the VRML cosmetics to false
	structs := []interface{}{s.UnlockedCosmetics.Arena, s.UnlockedCosmetics.Combat}
	for _, t := range structs {
		v := reflect.ValueOf(t).Elem()
		for i := 0; i < v.NumField(); i++ {
			tag := v.Type().Field(i).Tag.Get("validate")
			if strings.Contains(tag, "restricted") {
				if v.Field(i).CanSet() {
					v.Field(i).Set(reflect.ValueOf(false))
				}
			}
		}
	}
	return nil
}

func NewClientProfile() *ClientProfile {
	return &ClientProfile{

		CombatWeapon:       "assault",
		CombatGrenade:      "arc",
		CombatDominantHand: 1,
		CombatAbility:      "buff",
		LegalConsents: LegalConsents{
			PointsPolicyVersion: 1,
			EulaVersion:         1,
			GameAdminVersion:    1,
			SplashScreenVersion: 2,
		},
		NewPlayerProgress: NewPlayerProgress{
			Lobby: NpeMilestone{Completed: true},

			FirstMatch:        NpeMilestone{Completed: true},
			Movement:          NpeMilestone{Completed: true},
			ArenaBasics:       NpeMilestone{Completed: true},
			SocialTabSeen:     Versioned{Version: 1},
			Pointer:           Versioned{Version: 1},
			BlueTintTabSeen:   Versioned{Version: 1},
			HeraldryTabSeen:   Versioned{Version: 1},
			OrangeTintTabSeen: Versioned{Version: 1},
		},
		Customization: Customization{
			BattlePassSeasonPoiVersion: 0,
			NewUnlocksPoiVersion:       1,
			StoreEntryPoiVersion:       0,
			ClearNewUnlocksVersion:     1,
		},
		Social: Social{
			CommunityValuesVersion: 1,
			SetupVersion:           1,
		},
		NewUnlocks: []int64{},
	}
}

func DefaultGameProfiles(xplatformid *EvrId, displayname string) (GameProfiles, error) {
	client := NewClientProfile()
	server := NewServerProfile()
	client.EchoUserIdToken = xplatformid.String()
	server.EchoUserIdToken = xplatformid.String()
	client.DisplayName = displayname
	server.DisplayName = displayname

	return GameProfiles{
		Client: client,
		Server: server,
	}, nil
}
