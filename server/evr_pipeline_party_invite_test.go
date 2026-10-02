package server

import (
	"errors"
	"fmt"
	"testing"

	"github.com/heroiclabs/nakama-common/runtime"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestRespondToInviteMissAnswersAnAcceptWithAJoinFailure(t *testing.T) {
	accept := respondToInviteMiss(1)
	if len(accept) != 1 {
		t.Fatalf("accept with no invite: got %d messages, want 1", len(accept))
	}
	failure, ok := accept[0].(*evr.SNSPartyJoinFailure)
	if !ok {
		t.Fatalf("accept with no invite: got %T, want *evr.SNSPartyJoinFailure", accept[0])
	}
	if failure.ErrorCode != 1 {
		t.Errorf("ErrorCode = %d, want 1 (unknown party)", failure.ErrorCode)
	}
	if _, err := evr.Marshal(failure); err != nil {
		t.Errorf("the reply does not marshal: %v", err)
	}
	if reject := respondToInviteMiss(0); len(reject) != 0 {
		t.Errorf("reject with no invite: got %d messages, want none", len(reject))
	}
}

func TestPartyJoinFailureCodeIsTheGamesCode(t *testing.T) {
	cases := []struct {
		err  error
		want uint8
	}{
		{runtime.ErrPartyFull, 5},
		{fmt.Errorf("join: %w", runtime.ErrPartyFull), 5},
		{ErrPartyNotFound, 1},
		{runtime.ErrPartyClosed, 1},
		{runtime.ErrPartyJoinRequestAlreadyMember, 2},
		{errors.New("anything else"), 2},
	}
	for _, c := range cases {
		if got := partyJoinFailureCode(c.err); got != c.want {
			t.Errorf("partyJoinFailureCode(%v) = %d, want %d", c.err, got, c.want)
		}
	}
}
