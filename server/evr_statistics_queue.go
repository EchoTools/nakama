package server

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"reflect"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

type StatisticsQueue struct {
	logger runtime.Logger
	ch     chan []*StatisticsQueueEntry
}

func NewStatisticsQueue(logger runtime.Logger, nk runtime.NakamaModule) *StatisticsQueue {
	ch := make(chan []*StatisticsQueueEntry, 8*3*100) // three matches ending at the same time, 100 records per player
	r := &StatisticsQueue{
		logger: logger,
		ch:     ch,
	}

	go func() {
		ctx := context.Background()
		select {
		case <-ctx.Done():
			return
		case entries := <-ch:

			for _, e := range entries {
				if _, err := nk.LeaderboardRecordWrite(ctx, e.BoardMeta.ID(), e.UserID, e.DisplayName, e.Score, e.Subscore, map[string]any{}, &e.Override); err != nil {
					// Try to create the leaderboard
					operator, sortOrder, cronSchedule := OperatorToLeaderboardOperator(e.BoardMeta.operator), "desc", ResetScheduleToCron(e.BoardMeta.resetSchedule)
					if err = nk.LeaderboardCreate(ctx, e.BoardMeta.ID(), true, sortOrder, operator, cronSchedule, map[string]any{}, true); err != nil {
						logger.Error("Leaderboard create error", zap.Error(err))
					} else {
						logger.Error("Leaderboard record write error", zap.Error(err))
					}
				}
			}
		}

	}()

	return r
}

type LeaderboardMeta struct {
	GroupID       string
	Mode          evr.Symbol
	StatName      string
	Operator      int
	ResetSchedule evr.ResetSchedule
}

func (l LeaderboardMeta) ID() string {
	return StatisticBoardID(l.GroupID, l.Mode, l.StatName, l.ResetSchedule)
}

func LeaderboardMetaFromID(id string) (LeaderboardMeta, error) {
	parts := strings.Split(id, ":")
	if len(parts) != 3 {
		return LeaderboardMeta{}, fmt.Errorf("invalid leaderboard ID: %s", id)
	}

	mode := evr.Symbol(evr.ToSymbol(parts[0]))

	return LeaderboardMeta{
		GroupID:       parts[0],
		Mode:          mode,
		StatName:      parts[1],
		ResetSchedule: evr.ResetSchedule(parts[2]),
	}, nil
}

type StatisticsQueueEntry struct {
	BoardMeta   LeaderboardMeta
	UserID      string
	DisplayName string
	Score       int64
	Subscore    int64
	Metadata    map[string]string
	Override    int
}

func (r *StatisticsQueue) Add(entries []*StatisticsQueueEntry) {

	select {
	case r.ch <- entries:
		r.logger.Debug("Leaderboard record queued", zap.Int("count", len(entries)))
	default:
		r.logger.Warn("Leaderboard record write queue full, dropping entry", zap.Int("count", len(entries)))
	}
}

