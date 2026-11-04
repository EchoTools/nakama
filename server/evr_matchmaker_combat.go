package server

import (
	"context"
	"hash/fnv"
	"math"
	"math/bits"
	"slices"
	"sort"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/intinig/go-openskill/rating"
	"github.com/intinig/go-openskill/types"
)

// Combat team sort (#775).
//
// Combat has no match-making step: the players in a combat match are whoever
// the matchmaker packed together (groupEntriesSequentially), and the prediction
// stage fills teams strongest-four-versus-rest. Once the match is assembled,
// sortCombatMatches re-sorts the SAME players onto the two teams OpenSkill rates
// closest to a draw.
//
// Arena never reaches this code: every entry point checks the mode first.

const (
	// DefaultCombatDrawTolerance is the draw-probability a party-keeping split may
	// fall short of the closest-to-draw split by, and still be taken. See
	// GlobalMatchmakingSettings.CombatDrawTolerance.
	//
	// 0.003 is about 3.5 mu of difference between the two teams' summed mu. Measured
	// with rating.PredictDraw, 4v4, every player at the default sigma 10/3: draw
	// probability is 0.0608 at a difference of 0, 0.0580 at 3, 0.0559 at 4, 0.0503
	// at 6, 0.0434 at 8 and 0.0284 at 12.
	DefaultCombatDrawTolerance = 0.003

	// DefaultCombatSplitPenaltyWeight is the mu-points charged per unit of a
	// moved player's recent-split score. See
	// GlobalMatchmakingSettings.CombatSplitPenaltyWeight.
	DefaultCombatSplitPenaltyWeight = 1.0

	// combatSplitHistoryDecay multiplies a player's recent-split score each time
	// they are sorted into a combat match with a party, before this match's split
	// is added. 0.8 puts a split at 0.8 after one more such match, 0.64 after two,
	// and below combatSplitHistoryFloor after about 20.
	combatSplitHistoryDecay = 0.8

	// combatSplitHistoryFloor is the score under which the record is stored as 0.
	combatSplitHistoryFloor = 0.01

	// combatSplitHistoryKey is the storage key of CombatSplitHistory, in the same
	// collection as the player's MatchmakingSettings ("config") and JoinDirective
	// ("join"): MatchmakerStorageCollection, evr_lobby_matchmake.go.
	combatSplitHistoryKey = "combat_splits"

	// combatSortMaxPlayers bounds the exhaustive split search: C(15,7) = 6,435
	// splits for 16 players.
	combatSortMaxPlayers = 16
)

// CombatSplitHistory is one player's recent-split score: how often, and how
// recently, the combat sort moved them away from their party. It decays by
// combatSplitHistoryDecay each combat match the player is sorted into with a
// party, and gains 1 when they are the one moved.
type CombatSplitHistory struct {
	Score float64 `json:"score"`

	meta StorableMetadata
}

func (CombatSplitHistory) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection: MatchmakerStorageCollection,
		Key:        combatSplitHistoryKey,
	}
}

func (h *CombatSplitHistory) SetStorageMeta(meta StorableMetadata) { h.meta = meta }

// combatPlayerRating is the rating the sort used for one player and where it
// came from: "ticket" (the ticket carried rating_mu), "loaded" (the combat
// TeamSkillRating, read at sort time), or "default" (no record, the read failed,
// or there is no storage; the default rating).
type combatPlayerRating struct {
	UserID string  `json:"user_id"`
	Mu     float64 `json:"mu"`
	Sigma  float64 `json:"sigma"`
	Source string  `json:"source"`
}

