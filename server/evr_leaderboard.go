package server

import (
	"context"
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
	logger  runtime.Logger
	queueCh chan LeaderboardRecordWriteQueueEntry
}

func PeriodicityToSchedule(periodicity string) string {
	switch periodicity {
	case "daily":
		return "0 0 * * *"
	case "weekly":
		return "0 0 * * 1"
	default:
		return ""
	}
}

func NewLeaderboardRegistry(logger runtime.Logger, nk runtime.NakamaModule, node string) *LeaderboardRegistry {
	queueCh := make(chan LeaderboardRecordWriteQueueEntry, 8*3*100) // three matches ending at the same time, 100 records per player
	r := &LeaderboardRegistry{
		node:    node,
		nk:      nk,
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
				operator, sortOrder, resetSchedule := r.leaderboardConfig(e.BoardMeta.name, e.BoardMeta.operator, e.BoardMeta.periodicity)
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
	mode        evr.Symbol
	name        string
	operator    string
	periodicity string
}

func (l LeaderboardMeta) ID() string {
	return fmt.Sprintf("%s:%s:%s", l.mode.String(), l.name, l.periodicity)
}

func (r *LeaderboardRegistry) LeaderboardMetaFromID(id string) (LeaderboardMeta, error) {
	parts := strings.Split(id, ":")
	if len(parts) != 3 {
		return LeaderboardMeta{}, fmt.Errorf("invalid leaderboard ID: %s", id)
	}

	mode := evr.ToSymbol(parts[0])

	return LeaderboardMeta{
		mode:        mode,
		name:        parts[1],
		periodicity: parts[2],
	}, nil
}

func (r *LeaderboardRegistry) profileUpdate(userID, displayName string, profile *evr.ServerProfile, data map[string]map[LeaderboardMeta]float64) error {

	for statGroup, stats := range data {
		for meta, value := range stats {
			r.LeaderboardTabletStatWrite(context.Background(), meta, userID, displayName, value)

			// Update the tablet stats with the record from the leaderboard
			if _, ok := profile.Statistics[statGroup]; !ok {
				profile.Statistics[statGroup] = make(map[string]evr.MatchStatistic)
			}

			profile.Statistics[statGroup][meta.name] = evr.MatchStatistic{
				Count: 1,
				Value: value,
			}
		}
	}

	return nil
}

func (r *LeaderboardRegistry) ProcessProfileUpdate(ctx context.Context, logger *zap.Logger, userID, displayName string, mode evr.Symbol, payload *evr.UpdatePayload, serverProfile *evr.ServerProfile) error {

	// Build the operations
	ops := r.buildOperations(mode, payload)
	if err := r.profileUpdate(userID, displayName, serverProfile, ops); err != nil {
		return fmt.Errorf("Profile update error: %w", err)
	}
	return nil
}

func (r *LeaderboardRegistry) buildOperations(mode evr.Symbol, payload *evr.UpdatePayload) map[string]map[LeaderboardMeta]float64 {
	var prefix, periodicity string

	opsByStatGroup := make(map[string]map[LeaderboardMeta]float64)
	// Process each statGroup
	for statGroup, stats := range payload.Update.StatsGroups {

		opsByStatGroup[statGroup] = make(map[LeaderboardMeta]float64)

		// Determine the prefix and periodicity

		if statGroup == "arena" {

			prefix = "Arena"
			periodicity = "alltime"

		} else if statGroup == "combat" {

			prefix = "Combat"
			periodicity = "alltime"

		} else if strings.HasPrefix(statGroup, "daily_") {

			prefix = "Arena"
			periodicity = "daily"

		} else if strings.HasPrefix(statGroup, "weekly_") {

			prefix = "Arena"
			periodicity = "weekly"

		} else {
			r.logger.Warn("Unknown mode for profile update", zap.String("mode", mode.String()), zap.Any("payload", payload))
			continue
		}

		// Create the games played stat
		r.createGamesPlayedStat(stats, prefix)

		// Build the operations
		for name, stat := range stats {

			meta := LeaderboardMeta{
				mode:        mode,
				name:        name,
				operator:    stat.Operator,
				periodicity: periodicity,
			}
			opsByStatGroup[statGroup][meta] = stat.Value
		}
	}

	return opsByStatGroup
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

func (r *LeaderboardRegistry) createGamesPlayedStat(stats map[string]evr.StatUpdate, prefix string) {
	wins := stats[prefix+"Wins"].Value
	losses := stats[prefix+"Losses"].Value
	total := wins + losses

	if total == 0 {
		return
	}

	stats[prefix+"GamesPlayed"] = evr.StatUpdate{
		Operator: "add",
		Value:    total,
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

func (*LeaderboardRegistry) scoreToValue(score int64, subscore int64) float64 {
	// If there's no subscore, return the score as a whole number.
	if subscore == 0 {
		return float64(score)
	}

	// Otherwise, combine the score and subscore as a float.
	f, _ := strconv.ParseFloat(fmt.Sprintf("%d.%d", score, subscore), 64)
	return f
}
