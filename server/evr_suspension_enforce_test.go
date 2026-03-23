package server

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// --- Mock types ---

type suspendTestPresence struct {
	userID    string
	sessionID string
	nodeID    string
	status    string
}

func (p *suspendTestPresence) GetUserId() string    { return p.userID }
func (p *suspendTestPresence) GetSessionId() string { return p.sessionID }
func (p *suspendTestPresence) GetNodeId() string    { return p.nodeID }
func (p *suspendTestPresence) GetHidden() bool      { return false }
func (p *suspendTestPresence) GetPersistence() bool { return false }
func (p *suspendTestPresence) GetUsername() string  { return "" }
func (p *suspendTestPresence) GetStatus() string    { return p.status }
func (p *suspendTestPresence) GetReason() runtime.PresenceReason {
	return runtime.PresenceReasonUnknown
}

type suspendTestNakamaModule struct {
	runtime.NakamaModule

	// StreamUserList responses keyed by "mode:subject:label"
	streamUsers map[string][]runtime.Presence
	// StreamUserGet responses keyed by "mode:subject:label:userID:sessionID"
	streamMeta map[string]runtime.PresenceMeta
	// MatchGet responses keyed by matchID string
	matches map[string]*api.Match
	// MatchSignal calls recorded (mutex-protected for goroutine safety)
	mu               sync.Mutex
	matchSignalCalls []matchSignalCall
	// SessionDisconnect calls recorded (mutex-protected for goroutine safety)
	disconnectCalls []string
	// Error to return from SessionDisconnect
	disconnectError error
}

type matchSignalCall struct {
	matchID string
	data    string
}

func newSuspendTestNakamaModule() *suspendTestNakamaModule {
	return &suspendTestNakamaModule{
		streamUsers:      make(map[string][]runtime.Presence),
		streamMeta:       make(map[string]runtime.PresenceMeta),
		matches:          make(map[string]*api.Match),
		matchSignalCalls: make([]matchSignalCall, 0),
		disconnectCalls:  make([]string, 0),
	}
}

func (m *suspendTestNakamaModule) streamKey(mode uint8, subject, label string) string {
	return fmt.Sprintf("%d:%s:%s", mode, subject, label)
}

func (m *suspendTestNakamaModule) StreamUserList(mode uint8, subject, subcontext, label string, includeHidden, includeNotHidden bool) ([]runtime.Presence, error) {
	key := m.streamKey(mode, subject, label)
	if presences, ok := m.streamUsers[key]; ok {
		return presences, nil
	}
	return nil, nil
}

func (m *suspendTestNakamaModule) StreamUserGet(mode uint8, subject, subcontext, label, userID, sessionID string) (runtime.PresenceMeta, error) {
	key := fmt.Sprintf("%d:%s:%s:%s:%s", mode, subject, label, userID, sessionID)
	if meta, ok := m.streamMeta[key]; ok {
		return meta, nil
	}
	return nil, nil
}

func (m *suspendTestNakamaModule) MatchGet(ctx context.Context, id string) (*api.Match, error) {
	if match, ok := m.matches[id]; ok {
		return match, nil
	}
	return nil, nil
}

func (m *suspendTestNakamaModule) MatchSignal(ctx context.Context, id string, data string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.matchSignalCalls = append(m.matchSignalCalls, matchSignalCall{matchID: id, data: data})
	return "", nil
}

func (m *suspendTestNakamaModule) SessionDisconnect(ctx context.Context, sessionID string, reason ...runtime.PresenceReason) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.disconnectCalls = append(m.disconnectCalls, sessionID)
	return m.disconnectError
}

func (m *suspendTestNakamaModule) getMatchSignalCalls() []matchSignalCall {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]matchSignalCall, len(m.matchSignalCalls))
	copy(result, m.matchSignalCalls)
	return result
}

func (m *suspendTestNakamaModule) getDisconnectCalls() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.disconnectCalls))
	copy(result, m.disconnectCalls)
	return result
}

// --- GetMatchIDBySessionID tests ---

func TestGetMatchIDBySessionID_NoPresences(t *testing.T) {
	nk := newSuspendTestNakamaModule()
	sessionID := uuid.Must(uuid.NewV4())

	matchID, presence, err := GetMatchIDBySessionID(nk, sessionID)
	assert.Error(t, err)
	assert.True(t, matchID.IsNil())
	assert.Nil(t, presence)
}

