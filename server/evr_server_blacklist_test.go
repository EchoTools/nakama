package server

import (
	"context"
	"net"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func socialMatch(extIP string) *MatchLabelMeta {
	var gs *GameServerPresence
	if extIP != "" {
		gs = &GameServerPresence{Endpoint: evr.Endpoint{ExternalIP: net.ParseIP(extIP)}}
	}
	return &MatchLabelMeta{State: &MatchLabel{GameServer: gs}}
}

// TestFilterBlacklistedSocialMatches verifies the social find/create filter
// removes matches on blacklisted servers and is a no-op for an empty blacklist.
func TestFilterBlacklistedSocialMatches(t *testing.T) {
	t.Parallel()

	good := socialMatch("10.0.0.1")
	bad := socialMatch("10.0.0.2")
	noServer := socialMatch("") // pending allocation, GameServer nil

	t.Run("empty blacklist is no-op", func(t *testing.T) {
		t.Parallel()
		in := []*MatchLabelMeta{good, bad}
		out := filterBlacklistedSocialMatches(in, nil)
		if len(out) != 2 {
			t.Fatalf("expected 2 matches unchanged, got %d", len(out))
		}
	})

	t.Run("blacklisted server removed", func(t *testing.T) {
		t.Parallel()
		in := []*MatchLabelMeta{good, bad, noServer}
		bl := map[string]struct{}{"10.0.0.2": {}}
		out := filterBlacklistedSocialMatches(in, bl)
		for _, m := range out {
			if m.State.GameServer != nil && m.State.GameServer.Endpoint.GetExternalIP() == "10.0.0.2" {
				t.Fatalf("blacklisted server 10.0.0.2 was not filtered out")
			}
		}
		if len(out) != 2 {
			t.Fatalf("expected 2 remaining (good + nil-server), got %d", len(out))
		}
	})

	t.Run("unknown blacklist ip keeps everything", func(t *testing.T) {
		t.Parallel()
		in := []*MatchLabelMeta{good, bad}
		out := filterBlacklistedSocialMatches(in, map[string]struct{}{"203.0.113.1": {}})
		if len(out) != 2 {
			t.Fatalf("expected both matches kept, got %d", len(out))
		}
	})
}

// blacklistTestNK reuses the embedded-interface storage mock pattern.
// It implements only the storage methods StorableRead/StorableWrite need.
type blacklistTestNK struct {
	*mockPartyNK
}

func newBlacklistTestNK() *blacklistTestNK {
	return &blacklistTestNK{mockPartyNK: newMockPartyNK()}
}

// seed writes a raw stored value for a user's blacklist record.
func (m *blacklistTestNK) seed(userID, value string) {
	if _, ok := m.storage[ServerBlacklistStorageCollection]; !ok {
		m.storage[ServerBlacklistStorageCollection] = make(map[string]string)
	}
	m.storage[ServerBlacklistStorageCollection][ServerBlacklistStorageKey] = value
}

// perUserBlacklistNK partitions storage by userID so union-across-users can be
// exercised. It implements only the methods StorableRead needs.
type perUserBlacklistNK struct {
	runtime.NakamaModule
	// userID -> raw JSON record
	records map[string]string
}

func newPerUserBlacklistNK(records map[string]string) *perUserBlacklistNK {
	if records == nil {
		records = map[string]string{}
	}
	return &perUserBlacklistNK{records: records}
}

func (m *perUserBlacklistNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	var out []*api.StorageObject
	for _, r := range reads {
		if r.Collection != ServerBlacklistStorageCollection {
			continue
		}
		val, ok := m.records[r.UserID]
		if !ok {
			continue
		}
		out = append(out, &api.StorageObject{
			Collection: r.Collection,
			Key:        r.Key,
			UserId:     r.UserID,
			Value:      val,
		})
	}
	return out, nil
}

func TestServerBlacklist_RoundTrip(t *testing.T) {
	t.Parallel()
	nk := newBlacklistTestNK()
	ctx := context.Background()
	const userID = "user-1"

	// Add and write.
	bl := NewServerBlacklist()
	bl.Servers["1.2.3.4"] = "Chicago"
	bl.Servers["5.6.7.8"] = "5.6.7.8"
	if err := StorableWrite(ctx, nk, userID, bl); err != nil {
		t.Fatalf("StorableWrite: %v", err)
	}

	// Read back into a fresh value.
	got := NewServerBlacklist()
	if err := StorableRead(ctx, nk, userID, got, false); err != nil {
		t.Fatalf("StorableRead: %v", err)
	}
	if len(got.Servers) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(got.Servers))
	}
	if got.Servers["1.2.3.4"] != "Chicago" {
		t.Fatalf("expected label Chicago, got %q", got.Servers["1.2.3.4"])
	}

	// IPs and IPSet contain both IPs.
	ips := got.IPs()
	if len(ips) != 2 {
		t.Fatalf("IPs len = %d, want 2", len(ips))
	}
	set := got.IPSet()
	if _, ok := set["1.2.3.4"]; !ok {
		t.Fatalf("IPSet missing 1.2.3.4")
	}
	if _, ok := set["5.6.7.8"]; !ok {
		t.Fatalf("IPSet missing 5.6.7.8")
	}
}

