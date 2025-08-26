package evr

import (
	"encoding/binary"
	"fmt"

	"github.com/gofrs/uuid/v5"
)

type BuildNumber int64

const (
	StandaloneBuildNumber BuildNumber = 630783
	PCVRBuild             BuildNumber = 631547
)

var KnownBuilds = []BuildNumber{
	StandaloneBuildNumber,
	PCVRBuild,
}

// LoginRequest represents a message from client to server requesting for a user sign-in.
type LoginRequest struct {
	PreviousSessionID uuid.UUID // This is the old session id, if it had one.
	XPID              XPID
	Payload           LoginProfile
}

func (lr LoginRequest) String() string {
	return fmt.Sprintf("%T(Session=%s, XPID=%s, HMDSerialNumber=%s, HeadsetType=%s)", lr, lr.PreviousSessionID, lr.XPID, lr.Payload.HMDSerialNumber, lr.Payload.SystemInfo.HeadsetType)
}

func (m *LoginRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.PreviousSessionID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.XPID.PlatformCode) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.XPID.AccountId) },
		func() error { return s.StreamJson(&m.Payload, true, NoCompression) },
	})
}

func NewLoginRequest(session uuid.UUID, userId XPID, loginData LoginProfile) (*LoginRequest, error) {
	return &LoginRequest{
		PreviousSessionID: session,
		XPID:              userId,
		Payload:           loginData,
	}, nil
}

func (m *LoginRequest) GetEvrID() XPID {
	return m.XPID
}

type LoginProfile struct {
	AccountId                   uint64      `json:"accountid"`
	DisplayName                 string      `json:"displayname"`
	BypassAuth                  bool        `json:"bypassauth"`
	AccessToken                 string      `json:"access_token"`
	Nonce                       string      `json:"nonce"`
	BuildNumber                 BuildNumber `json:"buildversion"`
	LobbyVersion                uint64      `json:"lobbyversion"`
	AppId                       uint64      `json:"appid"`
	PublisherLock               string      `json:"publisher_lock"`
	HMDSerialNumber             string      `json:"hmdserialnumber"`
	DesiredClientProfileVersion int64       `json:"desiredclientprofileversion"`
	SystemInfo                  SystemInfo  `json:"system_info"`
}

func (ld *LoginProfile) String() string {
	return fmt.Sprintf("%s(account_id=%d, display_name=%s, hmd_serial_number=%s, "+
		")", "LoginData", ld.AccountId, ld.DisplayName, ld.HMDSerialNumber)
}

type GraphicsSettings struct {
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
	HeadsetType        string `json:"headset_type"`
	DriverVersion      string `json:"driver_version"`
	NetworkType        string `json:"network_type"`
	VideoCard          string `json:"video_card"`
	CPUModel           string `json:"cpu"`
	NumPhysicalCores   int64  `json:"num_physical_cores"`
	NumLogicalCores    int64  `json:"num_logical_cores"`
	MemoryTotal        int64  `json:"memory_total"`
	MemoryUsed         int64  `json:"memory_used"`
	DedicatedGPUMemory int64  `json:"dedicated_gpu_memory"`
}
