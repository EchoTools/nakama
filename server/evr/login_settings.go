package evr

type LoginSettings struct {
	LoginSettings EchoClientSettings `json:"Resource"`
}

func (m LoginSettings) Token() string {
	return "SNSLoginSettings"
}

func (m *LoginSettings) Symbol() Symbol {
	return SymbolOf(m)
}

func (m LoginSettings) String() string {
	return "SNSLoginSettings{...}"
}

func NewSNSLoginSettings(settings EchoClientSettings) *LoginSettings {
	return &LoginSettings{
		LoginSettings: settings,
	}
}

func (m *LoginSettings) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamJson(&m.LoginSettings, false, ZlibCompression) },
	})
}

// Represents the settings for an EchoVR client.
// This is the data that is sent to the client right after it's been authenticated.
type EchoClientSettings struct {
	// WARNING: EchoVR dictates this schema.
	ConfigData          ConfigData `json:"config_data"`           // ConfigData is a map that stores configuration data for the EchoVR client.
	Env                 string     `json:"env"`                   // Env represents the environment in which the EchoVR client is running.
	IapUnlocked         bool       `json:"iap_unlocked"`          // IapUnlocked indicates whether in-app purchases are unlocked for the EchoVR client.
	MatchmakerQueueMode string     `json:"matchmaker_queue_mode"` // MatchmakerQueueMode specifies the queue mode for the EchoVR client's matchmaker.

	RemoteLogErrors       bool `json:"remote_log_errors"`        // send remote logs for errors
	RemoteLogMetrics      bool `json:"remote_log_metrics"`       // send remote logs for metrics
	RemoteLogRichPresence bool `json:"remote_log_rich_presence"` // send remote logs for rich presence
	RemoteLogSocial       bool `json:"remote_log_social"`        // send remote logs for social events
	RemoteLogWarnings     bool `json:"remote_log_warnings"`      // send remote logs for warnings
}
type ConfigData struct {
	ActiveBattlePassSeason   Active `json:"active_battle_pass_season"`
	ActiveStoreEntry         Active `json:"active_store_entry"`
	ActiveStoreFeaturedEntry Active `json:"active_store_featured_entry"`
}
type Active struct {
	ID        string `json:"id"`
	Starttime int64  `json:"starttime"`
	Endtime   int64  `json:"endtime"`
}

const (
	LoneEchoDay = 17911198800
)

var (
	DefaultGameSettingsSettings = EchoClientSettings{
		IapUnlocked:           true,
		RemoteLogSocial:       false,
		RemoteLogWarnings:     false,
		RemoteLogErrors:       false,
		RemoteLogRichPresence: false,
		RemoteLogMetrics:      true,
		Env:                   "live",
		MatchmakerQueueMode:   "disabled",
		ConfigData: ConfigData{
			ActiveBattlePassSeason: Active{
				ID:        "active_battle_pass_season",
				Starttime: 0,
				Endtime:   LoneEchoDay,
			},
			ActiveStoreEntry: Active{
				ID:        "active_store_entry",
				Starttime: 0,
				Endtime:   LoneEchoDay,
			},
			ActiveStoreFeaturedEntry: Active{
				ID:        "active_store_featured_entry",
				Starttime: 0,
				Endtime:   LoneEchoDay,
			},
		},
	}
)
