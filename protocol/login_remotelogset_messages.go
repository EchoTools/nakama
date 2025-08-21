package evr

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
)

type RemoteLog interface {
	MessageType() string
	Message() string
}

type SessionIdentifierRemoteLog interface {
	RemoteLog
	SessionUUID() uuid.UUID
}

// Provides the current game clock time.
type GameTimer interface {
	SessionIdentifierRemoteLog
	GameTime() time.Duration
}

type GenericRemoteLog struct {
	MessageData string `json:"message,omitempty"`
	Type        string `json:"message_type,omitempty"`
	XPID        string `json:"userid"`
}

func (m GenericRemoteLog) MessageType() string {
	if m.Type != "" {
		return m.Type
	}
	return m.MessageData
}

func (m GenericRemoteLog) Message() string {
	return m.MessageData
}

func UUIDFromRemoteLogString(s string) uuid.UUID {
	return uuid.FromStringOrNil(strings.Trim(s, "{}"))
}

var (
	ErrUnknownRemoteLogMessageType = errors.New("unknown message type")
	ErrRemoteLogIsNotJSON          = errors.New("remote log is not JSON")
)

func UnmarshalRemoteLog(log []byte) (RemoteLog, error) {

	var m RemoteLog

	m = &GenericRemoteLog{}
	if err := json.Unmarshal(log, &m); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRemoteLogIsNotJSON, err)
	}

	switch m.MessageType() {
	// `message` property
	case "CUSTOMIZATION_METRICS_PAYLOAD":
		m = &RemoteLogCustomizationMetricsPayload{}
	case "STORE_METRICS_PAYLOAD":
		m = &RemoteLogStoreMetricsPayload{}
	case "Server connection failed":
		m = &RemoteLogServerConnectionFailed{}
	case "Disconnected from server due to timeout":
		m = &RemoteLogDisconnectedDueToTimeout{}

	// `message_type` property
	case "VOIP_LOUDNESS":
		m = &RemoteLogVOIPLoudness{}
	case "THREAT_SCANNER":
		m = &RemoteLogThreatScanner{}
	case "PLAYER_DEATH":
		m = &RemoteLogPlayerDeath{}
	case "NET_EXPLOSION":
		m = &RemoteLogNetExplosion{}
	case "ROUND_OVER":
		m = &RemoteLogRoundOver{}
	case "GEAR_STATS_PER_ROUND":
		m = &RemoteLogGearStatsPerRound{}
	case "GOAL":
		m = &RemoteLogGoal{}
	case "POST_MATCH_BATTLE_PASS_STATS":
		m = &RemoteLogPostMatchBattlePassStats{}
	case "POST_MATCH_BATTLE_PASS_UNLOCKS":
		m = &RemoteLogPostMatchBattlePassUnlocks{}
	case "POST_MATCH_BATTLE_PASS_XP":
		m = &RemoteLogPostMatchBattlePassXP{}
	case "POST_MATCH_EARNED_AWARD":
		m = &RemoteLogRepairMatrix{}
	case "POST_MATCH_MATCH_STATS":
		m = &RemoteLogPostMatchMatchStats{}
	case "POST_MATCH_MATCH_TYPE_STATS":
		m = &RemoteLogPostMatchTypeStats{}
	case "POST_MATCH_MATCH_TYPE_UNLOCKS":
		m = &RemoteLogRepairMatrix{}
	case "POST_MATCH_MATCH_TYPE_XP":
		m = &RemoteLogRepairMatrix{}
	case "POST_MATCH_MATCH_TYPE_XP_LEVEL":
		m = &RemoteLogPostMatchMatchTypeXPLevel{}
	case "LOAD_STATS":
		m = &RemoteLogLoadStats{}
	case "REPAIR_MATRIX":
		m = &RemoteLogRepairMatrix{}
	case "SESSION_STARTED":
		m = &RemoteLogSessionStarted{}
	case "USER_DISCONNECT":
		m = &RemoteLogUserDisconnected{}
	case "USER_DISPLAY_NAME_MISMATCH":
		m = &RemoteLogUserDisplayNameMismatch{}
	case "ENERGY_BARRIER":
		m = &RemoteLogRepairMatrix{}
	case "FIND_NEW_LOBBY":
		m = &RemoteLogFindNewLobby{}
	case "GAME_SETTINGS":
		m = &RemoteLogPauseSettings{}
	case "GHOST_ALL":
		m = &RemoteLogGhostAll{}
	case "GHOST_USER":
		m = &RemoteLogGhostUser{}
	case "PERSONAL_BUBBLE":
		m = &RemoteLogPersonalBubble{}
	case "MUTE_ALL":
		m = &RemoteLogInteractionEvent{}
	case "MUTE_USER":
		m = &RemoteLogInteractionEvent{}
	}

	// If the type is not known, return the top-level object.
	if _, ok := m.(*GenericRemoteLog); ok {
		return m, nil
	}

	if err := json.Unmarshal(log, m); err != nil {
		return nil, err
	}

	return m, nil
}

