package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/atomic"
)

const (
	ip2asnV4URL     = "https://iptoasn.com/data/ip2asn-v4.tsv.gz"
	ip2asnV6URL     = "https://iptoasn.com/data/ip2asn-v6.tsv.gz"
	ip2asnV4Cache   = "/var/tmp/ip2asn-v4.tsv.gz"
	ip2asnV6Cache   = "/var/tmp/ip2asn-v6.tsv.gz"
	asnCacheMaxAge  = 24 * time.Hour
	defaultMaxIPMap = 100_000
)

// cgnatDetector is the process-wide CGNAT detector, accessed atomically.
// Complements isKnownSharedIPProvider() in evr_ip_info_shared.go which
// identifies shared-IP providers by ISP/org name via external API for
// VPN/fraud scoring. This detector operates on raw IPs without external
// API calls, at the alt detection layer.
var cgnatDetector = atomic.NewPointer((*CGNATDetector)(nil))

func SetCGNATDetector(d *CGNATDetector) { cgnatDetector.Store(d) }
func GetCGNATDetector() *CGNATDetector  { return cgnatDetector.Load() }

// asnRange4 represents an IPv4 ASN range for binary search.
type asnRange4 struct {
	Start uint32
	End   uint32
	ASN   int
}

// asnRange6 represents an IPv6 ASN range for binary search.
type asnRange6 struct {
	Start [16]byte
	End   [16]byte
	ASN   int
}

// CGNATDetector identifies CGNAT and shared-IP addresses using three layers:
// CIDR range list (fastest, available immediately), ASN lookup (background loaded),
// and heuristic per-IP account tracking (optional, warns moderators only).
type CGNATDetector struct {
	mu         sync.RWMutex
	asnRanges4 []asnRange4
	asnRanges6 []asnRange6
	cidrNets   []*net.IPNet
	ipCounts   map[string]map[string]time.Time // IP → {userID → lastSeen}
	lastUpdate time.Time
	logger     runtime.Logger
	maxIPCount int

	// settings cached from CGNATSettings
	cgnatASNs                map[int]bool
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
		cgnatASNs:  make(map[int]bool),
	}
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
				d.logger.Warn("CGNAT: invalid CIDR in settings, skipping: %s: %v", cidr, err)
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

	// Layer 1: CIDR check (fastest, no external data needed)
	for _, cidr := range d.cidrNets {
		if cidr.Contains(ip) {
			return true
		}
	}

	// Layer 2: ASN lookup
	if len(d.cgnatASNs) > 0 {
		var asn int
		if ip4 := ip.To4(); ip4 != nil {
			asn = d.lookupASNv4(ip4)
		} else {
			asn = d.lookupASNv6(ip.To16())
		}
		if asn > 0 && d.cgnatASNs[asn] {
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
		ip      string
		newest  time.Time
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

// RefreshASNData downloads and parses both IPv4 and IPv6 ASN data.
func (d *CGNATDetector) RefreshASNData(ctx context.Context) error {
	ranges4, err := loadASNData(ctx, ip2asnV4URL, ip2asnV4Cache, true)
	if err != nil {
		d.logger.Warn("CGNAT: failed to load IPv4 ASN data: %v", err)
	}

	ranges6, err := loadASNData(ctx, ip2asnV6URL, ip2asnV6Cache, false)
	if err != nil {
		d.logger.Warn("CGNAT: failed to load IPv6 ASN data: %v", err)
	}

	d.mu.Lock()
	defer d.mu.Unlock()

	if ranges4 != nil {
		d.asnRanges4 = convertToRanges4(ranges4)
		d.logger.Info("CGNAT: loaded %d IPv4 ASN ranges", len(d.asnRanges4))
	}
	if ranges6 != nil {
		d.asnRanges6 = convertToRanges6(ranges6)
		d.logger.Info("CGNAT: loaded %d IPv6 ASN ranges", len(d.asnRanges6))
	}
	d.lastUpdate = time.Now()
	return nil
}

// lookupASNv4 performs a binary search on IPv4 ranges. Must hold d.mu.RLock.
func (d *CGNATDetector) lookupASNv4(ip net.IP) int {
	if len(d.asnRanges4) == 0 {
		return 0
	}
	target := ipv4ToUint32(ip)
	idx := sort.Search(len(d.asnRanges4), func(i int) bool {
		return d.asnRanges4[i].End >= target
	})
	if idx < len(d.asnRanges4) && target >= d.asnRanges4[idx].Start && target <= d.asnRanges4[idx].End {
		return d.asnRanges4[idx].ASN
	}
	return 0
}

// lookupASNv6 performs a binary search on IPv6 ranges. Must hold d.mu.RLock.
func (d *CGNATDetector) lookupASNv6(ip net.IP) int {
	if len(d.asnRanges6) == 0 {
		return 0
	}
	var target [16]byte
	copy(target[:], ip.To16())

	idx := sort.Search(len(d.asnRanges6), func(i int) bool {
		return bytes.Compare(d.asnRanges6[i].End[:], target[:]) >= 0
	})
	if idx < len(d.asnRanges6) && bytes.Compare(target[:], d.asnRanges6[idx].Start[:]) >= 0 && bytes.Compare(target[:], d.asnRanges6[idx].End[:]) <= 0 {
		return d.asnRanges6[idx].ASN
	}
	return 0
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

// --- ASN data loading ---

type rawASNRange struct {
	startStr string
	endStr   string
	asn      int
}

func loadASNData(ctx context.Context, url, cachePath string, isV4 bool) ([]rawASNRange, error) {
	// Check cache freshness
	if info, err := os.Stat(cachePath); err == nil {
		if time.Since(info.ModTime()) < asnCacheMaxAge {
			data, err := os.ReadFile(cachePath)
			if err == nil {
				return parseASNGzip(data, isV4)
			}
		}
	}

	// Download
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// Fall back to cache if available
		if data, cacheErr := os.ReadFile(cachePath); cacheErr == nil {
			return parseASNGzip(data, isV4)
		}
		return nil, fmt.Errorf("downloading ASN data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if data, cacheErr := os.ReadFile(cachePath); cacheErr == nil {
			return parseASNGzip(data, isV4)
		}
		return nil, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}

	// Cache to disk
	if writeErr := os.WriteFile(cachePath, data, 0644); writeErr != nil {
		// Non-fatal, just log
		fmt.Printf("CGNAT: failed to cache ASN data to %s: %v\n", cachePath, writeErr)
	}

	return parseASNGzip(data, isV4)
}

func parseASNGzip(data []byte, isV4 bool) ([]rawASNRange, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decompressing: %w", err)
	}
	defer gz.Close()

	var ranges []rawASNRange
	scanner := bufio.NewScanner(gz)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "\t", 5)
		if len(parts) < 3 {
			continue
		}
		asn, err := strconv.Atoi(parts[2])
		if err != nil || asn == 0 {
			continue // Skip unrouted ranges
		}
		ranges = append(ranges, rawASNRange{
			startStr: parts[0],
			endStr:   parts[1],
			asn:      asn,
		})
	}
	return ranges, scanner.Err()
}

