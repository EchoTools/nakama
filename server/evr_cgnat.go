package server

import (
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
// VPN/fraud scoring. This detector operates on raw IPs without external
// API calls, at the alt detection layer.
var cgnatDetector = atomic.NewPointer((*CGNATDetector)(nil))

func SetCGNATDetector(d *CGNATDetector) { cgnatDetector.Store(d) }
func GetCGNATDetector() *CGNATDetector  { return cgnatDetector.Load() }

// CGNATDetector identifies CGNAT and shared-IP addresses by the configured
// CIDR range list, with optional heuristic per-IP account tracking that warns
// moderators only.
type CGNATDetector struct {
	mu         sync.RWMutex
	cidrNets   []*net.IPNet
	ipCounts   map[string]map[string]time.Time // IP → {userID → lastSeen}
	logger     runtime.Logger
	maxIPCount int

	// settings cached from CGNATSettings
	commodityProfilePrefixes []string
	heuristicEnabled         bool
	heuristicThreshold       int
	heuristicWindowDays      int
}

// NewCGNATDetector creates a detector with the given logger.
func NewCGNATDetector(logger runtime.Logger) *CGNATDetector {
	return &CGNATDetector{
		logger:     logger,
		ipCounts:   make(map[string]map[string]time.Time),
		maxIPCount: defaultMaxIPMap,
	}
}

// UpdateSettings re-parses CIDR strings and commodity profiles from settings.
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

	d.commodityProfilePrefixes = settings.CommodityProfilePrefixes
	d.heuristicEnabled = settings.HeuristicEnabled
	d.heuristicThreshold = settings.HeuristicAccountThreshold
	d.heuristicWindowDays = settings.HeuristicWindowDays
}

// IsCGNAT returns true if the IP belongs to a known CGNAT system.
// Handles both IPv4 and IPv6. Returns false for unparseable input.
func (d *CGNATDetector) IsCGNAT(ipStr string) bool {
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

	return false
}

// IsWeakSignal returns true if the given alt match item is a weak signal:
// a CGNAT IP or a commodity system profile. HMD serials and XPIDs are
// always strong signals.
func (d *CGNATDetector) IsWeakSignal(item string) bool {
	if item == "" || item == "unknown" {
		return true
	}

	// Check if it's an IP address
	if ip := net.ParseIP(item); ip != nil {
		return d.IsCGNAT(item)
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
func filterStrongAlts(history *LoginHistory, altIDs []string, detector *CGNATDetector) []string {
	if detector == nil || len(altIDs) == 0 {
		return altIDs
	}

	strong := make([]string, 0, len(altIDs))
	for _, altID := range altIDs {
		matches, ok := history.AlternateMatches[altID]
		if !ok {
			continue
		}

		hasStrongSignal := false
		for _, m := range matches {
			for _, item := range m.Items {
				if !detector.IsWeakSignal(item) {
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
