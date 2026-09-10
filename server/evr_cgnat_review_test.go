package server

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"go.uber.org/zap"
)

// TestRefreshASNData_InvalidConfiguredRowIsAnError: a configured-ASN row that
// does not describe a range in its dataset's family must not be dropped
// silently. convertToRanges4/6 skip such rows, so without a check the family
// would be marked covered with that range missing -- every address in it
// answered not-CGNAT, and the table persisted.
func TestRefreshASNData_InvalidConfiguredRowIsAnError(t *testing.T) {
	tests := []struct {
		name   string
		family asnFamily
		row    string
	}{
		{"unparseable endpoint in v4", asnFamilyV4, "not-an-ip\t129.223.255.255\t14593\tUS\tSPACEX-STARLINK"},
		{"IPv6 range in the v4 dataset", asnFamilyV4, "2406:2d41::\t2406:2d41:ffff:ffff:ffff:ffff:ffff:ffff\t14593\tUS\tSPACEX-STARLINK"},
		{"IPv4 range in the v6 dataset", asnFamilyV6, "129.223.0.0\t129.223.255.255\t14593\tUS\tSPACEX-STARLINK"},
		{"reversed v4 range", asnFamilyV4, "129.224.255.255\t129.224.0.0\t14593\tUS\tSPACEX-STARLINK"},
		{"reversed v6 range", asnFamilyV6, "2406:2d42:ffff::\t2406:2d42::\t14593\tUS\tSPACEX-STARLINK"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d, f := newFixtureDetector(t, seededCGNATSettings())
			rows := slices.Clone(asnFixtureV4)
			if tt.family == asnFamilyV6 {
				rows = slices.Clone(asnFixtureV6)
			}
			f.gz[tt.family] = gzipTSV(t, append(rows, tt.row))

			err := d.RefreshASNData(context.Background(), nil)
			if err == nil {
				t.Fatalf("RefreshASNData returned nil with an invalid AS14593 row in the %s dataset", tt.family)
			}
			if !strings.Contains(err.Error(), tt.family.String()) {
				t.Errorf("error %q does not name the %s family", err, tt.family)
			}
			if d.ASNDataReady() {
				t.Errorf("ASNDataReady() = true after the %s dataset carried an invalid configured row", tt.family)
			}
		})
	}
}

// TestLoadASNRanges_RejectsInvalidStoredRow: the stored object is held to the
// same rule as a download.
func TestLoadASNRanges_RejectsInvalidStoredRow(t *testing.T) {
	stored := cgnatASNRangesData{
		V4: cgnatASNFamilyData{ASNs: []int{14593, 21928}, Ranges: []rawASNRange{
			{Start: "129.222.0.0", End: "129.222.255.255", ASN: 14593},
			{Start: "2406:2d40::", End: "2406:2d40::ffff", ASN: 14593}, // wrong family
		}},
		V6: cgnatASNFamilyData{ASNs: []int{14593, 21928}},
	}
	raw, err := json.Marshal(stored)
	if err != nil {
		t.Fatal(err)
	}
	nk := newOCCTestNakamaModule()
	nk.seedObject(SystemUserID, CGNATASNRangesStorageCollection, CGNATASNRangesStorageKey, string(raw))

	d := NewCGNATDetector(nil)
	d.UpdateSettings(seededCGNATSettings())
	if err := d.LoadASNRanges(context.Background(), nk); err == nil {
		t.Fatal("LoadASNRanges accepted a stored IPv4 family containing an IPv6 range")
	}
	if d.ASNDataReady() {
		t.Error("ASNDataReady() = true after rejecting the stored ranges")
	}
}

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

// newReadinessDropScan builds a two-page scan: page 0 is empty, page 1 holds
// user C linked to user D only by a residential IP -- a strong signal while the
// detector is ready. Before page 1 is served, an ASN is added to settings, so
// the detector is no longer ready and that same IP classifies as shared.
func newReadinessDropScan(t *testing.T, d *CGNATDetector) *pagedLoginHistoryNK {
	t.Helper()
	link := func(self, other string) string {
		h := NewLoginHistory(self)
		h.AlternateMatches = map[string][]*AlternateSearchMatch{
			other: {{OtherUserID: other, Items: []string{probeComcast4}}},
		}
		raw, err := json.Marshal(h)
		if err != nil {
			t.Fatalf("marshal history: %v", err)
		}
		return string(raw)
	}

	nk := &pagedLoginHistoryNK{occTestNakamaModule: newOCCTestNakamaModule()}
	nk.seedObject(scanUserD, LoginStorageCollection, LoginHistoryStorageKey, link(scanUserD, scanUserC))
	verC := nk.seedObject(scanUserC, LoginStorageCollection, LoginHistoryStorageKey, link(scanUserC, scanUserD))
	nk.pages = [][]*api.StorageObject{
		nil,
		{{Collection: LoginStorageCollection, Key: LoginHistoryStorageKey, UserId: scanUserC, Value: link(scanUserC, scanUserD), Version: verC}},
	}

	withVodafone := seededCGNATSettings()
	withVodafone.ASNs = append(withVodafone.ASNs, 3209)
	nk.beforePage = func(page int) {
		if page == 1 {
			d.UpdateSettings(withVodafone)
		}
	}
	return nk
}

func readyDetector(t *testing.T) *CGNATDetector {
	t.Helper()
	d := withDetector(t, seededCGNATSettings())
	markASNDataLoaded(t, d, nil, nil)
	if !d.ASNDataReady() {
		t.Fatal("precondition: detector should be ready")
	}
	return d
}

// TestRunCGNATCleanup_StopsWhenReadinessDropsMidScan: readiness is not a
// property of the moment the scan starts. An ASN added to settings while the
// cleanup walks storage makes every public address in the uncovered family
// classify as weak from then on; the cleanup must stop, not delete on it.
func TestRunCGNATCleanup_StopsWhenReadinessDropsMidScan(t *testing.T) {
	d := readyDetector(t)
	nk := newReadinessDropScan(t, d)

	broken, _, _, err := runCGNATCleanup(context.Background(), NewRuntimeGoLogger(zap.NewNop()), nk, d)
	if !errors.Is(err, ErrASNDataNotReady) {
		t.Errorf("runCGNATCleanup returned %v after readiness dropped mid-scan, want ErrASNDataNotReady", err)
	}
	if broken != 0 || nk.writeAttempts != 0 {
		t.Errorf("runCGNATCleanup broke %d links (%d storage writes) after readiness dropped; the C-D link rests on a residential IP", broken, nk.writeAttempts)
	}
}

// TestMigrationBreakIgnoredAlts_StopsWhenReadinessDropsMidScan: same, through
// matchIgnoredAltPattern.
func TestMigrationBreakIgnoredAlts_StopsWhenReadinessDropsMidScan(t *testing.T) {
	d := readyDetector(t)
	nk := newReadinessDropScan(t, d)

	err := (&MigrationBreakIgnoredAlts{}).MigrateSystem(context.Background(), NewRuntimeGoLogger(zap.NewNop()), nil, nk)
	if !errors.Is(err, ErrASNDataNotReady) {
		t.Errorf("MigrationBreakIgnoredAlts returned %v after readiness dropped mid-scan, want ErrASNDataNotReady", err)
	}
	if nk.writeAttempts != 0 {
		t.Errorf("MigrationBreakIgnoredAlts made %d storage writes after readiness dropped; the C-D link rests on a residential IP", nk.writeAttempts)
	}
}