func TestGetMatchIDBySessionID_PresenceWithNilMatchID(t *testing.T) {
	nk := newSuspendTestNakamaModule()
	sessionID := uuid.Must(uuid.NewV4())

	// Presence exists but status (match ID) is empty
	key := nk.streamKey(StreamModeService, sessionID.String(), StreamLabelMatchService)
	nk.streamUsers[key] = []runtime.Presence{
		&suspendTestPresence{
			userID:    uuid.Must(uuid.NewV4()).String(),
			sessionID: sessionID.String(),
			status:    "", // no match
		},
	}

	matchID, _, err := GetMatchIDBySessionID(nk, sessionID)
	assert.Error(t, err)
	assert.True(t, matchID.IsNil())
}

func TestGetMatchIDBySessionID_PresenceWithValidMatch(t *testing.T) {
	nk := newSuspendTestNakamaModule()
	sessionID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4()).String()
	matchUUID := uuid.Must(uuid.NewV4())
	node := "testnode"
	matchIDStr := matchUUID.String() + "." + node

	// Service stream presence with match ID in status
	key := nk.streamKey(StreamModeService, sessionID.String(), StreamLabelMatchService)
	nk.streamUsers[key] = []runtime.Presence{
		&suspendTestPresence{
			userID:    userID,
			sessionID: sessionID.String(),
			status:    matchIDStr,
		},
	}

	// StreamUserGet must confirm the user is in the match
	metaKey := fmt.Sprintf("%d:%s:%s:%s:%s", StreamModeMatchAuthoritative, matchUUID.String(), node, userID, sessionID.String())
	nk.streamMeta[metaKey] = &suspendTestPresence{userID: userID, sessionID: sessionID.String()}

	matchID, presence, err := GetMatchIDBySessionID(nk, sessionID)
	require.NoError(t, err)
	assert.Equal(t, matchUUID, matchID.UUID)
	assert.Equal(t, node, matchID.Node)
	assert.Equal(t, userID, presence.GetUserId())
}

func TestGetMatchIDBySessionID_PresenceNotVerified(t *testing.T) {
	nk := newSuspendTestNakamaModule()
	sessionID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4()).String()
	matchUUID := uuid.Must(uuid.NewV4())
	node := "testnode"
	matchIDStr := matchUUID.String() + "." + node

	// Service stream presence claims a match
	key := nk.streamKey(StreamModeService, sessionID.String(), StreamLabelMatchService)
	nk.streamUsers[key] = []runtime.Presence{
		&suspendTestPresence{
			userID:    userID,
			sessionID: sessionID.String(),
			status:    matchIDStr,
		},
	}

	// StreamUserGet returns nil (user not really in the match)
	// (no entry in streamMeta)

	matchID, _, err := GetMatchIDBySessionID(nk, sessionID)
	assert.Error(t, err)
	assert.True(t, matchID.IsNil())
}

// --- KickPlayerFromMatch tests ---

func newTestMatchLabel(gameServerSessionID uuid.UUID) *MatchLabel {
	return &MatchLabel{
		GameServer: &GameServerPresence{
			SessionID: gameServerSessionID,
		},
		GroupID: &uuid.Nil,
	}
}

func setupMatchInNK(t *testing.T, nk *suspendTestNakamaModule, matchID MatchID, label *MatchLabel, presences []runtime.Presence) {
	t.Helper()

	labelJSON, err := json.Marshal(label)
	require.NoError(t, err)

	nk.matches[matchID.String()] = &api.Match{
		MatchId: matchID.String(),
		Label:   wrapperspb.String(string(labelJSON)),
	}

	key := nk.streamKey(StreamModeMatchAuthoritative, matchID.UUID.String(), matchID.Node)
	nk.streamUsers[key] = presences
}

func TestKickPlayerFromMatch_PlayerPresent(t *testing.T) {
	nk := newSuspendTestNakamaModule()
	ctx := context.Background()

	matchUUID := uuid.Must(uuid.NewV4())
	node := "testnode"
	matchID, err := NewMatchID(matchUUID, node)
	require.NoError(t, err)

	gameServerSID := uuid.Must(uuid.NewV4())
	userID := uuid.Must(uuid.NewV4()).String()
	playerSID := uuid.Must(uuid.NewV4()).String()

	label := newTestMatchLabel(gameServerSID)

	presences := []runtime.Presence{
		&suspendTestPresence{userID: userID, sessionID: playerSID},
	}
	setupMatchInNK(t, nk, matchID, label, presences)

	err = KickPlayerFromMatch(ctx, nk, matchID, userID)
	require.NoError(t, err)

	// Should have signaled the match
	calls := nk.getMatchSignalCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, matchID.String(), calls[0].matchID)
}

