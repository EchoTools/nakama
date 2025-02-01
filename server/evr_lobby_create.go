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
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

func (p *EvrPipeline) lobbyCreate(ctx context.Context, logger *zap.Logger, session Session, params *LobbySessionParameters) (MatchID, error) {
	nk := p.runtimeModule
	db := p.db

	// Do authorization checks related to the guild.
	if err := p.lobbyAuthorize(ctx, logger, session, params); err != nil {
		logger.Warn("Failed to authorize create session request", zap.Error(err))
		return MatchID{}, err
	}

	query, err := lobbyCreateQuery(ctx, logger, db, nk, session, params)
	if err != nil {
		return MatchID{}, fmt.Errorf("failed to build query: %w", err)
	}
	logger.Debug("Create query", zap.String("query", query))
	labels, err := lobbyListGameServers(ctx, nk, query)
	if err != nil {
		return MatchID{}, err
	}

	if len(labels) == 0 {
		logger.Warn("No available servers for creation", zap.String("query", query))
		return MatchID{}, ErrMatchmakingNoAvailableServers
	}

	labelRTTs := params.latencyHistory.LabelsByAverageRTT(labels)
	labelLatencies := make([]int, len(labels))

	labels = make([]*MatchLabel, len(labelRTTs))
	for i, l := range labelRTTs {
		labels[i] = l.Label
		labelLatencies[i] = l.RTT
	}

	// Sort By Priority, Region, then latency
	lobbyCreateSortOptions(labels, labelLatencies, params)

	label := labels[0]

	settings := MatchSettings{
		Mode:             params.Mode,
		Level:            params.Level,
		GroupID:          params.GroupID,
		RequiredFeatures: params.RequiredFeatures,
		StartTime:        time.Now().UTC(),
		TeamAlignments:   map[string]int{session.UserID().String(): params.Role},
		SpawnedBy:        session.UserID().String(),
	}

	matchID := label.ID
	_, err = LobbyPrepareSession(ctx, nk, matchID, &settings)
	if err != nil {
		logger.Warn("Failed to prepare session", zap.Error(err), zap.String("mid", matchID.UUID.String()))

		if strings.Contains("bad request:", err.Error()) {
			err = NewLobbyErrorf(BadRequest, "required features not supported")
		}

		return MatchID{}, err
	}

	// Return the prepared session
	return matchID, nil
}

func lobbyListGameServers(ctx context.Context, nk runtime.NakamaModule, query string) ([]*MatchLabel, error) {
	limit := 200
	minSize, maxSize := 1, 1 // the game server counts as one.
	matches, err := listMatches(ctx, nk, limit, minSize, maxSize, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list matches: %w", err)
	}

	if len(matches) == 0 {
		return nil, ErrMatchmakingNoAvailableServers
	}

	labels := make([]*MatchLabel, 0, len(matches))
	for _, match := range matches {
		label := &MatchLabel{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
			return nil, fmt.Errorf("failed to unmarshal match label: %w", err)
		}
		labels = append(labels, label)
	}
	return labels, nil
}

func lobbyListLabels(ctx context.Context, nk runtime.NakamaModule, query string) ([]*MatchLabel, error) {
	limit := 200
	minSize, maxSize := 1, MatchLobbyMaxSize // the game server counts as one.
	matches, err := listMatches(ctx, nk, limit, minSize, maxSize, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list matches: %w", err)
	}

	if len(matches) == 0 {
		return nil, ErrMatchmakingNoAvailableServers
	}

	labels := make([]*MatchLabel, 0, len(matches))
	for _, match := range matches {
		label := &MatchLabel{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
			return nil, fmt.Errorf("failed to unmarshal match label: %w", err)
		}
		labels = append(labels, label)
	}
	return labels, nil
}

func lobbyCreateQuery(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, session Session, params *LobbySessionParameters) (string, error) {

	regions := []string{params.Region.String(), evr.DefaultRegion.String()}
	qparts := []string{
		"+label.open:T",
		"+label.lobby_type:unassigned",
		fmt.Sprintf("+label.broadcaster.group_ids:/(%s)/", Query.Escape(params.GroupID.String())),
		fmt.Sprintf("+label.broadcaster.regions:/(%s)/", Query.Join(regions, "|")),
		//fmt.Sprintf("+label.broadcaster.version_lock:%s", params.VersionLock),
	}

	if len(params.RequiredFeatures) > 0 {
		for _, f := range params.RequiredFeatures {
			qparts = append(qparts, fmt.Sprintf("+label.broadcaster.features:/(%s)/", Query.Escape(f)))
		}
	}

	query := strings.Join(qparts, " ")
	return query, nil
}

func lobbyCreateSortOptions(labels []*MatchLabel, labelLatencies []int, params *LobbySessionParameters) {
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
