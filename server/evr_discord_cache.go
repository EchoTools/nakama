package server

import "github.com/bwmarrin/discordgo"

const (
	StorageCollectionCache        = "Cache"
	StorageCollectionCacheDiscord = "discord"
)

var _ = IndexedVersionedStorable(&GuildMemberCacheData{})

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

func (GuildMemberCacheData) StorageMeta() StorageMeta {
	return StorageMeta{
		Collection: StorageCollectionCache,
		Key:        StorageCollectionCacheDiscord,
	}
}

func (GuildMemberCacheData) StorageIndexes() []StorageIndexMeta {
	return nil
}

func (h *GuildMemberCacheData) SetStorageVersion(userID, version string) {
	h.userID = userID
	h.version = version
}