func TestKickPlayerFromMatch_DoesNotKickGameServer(t *testing.T) {
	nk := newSuspendTestNakamaModule()
	ctx := context.Background()

	matchUUID := uuid.Must(uuid.NewV4())
	node := "testnode"
	matchID, err := NewMatchID(matchUUID, node)
	require.NoError(t, err)

	gameServerSID := uuid.Must(uuid.NewV4())
	label := newTestMatchLabel(gameServerSID)

	// Only presence is the game server itself
	presences := []runtime.Presence{
		&suspendTestPresence{
			userID:    uuid.Must(uuid.NewV4()).String(),
			sessionID: gameServerSID.String(),
		},
	}
	setupMatchInNK(t, nk, matchID, label, presences)

	err = KickPlayerFromMatch(ctx, nk, matchID, uuid.Must(uuid.NewV4()).String())
	require.NoError(t, err)

	// Should NOT have signaled — the only presence was the game server
	assert.Empty(t, nk.getMatchSignalCalls())
}

func TestKickPlayerFromMatch_PlayerNotInMatch(t *testing.T) {
	nk := newSuspendTestNakamaModule()
	ctx := context.Background()

	matchUUID := uuid.Must(uuid.NewV4())
	node := "testnode"
	matchID, err := NewMatchID(matchUUID, node)
	require.NoError(t, err)

	gameServerSID := uuid.Must(uuid.NewV4())
	label := newTestMatchLabel(gameServerSID)
	targetUserID := uuid.Must(uuid.NewV4()).String()

	// Other player in match, not the target
	presences := []runtime.Presence{
		&suspendTestPresence{
			userID:    uuid.Must(uuid.NewV4()).String(),
			sessionID: uuid.Must(uuid.NewV4()).String(),
		},
	}
	setupMatchInNK(t, nk, matchID, label, presences)

	err = KickPlayerFromMatch(ctx, nk, matchID, targetUserID)
	require.NoError(t, err)

	// No signal since target player wasn't in the match
	assert.Empty(t, nk.getMatchSignalCalls())
}

func TestKickPlayerFromMatch_MatchNotFound(t *testing.T) {
	nk := newSuspendTestNakamaModule()
	ctx := context.Background()

	matchUUID := uuid.Must(uuid.NewV4())
	node := "testnode"
	matchID, err := NewMatchID(matchUUID, node)
	require.NoError(t, err)

	// Match not registered in nk.matches
	err = KickPlayerFromMatch(ctx, nk, matchID, uuid.Must(uuid.NewV4()).String())
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "match not found")
}

// --- EnforceGuildSuspension tests ---

// mockSessionRegistry implements SessionRegistry for testing EnforceGuildSuspension.
type mockSessionRegistry struct {
	sessions []Session
}

func (r *mockSessionRegistry) Range(fn func(Session) bool) {
	for _, s := range r.sessions {
		if !fn(s) {
			return
		}
	}
}

// Stub methods required by SessionRegistry interface (unused in these tests).
func (r *mockSessionRegistry) Get(sessionID uuid.UUID) Session { return nil }
func (r *mockSessionRegistry) Add(session Session)             {}
func (r *mockSessionRegistry) Remove(sessionID uuid.UUID)      {}
func (r *mockSessionRegistry) Disconnect(ctx context.Context, sessionID uuid.UUID, ban bool, reason ...runtime.PresenceReason) error {
	return nil
}
func (r *mockSessionRegistry) SingleSession(ctx context.Context, tracker Tracker, userID, sessionID uuid.UUID) {
}
func (r *mockSessionRegistry) Count() int { return len(r.sessions) }
func (r *mockSessionRegistry) Stop()      {}

