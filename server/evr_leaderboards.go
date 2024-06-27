package server

import (
	"context"
	"fmt"
	"math"
	"strings"
	"sync"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

type leaderboard struct {
	node      string
	matchType string // -3791849610740453400
	group     string // arena, daily, weekly
	statName  string // ArenaLosses
}

func (b leaderboard) ID() string {
	name := fmt.Sprintf("%s.leaderboard:%s:%s:%s", b.node, b.matchType, b.group, b.statName)
	return uuid.NewV5(uuid.NamespaceDNS, name).String()
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

func (r *LeaderboardRegistry) Submission(ctx context.Context, ownerID, evrID, username, matchSessionID, matchType, group, statName, operator string, value any) (*api.LeaderboardRecord, error) {

	s := strings.SplitN(group, "_", 2)
	group = s[0]

	leaderboard := leaderboard{
		node:      r.node,
		matchType: matchType,
		group:     group,
		statName:  statName,
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
			"node":       r.node,
			"match_type": matchType,
			"group":      group,
			"stat_name":  statName,
			"operator":   operator,
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

func (r *LeaderboardRegistry) List(ctx context.Context, ownerID, matchType, group, statName string) ([]*api.LeaderboardRecord, error) {
	leaderboard := leaderboard{
		node:      r.node,
		matchType: matchType,
		group:     group,
		statName:  statName,
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
