package evr

import (
	"encoding/json"
	"fmt"
	"time"
)

var (
	NakamaEVRLaunchDay = time.Date(2024, 03, 27, 0, 0, 0, 0, time.UTC)
	// Lone Echo Day is Thursday, August 1, 2537
	// This is the date that the Lone Echo game takes place in.
	LoneEchoDay = time.Date(2537, 8, 1, 8, 0, 0, 0, time.UTC)
)

type GameSettings struct {
	ConfigData  ConfigData `json:"config_data"`  // SeasonPassConfigs is a map that stores configuration data for the game client.
	Env         string     `json:"env"`          // Env represents the environment in which the game client is running.
	IapUnlocked bool       `json:"iap_unlocked"` // IapUnlocked indicates whether in-app purchases are unlocked for the game client.

	RemoteLogErrors       bool `json:"remote_log_errors"`        // send remote logs for errors
	RemoteLogMetrics      bool `json:"remote_log_metrics"`       // send remote logs for metrics
	RemoteLogRichPresence bool `json:"remote_log_rich_presence"` // send remote logs for rich presence
	RemoteLogSocial       bool `json:"remote_log_social"`        // send remote logs for social events
	RemoteLogWarnings     bool `json:"remote_log_warnings"`      // send remote logs for warnings
}

func NewGameSettings(configData ConfigData, env string, iapUnlocked bool, matchmakerQueueMode string, remoteLogErrors bool, remoteLogMetrics bool, remoteLogRichPresence bool, remoteLogSocial bool, remoteLogWarnings bool) *GameSettings {
	return &GameSettings{
		ConfigData:            configData,
		Env:                   env,
		IapUnlocked:           iapUnlocked,
		RemoteLogErrors:       remoteLogErrors,
		RemoteLogMetrics:      remoteLogMetrics,
		RemoteLogRichPresence: remoteLogRichPresence,
		RemoteLogSocial:       remoteLogSocial,
		RemoteLogWarnings:     remoteLogWarnings,
	}
}

func (m *GameSettings) String() string {
	return fmt.Sprintf("%T()", m)
}

func (m *GameSettings) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamJson(&m, false, ZlibCompression) },
	})
}

type ConfigData struct {
	Schedules []SeasonPassSchedule
}

func NewConfigData(schedules ...SeasonPassSchedule) ConfigData {
	return ConfigData{Schedules: schedules}
}

func (m ConfigData) MarshalJSON() ([]byte, error) {
	cd := map[string]struct {
		ID    string `json:"id"`
		Start int64  `json:"starttime"`
		End   int64  `json:"endtime"`
	}{}

	for _, s := range m.Schedules {
		cd[s.ID] = struct {
			ID    string `json:"id"`
			Start int64  `json:"starttime"`
			End   int64  `json:"endtime"`
		}{
			ID:    s.ID,
			Start: s.Start.UTC().Unix(),
			End:   s.End.UTC().Unix(),
		}
	}

	return json.Marshal(cd)
}

func (m *ConfigData) UnmarshalJSON(data []byte) error {
	cd := map[string]struct {
		ID    string `json:"id"`
		Start int64  `json:"starttime"`
		End   int64  `json:"endtime"`
	}{}

	if err := json.Unmarshal(data, &cd); err != nil {
		return err
	}

	for _, v := range cd {
		m.Schedules = append(m.Schedules, SeasonPassSchedule{
			ID:    v.ID,
			Start: time.Unix(v.Start, 0).UTC(),
			End:   time.Unix(v.End, 0).UTC(),
		})
	}

	return nil
}

type SeasonPassSchedule struct {
	ID    string
	Start time.Time
	End   time.Time
}

func NewSeasonPassSchedule(id string, start, end time.Time) SeasonPassSchedule {
	return SeasonPassSchedule{
		ID:    id,
		Start: start,
		End:   end,
	}
}

func NewDefaultGameSettings() *GameSettings {
	return &GameSettings{
		IapUnlocked:           true,
		RemoteLogSocial:       false,
		RemoteLogWarnings:     false,
		RemoteLogErrors:       false,
		RemoteLogRichPresence: false,
		RemoteLogMetrics:      true,
		Env:                   "live",
		ConfigData: NewConfigData(
			NewSeasonPassSchedule("active_battle_pass_season", NakamaEVRLaunchDay, LoneEchoDay),
			NewSeasonPassSchedule("active_store_entry", NakamaEVRLaunchDay, LoneEchoDay),
			NewSeasonPassSchedule("active_store_featured_entry", NakamaEVRLaunchDay, LoneEchoDay),
		),
	}
}
