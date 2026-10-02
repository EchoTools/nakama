package server

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

// The game client crashes on SNSEarlyQuitConfig (right after login, an int3 in its stack allocator), so the
// game service does not send it from any path: SendEarlyQuitConfigOnLogin, which login and the Discord early
// quit command both use, sends nothing.
func TestEarlyQuitConfigIsNotSentToTheGameClient(t *testing.T) {
	session := capturingSession(t, newMockMatchmakingTracker())
	trigger := &SNSEarlyQuitMessageTrigger{logger: loggerForTest(t)}

	require.NoError(t, trigger.SendEarlyQuitConfigOnLogin(context.Background(), session))
	require.Empty(t, drain(session.outgoingCh), "the config reached the game client")
	require.NoError(t, trigger.SendEarlyQuitConfigOnLogin(context.Background(), nil))
}
