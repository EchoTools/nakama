package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

type UserLeaderboardRecordsRequest struct {
	UserID    string `json:"user_id"`
	DiscordID string `json:"discord_id"`
	GuildID   string `json:"guild_id"`
	GroupID   string `json:"group_id"`
}

func UserLeaderboardRecordsRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	request := &UserLeaderboardRecordsRequest{}
	if err := parseRequest(ctx, payload, request); err != nil {
		return "", err
	}

	var err error
	if request.DiscordID != "" {
		if request.UserID, err = GetUserIDByDiscordID(ctx, db, request.DiscordID); err != nil {
			return "", fmt.Errorf("failed to get user ID by discord ID: %w", err)
		}
	}

	if request.UserID == "" {
		return "", runtime.NewError("No user ID specified", StatusInvalidArgument)
	}

	if request.GuildID != "" {
		if request.GroupID, err = GetGroupIDByGuildID(ctx, db, request.GuildID); err != nil {
			return "", fmt.Errorf("failed to get group ID by discord ID: %w", err)
		}
	}

	if request.GroupID == "" {
		return "", runtime.NewError("No group ID specified", StatusInvalidArgument)
	}

	records, err := retrievePlayerLeaderboardRecords(ctx, db, request.UserID, request.GroupID)
	if err != nil {
		return "", err
	}

	data, err := json.MarshalIndent(records, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal records: %w", err)
	}

	return string(data), nil
}

func retrievePlayerLeaderboardRecords(ctx context.Context, db *sql.DB, userID string, groupID string) (map[evr.Symbol]map[string]map[evr.ResetSchedule]float64, error) {
	query := `
		SELECT 
			leaderboard_id,
			score,
			subscore
		FROM leaderboard_record
		WHERE owner_id = $1
		AND (expiry_time > NOW() OR expiry_time = '1970-01-01 00:00:00+00') -- Include "alltime" records
		AND leaderboard_id LIKE $2`

	rows, err := db.QueryContext(ctx, query, userID, groupID+":%")
	if err != nil {
		return nil, fmt.Errorf("failed to query latest leaderboard records: %w", err)
	}
	defer rows.Close()

	records := make(map[evr.Symbol]map[string]map[evr.ResetSchedule]float64) //map[mode]map[statID]map[resetSchedule]float64
	for rows.Next() {
		var leaderboardID string
		var score, subscore int64

		err = rows.Scan(&leaderboardID, &score, &subscore)
		if err != nil {
			return nil, fmt.Errorf("failed to scan latest leaderboard record: %w", err)
		}

		meta, err := LeaderboardMetaFromID(leaderboardID)
		if err != nil {
			continue
		}

		if _, ok := records[meta.Mode]; !ok {
			records[meta.Mode] = make(map[string]map[evr.ResetSchedule]float64)
		}

		if _, ok := records[meta.Mode][meta.StatName]; !ok {
			records[meta.Mode][meta.StatName] = make(map[evr.ResetSchedule]float64)
		}

		records[meta.Mode][meta.StatName][meta.ResetSchedule] = ScoreToValue(score, subscore)

	}

	return records, nil
}
