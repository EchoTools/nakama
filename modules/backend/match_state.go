package backend

import (
	"sort"
	"strings"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/echotools/nakama/v3/service"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
)

type matchState struct {
	*service.MatchLabel

	id              service.MatchID
	server          runtime.Presence                     // The broadcaster's presence
	levelLoaded     bool                                 // Whether the server has been sent the start instruction.
	presenceMap     map[string]*service.MatchPresence    // [sessionId]EvrMatchPresence
	reservationMap  map[string]*slotReservation          // map[sessionID]slotReservation
	presenceByEvrID map[evr.EvrId]*service.MatchPresence // map[evrID]EvrMatchPresence

	joinTimestamps       map[string]time.Time // The timestamps of when players joined the match. map[sessionId]time.Time
	joinTimeMilliseconds map[string]int64     // The round clock time of when players joined the match. map[sessionId]time.Time
	sessionStartExpiry   int64                // The tick count at which the match will be shut down if it has not started.
	tickRate             int64                // The number of ticks per second.
	emptyTicks           int64                // The number of ticks the match has been empty.
	terminateTick        int64                // The tick count at which the match will be shut down.
	goals                []*evr.MatchGoal     // The goals scored in the match.
}

func (s *matchState) loadAndDeleteReservation(sessionID string) (*service.MatchPresence, bool) {
	r, ok := s.reservationMap[sessionID]

	if !ok || r.Expiry.Before(time.Now()) {
		delete(s.reservationMap, sessionID)
		return nil, false
	}

	delete(s.reservationMap, sessionID)
	s.rebuildCache()

	return r.Presence, true
}