func PlayerStatisticsGetID(ctx context.Context, db *sql.DB, ownerID, groupID string, modes []evr.Symbol, includeDailyWeekly bool) (evr.PlayerStatistics, map[string]evr.Statistic, error) {

	resetSchedules := []evr.ResetSchedule{
		evr.ResetScheduleAllTime,
	}
	if includeDailyWeekly {
		if len(modes) > 1 {
			return nil, nil, fmt.Errorf("cannot include daily/weekly stats for multiple modes")
		}

		resetSchedules = append(resetSchedules, evr.ResetScheduleDaily, evr.ResetScheduleWeekly)
	}

	playerStatistics := evr.NewStatistics()

	boardMap := make(map[string]evr.Statistic)
	boardIDs := make([]string, 0, len(boardMap))

	for _, m := range modes {
		for _, r := range resetSchedules {

			var stats evr.Statistics
			switch m {
			case evr.ModeCombatPublic:
				stats = &evr.CombatStatistics{}
			case evr.ModeCombatPrivate:
				stats = &evr.GenericStats{}
			case evr.ModeArenaPublic:
				stats = &evr.ArenaStatistics{}
			case evr.ModeArenaPrivate:
				stats = &evr.GenericStats{}
			case evr.ModeSocialPublic:
				stats = &evr.GenericStats{}
			case evr.ModeSocialPrivate:
				stats = &evr.GenericStats{}
			default:
				return nil, nil, fmt.Errorf("invalid mode: %s", m)
			}

			playerStatistics[evr.StatisticsGroup{
				Mode:          m,
				ResetSchedule: r,
			}] = stats

			statsValue := reflect.ValueOf(stats)
			statsType := statsValue.Elem().Type()

			for i := 0; i < statsType.NumField(); i++ {
				fieldType := statsType.Field(i)

				boardID := StatisticBoardID(groupID, m, fieldType.Name, r)
				boardIDs = append(boardIDs, boardID)

				fieldValue := statsValue.Elem().Field(i)
				fieldValue.Set(reflect.New(fieldType.Type.Elem()))
				v := fieldValue.Elem()
				_ = v
				boardMap[boardID] = fieldValue.Interface().(evr.Statistic)
			}
		}

	}

	query := `
		SELECT 
			lr.leaderboard_id,
			lr.score, 
			lr.subscore
		FROM 
			leaderboard_record lr
		JOIN
			ROWS FROM (
				unnest(
                $2::TEXT[]
				)
			) t(id)
		ON 
			lr.leaderboard_id = t.id
		WHERE 
			lr.owner_id = $1`

	rows, err := db.QueryContext(ctx, query, ownerID, boardIDs)
	if err != nil {
		if err == sql.ErrNoRows {
			// Return the default profile's stats
			return playerStatistics, boardMap, nil
		}
		return nil, nil, fmt.Errorf("failed to query leaderboard records: %w", err)
	}
	defer rows.Close()

	var dbLeaderboardID string
	var dbScore, dbSubscore int64

	for rows.Next() {

		err = rows.Scan(&dbLeaderboardID, &dbScore, &dbSubscore)
		if err != nil {

			return nil, nil, fmt.Errorf("failed to scan leaderboard record: %w", err)
		}

		v, ok := boardMap[dbLeaderboardID]
		if !ok {
			log.Printf("Leaderboard record found for unknown leaderboard ID: %s", dbLeaderboardID)
			continue
		}
		v.FromScore(dbScore, dbSubscore)

	}

	// Use the GamesPlayed stat to fill in all the cnt's

	return playerStatistics, boardMap, nil
}

func StatisticsToEntries(userID, displayName, groupID string, mode evr.Symbol, prev, update evr.Statistics) ([]*StatisticsQueueEntry, error) {
	statsValue := reflect.ValueOf(update).Elem()
	prevStatsValue := reflect.ValueOf(prev).Elem()
	statsType := statsValue.Type()

	entries := make([]*StatisticsQueueEntry, 0, statsType.NumField()*3)
	for i := 0; i < statsType.NumField(); i++ {

		prev := prevStatsValue.Field(i).Interface().(evr.Statistic)
		value := statsValue.Field(i).Interface().(evr.Statistic)

		t := statsValue.Field(i).Type()
		statName := t.Field(i).Tag.Get("json")
		statName = strings.SplitN(statName, ",", 2)[0]

		// Based on the type of the field, modify the update value
		var updateValue float64
		switch statsValue.Field(i).Addr().Elem().Interface().(type) {
		case evr.StatisticIntegerIncrement:
			updateValue = value.GetValue() - prev.GetValue()
		case evr.StatisticFloatIncrement:
			updateValue = value.GetValue() - prev.GetValue()
		default:
			updateValue = value.GetValue()
		}

		for _, r := range []evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime} {
			op := StatisticOperator(value)
			meta := LeaderboardMeta{
				GroupID:       groupID,
				Mode:          mode,
				StatName:      statName,
				Operator:      op,
				ResetSchedule: r,
			}

			// If the value is zero, or the previous value is zero, do not update the stat
			if updateValue == 0 {
				continue
			}

			score, subscore := ValueToScore(updateValue)

			entries = append(entries, &StatisticsQueueEntry{
				BoardMeta:   meta,
				UserID:      userID,
				DisplayName: displayName,
				Score:       score,
				Subscore:    subscore,
				Metadata:    nil,
				Override:    2, // set
			})
		}
	}

	return entries, nil
}