// GAME_SETTINGS
type RemoteLogPauseSettings struct {
	GenericRemoteLog
	Settings GamePauseSettings `json:"game_settings"`
}

type GamePauseSettings struct {
	EnableAPIAccess      bool    `json:"EnableAPIAccess"`
	EnableGhostAll       bool    `json:"EnableGhostAll"`
	EnableMaxLoudness    bool    `json:"EnableMaxLoudness"`
	EnableMuteAll        bool    `json:"EnableMuteAll"`
	EnableMuteEnemyTeam  bool    `json:"EnableMuteEnemyTeam"`
	EnableNetStatusHUD   bool    `json:"EnableNetStatusHUD"`
	EnableNetStatusPause bool    `json:"EnableNetStatusPause"`
	EnablePersonalBubble bool    `json:"EnablePersonalBubble"`
	EnablePersonalSpace  bool    `json:"EnablePersonalSpace"`
	EnablePitch          bool    `json:"EnablePitch"`
	EnableRoll           bool    `json:"EnableRoll"`
	EnableSmoothRotation bool    `json:"EnableSmoothRotation"`
	EnableStreamerMode   bool    `json:"EnableStreamerMode"`
	EnableVoipLoudness   bool    `json:"EnableVoipLoudness"`
	EnableYaw            bool    `json:"EnableYaw"`
	Hud                  bool    `json:"HUD"`
	MatchTagDisplay      bool    `json:"MatchTagDisplay"`
	Announcer            int64   `json:"announcer"`
	Dynamicmusicmode     int64   `json:"dynamicmusicmode"`
	GrabDeadZone         float64 `json:"grabdeadzone"`
	Music                int64   `json:"music"`
	Personalbubblemode   int64   `json:"personalbubblemode"`
	Personalbubbleradius float64 `json:"personalbubbleradius"`
	Personalspacemode    int64   `json:"personalspacemode"`
	ReleaseDistance      float64 `json:"releasedistance"`
	Sfx                  int64   `json:"sfx"`
	Smoothrotationspeed  float64 `json:"smoothrotationspeed"`
	Voip                 int64   `json:"voip"`
	Voiploudnesslevel    float64 `json:"voiploudnesslevel"`
	Voipmode             int64   `json:"voipmode"`
	Voipmodeffect        int64   `json:"voipmodeffect"`
	WristAngleOffset     float64 `json:"wristangleoffset"`
}

// SESSION_STARTED
type RemoteLogSessionStarted struct {
	GenericRemoteLog
	SessionUUIDStr string `json:"[session][uuid]"`
	MatchType      string `json:"match_type"`
}

func (m RemoteLogSessionStarted) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

// CUSTOMIZATION_METRICS_PAYLOAD
type RemoteLogCustomizationMetricsPayload struct {
	GenericRemoteLog
	SessionUUIDStr string `json:"[session][uuid]"`
	PanelID        string `json:"[panel_id]"`
	EventType      string `json:"[event_type]"`
	EventDetail    string `json:"[event_detail]"`
	ItemID         int64  `json:"[item_id]"`
	ItemName       string `json:"[item_name]"`
	UserID         string `json:"[user_id]"`
}

func (m RemoteLogCustomizationMetricsPayload) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

