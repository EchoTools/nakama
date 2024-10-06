package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrNoUnfilledMatches = status.Errorf(codes.NotFound, "No unfilled matches found with enough open slots")
)

type LobbyQueuePresence struct {
	GroupID     uuid.UUID
	VersionLock evr.Symbol
	Mode        evr.Symbol
}

type LobbyQueue struct {
	sync.RWMutex
	ctx           context.Context
	logger        *zap.Logger
	matchRegistry MatchRegistry
	metrics       Metrics

	db       *sql.DB
	nk       runtime.NakamaModule
	createMu sync.Mutex
	cache    map[LobbyQueuePresence]map[MatchID]*MatchLabel
}

func NewLobbyQueue(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, metrics Metrics, matchRegistry MatchRegistry) *LobbyQueue {
	q := &LobbyQueue{
		ctx:    ctx,
		logger: logger,

		matchRegistry: matchRegistry,
		metrics:       metrics,
		nk:            nk,
		db:            db,
	}

	go func() {
		var labels []*MatchLabel
		var err error
		queueTicker := time.NewTicker(3 * time.Second)
		for {
			select {
			case <-ctx.Done():
				return
			case <-queueTicker.C:
				labels, err = q.findUnfilledMatches(ctx)
				if err != nil {
					logger.Error("Failed to find unfilled matches", zap.Error(err))
					continue
				}
				q.Lock()
				// Rebuild the unfilled lobbies map
				q.cache = make(map[LobbyQueuePresence]map[MatchID]*MatchLabel)
				for _, label := range labels {
					presence := LobbyQueuePresence{
						GroupID: label.GetGroupID(),
						//VersionLock: label.Broadcaster.VersionLock,
						Mode: label.Mode,
					}

					if _, ok := q.cache[presence]; !ok {
						q.cache[presence] = make(map[MatchID]*MatchLabel)
					}
					q.cache[presence][label.ID] = label

				}
				q.Unlock()
			}
		}
	}()

	return q
}

func (q *LobbyQueue) findUnfilledMatches(ctx context.Context) ([]*MatchLabel, error) {
	minSize := 0
	maxSize := MatchLobbyMaxSize
	limit := 100
	query := "+label.open:T +label.mode:/(echo_arena|social_2.0|echo_combat)/"

	// Search for possible matches
	matches, err := listMatches(ctx, q.nk, limit, minSize+1, maxSize+1, query)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to find matches: %v", err)
	}

	// Create a label slice of the matches
	labels := make([]*MatchLabel, 0, len(matches))
	for _, match := range matches {
		label := &MatchLabel{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
			continue
		}
		labels = append(labels, label)
	}

	return labels, nil
}

