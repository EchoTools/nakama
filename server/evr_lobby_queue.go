package server

import (
	"context"
	"encoding/json"
	"sort"
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

	nk runtime.NakamaModule

	unfilledLobbies map[LobbyQueuePresence][]*MatchLabel
}

func NewLobbyQueue(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, matchRegistry MatchRegistry) *LobbyQueue {
	q := &LobbyQueue{
		ctx:           ctx,
		logger:        logger,
		matchRegistry: matchRegistry,

		nk: nk,
	}

	go func() {
		var labels []*MatchLabel
		var err error
		queueTicker := time.NewTicker(5 * time.Second)
		for {
			select {
			case <-ctx.Done():
				return
			case <-queueTicker.C:
				labels, err = q.FindUnfilledMatches(ctx)
				if err != nil {
					logger.Error("Failed to find unfilled matches", zap.Error(err))
					continue
				}
				q.Lock()
				// Rebuild the unfilled lobbies map
				q.unfilledLobbies = make(map[LobbyQueuePresence][]*MatchLabel)
				for _, label := range labels {
					presence := LobbyQueuePresence{
						GroupID:     label.GetGroupID(),
						VersionLock: label.Broadcaster.VersionLock,
						Mode:        label.Mode,
					}

					if _, ok := q.unfilledLobbies[presence]; !ok {
						q.unfilledLobbies[presence] = make([]*MatchLabel, 0)
					}

					q.unfilledLobbies[presence] = append(q.unfilledLobbies[presence], label)
				}
				q.Unlock()
			}
		}
	}()

	return q
}

func (q *LobbyQueue) FindUnfilledMatches(ctx context.Context) ([]*MatchLabel, error) {
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

func (q *LobbyQueue) GetUnfilledMatch(ctx context.Context, params *LobbySessionParameters) (*MatchLabel, error) {
	q.Lock()
	defer q.Unlock()

	presence := LobbyQueuePresence{
		GroupID:     params.GroupID,
		VersionLock: params.VersionLock,
		Mode:        params.Mode,
	}

	var labels []*MatchLabel

	for _, label := range q.unfilledLobbies[presence] {
		if label.ID == params.CurrentMatchID {
			continue
		}
		labels = append(labels, label)
	}

	labelsWithLatency := params.latencyHistory.LabelsByAverageRTT(labels)

	if len(labelsWithLatency) == 0 {
		return nil, ErrNoUnfilledMatches
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
		if l.Label.OpenPlayerSlots() >= params.PartySize {
			l.Label.PlayerCount += params.PartySize
			return l.Label, nil
		}
	}

	return nil, ErrNoUnfilledMatches
}
