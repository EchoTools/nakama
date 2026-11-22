package server

// #747: the social-lobby search must never pick a lobby that is terminating or
// terminated.
//
//   - TestSocialSearch_SkipsCandidateThatNoLongerExists drives the real pick loop
//     (lobbyFindOrCreateSocial) with a candidate whose JoinAttempt reports
//     found == false, the way a match removed between the list and the join does.
//   - TestMatchRegistry_ListAfterRemoveBeforeFlush settles the issue's prediction at
//     the registry: does the list still return a match after RemoveMatch, before the
//     index flush?
//   - TestMatchShutdown_FlushesClosedLabelToIndex pins the smaller change: the
//     shutdown's label update reaches the index at once, not on the next tick.

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// TestSocialSearch_SkipsCandidateThatNoLongerExists: the first candidate was listed
// and then terminated before the join (JoinAttempt: found == false, which
// LobbyJoinEntrants turns into ServerDoesNotExist). The search must go on to the
// next candidate; it must not end the whole search at the first one.
func TestSocialSearch_SkipsCandidateThatNoLongerExists(t *testing.T) {
	f := newPartyCapFixture(t)
	player := f.session("player", false)
	lobbyParams := makeMatchmakeTestLobbyParams(player.userID, f.groupID, evr.ModeSocialPublic, 1)

	dead := f.socialLabel(0, 0)
	next := f.socialLabel(0, 0)
	f.register(dead, next) // list order: dead first

	err := f.env.ep.lobbyFindOrCreateSocial(player.Context(), f.logger, player, lobbyParams, nil, f.presence(player))

	require.Equal(t, 1, f.joinsTo(dead.ID), "the dead candidate is attempted once")
	require.Equal(t, 1, f.joinsTo(next.ID),
		"the search ended at the dead candidate instead of trying the next one (err=%v)", err)
	f.requireLog(zapcore.DebugLevel, "Match no longer exists, skipping.", map[string]any{"mid": dead.ID.UUID.String()})
}

const terminatingTestLabelInterval = 3600 * 1000 // ms: the ticker never fires; flushes are explicit

// newTerminatingTestMatch creates a match in a real LocalMatchRegistry with an open
// social label, flushed to the index.
func newTerminatingTestMatch(t *testing.T) (*LocalMatchRegistry, MatchID, uuid.UUID, string) {
	t.Helper()
	logger := loggerForTest(t)
	reg, createFn, _, err := createTestMatchRegistryWithInterval(t, logger, terminatingTestLabelInterval)
	require.NoError(t, err)

	groupID := uuid.Must(uuid.NewV4())
	label := &MatchLabel{
		Open:      true,
		LobbyType: PublicLobby,
		Mode:      evr.ModeSocialPublic,
		Level:     evr.LevelSocial,
		GroupID:   &groupID,
		MaxSize:   SocialLobbyMaxSize,
		TeamSize:  SocialLobbyMaxSize,
		StartTime: time.Now().UTC(),
		CreatedAt: time.Now().UTC(),
		Players:   make([]PlayerInfo, 0),
	}
	b, err := json.Marshal(label)
	require.NoError(t, err)

	idStr, err := reg.CreateMatch(context.Background(), createFn, "go", map[string]interface{}{"label": string(b)})
	require.NoError(t, err)
	mid := MatchIDFromStringOrNil(idStr)

	query := fmt.Sprintf("+label.open:T +label.mode:%s +label.group_id:%s",
		evr.ModeSocialPublic.String(), Query.QuoteStringValue(groupID.String()))
	return reg, mid, groupID, query
}

func listSocialMatches(t *testing.T, reg *LocalMatchRegistry, query string) int {
	t.Helper()
	minSize, maxSize := 0, MatchLobbyMaxSize
	matches, _, err := reg.ListMatches(context.Background(), 100, nil, nil,
		wrapperspb.Int32(int32(minSize)), wrapperspb.Int32(int32(maxSize)), wrapperspb.String(query), nil)
	require.NoError(t, err)
	return len(matches)
}

// TestMatchRegistry_ListAfterRemoveBeforeFlush: create a match, flush the index, then
// RemoveMatch (what MatchTerminate does) and list before the next flush. The issue
// predicted the match is still listed (the removal is queued, not written).
func TestMatchRegistry_ListAfterRemoveBeforeFlush(t *testing.T) {
	reg, mid, _, query := newTerminatingTestMatch(t)

	reg.FlushPendingLabelUpdates()
	require.Equal(t, 1, listSocialMatches(t, reg, query), "precondition: the flushed match is listed")

	reg.RemoveMatch(mid.UUID, PresenceStream{Mode: StreamModeMatchAuthoritative, Subject: mid.UUID, Label: mid.Node})

	require.Equal(t, 0, listSocialMatches(t, reg, query),
		"a removed match is listed before the index flush")
}

// shutdownLabelDispatcher forwards MatchLabelUpdate to the registry as the real
// match dispatcher does, so the label update lands in pendingUpdates.
type shutdownLabelDispatcher struct {
	drainTestDispatcher
	reg  *LocalMatchRegistry
	mid  MatchID
	tick int
}

func (d *shutdownLabelDispatcher) MatchLabelUpdate(label string) error {
	return d.reg.UpdateMatchLabel(d.mid.UUID, d.tick, "module", label, time.Now().UnixNano())
}

// TestMatchShutdown_FlushesClosedLabelToIndex: MatchShutdown sets Open = false and
// updates the label. Until that update is flushed, the index still says open:T and a
// search lists the lobby. After MatchShutdown returns, the index must say open:F.
func TestMatchShutdown_FlushesClosedLabelToIndex(t *testing.T) {
	reg, mid, groupID, query := newTerminatingTestMatch(t)
	reg.FlushPendingLabelUpdates()
	require.Equal(t, 1, listSocialMatches(t, reg, query), "precondition: the open match is listed")

	state, _ := drainTestState(0)
	state.ID = mid
	state.GroupID = &groupID
	// drainTestState's ModeSocialNPE stays: it is outside ValidLeaderboardModes, so the
	// shutdown writes no leaderboard record. The index entry is replaced by id, so the
	// mode in the new label does not matter to the query.
	state.Open = true

	// MatchShutdown reaches the registry through the node module, as production does.
	nk := &RuntimeGoNakamaModule{logger: loggerForTest(t), matchRegistry: reg, metrics: &testMetrics{}, db: stubDB(t)}
	var _ runtime.NakamaModule = nk
	dispatcher := &shutdownLabelDispatcher{reg: reg, mid: mid, tick: 10}

	m := &EvrMatch{}
	m.MatchShutdown(context.Background(), drainTestLogger(), nil, nk, dispatcher, 100, state, 30)

	require.False(t, state.Open, "shutdown closes the match")
	require.Equal(t, 0, listSocialMatches(t, reg, query),
		"the closed label was not flushed: the index still lists the terminating lobby")
}
