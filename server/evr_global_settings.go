package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

const (
	ServiceSettingsStorageCollection = "Global"
	ServiceSettingStorageKey         = "settings"
)

var serviceSettings = atomic.NewPointer((*ServiceSettingsData)(nil))

func ServiceSettings() *ServiceSettingsData {
	return serviceSettings.Load()
}

func ServiceSettingsUpdate(data *ServiceSettingsData) {
	serviceSettings.Store(data)
}

// RatingDefaults contains the default values for skill ratings
type RatingDefaults struct {
	Z     int     `json:"z"`     // Number of standard deviations for ordinal calculation
	Mu    float64 `json:"mu"`    // Initial mean skill rating
	Sigma float64 `json:"sigma"` // Initial uncertainty (standard deviation)
	Tau   float64 `json:"tau"`   // Dynamics factor - prevents sigma from dropping too low
}

// SkillRatingSettings contains configuration for skill-based matchmaking ratings
type SkillRatingSettings struct {
	Defaults RatingDefaults `json:"defaults"` // Default rating values

	// TeamStatMultipliers are used for team-based rating calculations
	// Keys should match the JSON field names from evr.MatchTypeStats
	// Example: {"Points": 1.0, "Assists": 2.0, "Saves": 3.0, "Passes": 1.0, "ShotsOnGoal": -1.0}
	TeamStatMultipliers map[string]float64 `json:"team_stat_multipliers"`

	// PlayerStatMultipliers are used for individual player rating calculations
	// Uses a simpler formula focused on personal contribution
	// Example: {"Points": 1.0, "Assists": 1.0, "Saves": 2.0}
	PlayerStatMultipliers map[string]float64 `json:"player_stat_multipliers"`

	// WinningTeamBonus is added to winning team players' scores before rating calculation
	WinningTeamBonus float64 `json:"winning_team_bonus"`
}

type ServiceSettingsData struct {
	LinkInstructions                      string                    `json:"link_instructions"`     // Instructions for linking the headset
	DisableLoginMessage                   string                    `json:"disable_login_message"` // Disable the login, and show this message
	ServiceGuildID                        string                    `json:"service_guild_id"`      // Central/Support guild ID
	DisableStatisticsUpdates              bool                      `json:"disable_statistics_updates"`
	DisableRatingsUpdates                 bool                      `json:"disable_ratings_updates"`
	PruneSettings                         PruneSettings             `json:"prune_settings"` // Settings for pruning Discord guilds and Nakama groups
	SkillRating                           SkillRatingSettings       `json:"skill_rating"`   // Skill rating configuration
	Matchmaking                           GlobalMatchmakingSettings `json:"matchmaking"`
	RemoteLogFilters                      map[string][]string       `json:"remote_logs_filter"` //	Ignore remote logs from specific servers
	ReportURL                             string                    `json:"report_url"`         // URL to report issues
	ServiceAuditChannelID                 string                    `json:"service_audit_channel_id"`
	ServiceDebugChannelID                 string                    `json:"service_debug_channel_id"`
	GlobalErrorChannelID                  string                    `json:"service_error_channel_id"`
	CommandLogChannelID                   string                    `json:"service_command_log_channel_id"`
	DiscordBotUserID                      string                    `json:"discord_bot_user_id"`
	KickPlayersWithDisabledAlternates     bool                      `json:"kick_players_with_disabled_alts"` // Kick players with disabled alts
	VRMLEntitlementNotifyChannelID        string                    `json:"vrml_entitlement_notify_channel_id"`
	EnableContinuousGameserverHealthCheck bool                      `json:"enable_continuous_gameserver_health_check"`
	DisplayNameInUseNotifications         bool                      `json:"display_name_in_use_notifications"` // Display name in use notifications
	EnableSessionDebug                    bool                      `json:"enable_session_debug"`
	version                               string
	serviceStatusMessage                  string
	PingServerBeforeJoin                  bool `json:"ping_server_before_join"` // Ping the server before joining to measure latency
}

type PruneSettings struct {
	LeaveOrphanedGuilds  bool `json:"leave_orphan_guilds"` // Prune Discord guilds that do not have a corresponding Nakama group
	DeleteOrphanedGroups bool `json:"leave_orphan_groups"` // Prune Nakama groups that do not have a corresponding Discord guild
	SafetyLimit          int  `json:"safety_limit"`        // The maximum number of orphaned groups or guilds that can be deleted/left before the pruning operation is aborted
}

