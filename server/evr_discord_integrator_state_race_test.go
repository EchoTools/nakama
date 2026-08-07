package server

import (
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
)

// discordgo owns Session.State and mutates it from the gateway goroutine under
// State.Lock():
//
// (all line numbers are discordgo v0.29.0's state.go)
//
//   - GuildAdd (:89) appends to State.Guilds (:146) and, for a guild already in
//     the cache, overwrites the existing *Guild IN PLACE (`*g = *guild`, :142).
//   - ChannelAdd (:482) appends to guild.Channels (:520) and overwrites an
//     existing *Channel in place (`*c = *channel`, :502).
//   - ChannelRemove (:529) compacts guild.Channels.
//
// pruneGuildGroups runs on the 15-minute prune ticker goroutine
// (evr_discord_integrator.go:189), NOT the gateway goroutine. It read
// d.dg.State.Guilds with no lock held and handed the LIVE *Guild pointers on to
// deep readers: guildSync (evr_discord_integrator.go:550) ranges guild.Channels
// looking for #rules, and the prune-leave path logs `"guild_metadata": guild`,
// which zap reflect-walks the whole struct.
//
// Before PR #514, guildSync only ever received the event-owned e.Guild from a
// Guild Create/Update, which is fresh and unshared; the orphan reconciliation
// added in #514 is what wired live state objects into a deep read.
//
// The copy-semantics tests below fail deterministically without the fix; the
// concurrency tests fail under -race.

// guildSyncStyleRead performs the same reads on a guild that guildSync does:
// the scalar fields it copies into the Nakama group, and the #rules channel
// scan over guild.Channels.
func guildSyncStyleRead(g *discordgo.Guild) string {
	rules := ""
	_ = g.ID
	_ = g.Name
	_ = g.OwnerID
	_ = g.Description
	_ = g.IconURL("512")
	for _, channel := range g.Channels {
		if channel == nil {
			continue
		}
		if channel.Type == discordgo.ChannelTypeGuildText && channel.Name == "rules" {
			rules = channel.Topic
			break
		}
	}
	return rules
}

// newTestDiscordState builds a state with n guilds, each carrying a #rules
// channel, as the gateway would after a Ready + Guild Create burst.
func newTestDiscordState(t *testing.T, n int) *discordgo.State {
	t.Helper()
	state := discordgo.NewState()
	for i := 0; i < n; i++ {
		guildID := fmt.Sprintf("guild-%d", i)
		g := &discordgo.Guild{
			ID:          guildID,
			Name:        fmt.Sprintf("Guild %d", i),
			OwnerID:     fmt.Sprintf("owner-%d", i),
			Description: "a guild",
			Icon:        "icon-hash",
			Channels: []*discordgo.Channel{
				{ID: guildID + "-rules", GuildID: guildID, Type: discordgo.ChannelTypeGuildText, Name: "rules", Topic: "be nice"},
				{ID: guildID + "-general", GuildID: guildID, Type: discordgo.ChannelTypeGuildText, Name: "general"},
			},
		}
		if err := state.GuildAdd(g); err != nil {
			t.Fatalf("GuildAdd: %v", err)
		}
	}
	return state
}

// TestSnapshotStateGuilds_ReturnsCopiesNotLiveGuilds is the deterministic core
// of the fix: the snapshot must not alias the *Guild objects that the gateway
// goroutine overwrites in place. It fails without -race.
func TestSnapshotStateGuilds_ReturnsCopiesNotLiveGuilds(t *testing.T) {
	state := newTestDiscordState(t, 2)

	live, err := state.Guild("guild-0")
	if err != nil {
		t.Fatalf("Guild: %v", err)
	}

	snap := snapshotStateGuilds(state)
	var got *discordgo.Guild
	for _, g := range snap {
		if g.ID == "guild-0" {
			got = g
		}
	}
	if got == nil {
		t.Fatalf("guild-0 missing from snapshot")
	}

	if got == live {
		t.Fatal("snapshot returned the LIVE *Guild from discordgo state; the gateway goroutine overwrites it in place under State.Lock()")
	}
	if len(got.Channels) > 0 && len(live.Channels) > 0 && got.Channels[0] == live.Channels[0] {
		t.Fatal("snapshot returned the LIVE *Channel pointers; ChannelAdd overwrites them in place under State.Lock()")
	}
}

