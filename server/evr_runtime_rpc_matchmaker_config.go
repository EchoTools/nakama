package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"reflect"

	"github.com/heroiclabs/nakama-common/runtime"
)

// FieldSchema describes a configuration field for documentation purposes
type FieldSchema struct {
	Type        string                 `json:"type"`
	Description string                 `json:"description"`
	Fields      map[string]FieldSchema `json:"fields,omitempty"`
}

// =============================================================================
// Query Addons Configuration
// =============================================================================

// MatchmakerConfigQueryAddons contains query addons for different matchmaking scenarios
type MatchmakerConfigQueryAddons struct {
	Allocate     string `json:"allocate" usage:"Additional query for match allocation."`
	Create       string `json:"create" usage:"Additional query for lobby creation."`
	RPCAllocate  string `json:"rpc_allocate" usage:"Additional query for RPC-based allocation."`
	Backfill     string `json:"backfill" usage:"Additional query for backfill matching."`
	Matchmaking  string `json:"matchmaking" usage:"Additional query for matchmaking tickets."`
	LobbyBuilder string `json:"lobby_builder" usage:"Additional query for lobby builder server allocation."`
}

// =============================================================================
// Skill Rating Configuration
// =============================================================================

// MatchmakerConfigRatingDefaults contains the default skill rating values
type MatchmakerConfigRatingDefaults struct {
	Z     int     `json:"z" usage:"Number of standard deviations for ordinal calculation."`
	Mu    float64 `json:"mu" usage:"Initial mean skill rating."`
	Sigma float64 `json:"sigma" usage:"Initial uncertainty (standard deviation)."`
	Tau   float64 `json:"tau" usage:"Dynamics factor - prevents sigma from dropping too low."`
}

// MatchmakerConfigSkillRating contains the skill rating configuration
type MatchmakerConfigSkillRating struct {
	Defaults              MatchmakerConfigRatingDefaults `json:"defaults" usage:"Default rating values for new players."`
	WinningTeamBonus      float64                        `json:"winning_team_bonus" usage:"Bonus added to winning team players' scores before rating calculation."`
	TeamStatMultipliers   map[string]float64             `json:"team_stat_multipliers" usage:"Multipliers for team-based rating calculations by stat name."`
	PlayerStatMultipliers map[string]float64             `json:"player_stat_multipliers" usage:"Multipliers for individual player rating calculations by stat name."`
}

// =============================================================================
// Timeout Configuration
// =============================================================================

// MatchmakerConfigTimeouts contains timeout-related settings
type MatchmakerConfigTimeouts struct {
	MatchmakingTimeoutSecs int `json:"matchmaking_timeout_secs" usage:"Maximum matchmaking timeout in seconds."`
	FailsafeTimeoutSecs    int `json:"failsafe_timeout_secs" usage:"Failsafe timeout in seconds before forcing a match."`
	FallbackTimeoutSecs    int `json:"fallback_timeout_secs" usage:"Fallback timeout in seconds before relaxing match criteria."`
}

// =============================================================================
// Backfill Configuration
// =============================================================================

// MatchmakerConfigBackfill contains backfill-related settings
type MatchmakerConfigBackfill struct {
	DisableArenaBackfill    bool `json:"disable_arena_backfill" usage:"Disable backfilling for arena matches."`
	BackfillMinTimeSecs     int  `json:"backfill_min_time_secs" usage:"Minimum time in seconds before backfilling a player to a match."`
	ArenaBackfillMaxAgeSecs int  `json:"arena_backfill_max_age_secs" usage:"Maximum age in seconds of arena matches to backfill."`
}

// =============================================================================
// Early Quit Penalty Configuration
// =============================================================================

// MatchmakerConfigEarlyQuit contains early quit penalty settings
type MatchmakerConfigEarlyQuit struct {
	EnableEarlyQuitPenalty  bool   `json:"enable_early_quit_penalty" usage:"Enable early quit penalty system."`
	SilentEarlyQuitSystem   bool   `json:"silent_early_quit_system" usage:"Disable Discord DM notifications for early quit tier changes."`
	EarlyQuitTier1Threshold *int32 `json:"early_quit_tier1_threshold" usage:"Penalty level threshold for Tier 1 (good standing)."`
	EarlyQuitTier2Threshold *int32 `json:"early_quit_tier2_threshold" usage:"Penalty level threshold for Tier 2 (reserved for future tiers)."`
}

// =============================================================================
// SBMM (Skill-Based Matchmaking) Configuration
// =============================================================================