type GlobalMatchmakingSettings struct {
	MatchmakingTimeoutSecs         int                     `json:"matchmaking_timeout_secs"`            // The matchmaking timeout
	FailsafeTimeoutSecs            int                     `json:"failsafe_timeout_secs"`               // The failsafe timeout
	FallbackTimeoutSecs            int                     `json:"fallback_timeout_secs"`               // The fallback timeout
	DisableArenaBackfill           bool                    `json:"disable_arena_backfill"`              // Disable backfilling for arena matches
	QueryAddons                    QueryAddons             `json:"query_addons"`                        // Additional queries to add to matchmaking queries
	MaxServerRTT                   int                     `json:"max_server_rtt"`                      // The maximum RTT to allow
	EnableSBMM                     bool                    `json:"enable_skill_based_mm"`               // Disable SBMM
	EnableDivisions                bool                    `json:"enable_divisions"`                    // Enable divisions
	GreenDivisionMaxAccountAgeDays int                     `json:"green_division_max_account_age_days"` // The maximum account age to be in the green division
	EnableEarlyQuitPenalty         bool                    `json:"enable_early_quit_penalty"`           // Disable early quit penalty
	EarlyQuitTier1Threshold        *int32                  `json:"early_quit_tier1_threshold"`          // Penalty level threshold for Tier 1 (good standing). Players with penalty <= threshold stay in Tier 1. Nil means not configured.
	EarlyQuitTier2Threshold        *int32                  `json:"early_quit_tier2_threshold"`          // Penalty level threshold for Tier 2 (reserved for future Tier 3+ implementation). Nil means not configured.
	ServerSelection                ServerSelectionSettings `json:"server_selection"`                    // The server selection settings
	EnableOrdinalRange             bool                    `json:"enable_ordinal_range"`                // Enable ordinal range
	RatingRange                    float64                 `json:"rating_range"`                        // The rating range
	MatchmakingTicketsUseMu        bool                    `json:"sbmm_matchmaking_tickets_use_mu"`     // Use Mu instead of Ordinal for matchmaking tickets
	BackfillQueriesUseMu           bool                    `json:"sbmm_backfill_queries_use_mu"`        // Use Mu instead of Ordinal for backfill queries
	MatchmakerUseMu                bool                    `json:"sbmm_matchmaker_use_mu"`              // Use Mu instead of Ordinal for matchmaker player MMR values
	BackfillMinTimeSecs            int                     `json:"backfill_min_time_secs"`              // Minimum time in seconds before backfilling a player to a match
	SBMMMinPlayerCount             int                     `json:"sbmm_min_player_count"`               // Minimum player count to enable skill-based matchmaking
}

type QueryAddons struct {
	Backfill     string `json:"lobby_backfill"`
	LobbyBuilder string `json:"matchmaker_server_allocation"`
	Create       string `json:"lobby_create"`
	Allocate     string `json:"allocate"`
	Matchmaking  string `json:"matchmaking_ticket"`
	RPCAllocate  string `json:"rpc_allocate"`
}

type ServerSelectionSettings struct {
	Ratings     map[string]float64 `json:"ratings"`
	ExcludeList []string           `json:"exclude_list"`
	RTTDelta    map[string]int     `json:"rtt_delta"`
}

func (g *ServiceSettingsData) String() string {
	data, _ := json.Marshal(g)
	return string(data)
}

func (g ServiceSettingsData) UseSkillBasedMatchmaking() bool {
	return g.Matchmaking.EnableSBMM
}

func ServiceSettingsLoad(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) (*ServiceSettingsData, error) {

	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: ServiceSettingsStorageCollection,
			Key:        ServiceSettingStorageKey,
			UserID:     SystemUserID,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read global settings: %w", err)
	}

	data := ServiceSettingsData{}

	// Always write back on first load
	if len(objs) > 0 {
		data.version = objs[0].Version
		if err := json.Unmarshal([]byte(objs[0].Value), &data); err != nil {
			return nil, fmt.Errorf("failed to unmarshal global settings: %w", err)
		}
	}
	FixDefaultServiceSettings(logger, &data)

	// If the object doesn't exist, or this is the first start
	// write the settings to the storage
	if serviceSettings.Load() == nil || data.version == "" {

		_, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection:      ServiceSettingsStorageCollection,
			Key:             ServiceSettingStorageKey,
			UserID:          SystemUserID,
			PermissionRead:  0,
			PermissionWrite: 0,
			Value:           data.String(),
		}})
		if err != nil {
			return nil, fmt.Errorf("failed to write global settings: %w", err)
		}
	}

	serviceSettings.Store(&data)

	return &data, nil
}

// ValidTeamStatFields contains the valid JSON field names from evr.MatchTypeStats that can be used in stat multipliers
var ValidTeamStatFields = map[string]bool{
	"Assists": true, "Blocks": true, "BounceGoals": true, "Catches": true, "Clears": true,
	"Goals": true, "HatTricks": true, "Interceptions": true, "Passes": true, "Points": true,
	"PossessionTime": true, "PunchesReceived": true, "Saves": true, "ShotsOnGoal": true,
	"ShotsOnGoalAgainst": true, "Steals": true, "Stuns": true, "ThreePointGoals": true,
	"TwoPointGoals": true,
}