// TestSnapshotStateGuilds_SnapshotIsStableAcrossStateMutation pins the whole
// point of copying: once taken, the snapshot the prune path iterates must not
// change underneath it when the gateway mutates state. Deterministic.
func TestSnapshotStateGuilds_SnapshotIsStableAcrossStateMutation(t *testing.T) {
	state := newTestDiscordState(t, 1)

	snap := snapshotStateGuilds(state)
	if len(snap) != 1 {
		t.Fatalf("got %d guilds, want 1", len(snap))
	}
	if rules := guildSyncStyleRead(snap[0]); rules != "be nice" {
		t.Fatalf("rules topic = %q, want %q", rules, "be nice")
	}

	// The gateway re-delivers the guild (in-place *g = *guild), renames it,
	// and drops the #rules channel.
	if err := state.GuildAdd(&discordgo.Guild{
		ID:          "guild-0",
		Name:        "Renamed",
		OwnerID:     "someone-else",
		Description: "changed",
		Channels: []*discordgo.Channel{
			{ID: "guild-0-general", GuildID: "guild-0", Type: discordgo.ChannelTypeGuildText, Name: "general"},
		},
	}); err != nil {
		t.Fatalf("GuildAdd: %v", err)
	}

	if snap[0].Name != "Guild 0" {
		t.Errorf("snapshot guild name changed to %q after a state mutation; it aliases live state", snap[0].Name)
	}
	if snap[0].OwnerID != "owner-0" {
		t.Errorf("snapshot guild owner changed to %q after a state mutation; it aliases live state", snap[0].OwnerID)
	}
	if rules := guildSyncStyleRead(snap[0]); rules != "be nice" {
		t.Errorf("snapshot rules topic changed to %q after a state mutation; it aliases live state", rules)
	}
}

