package server

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/thriftrw/ptr"
)

const (
	TabletStatisticIntegerValue = iota
	TabletStatisticFloatValue

	GamesPlayedStatisticID      = "GamesPlayed"
	RankPercentileStatisticID   = "RankPercentile"
	SkillRatingMuStatisticID    = "SkillRatingMu"
	SkillRatingSigmaStatisticID = "SkillRatingSigma"
	LobbyTimeStatisticID        = "LobbyTime"
	GameServerTimeStatisticsID  = "GameServerTime"
	EarlyQuitStatisticID        = "EarlyQuits"
)

type LeaderboardOperator string

const (
	OperatorBest      LeaderboardOperator = "best"
	OperatorSet       LeaderboardOperator = "set"
	OperatorIncrement LeaderboardOperator = "incr"
	OperatorDecrement LeaderboardOperator = "decr"
)

type LeaderboardMeta struct {
	GroupID       string
	Mode          evr.Symbol
	StatName      string
	Operator      LeaderboardOperator
	ResetSchedule evr.ResetSchedule
}

func (l LeaderboardMeta) ID() string {
	return StatisticBoardID(l.GroupID, l.Mode, l.StatName, l.ResetSchedule)
}

func LeaderboardMetaFromID(id string) (LeaderboardMeta, error) {
	parts := strings.Split(id, ":")
	if len(parts) != 4 {
		return LeaderboardMeta{}, fmt.Errorf("invalid leaderboard ID: %s", id)
	}

	mode := evr.Symbol(evr.ToSymbol(parts[1]))

	return LeaderboardMeta{
		GroupID:       parts[0],
		Mode:          mode,
		StatName:      parts[2],
		ResetSchedule: evr.ResetSchedule(parts[3]),
	}, nil
}

func MatchmakingRatingLoad(ctx context.Context, nk runtime.NakamaModule, userID, groupID string, mode evr.Symbol) (types.Rating, error) {
	// Look for an existing account.

	var sigma, mu float64

	structMap := map[string]*float64{
		SkillRatingMuStatisticID:    &mu,
		SkillRatingSigmaStatisticID: &sigma,
	}

	for statName, ptr := range structMap {
		boardID := StatisticBoardID(groupID, mode, statName, "alltime")

		_, ownerRecords, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 1, "", 0)
		if err != nil {
			return NewDefaultRating(), err
		}

		if len(ownerRecords) == 0 {
			return NewDefaultRating(), nil
		}

		record := ownerRecords[0]
		*ptr = ScoreToValue(record.Score, record.Subscore)
	}
	if sigma == 0 || mu == 0 {
		return NewDefaultRating(), nil
	}
	return rating.NewWithOptions(&types.OpenSkillOptions{
		Mu:    ptr.Float64(mu),
		Sigma: ptr.Float64(sigma),
	}), nil
}

func MatchmakingRatingStore(ctx context.Context, nk runtime.NakamaModule, userID, displayName, groupID string, mode evr.Symbol, rating types.Rating) error {

	scores := map[string]float64{
		StatisticBoardID(groupID, mode, SkillRatingSigmaStatisticID, "alltime"): rating.Sigma,
		StatisticBoardID(groupID, mode, SkillRatingMuStatisticID, "alltime"):    rating.Mu,
	}

	for id, value := range scores {
		score, subscore := ValueToScore(value)
		if score == 0 && subscore == 0 {
			continue
		}

		// Write the record
		if _, err := nk.LeaderboardRecordWrite(ctx, id, userID, displayName, score, subscore, nil, nil); err != nil {
			// Try to create the leaderboard
			err = nk.LeaderboardCreate(ctx, id, true, "desc", "set", "", nil, true)
			if err != nil {
				return fmt.Errorf("Leaderboard create error: %w", err)
			} else {
				// Retry the write
				_, err := nk.LeaderboardRecordWrite(ctx, id, userID, displayName, score, subscore, nil, nil)
				if err != nil {
					return fmt.Errorf("Leaderboard record write error: %w", err)
				}
			}
		}
	}

	return nil
}

func MatchmakingRankPercentileLoad(ctx context.Context, nk runtime.NakamaModule, userID, groupID string, mode evr.Symbol) (percentile float64, err error) {

	boardID := StatisticBoardID(groupID, mode, RankPercentileStatisticID, "alltime")

	_, records, _, _, err := nk.LeaderboardRecordsList(ctx, boardID, []string{userID}, 10000, "", 0)
	if err != nil {
		return ServiceSettings().Matchmaking.RankPercentile.Default, nil
	}

	if len(records) == 0 {
		return ServiceSettings().Matchmaking.RankPercentile.Default, nil
	}

	return ScoreToValue(records[0].Score, records[0].Subscore), nil
}

func MatchmakingRankPercentileStore(ctx context.Context, nk runtime.NakamaModule, userID, username, groupID string, mode evr.Symbol, percentile float64) error {

	id := StatisticBoardID(groupID, mode, RankPercentileStatisticID, "alltime")

	score, subscore := ValueToScore(percentile)

	if score == 0 && subscore == 0 {
		return nil
	}
	// Write the record
	_, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, score, subscore, nil, nil)

	if err != nil {
		// Try to create the leaderboard
		err = nk.LeaderboardCreate(ctx, id, true, "asc", "set", "", nil, true)

		if err != nil {
			return fmt.Errorf("Leaderboard create error: %w", err)
		} else {
			// Retry the write
			_, err := nk.LeaderboardRecordWrite(ctx, id, userID, username, score, subscore, nil, nil)
			if err != nil {
				return fmt.Errorf("Leaderboard record write error: %w", err)
			}
		}
	}

	return nil
}