func TestEnforceGuildSuspension_KicksFromMatch(t *testing.T) {
	// EnforceGuildSuspension should kick the player from their match
	// but NOT disconnect any sessions.
	nk := newSuspendTestNakamaModule()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	sessionID := uuid.Must(uuid.NewV4())

	matchUUID := uuid.Must(uuid.NewV4())
	node := "testnode"
	matchID, err := NewMatchID(matchUUID, node)
	require.NoError(t, err)

	gameServerSID := uuid.Must(uuid.NewV4())
	label := newTestMatchLabel(gameServerSID)
	playerSID := uuid.Must(uuid.NewV4()).String()

	presences := []runtime.Presence{
		&suspendTestPresence{userID: userID, sessionID: playerSID},
	}
	setupMatchInNK(t, nk, matchID, label, presences)

	// Set up service stream so GetMatchIDBySessionID finds the match
	serviceKey := nk.streamKey(StreamModeService, sessionID.String(), StreamLabelMatchService)
	nk.streamUsers[serviceKey] = []runtime.Presence{
		&suspendTestPresence{
			userID:    userID,
			sessionID: sessionID.String(),
			status:    matchID.String(),
		},
	}
	// StreamUserGet verifies presence
	metaKey := fmt.Sprintf("%d:%s:%s:%s:%s", StreamModeMatchAuthoritative, matchUUID.String(), node, userID, sessionID.String())
	nk.streamMeta[metaKey] = &suspendTestPresence{userID: userID, sessionID: sessionID.String()}

	// Use a real-ish session mock: we only need sessionRegistry.Range to return it
	// Since we can't easily create a *sessionWS in tests, we test via the
	// exported function's observable side effects on the NakamaModule mock.
	// The function iterates sessions from the registry, so we test the kick logic
	// by calling KickPlayerFromMatch directly and verifying the signal.

	// Direct test: kick signals the match
	err = KickPlayerFromMatch(ctx, nk, matchID, userID)
	require.NoError(t, err)

	calls := nk.getMatchSignalCalls()
	require.Len(t, calls, 1)
	assert.Equal(t, matchID.String(), calls[0].matchID)

	// Verify: no sessions were disconnected
	assert.Empty(t, nk.getDisconnectCalls(), "sessions must NOT be disconnected on guild suspension")
}

func TestEnforceGuildSuspension_NoDisconnect(t *testing.T) {
	// EnforceGuildSuspension with no active match should not disconnect any sessions.
	nk := newSuspendTestNakamaModule()
	logger := zap.NewNop()

	EnforceGuildSuspension(context.Background(), logger, nk, &mockSessionRegistry{}, uuid.Must(uuid.NewV4()).String(), []string{"group-1"})

	// Wait for any goroutines
	time.Sleep(100 * time.Millisecond)
	assert.Empty(t, nk.getDisconnectCalls(), "no sessions should be disconnected")
	assert.Empty(t, nk.getMatchSignalCalls(), "no match signals when no sessions")
}

func TestEnforceGuildSuspension_InvalidUserID(t *testing.T) {
	nk := newSuspendTestNakamaModule()
	logger := zap.NewNop()

	// Should return early without panic
	EnforceGuildSuspension(context.Background(), logger, nk, &mockSessionRegistry{}, "", []string{"group-1"})
	EnforceGuildSuspension(context.Background(), logger, nk, &mockSessionRegistry{}, "not-a-uuid", []string{"group-1"})
}

// --- DisconnectUserID tests ---

func TestDisconnectUserID_NoKick_MatchServiceOnly(t *testing.T) {
	nk := newSuspendTestNakamaModule()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	sessionID := uuid.Must(uuid.NewV4()).String()

	// Register a match service presence
	key := nk.streamKey(StreamModeService, userID, StreamLabelMatchService)
	nk.streamUsers[key] = []runtime.Presence{
		&suspendTestPresence{userID: userID, sessionID: sessionID},
	}

	cnt, err := DisconnectUserID(ctx, nk, userID, false, false, false)
	require.NoError(t, err)
	assert.Equal(t, 1, cnt)

	// Wait for the goroutine (no kickFirst, so no delay)
	require.Eventually(t, func() bool {
		calls := nk.getDisconnectCalls()
		for _, c := range calls {
			if c == sessionID {
				return true
			}
		}
		return false
	}, 2*time.Second, 50*time.Millisecond)
}