// TestSnapshotStateGuilds_DropsUncopiedLiveCollections pins the safety
// contract: reference-typed fields that the snapshot does not deep-copy must be
// cleared, not aliased. Otherwise the prune-leave path's
// `"guild_metadata": guild` zap reflect-walk reaches straight back into
// gateway-mutated slices.
func TestSnapshotStateGuilds_DropsUncopiedLiveCollections(t *testing.T) {
	pinnedAt := time.Now()
	sortOrder := discordgo.ForumSortOrderLatestActivity
	state := discordgo.NewState()
	if err := state.GuildAdd(&discordgo.Guild{
		ID:             "guild-x",
		Name:           "X",
		OwnerID:        "owner-x",
		Roles:          []*discordgo.Role{{ID: "role-1"}},
		Emojis:         []*discordgo.Emoji{{ID: "emoji-1"}},
		Stickers:       []*discordgo.Sticker{{ID: "sticker-1"}},
		Members:        []*discordgo.Member{{User: &discordgo.User{ID: "u1"}}},
		Presences:      []*discordgo.Presence{{User: &discordgo.User{ID: "u1"}}},
		VoiceStates:    []*discordgo.VoiceState{{UserID: "u1"}},
		StageInstances: []*discordgo.StageInstance{{ID: "stage-1"}},
		Threads:        []*discordgo.Channel{{ID: "thread-1", GuildID: "guild-x"}},
		Channels: []*discordgo.Channel{
			{ID: "c1", GuildID: "guild-x", Type: discordgo.ChannelTypeGuildText, Name: "rules", Topic: "t",
				Messages:             []*discordgo.Message{{ID: "m1"}},
				Recipients:           []*discordgo.User{{ID: "u1"}},
				PermissionOverwrites: []*discordgo.PermissionOverwrite{{ID: "po1"}},
				ThreadMetadata:       &discordgo.ThreadMetadata{Archived: true},
				Member:               &discordgo.ThreadMember{ID: "tm1"},
				Members:              []*discordgo.ThreadMember{{ID: "tm2"}},
				AvailableTags:        []discordgo.ForumTag{{ID: "tag1"}},
				AppliedTags:          []string{"tag1"},
				LastPinTimestamp:     &pinnedAt,
				DefaultSortOrder:     &sortOrder},
		},
	}); err != nil {
		t.Fatalf("GuildAdd: %v", err)
	}

	snap := snapshotStateGuilds(state)
	if len(snap) != 1 {
		t.Fatalf("got %d guilds, want 1", len(snap))
	}
	g := snap[0]

	// The eight Guild collections copyStateGuild clears. Channels is deep-copied
	// and Features cloned; together that is all ten of discordgo.Guild's
	// reference-typed fields.
	for name, v := range map[string]int{
		"Roles":          len(g.Roles),
		"Emojis":         len(g.Emojis),
		"Stickers":       len(g.Stickers),
		"Members":        len(g.Members),
		"Presences":      len(g.Presences),
		"VoiceStates":    len(g.VoiceStates),
		"StageInstances": len(g.StageInstances),
		"Threads":        len(g.Threads),
	} {
		if v != 0 {
			t.Errorf("snapshot still carries %s (%d entries); it aliases gateway-mutated state", name, v)
		}
	}
	if len(g.Channels) != 1 {
		t.Fatalf("snapshot lost the channels the prune path reads: %+v", g.Channels)
	}
	// Every reference-typed field on discordgo.Channel must be either
	// deep-copied or cleared. `go doc discordgo.Channel` enumerates exactly
	// these ten; none is read through the snapshot, so all ten are cleared.
	ch := g.Channels[0]
	for name, v := range map[string]int{
		"Messages":             len(ch.Messages),
		"Recipients":           len(ch.Recipients),
		"PermissionOverwrites": len(ch.PermissionOverwrites),
		"Members":              len(ch.Members),
		"AvailableTags":        len(ch.AvailableTags),
		"AppliedTags":          len(ch.AppliedTags),
	} {
		if v != 0 {
			t.Errorf("snapshot channel still carries %s (%d entries); it aliases gateway-mutated state", name, v)
		}
	}
	for name, ptr := range map[string]bool{
		"ThreadMetadata":   ch.ThreadMetadata != nil,
		"Member":           ch.Member != nil,
		"LastPinTimestamp": ch.LastPinTimestamp != nil,
		"DefaultSortOrder": ch.DefaultSortOrder != nil,
	} {
		if ptr {
			t.Errorf("snapshot channel still aliases %s; clear it or deep-copy it", name)
		}
	}
	// The scalar fields guildSync actually reads must survive.
	if ch.ID != "c1" || ch.Name != "rules" || ch.Topic != "t" || ch.Type != discordgo.ChannelTypeGuildText {
		t.Errorf("snapshot dropped the channel scalars guildSync reads: %+v", ch)
	}
}

// populateNilReferenceFields fills every nil reference-typed field on a struct
// with a non-nil value. It is what makes the aliasing guard below survive a
// discordgo upgrade: a field added by a future version is populated here by
// reflection, without this test knowing its name, so copyStateGuild's shallow
// `c := *g` cannot silently start aliasing it.
func populateNilReferenceFields(t *testing.T, v reflect.Value) {
	t.Helper()
	typ := v.Type()
	for i := 0; i < typ.NumField(); i++ {
		if !typ.Field(i).IsExported() {
			continue
		}
		fv := v.Field(i)
		switch fv.Kind() {
		case reflect.Slice:
			if fv.IsNil() {
				fv.Set(reflect.MakeSlice(fv.Type(), 1, 1))
			}
		case reflect.Map:
			if fv.IsNil() {
				fv.Set(reflect.MakeMap(fv.Type()))
			}
		case reflect.Pointer:
			if fv.IsNil() {
				fv.Set(reflect.New(fv.Type().Elem()))
			}
		}
	}
}

// assertNoLiveAliasing checks the contract copyStateGuild documents: every
// reference-typed field is either CLEARED (nil) or deep-copied. Sharing backing
// memory with the live object is the one thing it must never do, because the
// gateway goroutine mutates that memory under State.Lock().
func assertNoLiveAliasing(t *testing.T, label string, snap, live reflect.Value) {
	t.Helper()
	typ := snap.Type()
	for i := 0; i < typ.NumField(); i++ {
		f := typ.Field(i)
		if !f.IsExported() {
			continue
		}
		sv, lv := snap.Field(i), live.Field(i)
		switch sv.Kind() {
		case reflect.Slice, reflect.Map, reflect.Pointer, reflect.Chan, reflect.Func:
		default:
			continue
		}
		if sv.IsNil() || lv.IsNil() {
			continue // cleared, or nothing to alias
		}
		if sv.Kind() == reflect.Slice && (sv.Len() == 0 || lv.Len() == 0) {
			continue // no backing array to share
		}
		if sv.Pointer() == lv.Pointer() {
			t.Errorf("%s.%s aliases live discordgo state (both %#x); copyStateGuild must deep-copy it or clear it",
				label, f.Name, sv.Pointer())
		}
	}
}

