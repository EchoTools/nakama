package server

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// The early quit update carries a stamp at +0x18 and the lockout expiry at +0x20 (#712). The game
// ignores an update whose stamp is not newer than the one it holds, so every update sent to a player
// must carry a stamp greater than the last one sent to that player, in the same second as well, and
// the profile's earlyquit|penaltyts, which the client reads as its first stamp, must sit below the
// update sent right after it.

type earlyQuitSendEnv struct {
	trigger *SNSEarlyQuitMessageTrigger
	session *sessionWS
	logs    *observer.ObservedLogs
}

func newEarlyQuitSendEnv(t *testing.T) *earlyQuitSendEnv {
	t.Helper()
	registry := NewLocalSessionRegistry(&testMetrics{})
	session := capturingSession(t, nil)
	registry.Add(session)

	core, logs := observer.New(zapcore.DebugLevel)
	trigger := NewSNSEarlyQuitMessageTrigger(&EvrPipeline{sessionRegistry: registry}, zap.New(core), newEvrTestNakamaModule(), nil)
	return &earlyQuitSendEnv{trigger: trigger, session: session, logs: logs}
}

func (e *earlyQuitSendEnv) updates(t *testing.T) []*evr.SNSEarlyQuitUpdateNotification {
	t.Helper()
	out := []*evr.SNSEarlyQuitUpdateNotification{}
	for _, m := range sentTo(t, e.session) {
		if u, ok := m.(*evr.SNSEarlyQuitUpdateNotification); ok {
			out = append(out, u)
		}
	}
	return out
}

func TestEarlyQuitUpdateCarriesStampAndLockoutExpiry(t *testing.T) {
	e := newEarlyQuitSendEnv(t)
	expiry := time.Now().Add(10 * time.Minute).Unix()
	state := &EarlyQuitPlayerState{PenaltyTimestamp: expiry, NumEarlyQuits: 4, PenaltyLevel: 2}

	before := time.Now().Unix()
	require.NoError(t, e.trigger.SendEarlyQuitUpdateNotification(context.Background(), e.session.userID.String(), state))

	got := e.updates(t)
	require.Len(t, got, 1)
	require.Equal(t, expiry, got[0].PenaltyExpiry, "+0x20 is the lockout expiry")
	require.GreaterOrEqual(t, got[0].Stamp, before, "+0x18 is a stamp of about now, so a client that still reads it as the expiry sees a lockout that is over")
	require.NotEqual(t, expiry, got[0].Stamp, "the stamp is not the expiry")
	require.EqualValues(t, 4, got[0].NumEarlyQuits)
}

func TestEarlyQuitUpdatesToOnePlayerNeverRepeatAStamp(t *testing.T) {
	e := newEarlyQuitSendEnv(t)
	state := &EarlyQuitPlayerState{PenaltyTimestamp: time.Now().Add(time.Minute).Unix()}
	for range 5 {
		require.NoError(t, e.trigger.SendEarlyQuitUpdateNotification(context.Background(), e.session.userID.String(), state))
	}

	got := e.updates(t)
	require.Len(t, got, 5)
	for i := 1; i < len(got); i++ {
		require.Greater(t, got[i].Stamp, got[i-1].Stamp, "update %d must be newer than update %d, in the same second too", i, i-1)
	}
}

// max(previous + 1, now), per player.
func TestEarlyQuitStampIsMaxOfPreviousPlusOneAndNow(t *testing.T) {
	s := newEarlyQuitStamps()
	at := func(unix int64) time.Time { return time.Unix(unix, 0) }

	require.EqualValues(t, 1000, s.Next("a", at(1000)), "first stamp is now")
	require.EqualValues(t, 1001, s.Next("a", at(1000)), "same second: previous + 1")
	require.EqualValues(t, 1002, s.Next("a", at(1000)))
	require.EqualValues(t, 1003, s.Next("a", at(999)), "clock stepping back: previous + 1, never lower")
	require.EqualValues(t, 2000, s.Next("a", at(2000)), "time moved past previous + 1: now")
	require.EqualValues(t, 1000, s.Next("b", at(1000)), "another player has its own stamp")
}

func TestEarlyQuitStampRegistryDropsPlayersWhoseStampIsInThePast(t *testing.T) {
	s := newEarlyQuitStamps()
	for i := range 100 {
		s.Next(fmt.Sprintf("player-%d", i), time.Unix(1000, 0))
	}
	// Once the clock is past every stamp, an entry says nothing a fresh one would not.
	s.Next("later", time.Unix(1000+earlyQuitStampSweepSecs+1, 0))
	require.LessOrEqual(t, s.Len(), 1, "stale entries are swept, or the registry grows with every player ever seen")
}

