package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"strconv"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"gonum.org/v1/gonum/stat"
)

type PlayerTags struct {
	Group             uuid.UUID
	Mode              evr.Symbol
	PlayerGeoHash     string
	GameServerGeoHash string
	Latency           int
}

func NewPlayerTags(group uuid.UUID, mode evr.Symbol, playerGeoHash, gameServerGeoHash string, rtt int) PlayerTags {

	// Reduce the precision of the geohash to two characters
	if len(playerGeoHash) > 2 {
		playerGeoHash = playerGeoHash[:2]
	}
	if len(gameServerGeoHash) > 2 {
		gameServerGeoHash = gameServerGeoHash[:2]
	}

	// Reduce the precision of the latency to 10ms
	const precision_ms = 10

	return PlayerTags{
		Group:             group,
		Mode:              mode,
		PlayerGeoHash:     playerGeoHash,
		GameServerGeoHash: gameServerGeoHash,
		Latency:           ((rtt / 2) + (precision_ms / 2)) / precision_ms * precision_ms,
	}
}

func (t PlayerTags) AsMap() map[string]string {
	return map[string]string{
		"group":              t.Group.String(),
		"mode":               t.Mode.String(),
		"player_geohash":     t.PlayerGeoHash,
		"gameserver_geohash": t.GameServerGeoHash,
		"latency":            strconv.Itoa(t.Latency),
	}
}

type MatchStateTags struct {
	Type             LobbyType
	Mode             evr.Symbol
	Level            evr.Symbol
	OperatorID       string
	OperatorUsername string
	IPAddress        string
	Port             uint16
	Group            string
	Geohash          string
	Latitude         float64
	Longitude        float64
	ASNumber         int
}

func (t MatchStateTags) AsMap() map[string]string {
	return map[string]string{
		"type":              t.Type.String(),
		"mode":              t.Mode.String(),
		"level":             t.Level.String(),
		"operator_id":       t.OperatorID,
		"operator_username": t.OperatorUsername,
		"ext_ip":            t.IPAddress,
		"port":              strconv.Itoa(int(t.Port)),
		"group":             t.Group,
		"geohash":           t.Geohash,
		"latitude":          strconv.FormatFloat(t.Latitude, 'f', 2, 64),
		"longitude":         strconv.FormatFloat(t.Longitude, 'f', 2, 64),
		"as_number":         strconv.Itoa(t.ASNumber),
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

		matchStates = append(matchStates, &MatchLabelMeta{
			State:     &state,
			TickRate:  int(tickRate),
			Presences: presences,
		})
	}
	return matchStates, nil
}

func metricsUpdateLoop(ctx context.Context, logger runtime.Logger, nk *RuntimeGoNakamaModule, db *sql.DB) {

	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	previouslySeenMatches := make(map[MatchStateTags]struct{})
	previouslySeenMatchmaking := make(map[MatchmakingStatusTags]struct{})
	previouslySeenPlayers := make(map[PlayerTags]struct{})

	for {

		select {
		case <-ctx.Done():
			// Context has been cancelled, return
			return
		case <-ticker.C:
		}
		seenMatches := make(map[MatchStateTags]struct{})

		operatorUsernames := make(map[uuid.UUID]string)

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
			operatorUsername, ok := operatorUsernames[state.State.GameServer.OperatorID]
			if !ok {
				account, err := nk.AccountGetId(ctx, state.State.GameServer.OperatorID.String())
				if err != nil {
					logger.Error("Error getting account: %v", err)
					continue
				}
				operatorUsername = account.User.Username
				operatorUsernames[state.State.GameServer.OperatorID] = operatorUsername
			}

			stateTags := MatchStateTags{
				Type:             state.State.LobbyType,
				Mode:             state.State.Mode,
				Level:            state.State.Level,
				OperatorID:       state.State.GameServer.OperatorID.String(),
				OperatorUsername: operatorUsername,
				Group:            groupID.String(),
				IPAddress:        state.State.GameServer.Endpoint.ExternalIP.String(),
				Port:             state.State.GameServer.Endpoint.Port,
				Geohash:          state.State.GameServer.GeoHash,
				Latitude:         state.State.GameServer.Latitude,
				Longitude:        state.State.GameServer.Longitude,
				ASNumber:         state.State.GameServer.ASNumber,
			}
			playercounts[stateTags] = append(playercounts[stateTags], len(state.State.Players))

			rank_percentiles := make([]float64, 0, len(state.State.Players))

			for _, player := range state.State.Players {
				tags := NewPlayerTags(groupID, state.State.Mode, player.GeoHash, state.State.GameServer.GeoHash, player.PingMillis)
				playerData[tags] += 1

				rank_percentiles = append(rank_percentiles, float64(player.RankPercentile))
			}

			percentileVariances[stateTags] = append(percentileVariances[stateTags], stat.Variance(rank_percentiles, nil))
			matchRankPercentiles[stateTags] = append(matchRankPercentiles[stateTags], state.State.RankPercentile)
		}

		seenPlayers := make(map[PlayerTags]struct{})
		// Update the player to game server relationship metrics
		for tags, count := range playerData {
			seenPlayers[tags] = struct{}{}
			tagMap := tags.AsMap()
			nk.metrics.CustomGauge("player_geodata_gauge", tagMap, float64(count))
		}

		// Zero out the metrics for the previously seen, but not currently seen players
		for tags := range previouslySeenPlayers {
			if _, ok := seenPlayers[tags]; !ok {
				tagMap := tags.AsMap()
				nk.metrics.CustomGauge("player_geodata_gauge", tagMap, 0)
			}
		}

		previouslySeenPlayers = seenPlayers

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

		// Update linked headset counts
		linkedUsers, err := CountLinkedUsers(ctx, nk, db)
		if err != nil {
			logger.Error("Error counting linked users: %v", err)
		} else {
			nk.metrics.CustomGauge("linked_users_gauge", nil, float64(linkedUsers))
		}

		// Update the geomap data
		locations := make(map[string][]float64)

		for _, state := range matchStates {
			if state.State.GameServer.Endpoint.GetExternalIP() == "" {
				continue
			}
			locations[state.State.GameServer.Endpoint.GetExternalIP()] = []float64{
				state.State.GameServer.Latitude,
				state.State.GameServer.Longitude,
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

func CountLinkedUsers(ctx context.Context, nk runtime.NakamaModule, db *sql.DB) (int, error) {
	query := "SELECT count(user_id) FROM user_device WHERE id LIKE 'OVR-%' OR id LIKE 'DMO-%'"
	rows, err := db.QueryContext(ctx, query)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	var count int
	for rows.Next() {
		if err := rows.Scan(&count); err != nil {
			return 0, err
		}
	}
	return count, nil
}
