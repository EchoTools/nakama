package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"slices"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
	"github.com/samber/lo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

type skillBasedMatchmaker struct{}

var SkillBasedMatchmaker = &skillBasedMatchmaker{}

func (*skillBasedMatchmaker) TeamStrength(team RatedEntryTeam) float64 {
	s := 0.0
	for _, p := range team {
		s += p.Rating.Mu
	}
	return s
}

// Function to be used as a matchmaker function in Nakama (RegisterMatchmakerOverride)
func (m *skillBasedMatchmaker) EvrMatchmakerFn(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, candidates [][]runtime.MatchmakerEntry) (madeMatches [][]runtime.MatchmakerEntry) {
	//profileRegistry := &ProfileRegistry{nk: nk}
	logger.WithFields(map[string]interface{}{
		"num_candidates": len(candidates),
	}).Info("Running skill-based matchmaker.")

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

	groupID := candidates[0][0].GetProperties()["group_id"].(string)
	if groupID == "" {
		logger.Error("Group ID not found in matchmaker properties.")
		return nil
	}

	data, _ := json.Marshal(candidates)
	nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			UserID:          SystemUserID,
			Collection:      "Matchmaker",
			Key:             "latestCandidates",
			PermissionRead:  0,
			PermissionWrite: 0,
			Value:           string(data),
		},
	})

	if err := nk.StreamSend(StreamModeMatchmaker, groupID, "", "", string(data), nil, false); err != nil {
		logger.WithField("error", err).Warn("Error streaming candidates")
	}

	// If there is a non-hidden presence on the stream, then don't make any matches
	if presences, err := nk.StreamUserList(StreamModeMatchmaker, groupID, "", "", false, true); err != nil {
		logger.WithField("error", err).Warn("Error listing presences on stream.")
	} else if len(presences) > 0 {
		logger.WithField("num_presences", len(presences)).Info("Non-hidden presence on stream, not making matches.")
		return nil
	}

	// Remove duplicate rosters
	var count int
	candidates, count = removeDuplicateRosters(candidates)
	if count > 0 {
		logger.WithField("num_duplicates", count).Warn("Removed duplicate rosters.")
	}

	// Ensure that everyone in the match is within 100 ping of a server

	for i := 0; i < len(candidates); i++ {
		if len(m.eligibleServers(candidates[i])) == 0 {
			logger.WithField("match", candidates[i]).Warn("Match players have no common servers.")
			candidates = append(candidates[:i], candidates[i+1:]...)
			i--
		}
	}

	predictions := make([]PredictedMatch, 0, len(candidates))
	for _, match := range candidates {
		ratedMatch := m.BalancedMatchFromCandidate(match)
		predictions = append(predictions, PredictedMatch{
			Team1: ratedMatch[0],
			Team2: ratedMatch[1],
			Draw:  m.PredictDraw(ratedMatch),
		})
	}

	// Sort the matches by their balance
	slices.SortStableFunc(predictions, func(a, b PredictedMatch) int {
		return int((a.Draw - b.Draw) * 1000)
	})
	// Sort so that highest draw probability is first
	slices.Reverse(predictions)

	// Sort by matches that have players who have been waiting more than half the Matchmaking timeout
	// This is to prevent players from waiting too long

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

func (*skillBasedMatchmaker) PredictDraw(teams []RatedEntryTeam) float64 {
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

func (m *skillBasedMatchmaker) CreateBalancedMatch(groups [][]*RatedEntry, teamSize int) (RatedEntryTeam, RatedEntryTeam) {
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

func (m *skillBasedMatchmaker) BalancedMatchFromCandidate(candidate []runtime.MatchmakerEntry) RatedMatch {
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
	return RatedMatch{team1, team2}
}

func removeDuplicateRosters(candidates [][]runtime.MatchmakerEntry) ([][]runtime.MatchmakerEntry, int) {
	seenRosters := make(map[string]struct{})
	uniqueCandidates := make([][]runtime.MatchmakerEntry, 0, len(candidates))
	duplicates := 0
	for _, match := range candidates {

		roster := make([]string, 0, len(match))
		for _, e := range match {
			sessionID := e.GetPresence().GetSessionId()
			_ = sessionID
			roster = append(roster, e.GetPresence().GetSessionId())
		}

		slices.Sort(roster)
		rosterString := strings.Join(roster, ",")

		if _, ok := seenRosters[rosterString]; ok {
			duplicates++
			continue
		}
		seenRosters[rosterString] = struct{}{}
		uniqueCandidates = append(uniqueCandidates, match)
	}
	return uniqueCandidates, duplicates
}

func (m *skillBasedMatchmaker) eligibleServers(match []runtime.MatchmakerEntry) map[string]int {
	rttsByServer := make(map[string][]int)
	for _, entry := range match {
		props := entry.GetProperties()

		maxRTT := 500
		if rtt, ok := props["max_rtt"].(int); ok && rtt > 0 {
			maxRTT = rtt
		}

		for k, v := range props {
			if !strings.HasPrefix(k, "rtt_") {
				continue
			}

			if rtt, ok := v.(int); ok {
				if rtt > maxRTT {
					// Server is too far away from this player
					continue
				}
				rttsByServer[k] = append(rttsByServer[k], rtt)
			}
		}
	}

	average := make(map[string]int)
	for k, rtts := range rttsByServer {
		if len(rtts) != len(match) {
			// Server is unreachable to one or more players
			continue
		}

		mean := 0
		for _, rtt := range rtts {
			mean += rtt
		}
		average[k] = mean / len(rtts)
	}

	return average
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