// GetCategory returns the category of the item. The category is what determines the equipment slot.
func (m *RemoteLogCustomizationMetricsPayload) GetCategory() string {

	if m.ItemName == "loadout_number" {
		return "decal"
	}
	if m.ItemName[:4] == "rwd_" {
		// Reward Item.
		s := strings.SplitN(m.ItemName, "_", 3)
		if len(s) != 3 {
			return ""
		}
		return s[1]
	} else {
		// Standard Item.
		s := strings.SplitN(m.ItemName, "_", 2)
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
	GenericRemoteLog
	Ds          string        `json:"ds"`
	PanelName   string        `json:"panel_name"`
	PanelType   string        `json:"panel_type"`
	EventName   string        `json:"event_name"`
	UserID      string        `json:"user_id"`
	EventDetail RLEventDetail `json:"event_detail"`
}

func (m RemoteLogCustomizationMetricsPayload) MessageString() string {
	return m.Message()
}

type RLEventDetail struct {
	ItemID string `json:"item_id"`
}
type RemoteLogUserDisplayNameMismatch struct {
	GenericRemoteLog
	SessionUUIDStr    string `json:"[session][uuid]"`
	ClientDisplayName string `json:"client_display_name"`
	Level             string `json:"level"`
	MatchType         string `json:"match_type"`
	ServerDisplayName string `json:"server_display_name"`
	Userid            string `json:"userid"`
}

func (m RemoteLogUserDisplayNameMismatch) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogFindNewLobby struct {
	GenericRemoteLog
	PlayerDisplayname string `json:"[player][displayname]"`
	PlayerUserid      string `json:"[player][userid]"`
	RoomID            int64  `json:"[room_id]"`
	SocialGroupID     string `json:"[social_group_id]"`
}

type RemoteLogR15NetGameErrorMessage struct {
	ErrorMessage string `json:"error_message"`
}

type RemoteLogPurchasingItem struct {
	GenericRemoteLog
	Category string `json:"category"`
	Price    int64  `json:"price"`
	Sku      string `json:"sku"`
}

type RemoteLogPostMatchMatchTypeXPLevel struct {
	GenericRemoteLog
	CurrentLevel   int64  `json:"CurrentLevel"`
	CurrentXP      int64  `json:"CurrentXP"`
	PreviousLevel  int64  `json:"PreviousLevel"`
	PreviousXP     int64  `json:"PreviousXP"`
	RemainingXP    int64  `json:"RemainingXP"`
	SessionUUIDStr string `json:"[session][uuid]"`
	MatchType      string `json:"match_type"`
	Userid         string `json:"userid"`
}

func (m RemoteLogPostMatchMatchTypeXPLevel) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogPostMatchBattlePassXP struct {
	GenericRemoteLog
	BaseXP                          int64   `json:"BaseXP"`
	BonusXP                         int64   `json:"BonusXP"`
	CombinedXPMultiplier            float64 `json:"CombinedXPMultiplier"`
	DailyFirstGameBonusXP           int64   `json:"DailyFirstGameBonusXP"`
	GlobalXPMultiplier              float64 `json:"GlobalXPMultiplier"`
	IndividualXPMultiplier          float64 `json:"IndividualXPMultiplier"`
	IsPremiumUnlocked               bool    `json:"IsPremiumUnlocked"`
	PartyXPMultiplier               float64 `json:"PartyXPMultiplier"`
	PartyXPMultiplierTimesTeammates float64 `json:"PartyXPMultiplierTimesTeammates"`
	TotalXP                         int64   `json:"TotalXP"`
	WeeklyFirstWinBonusXP           int64   `json:"WeeklyFirstWinBonusXP"`
	SessionUUIDStr                  string  `json:"[session][uuid]"`
	BattlePassStatGroup             string  `json:"battle_pass_stat_group"`
	MatchType                       string  `json:"match_type"`
	Userid                          string  `json:"userid"`
}

func (m RemoteLogPostMatchBattlePassXP) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogPostMatchBattlePassUnlocks struct {
	GenericRemoteLog
	CurrentTier         int64         `json:"CurrentTier"`
	IsPremiumUnlocked   bool          `json:"IsPremiumUnlocked"`
	SessionUUIDStr      string        `json:"[session][uuid]"`
	BattlePassStatGroup string        `json:"battle_pass_stat_group"`
	MatchType           string        `json:"match_type"`
	NewUnlocks          []interface{} `json:"new_unlocks"`
	Userid              string        `json:"userid"`
}

func (m RemoteLogPostMatchBattlePassUnlocks) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogPostMatchBattlePassStats struct {
	GenericRemoteLog
	SessionUUIDStr string         `json:"[session][uuid]"`
	MatchType      string         `json:"match_type"`
	Stats          RemoteLogStats `json:"stats"`
	Userid         string         `json:"userid"`
}

func (m RemoteLogPostMatchBattlePassStats) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogStats struct {
	XPTotal int64 `json:"XPTotal"`
}

type RemoteLogServerConnectionFailed struct {
	GenericRemoteLog
	SessionUUIDStr string  `json:"[session][uuid]"`
	GameState      string  `json:"game_state"`
	ServerAddress  string  `json:"server_address"`
	ServerPing     float64 `json:"server_ping"`
	Userid         string  `json:"userid"`
}

func (m RemoteLogServerConnectionFailed) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogDisconnectedDueToTimeout struct {
	GenericRemoteLog
	SessionUUIDStr        string  `json:"[session][uuid]"`
	GameState             string  `json:"game_state"`
	ServerAddress         string  `json:"server_address"`
	ServerPing            float64 `json:"server_ping"`
	RealPingEstimate      float64 `json:"real_ping_estimate"`
	TimeSinceLastReceived float64 `json:"time_since_last_received"`
	DecodeFailureRate     float64 `json:"decode_failure_rate"`
	DropPercent           float64 `json:"drop_percent"`
}

func (m RemoteLogDisconnectedDueToTimeout) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogStoreMetricsPayload struct {
	GenericRemoteLog
	BundlePrice    bool   `json:"[bundle_price]"`
	EventDetail    string `json:"[event_detail]"`
	EventType      string `json:"[event_type]"`
	IsBundle       bool   `json:"[is_bundle]"`
	IsFeatured     bool   `json:"[is_featured]"`
	ItemID         int64  `json:"[item_id]"`
	ItemName       string `json:"[item_name]"`
	ItemPrice      bool   `json:"[item_price]"`
	PanelID        string `json:"[panel_id]"`
	SessionUUIDStr string `json:"[session][uuid]"`
	StoreSku       string `json:"[store_sku]"`
	StoreSlot      bool   `json:"[store_slot]"`
	UserID         string `json:"[user_id]"`
}

func (m RemoteLogStoreMetricsPayload) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogRepairMatrix struct {
	GenericRemoteLog
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
	SessionUUIDStr         string  `json:"[session][uuid]"`
	TriggerLocationVec3X   float64 `json:"[trigger_location][vec3][x]"`
	TriggerLocationVec3Y   float64 `json:"[trigger_location][vec3][y]"`
	TriggerLocationVec3Z   float64 `json:"[trigger_location][vec3][z]"`
	TriggerLocationXz      string  `json:"[trigger_location][xz]"`
	TriggerLocationYz      string  `json:"[trigger_location][yz]"`
	HealAmount             float64 `json:"heal_amount"`
	NumHealed              int64   `json:"num_healed"`
	SelfOnly               bool    `json:"self_only"`
}

func (m RemoteLogRepairMatrix) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogRepairMatrix) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