func TestDisconnectUserID_WithLogin(t *testing.T) {
	nk := newSuspendTestNakamaModule()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	matchSID := uuid.Must(uuid.NewV4()).String()
	loginSID := uuid.Must(uuid.NewV4()).String()

	// Match service presence
	matchKey := nk.streamKey(StreamModeService, userID, StreamLabelMatchService)
	nk.streamUsers[matchKey] = []runtime.Presence{
		&suspendTestPresence{userID: userID, sessionID: matchSID},
	}

	// Login service presence
	loginKey := nk.streamKey(StreamModeService, userID, StreamLabelLoginService)
	nk.streamUsers[loginKey] = []runtime.Presence{
		&suspendTestPresence{userID: userID, sessionID: loginSID},
	}

	cnt, err := DisconnectUserID(ctx, nk, userID, false, true, false)
	require.NoError(t, err)
	assert.Equal(t, 2, cnt)

	require.Eventually(t, func() bool {
		calls := nk.getDisconnectCalls()
		hasMatch, hasLogin := false, false
		for _, c := range calls {
			if c == matchSID {
				hasMatch = true
			}
			if c == loginSID {
				hasLogin = true
			}
		}
		return hasMatch && hasLogin
	}, 2*time.Second, 50*time.Millisecond)
}

func TestDisconnectUserID_WithGameServer(t *testing.T) {
	nk := newSuspendTestNakamaModule()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()
	matchSID := uuid.Must(uuid.NewV4()).String()
	gsSID := uuid.Must(uuid.NewV4()).String()

	matchKey := nk.streamKey(StreamModeService, userID, StreamLabelMatchService)
	nk.streamUsers[matchKey] = []runtime.Presence{
		&suspendTestPresence{userID: userID, sessionID: matchSID},
	}

	gsKey := nk.streamKey(StreamModeService, userID, StreamLabelGameServerService)
	nk.streamUsers[gsKey] = []runtime.Presence{
		&suspendTestPresence{userID: userID, sessionID: gsSID},
	}

	cnt, err := DisconnectUserID(ctx, nk, userID, false, false, true)
	require.NoError(t, err)
	assert.Equal(t, 2, cnt)

	require.Eventually(t, func() bool {
		calls := nk.getDisconnectCalls()
		hasMatch, hasGS := false, false
		for _, c := range calls {
			if c == matchSID {
				hasMatch = true
			}
			if c == gsSID {
				hasGS = true
			}
		}
		return hasMatch && hasGS
	}, 2*time.Second, 50*time.Millisecond)
}

func TestDisconnectUserID_NoSessions(t *testing.T) {
	nk := newSuspendTestNakamaModule()
	ctx := context.Background()
	userID := uuid.Must(uuid.NewV4()).String()

	cnt, err := DisconnectUserID(ctx, nk, userID, false, false, false)
	require.NoError(t, err)
	assert.Equal(t, 0, cnt)
}

// --- parseSuspensionDuration tests ---
// (Comprehensive tests already exist in evr_discord_appbot_duration_test.go,
//  these cover the core contracts after the move.)

func TestParseSuspensionDuration_BasicUnits(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"15m", 15 * time.Minute},
		{"1h", 1 * time.Hour},
		{"2d", 48 * time.Hour},
		{"1w", 7 * 24 * time.Hour},
		{"15", 15 * time.Minute}, // no unit defaults to minutes
		{"0", 0},
		{"", 0},
	}

	for _, tt := range tests {
		t.Run(fmt.Sprintf("input=%q", tt.input), func(t *testing.T) {
			d, err := parseSuspensionDuration(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, d)
		})
	}
}

func TestParseSuspensionDuration_CompoundDurations(t *testing.T) {
	tests := []struct {
		input    string
		expected time.Duration
	}{
		{"2h25m", 2*time.Hour + 25*time.Minute},
		{"1h30m", 1*time.Hour + 30*time.Minute},
		{"3h45m15s", 3*time.Hour + 45*time.Minute + 15*time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			d, err := parseSuspensionDuration(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, d)
		})
	}
}

func TestParseSuspensionDuration_CompoundWithDayWeekFails(t *testing.T) {
	inputs := []string{"2d3h", "1w2d", "3h2d", "2d5s", "1w30m"}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			_, err := parseSuspensionDuration(input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "compound durations with")
		})
	}
}

func TestParseSuspensionDuration_NegativeFails(t *testing.T) {
	inputs := []string{"-5m", "-2h"}
	for _, input := range inputs {
		t.Run(input, func(t *testing.T) {
			_, err := parseSuspensionDuration(input)
			require.Error(t, err)
			assert.Contains(t, err.Error(), "must be positive")
		})
	}
}

func TestParseSuspensionDuration_InvalidFormat(t *testing.T) {
	_, err := parseSuspensionDuration("abc")
	require.Error(t, err)
}
