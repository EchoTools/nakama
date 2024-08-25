package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

func (p *EvrPipeline) lobbyCreate(ctx context.Context, logger *zap.Logger, session Session, params SessionParameters) (MatchID, error) {
	nk := p.runtimeModule
	db := p.db
	matchRegistry := p.matchRegistry

	query, err := lobbyCreateQuery(ctx, logger, db, nk, session, params)
	if err != nil {
		return MatchID{}, fmt.Errorf("failed to build matchmaking query: %v", err)
	}
	logger.Debug("Matchmaking query", zap.String("query", query))
	labels, err := lobbyListGameServers(ctx, logger, db, nk, session, query)
	if err != nil {
		return MatchID{}, err
	}

	if len(labels) == 0 {
		return MatchID{}, ErrMatchmakingNoAvailableServers
	}

	latencyHistory, err := LoadLatencyHistory(ctx, logger, db, session.UserID())
	if err != nil {
		return MatchID{}, fmt.Errorf("failed to load latency history: %v", err)
	}
	labelRTTs := latencyHistory.LabelsByAverageRTT(labels)
	labelLatencies := make([]int, len(labels))
	for i, l := range labelRTTs {
		labelLatencies[i] = l.RTT
	}

	// Sort By Priority, Region, then latency
	lobbyCreateSortOptions(labels, labelLatencies, params)

	label := labels[0]

	requiredFeatures, ok := ctx.Value(ctxRequiredFeaturesKey{}).([]string)
	if !ok {
		requiredFeatures = make([]string, 0)
	}

	label.Mode = params.Mode
	label.Level = params.Level
	label.SpawnedBy = session.UserID().String()
	label.GroupID = &params.GroupID
	label.TeamAlignments = map[string]int{session.UserID().String(): params.Role}

	label.RequiredFeatures = requiredFeatures
	label.StartTime = time.Now().UTC()

	response, err := SignalMatch(ctx, matchRegistry, label.ID, SignalPrepareSession, label)
	if err != nil {
		logger.Warn("Failed to prepare session", zap.Error(err), zap.String("mid", label.ID.UUID.String()), zap.String("response", response))
		return MatchID{}, err
	}

	// Return the prepared session
	return label.ID, nil
}

func lobbyListGameServers(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, session Session, query string) ([]*MatchLabel, error) {
	logger.Debug("Listing unassigned lobbies", zap.String("query", query))
	limit := 200
	minSize, maxSize := 1, 1 // the game server counts as one.
	matches, err := listMatches(ctx, nk, limit, minSize, maxSize, query)
	if err != nil {
		return nil, fmt.Errorf("failed to find matches: %v", err)
	}

	if len(matches) == 0 {
		return nil, ErrMatchmakingNoAvailableServers
	}

	labels := make([]*MatchLabel, 0, len(matches))
	for _, match := range matches {
		label := &MatchLabel{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
			return nil, fmt.Errorf("failed to unmarshal match label: %v", err)
		}
		labels = append(labels, label)
	}
	return labels, nil
}

func lobbyCreateQuery(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, session Session, params SessionParameters) (string, error) {

	qparts := []string{
		"+label.open:T",
		"+label.lobby_type:unassigned",
		fmt.Sprintf("+label.broadcaster.group_ids:/(%s)/", params.GroupID.String()),
		fmt.Sprintf("+label.broadcaster.regions:%s", params.Region.String()),
		fmt.Sprintf("+label.broadcaster.version_lock:%s", params.VersionLock),
	}

	if len(params.RequiredFeatures) > 0 {
		for _, f := range params.RequiredFeatures {
			qparts = append(qparts, fmt.Sprintf("+label.broadcaster.features:%s", f))
		}
	}

	query := strings.Join(qparts, " ")
	return query, nil
}

func lobbyCreateSortOptions(labels []*MatchLabel, labelLatencies []int, params SessionParameters) {
	// Sort the labels by latency
	sort.SliceStable(labels, func(i, j int) bool {
		// Sort by region first
		a := labels[i].Broadcaster
		b := labels[j].Broadcaster

		if a.IsPriorityFor(params.Mode) && !b.IsPriorityFor(params.Mode) {
			return true
		}
		if a.IsPriorityFor(params.Mode) && !b.IsPriorityFor(params.Mode) {
			return false
		}

		if slices.Contains(a.Regions, params.Region) && !slices.Contains(b.Regions, params.Region) {
			return true
		}
		if !slices.Contains(a.Regions, params.Region) && slices.Contains(b.Regions, params.Region) {
			return false
		}

		if labelLatencies[i] == 0 && labelLatencies[j] != 0 {
			return false
		}

		return LatencyCmp(labelLatencies[i], labelLatencies[j], 10)
	})
}