type RemoteLogGhostAll struct {
	GenericRemoteLog
	Enabled                bool    `json:"[enabled]"`
	GameType               string  `json:"[game_type]"`
	Map                    string  `json:"[map]"`
	PlayerDisplayname      string  `json:"[player][displayname]"`
	PlayerUserid           string  `json:"[player][userid]"`
	RoomID                 int64   `json:"[room_id]"`
	SocialGroupID          string  `json:"[social_group_id]"`
	OtherPlayerDisplayname *string `json:"[other_player][displayname],omitempty"`
	OtherPlayerUserid      *string `json:"[other_player][userid],omitempty"`
}

type RemoteLogGoal struct {
	GenericRemoteLog
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
	PlayerInfoDisplayName  string  `json:"[player_info][displayname]"`
	PlayerInfoTeamID       int64   `json:"[player_info][teamid]"`
	PlaterInfoXPID         string  `json:"[player_info][userid]"`
	PrevPlayerDisplayname  string  `json:"[prev_player][displayname]"`
	PrevPlayerTeamID       int64   `json:"[prev_player][teamid]"`
	PrevPlayerXPID         string  `json:"[prev_player][userid]"`
	SessionUUIDStr         string  `json:"[session][uuid]"`
	WasHeadbutt            bool    `json:"[was_headbutt]"`
}

func (m RemoteLogGoal) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogGoal) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

func (m RemoteLogGoal) Points() int {
	switch m.GoalType {
	case "SLAM DUNK":
		return 2
	case "BOUNCE SHOT":
		return 2
	case "BUMPER SHOT":
		return 2
	case "HEADBUTT":
		return 2
	case "INSIDE SHOT":
		return 2
	case "LONG BOUNCE SHOT":
		return 3
	case "LONG BUMPER SHOT":
		return 3
	case "LONG HEADBUTT":
		return 3
	case "LONG SHOT":
		return 3
	case "SELF GOAL":
		return 2
	default:
		return 0
	}
}

type RemoteLogLoadStats struct {
	GenericRemoteLog
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
}

type RemoteLogGhostUser struct {
	GenericRemoteLog
	Enabled                bool   `json:"[enabled]"`
	GameType               string `json:"[game_type]"`
	Map                    string `json:"[map]"`
	OtherPlayerDisplayname string `json:"[other_player][displayname]"`
	OtherPlayerUserid      string `json:"[other_player][userid]"`
	PlayerDisplayname      string `json:"[player][displayname]"`
	PlayerUserid           string `json:"[player][userid]"`
	RoomID                 int64  `json:"[room_id]"`
	SocialGroupID          string `json:"[social_group_id]"`
}

