package server

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"slices"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// The configured ASNs' IP ranges, persisted so a process never starts cold.
//
// The detector only ever asks "is this address in a configured ASN?". For the
// two configured today (14593 Starlink, 21928 T-Mobile) that is 172 IPv4 and
// 221 IPv6 rows of iptoasn.com's 531,142 + 180,258 -- about 27 KB of TSV
// (measured 2026-09-10, #596). The /var/tmp download cache, by contrast, is
// empty in every freshly recreated container, i.e. after every deploy.
//
// Same shape as Global/settings (ServiceSettingsLoad/Save): a JSON object owned
// by SystemUserID, permissions 0/0, written unconditionally. Size is not a
// concern: the runtime write path checks only that the value is a JSON object
// (runtime_go_nakama.go StorageWrite), the column is an unbounded JSONB, and
// this package already writes LoginHistory objects of up to 5 MiB.
const (
	CGNATASNRangesStorageCollection = "Global"
	CGNATASNRangesStorageKey        = "cgnat_asn_ranges"
)

// cgnatASNRangesData is the stored form of the detector's ASN state.
type cgnatASNRangesData struct {
	V4 cgnatASNFamilyData `json:"v4"`
	V6 cgnatASNFamilyData `json:"v6"`
}

// cgnatASNFamilyData is one family's filtered ranges.
type cgnatASNFamilyData struct {
	// ASNs is the configured list these ranges were filtered for. It, not the
	// ASNs appearing in Ranges, is what the detector can answer for: an ASN that
	// announces nothing filters to zero rows and is still covered.
	ASNs      []int         `json:"asns"`
	UpdatedAt time.Time     `json:"updated_at"`
	Ranges    []rawASNRange `json:"ranges"`
}

// cgnatASNRangesLoad reads the persisted ranges. No stored object (first boot)
// yields the zero value, which covers no ASN.
func cgnatASNRangesLoad(ctx context.Context, nk runtime.NakamaModule) (cgnatASNRangesData, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: CGNATASNRangesStorageCollection,
		Key:        CGNATASNRangesStorageKey,
		UserID:     SystemUserID,
	}})
	if err != nil {
		return cgnatASNRangesData{}, fmt.Errorf("failed to read CGNAT ASN ranges: %w", err)
	}
	var data cgnatASNRangesData
	if len(objs) == 0 {
		return data, nil
	}
	if err := json.Unmarshal([]byte(objs[0].Value), &data); err != nil {
		return cgnatASNRangesData{}, fmt.Errorf("failed to unmarshal CGNAT ASN ranges: %w", err)
	}
	return data, nil
}

// cgnatASNRangesSave persists the ranges for the next boot.
func cgnatASNRangesSave(ctx context.Context, nk runtime.NakamaModule, data cgnatASNRangesData) error {
	value, err := json.Marshal(data)
	if err != nil {
		return fmt.Errorf("failed to marshal CGNAT ASN ranges: %w", err)
	}
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      CGNATASNRangesStorageCollection,
		Key:             CGNATASNRangesStorageKey,
		UserID:          SystemUserID,
		PermissionRead:  0,
		PermissionWrite: 0,
		Value:           string(value),
	}}); err != nil {
		return fmt.Errorf("failed to write CGNAT ASN ranges: %w", err)
	}
	return nil
}

