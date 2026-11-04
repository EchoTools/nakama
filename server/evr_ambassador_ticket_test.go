package server

import (
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/atomic"
)

func ambassadorTicketTestParams() *LobbySessionParameters {
	p := &LobbySessionParameters{
		UserID:               uuid.Must(uuid.NewV4()),
		GroupID:              uuid.Must(uuid.NewV4()),
		Mode:                 evr.ModeArenaPublic,
		EnableSBMM:           true,
		MatchmakingTimestamp: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC),
		HardDivision:         "Silver",
		latencyHistory:       atomic.NewPointer[LatencyHistory](nil),
	}
	p.SetRating(NewDefaultRating())
	return p
}

// Nothing ambassador-related may travel on the matchmaking ticket: the
// matchmaker is under overhaul and no ticket property may change what it does.
// The old is_ambassador property had no reader, and the division and rating_mu
// overrides that fed it are retired with the old /ambassador command.
func TestMatchmakingParameters_CarriesNoAmbassadorProperty(t *testing.T) {
	p := ambassadorTicketTestParams()
	_, str, num := p.MatchmakingParameters(&MatchmakingTicketParameters{MinCount: 8, MaxCount: 8, CountMultiple: 2})

	if v, ok := str["is_ambassador"]; ok {
		t.Errorf("ticket string properties carry is_ambassador=%q; want no ambassador property", v)
	}
	for k := range num {
		if k == "is_ambassador" {
			t.Errorf("ticket numeric properties carry %q", k)
		}
	}
}