func (q *LobbyQueue) GetUnfilledMatch(ctx context.Context, params *LobbySessionParameters) (*MatchLabel, int, error) {
	q.Lock()
	defer q.Unlock()

	presence := LobbyQueuePresence{
		GroupID: params.GroupID,
		//VersionLock: params.VersionLock,
		Mode: params.Mode,
	}

	var labels []*MatchLabel

	for _, label := range q.cache[presence] {
		if label.ID == params.CurrentMatchID {
			continue
		}
		labels = append(labels, label)
	}

	// If it's a social Lobby, just sort by least open slots
	if params.Mode == evr.ModeSocialPublic {
		slices.SortStableFunc(labels, func(a, b *MatchLabel) int {
			return a.OpenPlayerSlots() - b.OpenPlayerSlots()
		})

		// Find one that has enough slots
		for _, l := range labels {
			if l.OpenPlayerSlots() >= params.PartySize {
				l.PlayerCount += params.PartySize
				return l, evr.TeamSocial, nil
			}
		}
		// Create a new one.
		if q.createMu.TryLock() {
			go func() {
				<-time.After(5 * time.Second)
				q.createMu.Unlock()
			}()
			l, err := q.NewSocialLobby(ctx, params.VersionLock, params.GroupID)
			if err != nil {
				return nil, -1, err
			}
			return l, evr.TeamSocial, nil
		}
		return nil, -1, ErrNoUnfilledMatches
	}

	labelsWithLatency := params.latencyHistory.LabelsByAverageRTT(labels)

	if len(labelsWithLatency) == 0 {
		return nil, -1, ErrNoUnfilledMatches
	}

	// Sort the labels by size, then latency, putting the largest, lowest latency first
	sort.SliceStable(labelsWithLatency, func(i, j int) bool {
		if labelsWithLatency[i].Label.OpenPlayerSlots() > labelsWithLatency[j].Label.OpenPlayerSlots() {
			return false
		} else if labelsWithLatency[i].Label.OpenPlayerSlots() < labelsWithLatency[j].Label.OpenPlayerSlots() {
			return true
		}

		return labelsWithLatency[i].RTT < labelsWithLatency[j].RTT
	})

	for _, l := range labelsWithLatency {
		var team int

		if l.Label.OpenSlotsByRole(evr.TeamBlue) >= params.PartySize {
			team = evr.TeamBlue
		} else if l.Label.OpenSlotsByRole(evr.TeamOrange) >= params.PartySize {
			team = evr.TeamOrange
		} else {
			continue
		}

		label, err := MatchLabelByID(ctx, q.nk, l.Label.ID)
		if err != nil {
			continue
		} else if label == nil {
			continue
		}

		// Final check
		if label.RoleCount(team) >= label.RoleLimit(team) {
			continue
		}

		label.PlayerCount += params.PartySize
		q.cache[presence][label.ID] = label

		return label, team, nil

	}

	return nil, -1, ErrNoUnfilledMatches
}

type TeamAlignments map[string]int // map[UserID]Role

func (q *LobbyQueue) NewSocialLobby(ctx context.Context, versionLock evr.Symbol, groupID uuid.UUID) (*MatchLabel, error) {
	metricsTags := map[string]string{
		"version_lock": versionLock.String(),
		"group_id":     groupID.String(),
	}

	q.metrics.CustomCounter("lobby_create_social", metricsTags, 1)
	nk := q.nk
	matchRegistry := q.matchRegistry
	logger := q.logger

	qparts := []string{
		"+label.open:T",
		"+label.lobby_type:unassigned",
		"+label.broadcaster.regions:/(default)/",
		fmt.Sprintf("+label.broadcaster.group_ids:/(%s)/", Query.Escape(groupID.String())),
		//fmt.Sprintf("+label.broadcaster.version_lock:%s", versionLock.String()),
	}

	query := strings.Join(qparts, " ")

	labels, err := lobbyListGameServers(ctx, nk, query)
	if err != nil {
		logger.Warn("Failed to list game servers", zap.Any("query", query), zap.Error(err))
		return nil, err
	}

	// Get the latency history of all online pub players
	// Find server(s) that the most number of players have !999 (or 0) ping to
	// sort by servers that have <250 ping to all players
	// Find the with the best average ping to the most nubmer of players
	label := &MatchLabel{}

	rttByPlayerByExtIP, err := rttByPlayerByExtIP(ctx, logger, q.db, nk, groupID.String())
	if err != nil {
		logger.Warn("Failed to get RTT by player by extIP", zap.Error(err))
	} else {
		extIPs := sortByGreatestPlayerAvailability(rttByPlayerByExtIP)
		for _, extIP := range extIPs {
			for _, l := range labels {
				if l.Broadcaster.Endpoint.GetExternalIP() == extIP {
					label = l
					break
				}
			}
		}
	}

	// If no label was found, just pick a random one
	if label.ID.IsNil() {
		label = labels[rand.Intn(len(labels))]
	}

	if err := lobbyPrepareSession(ctx, logger, matchRegistry, label.ID, evr.ModeSocialPublic, evr.LevelSocial, uuid.Nil, groupID, TeamAlignments{}, time.Now().UTC()); err != nil {
		logger.Error("Failed to prepare session", zap.Error(err), zap.String("mid", label.ID.UUID.String()))
		return nil, err
	}

	match, _, err := matchRegistry.GetMatch(ctx, label.ID.String())
	if err != nil {
		return nil, errors.Join(NewLobbyErrorf(InternalError, "failed to get match"), err)
	} else if match == nil {
		logger.Warn("Match not found", zap.String("mid", label.ID.UUID.String()))
		return nil, ErrMatchNotFound
	}

	label = &MatchLabel{}
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
		return nil, errors.Join(NewLobbyError(InternalError, "failed to unmarshal match label"), err)
	}
	return label, nil

}

