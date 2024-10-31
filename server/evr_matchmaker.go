package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"math/rand"
	"slices"
	"strings"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type SkillBasedMatchmaker struct {
	logger *zap.Logger
	router MessageRouter
}

func NewSkillBasedMatchmaker(logger *zap.Logger, router MessageRouter) *SkillBasedMatchmaker {
	return &SkillBasedMatchmaker{
		logger: logger,
		router: router,
	}
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

	for _, group := range groups {
		if len(group) == 1 {
			solos = append(solos, group[0])
		} else {
			parties = append(parties, group)
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

	// Sort parties into teams by strength
	for _, party := range parties {
		if len(team1)+len(party) <= teamSize && (len(team2)+len(party) > teamSize || m.TeamStrength(team1) <= m.TeamStrength(team2)) {
			team1 = append(team1, party...)
		} else if len(team2)+len(party) <= teamSize {
			team2 = append(team2, party...)
		}
	}

	// Sort solo players onto teams by strength
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

func (m *SkillBasedMatchmaker) streamCandidates(candidates [][]runtime.MatchmakerEntry) {
	logger := m.logger
	v, ok := candidates[0][0].GetProperties()["group_id"]
	if !ok {
		logger.Error("Group ID not found in matchmaker properties.")
		return
	}
	groupIDStr, ok := v.(string)
	if !ok {
		logger.Error("Group ID is not a string.")
		return
	}
	groupID := uuid.FromStringOrNil(groupIDStr)

	data, _ := json.Marshal(candidates)
	m.router.SendToStream(m.logger,
		PresenceStream{
			Mode:    StreamModeMatchmaker,
			Subject: groupID,
		},
		&rtapi.Envelope{
			Message: &rtapi.Envelope_StreamData{
				StreamData: &rtapi.StreamData{
					Stream: &rtapi.Stream{
						Mode:    int32(SessionFormatJson),
						Subject: groupID.String(),
					},
					Data: string(data),
				},
			},
		}, false)
}

func removeDuplicateRosters(candidates [][]runtime.MatchmakerEntry) [][]runtime.MatchmakerEntry {
	seenRosters := make(map[string]struct{})
	uniqueCandidates := make([][]runtime.MatchmakerEntry, 0, len(candidates))
	for _, match := range candidates {
		roster := make([]string, 0, len(match))
		for _, e := range match {
			roster = append(roster, e.GetTicket())
		}
		slices.Sort(roster)
		rosterString := strings.Join(roster, ",")
		if _, ok := seenRosters[rosterString]; ok {
			continue
		}
		seenRosters[rosterString] = struct{}{}
		uniqueCandidates = append(uniqueCandidates, match)
	}
	return uniqueCandidates
}

// Function to be used as a matchmaker function in Nakama (RegisterMatchmakerOverride)
func (m *SkillBasedMatchmaker) EvrMatchmakerFn(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, candidates [][]runtime.MatchmakerEntry) (madeMatches [][]runtime.MatchmakerEntry) {
	//profileRegistry := &ProfileRegistry{nk: nk}

	if len(candidates) == 0 || len(candidates[0]) == 0 {
		return nil
	}

	// Remove odd sized teams
	for _, match := range candidates {
		if len(match)%2 != 0 {
			logger.WithField("match", match).Warn("Match has odd number of players.")
			continue
		}
	}

	logger.WithField("num_candidates", len(candidates)).Info("Running skill-based matchmaker.")

	// Remove duplicate rosters
	candidates = removeDuplicateRosters(candidates)

	m.streamCandidates(candidates)

	// Ensure that everyone in the match is within 100 ping of a server
	for i := 0; i < len(candidates); i++ {
		if !m.hasEligibleServers(candidates[i], 110) {
			logger.WithField("match", candidates[i]).Warn("Match has players with no eligible servers.")
			candidates = append(candidates[:i], candidates[i+1:]...)
			i--
		}
	}

	balanced := balanceMatches(candidates, m)

	predictions := make([]PredictedMatch, 0, len(balanced))
	for _, match := range balanced {
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
			seen[e.Entry.GetTicket()] = struct{}{}

			match = append(match, e.Entry)
		}

		madeMatches = append(madeMatches, match)
	}

	logger.WithFields(map[string]interface{}{
		"num_candidates": len(candidates),
		"num_matches":    len(madeMatches),
		"matches":        madeMatches,
	}).Info("Skill-based matchmaker completed.")

	return madeMatches
}

func (m *SkillBasedMatchmaker) hasEligibleServers(match []runtime.MatchmakerEntry, maxRTT int) bool {
	rttsByServer := make(map[string][]int)
	for _, entry := range match {
		for k, v := range entry.GetProperties() {
			if !strings.HasPrefix(k, "rtt_") {
				continue
			}

			if rtt, ok := v.(int); ok {
				if _, ok := rttsByServer[k]; !ok {
					rttsByServer[k] = make([]int, 0, len(match))
				}
				rttsByServer[k] = append(rttsByServer[k], rtt)
			}
		}
	}

	for _, s := range rttsByServer {
		if len(s) != len(match) {
			// Server is unreachable to one or more players
			return false
		}
	}

	for _, rtts := range rttsByServer {
		for _, rtt := range rtts {
			if rtt > maxRTT {
				// Server is too far away for one or more players
				return false
			}
		}
	}

	return true
}

func balanceMatches(candidateMatches [][]runtime.MatchmakerEntry, m *SkillBasedMatchmaker) []RatedMatch {
	seenRosters := make(map[string]struct{})

	balancedMatches := make([]RatedMatch, 0, len(candidateMatches))
	for _, match := range candidateMatches {
		roster := make([]string, 0, len(match))
		for _, e := range match {
			roster = append(roster, e.GetTicket())
		}

		rosterString := strings.Join(roster, ",")
		if _, ok := seenRosters[rosterString]; ok {
			continue
		}
		seenRosters[rosterString] = struct{}{}

		team1, team2 := m.BalancedMatchFromCandidate(match)
		balancedMatches = append(balancedMatches, RatedMatch{team1, team2})
	}
	return balancedMatches
}

func GetRatinByUserID(ctx context.Context, db *sql.DB, userID string) (rating types.Rating, err error) {
	// Look for an existing account.
	query := "SELECT value->>'rating' FROM storage WHERE user_id = $1 AND collection = $2 and key = $3"
	var ratingJSON string
	var found = true
	if err = db.QueryRowContext(ctx, query, userID, GameProfileStorageCollection, GameProfileStorageKey).Scan(&ratingJSON); err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return rating, status.Error(codes.Internal, "error finding rating by user ID")
		}
	}
	if !found {
		return rating, status.Error(codes.NotFound, "rating not found")
	}
	if err = json.Unmarshal([]byte(ratingJSON), &rating); err != nil {
		return rating, status.Error(codes.Internal, "error unmarshalling rating")
	}
	return rating, nil
}