func convertToRanges4(raw []rawASNRange) []asnRange4 {
	ranges := make([]asnRange4, 0, len(raw))
	for _, r := range raw {
		startIP := net.ParseIP(r.startStr)
		endIP := net.ParseIP(r.endStr)
		if startIP == nil || endIP == nil {
			continue
		}
		start4 := startIP.To4()
		end4 := endIP.To4()
		if start4 == nil || end4 == nil {
			continue
		}
		ranges = append(ranges, asnRange4{
			Start: ipv4ToUint32(start4),
			End:   ipv4ToUint32(end4),
			ASN:   r.asn,
		})
	}
	sort.Slice(ranges, func(i, j int) bool {
		return ranges[i].Start < ranges[j].Start
	})
	return ranges
}

func convertToRanges6(raw []rawASNRange) []asnRange6 {
	ranges := make([]asnRange6, 0, len(raw))
	for _, r := range raw {
		startIP := net.ParseIP(r.startStr)
		endIP := net.ParseIP(r.endStr)
		if startIP == nil || endIP == nil {
			continue
		}
		var start, end [16]byte
		copy(start[:], startIP.To16())
		copy(end[:], endIP.To16())
		ranges = append(ranges, asnRange6{
			Start: start,
			End:   end,
			ASN:   r.asn,
		})
	}
	sort.Slice(ranges, func(i, j int) bool {
		return bytes.Compare(ranges[i].Start[:], ranges[j].Start[:]) < 0
	})
	return ranges
}

func ipv4ToUint32(ip net.IP) uint32 {
	ip4 := ip.To4()
	if ip4 == nil {
		return 0
	}
	return binary.BigEndian.Uint32(ip4)
}
