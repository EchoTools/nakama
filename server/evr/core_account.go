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
)

var (
	validate *validator.Validate

	EvrIdNil = EvrId{}
)

func init() {
	validate = validator.New()
	validate.RegisterValidation("restricted", func(fl validator.FieldLevel) bool { return true })
	validate.RegisterValidation("blocked", forceFalse)
	validate.RegisterValidation("evrid", func(fl validator.FieldLevel) bool {
		evrId, err := ParseEvrId(fl.Field().String())
		if err != nil || evrId.Equals(EvrIdNil) {
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
	Client ClientProfile `json:"client"`
	Server ServerProfile `json:"server"`
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
	DisplayName string `json:"displayname,omitempty"` // Ignored and set by nakama
	EvrID       EvrId  `json:"xplatformid,omitempty"` // Ignored and set by nakama

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
	Social             ClientSocial      `json:"social,omitempty"`
	NewUnlocks         []int64           `json:"newunlocks,omitempty"`
	EarlyQuitFeatures  EarlyQuitFeatures `json:"earlyquit"` // Early quit features
}

type EarlyQuitFeatures struct {
	PenaltyTimestamp    int64 `json:"penaltyts"`
	NumEarlyQuits       int   `json:"numearlyquits"`
	NumSteadyMatches    int   `json:"numsteadymatches"`
	NumSteadyEarlyQuits int   `json:"numsteadyearlyquits"`
	PenaltyLevel        int   `json:"penaltylevel"`
	SteadyPlayerLevel   int   `json:"steadyplayerlevel"`
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
		c.DisplayName, c.EvrID.Token(), c.TeamName, c.CombatWeapon, c.CombatGrenade, c.CombatDominantHand, c.ModifyTime, c.CombatAbility, c.LegalConsents, c.MutedPlayers, c.GhostedPlayers, c.NewPlayerProgress, c.Customization, c.Social, c.NewUnlocks)
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

type ClientSocial struct {
	// WARNING: EchoVR dictates this struct/schema.
	CommunityValuesVersion int64 `json:"community_values_version,omitempty" validate:"gte=0"`
	SetupVersion           int64 `json:"setup_version,omitempty" validate:"gte=0"`
	Channel                GUID  `json:"group,omitempty" validate:"uuid_rfc4122"` // The channel. It is a GUID, uppercase.
}

type ServerSocial struct {
	Channel GUID `json:"group,omitempty" validate:"uuid_rfc4122"` // The channel. It is a GUID, uppercase.
}

type PlayerStatistics map[string]map[string]MatchStatistic

type ServerProfile struct {
	// WARNING: EchoVR dictates this struct/schema.
	DisplayName       string            `json:"displayname"`                                    // Overridden by nakama
	EvrID             EvrId             `json:"xplatformid"`                                    // Overridden by nakama
	SchemaVersion     int16             `json:"_version,omitempty" validate:"gte=0"`            // Version of the schema(?)
	PublisherLock     string            `json:"publisher_lock,omitempty"`                       // unused atm
	PurchasedCombat   int8              `json:"purchasedcombat,omitempty" validate:"eq=0|eq=1"` // unused (combat was made free)
	LobbyVersion      uint64            `json:"lobbyversion" validate:"gte=0"`                  // set from the login request
	LoginTime         int64             `json:"logintime" validate:"gte=0"`                     // When the player logged in
	UpdateTime        int64             `json:"updatetime" validate:"gte=0"`                    // When the profile was last stored.
	CreateTime        int64             `json:"createtime" validate:"gte=0"`                    // When the player's nakama account was created.
	Statistics        PlayerStatistics  `json:"stats,omitempty"`                                // Player statistics
	MaybeStale        bool              `json:"maybestale,omitempty" validate:"boolean"`        // If the profile is stale
	UnlockedCosmetics UnlockedCosmetics `json:"unlocks,omitempty"`                              // Unlocked cosmetics
	EquippedCosmetics EquippedCosmetics `json:"loadout,omitempty"`                              // Equipped cosmetics
	Social            ServerSocial      `json:"social,omitempty"`                               // Social settings
	Achievements      interface{}       `json:"achievements,omitempty"`                         // Achievements
	RewardState       interface{}       `json:"reward_state,omitempty"`                         // Reward state?
	// If DeveloperFeatures is not null, the player will have a gold name
	DeveloperFeatures *DeveloperFeatures `json:"dev,omitempty"` // Developer features
}

type DeveloperFeatures struct {
	DisableAfkTimeout bool  `json:"disable_afk_timeout,omitempty"`
	EvrIDOverride     EvrId `json:"xplatformid,omitempty"`
}

type MatchStatistic struct {
	Operation string `json:"op"`
	Value     any    `json:"val"`
	Count     int64  `json:"cnt,omitempty"`
}

type EquippedCosmetics struct {
	Instances  CosmeticInstances `json:"instances"`
	Number     int64             `json:"number"`
	NumberBody int64             `json:"number_body"`
}

type CosmeticInstances struct {
	Unified UnifiedCosmeticInstance `json:"unified"`
}

type UnifiedCosmeticInstance struct {
	Slots             CosmeticLoadout `json:"slots"`
	ApplyHeraldryTint bool            `json:"apply_heraldry_tint"`
}

type CosmeticLoadout struct {
	Banner         string `json:"banner"`
	Booster        string `json:"booster"`
	Bracer         string `json:"bracer"`
	Chassis        string `json:"chassis"`
	Decal          string `json:"decal"`
	DecalBody      string `json:"decal_body"`
	DecalBorder    string `json:"decal_border"`
	DecalBack      string `json:"decal_back"`
	Emissive       string `json:"emissive"`
	Emote          string `json:"emote"`
	GoalFX         string `json:"goal_fx"`
	Medal          string `json:"medal"`
	Pattern        string `json:"pattern"`
	PatternBody    string `json:"pattern_body"`
	PIP            string `json:"pip"`
	SecondEmote    string `json:"secondemote"`
	Tag            string `json:"tag"`
	Tint           string `json:"tint"`
	TintAlignmentA string `json:"tint_alignment_a"`
	TintAlignmentB string `json:"tint_alignment_b"`
	TintBody       string `json:"tint_body"`
	Title          string `json:"title"`
}

func DefaultCosmeticLoadout() CosmeticLoadout {
	return CosmeticLoadout{
		Emote:          "emote_blink_smiley_a",
		Decal:          "decal_default",
		Tint:           "tint_neutral_a_default",
		TintAlignmentA: "tint_blue_a_default",
		TintAlignmentB: "tint_orange_a_default",
		Pattern:        "pattern_default",
		PIP:            "rwd_decalback_default",
		Chassis:        "rwd_chassis_body_s11_a",
		Bracer:         "rwd_bracer_default",
		Booster:        "rwd_booster_default",
		Title:          "rwd_title_title_default",
		Tag:            "rwd_tag_s1_a_secondary",
		Banner:         "rwd_banner_s1_default",
		Medal:          "rwd_medal_default",
		GoalFX:         "rwd_goal_fx_default",
		SecondEmote:    "emote_blink_smiley_a",
		Emissive:       "emissive_default",
		TintBody:       "tint_neutral_a_default",
		PatternBody:    "pattern_default",
		DecalBody:      "decal_default",
	}
}

func (c CosmeticLoadout) ToMap() map[string]string {
	m := make(map[string]string)
	v := reflect.ValueOf(c)
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		s := strings.SplitN(tag, ",", 2)[0]
		m[s] = v.Field(i).String()
	}
	return m
}

func (c *CosmeticLoadout) FromMap(m map[string]string) {
	v := reflect.ValueOf(c).Elem()
	t := v.Type()
	for i := 0; i < v.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		s := strings.SplitN(tag, ",", 2)[0]
		if val, ok := m[s]; ok {
			v.Field(i).SetString(val)
		}
	}
}

type UnlockedCosmetics struct {
	Arena  ArenaUnlocks  `json:"arena"`
	Combat CombatUnlocks `json:"combat"`
}

func (u *UnlockedCosmetics) ToMap() map[string]map[string]bool {
	m := make(map[string]map[string]bool)
	m["arena"] = make(map[string]bool)
	m["combat"] = make(map[string]bool)

	arena := reflect.ValueOf(u.Arena)
	arenaType := arena.Type()
	for i := 0; i < arena.NumField(); i++ {
		tag := arenaType.Field(i).Tag.Get("json")
		s := strings.SplitN(tag, ",", 2)[0]
		m["arena"][s] = arena.Field(i).Bool()
	}

	combat := reflect.ValueOf(u.Combat)
	t := combat.Type()
	for i := 0; i < combat.NumField(); i++ {
		tag := t.Field(i).Tag.Get("json")
		s := strings.SplitN(tag, ",", 2)[0]
		m["combat"][s] = combat.Field(i).Bool()
	}

	return m
}

type ArenaUnlocks struct {
	// WARNING: EchoVR dictates this struct/schema.
	// If an unlock needs to be filtered out, use the  validate field tag.
	// If an unlock must not be allowed ever, use the "isdefault" validator tag.

	/*
		Decals
	*/

	Banner0000                  bool `json:"rwd_banner_0000,omitempty"`
	Banner0001                  bool `json:"rwd_banner_0001,omitempty"`
	Banner0002                  bool `json:"rwd_banner_0002,omitempty"`
	Banner0003                  bool `json:"rwd_banner_0003,omitempty"`
	Banner0008                  bool `json:"rwd_banner_0008,omitempty"`
	Banner0009                  bool `json:"rwd_banner_0009,omitempty"`
	Banner0010                  bool `json:"rwd_banner_0010,omitempty"`
	Banner0011                  bool `json:"rwd_banner_0011,omitempty"`
	Banner0014                  bool `json:"rwd_banner_0014,omitempty"`
	Banner0015                  bool `json:"rwd_banner_0015,omitempty"`
	Banner0016                  bool `json:"rwd_banner_0016,omitempty"`
	Banner0017                  bool `json:"rwd_banner_0017,omitempty"`
	Banner0021                  bool `json:"rwd_banner_0021,omitempty"`
	Banner0022                  bool `json:"rwd_banner_0022,omitempty"`
	Banner0023                  bool `json:"rwd_banner_0023,omitempty"`
	Banner0024                  bool `json:"rwd_banner_0024,omitempty"`
	Banner0025                  bool `json:"rwd_banner_0025,omitempty"`
	BannerStub1                 bool `json:"rwd_banner_0030,omitempty" validate:"blocked"`
	BannerStub2                 bool `json:"rwd_banner_0031,omitempty" validate:"blocked"`
	BannerLoneEcho2_A           bool `json:"rwd_banner_lone_echo_2_a,omitempty"`
	BannerS1Basic               bool `json:"rwd_banner_s1_basic,omitempty"`
	BannerS1BoldStripe          bool `json:"rwd_banner_s1_bold_stripe,omitempty"`
	BannerS1Chevrons            bool `json:"rwd_banner_s1_chevrons,omitempty"`
	BannerS1Default             bool `json:"rwd_banner_s1_default,omitempty"`
	BannerS1Digi                bool `json:"rwd_banner_s1_digi,omitempty"`
	BannerS1Flames              bool `json:"rwd_banner_s1_flames,omitempty"`
	BannerS1Hourglass           bool `json:"rwd_banner_s1_hourglass,omitempty"`
	BannerS1Squish              bool `json:"rwd_banner_s1_squish,omitempty"`
	BannerS1Tattered            bool `json:"rwd_banner_s1_tattered,omitempty"`
	BannerS1Trex                bool `json:"rwd_banner_s1_trex,omitempty"`
	BannerS1Tritip              bool `json:"rwd_banner_s1_tritip,omitempty"`
	BannerS1Wings               bool `json:"rwd_banner_s1_wings,omitempty"`
	BannerS2Deco                bool `json:"rwd_banner_s2_deco,omitempty"`
	BannerS2Gears               bool `json:"rwd_banner_s2_gears,omitempty"`
	BannerS2Ladybug             bool `json:"rwd_banner_s2_ladybug,omitempty"`
	BannerS2Pyramids            bool `json:"rwd_banner_s2_pyramids,omitempty"`
	BannerS2Squares             bool `json:"rwd_banner_s2_squares,omitempty"`
	BannerSashimonoA            bool `json:"rwd_banner_sashimono_a,omitempty"`
	BannerSpartanShieldA        bool `json:"rwd_banner_spartan_shield_a,omitempty"`
	BannerTrianglesA            bool `json:"rwd_banner_triangles_a,omitempty"`
	BoosterAnubisAHorus         bool `json:"rwd_booster_anubis_a_horus,omitempty"`
	BoosterAnubisS2A            bool `json:"rwd_booster_anubis_s2_a,omitempty"`
	BoosterArcadeS1A            bool `json:"rwd_booster_arcade_s1_a,omitempty"`
	BoosterArcadeVarS1A         bool `json:"rwd_booster_arcade_var_s1_a,omitempty"`
	BoosterAurumA               bool `json:"rwd_booster_aurum_a,omitempty"`
	BoosterAutomatonS2A         bool `json:"rwd_booster_automaton_s2_a,omitempty"`
	BoosterBeeS2A               bool `json:"rwd_booster_bee_s2_a,omitempty"`
	BoosterCovenantA            bool `json:"rwd_booster_covenant_a,omitempty"`
	BoosterCovenantAFlame       bool `json:"rwd_booster_covenant_a_flame,omitempty"`
	BoosterDefault              bool `json:"rwd_booster_default,omitempty"`
	BoosterFumeA                bool `json:"rwd_booster_fume_a,omitempty"`
	BoosterFumeADaydream        bool `json:"rwd_booster_fume_a_daydream,omitempty"`
	BoosterFunkA                bool `json:"rwd_booster_funk_a,omitempty"`
	BoosterHerosuitA            bool `json:"rwd_booster_herosuit_a,omitempty"`
	BoosterJunkyardA            bool `json:"rwd_booster_junkyard_a,omitempty"`
	BoosterLadybugS2A           bool `json:"rwd_booster_ladybug_s2_a,omitempty"`
	BoosterLazurliteA           bool `json:"rwd_booster_lazurlite_a,omitempty"`
	BoosterMakoS1A              bool `json:"rwd_booster_mako_s1_a,omitempty"`
	BoosterNobleA               bool `json:"rwd_booster_noble_a,omitempty"`
	BoosterNuclearA             bool `json:"rwd_booster_nuclear_a,omitempty"`
	BoosterNuclearAHydro        bool `json:"rwd_booster_nuclear_a_hydro,omitempty"`
	BoosterPlagueknightA        bool `json:"rwd_booster_plagueknight_a,omitempty"`
	BoosterRoverA               bool `json:"rwd_booster_rover_a,omitempty"`
	BoosterRoverADeco           bool `json:"rwd_booster_rover_a_deco,omitempty"`
	BoosterS11S1AFire           bool `json:"rwd_booster_s11_s1_a_fire,omitempty"`
	BoosterS11S1ARetro          bool `json:"rwd_booster_s11_s1_a_retro,omitempty"`
	BoosterSamuraiA             bool `json:"rwd_booster_samurai_a,omitempty"`
	BoosterScubaA               bool `json:"rwd_booster_scuba_a,omitempty"`
	BoosterSharkA               bool `json:"rwd_booster_shark_a,omitempty"`
	BoosterSharkATropical       bool `json:"rwd_booster_shark_a_tropical,omitempty"`
	BoosterSpartanA             bool `json:"rwd_booster_spartan_a,omitempty"`
	BoosterSpartanAHero         bool `json:"rwd_booster_spartan_a_hero,omitempty"`
	BoosterStreetwearA          bool `json:"rwd_booster_streetwear_a,omitempty"`
	BoosterTrexS1A              bool `json:"rwd_booster_trex_s1_a,omitempty"`
	BoosterVintageA             bool `json:"rwd_booster_vintage_a,omitempty"`
	BoosterWastelandA           bool `json:"rwd_booster_wasteland_a,omitempty"`
	BracerAnubisAHorus          bool `json:"rwd_bracer_anubis_a_horus,omitempty"`
	BracerAnubisS2A             bool `json:"rwd_bracer_anubis_s2_a,omitempty"`
	BracerArcadeS1A             bool `json:"rwd_bracer_arcade_s1_a,omitempty"`
	BracerArcadeVarS1A          bool `json:"rwd_bracer_arcade_var_s1_a,omitempty"`
	BracerAurumA                bool `json:"rwd_bracer_aurum_a,omitempty"`
	BracerAutomatonS2A          bool `json:"rwd_bracer_automaton_s2_a,omitempty"`
	BracerBeeS2A                bool `json:"rwd_bracer_bee_s2_a,omitempty"`
	BracerCovenantA             bool `json:"rwd_bracer_covenant_a,omitempty"`
	BracerCovenantAFlame        bool `json:"rwd_bracer_covenant_a_flame,omitempty"`
	BracerDefault               bool `json:"rwd_bracer_default,omitempty"`
	BracerFumeA                 bool `json:"rwd_bracer_fume_a,omitempty"`
	BracerFumeADaydream         bool `json:"rwd_bracer_fume_a_daydream,omitempty"`
	BracerFunkA                 bool `json:"rwd_bracer_funk_a,omitempty"`
	BracerJunkyardA             bool `json:"rwd_bracer_junkyard_a,omitempty"`
	BracerLadybugS2A            bool `json:"rwd_bracer_ladybug_s2_a,omitempty"`
	BracerLazurliteA            bool `json:"rwd_bracer_lazurlite_a,omitempty"`
	BracerMakoS1A               bool `json:"rwd_bracer_mako_s1_a,omitempty"`
	BracerNobleA                bool `json:"rwd_bracer_noble_a,omitempty"`
	BracerNuclearA              bool `json:"rwd_bracer_nuclear_a,omitempty"`
	BracerNuclearAHydro         bool `json:"rwd_bracer_nuclear_a_hydro,omitempty"`
	BracerPlagueknightA         bool `json:"rwd_bracer_plagueknight_a,omitempty"`
	BracerRoverA                bool `json:"rwd_bracer_rover_a,omitempty"`
	BracerRoverADeco            bool `json:"rwd_bracer_rover_a_deco,omitempty"`
	BracerSamuraiA              bool `json:"rwd_bracer_samurai_a,omitempty"`
	BracerScubaA                bool `json:"rwd_bracer_scuba_a,omitempty"`
	BracerSharkA                bool `json:"rwd_bracer_shark_a,omitempty"`
	BracerSharkATropical        bool `json:"rwd_bracer_shark_a_tropical,omitempty"`
	BracerSnacktimeA            bool `json:"rwd_bracer_snacktime_a,omitempty"`
	BracerSpartanA              bool `json:"rwd_bracer_spartan_a,omitempty"`
	BracerSpartanAHero          bool `json:"rwd_bracer_spartan_a_hero,omitempty"`
	BracerStreetwearA           bool `json:"rwd_bracer_streetwear_a,omitempty"`
	BracerTrexS1A               bool `json:"rwd_bracer_trex_s1_a,omitempty"`
	BracerVintageA              bool `json:"rwd_bracer_vintage_a,omitempty"`
	BracerWastelandA            bool `json:"rwd_bracer_wasteland_a,omitempty"`
	ChassisAnubisAHorus         bool `json:"rwd_chassis_anubis_a_horus,omitempty"`
	ChassisAnubisS2A            bool `json:"rwd_chassis_anubis_s2_a,omitempty"`
	ChassisAutomatonS2A         bool `json:"rwd_chassis_automaton_s2_a,omitempty"`
	ChassisBodyS11A             bool `json:"rwd_chassis_body_s11_a,omitempty"`
	ChassisFunkA                bool `json:"rwd_chassis_funk_a,omitempty"`
	ChassisHerosuitA            bool `json:"rwd_chassis_herosuit_a,omitempty"`
	ChassisJunkyardA            bool `json:"rwd_chassis_junkyard_a,omitempty"`
	ChassisMakoS1A              bool `json:"rwd_chassis_mako_s1_a,omitempty"`
	ChassisNobleA               bool `json:"rwd_chassis_noble_a,omitempty"`
	ChassisPlagueknightA        bool `json:"rwd_chassis_plagueknight_a,omitempty"`
	ChassisS11FlameA            bool `json:"rwd_chassis_s11_flame_a,omitempty"`
	ChassisS11RetroA            bool `json:"rwd_chassis_s11_retro_a,omitempty"`
	ChassisS8BA                 bool `json:"rwd_chassis_s8b_a,omitempty"`
	ChassisSamuraiA             bool `json:"rwd_chassis_samurai_a,omitempty"`
	ChassisScubaA               bool `json:"rwd_chassis_scuba_a,omitempty"`
	ChassisSharkA               bool `json:"rwd_chassis_shark_a,omitempty"`
	ChassisSharkATropical       bool `json:"rwd_chassis_shark_a_tropical,omitempty"`
	ChassisSpartanA             bool `json:"rwd_chassis_spartan_a,omitempty"`
	ChassisSpartanAHero         bool `json:"rwd_chassis_spartan_a_hero,omitempty"`
	ChassisStreetwearA          bool `json:"rwd_chassis_streetwear_a,omitempty"`
	ChassisTrexS1A              bool `json:"rwd_chassis_trex_s1_a,omitempty"`
	ChassisWastelandA           bool `json:"rwd_chassis_wasteland_a,omitempty"`
	CurrencyS0101               bool `json:"rwd_currency_s01_01,omitempty"`
	CurrencyS0102               bool `json:"rwd_currency_s01_02,omitempty"`
	CurrencyS0103               bool `json:"rwd_currency_s01_03,omitempty"`
	CurrencyS0104               bool `json:"rwd_currency_s01_04,omitempty"`
	CurrencyS0201               bool `json:"rwd_currency_s02_01,omitempty"`
	CurrencyS0202               bool `json:"rwd_currency_s02_02,omitempty"`
	CurrencyS0203               bool `json:"rwd_currency_s02_03,omitempty"`
	CurrencyS0204               bool `json:"rwd_currency_s02_04,omitempty"`
	CurrencyS0301               bool `json:"rwd_currency_s03_01,omitempty"`
	CurrencyS0302               bool `json:"rwd_currency_s03_02,omitempty"`
	CurrencyS0303               bool `json:"rwd_currency_s03_03,omitempty"`
	CurrencyS0304               bool `json:"rwd_currency_s03_04,omitempty"`
	CurrencyS0401               bool `json:"rwd_currency_s04_01,omitempty"`
	CurrencyS0402               bool `json:"rwd_currency_s04_02,omitempty"`
	CurrencyS0403               bool `json:"rwd_currency_s04_03,omitempty"`
	CurrencyS0404               bool `json:"rwd_currency_s04_04,omitempty"`
	CurrencyS0501               bool `json:"rwd_currency_s05_01,omitempty"`
	CurrencyS0502               bool `json:"rwd_currency_s05_02,omitempty"`
	CurrencyS0503               bool `json:"rwd_currency_s05_03,omitempty"`
	CurrencyS0504               bool `json:"rwd_currency_s05_04,omitempty"`
	CurrencyS0601               bool `json:"rwd_currency_s06_01,omitempty"`
	CurrencyS0602               bool `json:"rwd_currency_s06_02,omitempty"`
	CurrencyS0603               bool `json:"rwd_currency_s06_03,omitempty"`
	CurrencyS0604               bool `json:"rwd_currency_s06_04,omitempty"`
	CurrencyS0701               bool `json:"rwd_currency_s07_01,omitempty"`
	CurrencyS0702               bool `json:"rwd_currency_s07_02,omitempty"`
	CurrencyS0703               bool `json:"rwd_currency_s07_03,omitempty"`
	CurrencyS0704               bool `json:"rwd_currency_s07_04,omitempty"`
	Decal0000                   bool `json:"rwd_decal_0000,omitempty"`
	Decal0001                   bool `json:"rwd_decal_0001,omitempty"`
	Decal0002                   bool `json:"rwd_decal_0002,omitempty"`
	Decal0005                   bool `json:"rwd_decal_0005,omitempty"`
	Decal0006                   bool `json:"rwd_decal_0006,omitempty"`
	Decal0007                   bool `json:"rwd_decal_0007,omitempty"`
	Decal0010                   bool `json:"rwd_decal_0010,omitempty"`
	Decal0011                   bool `json:"rwd_decal_0011,omitempty"`
	Decal0012                   bool `json:"rwd_decal_0012,omitempty"`
	Decal0015                   bool `json:"rwd_decal_0015,omitempty"`
	Decal0016                   bool `json:"rwd_decal_0016,omitempty"`
	Decal0017                   bool `json:"rwd_decal_0017,omitempty"`
	Decal0018                   bool `json:"rwd_decal_0018,omitempty"`
	Decal0019                   bool `json:"rwd_decal_0019,omitempty"`
	Decal0020                   bool `json:"rwd_decal_0020,omitempty"`
	DecalStub1                  bool `json:"rwd_decal_0023,omitempty" validate:"blocked"`
	DecalStub2                  bool `json:"rwd_decal_0024,omitempty" validate:"blocked"`
	DecalAlienHeadA             bool `json:"decal_alien_head_a,omitempty"`
	DecalAnniversaryCupcakeA    bool `json:"decal_anniversary_cupcake_a,omitempty"`
	DecalAxolotlS2A             bool `json:"rwd_decal_axolotl_s2_a,omitempty"`
	DecalbackDefault            bool `json:"rwd_decalback_default,omitempty"`
	DecalBearPawA               bool `json:"decal_bear_paw_a,omitempty"`
	DecalBombA                  bool `json:"decal_bomb_a,omitempty"`
	DecalborderDefault          bool `json:"rwd_decalborder_default,omitempty"`
	DecalBowA                   bool `json:"decal_bow_a,omitempty"`
	DecalBullseyeA              bool `json:"decal_bullseye_a,omitempty"`
	DecalCatA                   bool `json:"decal_cat_a,omitempty"`
	DecalCherryBlossomA         bool `json:"rwd_decal_cherry_blossom_a,omitempty"`
	DecalCombatAnniversaryA     bool `json:"decal_combat_anniversary_a,omitempty"`
	DecalCombatCometA           bool `json:"decal_combat_comet_a,omitempty"`
	DecalCombatDemonA           bool `json:"decal_combat_demon_a,omitempty"`
	DecalCombatFlamingoA        bool `json:"decal_combat_flamingo_a,omitempty"`
	DecalCombatFlyingSaucerA    bool `json:"decal_combat_flying_saucer_a,omitempty"`
	DecalCombatIceCreamA        bool `json:"decal_combat_ice_cream_a,omitempty"`
	DecalCombatLionA            bool `json:"decal_combat_lion_a,omitempty"`
	DecalCombatLogoA            bool `json:"decal_combat_logo_a,omitempty"`
	DecalCombatMedicA           bool `json:"decal_combat_medic_a,omitempty"`
	DecalCombatMeteorA          bool `json:"decal_combat_meteor_a,omitempty"`
	DecalCombatMilitaryBadgeA   bool `json:"decal_combat_military_badge_a,omitempty"`
	DecalCombatNovaA            bool `json:"decal_combat_nova_a,omitempty"`
	DecalCombatOctopusA         bool `json:"decal_combat_octopus_a,omitempty"`
	DecalCombatPigA             bool `json:"decal_combat_pig_a,omitempty"`
	DecalCombatPizzaA           bool `json:"decal_combat_pizza_a,omitempty"`
	DecalCombatPulsarA          bool `json:"decal_combat_pulsar_a,omitempty"`
	DecalCombatPuppyA           bool `json:"decal_combat_puppy_a,omitempty"`
	DecalCombatRageBearA        bool `json:"decal_combat_rage_bear_a,omitempty"`
	DecalCombatScratchA         bool `json:"decal_combat_scratch_a,omitempty"`
	DecalCombatSkullCrossbonesA bool `json:"decal_combat_skull_crossbones_a,omitempty"`
	DecalCombatTrexSkullA       bool `json:"decal_combat_trex_skull_a,omitempty"`
	DecalCrosshairA             bool `json:"decal_crosshair_a,omitempty"`
	DecalCrownA                 bool `json:"decal_crown_a,omitempty"`
	DecalCupcakeA               bool `json:"decal_cupcake_a,omitempty"`
	DecalDefault                bool `json:"decal_default,omitempty"`
	DecalDinosaurA              bool `json:"decal_dinosaur_a,omitempty"`
	DecalDiscA                  bool `json:"decal_disc_a,omitempty"`
	DecalEagleA                 bool `json:"decal_eagle_a,omitempty"`
	DecalFangsA                 bool `json:"decal_fangs_a,omitempty"`
	DecalFireballA              bool `json:"decal_fireball_a,omitempty"`
	DecalGearsS2A               bool `json:"rwd_decal_gears_s2_a,omitempty"`
	DecalGgA                    bool `json:"rwd_decal_gg_a,omitempty"`
	DecalGingerbreadA           bool `json:"decal_gingerbread_a,omitempty"`
	DecalHalloweenBatA          bool `json:"decal_halloween_bat_a,omitempty"`
	DecalHalloweenCatA          bool `json:"decal_halloween_cat_a,omitempty"`
	DecalHalloweenCauldronA     bool `json:"decal_halloween_cauldron_a,omitempty"`
	DecalHalloweenGhostA        bool `json:"decal_halloween_ghost_a,omitempty"`
	DecalHalloweenPumpkinA      bool `json:"decal_halloween_pumpkin_a,omitempty"`
	DecalHalloweenScytheA       bool `json:"decal_halloween_scythe_a,omitempty"`
	DecalHalloweenSkullA        bool `json:"decal_halloween_skull_a,omitempty"`
	DecalHalloweenZombieA       bool `json:"decal_halloween_zombie_a,omitempty"`
	DecalHamburgerA             bool `json:"decal_hamburger_a,omitempty"`
	DecalKoiFishA               bool `json:"decal_koi_fish_a,omitempty"`
	DecalKronosA                bool `json:"decal_kronos_a,omitempty"`
	DecalLightningBoltA         bool `json:"decal_lightning_bolt_a,omitempty"`
	DecalLoneEcho2_A            bool `json:"rwd_decal_lone_echo_2_a,omitempty"`
	DecalMusicNoteA             bool `json:"decal_music_note_a,omitempty"`
	DecalNarwhalA               bool `json:"rwd_decal_narwhal_a,omitempty"`
	DecalOculusA                bool `json:"decal_oculus_a,omitempty"  validate:"restricted"`
	DecalOneYearA               bool `json:"decal_one_year_a,omitempty" validate:"restricted"`
	DecalOniA                   bool `json:"rwd_decal_oni_a,omitempty"`
	DecalPenguinA               bool `json:"decal_penguin_a,omitempty"`
	DecalPepperA                bool `json:"rwd_decal_pepper_a,omitempty"`
	DecalPresentA               bool `json:"decal_present_a,omitempty"`
	DecalProfileWolfA           bool `json:"decal_profile_wolf_a,omitempty"`
	DecalQuestLaunchA           bool `json:"decal_quest_launch_a,omitempty" validate:"restricted"`
	DecalRadioactiveA           bool `json:"decal_radioactive_a,omitempty"`
	DecalRadioactiveBioA        bool `json:"decal_radioactive_bio_a,omitempty"`
	DecalRadLogo                bool `json:"decal_radlogo_a,omitempty" validate:"restricted"`
	DecalRageWolfA              bool `json:"decal_rage_wolf_a,omitempty"`
	DecalRamenA                 bool `json:"rwd_decal_ramen_a,omitempty"`
	DecalRayGunA                bool `json:"decal_ray_gun_a,omitempty"`
	DecalReindeerA              bool `json:"decal_reindeer_a,omitempty"`
	DecalRocketA                bool `json:"decal_rocket_a,omitempty"`
	DecalRoseA                  bool `json:"decal_rose_a,omitempty"`
	DecalSaltShakerA            bool `json:"decal_salt_shaker_a,omitempty"`
	DecalSantaCubesatA          bool `json:"decal_santa_cubesat_a,omitempty"`
	DecalSaturnA                bool `json:"decal_saturn_a,omitempty"`
	DecalScarabS2A              bool `json:"rwd_decal_scarab_s2_a,omitempty"`
	DecalSheldonA               bool `json:"decal_sheldon_a,omitempty"`
	DecalSkullA                 bool `json:"decal_skull_a,omitempty"`
	DecalSnowflakeA             bool `json:"decal_snowflake_a,omitempty"`
	DecalSnowmanA               bool `json:"decal_snowman_a,omitempty"`
	DecalSpartanA               bool `json:"rwd_decal_spartan_a,omitempty"`
	DecalSpiderA                bool `json:"decal_spider_a,omitempty"`
	DecalSummerPirateA          bool `json:"decal_summer_pirate_a,omitempty"`
	DecalSummerSharkA           bool `json:"decal_summer_shark_a,omitempty"`
	DecalSummerSubmarineA       bool `json:"decal_summer_submarine_a,omitempty"`
	DecalSummerWhaleA           bool `json:"decal_summer_whale_a,omitempty"`
	DecalSwordsA                bool `json:"decal_swords_a,omitempty"`
	DecalVRML                   bool `json:"decal_vrml_a,omitempty" validate:"restricted"`
	DecalWreathA                bool `json:"decal_wreath_a,omitempty"`
	Emissive0001                bool `json:"rwd_emissive_0001,omitempty"`
	Emissive0002                bool `json:"rwd_emissive_0002,omitempty"`
	Emissive0003                bool `json:"rwd_emissive_0003,omitempty"`
	Emissive0004                bool `json:"rwd_emissive_0004,omitempty"`
	Emissive0005                bool `json:"rwd_emissive_0005,omitempty"`
	Emissive0006                bool `json:"rwd_emissive_0006,omitempty"`
	Emissive0007                bool `json:"rwd_emissive_0007,omitempty"`
	Emissive0008                bool `json:"rwd_emissive_0008,omitempty"`
	Emissive0009                bool `json:"rwd_emissive_0009,omitempty"`
	Emissive0010                bool `json:"rwd_emissive_0010,omitempty"`
	Emissive0011                bool `json:"rwd_emissive_0011,omitempty"`
	Emissive0012                bool `json:"rwd_emissive_0012,omitempty"`
	Emissive0013                bool `json:"rwd_emissive_0013,omitempty"`
	Emissive0014                bool `json:"rwd_emissive_0014,omitempty"`
	Emissive0016                bool `json:"rwd_emissive_0016,omitempty"`
	EmissiveUnreleasedLancer    bool `json:"rwd_emissive_0017,omitempty" validate:"restricted"`
	Emissive0023                bool `json:"rwd_emissive_0023,omitempty"`
	Emissive0024                bool `json:"rwd_emissive_0024,omitempty"`
	Emissive0025                bool `json:"rwd_emissive_0025,omitempty"`
	Emissive0026                bool `json:"rwd_emissive_0026,omitempty"`
	EmissiveMoonlight           bool `json:"rwd_emissive_0027,omitempty" validate:"restricted"`
	Emissive0028                bool `json:"rwd_emissive_0028,omitempty"`
	Emissive0029                bool `json:"rwd_emissive_0029,omitempty"`
	EmissiveHalloween           bool `json:"rwd_emissive_0031,omitempty" validate:"restricted"`
	EmissiveVroom               bool `json:"rwd_emissive_0033,omitempty" validate:"restricted"`
	EmissiveDayDream            bool `json:"rwd_emissive_0034,omitempty" validate:"restricted"`
	EmissivePaladin             bool `json:"rwd_emissive_0035,omitempty" validate:"restricted"`
	EmissiveFume                bool `json:"rwd_emissive_0036,omitempty" validate:"restricted"`
	EmissiveCircadian           bool `json:"rwd_emissive_0037,omitempty" validate:"restricted"`
	EmissiveSpringtime          bool `json:"rwd_emissive_0038,omitempty" validate:"restricted"`
	EmissiveValor               bool `json:"rwd_emissive_0040,omitempty" validate:"restricted"`
	EmissiveDefault             bool `json:"emissive_default,omitempty"`
	Emote0000                   bool `json:"rwd_emote_0000,omitempty"`
	Emote0001                   bool `json:"rwd_emote_0001,omitempty"`
	Emote0002                   bool `json:"rwd_emote_0002,omitempty"`
	Emote0005                   bool `json:"rwd_emote_0005,omitempty"`
	Emote0006                   bool `json:"rwd_emote_0006,omitempty"`
	Emote0007                   bool `json:"rwd_emote_0007,omitempty"`
	Emote0010                   bool `json:"rwd_emote_0010,omitempty"`
	Emote0011                   bool `json:"rwd_emote_0011,omitempty"`
	Emote0012                   bool `json:"rwd_emote_0012,omitempty"`
	Emote0016                   bool `json:"rwd_emote_0016,omitempty"`
	Emote0017                   bool `json:"rwd_emote_0017,omitempty"`
	Emote0018                   bool `json:"rwd_emote_0018,omitempty"`
	Emote0019                   bool `json:"rwd_emote_0019,omitempty"`
	Emote0020                   bool `json:"rwd_emote_0020,omitempty"`
	EmoteAngryFaceA             bool `json:"emote_angry_face_a,omitempty"`
	EmoteBatsA                  bool `json:"emote_bats_a,omitempty"`
	EmoteBatteryS1A             bool `json:"rwd_emote_battery_s1_a,omitempty"`
	EmoteBattleCryA             bool `json:"rwd_emote_battle_cry_a,omitempty"`
	EmoteBlinkSmileyA           bool `json:"emote_blink_smiley_a,omitempty"`
	EmoteBrokenHeartA           bool `json:"emote_broken_heart_a,omitempty"`
	EmoteClockA                 bool `json:"emote_clock_a,omitempty"`
	EmoteCoffeeS1A              bool `json:"rwd_emote_coffee_s1_a,omitempty"`
	EmoteCombatAnniversaryA     bool `json:"emote_combat_anniversary_a,omitempty"`
	EmoteCryingFaceA            bool `json:"emote_crying_face_a,omitempty"`
	EmoteDancingOctopusA        bool `json:"emote_dancing_octopus_a,omitempty"`
	EmoteDeadFaceA              bool `json:"emote_dead_face_a,omitempty"`
	EmoteDealGlassesA           bool `json:"emote_deal_glasses_a,omitempty"`
	EmoteDefault                bool `json:"emote_default,omitempty"`
	EmoteDing                   bool `json:"emote_ding,omitempty"`
	EmoteDizzyEyesA             bool `json:"emote_dizzy_eyes_a,omitempty"`
	EmoteDollarEyesA            bool `json:"emote_dollar_eyes_a,omitempty"`
	EmoteExclamationPointA      bool `json:"emote_exclamation_point_a,omitempty"`
	EmoteEyeRollA               bool `json:"emote_eye_roll_a,omitempty"`
	EmoteFireA                  bool `json:"emote_fire_a,omitempty"`
	EmoteFlyingHeartsA          bool `json:"emote_flying_hearts_a,omitempty"`
	EmoteGgA                    bool `json:"emote_gg_a,omitempty"`
	EmoteGingerbreadManA        bool `json:"emote_gingerbread_man_a,omitempty"`
	EmoteHeartEyesA             bool `json:"emote_heart_eyes_a,omitempty"`
	EmoteHourglassA             bool `json:"emote_hourglass_a,omitempty"`
	EmoteKissyLipsA             bool `json:"emote_kissy_lips_a,omitempty"`
	EmoteLightbulbA             bool `json:"emote_lightbulb_a,omitempty"`
	EmoteLightningA             bool `json:"rwd_emote_lightning_a,omitempty"`
	EmoteLoadingA               bool `json:"emote_loading_a,omitempty"`
	EmoteMeteorS1A              bool `json:"rwd_emote_meteor_s1_a,omitempty"`
	EmoteMoneyBagA              bool `json:"emote_money_bag_a,omitempty"`
	EmoteMoustacheA             bool `json:"emote_moustache_a,omitempty"`
	EmoteOneA                   bool `json:"emote_one_a,omitempty"`
	EmotePizzaDance             bool `json:"emote_pizza_dance,omitempty"`
	EmotePresentA               bool `json:"emote_present_a,omitempty"`
	EmotePumpkinFaceA           bool `json:"emote_pumpkin_face_a,omitempty"`
	EmoteQuestionMarkA          bool `json:"emote_question_mark_a,omitempty"`
	EmoteReticleA               bool `json:"emote_reticle_a,omitempty"`
	EmoteRIPA                   bool `json:"emote_rip_a,omitempty"`
	EmoteSamuraiMaskA           bool `json:"rwd_emote_samurai_mask_a,omitempty"`
	EmoteScaredA                bool `json:"emote_scared_a,omitempty"`
	EmoteShiftyEyesS2A          bool `json:"emote_shifty_eyes_s2_a,omitempty"`
	EmoteSickFaceA              bool `json:"emote_sick_face_a,omitempty"`
	EmoteSkullCrossbonesA       bool `json:"emote_skull_crossbones_a,omitempty"`
	EmoteSleepyZzzA             bool `json:"emote_sleepy_zzz_a,omitempty"`
	EmoteSmirkFaceA             bool `json:"emote_smirk_face_a,omitempty"`
	EmoteSnowGlobeA             bool `json:"emote_snow_globe_a,omitempty"`
	EmoteSnowmanA               bool `json:"emote_snowman_a,omitempty"`
	EmoteSoundWaveS2A           bool `json:"emote_sound_wave_s2_a,omitempty"`
	EmoteSpiderA                bool `json:"emote_spider_a,omitempty"`
	EmoteStarEyesA              bool `json:"emote_star_eyes_a,omitempty"`
	EmoteStarSparklesA          bool `json:"emote_star_sparkles_a,omitempty"`
	EmoteStinkyPoopA            bool `json:"emote_stinky_poop_a,omitempty"`
	EmoteTearDropA              bool `json:"emote_tear_drop_a,omitempty"`
	EmoteUwuS2A                 bool `json:"emote_uwu_s2_a,omitempty"`
	EmoteVRMLA                  bool `json:"emote_vrml_a,omitempty" validate:"restricted"`
	EmoteWifiSymbolA            bool `json:"emote_wifi_symbol_a,omitempty"`
	EmoteWinkyTongueA           bool `json:"emote_winky_tongue_a,omitempty"`
	GoalFx0002                  bool `json:"rwd_goal_fx_0002,omitempty"`
	GoalFx0003                  bool `json:"rwd_goal_fx_0003,omitempty"`
	GoalFx0004                  bool `json:"rwd_goal_fx_0004,omitempty"`
	GoalFx0005                  bool `json:"rwd_goal_fx_0005,omitempty"`
	GoalFx0008                  bool `json:"rwd_goal_fx_0008,omitempty"`
	GoalFx0009                  bool `json:"rwd_goal_fx_0009,omitempty" validate:"blocked"`
	GoalFx0010                  bool `json:"rwd_goal_fx_0010,omitempty"`
	GoalFx0011                  bool `json:"rwd_goal_fx_0011,omitempty"`
	GoalFx0012                  bool `json:"rwd_goal_fx_0012,omitempty"`
	GoalFx0013                  bool `json:"rwd_goal_fx_0013,omitempty"`
	GoalFx0015                  bool `json:"rwd_goal_fx_0015,omitempty"`
	GoalFxDefault               bool `json:"rwd_goal_fx_default,omitempty"`
	HeraldryDefault             bool `json:"heraldry_default,omitempty"`
	LoadoutNumber               bool `json:"loadout_number,omitempty"`
	Medal0000                   bool `json:"rwd_medal_0000,omitempty"`
	Medal0001                   bool `json:"rwd_medal_0001,omitempty"`
	Medal0002                   bool `json:"rwd_medal_0002,omitempty"`
	Medal0003                   bool `json:"rwd_medal_0003,omitempty"`
	Medal0004                   bool `json:"rwd_medal_0004,omitempty"`
	Medal0005                   bool `json:"rwd_medal_0005,omitempty"`
	Medal0009                   bool `json:"rwd_medal_0009,omitempty"`
	Medal0010                   bool `json:"rwd_medal_0010,omitempty"`
	Medal0011                   bool `json:"rwd_medal_0011,omitempty"`
	Medal0012                   bool `json:"rwd_medal_0012,omitempty"`
	Medal0013                   bool `json:"rwd_medal_0013,omitempty"`
	Medal0014                   bool `json:"rwd_medal_0014,omitempty"`
	Medal0015                   bool `json:"rwd_medal_0015,omitempty"`
	Medal0016                   bool `json:"rwd_medal_0016,omitempty"`
	Medal0017                   bool `json:"rwd_medal_0017,omitempty" validate:"blocked"`
	MedalDefault                bool `json:"rwd_medal_default,omitempty"`
	MedalLoneEcho2_A            bool `json:"rwd_medal_lone_echo_2_a,omitempty"`
	MedalS1ArenaBronze          bool `json:"rwd_medal_s1_arena_bronze,omitempty"`
	MedalS1ArenaGold            bool `json:"rwd_medal_s1_arena_gold,omitempty" `
	MedalS1ArenaSilver          bool `json:"rwd_medal_s1_arena_silver,omitempty" `
	MedalS1EchoPassBronze       bool `json:"rwd_medal_s1_echo_pass_bronze,omitempty" `
	MedalS1EchoPassGold         bool `json:"rwd_medal_s1_echo_pass_gold,omitempty" `
	MedalS1EchoPassSilver       bool `json:"rwd_medal_s1_echo_pass_silver,omitempty" `
	MedalS1QuestLaunch          bool `json:"rwd_medal_s1_quest_launch,omitempty" `
	MedalS2EchoPassBronze       bool `json:"rwd_medal_s2_echo_pass_bronze,omitempty" `
	MedalS2EchoPassGold         bool `json:"rwd_medal_s2_echo_pass_gold,omitempty" `
	MedalS2EchoPassSilver       bool `json:"rwd_medal_s2_echo_pass_silver,omitempty" `
	MedalS3EchoPassBronzeA      bool `json:"rwd_medal_s3_echo_pass_bronze_a,omitempty" `
	MedalS3EchoPassGoldA        bool `json:"rwd_medal_s3_echo_pass_gold_a,omitempty" `
	MedalS3EchoPassSilverA      bool `json:"rwd_medal_s3_echo_pass_silver_a,omitempty" `
	MedalVRMLPreseason          bool `json:"rwd_medal_s1_vrml_preseason,omitempty" validate:"restricted"`
	MedalVRMLS1                 bool `json:"rwd_medal_s1_vrml_s1_user,omitempty" validate:"restricted"`
	MedalVRMLS1Champion         bool `json:"rwd_medal_s1_vrml_s1_champion,omitempty" validate:"restricted"`
	MedalVRMLS1Finalist         bool `json:"rwd_medal_s1_vrml_s1_finalist,omitempty" validate:"restricted"`
	MedalVRMLS2                 bool `json:"rwd_medal_s1_vrml_s2,omitempty" validate:"restricted"`
	MedalVRMLS2Champion         bool `json:"rwd_medal_s1_vrml_s2_champion,omitempty" validate:"restricted"`
	MedalVRMLS2Finalist         bool `json:"rwd_medal_s1_vrml_s2_finalist,omitempty" validate:"restricted"`
	MedalVRMLS3                 bool `json:"rwd_medal_s1_vrml_s3,omitempty" validate:"restricted"`
	MedalVRMLS3Champion         bool `json:"rwd_medal_s1_vrml_s3_champion,omitempty" validate:"restricted"`
	MedalVRMLS3Finalist         bool `json:"rwd_medal_s1_vrml_s3_finalist,omitempty" validate:"restricted"`
	MedalVRMLS4                 bool `json:"rwd_medal_0006,omitempty"  validate:"restricted"`
	MedalVRMLS4Finalist         bool `json:"rwd_medal_0007,omitempty"  validate:"restricted"`
	MedalVRMLS4Champion         bool `json:"rwd_medal_0008,omitempty"  validate:"restricted"`
	Pattern0000                 bool `json:"rwd_pattern_0000,omitempty"`
	Pattern0001                 bool `json:"rwd_pattern_0001,omitempty"`
	Pattern0002                 bool `json:"rwd_pattern_0002,omitempty"`
	Pattern0005                 bool `json:"rwd_pattern_0005,omitempty"`
	Pattern0006                 bool `json:"rwd_pattern_0006,omitempty"`
	Pattern0008                 bool `json:"rwd_pattern_0008,omitempty"`
	Pattern0009                 bool `json:"rwd_pattern_0009,omitempty"`
	Pattern0010                 bool `json:"rwd_pattern_0010,omitempty"`
	Pattern0011                 bool `json:"rwd_pattern_0011,omitempty"`
	Pattern0012                 bool `json:"rwd_pattern_0012,omitempty"`
	Pattern0016                 bool `json:"rwd_pattern_0016,omitempty"`
	Pattern0017                 bool `json:"rwd_pattern_0017,omitempty"`
	Pattern0018                 bool `json:"rwd_pattern_0018,omitempty"`
	Pattern0019                 bool `json:"rwd_pattern_0019,omitempty"`
	Pattern0020                 bool `json:"rwd_pattern_0020,omitempty"`
	Pattern0021                 bool `json:"rwd_pattern_0021,omitempty"`
	PatternAlienA               bool `json:"rwd_pattern_alien_a,omitempty"`
	PatternAnglesA              bool `json:"pattern_angles_a,omitempty"`
	PatternArrowheadsA          bool `json:"pattern_arrowheads_a,omitempty"`
	PatternBananasA             bool `json:"pattern_bananas_a,omitempty"`
	PatternCatsA                bool `json:"pattern_cats_a,omitempty"`
	PatternChevronA             bool `json:"pattern_chevron_a,omitempty"`
	PatternCircuitBoardA        bool `json:"rwd_pattern_circuit_board_a,omitempty"`
	PatternCubesA               bool `json:"pattern_cubes_a,omitempty"`
	PatternCupcakeA             bool `json:"rwd_pattern_cupcake_a,omitempty"`
	PatternDefault              bool `json:"pattern_default,omitempty"`
	PatternDiamondPlateA        bool `json:"pattern_diamond_plate_a,omitempty"`
	PatternDiamondsA            bool `json:"pattern_diamonds_a,omitempty"`
	PatternDigitalCamoA         bool `json:"pattern_digital_camo_a,omitempty"`
	PatternDotsA                bool `json:"pattern_dots_a,omitempty"`
	PatternDumbbellsA           bool `json:"pattern_dumbbells_a,omitempty"`
	PatternGearsA               bool `json:"pattern_gears_a,omitempty"`
	PatternHamburgerA           bool `json:"rwd_pattern_hamburger_a,omitempty"`
	PatternHawaiianA            bool `json:"pattern_hawaiian_a,omitempty"`
	PatternHoneycombTripleA     bool `json:"pattern_honeycomb_triple_a,omitempty"`
	PatternInsetCubesA          bool `json:"pattern_inset_cubes_a,omitempty"`
	PatternLeopardA             bool `json:"pattern_leopard_a,omitempty"`
	PatternLightningA           bool `json:"pattern_lightning_a,omitempty"`
	PatternPawsA                bool `json:"pattern_paws_a,omitempty"`
	PatternPineappleA           bool `json:"pattern_pineapple_a,omitempty"`
	PatternPizzaA               bool `json:"rwd_pattern_pizza_a,omitempty"`
	PatternQuestA               bool `json:"rwd_pattern_quest_a,omitempty"`
	PatternRageWolfA            bool `json:"rwd_pattern_rage_wolf_a,omitempty"`
	PatternRocketA              bool `json:"rwd_pattern_rocket_a,omitempty"`
	PatternS1A                  bool `json:"rwd_pattern_s1_a,omitempty"`
	PatternS1B                  bool `json:"rwd_pattern_s1_b,omitempty"`
	PatternS1C                  bool `json:"rwd_pattern_s1_c,omitempty"`
	PatternS1D                  bool `json:"rwd_pattern_s1_d,omitempty"`
	PatternS2A                  bool `json:"rwd_pattern_s2_a,omitempty"`
	PatternS2B                  bool `json:"rwd_pattern_s2_b,omitempty"`
	PatternS2C                  bool `json:"rwd_pattern_s2_c,omitempty"`
	PatternSaltA                bool `json:"rwd_pattern_salt_a,omitempty"`
	PatternScalesA              bool `json:"pattern_scales_a,omitempty"`
	PatternSeigaihaA            bool `json:"rwd_pattern_seigaiha_a,omitempty"`
	PatternSkullA               bool `json:"rwd_pattern_skull_a,omitempty"`
	PatternSpearShieldA         bool `json:"rwd_pattern_spear_shield_a,omitempty"`
	PatternSpookyBandagesA      bool `json:"pattern_spooky_bandages_a,omitempty"`
	PatternSpookyBatsA          bool `json:"pattern_spooky_bats_a,omitempty"`
	PatternSpookyCobwebA        bool `json:"pattern_spooky_cobweb_a,omitempty"`
	PatternSpookyPumpkinsA      bool `json:"pattern_spooky_pumpkins_a,omitempty"`
	PatternSpookySkullsA        bool `json:"pattern_spooky_skulls_a,omitempty"`
	PatternSpookyStitchesA      bool `json:"pattern_spooky_stitches_a,omitempty"`
	PatternSquigglesA           bool `json:"pattern_squiggles_a,omitempty"`
	PatternStarsA               bool `json:"pattern_stars_a,omitempty"`
	PatternStreaksA             bool `json:"pattern_streaks_a,omitempty"`
	PatternStringsA             bool `json:"pattern_strings_a,omitempty"`
	PatternSummerHawaiianA      bool `json:"pattern_summer_hawaiian_a,omitempty"`
	PatternSwirlA               bool `json:"pattern_swirl_a,omitempty"`
	PatternTableclothA          bool `json:"pattern_tablecloth_a,omitempty"`
	PatternTigerA               bool `json:"pattern_tiger_a,omitempty"`
	PatternTreadsA              bool `json:"pattern_treads_a,omitempty"`
	PatternTrexSkullA           bool `json:"rwd_pattern_trex_skull_a,omitempty"`
	PatternTrianglesA           bool `json:"pattern_triangles_a,omitempty"`
	PatternWeaveA               bool `json:"pattern_weave_a,omitempty"`
	PatternXmasFlourishA        bool `json:"pattern_xmas_flourish_a,omitempty"`
	PatternXmasKnitA            bool `json:"pattern_xmas_knit_a,omitempty"`
	PatternXmasKnitFlowersA     bool `json:"pattern_xmas_knit_flowers_a,omitempty"`
	PatternXmasLightsA          bool `json:"pattern_xmas_lights_a,omitempty"`
	PatternXmasMistletoeA       bool `json:"pattern_xmas_mistletoe_a,omitempty"`
	PatternXmasSnowflakesA      bool `json:"pattern_xmas_snowflakes_a,omitempty"`
	Pip0001                     bool `json:"rwd_pip_0001,omitempty"`
	Pip0005                     bool `json:"rwd_pip_0005,omitempty"`
	Pip0006                     bool `json:"rwd_pip_0006,omitempty"`
	Pip0007                     bool `json:"rwd_pip_0007,omitempty"`
	Pip0008                     bool `json:"rwd_pip_0008,omitempty"`
	Pip0009                     bool `json:"rwd_pip_0009,omitempty"`
	Pip0010                     bool `json:"rwd_pip_0010,omitempty"`
	Pip0011                     bool `json:"rwd_pip_0011,omitempty"`
	Pip0013                     bool `json:"rwd_pip_0013,omitempty"`
	Pip0014                     bool `json:"rwd_pip_0014,omitempty"`
	Pip0015                     bool `json:"rwd_pip_0015,omitempty"`
	Pip0017                     bool `json:"rwd_pip_0017,omitempty"`
	Pip0018                     bool `json:"rwd_pip_0018,omitempty"`
	Pip0019                     bool `json:"rwd_pip_0019,omitempty"`
	Pip0020                     bool `json:"rwd_pip_0020,omitempty"`
	Pip0021                     bool `json:"rwd_pip_0021,omitempty"`
	Pip0022                     bool `json:"rwd_pip_0022,omitempty"`
	Pip0023                     bool `json:"rwd_pip_0023,omitempty"`
	Pip0024                     bool `json:"rwd_pip_0024,omitempty"`
	Pip0025                     bool `json:"rwd_pip_0025,omitempty"`
	PipStubUi                   bool `json:"rwd_pip_0026,omitempty" validate:"blocked"`
	PlaceholderBooster          bool `json:"rwd_booster_ranger_a,omitempty" validate:"blocked"`
	PlaceholderBracer           bool `json:"rwd_bracer_ranger_a,omitempty" validate:"blocked"`
	PlaceholderChassis          bool `json:"rwd_chassis_ranger_a,omitempty" validate:"blocked"`
	RWDBanner0004               bool `json:"rwd_banner_0004,omitempty"`
	RWDBanner0005               bool `json:"rwd_banner_0005,omitempty"`
	RWDBanner0006               bool `json:"rwd_banner_0006,omitempty"`
	RWDBanner0007               bool `json:"rwd_banner_0007,omitempty"`
	RWDBanner0012               bool `json:"rwd_banner_0012,omitempty"`
	RWDBanner0013               bool `json:"rwd_banner_0013,omitempty"`
	RWDBanner0018               bool `json:"rwd_banner_0018,omitempty"`
	RWDBanner0019               bool `json:"rwd_banner_0019,omitempty"`
	RWDBanner0020               bool `json:"rwd_banner_0020,omitempty"`
	RWDBanner0026               bool `json:"rwd_banner_0026,omitempty"`
	RWDBanner0027               bool `json:"rwd_banner_0027,omitempty"`
	RWDBanner0028               bool `json:"rwd_banner_0028,omitempty"`
	RWDBanner0029               bool `json:"rwd_banner_0029,omitempty"`
	RWDBannerS2Lines            bool `json:"rwd_banner_s2_lines,omitempty"`
	RWDBoosterAvianA            bool `json:"rwd_booster_avian_a,omitempty"`
	RWDBoosterBaroqueA          bool `json:"rwd_booster_baroque_a,omitempty"`
	RWDBoosterExoA              bool `json:"rwd_booster_exo_a,omitempty"`
	RWDBoosterFlamingoA         bool `json:"rwd_booster_flamingo_a,omitempty"`
	RWDBoosterFragmentA         bool `json:"rwd_booster_fragment_a,omitempty"`
	RWDBoosterFrostA            bool `json:"rwd_booster_frost_a,omitempty"`
	RWDBoosterHalloweenA        bool `json:"rwd_booster_halloween_a,omitempty"`
	RWDBoosterHeartbreakA       bool `json:"rwd_booster_heartbreak_a,omitempty"`
	RWDBoosterLavaA             bool `json:"rwd_booster_lava_a,omitempty"`
	RWDBoosterMechA             bool `json:"rwd_booster_mech_a,omitempty"`
	RWDBoosterNinjaA            bool `json:"rwd_booster_ninja_a,omitempty"`
	RWDBoosterOrganicA          bool `json:"rwd_booster_organic_a,omitempty"`
	RWDBoosterOvergrownA        bool `json:"rwd_booster_overgrown_a,omitempty"`
	RWDBoosterPaladinA          bool `json:"rwd_booster_paladin_a,omitempty"`
	RWDBoosterReptileA          bool `json:"rwd_booster_reptile_a,omitempty"`
	RWDBoosterRetroA            bool `json:"rwd_booster_retro_a,omitempty"`
	RWDBoosterSamuraiAOni       bool `json:"rwd_booster_samurai_a_oni,omitempty"`
	RWDBoosterSnacktimeA        bool `json:"rwd_booster_snacktime_a,omitempty"`
	RWDBoosterSpeedformA        bool `json:"rwd_booster_speedform_a,omitempty"`
	RWDBoosterTrexASkelerex     bool `json:"rwd_booster_trex_a_skelerex,omitempty"`
	RWDBoosterVroomA            bool `json:"rwd_booster_vroom_a,omitempty"`
	RWDBoosterWolfA             bool `json:"rwd_booster_wolf_a,omitempty"`
	RWDBracerAvianA             bool `json:"rwd_bracer_avian_a,omitempty"`
	RWDBracerBaroqueA           bool `json:"rwd_bracer_baroque_a,omitempty"`
	RWDBracerExoA               bool `json:"rwd_bracer_exo_a,omitempty"`
	RWDBracerFlamingoA          bool `json:"rwd_bracer_flamingo_a,omitempty"`
	RWDBracerFragmentA          bool `json:"rwd_bracer_fragment_a,omitempty"`
	RWDBracerFrostA             bool `json:"rwd_bracer_frost_a,omitempty"`
	RWDBracerHalloweenA         bool `json:"rwd_bracer_halloween_a,omitempty"`
	RWDBracerHeartbreakA        bool `json:"rwd_bracer_heartbreak_a,omitempty"`
	RWDBracerLavaA              bool `json:"rwd_bracer_lava_a,omitempty"`
	RWDBracerMechA              bool `json:"rwd_bracer_mech_a,omitempty"`
	RWDBracerNinjaA             bool `json:"rwd_bracer_ninja_a,omitempty"`
	RWDBracerOrganicA           bool `json:"rwd_bracer_organic_a,omitempty"`
	RWDBracerOvergrownA         bool `json:"rwd_bracer_overgrown_a,omitempty"`
	RWDBracerPaladinA           bool `json:"rwd_bracer_paladin_a,omitempty"`
	RWDBracerReptileA           bool `json:"rwd_bracer_reptile_a,omitempty"`
	RWDBracerRetroA             bool `json:"rwd_bracer_retro_a,omitempty"`
	RWDBracerSamuraiAOni        bool `json:"rwd_bracer_samurai_a_oni,omitempty"`
	RWDBracerSpeedformA         bool `json:"rwd_bracer_speedform_a,omitempty"`
	RWDBracerTrexASkelerex      bool `json:"rwd_bracer_trex_a_skelerex,omitempty"`
	RWDBracerVroomA             bool `json:"rwd_bracer_vroom_a,omitempty"`
	RWDBracerWolfA              bool `json:"rwd_bracer_wolf_a,omitempty"`
	RWDChassisExoA              bool `json:"rwd_chassis_exo_a,omitempty"`
	RWDChassisFrostA            bool `json:"rwd_chassis_frost_a,omitempty"`
	RWDChassisNinjaA            bool `json:"rwd_chassis_ninja_a,omitempty"`
	RWDChassisOvergrownA        bool `json:"rwd_chassis_overgrown_a,omitempty"`
	RWDChassisSamuraiAOni       bool `json:"rwd_chassis_samurai_a_oni,omitempty"`
	RWDChassisSportyA           bool `json:"rwd_chassis_sporty_a,omitempty"`
	RWDChassisTrexASkelerex     bool `json:"rwd_chassis_trex_a_skelerex,omitempty"`
	RWDChassisWolfA             bool `json:"rwd_chassis_wolf_a,omitempty"`
	RWDCurrencyStarterPack01    bool `json:"rwd_currency_starter_pack_01,omitempty"`
	RWDDecal0003                bool `json:"rwd_decal_0003,omitempty"`
	RWDDecal0004                bool `json:"rwd_decal_0004,omitempty"`
	RWDDecal0008                bool `json:"rwd_decal_0008,omitempty"`
	RWDDecal0009                bool `json:"rwd_decal_0009,omitempty"`
	RWDDecal0013                bool `json:"rwd_decal_0013,omitempty"`
	RWDDecal0014                bool `json:"rwd_decal_0014,omitempty"`
	RWDDecal0021                bool `json:"rwd_decal_0021,omitempty"`
	RWDDecal0022                bool `json:"rwd_decal_0022,omitempty"`
	RWDEmissive0015             bool `json:"rwd_emissive_0015,omitempty"`
	RWDEmissive0018             bool `json:"rwd_emissive_0018,omitempty"`
	RWDEmissive0019             bool `json:"rwd_emissive_0019,omitempty"`
	RWDEmissive0020             bool `json:"rwd_emissive_0020,omitempty"`
	RWDEmissive0021             bool `json:"rwd_emissive_0021,omitempty"`
	RWDEmissive0022             bool `json:"rwd_emissive_0022,omitempty"`
	RWDEmissive0030             bool `json:"rwd_emissive_0030,omitempty"`
	RWDEmissive0032             bool `json:"rwd_emissive_0032,omitempty"`
	RWDEmissive0039             bool `json:"rwd_emissive_0039,omitempty"`
	RWDEmote0003                bool `json:"rwd_emote_0003,omitempty"`
	RWDEmote0004                bool `json:"rwd_emote_0004,omitempty"`
	RWDEmote0008                bool `json:"rwd_emote_0008,omitempty"`
	RWDEmote0009                bool `json:"rwd_emote_0009,omitempty"`
	RWDEmote0013                bool `json:"rwd_emote_0013,omitempty"`
	RWDEmote0014                bool `json:"rwd_emote_0014,omitempty"`
	RWDEmoteGhost               bool `json:"rwd_emote_0015,omitempty" validate:"restricted"`
	RWDEmote0021                bool `json:"rwd_emote_0021,omitempty"`
	RWDEmote0022                bool `json:"rwd_emote_0022,omitempty"`
	RWDEmote0023                bool `json:"rwd_emote_0023,omitempty"`
	RWDEmote0024                bool `json:"rwd_emote_0024,omitempty"`
	RWDGoalFx0001               bool `json:"rwd_goal_fx_0001,omitempty"`
	RWDGoalFx0006               bool `json:"rwd_goal_fx_0006,omitempty"`
	RWDGoalFx0007               bool `json:"rwd_goal_fx_0007,omitempty"`
	RWDGoalFx0014               bool `json:"rwd_goal_fx_0014,omitempty"`
	RWDPattern0003              bool `json:"rwd_pattern_0003,omitempty"`
	RWDPattern0004              bool `json:"rwd_pattern_0004,omitempty"`
	RWDPattern0007              bool `json:"rwd_pattern_0007,omitempty"`
	RWDPip0002                  bool `json:"rwd_pip_0002,omitempty"`
	RWDPip0003                  bool `json:"rwd_pip_0003,omitempty"`
	RWDPip0004                  bool `json:"rwd_pip_0004,omitempty"`
	RWDPip0012                  bool `json:"rwd_pip_0012,omitempty"`
	RWDPip0016                  bool `json:"rwd_pip_0016,omitempty"`
	RWDTag0016                  bool `json:"rwd_tag_0016,omitempty"`
	RWDTag0017                  bool `json:"rwd_tag_0017,omitempty"`
	RWDTagS1RSecondary          bool `json:"rwd_tag_s1_r_secondary,omitempty"`
	RWDTint0003                 bool `json:"rwd_tint_0003,omitempty"`
	RWDTint0004                 bool `json:"rwd_tint_0004,omitempty"`
	RWDTint0005                 bool `json:"rwd_tint_0005,omitempty"`
	RWDTint0006                 bool `json:"rwd_tint_0006,omitempty"`
	RWDTint0010                 bool `json:"rwd_tint_0010,omitempty"`
	RWDTint0011                 bool `json:"rwd_tint_0011,omitempty"`
	RWDTint0012                 bool `json:"rwd_tint_0012,omitempty"`
	RWDTint0016                 bool `json:"rwd_tint_0016,omitempty"`
	RWDTint0017                 bool `json:"rwd_tint_0017,omitempty"`
	RWDTint0018                 bool `json:"rwd_tint_0018,omitempty"`
	RWDTint0028                 bool `json:"rwd_tint_0028,omitempty"`
	RWDTint0029                 bool `json:"rwd_tint_0029,omitempty"`
	TintBlueSeafoam             bool `json:"rwd_tint_s1_b_default,omitempty"`
	RWDTintS2ADefault           bool `json:"rwd_tint_s2_a_default,omitempty"`
	RWDTitle0003                bool `json:"rwd_title_0003,omitempty"`
	RWDTitle0016                bool `json:"rwd_title_0016,omitempty"`
	RWDTitle0017                bool `json:"rwd_title_0017,omitempty"`
	RWDTitle0018                bool `json:"rwd_title_0018,omitempty"`
	RWDTitle0019                bool `json:"rwd_title_0019,omitempty"`
	StubMedal0018               bool `json:"rwd_medal_0018,omitempty" validate:"blocked"`
	StubMedal0019               bool `json:"rwd_medal_0019,omitempty" validate:"blocked"`
	StubMedal0026               bool `json:"rwd_medal_0026,omitempty" validate:"blocked"`
	StubPattern0013             bool `json:"rwd_pattern_0013,omitempty" validate:"blocked"`
	StubPattern0014             bool `json:"rwd_pattern_0014,omitempty" validate:"blocked"`
	StubPattern0015             bool `json:"rwd_pattern_0015,omitempty" validate:"blocked"`
	StubPattern0022             bool `json:"rwd_pattern_0022,omitempty"  validate:"blocked"`
	Tag0000                     bool `json:"rwd_tag_0000,omitempty" `
	Tag0001                     bool `json:"rwd_tag_0001,omitempty"`
	Tag0003                     bool `json:"rwd_tag_0003,omitempty"`
	Tag0004                     bool `json:"rwd_tag_0004,omitempty"`
	Tag0005                     bool `json:"rwd_tag_0005,omitempty"`
	Tag0006                     bool `json:"rwd_tag_0006,omitempty"`
	Tag0007                     bool `json:"rwd_tag_0007,omitempty"`
	Tag0012                     bool `json:"rwd_tag_0012,omitempty" validate:"blocked"`
	Tag0013                     bool `json:"rwd_tag_0013,omitempty" validate:"blocked"`
	Tag0014                     bool `json:"rwd_tag_0014,omitempty" validate:"blocked"`
	Tag0015                     bool `json:"rwd_tag_0015,omitempty" validate:"blocked"`
	Tag0018                     bool `json:"rwd_tag_0018,omitempty" validate:"blocked"`
	Tag0019                     bool `json:"rwd_tag_0019,omitempty" validate:"blocked"`
	Tag0020                     bool `json:"rwd_tag_0020,omitempty" validate:"blocked"`
	Tag0021                     bool `json:"rwd_tag_0021,omitempty" validate:"blocked"`
	Tag0023                     bool `json:"rwd_tag_0023,omitempty" validate:"blocked"`
	Tag0025                     bool `json:"rwd_tag_0025,omitempty" validate:"blocked"`
	Tag0026                     bool `json:"rwd_tag_0026,omitempty" validate:"blocked"`
	Tag0027                     bool `json:"rwd_tag_0027,omitempty" validate:"blocked"`
	Tag0028                     bool `json:"rwd_tag_0028,omitempty" validate:"blocked"`
	Tag0029                     bool `json:"rwd_tag_0029,omitempty" validate:"blocked"`
	Tag0030                     bool `json:"rwd_tag_0030,omitempty" validate:"blocked"`
	Tag0031                     bool `json:"rwd_tag_0031,omitempty" validate:"blocked"`
	Tag0033                     bool `json:"rwd_tag_0033,omitempty" validate:"blocked"`
	Tag0034                     bool `json:"rwd_tag_0034,omitempty" validate:"blocked"`
	Tag0038                     bool `json:"rwd_tag_0038,omitempty" validate:"blocked"`
	Tag0039                     bool `json:"rwd_tag_0039,omitempty" validate:"blocked"`
	TagDefault                  bool `json:"rwd_tag_default,omitempty"`
	TagDeveloper                bool `json:"rwd_tag_s1_developer,omitempty" validate:"restricted"`
	TagDiamonds                 bool `json:"rwd_tag_diamonds_a,omitempty"`
	TagGameAdmin                bool `json:"rwd_tag_0011,omitempty" validate:"restricted"`
	TagLoneEcho2                bool `json:"rwd_tag_lone_echo_2_a,omitempty"`
	TagS1ASecondary             bool `json:"rwd_tag_s1_a_secondary,omitempty"`
	TagS1BSecondary             bool `json:"rwd_tag_s1_b_secondary,omitempty"`
	TagS1CSecondary             bool `json:"rwd_tag_s1_c_secondary,omitempty"`
	TagS1DSecondary             bool `json:"rwd_tag_s1_d_secondary,omitempty"`
	TagS1ESecondary             bool `json:"rwd_tag_s1_e_secondary,omitempty"`
	TagS1FSecondary             bool `json:"rwd_tag_s1_f_secondary,omitempty"`
	TagS1GSecondary             bool `json:"rwd_tag_s1_g_secondary,omitempty"`
	TagS1HSecondary             bool `json:"rwd_tag_s1_h_secondary,omitempty"`
	TagS1ISecondary             bool `json:"rwd_tag_s1_i_secondary,omitempty"`
	TagS1JSecondary             bool `json:"rwd_tag_s1_j_secondary,omitempty"`
	TagS1KSecondary             bool `json:"rwd_tag_s1_k_secondary,omitempty"`
	TagS1MSecondary             bool `json:"rwd_tag_s1_m_secondary,omitempty"`
	TagS1OSecondary             bool `json:"rwd_tag_s1_o_secondary,omitempty"`
	TagS1QSecondary             bool `json:"rwd_tag_s1_q_secondary,omitempty"`
	TagS1TSecondary             bool `json:"rwd_tag_s1_t_secondary,omitempty"`
	TagS1VSecondary             bool `json:"rwd_tag_s1_v_secondary,omitempty"`
	TagS2BSecondary             bool `json:"rwd_tag_s2_b_secondary,omitempty"`
	TagS2C                      bool `json:"rwd_tag_s2_c,omitempty"`
	TagS2GSecondary             bool `json:"rwd_tag_s2_g_secondary,omitempty"`
	TagS2HSecondary             bool `json:"rwd_tag_s2_h_secondary,omitempty"`
	TagSpearA                   bool `json:"rwd_tag_spear_a,omitempty"`
	TagStub0002                 bool `json:"rwd_tag_0002,omitempty" validate:"blocked"`
	TagStub0032                 bool `json:"rwd_tag_0032,omitempty" validate:"blocked"`
	TagStub0046                 bool `json:"rwd_tag_0046,omitempty" validate:"blocked"`
	TagStub0047                 bool `json:"rwd_tag_0047,omitempty" validate:"blocked"`
	TagStub0048                 bool `json:"rwd_tag_0048,omitempty" validate:"blocked"`
	TagStub0049                 bool `json:"rwd_tag_0049,omitempty" validate:"blocked"`
	TagStub0050                 bool `json:"rwd_tag_0050,omitempty" validate:"blocked"`
	TagStub0051                 bool `json:"rwd_tag_0051,omitempty" validate:"blocked"`
	TagStub0052                 bool `json:"rwd_tag_0052,omitempty" validate:"blocked"`
	TagStub0053                 bool `json:"rwd_tag_0053,omitempty" validate:"blocked"`
	TagStub0054                 bool `json:"rwd_tag_0054,omitempty" validate:"blocked"`
	TagTagUnreleased0024        bool `json:"rwd_tag_0024,omitempty" validate:"blocked"`
	TagToriA                    bool `json:"rwd_tag_tori_a,omitempty"`
	TagUnreleased0022           bool `json:"rwd_tag_0022,omitempty" validate:"blocked"`
	TagVRMLPreseason            bool `json:"rwd_tag_s1_vrml_preseason,omitempty" validate:"restricted"`
	TagVRMLS1                   bool `json:"rwd_tag_s1_vrml_s1,omitempty" validate:"restricted"`
	TagVRMLS1Champion           bool `json:"rwd_tag_s1_vrml_s1_champion,omitempty" validate:"restricted"`
	TagVRMLS1Finalist           bool `json:"rwd_tag_s1_vrml_s1_finalist,omitempty" validate:"restricted"`
	TagVRMLS2                   bool `json:"rwd_tag_s1_vrml_s2,omitempty" validate:"restricted"`
	TagVRMLS2Champion           bool `json:"rwd_tag_s1_vrml_s2_champion,omitempty" validate:"restricted"`
	TagVRMLS2Finalist           bool `json:"rwd_tag_s1_vrml_s2_finalist,omitempty" validate:"restricted"`
	TagVRMLS3                   bool `json:"rwd_tag_s1_vrml_s3,omitempty" validate:"restricted"`
	TagVRMLS3Champion           bool `json:"rwd_tag_s1_vrml_s3_champion,omitempty" validate:"restricted"`
	TagVRMLS3Finalist           bool `json:"rwd_tag_s1_vrml_s3_finalist,omitempty" validate:"restricted"`
	TagVRMLS4                   bool `json:"rwd_tag_0008,omitempty" validate:"restricted"`
	TagVRMLS4Champion           bool `json:"rwd_tag_0010,omitempty" validate:"restricted"`
	TagVRMLS4Finalist           bool `json:"rwd_tag_0009,omitempty" validate:"restricted"`
	TagVRMLS5                   bool `json:"rwd_tag_0035,omitempty" validate:"restricted"`
	TagVRMLS5Champion           bool `json:"rwd_tag_0037,omitempty" validate:"restricted"`
	TagVRMLS5Finalist           bool `json:"rwd_tag_0036,omitempty" validate:"restricted"`
	TagVRMLS6                   bool `json:"rwd_tag_0040,omitempty" validate:"restricted"`
	TagVRMLS6Champion           bool `json:"rwd_tag_0042,omitempty" validate:"restricted"`
	TagVRMLS6Finalist           bool `json:"rwd_tag_0041,omitempty" validate:"restricted"`
	TagVRMLS7                   bool `json:"rwd_tag_0043,omitempty" validate:"restricted"`
	TagVRMLS7Champion           bool `json:"rwd_tag_0045,omitempty" validate:"restricted"`
	TagVRMLS7Finalist           bool `json:"rwd_tag_0044,omitempty" validate:"restricted"`
	Tint0000                    bool `json:"rwd_tint_0000,omitempty"`
	Tint0001                    bool `json:"rwd_tint_0001,omitempty"`
	Tint0002                    bool `json:"rwd_tint_0002,omitempty"`
	Tint0007                    bool `json:"rwd_tint_0007,omitempty"`
	Tint0008                    bool `json:"rwd_tint_0008,omitempty"`
	Tint0009                    bool `json:"rwd_tint_0009,omitempty"`
	Tint0013                    bool `json:"rwd_tint_0013,omitempty"`
	Tint0014                    bool `json:"rwd_tint_0014,omitempty"`
	Tint0015                    bool `json:"rwd_tint_0015,omitempty"`
	TintMesopalgic              bool `json:"rwd_tint_0019,omitempty"  validate:"restricted"`
	Tint0020                    bool `json:"rwd_tint_0020,omitempty"`
	Tint0021                    bool `json:"rwd_tint_0021,omitempty"`
	Tint0022                    bool `json:"rwd_tint_0022,omitempty"`
	Tint0023                    bool `json:"rwd_tint_0023,omitempty"`
	Tint0024                    bool `json:"rwd_tint_0024,omitempty"`
	Tint0025                    bool `json:"rwd_tint_0025,omitempty"`
	TintStub1                   bool `json:"rwd_tint_0026,omitempty"  validate:"restricted"`
	TintStub2                   bool `json:"rwd_tint_0027,omitempty"  validate:"restricted"`
	TintBlueADefault            bool `json:"tint_blue_a_default,omitempty"`
	TintBlueBDefault            bool `json:"tint_blue_b_default,omitempty"`
	TintBlueCDefault            bool `json:"tint_blue_c_default,omitempty"`
	TintBlueDDefault            bool `json:"tint_blue_d_default,omitempty"`
	TintBlueEDefault            bool `json:"tint_blue_e_default,omitempty"`
	TintBlueFDefault            bool `json:"tint_blue_f_default,omitempty"`
	TintBlueGDefault            bool `json:"tint_blue_g_default,omitempty"`
	TintBlueHDefault            bool `json:"tint_blue_h_default,omitempty"`
	TintBlueDeepSea             bool `json:"tint_blue_i_default,omitempty"`
	TintBlueJDefault            bool `json:"tint_blue_j_default,omitempty"`
	TintBlueKDefault            bool `json:"tint_blue_k_default,omitempty"`
	TintChassisDefault          bool `json:"tint_chassis_default,omitempty"`
	TintNeutralADefault         bool `json:"tint_neutral_a_default,omitempty"`
	TintNeutralAS10Default      bool `json:"tint_neutral_a_s10_default,omitempty"`
	TintNeutralBDefault         bool `json:"tint_neutral_b_default,omitempty"`
	TintNeutralCDefault         bool `json:"tint_neutral_c_default,omitempty"`
	TintNeutralDDefault         bool `json:"tint_neutral_d_default,omitempty"`
	TintNeutralEDefault         bool `json:"tint_neutral_e_default,omitempty"`
	TintNeutralFDefault         bool `json:"tint_neutral_f_default,omitempty"`
	TintNeutralGDefault         bool `json:"tint_neutral_g_default,omitempty"`
	TintNeutralHDefault         bool `json:"tint_neutral_h_default,omitempty"`
	TintNeutralIDefault         bool `json:"tint_neutral_i_default,omitempty"`
	TintNeutralJDefault         bool `json:"tint_neutral_j_default,omitempty"`
	TintNeutralKDefault         bool `json:"tint_neutral_k_default,omitempty"`
	TintNeutralLDefault         bool `json:"tint_neutral_l_default,omitempty"`
	TintNeutralMDefault         bool `json:"tint_neutral_m_default,omitempty"`
	TintNeutralNDefault         bool `json:"tint_neutral_n_default,omitempty"`
	TintNeutralSpookyADefault   bool `json:"tint_neutral_spooky_a_default,omitempty"`
	TintNeutralSpookyBDefault   bool `json:"tint_neutral_spooky_b_default,omitempty"`
	TintNeutralSpookyCDefault   bool `json:"tint_neutral_spooky_c_default,omitempty"`
	TintNeutralSpookyDDefault   bool `json:"tint_neutral_spooky_d_default,omitempty"`
	TintNeutralSpookyEDefault   bool `json:"tint_neutral_spooky_e_default,omitempty"`
	TintBlueTerraformed         bool `json:"tint_neutral_summer_a_default,omitempty"`
	TintNeutralXmasADefault     bool `json:"tint_neutral_xmas_a_default,omitempty"`
	TintNeutralXmasBDefault     bool `json:"tint_neutral_xmas_b_default,omitempty"`
	TintNeutralXmasCDefault     bool `json:"tint_neutral_xmas_c_default,omitempty"`
	TintNeutralXmasDDefault     bool `json:"tint_neutral_xmas_d_default,omitempty"`
	TintNeutralXmasEDefault     bool `json:"tint_neutral_xmas_e_default,omitempty"`
	TintOrangeADefault          bool `json:"tint_orange_a_default,omitempty"`
	TintOrangeBDefault          bool `json:"tint_orange_b_default,omitempty"`
	TintOrangeCDefault          bool `json:"tint_orange_c_default,omitempty"`
	TintOrangeDDefault          bool `json:"tint_orange_d_default,omitempty"`
	TintOrangeEDefault          bool `json:"tint_orange_e_default,omitempty"`
	TintOrangeFDefault          bool `json:"tint_orange_f_default,omitempty"`
	TintOrangeGDefault          bool `json:"tint_orange_g_default,omitempty"`
	TintOrangeHDefault          bool `json:"tint_orange_h_default,omitempty"`
	TintOrangeIDefault          bool `json:"tint_orange_i_default,omitempty"`
	TintOrangeBioMass           bool `json:"tint_orange_j_default,omitempty"`
	TintOrangeKDefault          bool `json:"tint_orange_k_default,omitempty"`
	TintS1ADefault              bool `json:"rwd_tint_s1_a_default,omitempty"`
	TintS1CDefault              bool `json:"rwd_tint_s1_c_default,omitempty"`
	TintBlueLuminesence         bool `json:"rwd_tint_s1_d_default,omitempty"`
	TintS2BDefault              bool `json:"rwd_tint_s2_b_default,omitempty"`
	TintS2CDefault              bool `json:"rwd_tint_s2_c_default,omitempty"`
	TintS3TintA                 bool `json:"rwd_tint_s3_tint_a,omitempty"`
	TintBloodstained            bool `json:"rwd_tint_s3_tint_b,omitempty"`
	TintS3TintC                 bool `json:"rwd_tint_s3_tint_c,omitempty"`
	TintS3TintD                 bool `json:"rwd_tint_s3_tint_d,omitempty"`
	TintS3TintE                 bool `json:"rwd_tint_s3_tint_e,omitempty"`
	Title0000                   bool `json:"rwd_title_0000,omitempty"`
	Title0001                   bool `json:"rwd_title_0001,omitempty"`
	Title0002                   bool `json:"rwd_title_0002,omitempty"`
	Title0004                   bool `json:"rwd_title_0004,omitempty"`
	Title0005                   bool `json:"rwd_title_0005,omitempty"`
	Title0006                   bool `json:"rwd_title_0006,omitempty"`
	Title0007                   bool `json:"rwd_title_0007,omitempty"`
	Title0008                   bool `json:"rwd_title_0008,omitempty"`
	Title0009                   bool `json:"rwd_title_0009,omitempty"`
	Title0010                   bool `json:"rwd_title_0010,omitempty"`
	Title0011                   bool `json:"rwd_title_0011,omitempty"`
	Title0012                   bool `json:"rwd_title_0012,omitempty"`
	Title0013                   bool `json:"rwd_title_0013,omitempty"`
	Title0014                   bool `json:"rwd_title_0014,omitempty"`
	Title0015                   bool `json:"rwd_title_0015,omitempty"`
	TitleGuardianA              bool `json:"rwd_title_guardian_a,omitempty"`
	TitleRoninA                 bool `json:"rwd_title_ronin_a,omitempty"`
	TitleS1A                    bool `json:"rwd_title_s1_a,omitempty"`
	TitleS1B                    bool `json:"rwd_title_s1_b,omitempty"`
	TitleS1C                    bool `json:"rwd_title_s1_c,omitempty"`
	TitleS2A                    bool `json:"rwd_title_s2_a,omitempty"`
	TitleS2B                    bool `json:"rwd_title_s2_b,omitempty"`
	TitleS2C                    bool `json:"rwd_title_s2_c,omitempty"`
	TitleShieldBearerA          bool `json:"rwd_title_shield_bearer_a,omitempty"`
	TitleTitleA                 bool `json:"rwd_title_title_a,omitempty"`
	TitleTitleC                 bool `json:"rwd_title_title_c,omitempty"`
	TitleTitleD                 bool `json:"rwd_title_title_d,omitempty"`
	TitleTitleDefault           bool `json:"rwd_title_title_default,omitempty"`
	TitleTitleE                 bool `json:"rwd_title_title_e,omitempty"`
	XPBoostGroupS0101           bool `json:"rwd_xp_boost_group_s01_01,omitempty" validate:"restricted"`
	XPBoostGroupS0102           bool `json:"rwd_xp_boost_group_s01_02,omitempty"`
	XPBoostGroupS0103           bool `json:"rwd_xp_boost_group_s01_03,omitempty"`
	XPBoostGroupS0104           bool `json:"rwd_xp_boost_group_s01_04,omitempty"`
	XPBoostGroupS0105           bool `json:"rwd_xp_boost_group_s01_05,omitempty"`
	XPBoostGroupS0201           bool `json:"rwd_xp_boost_group_s02_01,omitempty"`
	XPBoostGroupS0202           bool `json:"rwd_xp_boost_group_s02_02,omitempty"`
	XPBoostGroupS0203           bool `json:"rwd_xp_boost_group_s02_03,omitempty"`
	XPBoostGroupS0204           bool `json:"rwd_xp_boost_group_s02_04,omitempty"`
	XPBoostGroupS0205           bool `json:"rwd_xp_boost_group_s02_05,omitempty"`
	XPBoostGroupS0301           bool `json:"rwd_xp_boost_group_s03_01,omitempty"`
	XPBoostGroupS0302           bool `json:"rwd_xp_boost_group_s03_02,omitempty"`
	XPBoostGroupS0303           bool `json:"rwd_xp_boost_group_s03_03,omitempty"`
	XPBoostGroupS0304           bool `json:"rwd_xp_boost_group_s03_04,omitempty"`
	XPBoostGroupS0305           bool `json:"rwd_xp_boost_group_s03_05,omitempty"`
	XPBoostGroupS0401           bool `json:"rwd_xp_boost_group_s04_01,omitempty"`
	XPBoostGroupS0402           bool `json:"rwd_xp_boost_group_s04_02,omitempty"`
	XPBoostGroupS0403           bool `json:"rwd_xp_boost_group_s04_03,omitempty"`
	XPBoostGroupS0404           bool `json:"rwd_xp_boost_group_s04_04,omitempty"`
	XPBoostGroupS0405           bool `json:"rwd_xp_boost_group_s04_05,omitempty"`
	XPBoostGroupS0501           bool `json:"rwd_xp_boost_group_s05_01,omitempty"`
	XPBoostGroupS0502           bool `json:"rwd_xp_boost_group_s05_02,omitempty"`
	XPBoostGroupS0503           bool `json:"rwd_xp_boost_group_s05_03,omitempty"`
	XPBoostGroupS0504           bool `json:"rwd_xp_boost_group_s05_04,omitempty"`
	XPBoostGroupS0505           bool `json:"rwd_xp_boost_group_s05_05,omitempty"`
	XPBoostGroupS0601           bool `json:"rwd_xp_boost_group_s06_01,omitempty"`
	XPBoostGroupS0602           bool `json:"rwd_xp_boost_group_s06_02,omitempty"`
	XPBoostGroupS0603           bool `json:"rwd_xp_boost_group_s06_03,omitempty"`
	XPBoostGroupS0604           bool `json:"rwd_xp_boost_group_s06_04,omitempty"`
	XPBoostGroupS0605           bool `json:"rwd_xp_boost_group_s06_05,omitempty"`
	XPBoostGroupS0701           bool `json:"rwd_xp_boost_group_s07_01,omitempty"`
	XPBoostGroupS0702           bool `json:"rwd_xp_boost_group_s07_02,omitempty"`
	XPBoostGroupS0703           bool `json:"rwd_xp_boost_group_s07_03,omitempty"`
	XPBoostGroupS0704           bool `json:"rwd_xp_boost_group_s07_04,omitempty"`
	XPBoostGroupS0705           bool `json:"rwd_xp_boost_group_s07_05,omitempty"`
	XPBoostIndividualS0101      bool `json:"rwd_xp_boost_individual_s01_01,omitempty"`
	XPBoostIndividualS0102      bool `json:"rwd_xp_boost_individual_s01_02,omitempty"`
	XPBoostIndividualS0103      bool `json:"rwd_xp_boost_individual_s01_03,omitempty"`
	XPBoostIndividualS0104      bool `json:"rwd_xp_boost_individual_s01_04,omitempty"`
	XPBoostIndividualS0105      bool `json:"rwd_xp_boost_individual_s01_05,omitempty"`
	XPBoostIndividualS0201      bool `json:"rwd_xp_boost_individual_s02_01,omitempty"`
	XPBoostIndividualS0202      bool `json:"rwd_xp_boost_individual_s02_02,omitempty"`
	XPBoostIndividualS0203      bool `json:"rwd_xp_boost_individual_s02_03,omitempty"`
	XPBoostIndividualS0204      bool `json:"rwd_xp_boost_individual_s02_04,omitempty"`
	XPBoostIndividualS0205      bool `json:"rwd_xp_boost_individual_s02_05,omitempty"`
	XPBoostIndividualS0301      bool `json:"rwd_xp_boost_individual_s03_01,omitempty"`
	XPBoostIndividualS0302      bool `json:"rwd_xp_boost_individual_s03_02,omitempty"`
	XPBoostIndividualS0303      bool `json:"rwd_xp_boost_individual_s03_03,omitempty"`
	XPBoostIndividualS0304      bool `json:"rwd_xp_boost_individual_s03_04,omitempty"`
	XPBoostIndividualS0305      bool `json:"rwd_xp_boost_individual_s03_05,omitempty"`
	XPBoostIndividualS0401      bool `json:"rwd_xp_boost_individual_s04_01,omitempty"`
	XPBoostIndividualS0402      bool `json:"rwd_xp_boost_individual_s04_02,omitempty"`
	XPBoostIndividualS0403      bool `json:"rwd_xp_boost_individual_s04_03,omitempty"`
	XPBoostIndividualS0404      bool `json:"rwd_xp_boost_individual_s04_04,omitempty"`
	XPBoostIndividualS0405      bool `json:"rwd_xp_boost_individual_s04_05,omitempty"`
	XPBoostIndividualS0501      bool `json:"rwd_xp_boost_individual_s05_01,omitempty"`
	XPBoostIndividualS0502      bool `json:"rwd_xp_boost_individual_s05_02,omitempty"`
	XPBoostIndividualS0503      bool `json:"rwd_xp_boost_individual_s05_03,omitempty"`
	XPBoostIndividualS0504      bool `json:"rwd_xp_boost_individual_s05_04,omitempty"`
	XPBoostIndividualS0505      bool `json:"rwd_xp_boost_individual_s05_05,omitempty"`
	XPBoostIndividualS0601      bool `json:"rwd_xp_boost_individual_s06_01,omitempty"`
	XPBoostIndividualS0602      bool `json:"rwd_xp_boost_individual_s06_02,omitempty"`
	XPBoostIndividualS0603      bool `json:"rwd_xp_boost_individual_s06_03,omitempty"`
	XPBoostIndividualS0604      bool `json:"rwd_xp_boost_individual_s06_04,omitempty"`
	XPBoostIndividualS0605      bool `json:"rwd_xp_boost_individual_s06_05,omitempty"`
	XPBoostIndividualS0701      bool `json:"rwd_xp_boost_individual_s07_01,omitempty"`
	XPBoostIndividualS0702      bool `json:"rwd_xp_boost_individual_s07_02,omitempty"`
	XPBoostIndividualS0703      bool `json:"rwd_xp_boost_individual_s07_03,omitempty"`
	XPBoostIndividualS0704      bool `json:"rwd_xp_boost_individual_s07_04,omitempty"`
	XPBoostIndividualS0705      bool `json:"rwd_xp_boost_individual_s07_05,omitempty"`
}

type CombatUnlocks struct {
	DecalCombatFlamingoA bool `json:"decal_combat_flamingo_a,omitempty"`
	DecalCombatLogoA     bool `json:"decal_combat_logo_a,omitempty"`
	EmoteDizzyEyesA      bool `json:"emote_dizzy_eyes_a,omitempty"`
	PatternLightningA    bool `json:"pattern_lightning_a,omitempty"`
	BoosterS10           bool `json:"rwd_booster_s10,omitempty"`
	ChassisBodyS10A      bool `json:"rwd_chassis_body_s10_a,omitempty"`
	MedalS1CombatBronze  bool `json:"rwd_medal_s1_combat_bronze,omitempty"`
	MedalS1CombatGold    bool `json:"rwd_medal_s1_combat_gold,omitempty"`
	MedalS1CombatSilver  bool `json:"rwd_medal_s1_combat_silver,omitempty"`
	TitleTitleB          bool `json:"rwd_title_title_b,omitempty"`
}

func NewStatistics() map[string]map[string]MatchStatistic {
	return map[string]map[string]MatchStatistic{
		"arena": {
			"Level": MatchStatistic{
				Operation: "add",
				Value:     1,
			},
		},
		"combat": {
			"Level": MatchStatistic{
				Operation: "add",
				Value:     1,
			},
		},
	}
}

func NewServerProfile() ServerProfile {
	// This is the default server profile that EchoVR shipped with.
	return ServerProfile{
		PurchasedCombat: 1,
		SchemaVersion:   4,
		EquippedCosmetics: EquippedCosmetics{
			Instances: CosmeticInstances{
				Unified: UnifiedCosmeticInstance{
					Slots: DefaultCosmeticLoadout(),
				},
			},
			Number:     1,
			NumberBody: 1,
		},
		Statistics: map[string]map[string]MatchStatistic{
			"arena": {
				"Level": MatchStatistic{
					Operation: "add",
					Value:     1,
				},
			},
			"combat": {
				"Level": MatchStatistic{
					Operation: "add",
					Value:     1,
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
		EvrID: EvrIdNil,
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

func NewClientProfile() ClientProfile {

	return ClientProfile{

		CombatWeapon:       "assault",
		CombatGrenade:      "det",
		CombatDominantHand: 1,
		CombatAbility:      "heal",
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
		Social: ClientSocial{
			CommunityValuesVersion: 1,
			SetupVersion:           1,
		},
		NewUnlocks: []int64{},
	}
}

func DefaultGameProfiles(evrID EvrId, displayname string) (GameProfiles, error) {
	client := NewClientProfile()
	server := NewServerProfile()
	client.EvrID = evrID
	server.EvrID = evrID
	client.DisplayName = displayname
	server.DisplayName = displayname

	return GameProfiles{
		Client: client,
		Server: server,
	}, nil
}