func lobbyPrepareSession(ctx context.Context, logger *zap.Logger, matchRegistry MatchRegistry, matchID MatchID, mode, level evr.Symbol, spawnedBy uuid.UUID, groupID uuid.UUID, teamAlignments TeamAlignments, startTime time.Time) error {
	label := &MatchLabel{
		ID:             matchID,
		Mode:           mode,
		Level:          level,
		SpawnedBy:      spawnedBy.String(),
		GroupID:        &groupID,
		TeamAlignments: teamAlignments,
		StartTime:      startTime,
	}
	response, err := SignalMatch(ctx, matchRegistry, matchID, SignalPrepareSession, label)
	if err != nil {
		return errors.Join(ErrMatchmakingUnknownError, fmt.Errorf("failed to prepare session `%s`: %s", label.ID.String(), response))
	}
	return nil
}

func rttByPlayerByExtIP(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, groupID string) (map[string]map[string]int, error) {
	qparts := []string{
		"+label.lobby_type:public",
		fmt.Sprintf("+label.broadcaster.group_ids:/(%s)/", Query.Escape(groupID)),
	}

	query := strings.Join(qparts, " ")

	pubLabels, err := lobbyListLabels(ctx, nk, query)
	if err != nil {
		logger.Warn("Failed to list game servers", zap.Any("query", query), zap.Error(err))
		return nil, err
	}

	rttByPlayerByExtIP := make(map[string]map[string]int)

	for _, label := range pubLabels {
		for _, p := range label.Players {
			history, err := LoadLatencyHistory(ctx, logger, db, uuid.FromStringOrNil(p.UserID))
			if err != nil {
				logger.Warn("Failed to load latency history", zap.Error(err))
				continue
			}
			rtts := history.LatestRTTs()
			for extIP, rtt := range rtts {
				if _, ok := rttByPlayerByExtIP[p.UserID]; !ok {
					rttByPlayerByExtIP[p.UserID] = make(map[string]int)
				}
				rttByPlayerByExtIP[p.UserID][extIP] = rtt
			}
		}
	}

	return rttByPlayerByExtIP, nil
}

func sortByGreatestPlayerAvailability(rttByPlayerByExtIP map[string]map[string]int) []string {

	maxPlayerCount := 0
	extIPsByAverageRTT := make(map[string]int)
	extIPsByPlayerCount := make(map[string]int)
	for extIP, players := range rttByPlayerByExtIP {
		extIPsByPlayerCount[extIP] += len(players)
		if len(players) > maxPlayerCount {
			maxPlayerCount = len(players)
		}

		averageRTT := 0
		for _, rtt := range players {
			averageRTT += rtt
		}
		averageRTT /= len(players)
	}

	// Sort by greatest player availability
	extIPs := make([]string, 0, len(extIPsByPlayerCount))
	for extIP := range extIPsByPlayerCount {
		extIPs = append(extIPs, extIP)
	}

	sort.SliceStable(extIPs, func(i, j int) bool {
		// Sort by player count first
		if extIPsByPlayerCount[extIPs[i]] > extIPsByPlayerCount[extIPs[j]] {
			return true
		} else if extIPsByPlayerCount[extIPs[i]] < extIPsByPlayerCount[extIPs[j]] {
			return false
		}

		// If the player count is the same, sort by RTT
		if extIPsByAverageRTT[extIPs[i]] < extIPsByAverageRTT[extIPs[j]] {
			return true
		}
		return false
	})

	return extIPs
}