func FixDefaultServiceSettings(logger runtime.Logger, data *ServiceSettingsData) {

	// Initialize skill rating defaults
	if data.SkillRating.Defaults.Z == 0 {
		data.SkillRating.Defaults.Z = 3
	}
	if data.SkillRating.Defaults.Mu == 0 {
		data.SkillRating.Defaults.Mu = 10.0
	}
	if data.SkillRating.Defaults.Sigma == 0 {
		data.SkillRating.Defaults.Sigma = data.SkillRating.Defaults.Mu / float64(data.SkillRating.Defaults.Z)
	}
	if data.SkillRating.Defaults.Tau == 0 {
		data.SkillRating.Defaults.Tau = 0.3
	}

	// Initialize default team stat multipliers
	if data.SkillRating.TeamStatMultipliers == nil {
		data.SkillRating.TeamStatMultipliers = map[string]float64{
			"Points":      1.0,  // base points
			"Assists":     2.0,  // bonus for assists
			"Saves":       3.0,  // bonus for saves
			"Passes":      1.0,  // bonus for successful passes
			"ShotsOnGoal": -1.0, // penalty for missed shots
		}
	} else {
		// Validate and remove invalid stat multiplier keys
		for key := range data.SkillRating.TeamStatMultipliers {
			if !ValidTeamStatFields[key] {
				if logger != nil {
          logger.WithField("key", key).Warn("Removing invalid team stat multiplier key from configuration")
				}
				delete(data.SkillRating.TeamStatMultipliers, key)
			}
		}
	}

	// Initialize default player stat multipliers
	if data.SkillRating.PlayerStatMultipliers == nil {
		data.SkillRating.PlayerStatMultipliers = map[string]float64{
			"Points":  1.0, // base points
			"Assists": 1.0, // same as points
			"Saves":   2.0, // bonus for saves
		}
	} else {
		// Validate and remove invalid stat multiplier keys
		for key := range data.SkillRating.PlayerStatMultipliers {
			if !ValidTeamStatFields[key] {
				if logger != nil {
					 logger.WithField("key", key).Warn("Removing invalid player stat multiplier key from configuration")
				}
				delete(data.SkillRating.PlayerStatMultipliers, key)
			}
		}
	}

	// Initialize default winning team bonus
	if data.SkillRating.WinningTeamBonus == 0 {
		data.SkillRating.WinningTeamBonus = 4.0
	}

	if data.Matchmaking.ServerSelection.Ratings == nil {
		data.Matchmaking.ServerSelection.Ratings = make(map[string]float64)
	}

	if data.Matchmaking.MatchmakingTimeoutSecs == 0 {
		data.Matchmaking.MatchmakingTimeoutSecs = 360
	}

	if data.Matchmaking.FailsafeTimeoutSecs == 0 {
		data.Matchmaking.FailsafeTimeoutSecs = data.Matchmaking.MatchmakingTimeoutSecs - 60
	}

	if data.Matchmaking.FallbackTimeoutSecs == 0 {
		data.Matchmaking.FallbackTimeoutSecs = data.Matchmaking.FailsafeTimeoutSecs / 2
	}

	if data.Matchmaking.MaxServerRTT == 0 {
		data.Matchmaking.MaxServerRTT = 180
	}

	if data.Matchmaking.SBMMMinPlayerCount == 0 {
		data.Matchmaking.SBMMMinPlayerCount = 24
	}

	// Set default tier thresholds if not configured
	// Tier 1 threshold default: 0 (players with penalty 0 or less are in good standing)
	// Tier 2 threshold default: 1 (reserved for future Tier 3+ implementation)
	if data.Matchmaking.EarlyQuitTier1Threshold == nil {
		tier1Threshold := int32(0)
		data.Matchmaking.EarlyQuitTier1Threshold = &tier1Threshold
	}
	if data.Matchmaking.EarlyQuitTier2Threshold == nil {
		tier2Threshold := int32(1)
		data.Matchmaking.EarlyQuitTier2Threshold = &tier2Threshold
	}

	if data.Matchmaking.ServerSelection.RTTDelta == nil {
		data.Matchmaking.ServerSelection.RTTDelta = make(map[string]int)
	}

	if data.RemoteLogFilters == nil {
		data.RemoteLogFilters = map[string][]string{
			"message": {
				"Podium Interaction",
				"Customization Item Preview",
				"Customization Item Equip",
				"Confirmation Panel Press",
				"server library loaded",
				"r15 net game error message",
				"cst_usage_metrics",
				"purchasing item",
				"Tutorial progress",
			},
			"category": {
				"iap",
				"rich_presence",
				"social",
			},
			"message_type": {
				"OVR_IAP",
			},
		}
	}
}

func ServiceSettingsSave(ctx context.Context, nk runtime.NakamaModule) error {
	data := ServiceSettings()
	data.version = ""

	_, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      ServiceSettingsStorageCollection,
		Key:             ServiceSettingStorageKey,
		UserID:          SystemUserID,
		PermissionRead:  0,
		PermissionWrite: 0,
		Value:           data.String(),
	}})
	if err != nil {
		return fmt.Errorf("failed to write global settings: %w", err)
	}

	return nil
}
