package server

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"gonum.org/v1/gonum/stat"
)

type MatchStateTags struct {
	Type             string
	Mode             string
	Level            string
	OperatorID       string
	OperatorUsername string
	IPAddress        string
	Port             string
	Group            string
	Geohash          string
	Latitude         string
	Longitude        string
	ASNumber         string
}

func (t MatchStateTags) AsMap() map[string]string {
	return map[string]string{
		"type":              t.Type,
		"mode":              t.Mode,
		"level":             t.Level,
		"operator_id":       t.OperatorID,
		"operator_username": t.OperatorUsername,
		"ext_ip":            t.IPAddress,
		"port":              t.Port,
		"group":             t.Group,
		"geohash":           t.Geohash,
		"latitude":          t.Latitude,
		"longitude":         t.Longitude,
		"as_number":         t.ASNumber,
	}
}

type MatchmakingStatusTags struct {
	Mode      string
	Group     string
	PartySize string
}

func (t MatchmakingStatusTags) AsMap() map[string]string {
	return map[string]string{
		"mode":       t.Mode,
		"group":      t.Group,
		"party_size": t.PartySize,
	}
}

var _ = Metrics(&EVRMetrics{})

type EVRMetrics struct {
	Metrics
}

func NewEVRMetrics(metrics Metrics) *EVRMetrics {
	return &EVRMetrics{
		Metrics: metrics,
	}
}

func (m *EVRMetrics) CountWebsocketOpened(delta int64) {
	m.CountWebsocketOpened(1)
	m.CustomCounter("session_evr_closed", nil, 1)
}

func (m *EVRMetrics) CountWebsocketClosed(delta int64) {
	m.CountWebsocketClosed(1)
	m.CustomCounter("session_evr_closed", nil, 1)
}

func ListMatchStates(ctx context.Context, nk runtime.NakamaModule, query string) ([]*MatchLabelMeta, error) {
	if query == "" {
		query = "*"
	}
	// Get the list of active matches
	minSize := 1
	maxSize := MatchLobbyMaxSize

	matches, err := nk.MatchList(ctx, 1000, true, "", &minSize, &maxSize, query)
	if err != nil {
		return nil, err
	}

	var matchStates []*MatchLabelMeta
	for _, match := range matches {
		mt := MatchIDFromStringOrNil(match.MatchId)
		presences, tickRate, data, err := nk.(*RuntimeGoNakamaModule).matchRegistry.GetState(ctx, mt.UUID, mt.Node)
		if err != nil {
			return nil, err
		}

		state := MatchLabel{}
		if err := json.Unmarshal([]byte(data), &state); err != nil {
			return nil, err
		}

		if state.LobbyType == UnassignedLobby {
			continue
		}

		matchStates = append(matchStates, &MatchLabelMeta{
			State:     &state,
			TickRate:  int(tickRate),
			Presences: presences,
		})
	}
	return matchStates, nil
}