// TestSnapshotStateGuilds_NoFieldAliasesLiveState is the self-enforcing version
// of the copy contract. The table-driven test above names the fields it knows;
// this one reflects over EVERY exported reference-typed field of Guild and
// Channel, so a discordgo upgrade that adds one fails here instead of silently
// handing gateway-mutated memory to the prune-leave path's zap reflect-walk.
func TestSnapshotStateGuilds_NoFieldAliasesLiveState(t *testing.T) {
	ch := &discordgo.Channel{
		ID: "c1", GuildID: "guild-a", Type: discordgo.ChannelTypeGuildText, Name: "rules", Topic: "t",
	}
	populateNilReferenceFields(t, reflect.ValueOf(ch).Elem())

	g := &discordgo.Guild{
		ID: "guild-a", Name: "A", OwnerID: "owner-a",
		Channels: []*discordgo.Channel{ch},
	}
	populateNilReferenceFields(t, reflect.ValueOf(g).Elem())

	// State.Guilds is set directly rather than via GuildAdd: the reflective
	// fixture leaves nil elements inside the pointer slices it fills, and
	// GuildAdd dereferences guild.Threads[i] (state.go:103-105 @ v0.29.0) and,
	// via createMemberMap, guild.Members[i].User (state.go:108-109). Only the
	// slice headers matter
	// for aliasing, and snapshotStateGuilds reads State.Guilds directly.
	state := discordgo.NewState()
	state.Guilds = []*discordgo.Guild{g}
	live := g

	snap := snapshotStateGuilds(state)
	if len(snap) != 1 {
		t.Fatalf("got %d guilds, want 1", len(snap))
	}
	got := snap[0]

	assertNoLiveAliasing(t, "Guild", reflect.ValueOf(got).Elem(), reflect.ValueOf(live).Elem())

	if len(got.Channels) != 1 || len(live.Channels) != 1 {
		t.Fatalf("channels not carried: snapshot=%d live=%d", len(got.Channels), len(live.Channels))
	}
	if got.Channels[0] == live.Channels[0] {
		t.Fatal("snapshot reused the live *Channel pointer")
	}
	assertNoLiveAliasing(t, "Channel", reflect.ValueOf(got.Channels[0]).Elem(), reflect.ValueOf(live.Channels[0]).Elem())
}

// TestSnapshotStateGuilds_SafeAgainstGatewayChannelMutation proves the guilds
// handed to a deep reader are not the live objects the gateway mutates via
// ChannelAdd / ChannelRemove. Fails under -race without the fix.
func TestSnapshotStateGuilds_SafeAgainstGatewayChannelMutation(t *testing.T) {
	const guilds = 4
	state := newTestDiscordState(t, guilds)

	stop := make(chan struct{})
	gatewayDone := make(chan struct{})
	go func() {
		defer close(gatewayDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			guildID := fmt.Sprintf("guild-%d", i%guilds)
			// A bounded churn set: the same channel is added and removed, so
			// guild.Channels is appended to and compacted repeatedly without
			// the state growing without bound.
			ch := &discordgo.Channel{
				ID:      fmt.Sprintf("%s-churn", guildID),
				GuildID: guildID,
				Type:    discordgo.ChannelTypeGuildText,
				Name:    "rules",
				Topic:   "churn",
			}
			_ = state.ChannelAdd(ch)
			_ = state.ChannelRemove(ch)
		}
	}()

	for i := 0; i < 500; i++ {
		for _, g := range snapshotStateGuilds(state) {
			_ = guildSyncStyleRead(g)
		}
	}

	close(stop)
	<-gatewayDone
}