// combatSortDecision is what one combat sort did. It is the structured record
// logged per sort, and the input to the history update.
type combatSortDecision struct {
	GroupID      string               // the match's guild group
	Players      []combatPlayerRating // each player's rating used, session id order
	Sorted       bool                 // false when the match was left as it was (size outside 2..16, or odd)
	SessionIDs   []string             // the match's players, session id order
	UserIDs      []string             // same order as SessionIDs
	Parties      int                  // tickets with more than one player
	KeptParties  bool                 // no party was split
	Fallback     bool                 // the party-keeping split was outside the draw tolerance
	Moved        []string             // user ids moved away from their party
	DrawBefore   float64              // PredictDraw of the arrangement the match came in with
	DrawAfter    float64              // PredictDraw of the chosen split
	DrawBest     float64              // PredictDraw of the closest-to-draw split, parties ignored
	DrawKept     float64              // PredictDraw of the closest party-keeping split (0 if none exists)
	Cost         float64              // |dmu| + weight x penalty of the chosen split
	Splits       int                  // splits evaluated
	Tolerance    float64
	Weight       float64
	scoresBefore map[string]float64 // party members' recent-split scores before this sort
	partyUserIDs []string           // user ids of every player on a party ticket
}

// resortCombatTeams returns match's players ordered [blue..., orange...] with
// the draw-closest split chosen as follows.
//
//  1. Among the splits that keep every party together, take the one with the
//     smallest |sum(mu) - sum(mu)|. If its draw probability is within tolerance
//     of the closest-to-draw split's, that is the result.
//  2. Otherwise score every split as |dmu| + weight x the sum, over the players
//     moved away from their party, of (1 + that player's scores[userID]), and
//     take the lowest. The 1 is a base cost per moved player, so the fewest
//     parties are broken; the score decides who moves. A split party's "home" side
//     is the one that makes this sum smallest, so the cheaper member is the one
//     moved.
//
// For two teams, rating.PredictDraw depends on the split only through |dmu|
// (the summed sigma^2 is the same for every split), and falls as |dmu| grows,
// so the closest-to-draw split is the smallest |dmu| and the search is
// exhaustive: C(n-1, n/2-1) splits, 35 for 8 players, 6,435 for 16.
//
// match is not modified. Players are ordered by session id first, so the result
// does not depend on the order they arrive in.
func resortCombatTeams(match []runtime.MatchmakerEntry, opts *types.OpenSkillOptions, tolerance, weight float64, scores map[string]float64, loaded map[string]types.Rating) ([]runtime.MatchmakerEntry, combatSortDecision) {
	n := len(match)
	d := combatSortDecision{Tolerance: tolerance, Weight: weight}
	if n < 2 || n%2 != 0 || n > combatSortMaxPlayers {
		return slices.Clone(match), d
	}

	players := slices.Clone(match)
	sort.SliceStable(players, func(i, j int) bool {
		return players[i].GetPresence().GetSessionId() < players[j].GetPresence().GetSessionId()
	})
	ratings := combatRatings(players, opts, loaded)
	mus := make([]float64, n)
	for i, r := range ratings {
		mus[i] = r.Mu
	}
	d.GroupID, _ = players[0].GetProperties()["group_id"].(string)
	def := NewDefaultRating()
	for i, p := range players {
		uid := p.GetPresence().GetUserId()
		source := "default"
		if _, has := p.GetProperties()["rating_mu"].(float64); has {
			source = "ticket"
		} else if lr, ok := loaded[uid]; ok && (lr.Mu != def.Mu || lr.Sigma != def.Sigma) {
			source = "loaded"
		}
		d.Players = append(d.Players, combatPlayerRating{UserID: uid, Mu: ratings[i].Mu, Sigma: ratings[i].Sigma, Source: source})
	}

	d.Sorted = true
	d.SessionIDs = make([]string, n)
	d.UserIDs = make([]string, n)
	for i, p := range players {
		d.SessionIDs[i] = p.GetPresence().GetSessionId()
		d.UserIDs[i] = p.GetPresence().GetUserId()
	}

	// A party is a ticket with more than one player.
	byTicket := map[string][]int{}
	for i, p := range players {
		byTicket[p.GetTicket()] = append(byTicket[p.GetTicket()], i)
	}
	var parties [][]int
	for _, idx := range byTicket {
		if len(idx) > 1 {
			parties = append(parties, idx)
		}
	}
	sort.Slice(parties, func(i, j int) bool { return parties[i][0] < parties[j][0] })
	d.Parties = len(parties)
	for _, p := range parties {
		for _, i := range p {
			d.partyUserIDs = append(d.partyUserIDs, d.UserIDs[i])
		}
	}
	d.scoresBefore = map[string]float64{}
	for _, uid := range d.partyUserIDs {
		d.scoresBefore[uid] = scores[uid]
	}

	// movedFor reports the penalty and the moved players of one split.
	movedFor := func(mask uint32) (float64, []int) {
		var pen float64
		var moved []int
		for _, p := range parties {
			onBlue := 0
			for _, i := range p {
				if mask>>uint(i)&1 == 1 {
					onBlue++
				}
			}
			if onBlue == 0 || onBlue == len(p) {
				continue
			}
			// Home blue: the members on orange are moved. Home orange: the members on blue.
			var costBlue, costOrange float64
			for _, i := range p {
				if mask>>uint(i)&1 == 1 {
					costOrange += 1 + scores[d.UserIDs[i]]
				} else {
					costBlue += 1 + scores[d.UserIDs[i]]
				}
			}
			homeBlue := costBlue < costOrange
			if costBlue == costOrange {
				// Equal cost: the larger group stays home; if equal, the side of the
				// lowest session id does.
				onOrange := len(p) - onBlue
				switch {
				case onBlue != onOrange:
					homeBlue = onBlue > onOrange
				default:
					homeBlue = mask>>uint(p[0])&1 == 1
				}
			}
			if homeBlue {
				pen += costBlue
			} else {
				pen += costOrange
			}
			for _, i := range p {
				onB := mask>>uint(i)&1 == 1
				if onB != homeBlue {
					moved = append(moved, i)
				}
			}
		}
		return pen, moved
	}

	total := 0.0
	for _, mu := range mus {
		total += mu
	}
	half := n / 2
	absDelta := func(mask uint32) float64 {
		var blue float64
		for i := 0; i < n; i++ {
			if mask>>uint(i)&1 == 1 {
				blue += mus[i]
			}
		}
		return math.Abs(2*blue - total)
	}
	together := func(mask uint32) bool {
		for _, p := range parties {
			first := mask >> uint(p[0]) & 1
			for _, i := range p[1:] {
				if mask>>uint(i)&1 != first {
					return false
				}
			}
		}
		return true
	}

	var (
		bestAny, bestKept   uint32
		bestAnyD, bestKeptD = math.Inf(1), math.Inf(1)
		bestCost            = math.Inf(1)
		bestCostMask        uint32
		haveKept            bool
		splits              int
		limit               = uint32(1) << uint(n)
	)
	for mask := uint32(1); mask < limit; mask += 2 { // bit 0 set: player 0 is on blue
		if bits.OnesCount32(mask) != half {
			continue
		}
		splits++
		a := absDelta(mask)
		if a < bestAnyD {
			bestAnyD, bestAny = a, mask
		}
		if together(mask) && a < bestKeptD {
			bestKeptD, bestKept, haveKept = a, mask, true
		}
		pen, _ := movedFor(mask)
		if c := a + weight*pen; c < bestCost {
			bestCost, bestCostMask = c, mask
		}
	}
	d.Splits = splits

	drawOf := func(mask uint32) float64 {
		var blue, orange types.Team
		for i := 0; i < n; i++ {
			if mask>>uint(i)&1 == 1 {
				blue = append(blue, ratings[i])
			} else {
				orange = append(orange, ratings[i])
			}
		}
		return rating.PredictDraw([]types.Team{blue, orange}, opts)
	}

	d.DrawBest = drawOf(bestAny)
	chosen := bestAny
	switch {
	case len(parties) == 0:
		d.KeptParties = true
		d.Cost = bestAnyD
	case haveKept:
		d.DrawKept = drawOf(bestKept)
		if d.DrawBest-d.DrawKept <= tolerance {
			chosen = bestKept
			d.KeptParties = true
			d.Cost = bestKeptD
			break
		}
		d.Fallback = true
		chosen = bestCostMask
		d.Cost = bestCost
	default:
		d.Fallback = true
		chosen = bestCostMask
		d.Cost = bestCost
	}
	_, movedIdx := movedFor(chosen)
	for _, i := range movedIdx {
		d.Moved = append(d.Moved, d.UserIDs[i])
	}
	d.KeptParties = len(d.Moved) == 0
	d.DrawAfter = drawOf(chosen)
	d.DrawBefore = combatDrawOfArrangement(match, opts, loaded)

	// Which colour is which does not matter to the rating. Alternate it by a
	// hash of the players so the player with the lowest session id is not always
	// blue.
	h := fnv.New32a()
	h.Write([]byte(strings.Join(d.SessionIDs, ",")))
	flip := h.Sum32()&1 == 1

	out := make([]runtime.MatchmakerEntry, 0, n)
	for _, wantBlue := range []bool{!flip, flip} {
		for i, p := range players {
			if (chosen>>uint(i)&1 == 1) == wantBlue {
				out = append(out, p)
			}
		}
	}
	return out, d
}

