package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

type LeaderboardRecordsListRequest struct {
	LeaderboardID string            `json:"leaderboard_id"`
	GuildID       string            `json:"guild_id"`
	GroupID       string            `json:"group_id"`
	Mode          evr.Symbol        `json:"game_mode"`
	StatName      string            `json:"stat_name"`
	ResetSchedule evr.ResetSchedule `json:"reset_schedule"`
	FromRank      int64             `json:"from_rank"`
	Limit         int               `json:"limit"`
	Cursor        string            `json:"cursor"`
}

type LeaderboardRecordsListResponse struct {
	LeaderboardID string                        `json:"leaderboard_id"`
	NextCursor    string                        `json:"next_cursor"`
	PrevCursor    string                        `json:"prev_cursor"`
	Records       []*LeaderboardRecordsListItem `json:"records"`
}

type LeaderboardRecordsListItem struct {
	*api.LeaderboardRecord
	Metadata json.RawMessage `json:"metadata"`
}

func (h *RPCHandler) LeaderboardRecordsListRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {

	var (
		request = &LeaderboardRecordsListRequest{}
		err     error
		boardID string
	)

	if err := parseRequest(ctx, payload, request); err != nil {
		return "", err
	}

	if request.LeaderboardID != "" {
		boardID = request.LeaderboardID
	} else {

		if request.GuildID != "" {
			if request.GroupID, err = GetGroupIDByGuildID(ctx, db, request.GuildID); err != nil {
				return "", fmt.Errorf("failed to get group ID by discord ID: %w", err)
			}
		}

		if request.GroupID == "" {
			return "", runtime.NewError("No group ID specified", StatusInvalidArgument)
		}
		if request.ResetSchedule == "" {
			request.ResetSchedule = evr.ResetScheduleAllTime
		}
		meta := LeaderboardMeta{
			GroupID:       request.GroupID,
			Mode:          request.Mode,
			StatName:      request.StatName,
			ResetSchedule: request.ResetSchedule,
		}
		boardID = meta.ID()
	}

	if request.Cursor == "" && request.FromRank > 0 {
		request.Cursor, err = nk.LeaderboardRecordsListCursorFromRank(boardID, request.FromRank, 0)
		if err != nil {
			return "", fmt.Errorf("failed to get cursor from rank: %w", err)
		}
	}
	if request.Limit < 1 || request.Limit > 100 {
		request.Limit = 25
	}

	records, _, nextCursor, prevCursor, err := nk.LeaderboardRecordsList(ctx, boardID, nil, request.Limit, request.Cursor, 0)
	if err != nil {
		logger.WithFields(map[string]any{
			"leaderboard_id": boardID,
			"err":            err,
		}).Error("Leaderboard record error.")
		return "", err
	}

	// Sort the records by rank
	slices.SortStableFunc(records, func(a, b *api.LeaderboardRecord) int {
		return int(a.Rank - b.Rank)
	})

	items := make([]*LeaderboardRecordsListItem, 0, len(records))
	for _, r := range records {
		r.LeaderboardId = ""
		items = append(items, &LeaderboardRecordsListItem{
			LeaderboardRecord: r,
			Metadata:          json.RawMessage(r.Metadata),
		})
	}

	response := LeaderboardRecordsListResponse{
		LeaderboardID: boardID,
		Records:       items,
		NextCursor:    nextCursor,
		PrevCursor:    prevCursor,
	}
	data, err := json.MarshalIndent(response, "", "  ")
	if err != nil {
		return "", fmt.Errorf("failed to marshal records: %w", err)
	}

	return string(data), nil
}

type LeaderboardHaystackRequest struct {
	OwnerID       string            `json:"owner_id"` // The owner ID around which to show records.
	DiscordID     string            `json:"owner_discord_id"`
	LeaderboardID string            `json:"leaderboard_id"` // The leaderboard ID to get leaderboard records for.
	GuildID       string            `json:"guild_id"`       // The guild ID to get the group ID from.
	GroupID       string            `json:"group_id"`       // The group ID to get leaderboard records for.
	Mode          evr.Symbol        `json:"game_mode"`      // The game mode to get leaderboard records for.
	ResetSchedule evr.ResetSchedule `json:"reset_schedule"` // The reset schedule to get leaderboard records for (daily, weekly, alltime)
	StatName      string            `json:"stat_name"`
	Limit         int               `json:"limit"`  // Return only the required number of leaderboard records denoted by this limit value. Between 1-100.
	Cursor        string            `json:"cursor"` // Pagination cursor from previous result. Don't set to start fetching from the beginning.
}

type LeaderboardHaystackRecord struct {
	DisplayName string          `json:"display_name"`
	OwnerID     string          `json:"owner_id"`
	Rank        int64           `json:"rank,omitempty"`
	Score       int64           `json:"score"`
	Subscore    int64           `json:"subscore,omitempty"`
	NumScore    int32           `json:"num_score"`
	CreateTime  int64           `json:"create_time"`
	UpdateTime  int64           `json:"update_time"`
	ExpiryTime  int64           `json:"expire_time,omitempty"`
	Metadata    json.RawMessage `json:"metadata"`
}