// TestSnapshotStateGuilds_SafeAgainstGatewayGuildMutation covers the other two
// live-state mutations GuildAdd performs under State.Lock(): appending to
// State.Guilds and overwriting an existing *Guild in place. Fails under -race
// without the fix.
func TestSnapshotStateGuilds_SafeAgainstGatewayGuildMutation(t *testing.T) {
	const guilds = 4
	state := newTestDiscordState(t, guilds)

	stop := make(chan struct{})
	gatewayDone := make(chan struct{})
	go func() {
		defer close(gatewayDone)
		for i := 0; ; i++ {
			select {
			case <-stop:
				return
			default:
			}
			// Re-deliver an existing guild (in-place *g = *guild overwrite)...
			existingID := fmt.Sprintf("guild-%d", i%guilds)
			_ = state.GuildAdd(&discordgo.Guild{
				ID:          existingID,
				Name:        fmt.Sprintf("Guild %d rev %d", i%guilds, i),
				OwnerID:     fmt.Sprintf("owner-%d", i),
				Description: "updated",
				Icon:        fmt.Sprintf("icon-%d", i),
				Channels: []*discordgo.Channel{
					{ID: existingID + "-rules", GuildID: existingID, Type: discordgo.ChannelTypeGuildText, Name: "rules", Topic: "rev"},
				},
			})
			// ...and join a guild from a bounded pool, which appends to
			// State.Guilds the first time each ID is seen. Bounded so the
			// snapshot cost stays constant instead of growing quadratically.
			newID := fmt.Sprintf("guild-new-%d", i%guilds)
			_ = state.GuildAdd(&discordgo.Guild{ID: newID, Name: newID, OwnerID: "owner-new"})
		}
	}()

	for i := 0; i < 500; i++ {
		for _, g := range snapshotStateGuilds(state) {
			_ = guildSyncStyleRead(g)
		}
	}

	close(stop)
	<-gatewayDone
}

// TestSnapshotStateGuilds_Contents pins what the snapshot must contain: every
// guild, with the fields the prune path and guildSync read.
func TestSnapshotStateGuilds_Contents(t *testing.T) {
	state := newTestDiscordState(t, 3)

	got := snapshotStateGuilds(state)
	if len(got) != 3 {
		t.Fatalf("got %d guilds, want 3", len(got))
	}

	byID := make(map[string]*discordgo.Guild, len(got))
	for _, g := range got {
		byID[g.ID] = g
	}
	g, ok := byID["guild-1"]
	if !ok {
		t.Fatalf("guild-1 missing from snapshot: %v", byID)
	}
	if g.Name != "Guild 1" || g.OwnerID != "owner-1" || g.Description != "a guild" {
		t.Errorf("guild-1 fields not carried over: %+v", g)
	}
	if g.IconURL("512") == "" {
		t.Errorf("guild-1 icon not carried over; guildSync passes IconURL to GroupCreate/GroupUpdate")
	}
	if len(g.Channels) != 2 {
		t.Fatalf("guild-1 channels not carried over: %+v", g.Channels)
	}
	if rules := guildSyncStyleRead(g); rules != "be nice" {
		t.Errorf("rules topic = %q, want %q", rules, "be nice")
	}
}

// TestSnapshotStateGuilds_NilState covers the defensive path: a session with no
// state must not panic the prune job.
func TestSnapshotStateGuilds_NilState(t *testing.T) {
	if got := snapshotStateGuilds(nil); got != nil {
		t.Fatalf("got %v, want nil", got)
	}
}

// TestSnapshotStateGuilds_EmptyState pins the signal pruneGuildGroups relies on
// to abort: an empty state yields an empty snapshot.
func TestSnapshotStateGuilds_EmptyState(t *testing.T) {
	if got := snapshotStateGuilds(discordgo.NewState()); len(got) != 0 {
		t.Fatalf("got %d guilds, want 0", len(got))
	}
}

// TestSnapshotStateGuilds_SkipsNilGuild covers a defensive path: a nil entry in
// State.Guilds must not panic the prune job.
func TestSnapshotStateGuilds_SkipsNilGuild(t *testing.T) {
	state := newTestDiscordState(t, 1)
	state.Guilds = append(state.Guilds, nil)

	got := snapshotStateGuilds(state)
	for _, g := range got {
		if g == nil {
			t.Fatal("snapshot carried a nil *Guild through to the prune path")
		}
	}
	if len(got) != 1 {
		t.Fatalf("got %d guilds, want 1", len(got))
	}
}
