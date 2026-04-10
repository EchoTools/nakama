package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// discordSnowflakePattern matches valid Discord snowflake IDs (17-20 digit numbers).
var discordSnowflakePattern = regexp.MustCompile(`^\d{17,20}$`)

// ServiceSettingsRPC handles reading and updating the global service settings.
// GET: returns the current settings.
// POST: validates and applies updated settings.
// Permission: Global Operators only (enforced via RPC registration).
func ServiceSettingsRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	if payload == "" || payload == "{}" {
		// GET: return current settings
		settings := ServiceSettings()
		if settings == nil {
			return "", runtime.NewError("Service settings not loaded", StatusInternalError)
		}

		data, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return "", runtime.NewError("Failed to marshal settings", StatusInternalError)
		}
		return string(data), nil
	}

	// POST: validate and update settings
	var incoming ServiceSettingsData
	if err := json.Unmarshal([]byte(payload), &incoming); err != nil {
		return "", runtime.NewError(fmt.Sprintf("Invalid JSON: %s", err.Error()), StatusInvalidArgument)
	}

	if errs := validateServiceSettings(&incoming); len(errs) > 0 {
		errJSON, _ := json.Marshal(map[string]interface{}{
			"errors": errs,
		})
		return "", runtime.NewError(string(errJSON), StatusInvalidArgument)
	}

	// Apply defaults for any zero-value fields that should have defaults
	FixDefaultServiceSettings(logger, &incoming)

	// Update in-memory and persist
	ServiceSettingsUpdate(&incoming)
	if err := ServiceSettingsSave(ctx, nk); err != nil {
		logger.Error("Failed to save service settings", zap.Error(err))
		return "", runtime.NewError("Failed to save settings", StatusInternalError)
	}

	logger.Info("Service settings updated",
		zap.String("caller_id", ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)),
	)

	data, err := json.MarshalIndent(&incoming, "", "  ")
	if err != nil {
		return "", runtime.NewError("Failed to marshal updated settings", StatusInternalError)
	}
	return string(data), nil
}