type LeaderboardHaystackResponse struct {
	PrevCursor   string                      `json:"prev_cursor"`
	NextCursor   string                      `json:"next_cursor"`
	RankCount    int64                       `json:"rank_count,omitempty"`
	OwnerRecords []LeaderboardHaystackRecord `json:"owner_records"`
	Records      []LeaderboardHaystackRecord `json:"records"`
}

func (h *RPCHandler) LeaderboardHaystackRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	request := &LeaderboardHaystackRequest{}
	if err := parseRequest(ctx, payload, request); err != nil {
		return "", err
	}

	var err error

	if request.DiscordID != "" {
		if request.OwnerID = h.DiscordIDToUserID(request.DiscordID); request.OwnerID == "" {
			return "", errors.New("failed to get user ID by discord ID")
		}
	}

	if request.OwnerID == "" {
		return "", runtime.NewError("No owner ID specified", StatusInvalidArgument)
	}

	if request.GuildID != "" {
		if request.GroupID = h.GuildIDToGroupID(request.GuildID); request.GroupID == "" {
			return "", errors.New("failed to get group ID by discord ID")
		}
	}

	if request.Mode.IsNil() {
		return "", runtime.NewError("No game mode specified", StatusInvalidArgument)
	}

	var leaderboardID string
	if request.LeaderboardID != "" {
		meta, err := LeaderboardMetaFromID(request.LeaderboardID)
		if err != nil {
			return "", fmt.Errorf("failed to parse leaderboard ID: %w", err)
		}

		request.GroupID = meta.GroupID
		request.Mode = meta.Mode
		request.StatName = meta.StatName
		request.ResetSchedule = meta.ResetSchedule
		leaderboardID = meta.ID()

	} else {

		if request.GroupID == "" {
			return "", runtime.NewError("No group ID specified", StatusInvalidArgument)
		}

		if request.ResetSchedule == "" {
			request.ResetSchedule = evr.ResetScheduleAllTime
		} else if !slices.Contains([]string{"daily", "weekly", "alltime"}, string(request.ResetSchedule)) {
			return "", runtime.NewError("Invalid reset schedule, must be one of daily, weekly, alltime.", StatusInvalidArgument)
		}

		if request.Mode.IsNil() {
			request.Mode = evr.ModeArenaPublic
		}

		leaderboardID = LeaderboardMeta{
			GroupID:       request.GroupID,
			Mode:          request.Mode,
			StatName:      request.StatName,
			ResetSchedule: request.ResetSchedule,
		}.ID()
	}

	// Set the default limit if not provided.
	if request.Limit < 1 || request.Limit > 100 {
		request.Limit = 10
	}

	var ()

	// Get the records around the user
	recordsList, err := nk.LeaderboardRecordsHaystack(ctx, leaderboardID, request.OwnerID, request.Limit, request.Cursor, 0)
	if err != nil {
		return "", fmt.Errorf("failed to get leaderboard haystack records: %w", err)
	}

	if len(recordsList.OwnerRecords) == 0 && len(recordsList.Records) == 0 {
		// No records found, return an empty response.
		return "{}", nil
	}

	if len(recordsList.OwnerRecords) == 0 && len(recordsList.Records) == 0 {
		// No records found, return an empty response.
		return "{}", nil
	}

	response := &LeaderboardHaystackResponse{
		Records:    make([]LeaderboardHaystackRecord, len(recordsList.Records)),
		RankCount:  recordsList.RankCount,
		PrevCursor: recordsList.PrevCursor,
		NextCursor: recordsList.NextCursor,
	}

	for i, r := range recordsList.OwnerRecords {
		response.OwnerRecords[i] = LeaderboardHaystackRecord{
			OwnerID:     r.OwnerId,
			DisplayName: r.Username.Value,
			NumScore:    r.NumScore,
			Score:       r.Score,
			Subscore:    r.Subscore,
			Rank:        r.Rank,
			CreateTime:  r.CreateTime.GetSeconds(),
			UpdateTime:  r.CreateTime.GetSeconds(),
			ExpiryTime:  r.ExpiryTime.GetSeconds(),
			Metadata:    json.RawMessage(r.Metadata),
		}
	}

	for i, r := range recordsList.Records {
		response.Records[i] = LeaderboardHaystackRecord{
			OwnerID:     r.OwnerId,
			DisplayName: r.Username.Value,
			NumScore:    r.NumScore,
			Score:       r.Score,
			Subscore:    r.Subscore,
			Rank:        r.Rank,
			CreateTime:  r.CreateTime.GetSeconds(),
			UpdateTime:  r.CreateTime.GetSeconds(),
			ExpiryTime:  r.ExpiryTime.GetSeconds(),
			Metadata:    json.RawMessage(r.Metadata),
		}
	}

	data, err := json.Marshal(response)
	if err != nil {
		return "", fmt.Errorf("failed to marshal leaderboard haystack records: %w", err)
	}
	return string(data), nil
}