func TestServerBlacklist_NotFoundIsEmpty(t *testing.T) {
	t.Parallel()
	nk := newBlacklistTestNK()
	ctx := context.Background()

	bl := NewServerBlacklist()
	err := StorableRead(ctx, nk, "missing-user", bl, false)
	if status.Code(err) != codes.NotFound {
		t.Fatalf("expected NotFound, got %v", err)
	}
	// Fail-open: callers use the zero blacklist, which must be safe to query.
	if len(bl.IPs()) != 0 {
		t.Fatalf("expected empty IPs on NotFound, got %v", bl.IPs())
	}
	if len(bl.IPSet()) != 0 {
		t.Fatalf("expected empty IPSet on NotFound")
	}
}

// A stored {"servers":null} unmarshals to a nil Servers map. The add path must
// not panic when writing the first entry, and IPs()/IPSet() must be safe.
func TestServerBlacklist_NullServersNoPanic(t *testing.T) {
	t.Parallel()
	nk := newBlacklistTestNK()
	ctx := context.Background()
	const userID = "null-user"
	nk.seed(userID, `{"servers":null}`)

	bl := NewServerBlacklist()
	if err := StorableRead(ctx, nk, userID, bl, false); err != nil {
		t.Fatalf("StorableRead: %v", err)
	}

	// IPs/IPSet must not panic on a nil map.
	if len(bl.IPs()) != 0 {
		t.Fatalf("expected empty IPs for null servers")
	}
	if len(bl.IPSet()) != 0 {
		t.Fatalf("expected empty IPSet for null servers")
	}

	// The add path must not panic on a nil map (assignment to nil map panics in Go).
	bl.Servers["9.9.9.9"] = "label"
	if err := StorableWrite(ctx, nk, userID, bl); err != nil {
		t.Fatalf("StorableWrite after add: %v", err)
	}

	got := NewServerBlacklist()
	if err := StorableRead(ctx, nk, userID, got, false); err != nil {
		t.Fatalf("StorableRead after add: %v", err)
	}
	if got.Servers["9.9.9.9"] != "label" {
		t.Fatalf("expected added entry to persist")
	}
}

