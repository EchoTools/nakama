package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

var Symbol_SNSLoginRequestv2 Symbol = LoginFailure{}.Symbol()

// LoginRequest represents a message from client to server requesting for a user sign-in.
type LoginRequest struct {
	Session   uuid.UUID    `json:"Session"` // This is the old session id, if it had one.
	EvrId     EvrId        `json:"UserId"`
	LoginData LoginProfile `json:"LoginData"`
}

func (m LoginRequest) Token() string   { return "SNSLogInRequestv2" }
func (m *LoginRequest) Symbol() Symbol { return SymbolOf(m) }

func (lr LoginRequest) String() string {
	return fmt.Sprintf("%s(session=%v, user_id=%s, login_data=%s)",
		lr.Token(), lr.Session, lr.EvrId.String(), lr.LoginData.String())
}

func (m *LoginRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGuid(&m.Session) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.EvrId.AccountId) },
		func() error { return s.StreamJson(&m.LoginData, true, NoCompression) },
	})
}

func NewLoginRequest(session uuid.UUID, userId EvrId, loginData LoginProfile) (*LoginRequest, error) {
	return &LoginRequest{
		Session:   session,
		EvrId:     userId,
		LoginData: loginData,
	}, nil
}

func (m *LoginRequest) GetEvrID() EvrId {
	return m.EvrId
}

type LoginProfile struct {
	// WARNING: EchoVR dictates this schema.
	AccountId                   uint64     `json:"accountid"`
	DisplayName                 string     `json:"displayname"`
	BypassAuth                  bool       `json:"bypassauth"`
	AccessToken                 string     `json:"access_token"`
	Nonce                       string     `json:"nonce"`
	BuildVersion                int64      `json:"buildversion"`
	LobbyVersion                uint64     `json:"lobbyversion"`
	AppId                       uint64     `json:"appid"`
	PublisherLock               string     `json:"publisher_lock"`
	HmdSerialNumber             string     `json:"hmdserialnumber"`
	DesiredClientProfileVersion int64      `json:"desiredclientprofileversion"`
	SystemInfo                  SystemInfo `json:"system_info"`
}

func (ld *LoginProfile) String() string {
	return fmt.Sprintf("%s(account_id=%d, display_name=%s, hmd_serial_number=%s, "+
		")", "LoginData", ld.AccountId, ld.DisplayName, ld.HmdSerialNumber)
}

type GraphicsSettings struct {
	// WARNING: EchoVR dictates this schema.
	TemporalAA                        bool    `json:"temporalaa"`
	Fullscreen                        bool    `json:"fullscreen"`
	Display                           int64   `json:"display"`
	ResolutionScale                   float32 `json:"resolutionscale"`
	AdaptiveResolutionTargetFramerate int64   `json:"adaptiverestargetframerate"`
	AdaptiveResolutionMaxScale        float32 `json:"adaptiveresmaxscale"`
	AdaptiveResolution                bool    `json:"adaptiveresolution"`
	AdaptiveResolutionMinScale        float32 `json:"adaptiveresminscale"`
	AdaptiveResolutionHeadroom        float32 `json:"adaptiveresheadroom"`
	QualityLevel                      int64   `json:"qualitylevel"`
	Quality                           Quality `json:"quality"`
	MSAA                              int64   `json:"msaa"`
	Sharpening                        float32 `json:"sharpening"`
	MultiResolution                   bool    `json:"multires"`
	Gamma                             float32 `json:"gamma"`
	CaptureFOV                        float32 `json:"capturefov"`
}

type Quality struct {
	// WARNING: EchoVR dictates this schema.
	ShadowResolution   int64   `json:"shadowresolution"`
	FX                 int64   `json:"fx"`
	Bloom              bool    `json:"bloom"`
	CascadeResolution  int64   `json:"cascaderesolution"`
	CascadeDistance    float32 `json:"cascadedistance"`
	Textures           int64   `json:"textures"`
	ShadowMSAA         int64   `json:"shadowmsaa"`
	Meshes             int64   `json:"meshes"`
	ShadowFilterScale  float32 `json:"shadowfilterscale"`
	StaggerFarCascades bool    `json:"staggerfarcascades"`
	Volumetrics        bool    `json:"volumetrics"`
	Lights             int64   `json:"lights"`
	Shadows            int64   `json:"shadows"`
	Anims              int64   `json:"anims"`
}

type SystemInfo struct {
	// WARNING: EchoVR dictates this schema.
	HeadsetType        string `json:"headset_type"`
	DriverVersion      string `json:"driver_version"`
	NetworkType        string `json:"network_type"`
	VideoCard          string `json:"video_card"`
	CPU                string `json:"cpu"`
	NumPhysicalCores   int64  `json:"num_physical_cores"`
	NumLogicalCores    int64  `json:"num_logical_cores"`
	MemoryTotal        int64  `json:"memory_total"`
	MemoryUsed         int64  `json:"memory_used"`
	DedicatedGPUMemory int64  `json:"dedicated_gpu_memory"`
}
