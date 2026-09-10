package server

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// pagedLoginHistoryNK serves LoginHistory objects one page per StorageList call
// and runs beforePage first, so a test can change the world between pages.
// Reads and writes fall through to occTestNakamaModule (writeAttempts counts
// every StorageWrite).
type pagedLoginHistoryNK struct {
	*occTestNakamaModule
	pages      [][]*api.StorageObject
	beforePage func(page int)
}

func (m *pagedLoginHistoryNK) StorageList(ctx context.Context, callerID, userID, collection string, limit int, cursor string) ([]*api.StorageObject, string, error) {
	page := 0
	if cursor != "" {
		page, _ = strconv.Atoi(cursor)
	}
	if m.beforePage != nil {
		m.beforePage(page)
	}
	next := ""
	if page+1 < len(m.pages) {
		next = strconv.Itoa(page + 1)
	}
	return m.pages[page], next, nil
}

const (
	scanUserC = "0c0c0c0c-0000-4000-8000-00000000000c"
	scanUserD = "0d0d0d0d-0000-4000-8000-00000000000d"
)

// newSharedIPLinkScan stores users C and D linked to each other by one client
// IP and nothing else, each with a login history entry for that IP carrying
// asn (0: none recorded), and serves C's record as the single page to scan.
func newSharedIPLinkScan(t *testing.T, ip string, asn int) *pagedLoginHistoryNK {
	t.Helper()
	link := func(self, other string, xpid evr.EvrId) string {
		h := NewLoginHistory(self)
		h.History = map[string]*LoginHistoryEntry{
			loginHistoryEntryKey(xpid, ip): {
				UpdatedAt: time.Now(),
				XPID:      xpid,
				ClientIP:  ip,
				LoginData: &evr.LoginProfile{},
				ASN:       asn,
			},
		}
		h.AlternateMatches = map[string][]*AlternateSearchMatch{
			other: {{OtherUserID: other, Items: []string{ip}}},
		}
		raw, err := json.Marshal(h)
		if err != nil {
			t.Fatalf("marshal history: %v", err)
		}
		return string(raw)
	}

	c := link(scanUserC, scanUserD, evr.EvrId{PlatformCode: evr.OVR, AccountId: 12})
	d := link(scanUserD, scanUserC, evr.EvrId{PlatformCode: evr.OVR, AccountId: 13})
	nk := &pagedLoginHistoryNK{occTestNakamaModule: newOCCTestNakamaModule()}
	nk.seedObject(scanUserD, LoginStorageCollection, LoginHistoryStorageKey, d)
	verC := nk.seedObject(scanUserC, LoginStorageCollection, LoginHistoryStorageKey, c)
	nk.pages = [][]*api.StorageObject{
		{{Collection: LoginStorageCollection, Key: LoginHistoryStorageKey, UserId: scanUserC, Value: c, Version: verC}},
	}
	return nk
}

// The two scans below DELETE alt links on a positive weak/ignored verdict. With
// recorded ASNs that verdict needs positive evidence -- a configured CIDR or a
// configured ASN recorded for the address -- so no readiness gate stands in
// front of them any more: a history that is not yet backfilled has nothing to
// delete on, and keeps its links. No IP->ASN range data is loaded anywhere.
func TestRunCGNATCleanup_DecidesFromRecordedASN(t *testing.T) {
	tests := []struct {
		name       string
		asn        int
		wantBroken int
	}{
		{"starlink ASN recorded: the link rests on a shared address", starlinkASN, 1},
		{"no ASN recorded: the address is not known to be shared", 0, 0},
		{"residential ASN recorded", residentialASN, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := withDetector(t, seededCGNATSettings())
			nk := newSharedIPLinkScan(t, starlinkIP, tt.asn)

			broken, _, _, err := runCGNATCleanup(context.Background(), NewRuntimeGoLogger(zap.NewNop()), nk, d)
			if err != nil {
				t.Fatalf("runCGNATCleanup: %v", err)
			}
			if broken != tt.wantBroken {
				t.Errorf("runCGNATCleanup broke %d links, want %d; the C-D link rests on %s with AS%d recorded", broken, tt.wantBroken, starlinkIP, tt.asn)
			}
		})
	}
}

func TestMigrationBreakIgnoredAlts_DecidesFromRecordedASN(t *testing.T) {
	tests := []struct {
		name       string
		asn        int
		wantWrites bool
	}{
		{"starlink ASN recorded: the link rests on an ignored address", starlinkASN, true},
		{"no ASN recorded: the address is not known to be shared", 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			withDetector(t, seededCGNATSettings())
			nk := newSharedIPLinkScan(t, starlinkIP, tt.asn)

			if err := (&MigrationBreakIgnoredAlts{}).MigrateSystem(context.Background(), NewRuntimeGoLogger(zap.NewNop()), nil, nk); err != nil {
				t.Fatalf("MigrateSystem: %v", err)
			}
			if got := nk.writeAttempts > 0; got != tt.wantWrites {
				t.Errorf("MigrationBreakIgnoredAlts made %d storage writes; want link broken = %v for %s with AS%d recorded", nk.writeAttempts, tt.wantWrites, starlinkIP, tt.asn)
			}
		})
	}
}
