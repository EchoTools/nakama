package evr

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
)

type SessionIdentifier interface {
	SessionUUID() uuid.UUID
}

// Provides the current game clock time.
type GameTimer interface {
	SessionUUID() uuid.UUID
	GameTime() time.Duration
}

func UUIDFromRemoteLogString(s string) uuid.UUID {
	return uuid.FromStringOrNil(strings.Trim(s, "{}"))
}

var (
	ErrUnknownRemoteLogMessageType = errors.New("unknown message type")
)

func RemoteLogMessageFromMessage(strMap map[string]interface{}, data []byte) (any, error) {

	var m any

	if message, ok := strMap["message"].(string); ok {
		switch message {
		case "CUSTOMIZATION_METRICS_PAYLOAD":
			m = &RemoteLogCustomizationMetricsPayload{}
		case "STORE_METRICS_PAYLOAD":
			m = &RemoteLogStoreMetricsPayload{}
		case "Server connection failed":
			m = &RemoteLogServerConnectionFailed{}
		case "Disconnected from server due to timeout":
			m = &RemoteLogDisconnectedDueToTimeout{}
		}
	}

	if typ, ok := strMap["message_type"].(string); ok {

		switch typ {
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
			m = &RemoteLogRepairMatrix{}
		case "POST_MATCH_MATCH_TYPE_STATS":
			m = &RemoteLogRepairMatrix{}
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
			m = &RemoteLogGameSettings{}
		case "GHOST_ALL":
			m = &RemoteLogGhostAll{}
		case "GHOST_USER":
			m = &RemoteLogGhostUser{}
		case "PERSONAL_BUBBLE":
			m = &RemoteLogRepairMatrix{}
		case "MUTE_ALL":
			m = &RemoteLogInteractionEvent{}
		case "MUTE_USER":
			m = &RemoteLogInteractionEvent{}
		}
	}

	if m == nil {
		return nil, ErrUnknownRemoteLogMessageType
	}

	if err := json.Unmarshal(data, m); err != nil {
		return nil, err
	}

	return m, nil
}

// GAME_SETTINGS
type RemoteLogGameSettings struct {
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
	Grabdeadzone         float64 `json:"grabdeadzone"`
	Music                int64   `json:"music"`
	Personalbubblemode   int64   `json:"personalbubblemode"`
	Personalbubbleradius float64 `json:"personalbubbleradius"`
	Personalspacemode    int64   `json:"personalspacemode"`
	Releasedistance      float64 `json:"releasedistance"`
	Sfx                  int64   `json:"sfx"`
	Smoothrotationspeed  float64 `json:"smoothrotationspeed"`
	Voip                 int64   `json:"voip"`
	Voiploudnesslevel    float64 `json:"voiploudnesslevel"`
	Voipmode             int64   `json:"voipmode"`
	Voipmodeffect        int64   `json:"voipmodeffect"`
	Wristangleoffset     float64 `json:"wristangleoffset"`
}

// SESSION_STARTED
type RemoteLogSessionStarted struct {
	Message        string `json:"message"`
	MessageType    string `json:"message_type"`
	SessionUUIDStr string `json:"[session][uuid]"`
	MatchType      string `json:"match_type"`
}

func (m RemoteLogSessionStarted) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

// CUSTOMIZATION_METRICS_PAYLOAD
type RemoteLogCustomizationMetricsPayload struct {
	Message        string `json:"message"`
	SessionUUIDStr string `json:"[session][uuid]"`
	PanelID        string `json:"[panel_id]"`
	EventType      string `json:"[event_type]"`
	EventDetail    string `json:"[event_detail]"`
	ItemID         int64  `json:"[item_id]"`
	ItemName       string `json:"[item_name]"`
	UserID         string `json:"[user_id]"`
}

func (m RemoteLogCustomizationMetricsPayload) MessageString() string {
	return m.Message
}

func (m RemoteLogCustomizationMetricsPayload) MessageType() string {
	return "customization_metrics_payload"
}

func (m RemoteLogCustomizationMetricsPayload) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
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
	SessionUUIDStr    string `json:"[session][uuid]"`
	ClientDisplayName string `json:"client_display_name"`
	Level             string `json:"level"`
	MatchType         string `json:"match_type"`
	Message           string `json:"message"`
	MessageType       string `json:"message_type"`
	ServerDisplayName string `json:"server_display_name"`
	Userid            string `json:"userid"`
}