// validateServiceSettings checks all fields for validity and returns a list of
// human-readable error strings. An empty slice means the settings are valid.
func validateServiceSettings(s *ServiceSettingsData) []string {
	var errs []string

	// --- String length limits ---
	if len(s.LinkInstructions) > 2000 {
		errs = append(errs, "link_instructions must be at most 2000 characters")
	}
	if len(s.DisableLoginMessage) > 500 {
		errs = append(errs, "disable_login_message must be at most 500 characters")
	}
	if len(s.ReportURL) > 500 {
		errs = append(errs, "report_url must be at most 500 characters")
	}

	// --- Discord IDs (optional but must be valid if set) ---
	checkDiscordID := func(field, value string) {
		if value != "" && !discordSnowflakePattern.MatchString(value) {
			errs = append(errs, fmt.Sprintf("%s must be a valid Discord snowflake ID (17-20 digits) or empty", field))
		}
	}
	checkDiscordID("service_guild_id", s.ServiceGuildID)
	checkDiscordID("service_audit_channel_id", s.ServiceAuditChannelID)
	checkDiscordID("service_sessions_channel_id", s.ServiceSessionsChannelID)
	checkDiscordID("service_debug_channel_id", s.ServiceDebugChannelID)
	checkDiscordID("service_error_channel_id", s.GlobalErrorChannelID)
	checkDiscordID("service_command_log_channel_id", s.CommandLogChannelID)
	checkDiscordID("discord_bot_user_id", s.DiscordBotUserID)
	checkDiscordID("vrml_entitlement_notify_channel_id", s.VRMLEntitlementNotifyChannelID)

	// --- Prune settings ---
	if s.PruneSettings.SafetyLimit < 0 {
		errs = append(errs, "prune_settings.safety_limit must be non-negative")
	}
	if s.PruneSettings.SafetyLimit > 10000 {
		errs = append(errs, "prune_settings.safety_limit must be at most 10000")
	}

	// --- Skill rating ---
	sr := s.SkillRating
	if sr.Defaults.Z < 1 || sr.Defaults.Z > 10 {
		errs = append(errs, "skill_rating.defaults.z must be between 1 and 10")
	}
	if sr.Defaults.Mu < 0.1 || sr.Defaults.Mu > 100 {
		errs = append(errs, "skill_rating.defaults.mu must be between 0.1 and 100")
	}
	if sr.Defaults.Sigma < 0.01 || sr.Defaults.Sigma > 100 {
		errs = append(errs, "skill_rating.defaults.sigma must be between 0.01 and 100")
	}
	if sr.Defaults.Tau < 0 || sr.Defaults.Tau > 10 {
		errs = append(errs, "skill_rating.defaults.tau must be between 0 and 10")
	}
	if sr.WinningTeamBonus < 0 || sr.WinningTeamBonus > 100 {
		errs = append(errs, "skill_rating.winning_team_bonus must be between 0 and 100")
	}

	// Validate stat multiplier keys
	for key := range sr.TeamStatMultipliers {
		if !ValidTeamStatFields[key] {
			errs = append(errs, fmt.Sprintf("skill_rating.team_stat_multipliers: invalid stat key %q (valid: %s)", key, validStatKeysStr()))
		}
	}
	for key := range sr.PlayerStatMultipliers {
		if !ValidTeamStatFields[key] {
			errs = append(errs, fmt.Sprintf("skill_rating.player_stat_multipliers: invalid stat key %q", key))
		}
	}

	// Validate stat multiplier values
	for key, val := range sr.TeamStatMultipliers {
		if val < -100 || val > 100 {
			errs = append(errs, fmt.Sprintf("skill_rating.team_stat_multipliers.%s must be between -100 and 100", key))
		}
	}
	for key, val := range sr.PlayerStatMultipliers {
		if val < -100 || val > 100 {
			errs = append(errs, fmt.Sprintf("skill_rating.player_stat_multipliers.%s must be between -100 and 100", key))
		}
	}

	// --- Matchmaking ---
	mm := s.Matchmaking

	if mm.MatchmakingTimeoutSecs < 10 || mm.MatchmakingTimeoutSecs > 600 {
		errs = append(errs, "matchmaking.matchmaking_timeout_secs must be between 10 and 600")
	}
	if mm.FailsafeTimeoutSecs < 0 || mm.FailsafeTimeoutSecs > 600 {
		errs = append(errs, "matchmaking.failsafe_timeout_secs must be between 0 and 600")
	}
	if mm.FallbackTimeoutSecs < 0 || mm.FallbackTimeoutSecs > 600 {
		errs = append(errs, "matchmaking.fallback_timeout_secs must be between 0 and 600")
	}
	if mm.FailsafeTimeoutSecs >= mm.MatchmakingTimeoutSecs && mm.FailsafeTimeoutSecs > 0 {
		errs = append(errs, "matchmaking.failsafe_timeout_secs must be less than matchmaking_timeout_secs")
	}
	if mm.FallbackTimeoutSecs >= mm.FailsafeTimeoutSecs && mm.FallbackTimeoutSecs > 0 && mm.FailsafeTimeoutSecs > 0 {
		errs = append(errs, "matchmaking.fallback_timeout_secs must be less than failsafe_timeout_secs")
	}
	if mm.MaxMatchmakingTickets < 0 || mm.MaxMatchmakingTickets > 1000 {
		errs = append(errs, "matchmaking.max_matchmaking_tickets must be between 0 and 1000")
	}
	if mm.ArenaBackfillMaxAgeSecs < 0 || mm.ArenaBackfillMaxAgeSecs > 600 {
		errs = append(errs, "matchmaking.arena_backfill_max_age_secs must be between 0 and 600")
	}
	if mm.MaxServerRTT < 0 || mm.MaxServerRTT > 1000 {
		errs = append(errs, "matchmaking.max_server_rtt must be between 0 and 1000")
	}
	if mm.SBMMMinPlayerCount < 0 || mm.SBMMMinPlayerCount > 1000 {
		errs = append(errs, "matchmaking.sbmm_min_player_count must be between 0 and 1000")
	}
	if mm.PartySkillBoostPercent < 0 || mm.PartySkillBoostPercent > 1.0 {
		errs = append(errs, "matchmaking.party_skill_boost_percent must be between 0 and 1.0")
	}
	if mm.GreenDivisionMaxAccountAgeDays < 0 || mm.GreenDivisionMaxAccountAgeDays > 3650 {
		errs = append(errs, "matchmaking.green_division_max_account_age_days must be between 0 and 3650")
	}
	if mm.RatingRange < 0 || mm.RatingRange > 100 {
		errs = append(errs, "matchmaking.rating_range must be between 0 and 100")
	}
	if mm.BackfillMinTimeSecs < 0 || mm.BackfillMinTimeSecs > 600 {
		errs = append(errs, "matchmaking.backfill_min_time_secs must be between 0 and 600")
	}
	if mm.ReducingPrecisionIntervalSecs < 0 || mm.ReducingPrecisionIntervalSecs > 600 {
		errs = append(errs, "matchmaking.reducing_precision_interval_secs must be between 0 and 600")
	}
	if mm.ReducingPrecisionMaxCycles < 0 || mm.ReducingPrecisionMaxCycles > 100 {
		errs = append(errs, "matchmaking.reducing_precision_max_cycles must be between 0 and 100")
	}
	if mm.WaitTimePriorityThresholdSecs < 0 || mm.WaitTimePriorityThresholdSecs > 600 {
		errs = append(errs, "matchmaking.wait_time_priority_threshold_secs must be between 0 and 600")
	}
	if mm.RatingRangeExpansionPerMinute < 0 || mm.RatingRangeExpansionPerMinute > 100 {
		errs = append(errs, "matchmaking.rating_range_expansion_per_minute must be between 0 and 100")
	}
	if mm.MaxRatingRangeExpansion < 0 || mm.MaxRatingRangeExpansion > 100 {
		errs = append(errs, "matchmaking.max_rating_range_expansion must be between 0 and 100")
	}
	if mm.AccumulationThresholdSecs < 0 || mm.AccumulationThresholdSecs > 600 {
		errs = append(errs, "matchmaking.accumulation_threshold_secs must be between 0 and 600")
	}
	if mm.AccumulationMaxAgeSecs < 0 || mm.AccumulationMaxAgeSecs > 3600 {
		errs = append(errs, "matchmaking.accumulation_max_age_secs must be between 0 and 3600")
	}
	if mm.AccumulationInitialRadius < 0 || mm.AccumulationInitialRadius > 100 {
		errs = append(errs, "matchmaking.accumulation_initial_radius must be between 0 and 100")
	}
	if mm.AccumulationRadiusExpansionPerCycle < 0 || mm.AccumulationRadiusExpansionPerCycle > 100 {
		errs = append(errs, "matchmaking.accumulation_radius_expansion_per_cycle must be between 0 and 100")
	}
	if mm.AccumulationMaxRadius < 0 || mm.AccumulationMaxRadius > 200 {
		errs = append(errs, "matchmaking.accumulation_max_radius must be between 0 and 200")
	}
	if mm.CrashRecoveryWindowSecs < -1 || mm.CrashRecoveryWindowSecs > 600 {
		errs = append(errs, "matchmaking.crash_recovery_window_secs must be between -1 and 600")
	}

	// Early quit thresholds
	if mm.EarlyQuitTier1Threshold != nil && (*mm.EarlyQuitTier1Threshold < -100 || *mm.EarlyQuitTier1Threshold > 100) {
		errs = append(errs, "matchmaking.early_quit_tier1_threshold must be between -100 and 100")
	}
	if mm.EarlyQuitTier2Threshold != nil && (*mm.EarlyQuitTier2Threshold < -100 || *mm.EarlyQuitTier2Threshold > 100) {
		errs = append(errs, "matchmaking.early_quit_tier2_threshold must be between -100 and 100")
	}
	if mm.EarlyQuitTier1Threshold != nil && mm.EarlyQuitTier2Threshold != nil {
		if *mm.EarlyQuitTier2Threshold <= *mm.EarlyQuitTier1Threshold {
			errs = append(errs, "matchmaking.early_quit_tier2_threshold must be greater than tier1_threshold")
		}
	}

	// Query addons - just length checks
	qa := mm.QueryAddons
	if len(qa.Backfill) > 2000 {
		errs = append(errs, "matchmaking.query_addons.lobby_backfill must be at most 2000 characters")
	}
	if len(qa.LobbyBuilder) > 2000 {
		errs = append(errs, "matchmaking.query_addons.matchmaker_server_allocation must be at most 2000 characters")
	}
	if len(qa.Create) > 2000 {
		errs = append(errs, "matchmaking.query_addons.lobby_create must be at most 2000 characters")
	}
	if len(qa.Allocate) > 2000 {
		errs = append(errs, "matchmaking.query_addons.allocate must be at most 2000 characters")
	}
	if len(qa.Matchmaking) > 2000 {
		errs = append(errs, "matchmaking.query_addons.matchmaking_ticket must be at most 2000 characters")
	}
	if len(qa.RPCAllocate) > 2000 {
		errs = append(errs, "matchmaking.query_addons.rpc_allocate must be at most 2000 characters")
	}

	// Server selection - validate server ratings are reasonable
	for server, rating := range mm.ServerSelection.Ratings {
		if rating < -100 || rating > 100 {
			errs = append(errs, fmt.Sprintf("matchmaking.server_selection.ratings.%s must be between -100 and 100", server))
		}
	}
	for server, delta := range mm.ServerSelection.RTTDelta {
		if delta < -1000 || delta > 1000 {
			errs = append(errs, fmt.Sprintf("matchmaking.server_selection.rtt_delta.%s must be between -1000 and 1000", server))
		}
	}

	// Matchmaker state capture dir
	if len(mm.MatchmakerStateCaptureDir) > 500 {
		errs = append(errs, "matchmaking.matchmaker_state_capture_dir must be at most 500 characters")
	}

	// Remote log filters
	for category, filters := range s.RemoteLogFilters {
		if len(category) > 100 {
			errs = append(errs, fmt.Sprintf("remote_logs_filter: category key %q exceeds 100 characters", category))
		}
		if len(filters) > 100 {
			errs = append(errs, fmt.Sprintf("remote_logs_filter.%s must have at most 100 entries", category))
		}
		for _, f := range filters {
			if len(f) > 200 {
				errs = append(errs, fmt.Sprintf("remote_logs_filter.%s: filter value exceeds 200 characters", category))
				break
			}
		}
	}

	return errs
}

// validStatKeysStr returns a comma-separated list of valid stat field names.
func validStatKeysStr() string {
	keys := make([]string, 0, len(ValidTeamStatFields))
	for k := range ValidTeamStatFields {
		keys = append(keys, k)
	}
	return strings.Join(keys, ", ")
}
