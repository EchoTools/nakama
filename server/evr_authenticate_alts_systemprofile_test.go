package server

import (
	"slices"
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

// --- SystemProfile is a comparison key, not a discovery key -----------------
//
// v3.27.2-evr.321 (56e9a9c2d, issue #516) added e.SystemProfile() to
// AltSearchPatterns, promoting the machine profile from a key that could
// CORROBORATE a link found some other way to one that could SURFACE a link on
// its own.
//
// Nothing in the string is machine-unique. It is
// headset_model::network_type::video_card::cpu_model plus four integers, and
// two people who bought the same headset produce the same bytes. Measured
// against production on 2026-08-12:
//
//	12,803 accounts share "Meta Quest 2::WIFI::::Unknown::3::8::0::0"
//	26,787 of 45,967 accounts sit in some collision group
//	 1,920 are enforcement-eligible after the commodity-profile filter
//	    49 currently share a profile with a disabled account
//
// So the discovery query returns strangers, and the comparison that follows
// confirms the very key that surfaced them. As a comparison key it is still
// useful and still runs: it corroborates a candidate that an IP, HMD serial or
// XPID already surfaced. Only the discovery role is withdrawn.

// sharedProfileAccounts builds two accounts that share a system profile and
// nothing else. Every hand-rotatable key differs.
func sharedProfileAccounts(t *testing.T) (a, b *LoginHistory, sharedProfile string) {
	t.Helper()
	sysinfo := richSystemInfo()

	newAccount := func(userID string, accountID uint64, ip, serial string) *LoginHistory {
		e := &LoginHistoryEntry{
			XPID:      evr.EvrId{PlatformCode: evr.OVR, AccountId: accountID},
			ClientIP:  ip,
			UpdatedAt: time.Now(),
			LoginData: &evr.LoginProfile{
				HMDSerialNumber: serial,
				SystemInfo:      sysinfo,
			},
		}
		h := &LoginHistory{userID: userID, History: map[string]*LoginHistoryEntry{e.Key(): e}}
		h.rebuildCache()
		return h
	}

	a = newAccount("user-a", 11111, "45.33.90.154", "SERIAL-A")
	b = newAccount("user-b", 22222, "198.51.100.7", "SERIAL-B")

	for _, e := range a.History {
		sharedProfile = e.SystemProfile()
	}

	// Guard the premise. If these two shared anything else the test would pass
	// or fail for a reason that has nothing to do with the profile.
	for _, e := range b.History {
		if e.SystemProfile() != sharedProfile {
			t.Fatalf("premise broken: the two accounts do not share a profile (%q vs %q)", sharedProfile, e.SystemProfile())
		}
	}
	for _, key := range []string{"45.33.90.154", "SERIAL-A"} {
		if slices.Contains(b.Cache, key) {
			t.Fatalf("premise broken: account b's cache contains account a's key %q", key)
		}
	}
	return a, b, sharedProfile
}

// The discovery query in LoginAlternatePatternSearch is
// `+value.cache:<AltSearchPatterns()>` against the indexed `cache` field, so
// one account surfaces the other exactly when one of its search patterns
// appears in the other's cache. Nothing the query does not return is ever
// passed to loginHistoryCompare.
func TestAltSearchPatterns_SystemProfileDoesNotDiscoverAlts(t *testing.T) {
	a, b, sharedProfile := sharedProfileAccounts(t)

	if slices.Contains(a.AltSearchPatterns(), sharedProfile) {
		t.Errorf("AltSearchPatterns() still queries the index on the system profile %q; a profile shared by 12,803 production accounts is a bucket, not a fingerprint", sharedProfile)
	}

	var hits []string
	for _, p := range a.AltSearchPatterns() {
		if slices.Contains(b.Cache, p) {
			hits = append(hits, p)
		}
	}
	if len(hits) > 0 {
		t.Errorf("two accounts sharing only a system profile are still discoverable as alts of each other: patterns %v matched %v in the other account's cache", hits, hits)
	}
}

// The narrowing must be exactly this narrow. The profile stays in the indexed
// cache and stays a comparison key, so a candidate surfaced by an IP, serial or
// XPID is still corroborated by it.
func TestLoginHistoryCompare_StillComparesSystemProfile(t *testing.T) {
	a, b, sharedProfile := sharedProfileAccounts(t)

	matches := loginHistoryCompare(a, b)
	if len(matches) == 0 {
		t.Fatalf("loginHistoryCompare no longer forms an edge on a shared system profile; the profile must remain a COMPARISON key")
	}
	found := false
	for _, m := range matches {
		if slices.Contains(m.Items, sharedProfile) {
			found = true
		}
	}
	if !found {
		t.Errorf("loginHistoryCompare formed an edge but did not report the shared system profile %q among its items; got %+v", sharedProfile, matches)
	}
}

// The profile must also stay in the indexed cache: dropping it there would
// break the comparison for anyone reading an alt's stored record, which is a
// wider change than withdrawing the discovery role.
func TestRebuildCache_StillContainsSystemProfile(t *testing.T) {
	a, _, sharedProfile := sharedProfileAccounts(t)

	if !slices.Contains(a.Cache, sharedProfile) {
		t.Errorf("rebuildCache dropped the system profile %q from the indexed cache; only the DISCOVERY role was meant to change, got %v", sharedProfile, a.Cache)
	}
}

// The keys that are actually machine- or account-specific must keep working as
// discovery keys. This is the control: it stays green through the change, so a
// green run of the tests above cannot be explained by AltSearchPatterns having
// been emptied.
func TestAltSearchPatterns_RotatableKeysStillDiscover(t *testing.T) {
	a, b, _ := sharedProfileAccounts(t)

	for _, key := range []string{"45.33.90.154", "SERIAL-A"} {
		if !slices.Contains(a.AltSearchPatterns(), key) {
			t.Errorf("AltSearchPatterns() no longer includes %q", key)
		}
	}
	if len(b.AltSearchPatterns()) == 0 {
		t.Error("AltSearchPatterns() returned nothing at all")
	}
}