// combatOpenSkillOptions returns opts with every field set to something
// rating.PredictDraw can use. Settings that were never filled in (a process that
// has not loaded Global/settings yet) leave Z, Mu and Sigma at 0, which makes
// every draw probability 0 and the tolerance meaningless; fall back to the
// service defaults then.
func combatOpenSkillOptions(opts *types.OpenSkillOptions) *types.OpenSkillOptions {
	def := NewDefaultRating()
	out := &types.OpenSkillOptions{Z: &def.Z, Mu: &def.Mu, Sigma: &def.Sigma}
	if opts == nil {
		return out
	}
	if opts.Z != nil && *opts.Z > 0 {
		out.Z = opts.Z
	}
	if opts.Mu != nil && *opts.Mu > 0 {
		out.Mu = opts.Mu
	}
	if opts.Sigma != nil && *opts.Sigma > 0 {
		out.Sigma = opts.Sigma
	}
	return out
}

// combatDrawOfArrangement is rating.PredictDraw of a match as it is laid out:
// the first half is blue, the second orange.
func combatDrawOfArrangement(match []runtime.MatchmakerEntry, opts *types.OpenSkillOptions, loaded map[string]types.Rating) float64 {
	n := len(match)
	if n < 2 || n%2 != 0 {
		return 0
	}
	r := combatRatings(match, opts, loaded)
	return rating.PredictDraw([]types.Team{r[:n/2], r[n/2:]}, opts)
}

