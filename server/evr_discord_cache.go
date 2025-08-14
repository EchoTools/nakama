package server

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

// CreateStorableAdapter creates a StorableAdapter for GuildMemberCacheData
func (h *GuildMemberCacheData) CreateStorableAdapter() *StorableAdapter {
	version := "*"
	if h != nil && h.version != "" {
		version = h.version
	}

	return NewStorableAdapter(h, StorageCollectionCache, StorageCollectionCacheDiscord).
		WithVersion(version).
		WithVersionSetter(func(userID, version string) {
			h.userID = userID
			h.version = version
		})
}