// LoadASNRanges installs the ranges the last successful refresh persisted,
// together with the ASN lists they were filtered for. It is one small read, so
// boot does it synchronously (bootCGNATDetector) and the detector never starts
// cold. Nothing stored installs nothing; the detector then stays not-ready
// until a refresh succeeds. Meant for boot: it replaces whatever is loaded.
//
// A stored row that does not parse rejects the whole object. Installing the
// rest would claim coverage the data does not have, which is the fail-open
// this state exists to prevent.
func (d *CGNATDetector) LoadASNRanges(ctx context.Context, nk runtime.NakamaModule) error {
	stored, err := cgnatASNRangesLoad(ctx, nk)
	if err != nil {
		return err
	}
	ranges4 := convertToRanges4(stored.V4.Ranges)
	ranges6 := convertToRanges6(stored.V6.Ranges)
	if len(ranges4) != len(stored.V4.Ranges) || len(ranges6) != len(stored.V6.Ranges) {
		return fmt.Errorf("stored CGNAT ASN ranges have unparseable rows (v4 %d of %d, v6 %d of %d parsed); not installed",
			len(ranges4), len(stored.V4.Ranges), len(ranges6), len(stored.V6.Ranges))
	}

	d.mu.Lock()
	d.asnRanges4, d.asnCovered4, d.asnUpdated4 = ranges4, asnSet(stored.V4.ASNs), stored.V4.UpdatedAt
	d.asnRanges6, d.asnCovered6, d.asnUpdated6 = ranges6, asnSet(stored.V6.ASNs), stored.V6.UpdatedAt
	d.notifyStateChangedLocked()
	d.mu.Unlock()

	if d.logger != nil {
		d.logger.WithFields(map[string]any{
			"v4_ranges":     len(ranges4),
			"v4_asns":       stored.V4.ASNs,
			"v4_updated_at": stored.V4.UpdatedAt,
			"v6_ranges":     len(ranges6),
			"v6_asns":       stored.V6.ASNs,
			"v6_updated_at": stored.V6.UpdatedAt,
		}).Info("CGNAT: loaded stored ASN ranges")
	}
	return nil
}

// storedFormLocked renders the loaded ASN state for storage. Must hold d.mu.
func (d *CGNATDetector) storedFormLocked() cgnatASNRangesData {
	v4 := make([]rawASNRange, 0, len(d.asnRanges4))
	for _, r := range d.asnRanges4 {
		v4 = append(v4, rawASNRange{Start: uint32ToIPv4(r.Start).String(), End: uint32ToIPv4(r.End).String(), ASN: r.ASN})
	}
	v6 := make([]rawASNRange, 0, len(d.asnRanges6))
	for _, r := range d.asnRanges6 {
		v6 = append(v6, rawASNRange{Start: net.IP(r.Start[:]).String(), End: net.IP(r.End[:]).String(), ASN: r.ASN})
	}
	return cgnatASNRangesData{
		V4: cgnatASNFamilyData{ASNs: sortedASNs(d.asnCovered4), UpdatedAt: d.asnUpdated4, Ranges: v4},
		V6: cgnatASNFamilyData{ASNs: sortedASNs(d.asnCovered6), UpdatedAt: d.asnUpdated6, Ranges: v6},
	}
}

// bootCGNATDetector builds the process-wide detector, loads the persisted ASN
// ranges before returning, and installs it.
//
// Settings are applied only if they are already loaded. In production they are
// not: InitializeEvrRuntimeModule runs before NewEvrPipeline loads them, and
// ServiceSettingsLoad hands them over when it does. ServiceSettings() never
// returns nil -- before the first load it is a zero struct -- so applying it
// here would mark the detector configured with no CIDRs and no ASNs, which
// reads as ready. Until real settings arrive the detector is not ready and
// fails closed.
func bootCGNATDetector(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) *CGNATDetector {
	d := NewCGNATDetector(logger)
	if s := serviceSettings.Load(); s != nil {
		d.UpdateSettings(s.CGNAT)
	}
	if err := d.LoadASNRanges(ctx, nk); err != nil {
		logger.WithField("error", err).Warn("CGNAT: stored ASN ranges not loaded; addresses outside the configured CIDRs are treated as shared until a refresh succeeds")
	}
	SetCGNATDetector(d)
	return d
}

func asnSet(asns []int) map[int]bool {
	set := make(map[int]bool, len(asns))
	for _, asn := range asns {
		set[asn] = true
	}
	return set
}

func sortedASNs(set map[int]bool) []int {
	asns := slices.Sorted(maps.Keys(set))
	if asns == nil {
		return []int{}
	}
	return asns
}

func uint32ToIPv4(v uint32) net.IP {
	ip := make(net.IP, net.IPv4len)
	binary.BigEndian.PutUint32(ip, v)
	return ip
}