type RemoteLogUserDisconnected struct {
	GenericRemoteLog
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
	PlayerXPID             string  `json:"[player_info][userid]"`
	SessionUUIDStr         string  `json:"[session][uuid]"`
}

func (m RemoteLogUserDisconnected) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogUserDisconnected) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

// VOIP LOUDNESS
type RemoteLogVOIPLoudness struct {
	GenericRemoteLog
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
	SessionUUIDStr         string  `json:"[session][uuid]"`
	MaxLoudnessDB          float64 `json:"max_loudness_db"`
	VoiceLoudnessDB        float64 `json:"voice_loudness_db"`
}

func (m RemoteLogVOIPLoudness) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogVOIPLoudness) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

type RemoteLogGearStatsPerRound struct {
	GenericRemoteLog
	SessionUUIDStr           string  `json:"[session][uuid]"`
	GameInfoMatchType        string  `json:"[game_info][match_type]"`
	GameInfoLevel            string  `json:"[game_info][level]"`
	GameInfoIsPayload        bool    `json:"[game_info][is_payload]"`
	GameInfoIsCapturePoint   bool    `json:"[game_info][is_capture_point]"`
	GameInfoIsSocial         bool    `json:"[game_info][is_social]"`
	GameInfoIsArena          bool    `json:"[game_info][is_arena]"`
	GameInfoIsCombat         bool    `json:"[game_info][is_combat]"`
	GameInfoIsPrivate        bool    `json:"[game_info][is_private]"`
	GameInfoGameTime         float64 `json:"[game_info][game_time]"`
	GameInfoRoundNumber      int64   `json:"[game_info][round_number]"`
	GameInfoBlueMatchScore   int64   `json:"[game_info][blue_match_score]"`
	GameInfoOrangeMatchScore int64   `json:"[game_info][orange_match_score]"`
	GameInfoRoundWinningTeam int64   `json:"[game_info][round_winning_team]"`
	PlayerInfoUserid         string  `json:"[player_info][userid]"`
	PlayerInfoDisplayname    string  `json:"[player_info][displayname]"`
	PlayerInfoTeamid         int64   `json:"[player_info][teamid]"`
	GEARAssaultTotalTime     float64 `json:"[GEAR][assault][total_time]"`
	GEARAssaultObjTime0      float64 `json:"[GEAR][assault][obj_time_0]"`
	GEARAssaultObjTime1      float64 `json:"[GEAR][assault][obj_time_1]"`
	GEARAssaultObjTime2      float64 `json:"[GEAR][assault][obj_time_2]"`
	GEARAssaultObjTime3      float64 `json:"[GEAR][assault][obj_time_3]"`
	GEARAssaultKillCount     int64   `json:"[GEAR][assault][kill_count]"`
	GEARBlasterTotalTime     float64 `json:"[GEAR][blaster][total_time]"`
	GEARBlasterObjTime0      float64 `json:"[GEAR][blaster][obj_time_0]"`
	GEARBlasterObjTime1      float64 `json:"[GEAR][blaster][obj_time_1]"`
	GEARBlasterObjTime2      float64 `json:"[GEAR][blaster][obj_time_2]"`
	GEARBlasterObjTime3      float64 `json:"[GEAR][blaster][obj_time_3]"`
	GEARBlasterKillCount     int64   `json:"[GEAR][blaster][kill_count]"`
	GEARDetTotalTime         float64 `json:"[GEAR][det][total_time]"`
	GEARDetObjTime0          float64 `json:"[GEAR][det][obj_time_0]"`
	GEARDetObjTime1          float64 `json:"[GEAR][det][obj_time_1]"`
	GEARDetObjTime2          float64 `json:"[GEAR][det][obj_time_2]"`
	GEARDetObjTime3          float64 `json:"[GEAR][det][obj_time_3]"`
	GEARDetKillCount         int64   `json:"[GEAR][det][kill_count]"`
	GEARStunTotalTime        float64 `json:"[GEAR][stun][total_time]"`
	GEARStunObjTime0         float64 `json:"[GEAR][stun][obj_time_0]"`
	GEARStunObjTime1         float64 `json:"[GEAR][stun][obj_time_1]"`
	GEARStunObjTime2         float64 `json:"[GEAR][stun][obj_time_2]"`
	GEARStunObjTime3         float64 `json:"[GEAR][stun][obj_time_3]"`
	GEARStunKillCount        int64   `json:"[GEAR][stun][kill_count]"`
	GEARArcTotalTime         float64 `json:"[GEAR][arc][total_time]"`
	GEARArcObjTime0          float64 `json:"[GEAR][arc][obj_time_0]"`
	GEARArcObjTime1          float64 `json:"[GEAR][arc][obj_time_1]"`
	GEARArcObjTime2          float64 `json:"[GEAR][arc][obj_time_2]"`
	GEARArcObjTime3          float64 `json:"[GEAR][arc][obj_time_3]"`
	GEARArcKillCount         int64   `json:"[GEAR][arc][kill_count]"`
	GEARBurstTotalTime       float64 `json:"[GEAR][burst][total_time]"`
	GEARBurstObjTime0        float64 `json:"[GEAR][burst][obj_time_0]"`
	GEARBurstObjTime1        float64 `json:"[GEAR][burst][obj_time_1]"`
	GEARBurstObjTime2        float64 `json:"[GEAR][burst][obj_time_2]"`
	GEARBurstObjTime3        float64 `json:"[GEAR][burst][obj_time_3]"`
	GEARBurstKillCount       int64   `json:"[GEAR][burst][kill_count]"`
	GEARHealTotalTime        float64 `json:"[GEAR][heal][total_time]"`
	GEARHealObjTime0         float64 `json:"[GEAR][heal][obj_time_0]"`
	GEARHealObjTime1         float64 `json:"[GEAR][heal][obj_time_1]"`
	GEARHealObjTime2         float64 `json:"[GEAR][heal][obj_time_2]"`
	GEARHealObjTime3         float64 `json:"[GEAR][heal][obj_time_3]"`
	GEARHealKillCount        int64   `json:"[GEAR][heal][kill_count]"`
	GEARSensorTotalTime      float64 `json:"[GEAR][sensor][total_time]"`
	GEARSensorObjTime0       float64 `json:"[GEAR][sensor][obj_time_0]"`
	GEARSensorObjTime1       float64 `json:"[GEAR][sensor][obj_time_1]"`
	GEARSensorObjTime2       float64 `json:"[GEAR][sensor][obj_time_2]"`
	GEARSensorObjTime3       float64 `json:"[GEAR][sensor][obj_time_3]"`
	GEARSensorKillCount      int64   `json:"[GEAR][sensor][kill_count]"`
	GEARShieldTotalTime      float64 `json:"[GEAR][shield][total_time]"`
	GEARShieldObjTime0       float64 `json:"[GEAR][shield][obj_time_0]"`
	GEARShieldObjTime1       float64 `json:"[GEAR][shield][obj_time_1]"`
	GEARShieldObjTime2       float64 `json:"[GEAR][shield][obj_time_2]"`
	GEARShieldObjTime3       float64 `json:"[GEAR][shield][obj_time_3]"`
	GEARShieldKillCount      int64   `json:"[GEAR][shield][kill_count]"`
	GEARWraithTotalTime      float64 `json:"[GEAR][wraith][total_time]"`
	GEARWraithObjTime0       float64 `json:"[GEAR][wraith][obj_time_0]"`
	GEARWraithObjTime1       float64 `json:"[GEAR][wraith][obj_time_1]"`
	GEARWraithObjTime2       float64 `json:"[GEAR][wraith][obj_time_2]"`
	GEARWraithObjTime3       float64 `json:"[GEAR][wraith][obj_time_3]"`
	GEARWraithKillCount      int64   `json:"[GEAR][wraith][kill_count]"`
}

