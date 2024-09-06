package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/ptr"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// Builds the match after the matchmaker has created it
type LobbyBuilder struct {
	sync.Mutex
	logger          *zap.Logger
	db              *sql.DB
	sessionRegistry SessionRegistry
	matchRegistry   MatchRegistry
	tracker         Tracker
	profileRegistry *ProfileRegistry
	metrics         Metrics
	mapQueue        map[evr.Symbol][]evr.Symbol // map[mode][]level
}

func NewLobbyBuilder(logger *zap.Logger, db *sql.DB, sessionRegistry SessionRegistry, matchRegistry MatchRegistry, tracker Tracker, metrics Metrics, profileRegistry *ProfileRegistry) *LobbyBuilder {
	logger = logger.With(zap.String("module", "lobby_builder"))

	return &LobbyBuilder{
		logger:          logger,
		db:              db,
		sessionRegistry: sessionRegistry,
		matchRegistry:   matchRegistry,
		tracker:         tracker,
		metrics:         metrics,
		profileRegistry: profileRegistry,
		mapQueue:        make(map[evr.Symbol][]evr.Symbol),
	}
}

func (b *LobbyBuilder) handleMatchedEntries(entries [][]*MatchmakerEntry) {
	// build matches one at a time.
	logger := b.logger.With(zap.Any("entries", entries))
	logger.Debug("Handling matched entries")

	for _, entrants := range entries {
		if err := b.buildMatch(b.logger, entrants); err != nil {
			logger.Error("Failed to build match", zap.Error(err))
			return
		}
	}
}

func (mr *LobbyBuilder) SortGameServerIPs(entrants []*MatchmakerEntry) []string {
	// Create a map of each endpoint and it's latencies to each entrant
	latencies := make(map[string][]int, 100)
	for _, e := range entrants {
		nprops := e.NumericProperties
		//sprops := e.StringProperties

		// loop over the number props and get the latencies
		for k, v := range nprops {
			if strings.HasPrefix(k, "rtt") {
				latencies[k] = append(latencies[k], int(v))
			}
		}
	}

	// Score each endpoint based on the latencies
	scored := make(map[string]int, len(latencies))
	for k, v := range latencies {
		if len(v) == 0 {
			scored[k] = 0
		}
		// Sort the latencies
		sort.Ints(v)
		// Get the average
		average := 0
		for _, i := range v {
			average += i
		}
		average /= len(v)
		scored[k] = average
	}
	// Sort the scored endpoints
	sorted := make([]string, 0, len(scored))
	for k := range scored {
		sorted = append(sorted, k)
	}
	sort.SliceStable(sorted, func(i, j int) bool {
		return scored[sorted[i]] < scored[sorted[j]]
	})
	return sorted
}

func (b *LobbyBuilder) GroupByTicket(entrants []*MatchmakerEntry) [][]*MatchmakerEntry {
	partyMap := make(map[string][]*MatchmakerEntry, 8)
	for _, e := range entrants {
		id := e.GetTicket()
		partyMap[id] = append(partyMap[id], e)
	}
	parties := make([][]*MatchmakerEntry, 0, len(partyMap))
	for _, v := range partyMap {
		parties = append(parties, v)
	}
	return parties
}

