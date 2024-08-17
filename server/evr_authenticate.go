package server

import (
	"encoding/json"
	"regexp"
	"strconv"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

const (
	SystemUserID = "00000000-0000-0000-0000-000000000000"

	LinkTicketCollection         = "LinkTickets"
	LinkTicketIndex              = "Index_" + LinkTicketCollection
	DiscordAccessTokenCollection = "DiscordAccessTokens"
	DiscordAccessTokenKey        = "accessToken"
	SuspensionStatusCollection   = "SuspensionStatus"
	ChannelInfoStorageCollection = "ChannelInfo"
	ChannelInfoStorageKey        = "channelInfo"
	EvrLoginStorageCollection    = "EvrLogins"
	ClientAddrStorageCollection  = "ClientAddrs"
	HmdSerialIndex               = "Index_HmdSerial"
	IpAddressIndex               = "Index_" + EvrLoginStorageCollection
	DisplayNameCollection        = "DisplayNames"
	DisplayNameIndex             = "Index_DisplayName"
	GhostedUsersIndex            = "Index_MutedUsers"
	ActiveSocialGroupIndex       = "Index_SocialGroup"
	ActivePartyGroupIndex        = "Index_PartyGroup"
	CacheStorageCollection       = "Cache"
	IPinfoCacheKey               = "IPinfo"
	CosmeticLoadoutCollection    = "CosmeticLoadouts"
	VRMLStorageCollection        = "VRML"

	// The Application ID for Echo VR
	NoOvrAppId uint64 = 0x0
	QuestAppId uint64 = 0x7de88f07bd07a
	PcvrAppId  uint64 = 0x4dd2b684a47fa
)

var (
	DisplayNameFilterRegex       = regexp.MustCompile(`[^-0-9A-Za-z_\[\] ]`)
	DisplayNameMatchRegex        = regexp.MustCompile(`[A-Za-z]`)
	DisplayNameFilterScoreSuffix = regexp.MustCompile(`\s\(\d+\)\s\[\d+\.\d+%]`)
)

type SessionVars struct {
	AppID           string `json:"app_id"`
	EvrID           string `json:"evr_id"`
	ClientIP        string `json:"client_ip"`
	HeadsetType     string `json:"headset_type"`
	HMDSerialNumber string `json:"hmd_serial_number"`
}

func NewSessionVars(appID uint64, evrID evr.EvrId, clientIP, headsetType, hmdSerialNumber string) *SessionVars {
	return &SessionVars{
		AppID:           strconv.FormatUint(appID, 10),
		EvrID:           evrID.Token(),
		ClientIP:        clientIP,
		HeadsetType:     headsetType,
		HMDSerialNumber: hmdSerialNumber,
	}
}

func (s *SessionVars) Vars() map[string]string {
	var m map[string]string
	b, _ := json.Marshal(s)
	_ = json.Unmarshal(b, &m)
	return m
}

func SessionVarsFromMap(m map[string]string) *SessionVars {
	b, _ := json.Marshal(m)
	var s SessionVars
	_ = json.Unmarshal(b, &s)
	return &s
}

func (s *SessionVars) DeviceID() *DeviceAuth {
	appID, _ := strconv.ParseUint(s.AppID, 10, 64)
	evrID, _ := evr.ParseEvrId(s.EvrID)
	return NewDeviceAuth(appID, *evrID, s.HMDSerialNumber, s.ClientIP)
}