func (m RemoteLogUserDisplayNameMismatch) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
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
	CurrentLevel   int64  `json:"CurrentLevel"`
	CurrentXP      int64  `json:"CurrentXP"`
	PreviousLevel  int64  `json:"PreviousLevel"`
	PreviousXP     int64  `json:"PreviousXP"`
	RemainingXP    int64  `json:"RemainingXP"`
	SessionUUIDStr string `json:"[session][uuid]"`
	MatchType      string `json:"match_type"`
	Message        string `json:"message"`
	MessageType    string `json:"message_type"`
	Userid         string `json:"userid"`
}

func (m RemoteLogPostMatchMatchTypeXPLevel) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogPostMatchBattlePassXP struct {
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
	Message                         string  `json:"message"`
	MessageType                     string  `json:"message_type"`
	Userid                          string  `json:"userid"`
}

func (m RemoteLogPostMatchBattlePassXP) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogPostMatchBattlePassUnlocks struct {
	CurrentTier         int64         `json:"CurrentTier"`
	IsPremiumUnlocked   bool          `json:"IsPremiumUnlocked"`
	SessionUUIDStr      string        `json:"[session][uuid]"`
	BattlePassStatGroup string        `json:"battle_pass_stat_group"`
	MatchType           string        `json:"match_type"`
	Message             string        `json:"message"`
	MessageType         string        `json:"message_type"`
	NewUnlocks          []interface{} `json:"new_unlocks"`
	Userid              string        `json:"userid"`
}

func (m RemoteLogPostMatchBattlePassUnlocks) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogPostMatchBattlePassStats struct {
	SessionUUIDStr string         `json:"[session][uuid]"`
	MatchType      string         `json:"match_type"`
	Message        string         `json:"message"`
	MessageType    string         `json:"message_type"`
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
	SessionUUIDStr string  `json:"[session][uuid]"`
	GameState      string  `json:"game_state"`
	Message        string  `json:"message"`
	ServerAddress  string  `json:"server_address"`
	ServerPing     float64 `json:"server_ping"`
	Userid         string  `json:"userid"`
}

func (m RemoteLogServerConnectionFailed) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogDisconnectedDueToTimeout struct {
	Message               string  `json:"message"`
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
	BundlePrice    bool   `json:"[bundle_price]"`
	EventDetail    string `json:"[event_detail]"`
	EventType      string `json:"[event_type]"`
	IsBundle       bool   `json:"[is_bundle]"`
	IsFeatured     bool   `json:"[is_featured]"`
	ItemID         int64  `json:"[item_id]"`
	ItemName       string `json:"[item_name]"`
	ItemPrice      bool   `json:"[item_price]"`
	Message        string `json:"message"`
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
	HealAmount             int64   `json:"heal_amount"`
	Message                string  `json:"message"`
	MessageType            string  `json:"message_type"`
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
	PlayerInfoDisplayName  string  `json:"[player_info][displayname]"`
	PlayerInfoTeamID       int64   `json:"[player_info][teamid]"`
	PlayerInfoEvrID        string  `json:"[player_info][userid]"`
	PrevPlayerDisplayname  string  `json:"[prev_player][displayname]"`
	PrevPlayerTeamID       int64   `json:"[prev_player][teamid]"`
	PrevPlayerEvrID        string  `json:"[prev_player][userid]"`
	SessionUUIDStr         string  `json:"[session][uuid]"`
	WasHeadbutt            bool    `json:"[was_headbutt]"`
	Message                string  `json:"message"`
	MessageType            string  `json:"message_type"`
}

func (m RemoteLogGoal) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogGoal) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
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
type RemoteLogUserDisconnected struct {
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
	PlayerEvrID            string  `json:"[player_info][userid]"`
	SessionUUIDStr         string  `json:"[session][uuid]"`
	Message                string  `json:"message"`
	MessageType            string  `json:"message_type"`
}

func (m RemoteLogUserDisconnected) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogUserDisconnected) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