// combatRatings are the players' ratings: the ticket's rating_mu / rating_sigma
// where it carries them, and for a player in loaded (one whose ticket carried
// none) the rating loaded from their combat TeamSkillRating.
func combatRatings(players []runtime.MatchmakerEntry, opts *types.OpenSkillOptions, loaded map[string]types.Rating) []types.Rating {
	ratings := MatchmakerEntries(players).Ratings(opts)
	for i, p := range players {
		if r, ok := loaded[p.GetPresence().GetUserId()]; ok {
			ratings[i] = r
		}
	}
	return ratings
}

// loadCombatRatings loads the combat TeamSkillRating of every player in match
// whose ticket carries no rating_mu (the ticket only carries one when
// enable_skill_based_mm is on, evr_lobby_parameters.go). It is the loader the
// ticket uses, MatchmakingRatingLoad, for the combat mode; a player with no
// record, or whose record cannot be read, gets the default rating. With a nil nk
// nothing is loaded and such a player is the default rating.
func loadCombatRatings(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, match []runtime.MatchmakerEntry) map[string]types.Rating {
	if nk == nil {
		return nil
	}
	var loaded map[string]types.Rating
	for _, e := range match {
		props := e.GetProperties()
		if _, has := props["rating_mu"].(float64); has {
			continue
		}
		groupID, _ := props["group_id"].(string)
		uid := e.GetPresence().GetUserId()
		r, err := MatchmakingRatingLoad(ctx, nk, uid, groupID, evr.ModeCombatPublic)
		if err != nil {
			logger.WithFields(map[string]any{"user_id": uid, "group_id": groupID, "error": err.Error()}).Warn("Failed to load combat rating; using the default")
			r = NewDefaultRating()
		}
		if loaded == nil {
			loaded = map[string]types.Rating{}
		}
		loaded[uid] = r
	}
	return loaded
}

