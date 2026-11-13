package server

import (
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// followReleaseReason names the path that ended a follow attempt without the
// follower joining. lobbyFind logs it when it releases the follower to solo
// matchmaking, so a release can be told apart as "leader's match was full",
// "leader's match was non-social and has room", "timed out", and so on.
type followReleaseReason string

const (
	// TryFollowPartyLeader
	followReleaseNoLeader             followReleaseReason = "no_leader"
	followReleaseLeaderOwnSession     followReleaseReason = "leader_own_session"
	followReleaseLeaderMatchmaking    followReleaseReason = "leader_matchmaking"
	followReleaseLeaderNoMatch        followReleaseReason = "leader_no_match"
	followReleaseLeaderMatchUnread    followReleaseReason = "leader_match_unreadable"
	followReleaseNonSocialMatch       followReleaseReason = "leader_match_non_social"
	followReleaseNonPublicSocialMatch followReleaseReason = "leader_match_non_public_social"
	followReleaseCapacityRefused      followReleaseReason = "capacity_refused"
	followReleaseJoinFailed           followReleaseReason = "join_failed"
	// pollFollowPartyLeader
	followReleasePollBudgetExhausted followReleaseReason = "poll_budget_exhausted"
	followReleasePollNonJoinable     followReleaseReason = "poll_leader_match_full_or_closed"
	followReleasePollNonSocialMatch  followReleaseReason = "poll_leader_match_non_social"
	followReleasePollJoinFailed      followReleaseReason = "poll_join_failed"
	// followReleaseUnrecorded is logged when a follow attempt returned false
	// without any path recording why. It is a defect marker, not a reason.
	followReleaseUnrecorded followReleaseReason = "unrecorded"
)

// followRelease is what the follow paths knew when they gave up.
type followRelease struct {
	Reason        followReleaseReason
	HasLeaderMid  bool
	LeaderMatchID MatchID
	LeaderMode    evr.Symbol
	Open          bool
	OpenSlots     int
	RequiredSlots int
}

// noteFollowRelease records why the current follow attempt is returning false.
// The last call wins: TryFollowPartyLeader may hand over to the poll, which
// then records its own reason. label and required are optional.
func (params *LobbySessionParameters) noteFollowRelease(reason followReleaseReason, leaderMatchID MatchID, label *MatchLabel, required int) {
	r := &followRelease{Reason: reason, RequiredSlots: required}
	if !leaderMatchID.IsNil() {
		r.HasLeaderMid = true
		r.LeaderMatchID = leaderMatchID
	}
	if label != nil {
		r.LeaderMode = label.Mode
		r.Open = label.Open
		r.OpenSlots = label.OpenPlayerSlots()
	}
	params.followRelease = r
}

// followReleaseFields renders the recorded release for the log line.
func (params *LobbySessionParameters) followReleaseFields(elapsed time.Duration) []zap.Field {
	r := params.followRelease
	if r == nil {
		r = &followRelease{Reason: followReleaseUnrecorded}
	}
	fields := []zap.Field{
		zap.String("release_reason", string(r.Reason)),
		zap.Duration("follow_elapsed", elapsed),
		zap.Int("required_slots", r.RequiredSlots),
		zap.Int("open_slots", r.OpenSlots),
	}
	if r.HasLeaderMid {
		fields = append(fields,
			zap.String("leader_mid", r.LeaderMatchID.String()),
			zap.String("leader_match_mode", r.LeaderMode.String()),
			zap.Bool("leader_match_open", r.Open))
	}
	return fields
}