// VOIP LOUDNESS
type RemoteLogVOIPLoudness struct {
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
	Message                string  `json:"message"`
	MessageType            string  `json:"message_type"`
	VoiceLoudnessDB        float64 `json:"voice_loudness_db"`
}

func (m RemoteLogVOIPLoudness) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogVOIPLoudness) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

type RemoteLogGearStatsPerRound struct {
	Message                  string  `json:"message"`
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
	MessageType              string  `json:"message_type"`
	PlayerInfoUserid         string  `json:"[player_info][userid]"`
	PlayerInfoDisplayname    string  `json:"[player_info][displayname]"`
	PlayerInfoTeamid         int64   `json:"[player_info][teamid]"`
	GEARAssaultTotalTime     float64 `json:"[GEAR][assault][total_time]"`
	GEARAssaultObjTime0      float64 `json:"[GEAR][assault][obj_time_0]"`
	GEARAssaultObjTime1      int64   `json:"[GEAR][assault][obj_time_1]"`
	GEARAssaultObjTime2      int64   `json:"[GEAR][assault][obj_time_2]"`
	GEARAssaultObjTime3      int64   `json:"[GEAR][assault][obj_time_3]"`
	GEARAssaultKillCount     int64   `json:"[GEAR][assault][kill_count]"`
	GEARBlasterTotalTime     float64 `json:"[GEAR][blaster][total_time]"`
	GEARBlasterObjTime0      float64 `json:"[GEAR][blaster][obj_time_0]"`
	GEARBlasterObjTime1      int64   `json:"[GEAR][blaster][obj_time_1]"`
	GEARBlasterObjTime2      int64   `json:"[GEAR][blaster][obj_time_2]"`
	GEARBlasterObjTime3      int64   `json:"[GEAR][blaster][obj_time_3]"`
	GEARBlasterKillCount     int64   `json:"[GEAR][blaster][kill_count]"`
	GEARDetTotalTime         float64 `json:"[GEAR][det][total_time]"`
	GEARDetObjTime0          float64 `json:"[GEAR][det][obj_time_0]"`
	GEARDetObjTime1          int64   `json:"[GEAR][det][obj_time_1]"`
	GEARDetObjTime2          int64   `json:"[GEAR][det][obj_time_2]"`
	GEARDetObjTime3          int64   `json:"[GEAR][det][obj_time_3]"`
	GEARDetKillCount         int64   `json:"[GEAR][det][kill_count]"`
	GEARStunTotalTime        float64 `json:"[GEAR][stun][total_time]"`
	GEARStunObjTime0         float64 `json:"[GEAR][stun][obj_time_0]"`
	GEARStunObjTime1         int64   `json:"[GEAR][stun][obj_time_1]"`
	GEARStunObjTime2         int64   `json:"[GEAR][stun][obj_time_2]"`
	GEARStunObjTime3         int64   `json:"[GEAR][stun][obj_time_3]"`
	GEARStunKillCount        int64   `json:"[GEAR][stun][kill_count]"`
	GEARArcTotalTime         float64 `json:"[GEAR][arc][total_time]"`
	GEARArcObjTime0          float64 `json:"[GEAR][arc][obj_time_0]"`
	GEARArcObjTime1          int64   `json:"[GEAR][arc][obj_time_1]"`
	GEARArcObjTime2          int64   `json:"[GEAR][arc][obj_time_2]"`
	GEARArcObjTime3          int64   `json:"[GEAR][arc][obj_time_3]"`
	GEARArcKillCount         int64   `json:"[GEAR][arc][kill_count]"`
	GEARBurstTotalTime       float64 `json:"[GEAR][burst][total_time]"`
	GEARBurstObjTime0        float64 `json:"[GEAR][burst][obj_time_0]"`
	GEARBurstObjTime1        int64   `json:"[GEAR][burst][obj_time_1]"`
	GEARBurstObjTime2        int64   `json:"[GEAR][burst][obj_time_2]"`
	GEARBurstObjTime3        int64   `json:"[GEAR][burst][obj_time_3]"`
	GEARBurstKillCount       int64   `json:"[GEAR][burst][kill_count]"`
	GEARHealTotalTime        float64 `json:"[GEAR][heal][total_time]"`
	GEARHealObjTime0         float64 `json:"[GEAR][heal][obj_time_0]"`
	GEARHealObjTime1         int64   `json:"[GEAR][heal][obj_time_1]"`
	GEARHealObjTime2         int64   `json:"[GEAR][heal][obj_time_2]"`
	GEARHealObjTime3         int64   `json:"[GEAR][heal][obj_time_3]"`
	GEARHealKillCount        int64   `json:"[GEAR][heal][kill_count]"`
	GEARSensorTotalTime      float64 `json:"[GEAR][sensor][total_time]"`
	GEARSensorObjTime0       float64 `json:"[GEAR][sensor][obj_time_0]"`
	GEARSensorObjTime1       int64   `json:"[GEAR][sensor][obj_time_1]"`
	GEARSensorObjTime2       int64   `json:"[GEAR][sensor][obj_time_2]"`
	GEARSensorObjTime3       int64   `json:"[GEAR][sensor][obj_time_3]"`
	GEARSensorKillCount      int64   `json:"[GEAR][sensor][kill_count]"`
	GEARShieldTotalTime      float64 `json:"[GEAR][shield][total_time]"`
	GEARShieldObjTime0       float64 `json:"[GEAR][shield][obj_time_0]"`
	GEARShieldObjTime1       int64   `json:"[GEAR][shield][obj_time_1]"`
	GEARShieldObjTime2       int64   `json:"[GEAR][shield][obj_time_2]"`
	GEARShieldObjTime3       int64   `json:"[GEAR][shield][obj_time_3]"`
	GEARShieldKillCount      int64   `json:"[GEAR][shield][kill_count]"`
	GEARWraithTotalTime      float64 `json:"[GEAR][wraith][total_time]"`
	GEARWraithObjTime0       float64 `json:"[GEAR][wraith][obj_time_0]"`
	GEARWraithObjTime1       int64   `json:"[GEAR][wraith][obj_time_1]"`
	GEARWraithObjTime2       int64   `json:"[GEAR][wraith][obj_time_2]"`
	GEARWraithObjTime3       int64   `json:"[GEAR][wraith][obj_time_3]"`
	GEARWraithKillCount      int64   `json:"[GEAR][wraith][kill_count]"`
}

