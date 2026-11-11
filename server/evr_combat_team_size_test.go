package server

// Public combat is 4v4, like arena.
//
// Combat used to be matchmade 1..5 per team (min_team_size=1, max_team_size=5,
// DefaultPublicCombatTeamSize=5, PlayerLimit=10). These tests build the ticket
// properties with the real LobbySessionParameters.MatchmakingParameters and
// feed them to the real matchmaker processor, so they pin what a combat ticket
// actually asks for rather than a constant in isolation.

import (
	"fmt"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/atomic"
)

const combatFailsafeSeconds = 300

func combatTicketProperties(t *testing.T) map[string]float64 {
	t.Helper()
	p := &LobbySessionParameters{
		Mode:                 evr.ModeCombatPublic,
		GroupID:              uuid.Must(uuid.NewV4()),
		UserID:               uuid.Must(uuid.NewV4()),
		MatchmakingTimestamp: time.Now().UTC(),
		FailsafeTimeout:      combatFailsafeSeconds * time.Second,
		PartySize:            atomic.NewInt64(1),
		latencyHistory:       atomic.NewPointer(NewLatencyHistory()),
	}
	ticketParams := DefaultMatchmakerTicketConfigs[evr.ModeCombatPublic]
	_, _, numeric := p.MatchmakingParameters(&ticketParams)
	return numeric
}

func TestCombatTicket_IsFourVersusFour(t *testing.T) {
	numeric := combatTicketProperties(t)

	assert.Equal(t, 4.0, numeric["min_team_size"])
	assert.Equal(t, 4.0, numeric["max_team_size"])
}

func TestCombatTeamSize_MatchesArena(t *testing.T) {
	assert.Equal(t, DefaultPublicArenaTeamSize, DefaultPublicCombatTeamSize)
	assert.Equal(t, 8, min(DefaultPublicCombatTeamSize*2, MatchLobbyMaxSize), "a combat match's PlayerLimit")
}

func TestCombatBackfillQuery_CapsAtEightPlayers(t *testing.T) {
	p := &LobbySessionParameters{
		Mode:                 evr.ModeCombatPublic,
		GroupID:              uuid.Must(uuid.NewV4()),
		MatchmakingTimestamp: time.Now().UTC(),
		PartySize:            atomic.NewInt64(1),
	}

	assert.Contains(t, p.BackfillSearchQuery(false, false), "+label.player_count:<=7")
}

// addCombatTickets submits n solo combat tickets carrying the properties a real
// combat ticket carries, aged waited.
func addCombatTickets(t *testing.T, matchmaker *LocalMatchmaker, n int, waited time.Duration) {
	t.Helper()
	numeric := combatTicketProperties(t)
	props := map[string]float64{
		"min_team_size":    numeric["min_team_size"],
		"max_team_size":    numeric["max_team_size"],
		"failsafe_timeout": numeric["failsafe_timeout"],
		"count_multiple":   numeric["count_multiple"],
		"max_count":        numeric["max_count"],
		"timestamp":        float64(time.Now().UTC().Add(-waited).Unix()),
	}
	for i := range n {
		addIntegrationProcessorTicketWithProps(t, matchmaker, fmt.Sprintf("combat4v4-%d-%02d", n, i),
			map[string]string{"game_mode": evr.ModeCombatPublic.String()}, props)
	}
}

func TestCombatMatchmaking_UndersizedUntilFailsafe(t *testing.T) {
	for _, n := range []int{2, 4, 6} {
		t.Run(fmt.Sprintf("%d players", n), func(t *testing.T) {
			logger := loggerForTest(t)

			// Fresh tickets: fewer than 8 players is undersized and must wait.
			matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
			defer cleanup()
			wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))
			addCombatTickets(t, matchmaker, n, 0)
			matched, _ := runProcessorCycle(matchmaker)
			require.Empty(t, matched, "%d combat players form no match before the failsafe timeout", n)

			// Oldest ticket past failsafe_timeout: the undersized match is allowed.
			matchmaker2, cleanup2 := createProcessorTestMatchmaker(t, logger)
			defer cleanup2()
			wireEvrProcessor(matchmaker2, NewRuntimeGoLogger(logger))
			addCombatTickets(t, matchmaker2, n, (combatFailsafeSeconds+100)*time.Second)
			matched, _ = runProcessorCycle(matchmaker2)
			require.Len(t, matched, 1, "%d combat players form a match once failsafe_timeout has passed", n)
			assert.Len(t, matched[0], n)
		})
	}
}

func TestCombatMatchmaking_EightFormsImmediately(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()
	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))
	addCombatTickets(t, matchmaker, 8, 0)

	matched, _ := runProcessorCycle(matchmaker)

	require.Len(t, matched, 1)
	assert.Len(t, matched[0], 8)
}

func TestCombatMatchmaking_TenIsNotFormed(t *testing.T) {
	logger := loggerForTest(t)
	matchmaker, cleanup := createProcessorTestMatchmaker(t, logger)
	defer cleanup()
	wireEvrProcessor(matchmaker, NewRuntimeGoLogger(logger))
	addCombatTickets(t, matchmaker, 10, 0)

	matched, _ := runProcessorCycle(matchmaker)

	require.Len(t, matched, 1, "8 of the 10 form a match; the other 2 are undersized and keep waiting")
	assert.Len(t, matched[0], 8, "max is 8")
}
