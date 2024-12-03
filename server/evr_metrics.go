package server

import (
	"context"
	"encoding/json"
	"strconv"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
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
		"ip":                t.IPAddress,
		"port":              t.Port,
		"group":             t.Group,
		"geohash":           t.Geohash,
		"latitude":          t.Latitude,
		"longitude":         t.Longitude,
		"as_number":         t.ASNumber,
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

	previouslySeen := make(map[MatchStateTags]struct{})

	for {

		select {
		case <-ctx.Done():
			// Context has been cancelled, return
			return
		case <-ticker.C:
		}
		seen := make(map[MatchStateTags]struct{})

		operatorUsernames := make(map[string]string)

		// Get the match states
		matchStates, err := ListMatchStates(ctx, nk, "")
		if err != nil {
			logger.Error("Error listing match states: %v", err)
			continue
		}
		playercounts := make(map[MatchStateTags][]int)
		for _, state := range matchStates {
			groupID := state.State.GetGroupID()

			operatorUsername, ok := operatorUsernames[state.State.Broadcaster.OperatorID]
			if !ok {
				account, err := nk.AccountGetId(ctx, state.State.Broadcaster.OperatorID)
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
		}
		// Update the metrics
		for tags, matches := range playercounts {
			seen[tags] = struct{}{}
			playerCount := 0
			for _, match := range matches {
				playerCount += match
			}
			tagMap := tags.AsMap()
			nk.metrics.CustomGauge("match_active_gauge", tagMap, float64(len(matches)))
			nk.metrics.CustomGauge("player_active_gauge", tagMap, float64(playerCount))
		}

		// Zero out the metrics for the previously seen, but not currently seen matches
		for tags := range previouslySeen {
			if _, ok := seen[tags]; !ok {
				tagMap := tags.AsMap()
				nk.metrics.CustomGauge("match_active_gauge", tagMap, 0)
				nk.metrics.CustomGauge("player_active_gauge", tagMap, 0)
			}
		}

		previouslySeen = seen

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

	}
}
