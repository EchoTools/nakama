package server

import (
	"cmp"
	"context"
	"maps"
	"net"
	"slices"
	"strings"
	"time"
)

// loginASNBackfillBudget bounds how long a login spends starting ASN backfill
// lookups. It is the per-provider request timeout (IPQSClient.Get,
// ipapiClient.Get), so the backfill adds about what the login's own IP info
// lookup can already cost: no lookup is started once it has elapsed, and one in
// flight finishes under its provider's own timeout. It is deliberately not a
// context deadline -- cancelling an in-flight provider request counts as a
// provider failure and opens that provider's circuit breaker for every login.
//
// Lookups are answered from Redis (180-day TTL) for any address a login has
// used in that time, so a converging history costs milliseconds; what the
// budget bounds is the first login after deploy of an account whose history
// holds many addresses older than that. What it defers is looked up on the
// account's next login.
const loginASNBackfillBudget = time.Second

// ipInfoGetter is the one IPInfoCache method the ASN backfill uses.
type ipInfoGetter interface {
	Get(ctx context.Context, ip string) (IPInfo, error)
}

// loginBackfillIPInfoGetter is the resolver a login's backfill runs through. It
// answers nothing once its deadline has passed, without touching a request
// already in flight (see loginASNBackfillBudget), and nothing for lookedUp, the
// address the login itself just looked up: if that lookup came back empty, the
// same providers are not asked again within the same login.
type loginBackfillIPInfoGetter struct {
	ipInfoGetter
	lookedUp string
	deadline time.Time
	now      func() time.Time
}

func (g loginBackfillIPInfoGetter) Get(ctx context.Context, ip string) (IPInfo, error) {
	if ip == g.lookedUp || !g.now().Before(g.deadline) {
		return nil, nil
	}
	return g.ipInfoGetter.Get(ctx, ip)
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

// backfillLoginHistoryASNs looks up an ASN for every public client IP in h that
// has none recorded, most recently used first, and records each one found on
// every entry for that IP. It returns whether any entry changed and how many IPs
// are still without an ASN. The caller persists h; this only mutates it.
//
// Idempotent: an IP with an ASN on any of its entries is never looked up again
// (its other entries are filled from that one), so a second call over the same
// history performs no lookups and reports no change.
//
// A lookup that yields nothing records nothing, and the IP is tried again on the
// next call. That covers a nil IPInfo -- what IPInfoCache.Get returns when every
// provider failed, since it fails open (SEC-6) -- and an ASN of 0. Both are
// transient, and neither may be written down as an answer: until an ASN is
// known the address is classified by the configured CIDRs alone, which is the
// state it was already in, and the next login's lookup is a Redis read once any
// provider has answered for it.
//
// resolver must be non-nil. It is *IPInfoCache in production.
func backfillLoginHistoryASNs(ctx context.Context, resolver ipInfoGetter, h *LoginHistory) (changed bool, unresolved int) {
	known := h.clientIPASNs()

	lastUsed := make(map[string]time.Time)
	for _, entries := range h.loginHistoryEntryMaps() {
		for _, e := range entries {
			if e == nil {
				continue
			}
			if _, ok := known[e.ClientIP]; ok {
				continue
			}
			// Private and non-routable addresses are never linking keys
			// (matchIgnoredAltPattern), and IPInfoCache.Get answers them
			// with an ASN-less stub without asking a provider.
			if ip := net.ParseIP(e.ClientIP); ip == nil || !ip.IsGlobalUnicast() || ip.IsPrivate() {
				continue
			}
			if t, ok := lastUsed[e.ClientIP]; !ok || e.UpdatedAt.After(t) {
				lastUsed[e.ClientIP] = e.UpdatedAt
			}
		}
	}

	pending := slices.SortedFunc(maps.Keys(lastUsed), func(a, b string) int {
		return cmp.Or(lastUsed[b].Compare(lastUsed[a]), strings.Compare(a, b))
	})
	for _, ip := range pending {
		if ctx.Err() != nil {
			unresolved++
			continue
		}
		info, _ := resolver.Get(ctx, ip) // never returns an error; nil is "unknown"
		if info == nil || info.ASN() <= 0 {
			unresolved++
			continue
		}
		known[ip] = info.ASN()
	}

	return h.recordASNs(known), unresolved
}

// recordLoginASNs records the ASN of this login's client IP from the lookup the
// login already made -- ipInfo, nil when every provider failed -- and then
// backfills any other IP in h without one, within loginASNBackfillBudget.
//
// It returns what the stored history does not know yet: every address whose
// ASN this call learned or changed. That is what EventUserAuthenticated
// carries, rather than the whole history's map; the handler fills everything
// else from the history it loads (see its Process), so once a history has
// converged the event carries nothing.
func recordLoginASNs(ctx context.Context, h *LoginHistory, clientIP string, ipInfo IPInfo, resolver ipInfoGetter) map[string]int {
	before := h.clientIPASNs()
	if ipInfo != nil {
		h.recordASNs(map[string]int{clientIP: ipInfo.ASN()})
	}
	backfillLoginHistoryASNs(ctx, loginBackfillIPInfoGetter{
		ipInfoGetter: resolver,
		lookedUp:     clientIP,
		deadline:     time.Now().Add(loginASNBackfillBudget),
		now:          time.Now,
	}, h)

	recorded := make(map[string]int)
	for ip, asn := range h.clientIPASNs() {
		if before[ip] != asn {
			recorded[ip] = asn
		}
	}
	return recorded
}
