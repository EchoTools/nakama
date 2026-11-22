package server

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/api"
	"go.uber.org/atomic"
	"go.uber.org/zap"
)

const (
	inSrcID    = "group-a-source"
	inChildID  = "group-b-child"
	inChild2ID = "group-c-child"
)

type sentNotice struct {
	channelID string
	msg       *discordgo.MessageSend
}

// inheritedNoticeFixture is guild A (the source) with guilds B and C inheriting
// from it. B has a notice channel, C only an audit channel.
func inheritedNoticeFixture(bFilter SuspensionInheritanceFilter) (*GuildGroupRegistry, *GuildGroup) {
	mk := func(id, name, notice, audit string) *GuildGroup {
		return &GuildGroup{
			GroupMetadata: GroupMetadata{GuildID: "guild-" + id, EnforcementNoticeChannelID: notice, AuditChannelID: audit},
			State:         &GuildGroupState{GroupID: id},
			Group:         &api.Group{Id: id, Name: name, LangTag: GuildGroupLangTag},
		}
	}
	src := mk(inSrcID, "Guild A", "chan-a-notice", "chan-a-audit")
	groups := map[string]*GuildGroup{
		inSrcID:    src,
		inChildID:  mk(inChildID, "Guild B", "chan-b-notice", "chan-b-audit"),
		inChild2ID: mk(inChild2ID, "Guild C", "", "chan-c-audit"),
	}
	inheritance := map[string][]string{inSrcID: {inChildID, inChild2ID}}
	filters := InheritanceFilters{}
	if !bFilter.IsDefault() {
		filters[inSrcID] = map[string]SuspensionInheritanceFilter{inChildID: bFilter}
	}
	return &GuildGroupRegistry{
		guildGroups:        atomic.NewPointer(&groups),
		inheritanceMap:     atomic.NewPointer(&inheritance),
		inheritanceFilters: atomic.NewPointer(&filters),
	}, src
}

func inheritedRecord(duration time.Duration, allowPrivate bool) GuildEnforcementRecord {
	j := NewGuildEnforcementJournal("target-user")
	return j.AddRecord(inSrcID, "enforcer", "enforcer-discord-id", "cheating", "secret moderator note", false, allowPrivate, duration)
}

func sendInherited(t *testing.T, registry *GuildGroupRegistry, src *GuildGroup, record GuildEnforcementRecord, failOn string) []sentNotice {
	t.Helper()
	return sendInheritedPosted(t, registry, src, record, failOn, "")
}

func sendInheritedPosted(t *testing.T, registry *GuildGroupRegistry, src *GuildGroup, record GuildEnforcementRecord, failOn string, postedChannelIDs ...string) []sentNotice {
	t.Helper()
	var sent []sentNotice
	subject := inheritedNoticeSubject{DiscordID: "target-discord-id", NameIn: func(groupID string) string {
		if groupID == inChildID {
			return "TargetIGN"
		}
		return ""
	}}
	sendInheritedEnforcementNotices(context.Background(), zap.NewNop(), func(channelID string, msg *discordgo.MessageSend) error {
		if channelID == failOn {
			return errors.New("discord says no")
		}
		sent = append(sent, sentNotice{channelID, msg})
		return nil
	}, registry, src, record, subject, postedChannelIDs)
	return sent
}

func (s sentNotice) text() string {
	var b strings.Builder
	e := s.msg.Embed
	b.WriteString(e.Title + "\n" + e.Description + "\n")
	for _, f := range e.Fields {
		b.WriteString(f.Name + "\n" + f.Value + "\n")
	}
	if e.Footer != nil {
		b.WriteString(e.Footer.Text)
	}
	return b.String()
}

// A full ban issued in A appears in B's enforcement channel, naming A as source.
func TestInheritedNotice_FullBanReachesInheritingGuildNamingSource(t *testing.T) {
	registry, src := inheritedNoticeFixture(SuspensionInheritanceFilter{})
	sent := sendInherited(t, registry, src, inheritedRecord(30*24*time.Hour, false), "")

	var toB *sentNotice
	for i := range sent {
		if sent[i].channelID == "chan-b-notice" {
			toB = &sent[i]
		}
	}
	if toB == nil {
		t.Fatalf("nothing sent to B's enforcement channel; sent: %+v", sent)
	}
	text := toB.text()
	for _, want := range []string{"Guild A", "TargetIGN", "<@!target-discord-id>", "Private lobbies: not allowed", "cheating"} {
		if !strings.Contains(text, want) {
			t.Errorf("B's notice lacks %q:\n%s", want, text)
		}
	}
	// Mentions in the notice must not ping anyone.
	if toB.msg.AllowedMentions == nil || len(toB.msg.AllowedMentions.Parse) != 0 {
		t.Errorf("notice must suppress mentions: %+v", toB.msg.AllowedMentions)
	}
}