// MatchmakerConfigSBMM contains skill-based matchmaking settings
type MatchmakerConfigSBMM struct {
	EnableSkillBasedMatchmaking bool    `json:"enable_skill_based_matchmaking" usage:"Enable skill-based matchmaking (SBMM)."`
	SBMMMinPlayerCount          int     `json:"sbmm_min_player_count" usage:"Minimum player count to enable skill-based matchmaking."`
	SBMMMatchmakerUseMu         bool    `json:"sbmm_matchmaker_use_mu" usage:"Use Mu instead of Ordinal for matchmaker player MMR values."`
	SBMMBackfillQueriesUseMu    bool    `json:"sbmm_backfill_queries_use_mu" usage:"Use Mu instead of Ordinal for backfill queries."`
	SBMMMatchmakingTicketsUseMu bool    `json:"sbmm_matchmaking_tickets_use_mu" usage:"Use Mu instead of Ordinal for matchmaking tickets."`
	RatingRange                 float64 `json:"rating_range" usage:"Maximum rating difference for matching players."`
	EnableOrdinalRange          bool    `json:"enable_ordinal_range" usage:"Enable ordinal range filtering in matchmaking."`
	PartySkillBoostPercent      float64 `json:"party_skill_boost_percent" usage:"Boost party effective skill by this percentage to account for coordination advantage."`
}

// =============================================================================
// Division Configuration
// =============================================================================

// MatchmakerConfigDivisions contains division-related settings
type MatchmakerConfigDivisions struct {
	EnableDivisions                bool `json:"enable_divisions" usage:"Enable skill-based divisions for matchmaking."`
	GreenDivisionMaxAccountAgeDays int  `json:"green_division_max_account_age_days" usage:"Maximum account age in days to be in the green (new player) division."`
}

// =============================================================================
// Team Formation Configuration
// =============================================================================

// MatchmakerConfigTeamFormation contains team formation settings
type MatchmakerConfigTeamFormation struct {
	EnableRosterVariants       bool `json:"enable_roster_variants" usage:"Generate multiple roster variants (balanced/stacked) for better match selection."`
	UseSnakeDraftTeamFormation bool `json:"use_snake_draft_team_formation" usage:"Use snake draft instead of sequential filling for team formation."`
}

// =============================================================================
// Server Selection Configuration
// =============================================================================

// MatchmakerConfigServerSelection contains server selection settings
type MatchmakerConfigServerSelection struct {
	MaxServerRTT int `json:"max_server_rtt" usage:"Maximum round-trip time (ms) to allow for server selection."`
}

// =============================================================================
// Main Response Structure
// =============================================================================

// MatchmakerConfigResponse is the response from the matchmaker/config RPC
type MatchmakerConfigResponse struct {
	QueryAddons     MatchmakerConfigQueryAddons     `json:"query_addons" usage:"Additional queries to add to matchmaking queries."`
	Timeouts        MatchmakerConfigTimeouts        `json:"timeouts" usage:"Timeout settings for matchmaking."`
	Backfill        MatchmakerConfigBackfill        `json:"backfill" usage:"Backfill settings for joining in-progress matches."`
	EarlyQuit       MatchmakerConfigEarlyQuit       `json:"early_quit" usage:"Early quit penalty settings."`
	SBMM            MatchmakerConfigSBMM            `json:"sbmm" usage:"Skill-based matchmaking settings."`
	Divisions       MatchmakerConfigDivisions       `json:"divisions" usage:"Division settings for player segmentation."`
	TeamFormation   MatchmakerConfigTeamFormation   `json:"team_formation" usage:"Team formation settings."`
	ServerSelection MatchmakerConfigServerSelection `json:"server_selection" usage:"Server selection settings."`
	SkillRating     MatchmakerConfigSkillRating     `json:"skill_rating" usage:"Skill rating configuration for SBMM."`
}

// MatchmakerConfigWithSchema wraps config values with their schema
type MatchmakerConfigWithSchema struct {
	Config MatchmakerConfigResponse `json:"config"`
	Schema map[string]FieldSchema   `json:"schema"`
}

// =============================================================================
// Schema Extraction Utilities
// =============================================================================

// extractSchema uses reflection to build a schema from struct tags
func extractSchema(t reflect.Type, _ string) map[string]FieldSchema {
	schema := make(map[string]FieldSchema)

	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	if t.Kind() != reflect.Struct {
		return schema
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}
		// Handle json tag options like "omitempty"
		for j, c := range jsonTag {
			if c == ',' {
				jsonTag = jsonTag[:j]
				break
			}
		}

		usage := field.Tag.Get("usage")
		fieldType := field.Type

		fs := FieldSchema{
			Type:        getTypeName(fieldType),
			Description: usage,
		}

		// Recurse into nested structs
		if fieldType.Kind() == reflect.Struct {
			fs.Fields = extractSchema(fieldType, jsonTag+".")
		} else if fieldType.Kind() == reflect.Ptr && fieldType.Elem().Kind() == reflect.Struct {
			fs.Fields = extractSchema(fieldType.Elem(), jsonTag+".")
		}

		schema[jsonTag] = fs
	}

	return schema
}

