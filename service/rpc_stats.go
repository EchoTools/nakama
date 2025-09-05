package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/heroiclabs/nakama-common/runtime"
)

type PlayerStatisticsRequest struct {
	UserID    string     `json:"user_id"`
	GroupID   string     `json:"group_id"`
	GuildID   string     `json:"guild_id"`
	DiscordID string     `json:"discord_id"`
	Mode      evr.Symbol `json:"mode"`
}

type PlayerStatisticsResponse struct {
	Stats evr.PlayerStatistics `json:"stats"`
}

func (r *PlayerStatisticsResponse) String() string {
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return ""
	}
	return string(data)
}

func PlayerStatisticsRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	request := &PlayerStatisticsRequest{}
	if err := parseRequest(ctx, payload, request); err != nil {
		return "", err
	}

	switch {
	case request.UserID != "":
	case request.DiscordID != "":
		if userID, _, err := GetUserIDByDiscordID(ctx, db, request.DiscordID); err != nil {
			return "", fmt.Errorf("failed to get user ID by discord ID: %w", err)
		} else {
			request.UserID = userID
		}
	}

	if request.UserID == "" {
		return "", runtime.NewError("user not found", StatusNotFound)
	}

	switch {
	case request.GroupID != "":
	case request.GuildID != "":
		if groupID, err := GetGroupIDByGuildID(ctx, db, request.GuildID); err != nil {
			return "", fmt.Errorf("failed to get group ID by guild ID: %w", err)
		} else {
			request.GroupID = groupID
		}
	}

	if request.GroupID == "" {
		return "", runtime.NewError("guild group not found", StatusNotFound)
	}

	var modes []evr.Symbol

	if request.Mode.IsNil() {
		modes = []evr.Symbol{
			evr.ModeArenaPublic,
			evr.ModeCombatPublic,
			evr.ModeCombatPrivate,
			evr.ModeArenaPrivate,
			evr.ModeSocialPublic,
			evr.ModeSocialPrivate,
		}
	} else {
		modes = []evr.Symbol{request.Mode}
	}

	stats, _, err := PlayerStatisticsGetID(ctx, db, nk, request.UserID, request.GroupID, modes, request.Mode)
	if err != nil {
		return "", err
	}

	response := &PlayerStatisticsResponse{
		Stats: stats,
	}

	return response.String(), nil
}
