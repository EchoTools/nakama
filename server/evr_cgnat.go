package server

import (
	"context"
	"fmt"
	"net"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/atomic"
)

const defaultMaxIPMap = 100_000

// cgnatDetector is the process-wide CGNAT detector, accessed atomically.
// Complements isKnownSharedIPProvider() in evr_ip_info_shared.go which
// identifies shared-IP providers by ISP/org name via external API for
// VPN/fraud scoring. This detector operates at the alt detection layer, on an
// address and the ASN recorded for it in a login history.
var cgnatDetector = atomic.NewPointer((*CGNATDetector)(nil))

func SetCGNATDetector(d *CGNATDetector) { cgnatDetector.Store(d) }
func GetCGNATDetector() *CGNATDetector  { return cgnatDetector.Load() }

// CGNATDetector identifies CGNAT and shared-IP addresses using three layers:
// the configured CIDR list, the configured ASN list matched against the ASN a
// login recorded for the address (LoginHistoryEntry.ASN), and heuristic per-IP
// account tracking (optional, warns moderators only).
//
// The ASN comes from the IP info lookup every login already makes, not from a
// downloaded IP->ASN table. A table has to be present before the detector can
// answer, and #596 and #598 were both about the window where it was not: first
// every carrier address read as residential, then every address read as shared.
// A recorded ASN is either there for the address or it is not, per address, and
// an address without one is classified by the CIDRs alone.
type CGNATDetector struct {
	mu         sync.RWMutex
	cidrNets   []*net.IPNet
	ipCounts   map[string]map[string]time.Time // IP → {userID → lastSeen}
	logger     runtime.Logger
	maxIPCount int

	// settingsApplied is closed by the first UpdateSettings. Until then the
	// detector does not know its CIDRs, ASNs or commodity prefixes: it is built
	// in InitializeEvrRuntimeModule, before NewEvrPipeline loads Global/settings.
	settingsApplied     chan struct{}
	settingsAppliedOnce sync.Once

	// settings cached from CGNATSettings
	cgnatASNs                map[int]bool
	commodityProfilePrefixes []string
	heuristicEnabled         bool
	heuristicThreshold       int
	heuristicWindowDays      int
}

// NewCGNATDetector creates a detector with the given logger. It classifies
// nothing as CGNAT until settings are applied.
func NewCGNATDetector(logger runtime.Logger) *CGNATDetector {
	return &CGNATDetector{
		logger:          logger,
		ipCounts:        make(map[string]map[string]time.Time),
		maxIPCount:      defaultMaxIPMap,
		cgnatASNs:       make(map[int]bool),
		settingsApplied: make(chan struct{}),
	}
}

// bootCGNATDetector builds the process-wide detector and installs it.
//
// Settings are applied only if they are already loaded. In production they are
// not: InitializeEvrRuntimeModule runs before NewEvrPipeline loads them, and
// ServiceSettingsLoad hands them over when it does. ServiceSettings() never
// returns nil -- before the first load it is a zero struct -- so applying it
// here would mark the detector configured with no CIDRs and no ASNs.
func bootCGNATDetector(logger runtime.Logger) *CGNATDetector {
	d := NewCGNATDetector(logger)
	if s := serviceSettings.Load(); s != nil {
		d.UpdateSettings(s.CGNAT)
	}
	SetCGNATDetector(d)
	return d
}

// UpdateSettings re-parses CIDR strings, ASN list, and commodity profiles from settings.
func (d *CGNATDetector) UpdateSettings(settings CGNATSettings) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// Parse CIDRs
	nets := make([]*net.IPNet, 0, len(settings.CIDRs))
	for _, cidr := range settings.CIDRs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			if d.logger != nil {
				d.logger.WithFields(map[string]interface{}{"cidr": cidr, "error": err}).Warn("CGNAT: invalid CIDR in settings, skipping")
			}
			continue
		}
		nets = append(nets, ipNet)
	}
	d.cidrNets = nets

	// Parse ASNs
	asnMap := make(map[int]bool, len(settings.ASNs))
	for _, asn := range settings.ASNs {
		asnMap[asn] = true
	}
	d.cgnatASNs = asnMap

	d.commodityProfilePrefixes = settings.CommodityProfilePrefixes
	d.heuristicEnabled = settings.HeuristicEnabled
	d.heuristicThreshold = settings.HeuristicAccountThreshold
	d.heuristicWindowDays = settings.HeuristicWindowDays

	// nil only in a zero-value detector, which nothing waits on.
	if d.settingsApplied != nil {
		d.settingsAppliedOnce.Do(func() { close(d.settingsApplied) })
	}
}

