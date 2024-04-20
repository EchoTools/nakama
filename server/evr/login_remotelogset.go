package evr

import (
	"fmt"
	"strings"

	"encoding/binary"
	"encoding/json"
)

type LoggingLevel uint64

const (
	Debug   LoggingLevel = 0x1
	Info    LoggingLevel = 0x2
	Warning LoggingLevel = 0x4
	Error   LoggingLevel = 0x8
	Default LoggingLevel = 0xE
	Any     LoggingLevel = 0xF
)

type RemoteLogSet struct {
	EvrID    EvrId
	Unk0     uint64
	Unk1     uint64
	Unk2     uint64
	Unk3     uint64
	LogLevel LoggingLevel
	Logs     []string
}

func (m RemoteLogSet) Token() string {
	return "SNSRemoteLogSetv3"
}

func (m RemoteLogSet) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m RemoteLogSet) String() string {
	return fmt.Sprintf("SNSRemoteLogSetv3 {EvrId=%s,LogLevel=%d, Logs=%d}", m.EvrID.String(), m.LogLevel, len(m.Logs))
}

func (m *RemoteLogSet) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrID.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrID.AccountId) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk0) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk2) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk3) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LogLevel) },
		func() error { return s.StreamStringTable(&m.Logs) },
	})
}

type RemoteLogString string

func (m RemoteLogString) GetGameSettings() (*RemoteLogGameSettings, error) {
	var out RemoteLogGameSettings
	err := json.Unmarshal([]byte(m), &out)
	return &out, err
}

func (m RemoteLogString) GetSessionStarted() (*RemoteLogSessionStarted, error) {
	var out RemoteLogSessionStarted
	err := json.Unmarshal([]byte(m), &out)
	return &out, err
}

func (m RemoteLogString) GetCustomizationMetricsPayload() (*RemoteLogCustomizationMetricsPayload, error) {
	var out RemoteLogCustomizationMetricsPayload
	err := json.Unmarshal([]byte(m), &out)
	return &out, err
}

func (m RemoteLogString) GetInteractionEvent() (*RemoteLogInteractionEvent, error) {
	var out RemoteLogInteractionEvent
	err := json.Unmarshal([]byte(m), &out)
	return &out, err
}

func (m RemoteLogString) String() string {
	return string(m)
}

// GAME_SETTINGS
type RemoteLogGameSettings struct {
	Message      string                     `json:"message"`
	MessageType  string                     `json:"message_type"`
	GameSettings RemoteLogGameSettingsClass `json:"game_settings"`
}

type RemoteLogGameSettingsClass struct {
	EnableAPIAccess      bool  `json:"EnableAPIAccess"`
	EnableGhostAll       bool  `json:"EnableGhostAll"`
	EnableMaxLoudness    bool  `json:"EnableMaxLoudness"`
	EnableMuteAll        bool  `json:"EnableMuteAll"`
	EnableMuteEnemyTeam  bool  `json:"EnableMuteEnemyTeam"`
	EnableNetStatusHUD   bool  `json:"EnableNetStatusHUD"`
	EnableNetStatusPause bool  `json:"EnableNetStatusPause"`
	EnablePersonalBubble bool  `json:"EnablePersonalBubble"`
	EnablePersonalSpace  bool  `json:"EnablePersonalSpace"`
	EnablePitch          bool  `json:"EnablePitch"`
	EnableRoll           bool  `json:"EnableRoll"`
	EnableSmoothRotation bool  `json:"EnableSmoothRotation"`
	EnableStreamerMode   bool  `json:"EnableStreamerMode"`
	EnableVoipLoudness   bool  `json:"EnableVoipLoudness"`
	EnableYaw            bool  `json:"EnableYaw"`
	Hud                  bool  `json:"HUD"`
	MatchTagDisplay      bool  `json:"MatchTagDisplay"`
	Announcer            int64 `json:"announcer"`
	Dynamicmusicmode     int64 `json:"dynamicmusicmode"`
	Grabdeadzone         int64 `json:"grabdeadzone"`
	Music                int64 `json:"music"`
	Personalbubblemode   int64 `json:"personalbubblemode"`
	Personalbubbleradius int64 `json:"personalbubbleradius"`
	Personalspacemode    int64 `json:"personalspacemode"`
	Releasedistance      int64 `json:"releasedistance"`
	Sfx                  int64 `json:"sfx"`
	Smoothrotationspeed  int64 `json:"smoothrotationspeed"`
	Voip                 int64 `json:"voip"`
	Voiploudnesslevel    int64 `json:"voiploudnesslevel"`
	Voipmode             int64 `json:"voipmode"`
	Voipmodeffect        int64 `json:"voipmodeffect"`
	Wristangleoffset     int64 `json:"wristangleoffset"`
}

