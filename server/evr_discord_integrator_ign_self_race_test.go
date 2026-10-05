package server

import (
	"context"
	"sync"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
)

// TestSyncMembersIGN_ConcurrentCallsForSameUserDoNotExhaustRetries pins the
// self-race described in the defect report: discordgo dispatches each
// GUILD_MEMBER_UPDATE in its own goroutine, and Discord can emit several such
// events for one member in quick succession (role change, nick change, avatar
// change). Each event independently reaches handleMemberUpdate ->
// syncMembersIGN with that SAME user's own profile, so the version-conflict
// retry loop at evr_discord_integrator.go:1103-1122 (maxDisplayNameRetries==3,
// no backoff) can lose all 3 attempts racing the user's OWN other concurrent
// writers rather than an unrelated writer. In a production window of about
// 3.7 days this exhausted the retry budget 37 times out of 357 total version
// conflicts for display-name syncs.
//
// This test drives N goroutines, gated behind a start barrier so they
// actually overlap (AGENTS.md defect class 2: a concurrency test that never
// achieves concurrency), all calling syncMembersIGN for the SAME synthetic
// userID against the same OCC-correct in-memory store. Before the fix, a
// nonzero number of them return "error updating EVR profile" because their 3
// retries were exhausted by their own sibling goroutines. After the fix,
// every goroutine must succeed and the final stored display name must be the
// one they all raced to set.
func TestSyncMembersIGN_ConcurrentCallsForSameUserDoNotExhaustRetries(t *testing.T) {
	const (
		userID      = "7c3e9a10-0000-4000-8000-00000000f1f1"
		groupID     = "7c3e9a10-0000-4000-8000-00000000f1f2"
		newName     = "test-user-a"
		concurrency = 20
	)

	m := newIGNSyncTestModule()
	seedStoredProfile(t, m.profileUpdateTestModule, userID, &EVRProfile{})

	d := &DiscordIntegrator{ctx: context.Background(), logger: zap.NewNop(), nk: m}
	member := &discordgo.Member{
		GuildID: "guild-1",
		Nick:    newName,
		User:    &discordgo.User{ID: "discord-1", Username: "tester"},
	}
	group := &GuildGroup{State: &GuildGroupState{}, Group: &api.Group{Id: groupID}}

	var (
		startBarrier sync.WaitGroup
		wg           sync.WaitGroup
		mu           sync.Mutex
		errs         []error
	)
	startBarrier.Add(1)

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Each goroutine stands in for an independent handleMemberUpdate
			// invocation, which loads its own copy of the profile before
			// calling syncMembersIGN -- exactly as production does.
			profile, err := EVRProfileLoad(d.ctx, m, userID)
			require.NoError(t, err)

			startBarrier.Wait()
			_, callErr := d.syncMembersIGN(d.ctx, zap.NewNop(), profile, member, group)
			if callErr != nil {
				mu.Lock()
				errs = append(errs, callErr)
				mu.Unlock()
			}
		}()
	}

	startBarrier.Done()
	wg.Wait()

	require.Empty(t, errs, "no concurrent self-race caller should exhaust its retry budget: %v", errs)

	stored := m.storedProfile(t, userID)
	require.Equal(t, newName, stored.GetGroupIGNData(groupID).DisplayName,
		"the serialized writes must not silently lose the update")
}
