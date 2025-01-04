package server

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

type LeaderboardRegistry struct {
	sync.RWMutex

	node    string
	nk      runtime.NakamaModule
	db      *sql.DB
	logger  runtime.Logger
	queueCh chan LeaderboardRecordWriteQueueEntry
}

func StatisticBoardID(groupID string, mode evr.Symbol, statName, resetSchedule string) string {
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

func ResetScheduleToCron(periodicity string) string {
	switch periodicity {
	case "daily":
		return "0 0 * * *"
	case "weekly":
		return "0 0 * * 1"
	default:
		return ""
	}
}

func NewLeaderboardRegistry(logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, node string) *LeaderboardRegistry {
	queueCh := make(chan LeaderboardRecordWriteQueueEntry, 8*3*100) // three matches ending at the same time, 100 records per player
	r := &LeaderboardRegistry{
		node:    node,
		nk:      nk,
		db:      db,
		logger:  logger,
		queueCh: queueCh,
	}

	go func() {
		ctx := context.Background()
		select {
		case <-ctx.Done():
			return
		case e := <-queueCh:
			if _, err := nk.LeaderboardRecordWrite(ctx, e.BoardMeta.ID(), e.UserID, e.DisplayName, e.Score, e.Subscore, map[string]any{}, &e.Override); err != nil {
				// Try to create the leaderboard
				operator, sortOrder, resetSchedule := r.leaderboardConfig(e.BoardMeta.name, e.BoardMeta.operator, e.BoardMeta.resetSchedule)
				if err = nk.LeaderboardCreate(ctx, e.BoardMeta.ID(), true, sortOrder, operator, resetSchedule, map[string]any{}, true); err != nil {
					logger.Error("Leaderboard create error", zap.Error(err))
				} else {
					logger.Error("Leaderboard record write error", zap.Error(err))
				}
			}
		}

	}()

	return r
}

func (r *LeaderboardRegistry) leaderboardConfig(name, op, group string) (operator, sortOrder, resetSchedule string) {

	// Split daily_/weekly_ from the group
	periodicity, _, _ := strings.Cut(group, "_")

	switch periodicity {
	case "daily":
		resetSchedule = "0 0 * * *"
	case "weekly":
		resetSchedule = "0 0 * * 1"
	default:
		resetSchedule = ""
	}

	switch op {
	case "add":
		operator = "incr"
	case "max":
		operator = "best"
	case "rep":
		operator = "set"
	default:
		operator = "set"
	}

	sortOrder = "desc"
	if strings.Contains(name, "Losses") {
		sortOrder = "asc"
	}

	return operator, sortOrder, resetSchedule
}

type LeaderboardMeta struct {
	groupID       string
	mode          evr.Symbol
	name          string
	operator      string
	resetSchedule string
}

func (l LeaderboardMeta) ID() string {
	return StatisticBoardID(l.groupID, l.mode, l.name, l.resetSchedule)
}

func LeaderboardMetaFromID(id string) (LeaderboardMeta, error) {
	parts := strings.Split(id, ":")
	if len(parts) != 3 {
		return LeaderboardMeta{}, fmt.Errorf("invalid leaderboard ID: %s", id)
	}

	mode := evr.ToSymbol(parts[0])

	return LeaderboardMeta{
		groupID:       parts[0],
		mode:          mode,
		name:          parts[1],
		resetSchedule: parts[2],
	}, nil
}

func (r *LeaderboardRegistry) ProcessProfileUpdate(ctx context.Context, logger *zap.Logger, userID, displayName, groupID string, mode evr.Symbol, payload *evr.UpdatePayload) error {
	var prefix, resetSchedule string

	opsByStatGroup := make(map[string]map[LeaderboardMeta]float64)
	// Process each statGroup
	for statGroup, stats := range payload.Update.StatsGroups {

		opsByStatGroup[statGroup] = make(map[LeaderboardMeta]float64)

		// Determine the prefix and periodicity

		if statGroup == "arena" {

			resetSchedule = "alltime"
			prefix = "Arena"

		} else if statGroup == "combat" {

			resetSchedule = "alltime"
			prefix = "Combat"

		}

		if strings.HasPrefix(statGroup, "daily_") {

			resetSchedule = "daily"

		} else if strings.HasPrefix(statGroup, "weekly_") {

			resetSchedule = "weekly"

		} else {
			r.logger.Warn("Unknown mode for profile update", zap.String("mode", mode.String()), zap.Any("payload", payload))
			continue
		}

		// Create games played stat
		stats["GamesPlayed"] = evr.StatUpdate{
			Operator: "add",
			Value:    stats[prefix+"Wins"].Value + stats[prefix+"Losses"].Value,
		}

		// Build the operations
		for name, stat := range stats {

			meta := LeaderboardMeta{
				groupID:       groupID,
				mode:          mode,
				name:          name,
				operator:      stat.Operator,
				resetSchedule: resetSchedule,
			}
			r.LeaderboardTabletStatWrite(context.Background(), meta, userID, displayName, stat.Value)
		}
	}

	return nil
}

type LeaderboardRecordWriteQueueEntry struct {
	BoardMeta   LeaderboardMeta
	UserID      string
	DisplayName string
	Score       int64
	Subscore    int64
	Metadata    map[string]string
	Override    int
}

func (r *LeaderboardRegistry) LeaderboardTabletStatWrite(ctx context.Context, meta LeaderboardMeta, userID, displayName string, value float64) {

	// split the value into score and subscore (whole and fractional)
	score, subscore := r.valueToScore(value)

	entry := LeaderboardRecordWriteQueueEntry{
		BoardMeta:   meta,
		UserID:      userID,
		DisplayName: displayName,
		Score:       score,
		Subscore:    subscore,
		Metadata:    nil,
		Override:    2, // set
	}

	select {
	case r.queueCh <- entry:
	default:
		r.logger.Warn("Leaderboard record write queue full, dropping entry", zap.Any("entry", entry))
	}
}

func (*LeaderboardRegistry) valueToScore(v float64) (int64, int64) {
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

func (r *LeaderboardRegistry) RecordToStatistic(operator int, score, subscore int64, count int64) evr.MatchStatistic {

	var op string
	switch operator {
	case LeaderboardOperatorSet:
		op = "set"
	case LeaderboardOperatorBest:
		op = "max"
	case LeaderboardOperatorIncrement:
		op = "add"
	}

	var value any
	if subscore == 0 {
		value = int64(score)
	} else {
		value, _ = strconv.ParseFloat(fmt.Sprintf("%d.%d", score, subscore), 64)
	}

	return evr.MatchStatistic{
		Operator: op,
		Value:    value,
		Count:    count,
	}
}

func LeaderboardsUserTabletStatisticsGet(ctx context.Context, db *sql.DB, userID, groupID string, modes []evr.Symbol, resetSchedule string) (map[string]map[string]evr.MatchStatistic, error) {
	if len(modes) == 0 {
		return nil, nil
	}
	query := `
	SELECT leaderboard_id, operator, score, subscore
 	FROM leaderboard_record lr, leaderboard l
 		WHERE lr.leaderboard_id = l.id
   		AND owner_id = $1
		AND (%s)
	`
	params := make([]interface{}, 0, len(modes)+1)

	params = append(params, userID)

	likes := make([]string, 0, len(modes))
	for i, mode := range modes {
		likes = append(likes, fmt.Sprintf(" l.id LIKE $%d", i+2))
		params = append(params, fmt.Sprintf("%s:%s:%%:%s", groupID, mode.String(), resetSchedule))
	}

	query = fmt.Sprintf(query, strings.Join(likes, " OR "))

	rows, err := db.QueryContext(ctx, query, params...)
	if err != nil {
		if err == sql.ErrNoRows {
			return map[string]map[string]evr.MatchStatistic{
				"arena": {
					"Level": evr.MatchStatistic{
						Operator: "add",
						Value:    1,
					},
				},
				"combat": {
					"Level": evr.MatchStatistic{
						Operator: "add",
						Value:    1,
					},
				},
			}, nil
		}
		return nil, fmt.Errorf("failed to query leaderboard records: %w", err)
	}

	records := make(map[string]map[string]evr.MatchStatistic) // map[mode]map[statName]MatchStatistic

	var dbLeaderboardID string
	var dbOperator int
	var dbScore int64
	var dbSubscore int64

	defer rows.Close()

	gamesPlayedByGroup := make(map[string]int64)

	for rows.Next() {

		err = rows.Scan(&dbLeaderboardID, &dbOperator, &dbScore, &dbSubscore)
		if err != nil {

			return nil, err
		}
		_, mode, statName, _, err := ParseStatisticBoardID(dbLeaderboardID)
		if err != nil {
			return nil, fmt.Errorf("invalid leaderboard ID: %s", dbLeaderboardID)
		}

		var statGroup string

		switch mode {
		case evr.ModeArenaPublic:
			statGroup = "arena"
		case evr.ModeCombatPublic:
			statGroup = "combat"
		default:
			continue
		}
		var op string
		switch dbOperator {
		case LeaderboardOperatorSet:
			op = "set"
		case LeaderboardOperatorBest:
			op = "max"
		case LeaderboardOperatorIncrement:
			op = "add"
		}

		if _, ok := records[statGroup]; !ok {
			records[statGroup] = make(map[string]evr.MatchStatistic)
		}

		records[statGroup][statName] = evr.MatchStatistic{
			Operator: op,
			Value:    ScoreToTableStatistic(mode, statName, dbScore, dbSubscore),
			Count:    1,
		}

		if statName == "GamesPlayed" {
			gamesPlayedByGroup[statGroup] = records[statGroup][statName].Value.(int64)
		}
	}

	// Use the GamesPlayed stat to fill in all the cnt's
	for group, gamesPlayed := range gamesPlayedByGroup {
		for statName, stat := range records[group] {
			if statName == "GamesPlayed" {
				continue
			}
			stat.Count = gamesPlayed
			records[group][statName] = stat
		}
	}

	return records, nil
}