// WaitSettingsApplied blocks until settings have reached the detector or ctx
// ends, returning ctx's error in the latter case.
func (d *CGNATDetector) WaitSettingsApplied(ctx context.Context) error {
	// Checked alone first: select picks at random among ready cases, and
	// settings that have arrived must win over a context that has also ended.
	select {
	case <-d.settingsApplied:
		return nil
	default:
	}
	select {
	case <-d.settingsApplied:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("waiting for CGNAT settings: %w", ctx.Err())
	}
}

// IsCGNAT reports whether ipStr is a shared address: it is in a configured
// CIDR, or asn -- the ASN recorded for it at login -- is a configured ASN.
//
// asn is 0 when no ASN is recorded for the address (an entry from before ASNs
// were recorded and not yet backfilled, or a login whose IP info lookup failed).
// Such an address is classified by the CIDRs alone. Unknown is not a positive:
// reporting it as shared would drop it from every login history's alt-discovery
// keys whenever the lookup is unavailable, and persist that degradation (#598).
//
// Handles both IPv4 and IPv6. Returns false for unparseable input.
func (d *CGNATDetector) IsCGNAT(ipStr string, asn int) bool {
	ip := net.ParseIP(ipStr)
	if ip == nil {
		return false
	}

	d.mu.RLock()
	defer d.mu.RUnlock()

	for _, cidr := range d.cidrNets {
		if cidr.Contains(ip) {
			return true
		}
	}
	return asn > 0 && d.cgnatASNs[asn]
}

// IsWeakSignal returns true if the given alt match item is a weak signal:
// a CGNAT IP or a commodity system profile. HMD serials and XPIDs are
// always strong signals. An IP is decided by IsCGNAT with asn, the ASN recorded
// for it; asn is ignored for anything that is not an IP.
func (d *CGNATDetector) IsWeakSignal(item string, asn int) bool {
	if item == "" || item == "unknown" {
		return true
	}

	// Check if it's an IP address
	if ip := net.ParseIP(item); ip != nil {
		return d.IsCGNAT(item, asn)
	}

	// Check if it matches a commodity profile prefix
	d.mu.RLock()
	prefixes := d.commodityProfilePrefixes
	d.mu.RUnlock()

	for _, prefix := range prefixes {
		// AN EMPTY PREFIX IS A NO-OP, NOT A UNIVERSAL MATCH.
		//
		// strings.HasPrefix(anything, "") is ALWAYS true, so a single ""
		// in the operator-configured commodity_profile_prefixes list makes
		// this function return true for every non-IP string it is ever
		// asked about. Measured in production 2026-09-08: the live
		// Global/settings record carried a "" in that list, and the effect
		// was total and silent --
		//
		//   matchIgnoredAltPattern() drops any item where IsWeakSignal is
		//   true AND net.ParseIP fails. IPs parse, so IPs survived; XPIDs,
		//   HMD serials and system profiles do not parse, so ALL THREE were
		//   filtered out of both LoginHistory.Cache and AltSearchPatterns.
		//   Alt DISCOVERY is a storage query against value.cache, so the
		//   strongest signal we have could not surface a candidate at all.
		//
		//   Blast radius at the time: 7,254 of 7,254 alternate-account links
		//   in production -- every single one -- rested on a shared IP, and
		//   ZERO carried an XPID, serial or profile. The doc comment six
		//   lines above this loop says "HMD serials and XPIDs are always
		//   strong signals"; one empty string made them weak.
		//
		// The source was correct and had been since 2024-12-17; the defect
		// was one character of config. So the guard belongs HERE, where no
		// settings value can reach around it, rather than only at load.
		if prefix == "" {
			continue
		}
		if strings.HasPrefix(item, prefix) {
			return true
		}
	}

	return false
}