func (b *LobbyBuilder) buildMatch(logger *zap.Logger, entrants []*MatchmakerEntry) (err error) {
	// Build matches one at a time.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	logger = logger.With(zap.Int("entrants", len(entrants)))

	logger.Debug("Building match", zap.Any("entrants", entrants))

	// Verify that all entrants have the same group_id

	groupIDs := make([]uuid.UUID, 0, len(entrants))
	for _, e := range entrants {
		groupID := uuid.FromStringOrNil(e.StringProperties["group_id"])
		if groupID == uuid.Nil {
			continue
		}
		groupIDs = append(groupIDs, groupID)
	}
	if len(groupIDs) == 0 {
		logger.Warn("Entrants have no group_id")
	}

	groupIDs = CompactedFrequencySort(groupIDs, true)

	if len(groupIDs) == 0 {
		logger.Warn("Entrants have no group_id")
	} else if len(groupIDs) > 1 {
		logger.Warn("Entrants are not in the same group", zap.Any("groupIDs", groupIDs))
	}

	groupID := groupIDs[0]

	// Group the entrants by ticket
	presencesByTicket := b.GroupByTicket(entrants)
	teams := b.distributeParties(presencesByTicket)

	teamAlignments := make(TeamAlignments, len(teams))
	for i, players := range teams {
		for _, p := range players {
			teamAlignments[p.Presence.GetUserId()] = i
		}
	}

	gameServers := b.SortGameServerIPs(entrants)

	mode := evr.ToSymbol(entrants[0].StringProperties["mode"])

	level := b.selectNextMap(mode)
	start := true
	timeout := time.After(60 * time.Second)
	var matchID MatchID
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timeout:
			return ErrMatchmakingNoAvailableServers
		default:
		}
		matchID, err = b.allocateGameServer(ctx, logger, groupID, gameServers, mode, level, teamAlignments, start)
		if err != nil {
			logger.Error("Failed to allocate game server.", zap.Error(err))
			<-time.After(5 * time.Second)
			continue
		}
		break
	}

	entrantPresences := make([]*EvrMatchPresence, 0, len(entrants))
	for i, players := range teams {
		// Assign each player in the team to the match
		for _, entry := range players {

			session := b.sessionRegistry.Get(uuid.FromStringOrNil(entry.Presence.GetSessionId()))
			if session == nil {
				logger.Warn("Failed to get session from session registry", zap.String("sid", entry.Presence.GetSessionId()))
				continue
			}
			sessionCtx := session.Context()
			params, ok := LoadParams(sessionCtx)
			if !ok {
				logger.Warn("Failed to get session parameters from session context", zap.String("sid", entry.Presence.GetSessionId()))
				continue
			}

			metadata := params.AccountMetadata
			presence := &EvrMatchPresence{
				Node:           params.LoginSession.pipeline.node,
				UserID:         session.UserID(),
				SessionID:      session.ID(),
				LoginSessionID: params.LoginSession.ID(),
				Username:       session.Username(),
				DisplayName:    metadata.GetGroupDisplayNameOrDefault(groupID.String()),
				EvrID:          params.EvrID,
				PartyID:        uuid.FromStringOrNil(entry.StringProperties["party_id"]),
				RoleAlignment:  int(i),
				DiscordID:      params.DiscordID,
				ClientIP:       session.ClientIP(),
				ClientPort:     session.ClientPort(),
				IsPCVR:         params.IsPCVR,
				Rating: rating.NewWithOptions(&types.OpenSkillOptions{
					Mu:    ptr.Float64(entry.NumericProperties["rating_mu"]),
					Sigma: ptr.Float64(entry.NumericProperties["rating_sigma"]),
				}),
			}
			entrantPresences = append(entrantPresences, presence)
		}
	}
	tags := map[string]string{
		"mode":    mode.String(),
		"level":   level.String(),
		"groupID": groupID.String(),
	}

	successful := make([]*EvrMatchPresence, 0, len(entrants))
	errored := make([]*EvrMatchPresence, 0, len(entrants))
	for _, p := range entrantPresences {

		if err := LobbyJoinEntrants(ctx, logger, b.db, b.matchRegistry, b.sessionRegistry, b.tracker, b.profileRegistry, matchID, p.RoleAlignment, []*EvrMatchPresence{p}); err != nil {
			logger.Error("Failed to join entrant to match", zap.String("mid", matchID.UUID.String()), zap.String("uid", p.GetUserId()), zap.Error(err))
			errored = append(errored, p)
			continue
		}

		successful = append(successful, p)
	}

	match, _, err := b.matchRegistry.GetMatch(ctx, matchID.String())
	if err != nil {
		logger.Error("Failed to get match", zap.String("mid", matchID.UUID.String()), zap.Error(err))
	}

	label := MatchLabel{}
	if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), &label); err != nil {
		logger.Error("Failed to unmarshal match label", zap.String("mid", matchID.UUID.String()), zap.Error(err))
	}
	b.metrics.CustomCounter("lobby_join_match_made", tags, int64(len(successful)))
	b.metrics.CustomCounter("lobby_error_match_made", tags, int64(len(errored)))

	logger.Info("Match made", zap.Any("label", label), zap.String("mid", matchID.UUID.String()), zap.Any("teams", teams), zap.Any("successful", successful), zap.Any("errored", errored))
	return nil
}