// The source guild's moderator notes and its enforcer's identity stay in A, and A's
// Edit/Void buttons (which act on A's own records) are not offered to B.
func TestInheritedNotice_DoesNotLeakSourceInternals(t *testing.T) {
	registry, src := inheritedNoticeFixture(SuspensionInheritanceFilter{})
	for _, n := range sendInherited(t, registry, src, inheritedRecord(time.Hour, false), "") {
		text := n.text()
		for _, leak := range []string{"secret moderator note", "enforcer-discord-id"} {
			if strings.Contains(text, leak) {
				t.Errorf("notice to %s leaks %q:\n%s", n.channelID, leak, text)
			}
		}
		if len(n.msg.Components) != 0 {
			t.Errorf("notice to %s carries buttons: %+v", n.channelID, n.msg.Components)
		}
	}
}

// An enforcement that B's inheritance filters exclude does not appear in B.
func TestInheritedNotice_FilteredOutEnforcementPostsNothingInThatGuild(t *testing.T) {
	for _, tc := range []struct {
		name         string
		filter       SuspensionInheritanceFilter
		duration     time.Duration
		allowPrivate bool
	}{
		{"too short", SuspensionInheritanceFilter{MinimumDurationSeconds: 7 * 24 * 3600}, time.Hour, false},
		{"public-only under full_bans scope", SuspensionInheritanceFilter{Scope: InheritScopeFullBans}, 30 * 24 * time.Hour, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			registry, src := inheritedNoticeFixture(tc.filter)
			sent := sendInherited(t, registry, src, inheritedRecord(tc.duration, tc.allowPrivate), "")
			for _, n := range sent {
				if n.channelID == "chan-b-notice" || n.channelID == "chan-b-audit" {
					t.Fatalf("filtered-out enforcement was posted in B: %s", n.channelID)
				}
			}
			// C has no filter, so it still hears about it.
			var toC bool
			for _, n := range sent {
				toC = toC || n.channelID == "chan-c-audit"
			}
			if !toC {
				t.Fatalf("unfiltered inheriting guild C was not notified: %+v", sent)
			}
		})
	}
}

// B's notice follows the same rule as the enforcement itself: a record B's filter
// admits is posted, so the two cannot disagree.
func TestInheritedNotice_AgreesWithEnforcementFilter(t *testing.T) {
	filter := SuspensionInheritanceFilter{MinimumDurationSeconds: 24 * 3600, Scope: InheritScopeFullBans}
	registry, src := inheritedNoticeFixture(filter)
	for _, tc := range []struct {
		duration     time.Duration
		allowPrivate bool
	}{
		{time.Hour, false}, {time.Hour, true}, {48 * time.Hour, false}, {48 * time.Hour, true}, {LifetimeSuspensionDuration, false},
	} {
		record := inheritedRecord(tc.duration, tc.allowPrivate)
		enforced := false
		got, _ := CheckEnforcementSuspensions(
			GuildEnforcementJournalList{"target-user": {RecordsByGroupID: map[string][]GuildEnforcementRecord{inSrcID: {record}}}},
			registry.InheritanceByParentGroupID(), registry.InheritanceFilters())
		enforced = len(got[inChildID]) > 0

		notified := false
		for _, n := range sendInherited(t, registry, src, record, "") {
			notified = notified || n.channelID == "chan-b-notice"
		}
		if enforced != notified {
			t.Errorf("duration=%v allowPrivate=%v: enforced in B = %v, notified B = %v", tc.duration, tc.allowPrivate, enforced, notified)
		}
	}
}

// A guild with no enforcement notice channel gets the notice in its audit channel,
// and the source guild's own channels are not posted to a second time.
func TestInheritedNotice_ChannelSelection(t *testing.T) {
	registry, src := inheritedNoticeFixture(SuspensionInheritanceFilter{})
	sent := sendInherited(t, registry, src, inheritedRecord(time.Hour, false), "")
	var channels []string
	for _, n := range sent {
		channels = append(channels, n.channelID)
	}
	if len(channels) != 2 || channels[0] != "chan-b-notice" || channels[1] != "chan-c-audit" {
		t.Fatalf("channels = %v, want [chan-b-notice chan-c-audit]", channels)
	}

	// B is configured with the very channel A's own notice went to.
	registry.GuildGroups()[inChildID].EnforcementNoticeChannelID = "chan-a-notice"
	for _, n := range sendInheritedPosted(t, registry, src, inheritedRecord(time.Hour, false), "", "chan-a-notice") {
		if n.channelID == "chan-a-notice" {
			t.Fatalf("channel the source notice went to was posted to again")
		}
	}
}