// getTypeName returns a human-readable type name
func getTypeName(t reflect.Type) string {
	switch t.Kind() {
	case reflect.Ptr:
		return getTypeName(t.Elem())
	case reflect.Slice:
		return "array"
	case reflect.Map:
		return "object"
	case reflect.Struct:
		return "object"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return "integer"
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	case reflect.Bool:
		return "boolean"
	case reflect.String:
		return "string"
	default:
		return t.String()
	}
}

// =============================================================================
// RPC Handler
// =============================================================================

// MatchmakerConfigRPC returns the current matchmaker configuration with schema
func MatchmakerConfigRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	settings := ServiceSettings()
	if settings == nil {
		return "", runtime.NewError("Service settings not initialized", StatusInternalError)
	}

	mm := settings.Matchmaking
	sr := settings.SkillRating

	config := MatchmakerConfigResponse{
		QueryAddons: MatchmakerConfigQueryAddons{
			Allocate:     mm.QueryAddons.Allocate,
			Create:       mm.QueryAddons.Create,
			RPCAllocate:  mm.QueryAddons.RPCAllocate,
			Backfill:     mm.QueryAddons.Backfill,
			Matchmaking:  mm.QueryAddons.Matchmaking,
			LobbyBuilder: mm.QueryAddons.LobbyBuilder,
		},
		Timeouts: MatchmakerConfigTimeouts{
			MatchmakingTimeoutSecs: mm.MatchmakingTimeoutSecs,
			FailsafeTimeoutSecs:    mm.FailsafeTimeoutSecs,
			FallbackTimeoutSecs:    mm.FallbackTimeoutSecs,
		},
		Backfill: MatchmakerConfigBackfill{
			DisableArenaBackfill:    mm.DisableArenaBackfill,
			BackfillMinTimeSecs:     mm.BackfillMinTimeSecs,
			ArenaBackfillMaxAgeSecs: mm.ArenaBackfillMaxAgeSecs,
		},
		EarlyQuit: MatchmakerConfigEarlyQuit{
			EnableEarlyQuitPenalty:  mm.EnableEarlyQuitPenalty,
			SilentEarlyQuitSystem:   mm.SilentEarlyQuitSystem,
			EarlyQuitTier1Threshold: mm.EarlyQuitTier1Threshold,
			EarlyQuitTier2Threshold: mm.EarlyQuitTier2Threshold,
		},
		SBMM: MatchmakerConfigSBMM{
			EnableSkillBasedMatchmaking: mm.EnableSBMM,
			SBMMMinPlayerCount:          mm.SBMMMinPlayerCount,
			SBMMMatchmakerUseMu:         mm.MatchmakerUseMu,
			SBMMBackfillQueriesUseMu:    mm.BackfillQueriesUseMu,
			SBMMMatchmakingTicketsUseMu: mm.MatchmakingTicketsUseMu,
			RatingRange:                 mm.RatingRange,
			EnableOrdinalRange:          mm.EnableOrdinalRange,
			PartySkillBoostPercent:      mm.PartySkillBoostPercent,
		},
		Divisions: MatchmakerConfigDivisions{
			EnableDivisions:                mm.EnableDivisions,
			GreenDivisionMaxAccountAgeDays: mm.GreenDivisionMaxAccountAgeDays,
		},
		TeamFormation: MatchmakerConfigTeamFormation{
			EnableRosterVariants:       mm.EnableRosterVariants,
			UseSnakeDraftTeamFormation: mm.UseSnakeDraftTeamFormation,
		},
		ServerSelection: MatchmakerConfigServerSelection{
			MaxServerRTT: mm.MaxServerRTT,
		},
		SkillRating: MatchmakerConfigSkillRating{
			Defaults: MatchmakerConfigRatingDefaults{
				Z:     sr.Defaults.Z,
				Mu:    sr.Defaults.Mu,
				Sigma: sr.Defaults.Sigma,
				Tau:   sr.Defaults.Tau,
			},
			WinningTeamBonus:      sr.WinningTeamBonus,
			TeamStatMultipliers:   sr.TeamStatMultipliers,
			PlayerStatMultipliers: sr.PlayerStatMultipliers,
		},
	}

	// Build schema from struct tags using reflection
	schema := extractSchema(reflect.TypeOf(config), "")

	response := MatchmakerConfigWithSchema{
		Config: config,
		Schema: schema,
	}

	data, err := json.Marshal(response)
	if err != nil {
		return "", runtime.NewError("Failed to marshal response", StatusInternalError)
	}

	return string(data), nil
}