// SESSION_STARTED
type RemoteLogSessionStarted struct {
	Message     string `json:"message"`
	MessageType string `json:"message_type"`
	SessionUUID string `json:"[session][uuid]"`
	MatchType   string `json:"match_type"`
}

// CUSTOMIZATION_METRICS_PAYLOAD
type RemoteLogCustomizationMetricsPayload struct {
	Message     string `json:"message"`
	SessionUUID string `json:"[session][uuid]"`
	PanelID     string `json:"[panel_id]"`
	EventType   string `json:"[event_type]"`
	EventDetail string `json:"[event_detail]"`
	ItemID      int64  `json:"[item_id]"`
	ItemName    string `json:"[item_name]"`
	UserID      string `json:"[user_id]"`
}

// GetCategory returns the category of the item. The category is what determines the equipment slot.
func (m *RemoteLogCustomizationMetricsPayload) GetCategory() string {
	itemName := m.ItemName
	if itemName[:4] == "rwd_" {
		// Reward Item.
		s := strings.SplitN(itemName, "_", 3)
		if len(s) != 3 {
			return ""
		}
		return s[1]
	} else {
		// Standard Item.
		s := strings.SplitN(itemName, "_", 2)
		if len(s) != 2 {
			return ""
		}
		return s[0]
	}
}

func (m *RemoteLogCustomizationMetricsPayload) GetEquippedCustomization() (category string, name string, err error) {

	if m.ItemName == "" {
		return "", "", fmt.Errorf("item name is empty")
	}
	return m.GetCategory(), m.ItemName, nil
}

// CUSTOMIZATION_ITEM_PREVIEW
// CUSTOMIZATION_ITEM_EQUIP
// PODIUM_INTERACTION
type RemoteLogInteractionEvent struct {
	Message     string        `json:"message"`
	Ds          string        `json:"ds"`
	PanelName   string        `json:"panel_name"`
	PanelType   string        `json:"panel_type"`
	EventName   string        `json:"event_name"`
	UserID      string        `json:"user_id"`
	EventDetail RLEventDetail `json:"event_detail"`
}

type RLEventDetail struct {
	ItemID string `json:"item_id"`
}
type RemoteLogUserDisplayNameMismatch struct {
	SessionUUID       string `json:"[session][uuid]"`
	ClientDisplayName string `json:"client_display_name"`
	Level             string `json:"level"`
	MatchType         string `json:"match_type"`
	Message           string `json:"message"`
	MessageType       string `json:"message_type"`
	ServerDisplayName string `json:"server_display_name"`
	Userid            string `json:"userid"`
}

type RemoteLogFindNewLobby struct {
	PlayerDisplayname string `json:"[player][displayname]"`
	PlayerUserid      string `json:"[player][userid]"`
	RoomID            int64  `json:"[room_id]"`
	SocialGroupID     string `json:"[social_group_id]"`
	Message           string `json:"message"`
	MessageType       string `json:"message_type"`
}

type RemoteLogTopLevel struct {
}

type RemoteLogR15NetGameErrorMessage struct {
	ErrorMessage string `json:"error_message"`
	Message      string `json:"message"`
}

type RemoteLogPurchasingItem struct {
	Category string `json:"category"`
	Message  string `json:"message"`
	Price    int64  `json:"price"`
	Sku      string `json:"sku"`
}

type RemoteLogPostMatchMatchTypeXPLevel struct {
	CurrentLevel  int64  `json:"CurrentLevel"`
	CurrentXP     int64  `json:"CurrentXP"`
	PreviousLevel int64  `json:"PreviousLevel"`
	PreviousXP    int64  `json:"PreviousXP"`
	RemainingXP   int64  `json:"RemainingXP"`
	SessionUUID   string `json:"[session][uuid]"`
	MatchType     string `json:"match_type"`
	Message       string `json:"message"`
	MessageType   string `json:"message_type"`
	Userid        string `json:"userid"`
}