// isCombatMatch reports whether a made match is public combat, by the same
// per-entry game_mode property groupEntriesSequentially and isCombatCandidate read.
func isCombatMatch(match []runtime.MatchmakerEntry) bool {
	if len(match) == 0 {
		return false
	}
	modeStr, _ := match[0].GetProperties()["game_mode"].(string)
	return modeStr == evr.ModeCombatPublic.String()
}

// loadCombatSplitScores reads the recent-split score of every player on a party
// ticket in match. A player with no record, and every player when nk is nil, scores 0.
func loadCombatSplitScores(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, match []runtime.MatchmakerEntry) map[string]float64 {
	scores := map[string]float64{}
	if nk == nil {
		return scores
	}
	perTicket := map[string]int{}
	for _, e := range match {
		perTicket[e.GetTicket()]++
	}
	for _, e := range match {
		if perTicket[e.GetTicket()] < 2 {
			continue
		}
		uid := e.GetPresence().GetUserId()
		if _, done := scores[uid]; done {
			continue
		}
		h := &CombatSplitHistory{}
		if err := StorableRead(ctx, nk, uid, h, false); err != nil {
			if !isStorageNotFoundError(err) {
				logger.WithFields(map[string]any{"user_id": uid, "error": err.Error()}).Warn("Failed to read combat split history; scoring 0")
			}
			scores[uid] = 0
			continue
		}
		scores[uid] = h.Score
	}
	return scores
}

// sortCombatMatches re-sorts every combat match in matches onto the draw-closest
// teams and returns the new matches (new slices; matches and the slices in it are
// not modified) with one decision per sorted match. Other modes pass through
// untouched. The decisions are logged and recorded by finishCombatSorts, once it
// is known which matches stand.
func sortCombatMatches(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, matches [][]runtime.MatchmakerEntry, opts *types.OpenSkillOptions, settings *GlobalMatchmakingSettings) ([][]runtime.MatchmakerEntry, []combatSortDecision) {
	tolerance, weight := DefaultCombatDrawTolerance, DefaultCombatSplitPenaltyWeight
	if settings != nil {
		if settings.CombatDrawTolerance > 0 {
			tolerance = settings.CombatDrawTolerance
		}
		if settings.CombatSplitPenaltyWeight > 0 {
			weight = settings.CombatSplitPenaltyWeight
		}
	}

	opts = combatOpenSkillOptions(opts)

	var decisions []combatSortDecision
	var out [][]runtime.MatchmakerEntry
	for i, match := range matches {
		if !isCombatMatch(match) {
			continue
		}
		if out == nil {
			out = slices.Clone(matches)
		}
		scores := loadCombatSplitScores(ctx, nk, logger, match)
		loaded := loadCombatRatings(ctx, nk, logger, match)
		sorted, d := resortCombatTeams(match, opts, tolerance, weight, scores, loaded)
		out[i] = sorted
		if !d.Sorted {
			continue
		}
		decisions = append(decisions, d)
	}
	if out == nil {
		return matches, nil
	}
	return out, decisions
}

// nextCombatSplitScore is a party member's score after one more combat match.
func nextCombatSplitScore(old float64, moved bool) float64 {
	v := old * combatSplitHistoryDecay
	if moved {
		v++
	}
	if v < combatSplitHistoryFloor {
		return 0
	}
	return v
}