// TrackLogin records a login for heuristic purposes. If the heuristic is enabled
// and the threshold is exceeded, sends a warning to the audit channel.
func (d *CGNATDetector) TrackLogin(ipStr string, userID string, auditChannelID string, dg *discordgo.Session) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.heuristicEnabled || d.heuristicThreshold <= 0 {
		return
	}

	now := time.Now()
	windowCutoff := now.AddDate(0, 0, -d.heuristicWindowDays)

	// Add/update entry
	if d.ipCounts[ipStr] == nil {
		d.ipCounts[ipStr] = make(map[string]time.Time)
	}
	d.ipCounts[ipStr][userID] = now

	// Prune old entries for this IP
	for uid, lastSeen := range d.ipCounts[ipStr] {
		if lastSeen.Before(windowCutoff) {
			delete(d.ipCounts[ipStr], uid)
		}
	}

	// Check threshold
	count := len(d.ipCounts[ipStr])
	if count >= d.heuristicThreshold {
		userIDs := make([]string, 0, count)
		for uid := range d.ipCounts[ipStr] {
			userIDs = append(userIDs, uid)
		}

		if dg != nil && auditChannelID != "" {
			msg := fmt.Sprintf("CGNAT heuristic: IP `%s` has %d unique accounts in %d days: %s. Consider adding to CGNAT CIDR list.",
				ipStr, count, d.heuristicWindowDays, strings.Join(userIDs, ", "))
			AuditLogSend(dg, auditChannelID, msg)
		}
	}

	// Enforce memory cap
	if len(d.ipCounts) > d.maxIPCount {
		d.evictOldestIPs()
	}
}

// evictOldestIPs removes the oldest-accessed IPs to stay under maxIPCount.
// Must be called with d.mu held.
func (d *CGNATDetector) evictOldestIPs() {
	type ipAge struct {
		ip     string
		newest time.Time
	}

	ages := make([]ipAge, 0, len(d.ipCounts))
	for ip, users := range d.ipCounts {
		var newest time.Time
		for _, t := range users {
			if t.After(newest) {
				newest = t
			}
		}
		ages = append(ages, ipAge{ip, newest})
	}

	sort.Slice(ages, func(i, j int) bool {
		return ages[i].newest.Before(ages[j].newest)
	})

	// Remove oldest until under cap
	toRemove := len(d.ipCounts) - d.maxIPCount
	for i := 0; i < toRemove && i < len(ages); i++ {
		delete(d.ipCounts, ages[i].ip)
	}
}

// filterStrongAlts returns only alt IDs that have at least one strong-signal
// match in the login history. Iterates history.AlternateMatches[altID] and
// checks each match's Items list via detector.IsWeakSignal().
//
// An IP item's ASN is resolved from history itself. Every item in
// history.AlternateMatches was produced by loginHistoryCompare, which emits a
// client IP only when it appears in the History of BOTH accounts -- so the IP is
// one this user logged in from, and this user's own entry for it carries the
// recorded ASN. Carrying the ASN on AlternateSearchMatch instead would duplicate
// that into every stored match and change the stored shape for no information.
//
// The one way an item can outlive its entry: MarshalJSON prunes the oldest
// History entries once a record passes 5 MiB, and AlternateMatches is not pruned
// with them. clientIPASNs also reads Active and PendingAuthorizations, which the
// prune does not touch, so that leaves an unauthenticated login's IP from a
// record past 5 MiB. It resolves to no ASN and is classified by the CIDRs alone,
// exactly as any address with no recorded ASN is.
func filterStrongAlts(history *LoginHistory, altIDs []string, detector *CGNATDetector) []string {
	if detector == nil || len(altIDs) == 0 {
		return altIDs
	}

	asns := history.clientIPASNs()
	strong := make([]string, 0, len(altIDs))
	for _, altID := range altIDs {
		matches, ok := history.AlternateMatches[altID]
		if !ok {
			continue
		}

		hasStrongSignal := false
		for _, m := range matches {
			for _, item := range m.Items {
				if !detector.IsWeakSignal(item, asns[item]) {
					hasStrongSignal = true
					break
				}
			}
			if hasStrongSignal {
				break
			}
		}

		if hasStrongSignal {
			strong = append(strong, altID)
		}
	}
	return strong
}
