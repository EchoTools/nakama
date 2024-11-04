package server

import (
	"math"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"go.uber.org/thriftrw/ptr"
)

type (
	RatedMatch []RatedEntryTeam
)

type RatedEntry struct {
	Entry  *MatchmakerEntry
	Rating types.Rating
}

func NewRatedEntryFromMatchmakerEntry(e runtime.MatchmakerEntry) *RatedEntry {
	props := e.GetProperties()
	mu, ok := props["rating_mu"].(float64)
	if !ok {
		mu = 25.0
	}
	sigma, ok := props["rating_sigma"].(float64)
	if !ok {
		sigma = 8.333
	}
	return &RatedEntry{
		Entry: e.(*MatchmakerEntry),
		Rating: rating.NewWithOptions(&types.OpenSkillOptions{
			Mu:    ptr.Float64(mu),
			Sigma: ptr.Float64(sigma),
		}),
	}
}

type RatedEntryTeam []*RatedEntry

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

	return rating.NewWithOptions(&types.OpenSkillOptions{
		Mu:    ptr.Float64(meanMu),
		Sigma: ptr.Float64(rmsSigma),
	})
}

func (t RatedTeam) Ordinal() float64 {
	return rating.Ordinal(t.Rating())
}

type PredictedMatch struct {
	Team1 RatedEntryTeam `json:"team1"`
	Team2 RatedEntryTeam `json:"team2"`
	Draw  float64        `json:"draw"`
}

func (p PredictedMatch) Entrants() RatedEntryTeam {
	return append(p.Team1, p.Team2...)
}

func NewDefaultRating() types.Rating {
	return rating.NewWithOptions(&types.OpenSkillOptions{
		Mu:    ptr.Float64(25.0),
		Sigma: ptr.Float64(8.333),
	})
}

func CalculateNewPlayerRating(evrID evr.EvrId, players []PlayerInfo, orangeWins bool) types.Rating {

	teams := make([]types.Team, 2)

	// The player's team
	var teamIdx TeamIndex

	for _, p := range players {
		if p.Team != 0 && p.Team != 1 {
			// skip non players
			continue
		}

		if p.JoinTime != 0.0 {
			// Skip backfill players
			continue
		}

		t := p.Team
		teams[t] = append(teams[t], p.Rating)

		if p.EvrID == evrID {
			teamIdx = p.Team
			if len(teams[t]) > 0 {
				// Move the player to the beginning
				l := len(teams[t])
				teams[t][0], teams[t][l-1] = teams[t][l-1], teams[t][0]
			}
		}
	}

	// Pad the teams to 4 players
	for i := range teams {
		for j := len(teams[i]); j < 4; j++ {
			teams[i] = append(teams[i], NewDefaultRating())
		}
	}

	// Swap the teams if the orangeTeam won

	if orangeWins {
		teams[0], teams[1] = teams[1], teams[0]
		teamIdx = 1 - teamIdx
	}

	teams = rating.Rate(teams, nil)

	return teams[teamIdx][0]
}
