package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

type GuildGroupResponse struct {
	Groups []*GuildGroup `json:"guild_groups,omitempty"`
}

// GuildGroupGetRPC returns metadata and group info for a guild group by ID.
func GuildGroupGetRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	params := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string)
	groupIDs := make([]string, 0)
	// Parse multiple IDs from query params
	if idsParam, exists := params["ids"]; exists {
		for _, id := range idsParam {
			for splitID := range strings.SplitSeq(id, ",") {
				trimmedID := strings.TrimSpace(splitID)
				if trimmedID != "" {
					groupIDs = append(groupIDs, trimmedID)
				}
			}
		}
	}
	if len(groupIDs) == 0 {
		return "", runtime.NewError("no group IDs provided", 3) // InvalidArgument
	}
	guildGroups, err := GuildGroupsLoad(ctx, nk, groupIDs)
	if err != nil {
		return "", runtime.NewError(err.Error(), 5) // NotFound or Internal
	}
	resp := GuildGroupResponse{Groups: guildGroups}
	b, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("failed to marshal response", 13)
	}
	return string(b), nil
}

type UserGuildGroupsResponse struct {
	Groups []*GuildGroup `json:"user_guild_groups,omitempty"`
}

func UserGuildGroupListRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	guildGroupMap, err := GuildUserGroupsList(ctx, nk, nil, userID)
	if err != nil {
		return "", runtime.NewError(err.Error(), 5) // NotFound or Internal
	}

	guildGroups := make([]*GuildGroup, 0, len(guildGroupMap))
	for _, gg := range guildGroupMap {
		guildGroups = append(guildGroups, gg)
	}

	resp := UserGuildGroupsResponse{Groups: guildGroups}
	b, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("failed to marshal response", 13)
	}
	return string(b), nil
}

// ServiceMetadataResponse contains public service metadata including the service guild info.
type ServiceMetadataResponse struct {
	ServiceGuildID   string `json:"service_guild_id,omitempty"`
	ServiceGuildName string `json:"service_guild_name,omitempty"`
	ServiceAvatarURL string `json:"service_avatar_url,omitempty"`
	ReportURL        string `json:"report_url,omitempty"`
}

// ServiceMetadataRPC returns public service metadata including the service guild avatar URL.
// This RPC does not require authentication.
func ServiceMetadataRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	settings := ServiceSettings()
	if settings == nil {
		return "", runtime.NewError("service settings not loaded", 13) // Internal
	}

	resp := ServiceMetadataResponse{
		ServiceGuildID: settings.ServiceGuildID,
		ReportURL:      settings.ReportURL,
	}

	// If we have a service guild ID, try to load its group info to get the avatar URL
	if settings.ServiceGuildID != "" {
		// The service guild ID is a Discord guild ID, we need to find the corresponding Nakama group
		// Groups are linked by the guild_id in their metadata
		groups, _, err := nk.GroupsList(ctx, settings.ServiceGuildID, GuildGroupLangTag, nil, nil, 1, "")
		if err == nil && len(groups) > 0 {
			group := groups[0]
			resp.ServiceGuildName = group.Name
			resp.ServiceAvatarURL = group.AvatarUrl
		}
	}

	b, err := json.Marshal(resp)
	if err != nil {
		return "", runtime.NewError("failed to marshal response", 13)
	}
	return string(b), nil
}
