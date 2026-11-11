package server

import (
	"context"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// withDisableLoginMessage sets the service-wide login block for one test and restores what was there.
func withDisableLoginMessage(t *testing.T, message string) {
	t.Helper()
	saved := ServiceSettings()
	t.Cleanup(func() { ServiceSettingsUpdate(saved) })
	blocked := *saved
	blocked.DisableLoginMessage = message
	ServiceSettingsUpdate(&blocked)
}

// With disable_login_message set, a login gets exactly one message, the LoginFailure, and the handler
// stops there: no LoginSuccess, and no error (the failure was sent, so there is nothing left to
// report; a non-nil return is logged as a pipeline error for a login that was refused on purpose).
//
// The request's profile payload is empty, so on the unfixed code the handler fell through the block
// and returned "login profile is empty"; a request that got further would have reached LoginSuccess.
func TestLoginRequest_DisableLoginMessageBlocksLogin(t *testing.T) {
	withDisableLoginMessage(t, "back at noon")

	p := &EvrPipeline{}
	session, sent := newNilIdentityConfigSession(t, p)
	request := &evr.LoginRequest{XPID: evr.EvrId{PlatformCode: 4, AccountId: 123456789}}

	err := p.loginRequest(context.Background(), zap.NewNop(), session, request)

	require.NoError(t, err)
	require.Len(t, *sent, 1, "exactly one message: the failure; got %v", *sent)
	failure, ok := (*sent)[0].(*evr.LoginFailure)
	require.True(t, ok, "the one message must be a LoginFailure, got %T", (*sent)[0])
	require.Equal(t, request.XPID, failure.UserId)
	require.Equal(t, "System is Temporarily Unavailable:\nback at noon", failure.ErrorMessage)
	for _, m := range *sent {
		require.IsType(t, &evr.LoginFailure{}, m, "a blocked login must never be sent LoginSuccess or anything else")
	}
}

// With the block unset the handler is unchanged: it sends nothing before it looks at the request, and
// goes on to its own checks (here, the empty profile).
func TestLoginRequest_NoDisableLoginMessageIsUnchanged(t *testing.T) {
	withDisableLoginMessage(t, "")

	p := &EvrPipeline{}
	session, sent := newNilIdentityConfigSession(t, p)
	request := &evr.LoginRequest{XPID: evr.EvrId{PlatformCode: 4, AccountId: 123456789}}

	err := p.loginRequest(context.Background(), zap.NewNop(), session, request)

	require.EqualError(t, err, "login profile is empty")
	require.Empty(t, *sent)
}
