package service

import "github.com/bwmarrin/discordgo"

const (
	StorageCollectionCache        = "Cache"
	StorageCollectionCacheDiscord = "discord"
)

type GuildMemberCacheData struct {
	Member *discordgo.Member `json:"member"`

	userID  string
	version string
}

func NewGuildMemberCacheData(member *discordgo.Member) *GuildMemberCacheData {
	return &GuildMemberCacheData{
		Member:  member,
		userID:  member.User.ID,
		version: "", // always overwrite existing data
	}
}

func (h *GuildMemberCacheData) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionCache,
		Key:             StorageCollectionCacheDiscord,
		PermissionRead:  0,
		PermissionWrite: 0,
		Version:         h.version,
	}
}

func (h *GuildMemberCacheData) SetStorageMeta(meta StorableMetadata) {
	h.userID = meta.UserID
	h.version = meta.Version
}