func metricsUpdateLoop(ctx context.Context, logger runtime.Logger, nk *RuntimeGoNakamaModule) {

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	previouslySeenMatches := make(map[MatchStateTags]struct{})
	previouslySeenMatchmaking := make(map[MatchmakingStatusTags]struct{})

	for {

		select {
		case <-ctx.Done():
			// Context has been cancelled, return
			return
		case <-ticker.C:
		}
		seenMatches := make(map[MatchStateTags]struct{})

		operatorUsernames := make(map[string]string)

		// Get the match states
		matchStates, err := ListMatchStates(ctx, nk, "")
		if err != nil {
			logger.Error("Error listing match states: %v", err)
			continue
		}

		playerData := make(map[PlayerTags]int)

		playercounts := make(map[MatchStateTags][]int)

		groupIDs := make(map[string]struct{})

		percentileVariances := make(map[MatchStateTags][]float64)
		matchRankPercentiles := make(map[MatchStateTags][]float64)

		for _, state := range matchStates {
			groupID := state.State.GetGroupID()
			groupIDs[groupID.String()] = struct{}{}
			operatorUsername, ok := operatorUsernames[state.State.Broadcaster.OperatorID]
			if !ok {
				account, err := nk.AccountGetId(ctx, state.State.Broadcaster.OperatorID.String())
				if err != nil {
					logger.Error("Error getting account: %v", err)
					continue
				}
				operatorUsername = account.User.Username
				operatorUsernames[state.State.Broadcaster.OperatorID] = operatorUsername
			}

			stateTags := MatchStateTags{
				Type:             state.State.LobbyType.String(),
				Mode:             state.State.Mode.String(),
				Level:            state.State.Level.String(),
				OperatorID:       state.State.Broadcaster.OperatorID,
				OperatorUsername: operatorUsername,
				Group:            groupID.String(),
				IPAddress:        state.State.Broadcaster.Endpoint.GetExternalIP(),
				Port:             strconv.Itoa(int(state.State.Broadcaster.Endpoint.Port)),
				Geohash:          state.State.Broadcaster.GeoHash,
				Latitude:         strconv.FormatFloat(state.State.Broadcaster.Latitude, 'f', -1, 64),
				Longitude:        strconv.FormatFloat(state.State.Broadcaster.Longitude, 'f', -1, 64),
				ASNumber:         strconv.FormatInt(int64(state.State.Broadcaster.ASNumber), 10),
			}

			playercounts[stateTags] = append(playercounts[stateTags], len(state.State.Players))

			rank_percentiles := make([]float64, 0, len(state.State.Players))
			for _, player := range state.State.Players {
				rank_percentiles = append(rank_percentiles, float64(player.RankPercentile))
			}

			percentileVariances[stateTags] = append(percentileVariances[stateTags], stat.Variance(rank_percentiles, nil))
			matchRankPercentiles[stateTags] = append(matchRankPercentiles[stateTags], state.State.RankPercentile)
		}
		// Update the metrics
		for tags, matches := range playercounts {
			seenMatches[tags] = struct{}{}
			playerCount := 0
			for _, match := range matches {
				playerCount += match
			}
			tagMap := tags.AsMap()
			nk.metrics.CustomGauge("match_active_gauge", tagMap, float64(len(matches)))
			nk.metrics.CustomGauge("player_active_gauge", tagMap, float64(playerCount))
			nk.metrics.CustomGauge("match_rank_percentile_average_variance", tagMap, stat.Mean(percentileVariances[tags], nil))
			nk.metrics.CustomGauge("match_rank_percentile_average", tagMap, stat.Mean(matchRankPercentiles[tags], nil))
		}

		// Zero out the metrics for the previously seen, but not currently seen matches
		for tags := range previouslySeenMatches {
			if _, ok := seenMatches[tags]; !ok {
				tagMap := tags.AsMap()
				nk.metrics.CustomGauge("match_active_gauge", tagMap, 0)
				nk.metrics.CustomGauge("player_active_gauge", tagMap, 0)
				nk.metrics.CustomGauge("match_rank_percentile_average_variance", tagMap, 0)
				nk.metrics.CustomGauge("match_rank_percentile_average", tagMap, 0)
			}
		}

		previouslySeenMatches = seenMatches

		// Update the geomap data
		locations := make(map[string][]float64)

		for _, state := range matchStates {
			if state.State.Broadcaster.Endpoint.GetExternalIP() == "" {
				continue
			}
			locations[state.State.Broadcaster.Endpoint.GetExternalIP()] = []float64{
				state.State.Broadcaster.Latitude,
				state.State.Broadcaster.Longitude,
			}
		}

		seenMatchmaking := make(map[MatchmakingStatusTags]struct{})
		// Matchmaking gauge
		matchmakingPlayers := make(map[MatchmakingStatusTags]int)
		for groupID := range groupIDs {

			presences, err := nk.StreamUserList(StreamModeMatchmaking, groupID, "", "", false, true)
			if err != nil {
				logger.Error("Error listing matchmaking presences: %v", err)
				continue
			}

			for _, presence := range presences {
				lobbyParams := LobbySessionParameters{}
				if err := json.Unmarshal([]byte(presence.GetStatus()), &lobbyParams); err != nil {
					logger.Error("Error unmarshalling matchmaking presence: %v", err)
					continue
				}

				tags := MatchmakingStatusTags{
					Mode:      lobbyParams.Mode.String(),
					Group:     groupID,
					PartySize: strconv.Itoa(lobbyParams.GetPartySize()),
				}
				matchmakingPlayers[tags] += 1
			}

			for tags, count := range matchmakingPlayers {
				seenMatchmaking[tags] = struct{}{}
				tagMap := tags.AsMap()
				nk.metrics.CustomGauge("matchmaking_active_gauge", tagMap, float64(count))
			}

		}
		// Zero out the metrics for the previously seen, but not currently seen matches
		for tags := range previouslySeenMatchmaking {
			if _, ok := seenMatchmaking[tags]; !ok {
				tagMap := tags.AsMap()
				nk.metrics.CustomGauge("matchmaking_active_gauge", tagMap, 0)
			}
		}
		previouslySeenMatchmaking = seenMatchmaking
	}
}