// rebuildCache is called after the presences map is updated.
func (s *matchState) rebuildCache() {
	presences := make([]*service.MatchPresence, 0, len(s.presenceMap))
	for _, p := range s.presenceMap {
		presences = append(presences, p)
	}

	// Include the reservations in the cache.
	for id, r := range s.reservationMap {
		if r.Expiry.Before(time.Now()) {
			delete(s.reservationMap, id)
			continue
		}
		// Include the reservation in the cache.
		presences = append(presences, r.Presence)
	}

	// Rebuild the lookup tables.
	s.Size = len(presences)
	s.Players = make([]service.PlayerInfo, 0, s.Size)
	s.PlayerCount = 0
	ratingScores := s.caculateRatingWeights()
	for _, p := range presences {
		// Do not include spectators or moderators in player count
		if p.RoleAlignment != evr.TeamSpectator && p.RoleAlignment != evr.TeamModerator {
			s.PlayerCount++
		}

		if p.RoleAlignment == evr.TeamSpectator {
			s.Players = append(s.Players, service.PlayerInfo{
				UserID:      p.UserID.String(),
				Username:    p.Username,
				DisplayName: p.DisplayName,
				EvrID:       p.EvrID,
				Team:        service.TeamIndex(p.RoleAlignment),
				ClientIP:    p.ClientIP,
				DiscordID:   p.DiscordID,
				SessionID:   p.SessionID.String(),
				JoinTime:    s.joinTimeMilliseconds[p.SessionID.String()],
				GeoHash:     p.GeoHash,
			})
		} else {
			ordinal := rating.Ordinal(p.Rating)
			switch s.Mode {
			case evr.ModeArenaPublic:
				s.Players = append(s.Players, service.PlayerInfo{
					UserID:         p.UserID.String(),
					Username:       p.Username,
					DisplayName:    p.DisplayName,
					EvrID:          p.EvrID,
					Team:           service.TeamIndex(p.RoleAlignment),
					ClientIP:       p.ClientIP,
					DiscordID:      p.DiscordID,
					PartyID:        p.PartyID.String(),
					RatingMu:       p.Rating.Mu,
					RatingSigma:    p.Rating.Sigma,
					RatingOrdinal:  ordinal,
					RatingScore:    ratingScores[p.EvrID],
					JoinTime:       s.joinTimeMilliseconds[p.SessionID.String()],
					RankPercentile: p.RankPercentile,
					SessionID:      p.SessionID.String(),
					IsReservation:  s.reservationMap[p.SessionID.String()] != nil,
					GeoHash:        p.GeoHash,
					PingMillis:     p.PingMillis,
				})
			case evr.ModeCombatPublic, evr.ModeSocialPublic:
				s.Players = append(s.Players, service.PlayerInfo{
					UserID:         p.UserID.String(),
					Username:       p.Username,
					DisplayName:    p.DisplayName,
					EvrID:          p.EvrID,
					Team:           service.TeamIndex(p.RoleAlignment),
					ClientIP:       p.ClientIP,
					DiscordID:      p.DiscordID,
					PartyID:        p.PartyID.String(),
					JoinTime:       s.joinTimeMilliseconds[p.SessionID.String()],
					RatingMu:       p.Rating.Mu,
					RatingSigma:    p.Rating.Sigma,
					RatingOrdinal:  ordinal,
					RatingScore:    ratingScores[p.EvrID],
					RankPercentile: p.RankPercentile,
					SessionID:      p.SessionID.String(),
					IsReservation:  s.reservationMap[p.SessionID.String()] != nil,
					GeoHash:        p.GeoHash,
					PingMillis:     p.PingMillis,
				})

			case evr.ModeArenaPrivate, evr.ModeCombatPrivate:
				s.Players = append(s.Players, service.PlayerInfo{
					UserID:        p.UserID.String(),
					Username:      p.Username,
					DisplayName:   p.DisplayName,
					EvrID:         p.EvrID,
					Team:          service.AnyTeam, // Roles are not tracked in private matches.
					ClientIP:      p.ClientIP,
					DiscordID:     p.DiscordID,
					PartyID:       p.PartyID.String(),
					SessionID:     p.SessionID.String(),
					IsReservation: s.reservationMap[p.SessionID.String()] != nil,
					GeoHash:       p.GeoHash,
					PingMillis:    p.PingMillis,
				})
			case evr.ModeSocialPrivate:
				s.Players = append(s.Players, service.PlayerInfo{
					UserID:        p.UserID.String(),
					Username:      p.Username,
					DisplayName:   p.DisplayName,
					EvrID:         p.EvrID,
					Team:          service.TeamIndex(p.RoleAlignment),
					ClientIP:      p.ClientIP,
					DiscordID:     p.DiscordID,
					PartyID:       p.PartyID.String(),
					SessionID:     p.SessionID.String(),
					IsReservation: s.reservationMap[p.SessionID.String()] != nil,
					GeoHash:       p.GeoHash,
					PingMillis:    p.PingMillis,
				})
			default:
				s.Players = append(s.Players, service.PlayerInfo{
					UserID:        p.UserID.String(),
					Username:      p.Username,
					DisplayName:   p.DisplayName,
					EvrID:         p.EvrID,
					Team:          service.TeamIndex(p.RoleAlignment),
					ClientIP:      p.ClientIP,
					DiscordID:     p.DiscordID,
					PartyID:       p.PartyID.String(),
					SessionID:     p.SessionID.String(),
					IsReservation: s.reservationMap[p.SessionID.String()] != nil,
					GeoHash:       p.GeoHash,
					PingMillis:    p.PingMillis,
				})
			}
		}
		if p.MatchmakingAt != nil {
			s.joinTimestamps[p.SessionID.String()] = *p.MatchmakingAt
		}
		switch s.Mode {
		case evr.ModeArenaPublic:
			teamRatings := make([]types.Team, 2)

			teams := make(map[service.TeamIndex]service.RatedTeam, 2)
			for _, p := range s.Players {
				if p.Team != service.BlueTeam && p.Team != service.OrangeTeam {
					continue
				}
				teams[p.Team] = append(teams[p.Team], p.Rating())
				teamRatings[p.Team] = append(teamRatings[p.Team], p.Rating())
			}

			// Calculate the average rank percentile for each team
			rankPercentileAverages := make(map[service.TeamIndex]float64, 2)
			for _, p := range s.Players {
				if p.Team != service.BlueTeam && p.Team != service.OrangeTeam {
					continue
				}
				rankPercentileAverages[p.Team] += p.RankPercentile
			}

			for t, r := range rankPercentileAverages {
				if r > 0 {
					rankPercentileAverages[t] = r / float64(len(teams[t]))
				}
			}

			winPredictions := rating.PredictWin(teamRatings, nil)
			meta := make(map[service.TeamIndex]service.TeamMetadata, 2)
			for _, t := range [...]service.TeamIndex{service.BlueTeam, service.OrangeTeam} {
				meta[t] = service.TeamMetadata{
					Strength:              teams[t].Strength(),
					RankPercentileAverage: rankPercentileAverages[t],
					PredictWin:            winPredictions[t],
				}
			}

			if s.GameState == nil {
				s.GameState = &service.GameState{}
			}
			s.GameState.Teams = meta
		}
	}
	// Sort the players by team, party ID, and join time.
	sort.SliceStable(s.Players, func(i, j int) bool {
		// by team
		if s.Players[i].Team < s.Players[j].Team {
			return true
		}
		if s.Players[i].Team > s.Players[j].Team {
			return false
		}

		// by party ID
		if s.Players[i].PartyID < s.Players[j].PartyID {
			return true
		}
		if s.Players[i].PartyID > s.Players[j].PartyID {
			return false
		}

		// by join time
		return s.Players[i].JoinTime < s.Players[j].JoinTime
	})

	// Recalculate the match's aggregate rank percentile
	s.RankPercentile = 0.0

	s.RatingOrdinal = calculateMedianRating(s.Players)

	count := 0
	if len(s.Players) > 0 {
		for _, p := range s.Players {
			if (p.Team != service.BlueTeam && p.Team != service.OrangeTeam) || p.RankPercentile == 0 {
				continue
			}
			count++
			s.RankPercentile += p.RankPercentile
		}
		if count > 0 {
			s.RankPercentile = s.RankPercentile / float64(count)
		}
	}
}

