package evr

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"reflect"
	"unicode/utf8"

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
	XPID              EvrId
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

func NewLoginRequest(session uuid.UUID, userId EvrId, loginData LoginProfile) (*LoginRequest, error) {
	return &LoginRequest{
		PreviousSessionID: session,
		XPID:              userId,
		Payload:           loginData,
	}, nil
}

func (m *LoginRequest) GetEvrID() EvrId {
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
	// Written by the nevr-runtime client's login, absent from the stock game's. NevrSocial is the
	// social message level the client understands; the server sends a newer social message only to a
	// session that declared its level.
	NevrIdentity *NevrIdentity `json:"nevr_identity,omitempty"`
	NevrSocial   int           `json:"nevr_social,omitempty"`
	// NevrPlugins is what the client's plugin loader did with each plugin its config lists.
	NevrPlugins NevrPlugins `json:"nevr_plugins,omitempty"`
}

// The report is client input that is logged: at most MaxNevrPlugins entries, each text at most
// MaxNevrPluginText bytes.
const (
	MaxNevrPlugins    = 64
	MaxNevrPluginText = 128
)

// NevrPlugins is the plugin report of a login. It decodes whatever the client sent without ever failing the
// login payload it sits in: a value that is not an array is an empty report, an entry of the wrong shape
// is dropped and the others are kept, at most MaxNevrPlugins entries are kept, and each text is cut to
// MaxNevrPluginText bytes at a rune boundary (it is logged).
type NevrPlugins []NevrPlugin

func (l *NevrPlugins) UnmarshalJSON(data []byte) error {
	*l = nil
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil // not an array (or null): an empty report
	}
	for _, entry := range raw {
		if len(*l) >= MaxNevrPlugins {
			break
		}
		var plugin NevrPlugin
		if err := json.Unmarshal(entry, &plugin); err != nil {
			continue
		}
		plugin.Name = cutText(plugin.Name, MaxNevrPluginText)
		plugin.File = cutText(plugin.File, MaxNevrPluginText)
		plugin.Error = cutText(plugin.Error, MaxNevrPluginText)
		plugin.Version = cutText(plugin.Version, MaxNevrPluginText)
		*l = append(*l, plugin)
	}
	return nil
}

// cutText returns s cut to at most max bytes, never in the middle of a rune.
func cutText(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

// NevrPlugin is one entry of the plugin report a nevr-runtime client declares at login: what its loader
// did with one configured plugin. Error is set for an enabled plugin that did not load; Version, API and
// Caps for one that did.
type NevrPlugin struct {
	Name     string `json:"name"`
	File     string `json:"file"`
	Enabled  bool   `json:"enabled"`
	Required bool   `json:"required"`
	Loaded   bool   `json:"loaded"`
	Error    string `json:"error,omitempty"`
	Version  string `json:"ver,omitempty"`
	API      uint32 `json:"api,omitempty"`
	Caps     uint32 `json:"caps,omitempty"`
}

// NevrIdentity is the nevr-runtime build a client declares at login.
type NevrIdentity struct {
	Version   string `json:"version"`
	Commit    string `json:"commit"`
	Build     string `json:"build"`
	BuildType string `json:"build_type"`
}

// IsEmpty reports whether nothing at all was declared: the payload is the zero LoginProfile. (The plugin
// report is a slice, so the struct is no longer comparable with ==.)
func (ld *LoginProfile) IsEmpty() bool {
	return ld == nil || reflect.ValueOf(*ld).IsZero()
}

// SocialLevel is the social message level the client declared, 0 for a client that declared none.
func (ld *LoginProfile) SocialLevel() int {
	if ld == nil {
		return 0
	}
	return ld.NevrSocial
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