func (m RemoteLogGearStatsPerRound) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogGearStatsPerRound) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

type RemoteLogNetExplosion struct {
	GenericRemoteLog
	SessionUUIDStr         string  `json:"[session][uuid]"`
	GameInfoMatchType      string  `json:"[game_info][match_type]"`
	GameInfoLevel          string  `json:"[game_info][level]"`
	GameInfoIsPayload      bool    `json:"[game_info][is_payload]"`
	GameInfoIsCapturePoint bool    `json:"[game_info][is_capture_point]"`
	GameInfoIsSocial       bool    `json:"[game_info][is_social]"`
	GameInfoIsArena        bool    `json:"[game_info][is_arena]"`
	GameInfoIsCombat       bool    `json:"[game_info][is_combat]"`
	GameInfoIsPrivate      bool    `json:"[game_info][is_private]"`
	GameInfoGameTime       float64 `json:"[game_info][game_time]"`
	PlayerInfoUserid       string  `json:"[player_info][userid]"`
	PlayerInfoDisplayname  string  `json:"[player_info][displayname]"`
	PlayerInfoTeamid       int64   `json:"[player_info][teamid]"`
	ExplosionLocationVec3X float64 `json:"[explosion_location][vec3][x]"`
	ExplosionLocationVec3Y float64 `json:"[explosion_location][vec3][y]"`
	ExplosionLocationVec3Z float64 `json:"[explosion_location][vec3][z]"`
	ExplosionLocationXz    string  `json:"[explosion_location][xz]"`
	ExplosionLocationYz    string  `json:"[explosion_location][yz]"`
	ExplosionType          string  `json:"explosion_type"`
	DamageAmount           float64 `json:"damage_amount"`
}

