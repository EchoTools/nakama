package server

const (
	SystemUserID = "00000000-0000-0000-0000-000000000000"

	AuthorizationCollection      = "Authorization"
	LinkTicketKey                = "linkTickets"
	DiscordAccessTokenCollection = "DiscordAccessTokens"
	DiscordAccessTokenKey        = "accessToken"
	SuspensionStatusCollection   = "SuspensionStatus"
	ChannelInfoStorageCollection = "ChannelInfo"
	ChannelInfoStorageKey        = "channelInfo"

	HmdSerialIndex            = "Index_HmdSerial"
	GhostedUsersIndex         = "Index_MutedUsers"
	ActiveSocialGroupIndex    = "Index_SocialGroup"
	ActivePartyGroupIndex     = "Index_PartyGroup"
	CacheStorageCollection    = "Cache"
	IPinfoCacheKey            = "IPinfo"
	CosmeticLoadoutCollection = "CosmeticLoadouts"
	CosmeticLoadoutKey        = "loadouts"
	VRMLStorageCollection     = "VRML"

	// The Application ID for Echo VR
	NoOvrAppId uint64 = 0x0
	QuestAppId uint64 = 0x7de88f07bd07a
	PcvrAppId  uint64 = 0x4dd2b684a47fa
)