// The source's channel is skipped only when the source's notice actually went there.
// A posted nothing (or its post failed): B, sharing that channel, must still hear.
func TestInheritedNotice_SourceChannelNotSeededUnlessPostedTo(t *testing.T) {
	registry, src := inheritedNoticeFixture(SuspensionInheritanceFilter{})
	registry.GuildGroups()[inChildID].EnforcementNoticeChannelID = "chan-a-notice"
	var toB bool
	for _, n := range sendInheritedPosted(t, registry, src, inheritedRecord(time.Hour, false), "", "") {
		toB = toB || n.channelID == "chan-a-notice"
	}
	if !toB {
		t.Fatal("B shares A's configured notice channel, A posted nothing there, and B was not notified")
	}
}

// The player is named as B knows them. With no name in B the notice carries the
// mention alone: another guild's per-guild name is never shown as B's, labeled or not.
func TestInheritedNotice_SubjectNaming(t *testing.T) {
	registry, src := inheritedNoticeFixture(SuspensionInheritanceFilter{})
	b := registry.Get(inChildID)
	rec := inheritedRecord(time.Hour, false)
	desc := func(s inheritedNoticeSubject) string {
		return inheritedEnforcementNoticeEmbed(src, b, rec, s).Description
	}

	got := desc(inheritedNoticeSubject{DiscordID: "d1", NameIn: func(g string) string { return map[string]string{inChildID: "NameInB", inSrcID: "NameInA"}[g] }})
	if !strings.Contains(got, "NameInB (<@!d1>)") || strings.Contains(got, "NameInA") {
		t.Errorf("B's own name should be shown alone:\n%s", got)
	}
	got = desc(inheritedNoticeSubject{DiscordID: "d1", NameIn: func(g string) string { return map[string]string{inSrcID: "NameInA"}[g] }})
	if !strings.HasPrefix(got, "(<@!d1>)\n") || strings.Contains(got, "NameInA") {
		t.Errorf("B has no name: want the bare mention and no name from Guild A, got:\n%s", got)
	}
	got = desc(inheritedNoticeSubject{DiscordID: "d1"})
	if !strings.HasPrefix(got, "(<@!d1>)\n") || strings.Contains(got, "unknown") {
		t.Errorf("no name: want the bare mention, got:\n%s", got)
	}
	got = desc(inheritedNoticeSubject{})
	if strings.HasPrefix(got, "(") || strings.HasPrefix(got, "\n") || strings.Contains(got, "<@!>") {
		t.Errorf("no name and no id: want neither placeholder, got:\n%s", got)
	}
}

// One guild's channel failing does not stop the others, and does not panic.
func TestInheritedNotice_OneFailureDoesNotStopTheRest(t *testing.T) {
	registry, src := inheritedNoticeFixture(SuspensionInheritanceFilter{})
	sent := sendInherited(t, registry, src, inheritedRecord(time.Hour, false), "chan-b-notice")
	if len(sent) != 1 || sent[0].channelID != "chan-c-audit" {
		t.Fatalf("sent = %+v, want only C's audit channel", sent)
	}
}

func TestInheritedNotice_NoInheritorsOrNilRegistryPostsNothing(t *testing.T) {
	registry, src := inheritedNoticeFixture(SuspensionInheritanceFilter{})
	empty := map[string][]string{}
	registry.inheritanceMap = atomic.NewPointer(&empty)
	if sent := sendInherited(t, registry, src, inheritedRecord(time.Hour, false), ""); len(sent) != 0 {
		t.Fatalf("no link, but sent %+v", sent)
	}
	if sent := sendInherited(t, nil, src, inheritedRecord(time.Hour, false), ""); len(sent) != 0 {
		t.Fatalf("nil registry, but sent %+v", sent)
	}
}

// Every channel the source posted to is skipped, not just one: its notice and its
// audit line may both be shared with an inheriting guild.
func TestInheritedNotice_SkipsEveryChannelTheSourcePostedTo(t *testing.T) {
	registry, src := inheritedNoticeFixture(SuspensionInheritanceFilter{})
	registry.GuildGroups()[inChildID].EnforcementNoticeChannelID = "chan-a-notice"
	registry.GuildGroups()[inChild2ID].AuditChannelID = "chan-a-audit"

	for _, tc := range []struct {
		name   string
		posted []string
		want   []string
	}{
		{"both posted: neither is told", []string{"chan-a-notice", "chan-a-audit"}, nil},
		{"only the notice posted: C is told in the audit channel", []string{"chan-a-notice"}, []string{"chan-a-audit"}},
		{"only the audit line posted: B is told in the notice channel", []string{"chan-a-audit"}, []string{"chan-a-notice"}},
		{"nothing posted: both are told", nil, []string{"chan-a-notice", "chan-a-audit"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			for _, n := range sendInheritedPosted(t, registry, src, inheritedRecord(time.Hour, false), "", tc.posted...) {
				got = append(got, n.channelID)
			}
			if !slices.Equal(got, tc.want) {
				t.Fatalf("notified channels = %v, want %v", got, tc.want)
			}
		})
	}
}