func (l *matchState) caculateRatingWeights() map[evr.EvrId]int {
	// Calculate the weight of each player's rating in the match
	byPlayer := make(map[evr.EvrId]int)
	byTeam := make(map[service.TeamIndex]int)
	for _, g := range l.goals {
		byPlayer[g.XPID] += g.PointsValue                    // Shooter gets the points
		byTeam[service.TeamIndex(g.TeamID)] += g.PointsValue // Team gets the points
		if !g.PrevPlayerXPID.IsNil() && g.PrevPlayerXPID != g.XPID {
			byPlayer[g.PrevPlayerXPID] += g.PointsValue - 1 // Assist gets the points - 1
		}
	}

	winningTeam := service.BlueTeam
	if byTeam[service.BlueTeam] < byTeam[service.OrangeTeam] {
		winningTeam = service.OrangeTeam
	}

	for _, p := range l.Players {
		if p.Team == winningTeam {
			byPlayer[p.EvrID] += 4
		}
	}
	return byPlayer
}

func (s *matchState) MetricsTags() map[string]string {

	tags := map[string]string{
		"mode":        s.Mode.String(),
		"level":       s.Level.String(),
		"type":        s.LobbyType.String(),
		"group_id":    s.GetGroupID().String(),
		"operator_id": s.GameServer.OperatorID.String(),
	}

	if s.server != nil {
		tags["operator_username"] = strings.TrimPrefix("broadcaster:", s.server.GetUsername())
	}
	return tags
}

// calculateMedianRating calculates the median rating ordinal of all competitor players in the match.
func calculateMedianRating(players []service.PlayerInfo) float64 {
	var ordinals []float64
	for _, p := range players {
		if p.RatingOrdinal != 0 && p.IsCompetitor() {
			ordinals = append(ordinals, p.RatingOrdinal)
		}
	}
	if len(ordinals) == 0 {
		return 0
	}
	sort.Float64s(ordinals)
	mid := len(ordinals) / 2
	if len(ordinals)%2 == 0 {
		return (ordinals[mid-1] + ordinals[mid]) / 2
	}
	return ordinals[mid]
}
