package server

import (
	"context"
	"database/sql"
	"math"
	"math/rand"
	"slices"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"github.com/samber/lo"
	"go.uber.org/thriftrw/ptr"
)

type (
	RatedMatch []RatedEntryTeam
)

func NewDefaultRating() types.Rating {
	return rating.NewWithOptions(&types.OpenSkillOptions{
		Mu:    ptr.Float64(25.0),
		Sigma: ptr.Float64(8.333),
	})
}

type RatedEntry struct {
	Entry  runtime.MatchmakerEntry
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
		Entry: e,
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

func (p PredictedMatch) Entrants() []*RatedEntry {
	return append(p.Team1, p.Team2...)
}

type SkillBasedMatchmaker struct{}

func NewSkillBasedMatchmaker() *SkillBasedMatchmaker {
	return &SkillBasedMatchmaker{}
}

func (*SkillBasedMatchmaker) TeamStrength(team RatedEntryTeam) float64 {
	s := 0.0
	for _, p := range team {
		s += p.Rating.Mu
	}
	return s
}

func (*SkillBasedMatchmaker) PredictDraw(teams []RatedEntryTeam) float64 {
	team1 := make(types.Team, 0, len(teams[0]))
	team2 := make(types.Team, 0, len(teams[1]))
	for _, e := range teams[0] {
		team1 = append(team1, e.Rating)
	}
	for _, e := range teams[1] {
		team2 = append(team2, e.Rating)
	}
	return rating.PredictDraw([]types.Team{team1, team2}, nil)
}

func (m *SkillBasedMatchmaker) CreateBalancedMatch(groups [][]*RatedEntry, teamSize int) (RatedEntryTeam, RatedEntryTeam) {
	// Split out the solo players
	solos := make([]*RatedEntry, 0, len(groups))
	parties := make([][]*RatedEntry, 0, len(groups))
	for _, party := range parties {
		if len(party) == 1 {
			solos = append(solos, party[0])
		} else {
			parties = append(parties, party)
		}
	}

	// Shuffle parties
	for i := range parties {
		j := rand.Intn(i + 1)
		parties[i], parties[j] = parties[j], parties[i]
	}

	// Shuffle solo players
	for i := range solos {
		j := rand.Intn(i + 1)
		solos[i], solos[j] = solos[j], solos[i]
	}

	team1 := make(RatedEntryTeam, 0, teamSize)
	team2 := make(RatedEntryTeam, 0, teamSize)

	for _, party := range parties {
		if len(team1)+len(party) <= teamSize && (len(team2)+len(party) > teamSize || m.TeamStrength(team1) <= m.TeamStrength(team2)) {
			team1 = append(team1, party...)
		} else if len(team2)+len(party) <= teamSize {
			team2 = append(team2, party...)
		}
	}

	for _, player := range solos {
		if len(team1) < teamSize && (len(team2) >= teamSize || m.TeamStrength(team1) <= m.TeamStrength(team2)) {
			team1 = append(team1, player)
		} else if len(team2) < teamSize {
			team2 = append(team2, player)
		}
	}

	return team1, team2
}

func (m *SkillBasedMatchmaker) BalancedMatchFromCandidate(candidate []runtime.MatchmakerEntry) (RatedEntryTeam, RatedEntryTeam) {
	// Create a balanced match
	ticketMap := lo.GroupBy(candidate, func(e runtime.MatchmakerEntry) string {
		return e.GetTicket()
	})
	groups := make([][]*RatedEntry, 0)
	for _, group := range ticketMap {
		ratedGroup := make([]*RatedEntry, 0, len(group))
		for _, e := range group {
			ratedGroup = append(ratedGroup, NewRatedEntryFromMatchmakerEntry(e))
		}
		groups = append(groups, ratedGroup)
	}

	team1, team2 := m.CreateBalancedMatch(groups, len(candidate)/2)
	return team1, team2
}

func (m *SkillBasedMatchmaker) EvrMatchmakerFn(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, candidateMatches [][]runtime.MatchmakerEntry) (madeMatches [][]runtime.MatchmakerEntry) {
	//profileRegistry := &ProfileRegistry{nk: nk}
	logger.WithField("matches", candidateMatches).Debug("Match candidates.")

	// TODO FIXME get the team size from the ticket properties

	balancedMatches := make([]RatedMatch, 0, len(candidateMatches))

	for _, match := range candidateMatches {
		if len(match)%2 != 0 {
			logger.WithField("match", match).Warn("Match has odd number of players.")
			continue
		}
		team1, team2 := m.BalancedMatchFromCandidate(match)
		balancedMatches = append(balancedMatches, RatedMatch{team1, team2})

	}

	predictions := make([]PredictedMatch, 0, len(balancedMatches))
	for _, match := range balancedMatches {
		predictions = append(predictions, PredictedMatch{
			Team1: match[0],
			Team2: match[1],
			Draw:  m.PredictDraw(match),
		})
	}

	// Sort the matches by their balance
	slices.SortStableFunc(predictions, func(a, b PredictedMatch) int {
		return int((a.Draw - b.Draw) * 1000)
	})

	// Sort so that highest draw probability is first
	slices.Reverse(predictions)

	logger.WithField("matches", predictions).Debug("Match options.")

	seen := make(map[string]struct{})
	madeMatches = make([][]runtime.MatchmakerEntry, 0, len(predictions))
OuterLoop:
	for _, p := range predictions {
		// The players are ordered by their team
		match := make([]runtime.MatchmakerEntry, 0, 8)
		for _, e := range p.Entrants() {

			// Skip a match where any player of any ticket that has already been seen
			if _, ok := seen[e.Entry.GetTicket()]; ok {
				continue OuterLoop
			}
			match = append(match, e.Entry)
		}

		madeMatches = append(madeMatches, match)
	}

	logger.WithField("matches", madeMatches).Debug("Made matches.")
	return madeMatches
}
