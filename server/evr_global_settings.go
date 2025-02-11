package server

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
)

const (
	GlobalSettingsStorageCollection = "Global"
	GlobalSettingsKey               = "settings"
)

var serviceSettings = atomic.NewPointer((*GlobalSettingsData)(nil))

func ServiceSettings() *GlobalSettingsData {
	return serviceSettings.Load()
}

type GlobalSettingsData struct {
	DisableLoginMessage      string                    `json:"disable_login_message"` // Disable the login, and show this message
	ServiceGuildID           string                    `json:"service_guild_id"`      // Central/Support guild ID
	DisableStatisticsUpdates bool                      `json:"disable_statistics_updates"`
	DisableRatingsUpdates    bool                      `json:"disable_ratings_updates"`
	Matchmaking              GlobalMatchmakingSettings `json:"matchmaking"`
	version                  string
	defaultCosmetics         map[string]struct{}
	RemoteLogFilters         map[string][]string `json:"remote_logs_filter"` //	Ignore remote logs from specific servers
	ReportURL                string              `json:"report_url"`         // URL to report issues
	GlobalAuditChannelID     string              `json:"global_audit_channel_id"`
	GlobalErrorChannelID     string              `json:"global_error_channel_id"`
}

type GlobalMatchmakingSettings struct {
	MatchmakingTimeoutSecs int                    `json:"matchmaking_timeout_secs"` // The matchmaking timeout
	FailsafeTimeoutSecs    int                    `json:"failsafe_timeout_secs"`    // The failsafe timeout
	FallbackTimeoutSecs    int                    `json:"fallback_timeout_secs"`    // The fallback timeout
	DisableArenaBackfill   bool                   `json:"disable_arena_backfill"`   // Disable backfilling for arena matches
	QueryAddons            QueryAddons            `json:"query_addons"`             // Additional queries to add to matchmaking queries
	MaxServerRTT           int                    `json:"max_server_rtt"`           // The maximum RTT to allow
	RankPercentile         RankPercentileSettings `json:"rank_percentile"`          // The rank percentile settings
	DisableSBMM            bool                   `json:"disable_skill_based_mm"`   // Disable SBMM
	ServerRatings          ServerRatings          `json:"server_ratings"`           // The server ratings
}

type QueryAddons struct {
	Backfill     string `json:"lobby_backfill"`
	LobbyBuilder string `json:"matchmaker_server_allocation"`
	Create       string `json:"lobby_create"`
	Matchmaking  string `json:"matchmaking_ticket"`
}

type RankPercentileSettings struct {
	ResetSchedule       evr.ResetSchedule                 `json:"reset_schedule"`       // The reset schedule to use for rankings
	ResetScheduleDamper evr.ResetSchedule                 `json:"damping_schedule"`     // The reset schedule to use for rankings
	DampeningFactor     float64                           `json:"damping_factor"`       // The damping factor to use for rank percentile
	Default             float64                           `json:"default"`              // The default rank percentile to use
	MaxDelta            float64                           `json:"player_range"`         // The upper limit percentile range to matchmake with
	DisplayRankInName   bool                              `json:"rank_in_display_name"` // Display the rank in the display name
	LeaderboardWeights  map[evr.Symbol]map[string]float64 `json:"board_weights"`        // The weights to use for ranking boards map[mode][board]weight
}

type ServerRatings struct {
	ByExternalIP map[string]float64 `json:"by_external_ip"`
	ByOperatorID map[string]float64 `json:"by_operator_id"`
}

func GlobalSettings() *GlobalSettingsData {
	return serviceSettings.Load()
}

func (g *GlobalSettingsData) String() string {
	data, _ := json.Marshal(g)
	return string(data)
}

func (g GlobalSettingsData) UseSkillBasedMatchmaking() bool {
	return !g.Matchmaking.DisableSBMM
}

func LoadGlobalSettingsData(ctx context.Context, nk runtime.NakamaModule) (*GlobalSettingsData, error) {

	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: GlobalSettingsStorageCollection,
			Key:        GlobalSettingsKey,
			UserID:     SystemUserID,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to read global settings: %w", err)
	}

	data := GlobalSettingsData{}

	// Always write back on first load
	if len(objs) > 0 {
		data.version = objs[0].Version
		if err := json.Unmarshal([]byte(objs[0].Value), &data); err != nil {
			return nil, fmt.Errorf("failed to unmarshal global settings: %w", err)
		}
	}

	if data.Matchmaking.ServerRatings.ByExternalIP == nil {
		data.Matchmaking.ServerRatings.ByExternalIP = make(map[string]float64)
	}
	if data.Matchmaking.ServerRatings.ByOperatorID == nil {
		data.Matchmaking.ServerRatings.ByOperatorID = make(map[string]float64)
	}
	if data.Matchmaking.RankPercentile.LeaderboardWeights == nil {
		data.Matchmaking.RankPercentile.LeaderboardWeights = make(map[evr.Symbol]map[string]float64)
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
		data.Matchmaking.MaxServerRTT = 150
	}

	if data.Matchmaking.RankPercentile.Default == 0 {
		data.Matchmaking.RankPercentile.Default = 0.5
	}

	if data.Matchmaking.RankPercentile.MaxDelta == 0 {
		data.Matchmaking.RankPercentile.MaxDelta = 0.3
	}

	if data.Matchmaking.RankPercentile.DampeningFactor == 0 {
		data.Matchmaking.RankPercentile.DampeningFactor = 0.5
	}

	if data.Matchmaking.RankPercentile.ResetSchedule == "" {
		data.Matchmaking.RankPercentile.ResetSchedule = "daily"
	}

	if data.Matchmaking.RankPercentile.ResetScheduleDamper == "" {
		data.Matchmaking.RankPercentile.ResetScheduleDamper = "weekly"
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

	// If the object doesn't exist, or this is the first start
	// write the settings to the storage
	if serviceSettings.Load() == nil || data.version == "" {

		_, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection:      GlobalSettingsStorageCollection,
			Key:             GlobalSettingsKey,
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
