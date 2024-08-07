package server

import (
	"context"
	"database/sql"
	"errors"
	"math/rand"
	"slices"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"github.com/samber/lo"
	"go.uber.org/thriftrw/ptr"
	"go.uber.org/zap"
)

type BroadcasterRegistrationMap *MapOf[string, *MatchBroadcaster]

type SkillBasedMatchmaker struct {
	logger                           *zap.Logger
	metrics                          Metrics
	profileRegistry                  *ProfileRegistry
	broadcasterRegistrationBySession *MapOf[string, *MatchBroadcaster]
	matchmakingRegistry              *MatchmakingRegistry
	activeMatches                    MapOf[MatchID, [][]string]
}

func NewSkillBasedMatchmaker(logger *zap.Logger, profileRegistry *ProfileRegistry, matchmakingRegistry *MatchmakingRegistry, metrics Metrics, broadcasterRegistrationBySession *MapOf[string, *MatchBroadcaster]) *SkillBasedMatchmaker {
	return &SkillBasedMatchmaker{
		logger:  logger,
		metrics: metrics,

		profileRegistry:                  profileRegistry,
		activeMatches:                    MapOf[MatchID, [][]string]{},
		matchmakingRegistry:              matchmakingRegistry,
		broadcasterRegistrationBySession: broadcasterRegistrationBySession,
	}
}

func (m *SkillBasedMatchmaker) AddMatch(matchID MatchID, teams [][]string) {
	m.activeMatches.Store(matchID, teams)
}

func (m *SkillBasedMatchmaker) RecordResult(matchID MatchID, userID string, win bool) error {
	teams, ok := m.activeMatches.LoadAndDelete(matchID)
	if !ok {
		return nil
	}

	result := teams[:]
	// First team is the winner
	if slices.Contains(teams[0], userID) {
		if !win {
			slices.Reverse(result)
		}
	} else if slices.Contains(teams[1], userID) {
		if win {
			slices.Reverse(result)
		}
	} else {
		return errors.New("user not in match, they might have been a backfill")
	}
	return nil
}

func TeamStrength(team []*RatedEntry) float64 {
	s := 0.0
	for _, p := range team {
		s += p.Rating.Mu
	}
	return s
}

type MatchHistory struct {
	Teams [][]MatchmakerEntry `json:"teams"`
}

type MatchmakerMeta struct {
	PartyID     string  `json:"party_id"`
	RatingMu    float64 `json:"rating_mu"`
	RatingSigma float64 `json:"rating_sigma"`
}

type RatedMatch []RatedTeam
type RatedTeam []*RatedEntry

func (t RatedTeam) Strength() float64 {
	s := 0.0
	for _, e := range t {
		s += e.Rating.Mu
	}
	return s
}

type RatedEntry struct {
	Entry  runtime.MatchmakerEntry
	Rating types.Rating
}

type PredictedMatch struct {
	Team1 RatedTeam `json:"team1"`
	Team2 RatedTeam `json:"team2"`
	Draw  float64   `json:"draw"`
}

func (p PredictedMatch) Entrants() []*RatedEntry {
	return append(p.Team1, p.Team2...)
}
func PredictDraw(teams []RatedTeam) float64 {
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

func CreateBalancedMatch(groups [][]*RatedEntry, teamSize int) (RatedTeam, RatedTeam) {
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

	team1 := make(RatedTeam, 0, teamSize)
	team2 := make(RatedTeam, 0, teamSize)

	for _, party := range parties {
		if len(team1)+len(party) <= teamSize && (len(team2)+len(party) > teamSize || TeamStrength(team1) <= TeamStrength(team2)) {
			team1 = append(team1, party...)
		} else if len(team2)+len(party) <= teamSize {
			team2 = append(team2, party...)
		}
	}

	for _, player := range solos {
		if len(team1) < teamSize && (len(team2) >= teamSize || TeamStrength(team1) <= TeamStrength(team2)) {
			team1 = append(team1, player)
		} else if len(team2) < teamSize {
			team2 = append(team2, player)
		}
	}

	return team1, team2
}

func (*SkillBasedMatchmaker) BalancedMatchFromCandidate(candidate []runtime.MatchmakerEntry) (RatedTeam, RatedTeam) {
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

	team1, team2 := CreateBalancedMatch(groups, len(candidate)/2)
	return team1, team2
}

func (m *SkillBasedMatchmaker) EvrMatchmakerFn(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, candidateMatches [][]runtime.MatchmakerEntry) (madeMatches [][]runtime.MatchmakerEntry) {
	//profileRegistry := &ProfileRegistry{nk: nk}
	logger.Debug("Match candidates.", zap.Any("matches", candidateMatches))

	// TODO FIXME get the team size from the ticket properties

	balancedMatches := make([]RatedMatch, 0, len(candidateMatches))

	for _, match := range candidateMatches {
		if len(match)%2 != 0 {
			logger.Warn("Match has odd number of players.", zap.Any("match", match))
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
			Draw:  PredictDraw(match),
		})
	}

	// Sort the matches by their balance
	slices.SortStableFunc(predictions, func(a, b PredictedMatch) int {
		return int((a.Draw - b.Draw) * 1000)
	})

	// Sort so that highest draw probability is first
	slices.Reverse(predictions)

	logger.Debug("Match options.", zap.Any("matches", predictions))

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

	logger.Debug("Made matches.", zap.Any("matches", madeMatches))
	return madeMatches
}
