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

func PredictMatch(teams [][]*RatedEntry) []float64 {
	team1 := make(types.Team, 0, len(teams[0]))
	team2 := make(types.Team, 0, len(teams[1]))
	for _, e := range teams[0] {
		team1 = append(team1, e.Rating)
	}
	for _, e := range teams[1] {
		team2 = append(team2, e.Rating)
	}
	return rating.PredictWin([]types.Team{team1, team2}, nil)
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

func createBalancedMatch(allParties [][]*RatedEntry, teamSize int) [][]*RatedEntry {
	// Split out the solo players
	solos := make([]*RatedEntry, 0, len(allParties))
	parties := make([][]*RatedEntry, 0, len(allParties))
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

	team1 := make([]*RatedEntry, 0, teamSize)
	team2 := make([]*RatedEntry, 0, teamSize)

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

	return [][]*RatedEntry{team1, team2}
}

func EvrMatchmakerFn(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, candidateMatches [][]runtime.MatchmakerEntry) (results [][]runtime.MatchmakerEntry) {
	//profileRegistry := &ProfileRegistry{nk: nk}

	partyMap := make(map[string][]*RatedEntry)
	teamSize := 4

	for _, match := range candidateMatches {
		for _, e := range match {
			partyMap[e.GetTicket()] = append(partyMap[e.GetTicket()], NewRatedEntryFromMatchmakerEntry(e))
		}
	}

	allParties := make([][]*RatedEntry, 0, len(partyMap))
	for _, party := range partyMap {
		allParties = append(allParties, party)
	}

	matches := make([][][]*RatedEntry, 0, len(candidateMatches))
	matches = append(matches, createBalancedMatch(allParties, teamSize))

	// Sort the matches by their balance
	slices.SortStableFunc(matches, func(match1, match2 [][]*RatedEntry) int {
		return int(10000 * (PredictMatch(match1)[0] - PredictMatch(match2)[0]))
	})

	logger.Debug("Match options", zap.Any("teams", matches))

	results = make([][]runtime.MatchmakerEntry, 0, len(matches))
	for _, match := range matches {
		option := make([]runtime.MatchmakerEntry, 0, len(match))
		for _, t := range match {
			for _, e := range t {
				option = append(option, e.Entry)
			}
		}
		results = append(results, option)
	}

	return results
}
