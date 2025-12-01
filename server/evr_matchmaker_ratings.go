package server

import (
	"math"

	"slices"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/thriftrw/ptr"
)

type (
	RatedMatch [2]RatedEntryTeam
)

type RatedEntry struct {
	Entry  *MatchmakerEntry
	Rating types.Rating
}

type RatedEntryTeam []RatedEntry

func (t RatedEntryTeam) Len() int {
	return len(t)
}

func (t RatedEntryTeam) Strength() float64 {
	s := 0.0
	for _, p := range t {
		s += p.Rating.Mu
	}
	return s
}

type RatedTeam []types.Rating

func (t RatedTeam) Strength() float64 {
	s := 0.0
	for _, p := range t {
		s += p.Mu
	}
	return s
}

func (t RatedTeam) Rating() types.Rating {
	if len(t) == 0 {
		return NewDefaultRating()
	}
	meanMu := t.Strength() / float64(len(t))
	sumSigmaSquared := 0.0
	for _, p := range t {
		sumSigmaSquared += p.Sigma * p.Sigma
	}
	averageSigmaSquared := sumSigmaSquared / float64(len(t))
	rmsSigma := math.Sqrt(averageSigmaSquared)

	return NewRating(0, meanMu, rmsSigma)
}

func (t RatedTeam) Ordinal() float64 {
	return rating.Ordinal(t.Rating())
}

func NewRating[T int | int64 | float64](z, mu, sigma T) types.Rating {
	r := NewDefaultRating()
	if zInt, ok := any(z).(int); ok {
		r.Z = zInt
	}
	if muFloat := float64(mu); muFloat > 0 {
		r.Mu = muFloat
	}
	if sigmaFloat := float64(sigma); sigmaFloat > 0 {
		r.Sigma = sigmaFloat
	} else {
		r.Sigma = r.Mu / float64(r.Z)
	}
	return r
}

func NewDefaultRating() types.Rating {
	return types.Rating{
		Z:     3,
		Mu:    10.0,
		Sigma: 10.0 / 3.0,
	}
}

// CalculateNewPlayerRatings calculates the new player ratings based on the match outcome.
func CalculateNewPlayerRatings(playerInfos []PlayerInfo, playerStats map[evr.EvrId]evr.MatchTypeStats, blueWins bool) map[string]types.Rating {

	const WinningTeamBonus = 4

	winningTeam := BlueTeam
	if !blueWins {
		winningTeam = OrangeTeam
	}

	// copy the players slice so as to not modify the original
	playerInfos = slices.Clone(playerInfos)

	// Remove players that are not on blue/orange
	for i := 0; i < len(playerInfos); i++ {
		if !playerInfos[i].IsCompetitor() {
			playerInfos = slices.Delete(playerInfos, i, i+1)
			i--
			continue
		}
	}

	// Create a map of player scores
	playerScores := make(map[string]int, len(playerInfos))
	for _, p := range playerInfos {
		var score int64 = 0
		if stats, ok := playerStats[p.EvrID]; ok {
			score += stats.Points
			score += stats.Assists * 2
			score += stats.Saves * 3
			score += stats.Passes * 1
			score -= stats.ShotsOnGoal * 1 // penalty for missed shots
		}
		if p.Team == winningTeam {
			score += WinningTeamBonus
		}
		playerScores[p.SessionID] = int(score)
	}

	// Sort the players by score
	slices.SortStableFunc(playerInfos, func(a, b PlayerInfo) int {
		// Sort winning team first
		if a.Team == winningTeam && b.Team != winningTeam {
			return -1
		}
		if a.Team != winningTeam && b.Team == winningTeam {
			return 1
		}
		// Sort by score (descending)
		return playerScores[b.SessionID] - playerScores[a.SessionID]
	})

	// Split the roster by teamRatings (all players are separated on their own team)
	teamRatings := make([]types.Team, len(playerInfos))
	for i, p := range playerInfos {
		teamRatings[i] = types.Team{p.Rating()}
	}

	// Create a map of player scores; used to weight the new ratings
	scores := make([]int, len(playerInfos))
	for i, p := range playerInfos {
		scores[i] = playerScores[p.SessionID]
	}

	// Calculate the new ratings
	teamRatings = rating.Rate(teamRatings, &types.OpenSkillOptions{
		Score: scores,
		Tau:   ptr.Float64(0.3), // prevent sigma from dropping too low
	})

	ratingMap := make(map[string]types.Rating, len(playerInfos))
	for i, team := range teamRatings {
		sID := playerInfos[i].SessionID
		ratingMap[sID] = team[0]
	}

	// Return the new rating for the target player
	return ratingMap
}