func (m RemoteLogNetExplosion) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogNetExplosion) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

type RemoteLogPlayerDeath struct {
	GenericRemoteLog
	SessionUUIDStr              string    `json:"[session][uuid]"`
	GameInfoMatchType           string    `json:"[game_info][match_type]"`
	GameInfoLevel               string    `json:"[game_info][level]"`
	GameInfoIsPayload           bool      `json:"[game_info][is_payload]"`
	GameInfoIsCapturePoint      bool      `json:"[game_info][is_capture_point]"`
	GameInfoIsSocial            bool      `json:"[game_info][is_social]"`
	GameInfoIsArena             bool      `json:"[game_info][is_arena]"`
	GameInfoIsCombat            bool      `json:"[game_info][is_combat]"`
	GameInfoIsPrivate           bool      `json:"[game_info][is_private]"`
	GameInfoGameTime            float64   `json:"[game_info][game_time]"`
	GameInfoRoundNumber         int64     `json:"[game_info][round_number]"`
	GameInfoBlueMatchScore      int64     `json:"[game_info][blue_match_score]"`
	GameInfoOrangeMatchScore    int64     `json:"[game_info][orange_match_score]"`
	GameInfoRoundWinningTeam    int64     `json:"[game_info][round_winning_team]"`
	ObjectivePossession         string    `json:"[objective][possession]"`
	ObjectiveBlueProgress       float64   `json:"[objective][blue_progress]"`
	ObjectiveOrangeProgress     float64   `json:"[objective][orange_progress]"`
	PlayerInfoUserid            string    `json:"[player_info][userid]"`
	PlayerInfoDisplayname       string    `json:"[player_info][displayname]"`
	PlayerInfoTeamid            int64     `json:"[player_info][teamid]"`
	DeathLocationVec3X          float64   `json:"[death_location][vec3][x]"`
	DeathLocationVec3Y          float64   `json:"[death_location][vec3][y]"`
	DeathLocationVec3Z          float64   `json:"[death_location][vec3][z]"`
	DeathLocationXz             string    `json:"[death_location][xz]"`
	DeathLocationYz             string    `json:"[death_location][yz]"`
	KillerLocationVec3X         float64   `json:"[killer_location][vec3][x]"`
	KillerLocationVec3Y         float64   `json:"[killer_location][vec3][y]"`
	KillerLocationVec3Z         float64   `json:"[killer_location][vec3][z]"`
	KillerLocationXz            string    `json:"[killer_location][xz]"`
	KillerLocationYz            string    `json:"[killer_location][yz]"`
	KillerDistance              float64   `json:"killer_distance"`
	SelfKill                    bool      `json:"self_kill"`
	KillingBlowUserid           string    `json:"[killing_blow][userid]"`
	KillingBlowDamageType       string    `json:"[killing_blow][damage_type]"`
	KillingBlowDamageAmount     float64   `json:"[killing_blow][damage_amount]"`
	TimeAlive                   float64   `json:"time_alive"`
	StunnedBeforeDeath          bool      `json:"stunned_before_death"`
	StunnedOnDeath              bool      `json:"stunned_on_death"`
	KillParticipantsUserids     []string  `json:"kill_participants_userids"`
	KillParticipantsDmgAmt      []float64 `json:"kill_participants_dmg_amt"`
	KillParticipantsLastDmgType []string  `json:"kill_participants_last_dmg_type"`
}

func (m RemoteLogPlayerDeath) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogPlayerDeath) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

