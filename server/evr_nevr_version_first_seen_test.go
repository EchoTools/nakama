package server

import (
	"strconv"
	"testing"
	"time"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func nevrLogin(version string) *evr.LoginProfile {
	if version == "" {
		return &evr.LoginProfile{AccountId: 1}
	}
	return &evr.LoginProfile{AccountId: 1, NevrIdentity: &evr.NevrIdentity{Version: version}}
}

// historyWith returns a LoginHistory with one entry per (version, updated) pair, each on its own IP.
func historyWith(entries map[string]time.Time) *LoginHistory {
	h := NewLoginHistory("user-1")
	i := 0
	for version, at := range entries {
		i++
		key := loginHistoryEntryKey(evr.EvrId{PlatformCode: 4, AccountId: 1}, string(rune('a'+i)))
		if h.History == nil {
			h.History = map[string]*LoginHistoryEntry{}
		}
		h.History[key] = &LoginHistoryEntry{UpdatedAt: at, LoginData: nevrLogin(version)}
	}
	return h
}

func TestNevrVersionFirstSeenToday(t *testing.T) {
	now := time.Date(2026, 11, 11, 15, 0, 0, 0, time.UTC)
	today := time.Date(2026, 11, 11, 0, 1, 0, 0, time.UTC)
	yesterday := time.Date(2026, 11, 10, 23, 59, 0, 0, time.UTC)

	cases := []struct {
		name    string
		history *LoginHistory
		login   *evr.LoginProfile
		want    bool
	}{
		{"no history is first seen", NewLoginHistory("u"), nevrLogin("6.16.5"), true},
		{"same version earlier today is not", historyWith(map[string]time.Time{"6.16.5": today}), nevrLogin("6.16.5"), false},
		{"same version yesterday is first seen today", historyWith(map[string]time.Time{"6.16.5": yesterday}), nevrLogin("6.16.5"), true},
		{"another version today does not count", historyWith(map[string]time.Time{"6.16.4": today}), nevrLogin("6.16.5"), true},
		{"build metadata does not make a new version", historyWith(map[string]time.Time{"6.16.5+abc": today}), nevrLogin("6.16.5"), false},
		{"stock seen today", historyWith(map[string]time.Time{"": today}), nevrLogin(""), false},
		{"stock first seen", historyWith(map[string]time.Time{"6.16.5": today}), nevrLogin(""), true},
		{"entry with no login data is ignored", func() *LoginHistory {
			h := NewLoginHistory("u")
			h.History = map[string]*LoginHistoryEntry{"k": {UpdatedAt: today}}
			return h
		}(), nevrLogin("6.16.5"), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := nevrVersionFirstSeenToday(tc.history, normalizeNevrVersion(tc.login.NevrIdentity), now)
			if got != tc.want {
				t.Fatalf("nevrVersionFirstSeenToday = %v, want %v", got, tc.want)
			}
		})
	}
}

// Two logins on one UTC day from one account count once; the next day counts again; the counter carries
// only the bounded nevr_version label.
func TestRecordNevrVersionFirstSeenCountsOncePerAccountPerDay(t *testing.T) {
	core, logs := observer.New(zap.InfoLevel)
	logger := zap.New(core)
	m := newRecordingMetrics()
	labels := newNevrVersionLabeler()
	h := NewLoginHistory("user-1")

	day1 := time.Date(2026, 11, 11, 9, 0, 0, 0, time.UTC)
	login := nevrLogin("6.16.5")

	record := func(at time.Time) {
		recordNevrVersionFirstSeen(logger, m, labels, h, "user-1", login, at)
		h.Update(evr.EvrId{PlatformCode: 4, AccountId: 1}, "10.0.0.1", login, true)
		h.History[loginHistoryEntryKey(evr.EvrId{PlatformCode: 4, AccountId: 1}, "10.0.0.1")].UpdatedAt = at
	}

	record(day1)
	record(day1.Add(3 * time.Hour))
	if got := m.count(nevrVersionFirstSeenCounter); got != 1 {
		t.Fatalf("after two same-day logins counter = %d, want 1", got)
	}
	tags := m.tagsFor(nevrVersionFirstSeenCounter)
	if len(tags) != 1 || tags[nevrVersionTag] != "6.16.5" {
		t.Fatalf("counter tags = %v, want only nevr_version=6.16.5", tags)
	}

	record(day1.Add(24 * time.Hour))
	if got := m.count(nevrVersionFirstSeenCounter); got != 2 {
		t.Fatalf("after a login the next day counter = %d, want 2", got)
	}

	if n := logs.FilterMessage("nevr version first seen").Len(); n != 2 {
		t.Fatalf("first-seen log records = %d, want 2", n)
	}
	rec := logs.FilterMessage("nevr version first seen").All()[0].ContextMap()
	if rec["user_id"] != "user-1" || rec["nevr_version"] != "6.16.5" {
		t.Fatalf("log record = %v, want user_id and nevr_version", rec)
	}
}

// A client that varies its version cannot mint more counter series than the label cap.
func TestRecordNevrVersionFirstSeenStaysBounded(t *testing.T) {
	m := newRecordingMetrics()
	labels := newNevrVersionLabeler()
	seen := map[string]struct{}{}
	for i := 0; i < 1000; i++ {
		h := NewLoginHistory("u")
		v := "1." + strconv.Itoa(i) + ".0"
		recordNevrVersionFirstSeen(zap.NewNop(), m, labels, h, "u", nevrLogin(v), time.Now())
		seen[m.tagsFor(nevrVersionFirstSeenCounter)[nevrVersionTag]] = struct{}{}
	}
	if len(seen) > nevrVersionTagCardinalityBound {
		t.Fatalf("counter took %d distinct nevr_version values, bound %d", len(seen), nevrVersionTagCardinalityBound)
	}
}