// persistCombatSplitHistory records the decisions of the combat matches that
// were actually made: every party member's score decays, and the players moved
// away from their party gain 1. A record is written only when its value changes.
// Failures are logged at Warn and do not fail the matchmaker.
func persistCombatSplitHistory(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, decisions []combatSortDecision) {
	for _, d := range decisions {
		persistCombatDecision(ctx, nk, logger, d)
	}
}

// persistCombatDecision records one decision's history and reports whether every
// write that was needed succeeded (false with a nil nk).
func persistCombatDecision(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, d combatSortDecision) bool {
	if nk == nil {
		return false
	}
	moved := make(map[string]bool, len(d.Moved))
	for _, uid := range d.Moved {
		moved[uid] = true
	}
	ok := true
	for _, uid := range d.partyUserIDs {
		old := d.scoresBefore[uid]
		next := nextCombatSplitScore(old, moved[uid])
		if next == old {
			continue
		}
		h := &CombatSplitHistory{Score: next}
		if err := StorableWrite(ctx, nk, uid, h); err != nil {
			ok = false
			logger.WithFields(map[string]any{"user_id": uid, "error": err.Error()}).Warn("Failed to write combat split history")
		}
	}
	return ok
}

// finishCombatSorts logs one record per combat sort and records the history of
// the sorts whose match stands. It runs after EvrMatchmakerFn's backfill-first
// step may have dropped made matches, so the record says what became of the sort:
// made (the match was returned to the matchmaker) and history_persisted. A match
// can still be discarded after this (LocalMatchmaker.Process drops a match whose
// tickets are gone, and LobbyBuilder.buildMatch can fail and reinsert the
// tickets); the history has been counted by then.
func finishCombatSorts(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, decisions []combatSortDecision, matches [][]runtime.MatchmakerEntry) {
	stands := map[string]bool{}
	for _, d := range combatDecisionsStillMade(decisions, matches) {
		stands[strings.Join(d.SessionIDs, ",")] = true
	}
	for _, d := range decisions {
		made := stands[strings.Join(d.SessionIDs, ",")]
		persisted := false
		if made {
			persisted = persistCombatDecision(ctx, nk, logger, d)
		}
		logger.WithFields(map[string]any{
			"mode":              evr.ModeCombatPublic.String(),
			"group_id":          d.GroupID,
			"session_ids":       d.SessionIDs,
			"user_ids":          d.UserIDs,
			"players":           d.Players,
			"parties":           d.Parties,
			"kept_parties":      d.KeptParties,
			"fallback":          d.Fallback,
			"moved":             d.Moved,
			"draw_before":       d.DrawBefore,
			"draw_after":        d.DrawAfter,
			"draw_best":         d.DrawBest,
			"draw_kept":         d.DrawKept,
			"cost":              d.Cost,
			"splits":            d.Splits,
			"tolerance":         d.Tolerance,
			"weight":            d.Weight,
			"made":              made,
			"history_persisted": persisted,
		}).Info("Combat teams sorted")
	}
}

// combatDecisionsStillMade keeps the decisions whose match is still in matches.
// The backfill-first path in EvrMatchmakerFn can drop made matches after the sort.
func combatDecisionsStillMade(decisions []combatSortDecision, matches [][]runtime.MatchmakerEntry) []combatSortDecision {
	if len(decisions) == 0 || len(matches) == 0 {
		return nil
	}
	made := map[string]struct{}{}
	for _, m := range matches {
		ids := make([]string, 0, len(m))
		for _, e := range m {
			ids = append(ids, e.GetPresence().GetSessionId())
		}
		sort.Strings(ids)
		made[strings.Join(ids, ",")] = struct{}{}
	}
	var kept []combatSortDecision
	for _, d := range decisions {
		if _, ok := made[strings.Join(d.SessionIDs, ",")]; ok {
			kept = append(kept, d)
		}
	}
	return kept
}