// TestUnionBlacklistedIPs verifies the helper that post-match social allocation
// uses to avoid landing present players on a server any of them has blacklisted.
func TestUnionBlacklistedIPs(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	tests := []struct {
		name    string
		records map[string]string // userID -> raw JSON record (empty => NotFound)
		userIDs []string
		want    map[string]struct{}
	}{
		{
			name:    "no users yields empty",
			records: nil,
			userIDs: nil,
			want:    map[string]struct{}{},
		},
		{
			name:    "unknown user (NotFound) contributes nothing",
			records: nil,
			userIDs: []string{"ghost"},
			want:    map[string]struct{}{},
		},
		{
			name: "union of two players including overlap",
			records: map[string]string{
				"u1": `{"servers":{"1.1.1.1":"A","2.2.2.2":"shared"}}`,
				"u2": `{"servers":{"2.2.2.2":"shared","3.3.3.3":"C"}}`,
			},
			userIDs: []string{"u1", "u2"},
			want: map[string]struct{}{
				"1.1.1.1": {}, "2.2.2.2": {}, "3.3.3.3": {},
			},
		},
		{
			name: "null-servers record contributes nothing and does not panic",
			records: map[string]string{
				"u1": `{"servers":null}`,
			},
			userIDs: []string{"u1"},
			want:    map[string]struct{}{},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Per-subtest mock so the single-key storage doesn't collide.
			m := newPerUserBlacklistNK(tc.records)
			got := unionBlacklistedIPs(ctx, m, tc.userIDs)
			if len(got) != len(tc.want) {
				t.Fatalf("union size = %d (%v), want %d (%v)", len(got), got, len(tc.want), tc.want)
			}
			for ip := range tc.want {
				if _, ok := got[ip]; !ok {
					t.Fatalf("union missing %s; got %v", ip, got)
				}
			}
		})
	}
}

// TestBlacklistCommandsAreSelfOnly asserts the blacklist slash commands cannot
// target another user: their only option is the server IP, and the handlers
// derive the userID from the invoking Discord user. This guards against a future
// edit adding a "user" option that would let one player edit another's blacklist.
func TestBlacklistCommandsAreSelfOnly(t *testing.T) {
	t.Parallel()

	want := map[string]bool{"blacklist-server": false, "blacklist-server-remove": false}
	for _, cmd := range mainSlashCommands {
		if _, tracked := want[cmd.Name]; !tracked {
			continue
		}
		want[cmd.Name] = true
		if len(cmd.Options) != 1 {
			t.Fatalf("%s: expected exactly 1 option, got %d", cmd.Name, len(cmd.Options))
		}
		opt := cmd.Options[0]
		if opt.Name != "server" {
			t.Fatalf("%s: expected only a 'server' option, got %q", cmd.Name, opt.Name)
		}
		// A user/member/target option would imply acting on another player.
		for _, o := range cmd.Options {
			switch o.Name {
			case "user", "member", "target", "player":
				t.Fatalf("%s: must not accept a %q option (self-only command)", cmd.Name, o.Name)
			}
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("command %q not found in mainSlashCommands", name)
		}
	}
}

func TestServerBlacklist_RemoveNonexistentAndAddRemove(t *testing.T) {
	t.Parallel()
	nk := newBlacklistTestNK()
	ctx := context.Background()
	const userID = "rm-user"

	bl := NewServerBlacklist()
	// Removing from empty is a no-op (no panic).
	delete(bl.Servers, "1.1.1.1")
	if len(bl.Servers) != 0 {
		t.Fatalf("expected empty after remove-nonexistent")
	}

	// Add then remove.
	bl.Servers["1.1.1.1"] = "A"
	if err := StorableWrite(ctx, nk, userID, bl); err != nil {
		t.Fatalf("write: %v", err)
	}
	delete(bl.Servers, "1.1.1.1")
	if err := StorableWrite(ctx, nk, userID, bl); err != nil {
		t.Fatalf("write after remove: %v", err)
	}

	got := NewServerBlacklist()
	if err := StorableRead(ctx, nk, userID, got, false); err != nil {
		t.Fatalf("read: %v", err)
	}
	if _, ok := got.Servers["1.1.1.1"]; ok {
		t.Fatalf("expected 1.1.1.1 removed")
	}
}
