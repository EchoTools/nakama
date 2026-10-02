package server

import (
	"context"
	"net/http"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// A game client asks for another player's profile when it opens that player's page (a friend, a party
// member). When the game service has no profile for them, it answers with OtherUserProfileFailure 404.
// It used to send nothing, and the game client keeps the page open until the request resolves, so a
// friend's page stayed blank forever.
func TestOtherUserProfileMissIsAnswered404(t *testing.T) {
	s := newPartyMemberSession(t, "viewer", nil, nil, nil)
	s.format = SessionFormatEVR
	p := &EvrPipeline{
		db: stubDB(t), // every lookup fails: the profile is not found by EvrId or by Discord id
		nk: &RuntimeGoNakamaModule{metrics: &testMetrics{}, storageIndex: emptyStorageIndex{}},
	}
	unknown := evr.EvrId{PlatformCode: evr.OVR_ORG, AccountId: 900000000000000999}

	require.NoError(t, p.otherUserProfileRequest(s.Context(), loggerForTest(t), s, &evr.OtherUserProfileRequest{EvrId: unknown}))

	sent := drain(s.outgoingCh)
	require.Len(t, sent, 1, "the miss is answered")
	msgs, err := evr.ParsePacket(sent[0])
	require.NoError(t, err)
	require.Len(t, msgs, 1)
	failure, ok := msgs[0].(*evr.OtherUserProfileFailure)
	require.True(t, ok, "got %T", msgs[0])
	require.Equal(t, unknown, failure.EvrId, "the failure names the player asked for")
	require.Equal(t, uint64(http.StatusNotFound), failure.StatusCode)
}

// emptyStorageIndex finds nothing in any index.
type emptyStorageIndex struct{ StorageIndex }

func (emptyStorageIndex) List(context.Context, uuid.UUID, string, string, int, []string, string) (*api.StorageObjects, string, error) {
	return &api.StorageObjects{}, "", nil
}
