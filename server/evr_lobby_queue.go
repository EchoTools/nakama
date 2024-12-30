package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrNoUnfilledMatches  = status.Errorf(codes.NotFound, "No unfilled matches found with enough open slots")
	ErrMatchDuplicateJoin = errors.New("Match already joined")
	ErrMatchRoleMismatch  = errors.New("Role alignment mismatch")
)

type LobbyQueue struct {
	ctx             context.Context
	logger          *zap.Logger
	matchRegistry   MatchRegistry
	sessionRegistry SessionRegistry
	tracker         Tracker
	metrics         Metrics

	db *sql.DB
	nk runtime.NakamaModule

	createSocialMu *sync.Mutex

	cacheMu    *sync.RWMutex
	labelCache map[MatchID]*MatchLabel
	matchMus   *MapOf[MatchID, *sync.Mutex]
}

func NewLobbyQueue(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, metrics Metrics, matchRegistry MatchRegistry, tracker Tracker, sessionRegistry SessionRegistry) *LobbyQueue {
	q := &LobbyQueue{
		ctx:    ctx,
		logger: logger,

		matchRegistry:   matchRegistry,
		metrics:         metrics,
		tracker:         tracker,
		sessionRegistry: sessionRegistry,

		nk: nk,
		db: db,

		createSocialMu: &sync.Mutex{},
		cacheMu:        &sync.RWMutex{},

		labelCache: make(map[MatchID]*MatchLabel),

		matchMus: &MapOf[MatchID, *sync.Mutex]{},
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

				q.cacheMu.Lock()

				// Find unfilled matches
				labels, err = q.listUnfilledMatches(ctx)
				if err != nil {
					logger.Error("Failed to find unfilled matches", zap.Error(err))
					q.cacheMu.Unlock()
					continue
				}
				for i := 0; i < len(labels); i++ {
					if labels[i].OpenPlayerSlots() <= 0 {
						labels = append(labels[:i], labels[i+1:]...)
						i--
					}
				}
				labelCache := make(map[MatchID]*MatchLabel)
				for _, label := range labels {
					labelCache[label.ID] = label
				}

				q.labelCache = labelCache

				q.matchMus.Range(func(k MatchID, v *sync.Mutex) bool {
					if _, ok := labelCache[k]; !ok {
						q.matchMus.Delete(k)
					}
					return true
				})

				q.cacheMu.Unlock()
			}
		}
	}()

	return q
}

func (q *LobbyQueue) listUnfilledMatches(ctx context.Context) ([]*MatchLabel, error) {
	minSize := 0
	maxSize := MatchLobbyMaxSize
	limit := 100
	query := "+label.open:T +label.mode:/(echo_arena|social_2.0|echo_combat)/"

	// Search for possible matches
	matches, err := listMatches(ctx, q.nk, limit, minSize+1, maxSize+1, query)
	if err != nil {
		return nil, fmt.Errorf("Failed to list matches: %v", err)
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

func rttByPlayerByExtIP(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, groupID string) (map[string]map[string]int, error) {
	qparts := []string{
		fmt.Sprintf("+label.broadcaster.group_ids:/(%s)/", Query.Escape(groupID)),
	}

	query := strings.Join(qparts, " ")

	pubLabels, err := lobbyListLabels(ctx, nk, query)
	if err != nil {
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
