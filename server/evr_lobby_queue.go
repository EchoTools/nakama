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

const (
	MatchReservationExpiry = 5 * time.Second
)

var (
	ErrNoUnfilledMatches  = status.Errorf(codes.NotFound, "No unfilled matches found with enough open slots")
	ErrMatchDuplicateJoin = errors.New("Match already joined")
	ErrMatchRoleMismatch  = errors.New("Role alignment mismatch")
)

type LobbyQueue struct {
	ctx           context.Context
	logger        *zap.Logger
	matchRegistry MatchRegistry
	metrics       Metrics

	db *sql.DB
	nk runtime.NakamaModule

	createSocialMu *sync.Mutex

	cacheMu    *sync.RWMutex
	labelCache map[MatchID]*MatchLabel
	matchMus   *MapOf[MatchID, *sync.Mutex]
}

func NewLobbyQueue(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, metrics Metrics, matchRegistry MatchRegistry) *LobbyQueue {
	q := &LobbyQueue{
		ctx:    ctx,
		logger: logger,

		matchRegistry: matchRegistry,
		metrics:       metrics,
		nk:            nk,
		db:            db,

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

func (q *LobbyQueue) FindBackfill(ctx context.Context, logger *zap.Logger, params *LobbySessionParameters, entrants []*EvrMatchPresence) (*MatchLabel, int, error) {
	if len(entrants) == 0 {
		return nil, -1, ErrNoEntrants
	}

	partySize := len(entrants)

	// Find the labels that have enough open slots for the party

	q.cacheMu.RLock()
	labels := make([]*MatchLabel, 0, len(q.labelCache))
	for _, label := range q.labelCache {
		// Skip the current match (this is critical for "New Lobby" from the pause menu)
		if label == nil || label.ID == params.CurrentMatchID {
			continue
		}

		// Skip the match if it doesn't have enough open slots for the party
		if label.OpenPlayerSlots() < partySize {
			continue
		}

		// Skip the match if it's not the same group and mode
		if label.GetGroupID() != params.GroupID || label.Mode != params.Mode {
			continue
		}

		labels = append(labels, label)
	}
	q.cacheMu.RUnlock()

	var err error

	switch {
	case params.Mode == evr.ModeSocialPublic:
		leastFirst := true
		labels = q.sortLabelsByPopulation(labels, leastFirst)

		// Create a new lobby if none were found
		if len(labels) == 0 {
			if q.createSocialMu.TryLock() {
				go func() {
					<-time.After(5 * time.Second)
					q.createSocialMu.Unlock()
				}()
				l, err := q.NewSocialLobby(ctx, params.VersionLock, params.GroupID)
				if err != nil {
					return nil, -1, err
				}
				q.cacheMu.Lock()
				q.labelCache[l.ID] = l
				q.cacheMu.Unlock()
				return l, evr.TeamSocial, nil
			}
		}
		if err != nil {
			return nil, -1, err
		}
	default:
		labels = q.sortLabelsByRTT(params.latencyHistory, labels)
	}

OuterLoop:
	for _, label := range labels {

		// Lock the joins from the backfill queue for this match
		mu, _ := q.matchMus.LoadOrStore(label.ID, &sync.Mutex{})
		mu.Lock()
		defer mu.Unlock()

		// Get the latest label
		label, err := MatchLabelByID(ctx, q.nk, label.ID)
		if err != nil {
			logger.Warn("Failed to get the latest label", zap.String("mid", label.ID.String()), zap.Error(err))
			continue
		}

		// Update the label cache
		q.cacheMu.Lock()
		q.labelCache[label.ID] = label
		q.cacheMu.Unlock()

		var team int

		if params.Mode == evr.ModeSocialPublic {
			team = evr.TeamSocial
		} else if label.OpenSlotsByRole(evr.TeamBlue) >= partySize {
			team = evr.TeamBlue
		} else if label.OpenSlotsByRole(evr.TeamOrange) >= partySize {
			team = evr.TeamOrange
		}

		// Final check if the label has enough open player slots for the team
		if label.OpenSlotsByRole(team) < partySize {
			logger.With(
				zap.String("mid",
					label.ID.String()),
				zap.Int("open_slots",
					label.OpenPlayerSlots()),
				zap.Int("party_size", partySize),
				zap.Int("team", team),
			).Warn("Label does not have enough open slots for the team")
			continue
		}

		// Set the entrant role to the team
		for _, entrant := range entrants {
			entrant.RoleAlignment = team
		}

		// Reserve the slots until the join is complete
		if err := q.matchJoinPartial(ctx, label, entrants); err != nil {
			logger.Warn("Failed to reserve slots", zap.Any("label", label), zap.Int("party_size", partySize), zap.Error(err))
			continue OuterLoop
		}

		return label, team, nil
	}

	return nil, -1, ErrNoUnfilledMatches
}

func (q *LobbyQueue) sortLabelsByPopulation(labels []*MatchLabel, leastFirst bool) []*MatchLabel {

	// Sort the labels by open slots
	slices.SortStableFunc(labels, func(a, b *MatchLabel) int {
		return a.OpenPlayerSlots() - b.OpenPlayerSlots()
	})

	// Reverse the order
	slices.Reverse(labels)

	return labels
}

func (q *LobbyQueue) sortLabelsByRTT(latencyHistory LatencyHistory, labels []*MatchLabel) []*MatchLabel {
	// if it's an arena/combat lobby, sort by size, then latency
	labelsWithLatency := latencyHistory.LabelsByAverageRTT(labels)

	// Sort the labels by size, then latency, putting the largest, lowest latency first
	sort.SliceStable(labelsWithLatency, func(i, j int) bool {
		// If the open slots are the same, sort by latency
		return labelsWithLatency[i].RTT < labelsWithLatency[j].RTT
	})

	labels = make([]*MatchLabel, 0, len(labelsWithLatency))
	for _, ll := range labelsWithLatency {
		labels = append(labels, ll.Label)
	}

	return labels
}

func (q *LobbyQueue) matchJoinPartial(ctx context.Context, label *MatchLabel, entrants []*EvrMatchPresence) error {

	for _, e := range entrants {
		var err error
		metadata := EntrantMetadata{Presence: *e}.MarshalMap()

		found, allowed, isNew, reason, labelStr, _ := q.matchRegistry.JoinAttempt(ctx, label.ID.UUID, label.ID.Node, e.UserID, e.SessionID, e.Username, e.SessionExpiry, nil, e.ClientIP, e.ClientPort, label.ID.Node, metadata)
		if !found {
			err = ErrMatchNotFound
		} else if labelStr == "" {
			err = fmt.Errorf("failed to get match label")
		} else if !allowed {
			err = fmt.Errorf("not allowed to join match: %s", reason)
		} else if !isNew {
			err = ErrMatchDuplicateJoin
		}
		if err != nil {
			return fmt.Errorf("failed to join match %w", err)
		}

		presence := &EvrMatchPresence{}
		if err := json.Unmarshal([]byte(reason), &e); err != nil {
			return fmt.Errorf("failed to unmarshal match presence", err)
		}

		if presence.RoleAlignment != e.RoleAlignment {
			return fmt.Errorf("role alignment mismatch")
		}
	}

	return nil
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