func (m RemoteLogGearStatsPerRound) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogGearStatsPerRound) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

type RemoteLogNetExplosion struct {
	Message                string  `json:"message"`
	MessageType            string  `json:"message_type"`
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
	DamageAmount           int64   `json:"damage_amount"`
}

func (m RemoteLogNetExplosion) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogNetExplosion) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

type RemoteLogPlayerDeath struct {
	Message                     string    `json:"message"`
	MessageType                 string    `json:"message_type"`
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
	KillingBlowDamageAmount     int64     `json:"[killing_blow][damage_amount]"`
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
	Message                  string  `json:"message"`
	MessageType              string  `json:"message_type"`
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
	ObjectiveBlueProgress    int64   `json:"[objective][blue_progress]"`
	ObjectiveOrangeProgress  float64 `json:"[objective][orange_progress]"`
}

func (m RemoteLogRoundOver) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

func (m RemoteLogRoundOver) GameTime() time.Duration {
	return time.Duration(m.GameInfoGameTime) * time.Second
}

type RemoteLogThreatScanner struct {
	Message                string  `json:"message"`
	MessageType            string  `json:"message_type"`
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
	Message        string   `json:"message"`
	Userid         string   `json:"userid"`
	SessionUUIDStr string   `json:"[session][uuid]"`
	MatchType      string   `json:"match_type"`
	UnknownUnlocks []string `json:"unknown_unlocks"`
	Level          string   `json:"level"`
}

func (m RemoteLogUnknownUnlocks) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}

type RemoteLogCSTUsageMetrics struct {
	Message                  string `json:"message"`
	SessionUUIDStr           string `json:"[session][uuid]"`
	BattlepassOpen           int64  `json:"[battlepass_open]"`
	CstPremiumPurchaseCancel int64  `json:"[cst_premium_purchase_cancel]"`
}

func (m RemoteLogCSTUsageMetrics) SessionUUID() uuid.UUID {
	return UUIDFromRemoteLogString(m.SessionUUIDStr)
}
