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
	"go.uber.org/atomic"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	MatchReservationExpiry = 5 * time.Second
)

var (
	ErrNoUnfilledMatches = status.Errorf(codes.NotFound, "No unfilled matches found with enough open slots")
)

type LobbyQueuePresence struct {
	GroupID     uuid.UUID
	VersionLock evr.Symbol
	Mode        evr.Symbol
}

type SlotReservation struct {
	Expiry time.Time
	Slots  int
}

type LobbyQueue struct {
	ctx           context.Context
	logger        *zap.Logger
	matchRegistry MatchRegistry
	metrics       Metrics

	db *sql.DB
	nk runtime.NakamaModule

	createSocialMu *sync.Mutex

	reservationsMu       *sync.Mutex
	reservationsMap      map[MatchID][]SlotReservation
	openSlotsByMatchRead *atomic.Value // map[MatchID]int
	labelRead            *atomic.Value // map[LobbyQueuePresence]map[MatchID]*MatchLabel
}

func NewLobbyQueue(ctx context.Context, logger *zap.Logger, db *sql.DB, nk runtime.NakamaModule, metrics Metrics, matchRegistry MatchRegistry) *LobbyQueue {
	q := &LobbyQueue{
		ctx:    ctx,
		logger: logger,

		matchRegistry: matchRegistry,
		metrics:       metrics,
		nk:            nk,
		db:            db,

		createSocialMu:       &sync.Mutex{},
		reservationsMu:       &sync.Mutex{},
		reservationsMap:      make(map[MatchID][]SlotReservation),
		labelRead:            &atomic.Value{},
		openSlotsByMatchRead: &atomic.Value{},
	}

	q.labelRead.Store(make(map[LobbyQueuePresence]map[MatchID]*MatchLabel))
	q.openSlotsByMatchRead.Store(make(map[MatchID]int))

	go func() {
		var labels []*MatchLabel
		var err error
		queueTicker := time.NewTicker(3 * time.Second)
		for {
			select {
			case <-ctx.Done():
				return
			case <-queueTicker.C:
				// Find unfilled matches
				labels, err = q.findUnfilledMatches(ctx)
				if err != nil {
					logger.Error("Failed to find unfilled matches", zap.Error(err))
					continue
				}

				// Rebuild the unfilled lobbies label map
				labelsByPresence := make(map[LobbyQueuePresence]map[MatchID]*MatchLabel)
				matchIDs := make(map[MatchID]struct{})

				for _, label := range labels {
					presence := LobbyQueuePresence{
						GroupID: label.GetGroupID(),
						//VersionLock: label.Broadcaster.VersionLock,
						Mode: label.Mode,
					}

					if _, ok := labelsByPresence[presence]; !ok {
						labelsByPresence[presence] = make(map[MatchID]*MatchLabel)
					}
					labelsByPresence[presence][label.ID] = label
					matchIDs[label.ID] = struct{}{}
				}
				q.labelRead.Store(labelsByPresence)

				// Generate a new open slot map
				q.reservationsMu.Lock()

				openSlotsByMatch := make(map[MatchID]int, len(labels))
				for _, label := range labels {
					reserved := 0
					if n, ok := q.reservationsMap[label.ID]; ok {
						for _, n := range n {
							if n.Expiry.Before(time.Now()) {
								continue
							}
							reserved += n.Slots
						}
					}
					openSlotsByMatch[label.ID] = label.OpenPlayerSlots() - reserved
					if openSlotsByMatch[label.ID] < 0 {
						openSlotsByMatch[label.ID] = 0
					}
				}

				// Remove any reservations for matches that are no longer in the queue
				reservationMap := make(map[MatchID][]SlotReservation, len(q.reservationsMap))
				for matchID, reservations := range q.reservationsMap {
					if _, ok := matchIDs[matchID]; ok {
						reservationMap[matchID] = reservations
					}
				}

				// Update the maps
				q.reservationsMap = reservationMap
				q.openSlotsByMatchRead.Store(openSlotsByMatch)
				q.reservationsMu.Unlock()
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

func (q *LobbyQueue) openSlotsByMatch() map[MatchID]int {
	openSlots := q.openSlotsByMatchRead.Load().(map[MatchID]int)
	return openSlots
}

func (q *LobbyQueue) labelsByPresenceByOpenSlots(presence LobbyQueuePresence, slots int) map[MatchID]*MatchLabel {
	byPresence := q.labelRead.Load().(map[LobbyQueuePresence]map[MatchID]*MatchLabel)
	labels, ok := byPresence[presence]
	if !ok {
		return nil
	}

	openSlots := q.openSlotsByMatch()

	resultMap := make(map[MatchID]*MatchLabel)
	for _, label := range labels {
		if openSlots[label.ID] >= slots {
			resultMap[label.ID] = label
		}
	}

	return resultMap
}

func (q *LobbyQueue) GetUnfilledMatch(ctx context.Context, logger *zap.Logger, params *LobbySessionParameters) (*MatchLabel, int, error) {

	partySize := params.GetPartySize()
	if partySize < 1 {
		q.logger.Warn("Party size must be at least 1")
		partySize = 1
	}

	presence := LobbyQueuePresence{
		GroupID: params.GroupID,
		//VersionLock: params.VersionLock,
		Mode: params.Mode,
	}

	labels := make([]*MatchLabel, 0)
	for _, label := range q.labelsByPresenceByOpenSlots(presence, partySize) {
		// Skip the current match (this is critical for "New Lobby" from the pause menu)
		if label == nil || label.ID == params.CurrentMatchID {
			continue
		}
		labels = append(labels, label)
	}

	// Handle social lobbies separately
	switch {
	case params.Mode == evr.ModeSocialPublic:
		return q.SelectSocial(ctx, logger, params, labels, partySize)
	default:
		return q.SelectArena(ctx, logger, params, labels, partySize)
	}
}

func (q *LobbyQueue) SelectArena(ctx context.Context, logger *zap.Logger, params *LobbySessionParameters, labels []*MatchLabel, partySize int) (*MatchLabel, int, error) {
	// if it's an arena/combat lobby, sort by size, then latency
	labelsWithLatency := params.latencyHistory.LabelsByAverageRTT(labels)

	if len(labelsWithLatency) == 0 {
		return nil, -1, ErrNoUnfilledMatches
	}

	openSlotsByMatch := q.openSlotsByMatch()

	// Sort the labels by size, then latency, putting the largest, lowest latency first
	sort.SliceStable(labelsWithLatency, func(i, j int) bool {
		a := labelsWithLatency[i]
		b := labelsWithLatency[j]

		// Sort by open slots (least open slots first)
		if openSlotsByMatch[a.Label.ID] > openSlotsByMatch[b.Label.ID] {
			return false
		}

		if openSlotsByMatch[a.Label.ID] < openSlotsByMatch[b.Label.ID] {
			return true
		}

		// If the open slots are the same, sort by latency
		return a.RTT < b.RTT
	})

	for _, ll := range labelsWithLatency {
		var team int
		logger := logger.With(zap.String("mid", ll.Label.ID.String()), zap.Int("open_slots", openSlotsByMatch[ll.Label.ID]), zap.Int("party_size", partySize))

		// Get the latest label
		label, err := MatchLabelByID(ctx, q.nk, ll.Label.ID)
		if err != nil {
			if err == ErrMatchNotFound {
				continue
			}
			q.logger.Warn("Failed to get match label", zap.Error(err))
			continue
		}

		// Last-minute check if the label has enough open player slots
		if openSlotsByMatch[label.ID] < partySize || label.OpenPlayerSlots() < partySize {
			logger.Debug("Match is full", zap.Int("open_slots", openSlotsByMatch[label.ID]), zap.Int("party_size", partySize))
			continue
		}

		if label.OpenSlotsByRole(evr.TeamBlue) >= partySize {
			team = evr.TeamBlue
		} else if label.OpenSlotsByRole(evr.TeamOrange) >= partySize {
			team = evr.TeamOrange
		} else {
			// No team has enough open slots for the whole party
			logger.Warn("No team has enough open slots for the party", zap.Int("party_size", partySize))
			continue
		}

		// Final check if the label has enough open player slots for the team
		if label.RoleCount(team)+partySize > label.RoleLimit(team) {
			logger.Warn("Label does not have enough open slots for the team", zap.Int("party_size", partySize), zap.Int("team", team))
			continue
		}

		q.reserveSlots(label.ID, partySize)
		return label, team, nil

	}

	return nil, -1, ErrNoUnfilledMatches
}
func (q *LobbyQueue) SelectSocial(ctx context.Context, logger *zap.Logger, params *LobbySessionParameters, labels []*MatchLabel, partySize int) (*MatchLabel, int, error) {

	openSlotsByMatch := q.openSlotsByMatch()

	// Sort the labels by open slots
	slices.SortStableFunc(labels, func(a, b *MatchLabel) int {
		return openSlotsByMatch[a.ID] - openSlotsByMatch[b.ID]
	})

	// Reverse the order
	slices.Reverse(labels)

	// Find one that has enough slots
	for _, l := range labels {
		if openSlotsByMatch[l.ID] >= partySize {
			q.reserveSlots(l.ID, partySize)
			return l, evr.TeamSocial, nil
		}
	}
	// Create a new one.
	if q.createSocialMu.TryLock() {
		go func() {
			<-time.After(5 * time.Second)
			q.createSocialMu.Unlock()
		}()
		l, err := q.NewSocialLobby(ctx, params.VersionLock, params.GroupID)
		if err != nil {
			return nil, -1, err
		}

		q.reserveSlots(l.ID, partySize)
		return l, evr.TeamSocial, nil
	}
	return nil, -1, ErrNoUnfilledMatches

}

func (q *LobbyQueue) reserveSlots(matchID MatchID, n int) {
	q.reservationsMu.Lock()
	defer q.reservationsMu.Unlock()
	if _, ok := q.reservationsMap[matchID]; !ok {
		q.reservationsMap[matchID] = make([]SlotReservation, 0)
	}
	q.reservationsMap[matchID] = append(q.reservationsMap[matchID], SlotReservation{
		Expiry: time.Now().Add(MatchReservationExpiry),
		Slots:  n,
	})
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