func (b *LobbyBuilder) distributeParties(parties [][]*MatchmakerEntry) [][]*MatchmakerEntry {
	// Distribute the players from each party on the two teams.
	// Try to keep the parties together, but the teams must be balanced.
	// The algorithm is greedy and may not always produce the best result.
	// Each team must be 4 players or less
	teams := [][]*MatchmakerEntry{{}, {}}

	// Sort the parties by size, single players last
	sort.SliceStable(parties, func(i, j int) bool {
		if len(parties[i]) == 1 {
			return false
		}
		return len(parties[i]) < len(parties[j])
	})

	// Distribute the parties to the teams
	for _, party := range parties {
		// Find the team with the least players
		team := 0
		for i, t := range teams {
			if len(t) < len(teams[team]) {
				team = i
			}
		}
		teams[team] = append(teams[team], party...)
	}
	// sort the teams by size
	sort.SliceStable(teams, func(i, j int) bool {
		return len(teams[i]) > len(teams[j])
	})

	for i, player := range teams[0] {
		// If the team is more than two players larger than the other team, distribute the players evenly
		if len(teams[0]) > len(teams[1])+1 {
			// Move a player from teams[0] to teams[1]
			teams[1] = append(teams[1], player)
			teams[0] = append(teams[0][:i], teams[0][i+1:]...)
		}
	}

	return teams
}
func (b *LobbyBuilder) allocateGameServer(ctx context.Context, logger *zap.Logger, groupID uuid.UUID, sorted []string, mode, level evr.Symbol, teamAlignments TeamAlignments, start bool) (MatchID, error) {
	// Lock the game servers so that they aren't double allocated

	available, err := b.listUnassignedLobbies(ctx, logger, groupID)
	if err != nil {
		return MatchID{}, err
	}
	if len(available) == 0 {
		return MatchID{}, ErrMatchmakingNoAvailableServers
	}
	availableByExtIP := make(map[string]MatchID, len(available))

	for _, label := range available {
		k := ipToKey(label.Broadcaster.Endpoint.ExternalIP)
		availableByExtIP[k] = label.ID
	}

	var matchID MatchID
	var found bool
	for _, k := range sorted {
		// Get the endpoint
		matchID, found = availableByExtIP[k]
		if !found {
			continue
		}
		break
	}
	// shuffle the available game servers and pick one

	if matchID.IsNil() {
		matchID = available[rand.Intn(len(available))].ID
	}

	label := &MatchLabel{}
	label.Mode = mode
	label.Level = level
	label.SpawnedBy = SystemUserID
	label.GroupID = &groupID
	label.TeamAlignments = teamAlignments

	label.StartTime = time.Now().UTC()

	// Instruct the server to prepare the level
	response, err := SignalMatch(ctx, b.matchRegistry, matchID, SignalPrepareSession, label)
	if err != nil {
		return MatchID{}, fmt.Errorf("error signaling match `%s`: %s: %v", matchID.String(), response, err)
	}
	return matchID, nil
}

func (b *LobbyBuilder) listUnassignedLobbies(ctx context.Context, logger *zap.Logger, groupID uuid.UUID) ([]*MatchLabel, error) {

	qparts := []string{
		"+label.open:T",
		"+label.lobby_type:unassigned",
		fmt.Sprintf("+label.broadcaster.group_ids:/(%s)/", Query.Escape(groupID.String())),
	}
	query := strings.Join(qparts, " ")
	// TODO FIXME Add version lock and appid

	logger.Debug("Listing unassigned lobbies", zap.String("query", query))
	limit := 200
	minSize, maxSize := 1, 1 // Only the 1game server should be in the match handler
	matches, err := b.listMatches(ctx, limit, minSize, maxSize, query)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "Failed to find matches: %v", err)
	}

	if len(matches) == 0 {
		return nil, ErrMatchmakingNoAvailableServers
	}

	// Create a slice containing the matches' labels
	labels := make([]*MatchLabel, 0, len(matches))
	for _, match := range matches {
		label := &MatchLabel{}
		if err := json.Unmarshal([]byte(match.GetLabel().GetValue()), label); err != nil {
			return nil, status.Errorf(codes.Internal, "Failed to unmarshal match label: %v", err)
		}
		labels = append(labels, label)
	}

	return labels, nil
}

func (b *LobbyBuilder) listMatches(ctx context.Context, limit int, minSize, maxSize int, query string) ([]*api.Match, error) {
	authoritativeWrapper := &wrapperspb.BoolValue{Value: true}
	var labelWrapper *wrapperspb.StringValue
	var queryWrapper *wrapperspb.StringValue
	if query != "" {
		queryWrapper = &wrapperspb.StringValue{Value: query}
	}
	minSizeWrapper := &wrapperspb.Int32Value{Value: int32(minSize)}

	maxSizeWrapper := &wrapperspb.Int32Value{Value: int32(maxSize)}

	matches, _, err := b.matchRegistry.ListMatches(ctx, limit, authoritativeWrapper, labelWrapper, minSizeWrapper, maxSizeWrapper, queryWrapper, nil)
	return matches, err
}

func (b *LobbyBuilder) selectNextMap(mode evr.Symbol) evr.Symbol {
	queue := b.mapQueue[mode]

	if levels, ok := b.mapQueue[mode]; !ok || len(levels) == 0 {
		return evr.LevelUnspecified
	} else if len(evr.LevelsByMode[mode]) == 1 {
		return evr.LevelsByMode[mode][0]
	}

	if len(queue) <= 1 {
		// Fill the queue with the available levels and shuffle.
		queue = append(queue, evr.LevelsByMode[mode]...)

		rand.Shuffle(len(queue), func(i, j int) {
			// leave the first (next) level in place
			if i == 0 || j == 0 {
				return
			}
			queue[i], queue[j] = queue[j], queue[i]
		})

		// If the first two levels are the same, move the first level to the end of the queue.
		if queue[0] == queue[1] {
			queue = append(queue[1:], queue[0])
		}
	}

	// Pop the first level from the queue
	b.mapQueue[mode] = queue[1:]

	return queue[0]
}
