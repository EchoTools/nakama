package server

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

type LeaderboardMeta struct {
	statName string // ArenaLosses
	group    string // all, daily, weekly
}

func (b LeaderboardMeta) ID() string {
	return fmt.Sprintf("%s:%s", b.statName, b.group)
}

type LeaderboardRegistry struct {
	sync.RWMutex

	node   string
	nk     runtime.NakamaModule
	logger runtime.Logger
	cache  map[string]struct{} // to check if the leaderboard is known to exists
}

func NewLeaderboardRegistry(logger runtime.Logger, nk runtime.NakamaModule, node string) *LeaderboardRegistry {
	return &LeaderboardRegistry{
		node:   node,
		nk:     nk,
		logger: logger,
		cache:  make(map[string]struct{}),
	}
}

func (r *LeaderboardRegistry) exists(id string) bool {
	r.RLock()
	defer r.RUnlock()
	_, ok := r.cache[id]
	return ok
}

func (r *LeaderboardRegistry) set(id string) {
	r.Lock()
	defer r.Unlock()
	r.cache[id] = struct{}{}
}

func (r *LeaderboardRegistry) Submission(ctx context.Context, ownerID, evrID, username, matchSessionID, group, statName, operator string, value any) (*api.LeaderboardRecord, error) {

	s := strings.SplitN(group, "_", 2)
	group = s[0]

	leaderboard := LeaderboardMeta{
		group:    group,
		statName: statName,
	}

	id := leaderboard.ID()

	if !r.exists(id) {
		authoritative := true

		sortOrder := "desc"
		switch statName {
		case "ArenaLosses", "CombatLosses":
			sortOrder = "asc"
		default:
			sortOrder = "desc"
		}

		resetSchedule := ""
		switch group {
		case "arena":
			resetSchedule = ""
		case "daily":
			resetSchedule = "0 0 * * *"
		case "weekly":
			resetSchedule = "0 0 * * 1"
		}

		operator := "set"
		switch operator {
		case "add":
			operator = "incr"
		case "max":
			operator = "best"
		case "rep":
			operator = "set"
		}

		metadata := map[string]interface{}{
			"node":      r.node,
			"group":     group,
			"stat_name": statName,
			"operator":  operator,
		}

		if err := r.nk.LeaderboardCreate(ctx, id, authoritative, sortOrder, operator, resetSchedule, metadata); err != nil {
			r.logger.WithField("err", err).Error("Leaderboard create error.")
		}
		r.set(id)
	}

	var score, subscore int64
	// if the value is a float, store the decimal as a subscore
	switch value := value.(type) {
	case float64:
		score = int64(value)
		subscore = int64(math.Mod(value, 1) * 10000)
	case int64:
		score = value
	}

	metadata := map[string]any{
		"session_id": matchSessionID,
		"evrID":      evrID,
	}

	record, err := r.nk.LeaderboardRecordWrite(ctx, id, ownerID, username, score, subscore, metadata, nil)
	if err != nil {
		r.logger.WithField("err", err).Error("Leaderboard record write error.")
	}
	//r.logger.WithField("record", record).Debug("Leaderboard record write success.")

	return record, err
}

func (r *LeaderboardRegistry) List(ctx context.Context, ownerID, group, statName string) ([]*api.LeaderboardRecord, error) {
	leaderboard := LeaderboardMeta{
		group:    group,
		statName: statName,
	}

	id := leaderboard.ID()

	if !r.exists(id) {
		// Check if the leaderboard exists
		_, err := r.nk.LeaderboardsGetId(ctx, []string{id})
		if err != nil {
			r.logger.WithField("err", err).Error("Leaderboard get error")
		}

		return nil, nil
	}

	ownerIDs := []string{ownerID}
	ownerRecords := make([]*api.LeaderboardRecord, 0)
	cursor := ""
	var err error

	limit := 500

	for {
		_, ownerRecords, _, cursor, err = r.nk.LeaderboardRecordsList(ctx, id, ownerIDs, limit, cursor, 0)
		if err != nil {
			r.logger.WithField("err", err).Error("Leaderboard records list error.")
		}
		if cursor == "" {
			break
		}
	}

	return ownerRecords, nil
}

func OverallPercentile(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, userID string) (float64, map[string]*api.LeaderboardRecord, error) {

	statNames := []string{
		"ArenaGamesPlayed",
		"ArenaWins",
		"ArenaLosses",
		"ArenaWinPercentage",
		"AssistsPerGame",
		"AveragePointsPerGame",
		"AverageTopSpeedPerGame",
		"BlockPercentage",
		"GoalScorePercentage",
		"GoalsPerGame",
	}

	statRecords := make(map[string]*api.LeaderboardRecord)
	for _, s := range statNames {
		id := LeaderboardMeta{
			statName: s,
			group:    "weekly",
		}.ID()

		records, _, _, _, err := nk.LeaderboardRecordsList(ctx, id, []string{userID}, 10000, "", 0)
		if err != nil {
			logger.Error("Leaderboard records list error", zap.Error(err), zap.String("id", id))
			continue
		}

		if len(records) == 0 {
			continue
		}

		statRecords[s] = records[0]
	}

	if len(statRecords) == 0 {
		return 0, nil, nil
	}

	// Combine all the stat ranks into a single percentile.
	percentiles := make([]float64, 0, len(statRecords))
	for _, r := range statRecords {
		percentile := float64(r.GetRank()) / float64(r.GetNumScore())
		percentiles = append(percentiles, percentile)
	}

	// Calculate the overall percentile.
	overallPercentile := 0.0
	for _, p := range percentiles {
		overallPercentile += p
	}
	overallPercentile /= float64(len(percentiles))

	logger.Info("Overall percentile", zap.Float64("percentile", overallPercentile))

	return overallPercentile, statRecords, nil
}
