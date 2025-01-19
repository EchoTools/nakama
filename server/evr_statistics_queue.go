package server

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"reflect"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
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
					operator, sortOrder, cronSchedule := OperatorToLeaderboardOperator(e.BoardMeta.Operator), "desc", ResetScheduleToCron(e.BoardMeta.ResetSchedule)
					if err = nk.LeaderboardCreate(ctx, e.BoardMeta.ID(), true, sortOrder, operator, cronSchedule, map[string]any{}, true); err != nil {
						logger.WithFields(map[string]interface{}{
							"leaderboard_id": e.BoardMeta.ID(),
							"error":          err.Error(),
						}).Error("Failed to create leaderboard")

					} else {
						logger.WithFields(map[string]interface{}{
							"leaderboard_id": e.BoardMeta.ID(),
						}).Debug("Leaderboard created")

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
		r.logger.WithFields(map[string]interface{}{
			"count": len(entries),
		}).Debug("Leaderboard record queued")
	default:
		r.logger.WithFields(map[string]interface{}{
			"count": len(entries),
		}).Warn("Leaderboard record write queue full, dropping entry")
	}
}

func PlayerStatisticsGetID(ctx context.Context, db *sql.DB, nk runtime.NakamaModule, ownerID, groupID string, modes []evr.Symbol, includeDailyWeekly bool) (evr.PlayerStatistics, map[string]evr.Statistic, error) {

	startTime := time.Now()

	defer func() {
		nk.MetricsTimerRecord("player_statistics_get_latency", nil, time.Since(startTime))
	}()

	resetSchedules := []evr.ResetSchedule{evr.ResetScheduleAllTime}

	if includeDailyWeekly {
		if len(modes) > 1 {
			return nil, nil, fmt.Errorf("cannot include daily/weekly stats for multiple modes")
		}

		resetSchedules = append(resetSchedules, evr.ResetScheduleDaily, evr.ResetScheduleWeekly)
	}

	playerStatistics := evr.NewStatistics()

	boardMap := make(map[string]evr.Statistic)
	boardIDs := make([]string, 0, len(boardMap))
	gamesPlayedBoardIDs := make(map[evr.StatisticsGroup]string)

	for _, m := range modes {
		for _, r := range resetSchedules {

			var stats evr.Statistics
			switch m {
			case evr.ModeCombatPublic:
				stats = &evr.CombatStatistics{}
			case evr.ModeArenaPublic:
				stats = &evr.ArenaStatistics{}
			case evr.ModeCombatPrivate:
				stats = &evr.GenericStats{}
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

				if fieldType.Name == "GamesPlayed" {
					gamesPlayedBoardIDs[evr.StatisticsGroup{
						Mode:          m,
						ResetSchedule: r,
					}] = boardID
				}

				fieldValue := statsValue.Elem().Field(i)
				fieldValue.Set(reflect.New(fieldType.Type.Elem()))
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

		if dbScore == 0 && dbSubscore == 0 {
			continue
		}

		v, ok := boardMap[dbLeaderboardID]
		if !ok {
			log.Printf("Leaderboard record found for unknown leaderboard ID: %s", dbLeaderboardID)
			continue
		}
		v.SetCount(1)
		v.FromScore(dbScore, dbSubscore)

	}

	// Use the GamesPlayed stat to fill in all the cnt's
	for _, m := range modes {
		for _, r := range resetSchedules {

			gamesPlayedID := gamesPlayedBoardIDs[evr.StatisticsGroup{
				Mode:          m,
				ResetSchedule: r,
			}]
			if stat, ok := boardMap[gamesPlayedID]; ok {
				if gamesPlayed, ok := stat.(*evr.StatisticIntegerIncrement); ok {
					for _, boardID := range boardIDs {
						if v, ok := boardMap[boardID]; ok {

							// If the board's value is 0, remove it.
							if boardMap[boardID].GetValue() == 0 {
								delete(boardMap, boardID)
								continue
							}
							if _, ok := v.(*evr.StatisticFloatAverage); ok {
								boardMap[boardID].SetCount(int64(gamesPlayed.GetValue()))
							}
						}
					}
				}
			}
		}
	}

	// Ensure arena level is always set
	if s := playerStatistics[evr.StatisticsGroup{
		Mode:          evr.ModeArenaPublic,
		ResetSchedule: evr.ResetScheduleAllTime,
	}].(*evr.ArenaStatistics); s != nil {
		if s.Level == nil {
			s.Level = &evr.StatisticIntegerIncrement{
				IntegerStatistic: evr.IntegerStatistic{
					Count: 1,
					Value: 1,
				},
			}
		} else {
			if s.Level.GetCount() <= 0 {
				s.Level.SetCount(1)
			}
			if s.Level.GetValue() <= 0 {
				s.Level.SetValue(1)
			}
		}
	}

	// Ensure combat level is always set
	if s := playerStatistics[evr.StatisticsGroup{
		Mode:          evr.ModeCombatPublic,
		ResetSchedule: evr.ResetScheduleAllTime,
	}].(*evr.CombatStatistics); s != nil {
		if s.Level == nil {
			s.Level = &evr.StatisticIntegerIncrement{
				IntegerStatistic: evr.IntegerStatistic{
					Count: 1,
					Value: 1,
				},
			}
		} else {
			if s.Level.GetCount() <= 0 {
				s.Level.SetCount(1)
			}
			if s.Level.GetValue() <= 0 {
				s.Level.SetValue(1)
			}
		}
	}

	return playerStatistics, boardMap, nil
}

func StatisticsToEntries(userID, displayName, groupID string, mode evr.Symbol, prev, update evr.Statistics) ([]*StatisticsQueueEntry, error) {

	// Modify the update based on the previous stats
	updateValue := reflect.ValueOf(update)
	prevValue := reflect.ValueOf(prev)
	for i := 0; i < updateValue.Elem().NumField(); i++ {

		if field := updateValue.Elem().Field(i); !field.IsNil() {

			stat := field.Interface().(evr.Statistic)

			if prevField := prevValue.Elem().Field(i); !prevField.IsNil() {

				switch prevStat := prevField.Interface().(type) {
				case *evr.StatisticIntegerIncrement:
					stat.SetValue(stat.GetValue() - prevStat.GetValue())
				case *evr.StatisticFloatIncrement:
					stat.SetValue(stat.GetValue() - prevStat.GetValue())
				}
			}
		}
	}
	resetSchedules := []evr.ResetSchedule{evr.ResetScheduleDaily, evr.ResetScheduleWeekly, evr.ResetScheduleAllTime}
	// construct the entries
	entries := make([]*StatisticsQueueEntry, 0, len(resetSchedules)*reflect.ValueOf(update).Elem().NumField())

	for _, r := range resetSchedules {

		for i := 0; i < reflect.ValueOf(update).Elem().NumField(); i++ {

			if reflect.ValueOf(update).Elem().Field(i).IsNil() {
				continue
			}

			statName := reflect.ValueOf(update).Elem().Type().Field(i).Tag.Get("json")
			statName = strings.SplitN(statName, ",", 2)[0]
			statValue := reflect.ValueOf(update).Elem().Field(i).Interface().(evr.Statistic).GetValue()
			op := LeaderboardOperatorSet

			switch reflect.ValueOf(update).Elem().Field(i).Interface().(type) {
			case *evr.StatisticFloatIncrement:
				op = LeaderboardOperatorIncrement
			case *evr.StatisticIntegerIncrement:
				op = LeaderboardOperatorIncrement
			case *evr.StatisticFloatBest:
				op = LeaderboardOperatorBest
			case *evr.StatisticIntegerBest:
				op = LeaderboardOperatorBest
			case *evr.StatisticFloatSet:
				op = LeaderboardOperatorSet
			case *evr.StatisticIntegerSet:
				op = LeaderboardOperatorSet
			default:
				op = LeaderboardOperatorSet
			}

			meta := LeaderboardMeta{
				GroupID:       groupID,
				Mode:          mode,
				StatName:      statName,
				Operator:      op,
				ResetSchedule: r,
			}

			score, subscore := ValueToScore(statValue)

			entries = append(entries, &StatisticsQueueEntry{
				BoardMeta:   meta,
				UserID:      userID,
				DisplayName: displayName,
				Score:       score,
				Subscore:    subscore,
				Metadata:    nil,
				Override:    op,
			})
		}
	}

	return entries, nil
}
