package server

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

type LeaderboardRegistry struct {
	sync.RWMutex

	node   string
	nk     runtime.NakamaModule
	logger runtime.Logger
}

func NewLeaderboardRegistry(logger runtime.Logger, nk runtime.NakamaModule, node string) *LeaderboardRegistry {
	return &LeaderboardRegistry{
		node:   node,
		nk:     nk,
		logger: logger,
	}
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

func (r *LeaderboardRegistry) ProcessProfileUpdate(ctx context.Context, userID, username string, mode evr.Symbol, payload *evr.UpdatePayload, serverProfile *evr.ServerProfile) error {

	var statGroup, prefix string

	switch mode {
	case evr.ModeArenaPublic:
		prefix = "Arena"
		statGroup = "arena"
	case evr.ModeCombatPublic:
		prefix = "Combat"
		statGroup = "combat"
	default:
		return fmt.Errorf("unknown mode: %s", mode)
	}

	stats, ok := payload.Update.StatsGroups[statGroup]
	if !ok {
		return fmt.Errorf("no stats found for group: %s", statGroup)
	}

	// Create the games played stat
	r.createGamesPlayedStat(stats, prefix)

	// Create the leaderboard submissions
	submissions := make(map[LeaderboardMeta]float64)

	for name, stat := range stats {

		// Use the daily and weekly leaderboards
		for _, p := range []string{"alltime", "daily", "weekly"} {
			meta := LeaderboardMeta{
				mode:        mode,
				name:        name,
				operator:    stat.Operator,
				periodicity: p,
			}

			submissions[meta] = stat.Value

		}
	}

	// Submit the score
	for meta, value := range submissions {
		record, err := r.RecordWriteTabletStat(ctx, meta, userID, username, value)
		if err != nil {
			return fmt.Errorf("Leaderboard record write error: %v", err)
		}

		if _, ok := serverProfile.Statistics[statGroup]; !ok {
			serverProfile.Statistics[statGroup] = make(map[string]evr.MatchStatistic)
		}

		serverProfile.Statistics[statGroup][meta.name] = evr.MatchStatistic{
			Count: int64(record.NumScore),
			Value: r.scoreToValue(record.Score, record.Subscore),
		}
	}

	return nil
}

func (r *LeaderboardRegistry) RecordWriteTabletStat(ctx context.Context, meta LeaderboardMeta, userID, username string, value float64) (*api.LeaderboardRecord, error) {
	score, subscore := r.valueToScore(value)

	// All tablet stat updates are "set" operations
	override := 2 // set
	record, err := r.nk.LeaderboardRecordWrite(ctx, meta.ID(), userID, username, score, subscore, nil, &override)

	if err != nil {
		// Try to create the leaderboard
		operator, sortOrder, resetSchedule := r.leaderboardConfig(meta.name, meta.operator, meta.periodicity)
		err = r.nk.LeaderboardCreate(ctx, meta.ID(), true, sortOrder, operator, resetSchedule, nil)

		if err != nil {
			return nil, fmt.Errorf("Leaderboard create error: %v", err)
		} else {
			// Retry the write
			record, err = r.nk.LeaderboardRecordWrite(ctx, meta.ID(), userID, username, score, subscore, nil, &override)
		}
	}

	return record, err
}
func (r *LeaderboardRegistry) createGamesPlayedStat(stats map[string]evr.StatUpdate, prefix string) {
	wins := stats[prefix+"Wins"].Value
	losses := stats[prefix+"Losses"].Value
	total := wins + losses

	if total == 0 {
		return
	}

	stats["GamesPlayed"] = evr.StatUpdate{
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

func (*LeaderboardRegistry) scoreToValue(score int64, subscore int64) float64 {
	// If there's no subscore, return the score as a whole number.
	if subscore == 0 {
		return float64(score)
	}

	// Otherwise, combine the score and subscore as a float.
	f, _ := strconv.ParseFloat(fmt.Sprintf("%d.%d", score, subscore), 64)
	return f
}