type RemoteLogRoundOver struct {
	GenericRemoteLog
	SessionUUIDStr           string  `json:"[session][uuid]"`
	GameInfoMatchType        string  `json:"[game_info][match_type]"`
	GameInfoLevel            string  `json:"[game_info][level]"`
	GameInfoIsPayload        bool    `json:"[game_info][is_payload]"`
	GameInfoIsCapturePoint   bool    `json:"[game_info][is_capture_point]"`
	GameInfoIsSocial         bool    `json:"[game_info][is_social]"`
	GameInfoIsArena          bool    `json:"[game_info][is_arena]"`
	GameInfoIsCombat         bool    `json:"[game_info][is_combat]"`
	GameInfoIsPrivate        bool    `json:"[game_info][is_private]"`
	GameInfoGameTime         float64 `json:"[game_info][game_time]"`
	GameInfoRoundNumber      int64   `json:"[game_info][round_number]"`
	GameInfoBlueMatchScore   int64   `json:"[game_info][blue_match_score]"`
	GameInfoOrangeMatchScore int64   `json:"[game_info][orange_match_score]"`
	GameInfoRoundWinningTeam int64   `json:"[game_info][round_winning_team]"`
	ObjectivePossession      string  `json:"[objective][possession]"`
	ObjectiveBlueProgress    float64 `json:"[objective][blue_progress]"`
	ObjectiveOrangeProgress  float64 `json:"[objective][orange_progress]"`
}

func (m RemoteLogRoundOver) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogRoundOver) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

type RemoteLogThreatScanner struct {
	GenericRemoteLog
	SessionUUIDStr         string  `json:"[session][uuid]"`
	GameInfoMatchType      string  `json:"[game_info][match_type]"`
	GameInfoLevel          string  `json:"[game_info][level]"`
	GameInfoIsPayload      bool    `json:"[game_info][is_payload]"`
	GameInfoIsCapturePoint bool    `json:"[game_info][is_capture_point]"`
	GameInfoIsSocial       bool    `json:"[game_info][is_social]"`
	GameInfoIsArena        bool    `json:"[game_info][is_arena]"`
	GameInfoIsCombat       bool    `json:"[game_info][is_combat]"`
	GameInfoIsPrivate      bool    `json:"[game_info][is_private]"`
	GameInfoGameTime       float64 `json:"[game_info][game_time]"`
	PlayerInfoUserid       string  `json:"[player_info][userid]"`
	PlayerInfoDisplayname  string  `json:"[player_info][displayname]"`
	PlayerInfoTeamid       int64   `json:"[player_info][teamid]"`
	TriggerLocationVec3X   float64 `json:"[trigger_location][vec3][x]"`
	TriggerLocationVec3Y   float64 `json:"[trigger_location][vec3][y]"`
	TriggerLocationVec3Z   float64 `json:"[trigger_location][vec3][z]"`
	TriggerLocationXz      string  `json:"[trigger_location][xz]"`
	TriggerLocationYz      string  `json:"[trigger_location][yz]"`
}

func (m RemoteLogThreatScanner) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogThreatScanner) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

type RemoteLogUnknownUnlocks struct {
	GenericRemoteLog
	SessionUUIDStr string   `json:"[session][uuid]"`
	MatchType      string   `json:"match_type"`
	UnknownUnlocks []string `json:"unknown_unlocks"`
	Level          string   `json:"level"`
}

func (m RemoteLogUnknownUnlocks) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogCSTUsageMetrics struct {
	GenericRemoteLog
	SessionUUIDStr           string `json:"[session][uuid]"`
	BattlepassOpen           int64  `json:"[battlepass_open]"`
	CstPremiumPurchaseCancel int64  `json:"[cst_premium_purchase_cancel]"`
}

func (m RemoteLogCSTUsageMetrics) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogPersonalBubble struct {
	GenericRemoteLog
	Enabled           bool   `json:"[enabled]"`
	GameType          string `json:"[game_type]"`
	Map               string `json:"[map]"`
	PlayerDisplayname string `json:"[player][displayname]"`
	PlayerXPID        string `json:"[player][userid]"`
	RoomID            uint64 `json:"[room_id]"`
	SocialGroupID     string `json:"[social_group_id]"`
}

type RemoteLogPostMatchMatchStats struct {
	GenericRemoteLog
	SessionUUIDStr string     `json:"[session][uuid]"`
	MatchType      string     `json:"match_type"`
	MatchStats     MatchStats `json:"match_stats"`
}

func (m RemoteLogPostMatchMatchStats) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogPostMatchTypeStats struct {
	GenericRemoteLog
	SessionUUIDStr string         `json:"[session][uuid]"`
	MatchType      string         `json:"match_type"`
	Stats          MatchTypeStats `json:"stats"`
}

func (m RemoteLogPostMatchTypeStats) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogPostMatchTypeStats) IsWinner() bool {
	return m.Stats.ArenaWins > 0
}
