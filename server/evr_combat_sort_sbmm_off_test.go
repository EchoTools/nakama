package server

// Combat sort with SBMM off (#775). The ticket carries rating_mu only when
// enable_skill_based_mm is on (evr_lobby_parameters.go); every player always has
// a combat rating (the default when there is no record), so the sort loads it
// at sort time instead of treating a ticket without one as everybody equal.

import (
	"context"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// combatRatingNK serves combat TeamSkillRating mu/sigma records from a map,
// the way MatchmakingRatingLoad reads them.
type combatRatingNK struct {
	*evrTestNakamaModule
	groupID string
	records map[string]map[string]float64 // userID -> stat name -> value
	loads   int
}

func (n *combatRatingNK) LeaderboardRecordsList(_ context.Context, id string, ownerIDs []string, _ int, _ string, _ int64) ([]*api.LeaderboardRecord, []*api.LeaderboardRecord, string, string, error) {
	n.loads++
	for stat := range map[string]struct{}{TeamSkillRatingMuStatisticID: {}, TeamSkillRatingSigmaStatisticID: {}} {
		if id != StatisticBoardID(n.groupID, evr.ModeCombatPublic, stat, evr.ResetScheduleAllTime) {
			continue
		}
		for _, o := range ownerIDs {
			if v, ok := n.records[o][stat]; ok {
				score, sub, err := Float64ToScore(v)
				if err != nil {
					return nil, nil, "", "", err
				}
				return nil, []*api.LeaderboardRecord{{LeaderboardId: id, OwnerId: o, Score: score, Subscore: sub}}, "", "", nil
			}
		}
	}
	return nil, nil, "", "", nil
}

// SBMM off: no ticket carries a rating. Two players have a strong stored combat
// rating, six have no record (the default). Both strong players have the lowest
// session ids, so a sort that sees everyone as equal (lowest split in session-id
// order) puts them on the same team. The sort must load their ratings and separate
// them. Red before the loader: the strong pair stays together.
func TestCombatSort_SBMMOff_LoadsCombatRatings(t *testing.T) {
	const group = "00000000-0000-4000-8000-0000000c0de1" // the group_id combatSortEntry sets
	es := combatSolos(-1, -1, -1, -1, -1, -1, -1, -1)    // no rating properties
	nk := &combatRatingNK{evrTestNakamaModule: newEvrTestNakamaModule(), groupID: group, records: map[string]map[string]float64{
		"u-p00": {TeamSkillRatingMuStatisticID: 40, TeamSkillRatingSigmaStatisticID: 10.0 / 3.0},
		"u-p01": {TeamSkillRatingMuStatisticID: 40, TeamSkillRatingSigmaStatisticID: 10.0 / 3.0},
	}}

	m := NewSkillBasedMatchmaker()
	_, matches, _, _, decisions := m.processPotentialMatchesWith(context.Background(), nk, NewRuntimeGoLogger(zap.NewNop()), asRuntime(es))
	if len(matches) != 1 || len(matches[0]) != 8 {
		t.Fatalf("expected one 8-player match, got %s", memberOrder(matches))
	}
	if sameTeam(matches[0], "p00", "p01") {
		t.Errorf("the two strong players were left on the same team: %s", memberOrder(matches))
	}
	if len(decisions) != 1 || decisions[0].DrawAfter <= decisions[0].DrawBefore {
		t.Errorf("sort did not improve the draw: %+v", decisions)
	}
	if nk.loads == 0 {
		t.Errorf("no combat rating was loaded")
	}
}

// A ticket that carries a rating is not loaded again.
func TestCombatSort_TicketRatingIsNotReloaded(t *testing.T) {
	nk := &combatRatingNK{evrTestNakamaModule: newEvrTestNakamaModule(), groupID: "00000000-0000-4000-8000-0000000c0de1"}
	m := NewSkillBasedMatchmaker()
	m.processPotentialMatchesWith(context.Background(), nk, NewRuntimeGoLogger(zap.NewNop()), asRuntime(combatSolos(5, 8, 12, 15, 20, 24, 31, 40)))
	if nk.loads != 0 {
		t.Errorf("rated tickets caused %d rating loads", nk.loads)
	}
}