func TestLoginProfileStampIsBelowTheUpdateSentRightAfter(t *testing.T) {
	e := newEarlyQuitSendEnv(t)
	expiry := time.Now().Add(10 * time.Minute).Unix()
	state := &EarlyQuitPlayerState{PenaltyTimestamp: expiry, NumEarlyQuits: 3, NumSteadyMatches: 9, PenaltyLevel: 1, SteadyPlayerLevel: 2}
	uid := e.session.userID.String()

	profile := loginEarlyQuitFeatures(uid, state, time.Now())
	require.NoError(t, e.trigger.SendEarlyQuitUpdateNotification(context.Background(), uid, state))

	got := e.updates(t)
	require.Len(t, got, 1)
	require.Less(t, profile.PenaltyTimestamp, got[0].Stamp, "the update after the profile must be newer, or the client ignores it")
	require.NotEqual(t, expiry, profile.PenaltyTimestamp, "the profile's penaltyts is a stamp; the expiry comes in the update")
	require.Equal(t, 3, profile.NumEarlyQuits)
	require.Equal(t, 9, profile.NumSteadyMatches)
	require.Equal(t, 1, profile.PenaltyLevel)
	require.Equal(t, 2, profile.SteadyPlayerLevel)
}

// The login path hands the client no expiry (the game's setter gets -1 there), so the update that
// follows the profile is what starts the countdown.
func TestLoginSendsConfigThenFlagsThenAnUpdateWithTheExpiry(t *testing.T) {
	e := newEarlyQuitSendEnv(t)
	expiry := time.Now().Add(10 * time.Minute).Unix()
	state := &EarlyQuitPlayerState{PenaltyTimestamp: expiry, PenaltyLevel: 1}

	require.NoError(t, e.trigger.SendEarlyQuitOnLogin(context.Background(), e.session, state))

	msgs := sentTo(t, e.session)
	require.Equal(t, []string{"*evr.SNSEarlyQuitConfig", "*evr.SNSEarlyQuitFeatureFlags", "*evr.SNSEarlyQuitUpdateNotification"}, typeNames(msgs))
	require.EqualValues(t, 0x23, msgs[1].(*evr.SNSEarlyQuitFeatureFlags).Flags, "the login flag byte is bits 0, 1 and 5")
	require.Equal(t, expiry, msgs[2].(*evr.SNSEarlyQuitUpdateNotification).PenaltyExpiry)
}

func TestEveryEarlyQuitSendLogsUserStampExpiryAndFlagsAtInfo(t *testing.T) {
	e := newEarlyQuitSendEnv(t)
	expiry := time.Now().Add(10 * time.Minute).Unix()
	uid := e.session.userID.String()

	require.NoError(t, e.trigger.SendEarlyQuitUpdateNotification(context.Background(), uid, &EarlyQuitPlayerState{PenaltyTimestamp: expiry}))
	sent := e.updates(t)
	require.Len(t, sent, 1)

	entries := e.logs.FilterMessage("Sent early quit update").FilterLevelExact(zapcore.InfoLevel).All()
	require.Len(t, entries, 1, "one Info record per update sent")
	f := entries[0].ContextMap()
	require.Equal(t, uid, f["user_id"])
	require.EqualValues(t, sent[0].Stamp, f["stamp"])
	require.EqualValues(t, expiry, f["penalty_expiry"])
	require.EqualValues(t, 0x23, f["flags"])

	require.NoError(t, e.trigger.SendFeatureFlagsOnLogin(context.Background(), e.session))
	flagLogs := e.logs.FilterMessage("Sent early quit feature flags").FilterLevelExact(zapcore.InfoLevel).All()
	require.Len(t, flagLogs, 1)
	require.Equal(t, uid, flagLogs[0].ContextMap()["user_id"])
	require.EqualValues(t, 0x23, flagLogs[0].ContextMap()["flags"])
}

// Auto-report was gated on bit 3 of the wire byte, which the client never reads. It is a service
// setting now, and on by default as it was.
func TestAutoReportIsAServiceSettingNotTheWireByte(t *testing.T) {
	on := &GlobalMatchmakingSettings{}
	require.True(t, shouldAutoReportEarlyQuit(on, 3), "at penalty level 3 and above, by default")
	require.True(t, shouldAutoReportEarlyQuit(on, 4))
	require.False(t, shouldAutoReportEarlyQuit(on, 2))
	require.False(t, shouldAutoReportEarlyQuit(&GlobalMatchmakingSettings{DisableEarlyQuitAutoReport: true}, 3))
	require.NotZero(t, evr.DefaultEarlyQuitFeatureFlags()&0x23, "sanity: flags are still sent")
	require.Zero(t, evr.DefaultEarlyQuitFeatureFlags()&(1<<3), "bit 3 is not sent, so nothing can gate on it")
}