func ValueToScore(v float64) (int64, int64) {
	// If it's a whole number, return it as such.
	if v == float64(int64(v)) {
		return int64(v), 0
	}

	// Otherwise, split the float into whole and fractional parts.
	str := strconv.FormatFloat(float64(v), 'f', -1, 64)
	s := strings.Split(str, ".")

	// Parse the whole and fractional parts as integers.
	whole, _ := strconv.ParseInt(s[0], 10, 64)
	fractional, _ := strconv.ParseInt(s[1], 10, 64)

	return whole, fractional
}

func ScoreToValue(score int64, subscore int64) float64 {
	// If there's no subscore, return the score as a whole number.
	if subscore == 0 {
		return float64(score)
	}

	// Otherwise, combine the score and subscore as a float.
	f, _ := strconv.ParseFloat(fmt.Sprintf("%d.%d", score, subscore), 64)
	return f
}

func StatisticBoardID(groupID string, mode evr.Symbol, statName string, resetSchedule evr.ResetSchedule) string {
	return fmt.Sprintf("%s:%s:%s:%s", groupID, mode.String(), statName, resetSchedule)
}

func ParseStatisticBoardID(id string) (groupID string, mode evr.Symbol, statName string, resetSchedule string, err error) {
	parts := strings.SplitN(id, ":", 4)
	if len(parts) != 4 {
		err = fmt.Errorf("invalid leaderboard ID: %s", id)
		return
	}
	return parts[0], evr.ToSymbol(parts[1]), parts[2], parts[3], nil
}

func ResetScheduleToCron(resetSchedule evr.ResetSchedule) string {
	switch resetSchedule {
	case evr.ResetScheduleDaily:
		return "0 16 * * *"
	case evr.ResetScheduleWeekly:
		return "0 16 * * 4"
	case evr.ResetScheduleAllTime:
		fallthrough
	default:
		return ""
	}
}

func PlayerStatisticsGetID(ctx context.Context, db *sql.DB, nk runtime.NakamaModule, ownerID, groupID string, modes []evr.Symbol, dailyWeeklyStatMode evr.Symbol) (evr.PlayerStatistics, map[string]evr.Statistic, error) {

	startTime := time.Now()

	defer func() {
		nk.MetricsTimerRecord("player_statistics_get_latency", nil, time.Since(startTime))
	}()

	statGroups := make(map[evr.Symbol][]evr.ResetSchedule)

	// Always include Arena and Combat Public stats
	modes = append(modes, evr.ModeCombatPublic, evr.ModeArenaPublic)
	slices.Sort(modes)
	modes = slices.Compact(modes)

	for _, m := range modes {
		if m == evr.Symbol(0) {
			continue
		}
		statGroups[m] = []evr.ResetSchedule{evr.ResetScheduleAllTime}
		if dailyWeeklyStatMode == m {
			statGroups[m] = append(statGroups[m], evr.ResetScheduleDaily, evr.ResetScheduleWeekly)
		}
	}

	playerStatistics := evr.NewStatistics()

	boardMap := make(map[string]evr.Statistic)
	boardIDs := make([]string, 0, len(boardMap))
	gamesPlayedBoardIDs := make(map[evr.StatisticsGroup]string)

	// Build the stats structs
	for m, resetSchedules := range statGroups {
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
			lr.owner_id = $1
			AND (lr.expiry_time > NOW() OR lr.expiry_time = '1970-01-01 00:00:00+00') -- Include "alltime" records
			`

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
	for m, resetSchedules := range statGroups {
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

			if !prevValue.IsValid() || field.IsNil() {
				continue
			}
			stat := field.Interface().(evr.Statistic)
			if stat == nil {
				continue
			}

			if prevValue.IsValid() && !prevValue.IsNil() {
				prevField := prevValue.Elem().Field(i)
				if prevField.IsValid() && !prevField.IsNil() {
					switch prevStat := prevField.Interface().(type) {
					case *evr.StatisticIntegerIncrement:
						stat.SetValue(stat.GetValue() - prevStat.GetValue())
					case *evr.StatisticFloatIncrement:
						stat.SetValue(stat.GetValue() - prevStat.GetValue())
					}
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

			var op LeaderboardOperator
			switch reflect.ValueOf(update).Elem().Field(i).Interface().(type) {
			case *evr.StatisticFloatIncrement:
				op = OperatorIncrement
			case *evr.StatisticIntegerIncrement:
				op = OperatorIncrement
			case *evr.StatisticFloatBest:
				op = OperatorBest
			case *evr.StatisticIntegerBest:
				op = OperatorBest
			case *evr.StatisticFloatSet:
				op = OperatorSet
			case *evr.StatisticIntegerSet:
				op = OperatorSet
			default:
				op = OperatorSet
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
			})
		}
	}

	return entries, nil
}
