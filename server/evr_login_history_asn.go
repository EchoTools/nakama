package server

import (
	"context"
	"errors"
	"maps"
	"net"
	"slices"
	"time"
)

// cachedIPInfoReader is the one IPInfoCache method the ASN backfill uses: a
// read of what the IP info providers already hold in Redis, never a request to
// a provider.
type cachedIPInfoReader interface {
	GetCached(ctx context.Context, ip string) (IPInfo, error)
}

// loginHistoryEntryMaps are the three places a LoginHistory keeps entries.
// Active and PendingAuthorizations are not pruned with History (MarshalJSON's
// 5 MiB cap), so an address can survive in them after its History entry is gone.
func (h *LoginHistory) loginHistoryEntryMaps() [3]map[string]*LoginHistoryEntry {
	return [3]map[string]*LoginHistoryEntry{h.History, h.Active, h.PendingAuthorizations}
}

// clientIPASNs returns the ASN recorded for each client IP in the history. See
// mergedClientIPASNs.
func (h *LoginHistory) clientIPASNs() map[string]int {
	return mergedClientIPASNs(h)
}

// mergedClientIPASNs returns the ASN recorded for each client IP across the
// given histories. An address whose entries disagree -- it moved between
// networks -- takes the ASN of its most recently updated entry, whichever
// history holds it; an exact tie takes the larger ASN. Neither rule depends on
// the order of the histories or of map iteration, so two accounts compared in
// either direction see the same ASN. Addresses with no recorded ASN are absent,
// so a lookup of one yields 0, which every classifier reads as unknown.
func mergedClientIPASNs(histories ...*LoginHistory) map[string]int {
	type record struct {
		asn int
		at  time.Time
	}
	newest := make(map[string]record)
	for _, h := range histories {
		if h == nil {
			continue
		}
		for _, entries := range h.loginHistoryEntryMaps() {
			for _, e := range entries {
				if e == nil || e.ASN <= 0 {
					continue
				}
				r, ok := newest[e.ClientIP]
				if ok && (r.at.After(e.UpdatedAt) || (r.at.Equal(e.UpdatedAt) && r.asn >= e.ASN)) {
					continue
				}
				newest[e.ClientIP] = record{asn: e.ASN, at: e.UpdatedAt}
			}
		}
	}
	asns := make(map[string]int, len(newest))
	for ip, r := range newest {
		asns[ip] = r.asn
	}
	return asns
}

// recordASNs records asns[ip] on every entry for ip. An ASN of 0 or less is
// never recorded, so an unknown can not overwrite a known one. Returns whether
// any entry changed.
func (h *LoginHistory) recordASNs(asns map[string]int) (changed bool) {
	for _, entries := range h.loginHistoryEntryMaps() {
		for _, e := range entries {
			if e == nil {
				continue
			}
			if asn := asns[e.ClientIP]; asn > 0 && e.ASN != asn {
				e.ASN = asn
				changed = true
			}
		}
	}
	return changed
}

// backfillLoginHistoryASNs reads a cached ASN for every public client IP in h
// that has none recorded, and records each one found on every entry for that
// IP. It returns whether any entry changed and how many IPs are still without
// an ASN. The caller persists h; this only mutates it.
//
// It is MigrationClearAlternateMatches' step, and nothing on the login path
// calls it: a login records only the ASN of its own address, from the lookup it
// already makes (recordLoginASN).
//
// Cache only, by ruling (#596): resolver answers from what the providers have
// stored in Redis, and an address with no cache entry stays unknown. Nothing
// is fetched live -- IPQS is paid, and the addresses in old histories are the
// ones least likely to be cached. An unknown address is classified by the
// configured CIDRs alone; it gains a cache entry, and so an ASN, when a login
// next comes from it.
//
// Idempotent: an IP with an ASN on any of its entries is never read again (its
// other entries are filled from that one), so a second call over the same
// history reads nothing and reports no change.
//
// err joins the cache read failures. A failure is not a miss -- the cache may
// hold the address -- so the caller must not treat the IPs it left unresolved
// as unknown.
func backfillLoginHistoryASNs(ctx context.Context, resolver cachedIPInfoReader, h *LoginHistory) (changed bool, unresolved int, err error) {
	known := h.clientIPASNs()

	pending := make(map[string]struct{})
	for _, entries := range h.loginHistoryEntryMaps() {
		for _, e := range entries {
			if e == nil {
				continue
			}
			if _, ok := known[e.ClientIP]; ok {
				continue
			}
			// Private and non-routable addresses are never linking keys
			// (matchIgnoredAltPattern), and no provider caches them.
			if ip := net.ParseIP(e.ClientIP); ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
				continue
			}
			pending[e.ClientIP] = struct{}{}
		}
	}

	var errs []error
	ips := slices.Sorted(maps.Keys(pending))
	for i, ip := range ips {
		if ctxErr := ctx.Err(); ctxErr != nil {
			// A cancelled run is not a miss either.
			unresolved += len(ips) - i
			errs = append(errs, ctxErr)
			break
		}
		info, readErr := resolver.GetCached(ctx, ip)
		if readErr != nil {
			errs = append(errs, readErr)
		}
		if info == nil || info.ASN() <= 0 {
			unresolved++
			continue
		}
		known[ip] = info.ASN()
	}

	return h.recordASNs(known), unresolved, errors.Join(errs...)
}

// recordLoginASN records the ASN of this login's client IP from the IP info
// lookup the login already made -- ipInfo, nil when every provider failed --
// on every entry for that address, and returns it for EventUserAuthenticated
// to carry to the handler that persists the history. 0 means the lookup
// yielded none, and nothing is recorded: the address stays unknown and the
// next login from it tries again.
//
// It looks nothing up. Other addresses in the history are not the login's
// business (#596 ruling); the alt-clear migration reads them from the cache.
func recordLoginASN(h *LoginHistory, clientIP string, ipInfo IPInfo) int {
	if ipInfo == nil || ipInfo.ASN() <= 0 {
		return 0
	}
	h.recordASNs(map[string]int{clientIP: ipInfo.ASN()})
	return ipInfo.ASN()
}
