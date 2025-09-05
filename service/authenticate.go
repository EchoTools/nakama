package service

const (
	SystemUserID = "00000000-0000-0000-0000-000000000000"

	AuthorizationCollection      = "Authorization"
	DiscordAccessTokenCollection = "DiscordAccessTokens"
	DiscordAccessTokenKey        = "accessToken"
	ActivePartyGroupIndex        = "Index_PartyGroup"
	CacheStorageCollection       = "Cache"
	IPinfoCacheKey               = "IPinfo"
	CosmeticLoadoutCollection    = "CosmeticLoadouts"
	CosmeticLoadoutKey           = "loadouts"
	VRMLStorageCollection        = "VRML"

	// The Application IDs for the default clients
	NoOvrAppId uint64 = 0x0
	QuestAppId uint64 = 0x7de88f07bd07a
	PcvrAppId  uint64 = 0x4dd2b684a47fa
)
