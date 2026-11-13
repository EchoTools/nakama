package server

import (
	"context"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// Each path that ends a follow attempt without a join records its own reason
// word, which lobbyFind logs on "Follower cannot join leader's match, releasing
// to independent matchmaking". Without it the line carries only the mode and a
// release because the leader's match was full cannot be told from one because
// the leader's match is private, or from a timeout.
func TestFollowRelease_EachPathRecordsItsReason(t *testing.T) {
	newMatch := func() MatchID { return MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"} }

	type tc struct {
		name   string
		want   followReleaseReason
		setup  func(env *followTestEnv)
		run    func(env *followTestEnv, ctx context.Context) // default: TryFollowPartyLeader
		assert func(t *testing.T, r *followRelease)
	}
	withLabel := func(env *followTestEnv, label *MatchLabel) {
		mid := newMatch()
		label.ID = mid
		env.setLeaderMatch(mid)
		reg := newMockFollowMatchRegistry()
		reg.SetMatch(mid, label)
		env.withMockNK(reg)
	}
	poll := func(env *followTestEnv, ctx context.Context) {
		env.pipeline.pollFollowPartyLeader(ctx, zap.NewNop(), env.session, env.params, env.lobbyGroup)
	}

	cases := []tc{
		{name: "no leader", want: followReleaseNoLeader,
			setup: func(env *followTestEnv) { env.clearLeader() }},
		{name: "leader matchmaking", want: followReleaseLeaderMatchmaking,
			setup: func(env *followTestEnv) { env.setLeaderMatchmaking() }},
		{name: "leader not in a match", want: followReleaseLeaderNoMatch,
			setup: func(env *followTestEnv) { env.removeLeaderMatch() }},
		{name: "leader in private arena, follower asked for arena (open, has room)", want: followReleaseNonSocialMatch,
			setup: func(env *followTestEnv) {
				env.params.Mode = evr.ModeArenaPublic
				withLabel(env, &MatchLabel{Mode: evr.ModeArenaPrivate, Open: true, PlayerLimit: 8})
			},
			assert: func(t *testing.T, r *followRelease) {
				if r.LeaderMode != evr.ModeArenaPrivate || !r.Open || r.OpenSlots != 8 || r.RequiredSlots != 1 || !r.HasLeaderMid {
					t.Errorf("record = %+v", r)
				}
			}},
		{name: "leader in private social", want: followReleaseNonPublicSocialMatch,
			setup: func(env *followTestEnv) {
				withLabel(env, &MatchLabel{Mode: evr.ModeSocialPrivate, Open: true, PlayerLimit: 12})
			}},
		{name: "poll: leader's arena match full or closed", want: followReleasePollNonJoinable,
			setup: func(env *followTestEnv) {
				withLabel(env, &MatchLabel{Mode: evr.ModeArenaPublic, Open: false, PlayerLimit: 8})
			},
			run: poll,
			assert: func(t *testing.T, r *followRelease) {
				if r.Open || r.OpenSlots != 8 || r.RequiredSlots != 1 {
					t.Errorf("record = %+v", r)
				}
			}},
		{name: "full arena, client in a lobby: TryFollow hands to the poll", want: followReleasePollNonJoinable,
			setup: func(env *followTestEnv) {
				env.params.CurrentMatchID = newMatch()
				withLabel(env, &MatchLabel{Mode: evr.ModeArenaPublic, Open: false, PlayerLimit: 8})
			}},
		{name: "poll: leader's open arena match with room", want: followReleasePollNonSocialMatch,
			setup: func(env *followTestEnv) {
				withLabel(env, &MatchLabel{Mode: evr.ModeArenaPublic, Open: true, PlayerLimit: 8})
			},
			run: poll},
		{name: "poll: leader never lands", want: followReleasePollBudgetExhausted,
			setup: func(env *followTestEnv) { env.removeLeaderMatch() },
			run:   poll},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			env := newFollowTestEnv(t)
			env.pipeline.pollFollowInterval = 2 * time.Millisecond
			env.pipeline.pollFollowMaxDuration = 200 * time.Millisecond
			c.setup(env)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			env.params.followRelease = nil
			if c.run != nil {
				c.run(env, ctx)
			} else if env.pipeline.TryFollowPartyLeader(ctx, zap.NewNop(), env.session, env.params, env.lobbyGroup) {
				t.Fatal("follow succeeded; expected a release")
			}
			r := env.params.followRelease
			if r == nil {
				t.Fatal("no release reason recorded")
			}
			if r.Reason != c.want {
				t.Fatalf("reason = %q, want %q", r.Reason, c.want)
			}
			if c.assert != nil {
				c.assert(t, r)
			}
		})
	}
}

// The logged fields carry the reason, the leader's match id and mode, open and
// required slots, and the elapsed time; an unrecorded release says so.
func TestFollowRelease_LogFields(t *testing.T) {
	params := &LobbySessionParameters{}
	mid := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "testnode"}
	params.noteFollowRelease(followReleaseNonSocialMatch, mid, &MatchLabel{Mode: evr.ModeArenaPrivate, Open: true, PlayerLimit: 8}, 1)
	got := map[string]any{}
	for _, f := range params.followReleaseFields(1500 * time.Millisecond) {
		switch {
		case f.String != "":
			got[f.Key] = f.String
		default:
			got[f.Key] = f.Integer
		}
	}
	if got["release_reason"] != "leader_match_non_social" || got["leader_mid"] != mid.String() ||
		got["leader_match_mode"] != evr.ModeArenaPrivate.String() || got["open_slots"] != int64(8) ||
		got["required_slots"] != int64(1) || got["follow_elapsed"] != int64(1500*time.Millisecond) {
		t.Fatalf("fields = %v", got)
	}

	params.followRelease = nil
	for _, f := range params.followReleaseFields(0) {
		if f.Key == "release_reason" && f.String != string(followReleaseUnrecorded) {
			t.Fatalf("unrecorded release logged %q", f.String)
		}
	}
}