type RemoteLogPostMatchBattlePassXP struct {
	BaseXP                          int64  `json:"BaseXP"`
	BonusXP                         int64  `json:"BonusXP"`
	CombinedXPMultiplier            int64  `json:"CombinedXPMultiplier"`
	DailyFirstGameBonusXP           int64  `json:"DailyFirstGameBonusXP"`
	GlobalXPMultiplier              int64  `json:"GlobalXPMultiplier"`
	IndividualXPMultiplier          int64  `json:"IndividualXPMultiplier"`
	IsPremiumUnlocked               bool   `json:"IsPremiumUnlocked"`
	PartyXPMultiplier               int64  `json:"PartyXPMultiplier"`
	PartyXPMultiplierTimesTeammates int64  `json:"PartyXPMultiplierTimesTeammates"`
	TotalXP                         int64  `json:"TotalXP"`
	WeeklyFirstWinBonusXP           int64  `json:"WeeklyFirstWinBonusXP"`
	SessionUUID                     string `json:"[session][uuid]"`
	BattlePassStatGroup             string `json:"battle_pass_stat_group"`
	MatchType                       string `json:"match_type"`
	Message                         string `json:"message"`
	MessageType                     string `json:"message_type"`
	Userid                          string `json:"userid"`
}

type RemoteLogPostMatchBattlePassUnlocks struct {
	CurrentTier         int64         `json:"CurrentTier"`
	IsPremiumUnlocked   bool          `json:"IsPremiumUnlocked"`
	SessionUUID         string        `json:"[session][uuid]"`
	BattlePassStatGroup string        `json:"battle_pass_stat_group"`
	MatchType           string        `json:"match_type"`
	Message             string        `json:"message"`
	MessageType         string        `json:"message_type"`
	NewUnlocks          []interface{} `json:"new_unlocks"`
	Userid              string        `json:"userid"`
}

type RemoteLogPostMatchBattlePassStats struct {
	SessionUUID string         `json:"[session][uuid]"`
	MatchType   string         `json:"match_type"`
	Message     string         `json:"message"`
	MessageType string         `json:"message_type"`
	Stats       RemoteLogStats `json:"stats"`
	Userid      string         `json:"userid"`
}

type RemoteLogStats struct {
	XPTotal int64 `json:"XPTotal"`
}

type RemoteLogServerConnectionFailed struct {
	SessionUUID   string  `json:"[session][uuid]"`
	GameState     string  `json:"game_state"`
	Message       string  `json:"message"`
	ServerAddress string  `json:"server_address"`
	ServerPing    float64 `json:"server_ping"`
	Userid        string  `json:"userid"`
}

type RemoteLogStoreMetricsPayload struct {
	BundlePrice bool   `json:"[bundle_price]"`
	EventDetail string `json:"[event_detail]"`
	EventType   string `json:"[event_type]"`
	IsBundle    bool   `json:"[is_bundle]"`
	IsFeatured  bool   `json:"[is_featured]"`
	ItemID      int64  `json:"[item_id]"`
	ItemName    string `json:"[item_name]"`
	ItemPrice   bool   `json:"[item_price]"`
	PanelID     string `json:"[panel_id]"`
	SessionUUID string `json:"[session][uuid]"`
	StoreSku    string `json:"[store_sku]"`
	StoreSlot   bool   `json:"[store_slot]"`
	UserID      string `json:"[user_id]"`
	Message     string `json:"message"`
}

type RemoteLogRepairMatrix struct {
	GameInfoGameTime       float64 `json:"[game_info][game_time]"`
	GameInfoIsArena        bool    `json:"[game_info][is_arena]"`
	GameInfoIsCapturePoint bool    `json:"[game_info][is_capture_point]"`
	GameInfoIsCombat       bool    `json:"[game_info][is_combat]"`
	GameInfoIsPayload      bool    `json:"[game_info][is_payload]"`
	GameInfoIsPrivate      bool    `json:"[game_info][is_private]"`
	GameInfoIsSocial       bool    `json:"[game_info][is_social]"`
	GameInfoLevel          string  `json:"[game_info][level]"`
	GameInfoMatchType      string  `json:"[game_info][match_type]"`
	PlayerInfoDisplayname  string  `json:"[player_info][displayname]"`
	PlayerInfoTeamid       int64   `json:"[player_info][teamid]"`
	PlayerInfoUserid       string  `json:"[player_info][userid]"`
	SessionUUID            string  `json:"[session][uuid]"`
	TriggerLocationVec3X   float64 `json:"[trigger_location][vec3][x]"`
	TriggerLocationVec3Y   float64 `json:"[trigger_location][vec3][y]"`
	TriggerLocationVec3Z   float64 `json:"[trigger_location][vec3][z]"`
	TriggerLocationXz      string  `json:"[trigger_location][xz]"`
	TriggerLocationYz      string  `json:"[trigger_location][yz]"`
	HealAmount             int64   `json:"heal_amount"`
	Message                string  `json:"message"`
	MessageType            string  `json:"message_type"`
	NumHealed              int64   `json:"num_healed"`
	SelfOnly               bool    `json:"self_only"`
}

