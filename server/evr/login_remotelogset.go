package evr

import (
	"fmt"

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
	UserId   EvrId
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
	return fmt.Sprintf("SNSRemoteLogSetv3 {EvrId=%s,LogLevel=%d, Logs=%s}",
		m.UserId.String(), m.LogLevel, m.Logs)
}

func (m *RemoteLogSet) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.UserId.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.UserId.AccountId) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk0) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk1) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk2) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk3) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LogLevel) },
		func() error { return s.StreamStringTable(&m.Logs) },
	})
}

type RemoteLog string

func (m RemoteLog) GetGameSettings() (*RemoteLogGameSettings, error) {
	var out RemoteLogGameSettings
	err := json.Unmarshal([]byte(m), &out)
	return &out, err
}

func (m RemoteLog) GetSessionStarted() (*RemoteLogSessionStarted, error) {
	var out RemoteLogSessionStarted
	err := json.Unmarshal([]byte(m), &out)
	return &out, err
}

func (m RemoteLog) GetCustomizationMetricsPayload() (*RemoteLogCustomizationMetricsPayload, error) {
	var out RemoteLogCustomizationMetricsPayload
	err := json.Unmarshal([]byte(m), &out)
	return &out, err
}

func (m RemoteLog) GetInteractionEvent() (*RemoteLogInteractionEvent, error) {
	var out RemoteLogInteractionEvent
	err := json.Unmarshal([]byte(m), &out)
	return &out, err
}

func (m RemoteLog) String() string {
	return string(m)
}

// GAME_SETTINGS
type RemoteLogGameSettings struct {
	Message      string       `json:"message"`
	MessageType  string       `json:"message_type"`
	GameSettings GameSettings `json:"game_settings"`
}

type GameSettings struct {
	// WARNING: EchoVR dictates this schema.
	Experimentalthrowing int64   `json:"experimentalthrowing"`
	Smoothrotationspeed  float32 `json:"smoothrotationspeed"`
	HUD                  bool    `json:"HUD"`
	VoIPMode             int64   `json:"voipmode"`
	MatchTagDisplay      bool    `json:"MatchTagDisplay"`
	EnableNetStatusHUD   bool    `json:"EnableNetStatusHUD"`
	EnableGhostAll       bool    `json:"EnableGhostAll"`
	EnablePitch          bool    `json:"EnablePitch"`
	EnablePersonalBubble bool    `json:"EnablePersonalBubble"`
	ReleaseDistance      float32 `json:"releasedistance"`
	EnableYaw            bool    `json:"EnableYaw"`
	EnableNetStatusPause bool    `json:"EnableNetStatusPause"`
	DynamicMusicMode     int64   `json:"dynamicmusicmode"`
	EnableRoll           bool    `json:"EnableRoll"`
	EnableMuteAll        bool    `json:"EnableMuteAll"`
	EnableMaxLoudness    bool    `json:"EnableMaxLoudness"`
	EnableSmoothRotation bool    `json:"EnableSmoothRotation"`
	EnableAPIAccess      bool    `json:"EnableAPIAccess"`
	EnableMuteEnemyTeam  bool    `json:"EnableMuteEnemyTeam"`
	EnableVoIPLoudness   bool    `json:"EnableVoipLoudness"`
	VoIPLoudnessLevel    int64   `json:"voiploudnesslevel"`
	VoIPModEffect        int64   `json:"voipmodeffect"`
	PersonalBubbleMode   float32 `json:"personalbubblemode"`
	Announcer            int64   `json:"announcer"`
	GhostAllMode         int64   `json:"ghostallmode"`
	Music                int64   `json:"music"`
	PersonalBubbleRadius float32 `json:"personalbubbleradius"`
	SFX                  int64   `json:"sfx"`
	VoIP                 int64   `json:"voip"`
	GrabDeadZone         float32 `json:"grabdeadzone"`
	WristAngleOffset     float32 `json:"wristangleoffset"`
	MuteAllMode          int64   `json:"muteallmode"`
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