type RemoteLogGhostAll struct {
	Enabled                bool    `json:"[enabled]"`
	GameType               string  `json:"[game_type]"`
	Map                    string  `json:"[map]"`
	PlayerDisplayname      string  `json:"[player][displayname]"`
	PlayerUserid           string  `json:"[player][userid]"`
	RoomID                 int64   `json:"[room_id]"`
	SocialGroupID          string  `json:"[social_group_id]"`
	Message                string  `json:"message"`
	MessageType            string  `json:"message_type"`
	OtherPlayerDisplayname *string `json:"[other_player][displayname],omitempty"`
	OtherPlayerUserid      *string `json:"[other_player][userid],omitempty"`
}

type RemoteLogGoal struct {
	GameInfoGameTime       float64 `json:"[game_info][game_time]"`
	GameInfoIsArena        bool    `json:"[game_info][is_arena]"`
	GameInfoIsCapturePoint bool    `json:"[game_info][is_capture_point]"`
	GameInfoIsCombat       bool    `json:"[game_info][is_combat]"`
	GameInfoIsPayload      bool    `json:"[game_info][is_payload]"`
	GameInfoIsPrivate      bool    `json:"[game_info][is_private]"`
	GameInfoIsSocial       bool    `json:"[game_info][is_social]"`
	GameInfoLevel          string  `json:"[game_info][level]"`
	GameInfoMatchType      string  `json:"[game_info][match_type]"`
	GoalType               string  `json:"[goal_type]"`
	PlayerInfoDisplayname  string  `json:"[player_info][displayname]"`
	PlayerInfoTeamid       int64   `json:"[player_info][teamid]"`
	PlayerInfoUserid       string  `json:"[player_info][userid]"`
	PrevPlayerDisplayname  string  `json:"[prev_player][displayname]"`
	PrevPlayerTeamid       int64   `json:"[prev_player][teamid]"`
	PrevPlayerUserid       string  `json:"[prev_player][userid]"`
	SessionUUID            string  `json:"[session][uuid]"`
	WasHeadbutt            bool    `json:"[was_headbutt]"`
	Message                string  `json:"message"`
	MessageType            string  `json:"message_type"`
}

type RemoteLogLoadStats struct {
	ClientLoadTime        float64 `json:"[client_load_time]"`
	DestinationLevel      string  `json:"[destination][level]"`
	DestinationMatchType  string  `json:"[destination][match_type]"`
	HeadsetType           string  `json:"[headset_type]"`
	LoadTime              float64 `json:"[load_time]"`
	MatchmakingTime       float64 `json:"[matchmaking_time]"`
	MultiplayerLoadTime   float64 `json:"[multiplayer_load_time]"`
	PlayerInfoDisplayname string  `json:"[player_info][displayname]"`
	PlayerInfoUserid      string  `json:"[player_info][userid]"`
	ServerLoadTime        float64 `json:"[server_load_time]"`
	Message               string  `json:"message"`
	MessageType           string  `json:"message_type"`
}

type RemoteLogGhostUser struct {
	Enabled                bool   `json:"[enabled]"`
	GameType               string `json:"[game_type]"`
	Map                    string `json:"[map]"`
	OtherPlayerDisplayname string `json:"[other_player][displayname]"`
	OtherPlayerUserid      string `json:"[other_player][userid]"`
	PlayerDisplayname      string `json:"[player][displayname]"`
	PlayerUserid           string `json:"[player][userid]"`
	RoomID                 int64  `json:"[room_id]"`
	SocialGroupID          string `json:"[social_group_id]"`
	Message                string `json:"message"`
	MessageType            string `json:"message_type"`
}
