package server

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"maps"
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

	// asnDownloadTimeout bounds one dataset download end to end, body
	// included. Measured 2026-09-10: v4 is 6.96 MB gzipped, v6 1.99 MB, so
	// two minutes still completes below 1 Mbit/s. The fetch used to go through
	// http.DefaultClient, which has no timeout at all, so a stalled
	// iptoasn.com held the refresh -- and now readiness -- open indefinitely.
	asnDownloadTimeout = 2 * time.Minute

	// defaultASNRefreshRetryInterval is how long RunASNRefresher waits before
	// retrying a failed refresh. A failure can leave the detector not-ready,
	// which fails closed and costs every IP-based alt signal, so it is retried
	// rather than left until the next restart -- but not so often that an
	// iptoasn.com outage is met with a download loop.
	defaultASNRefreshRetryInterval = 15 * time.Minute
)

// asnHTTPClient fetches the iptoasn.com datasets. See asnDownloadTimeout.
var asnHTTPClient = &http.Client{Timeout: asnDownloadTimeout}

// ErrASNDataNotReady is returned by operations that act on a POSITIVE weak or
// ignored verdict -- breaking alt links -- when the detector cannot yet answer
// for every configured ASN. In that state every public address outside the
// configured CIDRs classifies as shared, so such an operation would break every
// IP-only link it walked. See CGNATDetector.ASNDataReady.
var ErrASNDataNotReady = errors.New("CGNAT ASN data not ready: addresses outside the configured CIDRs cannot be classified")

// asnFamily selects one of the two iptoasn.com datasets.
type asnFamily int

const (
	asnFamilyV4 asnFamily = iota
	asnFamilyV6
)

func (f asnFamily) String() string {
	if f == asnFamilyV4 {
		return "v4"
	}
	return "v6"
}

func (f asnFamily) source() (url, cachePath string) {
	if f == asnFamilyV4 {
		return ip2asnV4URL, ip2asnV4Cache
	}
	return ip2asnV6URL, ip2asnV6Cache
}

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
// CIDR range list (fastest, available immediately), ASN lookup (the configured
// ASNs' ranges, loaded from storage at boot and refreshed in the background),
// and heuristic per-IP account tracking (optional, warns moderators only).
type CGNATDetector struct {
	mu         sync.RWMutex
	asnRanges4 []asnRange4
	asnRanges6 []asnRange6
	// asnCovered4 and asnCovered6 are the ASN lists the loaded ranges were
	// filtered for, per family. They -- not the ASNs that happen to appear in
	// the ranges -- define what the detector can answer for: a configured ASN
	// that announces nothing filters to zero rows and is still covered.
	asnCovered4 map[int]bool
	asnCovered6 map[int]bool
	asnUpdated4 time.Time
	asnUpdated6 time.Time
	cidrNets    []*net.IPNet
	ipCounts    map[string]map[string]time.Time // IP → {userID → lastSeen}
	logger      runtime.Logger
	maxIPCount  int

	// fetchASN returns one gzipped iptoasn.com dataset. fetchASNDataset in
	// production; tests substitute fixtures so nothing touches the network.
	fetchASN func(ctx context.Context, family asnFamily) ([]byte, error)
	// refreshRequests carries rebuild requests to RunASNRefresher. Capacity 1:
	// requests arriving while one is pending coalesce, which is safe because a
	// refresh reads the ASN list current when it runs, not when it was asked.
	refreshRequests      chan struct{}
	refreshRetryInterval time.Duration
	// stateChanged is closed and replaced whenever readiness may have changed.
	// WaitASNDataReady blocks on it.
	stateChanged chan struct{}

	// settingsApplied is false until UpdateSettings first runs. Before that the
	// detector does not know which ASNs it must answer for, so it is not ready.
	settingsApplied bool

	// settings cached from CGNATSettings
	cgnatASNs                map[int]bool
	commodityProfilePrefixes []string
	heuristicEnabled         bool
	heuristicThreshold       int
	heuristicWindowDays      int
}

// NewCGNATDetector creates a detector with the given logger. It is not ready
// (see ASNDataReady) until settings are applied and ASN ranges are loaded.
func NewCGNATDetector(logger runtime.Logger) *CGNATDetector {
	return &CGNATDetector{
		logger:               logger,
		ipCounts:             make(map[string]map[string]time.Time),
		maxIPCount:           defaultMaxIPMap,
		cgnatASNs:            make(map[int]bool),
		fetchASN:             fetchASNDataset,
		refreshRequests:      make(chan struct{}, 1),
		refreshRetryInterval: defaultASNRefreshRetryInterval,
		stateChanged:         make(chan struct{}),
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
				d.logger.WithFields(map[string]interface{}{"cidr": cidr, "error": err}).Warn("CGNAT: invalid CIDR in settings, skipping")
			}
			continue
		}
		nets = append(nets, ipNet)
	}
	d.cidrNets = nets

	// Parse ASNs. A changed list needs a rebuilt range set: the stored ranges
	// hold only the ASNs they were filtered for. The first application at boot
	// always counts as a change (from the empty list), which is what triggers
	// each process's background refresh.
	asnMap := make(map[int]bool, len(settings.ASNs))
	for _, asn := range settings.ASNs {
		asnMap[asn] = true
	}
	if !maps.Equal(asnMap, d.cgnatASNs) {
		d.requestASNRefresh()
	}
	d.cgnatASNs = asnMap
	d.settingsApplied = true
	d.notifyStateChangedLocked()

	d.commodityProfilePrefixes = settings.CommodityProfilePrefixes
	d.heuristicEnabled = settings.HeuristicEnabled
	d.heuristicThreshold = settings.HeuristicAccountThreshold
	d.heuristicWindowDays = settings.HeuristicWindowDays
}

// IsCGNAT reports whether ipStr must be treated as a shared address: it is in
// a configured CIDR, or in a configured ASN, or the detector cannot rule it out.
//
// The last case is the fail-closed one (#596). When the loaded IP->ASN ranges
// for the address's family were not filtered for every configured ASN --
// nothing loaded yet, a failed download, an ASN added since the last refresh,
// or no settings applied -- an address outside the CIDRs is reported as shared.
// Missing data is not evidence of a negative, and the costs are not symmetric:
// a missed alt link is found again on a later login, while a false one is
// persisted on both accounts and nothing afterwards tells it from a real one.
// ASNDataReady reports whether this case can currently occur.
//
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

	// Layer 2: ASN lookup, only on data that can answer for every configured ASN.
	ip4 := ip.To4()
	if !d.asnCoveredLocked(ip4 != nil) {
		return true
	}
	var asn int
	if ip4 != nil {
		asn = d.lookupASNv4(ip4)
	} else {
		asn = d.lookupASNv6(ip.To16())
	}
	return asn > 0 && d.cgnatASNs[asn]
}

// asnCoveredLocked reports whether the loaded ranges for one family were
// filtered for every configured ASN. Must hold d.mu.
func (d *CGNATDetector) asnCoveredLocked(v4 bool) bool {
	if !d.settingsApplied {
		return false
	}
	covered := d.asnCovered6
	if v4 {
		covered = d.asnCovered4
	}
	for asn := range d.cgnatASNs {
		if !covered[asn] {
			return false
		}
	}
	return true
}

// ASNDataReady reports whether IsCGNAT can answer definitively for every
// address: settings have been applied and, for both IPv4 and IPv6, the loaded
// ranges were filtered for every configured ASN. While it is false IsCGNAT fails
// closed, reporting any address outside the configured CIDRs as shared. With no
// ASNs configured there is nothing to load, and it is true once settings apply.
//
// Cheap (a read lock and a pass over the configured ASNs) and safe for
// concurrent use. It can go false again: adding an ASN in settings makes the
// detector not-ready until the rebuild for the new list lands.
//
// A caller about to make a persistent decision from IsCGNAT, IsWeakSignal or
// matchIgnoredAltPattern -- a migration that rebuilds or breaks alt links --
// must require it, or block on WaitASNDataReady with a deadline.
func (d *CGNATDetector) ASNDataReady() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.asnCoveredLocked(true) && d.asnCoveredLocked(false)
}

// WaitASNDataReady blocks until ASNDataReady is true or ctx ends, returning
// ctx's error in the latter case. It does not poll; it wakes on each settings
// application and each install of ranges.
func (d *CGNATDetector) WaitASNDataReady(ctx context.Context) error {
	for {
		d.mu.RLock()
		ready := d.asnCoveredLocked(true) && d.asnCoveredLocked(false)
		changed := d.stateChanged
		d.mu.RUnlock()
		if ready {
			return nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
			return fmt.Errorf("waiting for CGNAT ASN data: %w", ctx.Err())
		}
	}
}

// notifyStateChangedLocked wakes every WaitASNDataReady. Must hold d.mu for writing.
func (d *CGNATDetector) notifyStateChangedLocked() {
	if d.stateChanged != nil {
		close(d.stateChanged)
	}
	d.stateChanged = make(chan struct{})
}

// requestASNRefresh asks RunASNRefresher for a rebuild without blocking.
func (d *CGNATDetector) requestASNRefresh() {
	select {
	case d.refreshRequests <- struct{}{}:
	default: // one is already pending, and it reads the list current when it runs
	}
}

// IsWeakSignal returns true if the given alt match item is a weak signal:
// a CGNAT IP or a commodity system profile. HMD serials and XPIDs are
// always strong signals. An IP is decided by IsCGNAT, so an address the
// detector cannot yet classify is weak, never strong.
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

// RefreshASNData fetches both iptoasn.com datasets, keeps only the ranges of
// the configured ASNs, installs them, and -- when nk is non-nil -- persists them
// for the next boot (see LoadASNRanges). The detector never holds the ~711k
// rows of the full datasets, only the few hundred it can be asked about.
//
// Each family succeeds or fails on its own. One that fails keeps what it held,
// including the ASN list that data was filtered for, so readiness stays
// truthful. ANY failure is returned, joined per family: a partial refresh is
// not a success. (It used to report an error only when both families failed,
// so a v4-only failure -- the one that matters for nearly every player --
// looked like success.) Nothing is fetched while no ASNs are configured.
func (d *CGNATDetector) RefreshASNData(ctx context.Context, nk runtime.NakamaModule) error {
	d.mu.RLock()
	asns := maps.Clone(d.cgnatASNs)
	fetch := d.fetchASN
	d.mu.RUnlock()
	if len(asns) == 0 {
		return nil
	}

	var errs []error
	fetched := make(map[asnFamily][]rawASNRange, 2)
	for _, family := range []asnFamily{asnFamilyV4, asnFamilyV6} {
		ranges, err := fetchFilteredASNRanges(ctx, fetch, family, asns)
		if err == nil {
			err = validateASNRows(family, ranges)
		}
		if err != nil {
			if d.logger != nil {
				d.logger.WithFields(map[string]any{"family": family.String(), "error": err}).Warn("CGNAT: failed to load ASN data")
			}
			errs = append(errs, fmt.Errorf("%s: %w", family, err))
			continue
		}
		fetched[family] = ranges
	}

	if len(fetched) > 0 {
		stored := d.installFetched(fetched, asns)
		if nk != nil {
			if err := cgnatASNRangesSave(ctx, nk, stored); err != nil {
				errs = append(errs, err)
			}
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("CGNAT ASN refresh: %w", errors.Join(errs...))
	}
	return nil
}

// installFetched swaps in freshly filtered families and returns the detector's
// whole ASN state in stored form. Families absent from fetched keep their
// previous ranges and coverage.
func (d *CGNATDetector) installFetched(fetched map[asnFamily][]rawASNRange, asns map[int]bool) cgnatASNRangesData {
	now := time.Now().UTC()

	d.mu.Lock()
	defer d.mu.Unlock()
	if raw, ok := fetched[asnFamilyV4]; ok {
		d.asnRanges4, d.asnCovered4, d.asnUpdated4 = convertToRanges4(raw), asns, now
		if d.logger != nil {
			d.logger.WithField("count", len(d.asnRanges4)).Info("CGNAT: loaded IPv4 ASN ranges")
		}
	}
	if raw, ok := fetched[asnFamilyV6]; ok {
		d.asnRanges6, d.asnCovered6, d.asnUpdated6 = convertToRanges6(raw), asns, now
		if d.logger != nil {
			d.logger.WithField("count", len(d.asnRanges6)).Info("CGNAT: loaded IPv6 ASN ranges")
		}
	}
	d.notifyStateChangedLocked()
	return d.storedFormLocked()
}

// RunASNRefresher rebuilds the filtered ranges each time one is requested --
// on the first settings application at boot and on every change to the ASN
// list, see UpdateSettings -- and persists them through nk. A failed refresh is
// retried every refreshRetryInterval until one succeeds. Returns when ctx ends.
func (d *CGNATDetector) RunASNRefresher(ctx context.Context, nk runtime.NakamaModule) {
	var retry <-chan time.Time
	for {
		select {
		case <-ctx.Done():
			return
		case <-d.refreshRequests:
		case <-retry:
		}
		retry = nil
		if err := d.RefreshASNData(ctx, nk); err != nil {
			if d.logger != nil {
				d.logger.WithFields(map[string]any{
					"error":          err,
					"retry_in":       d.refreshRetryInterval.String(),
					"asn_data_ready": d.ASNDataReady(),
				}).Warn("CGNAT: ASN data refresh failed")
			}
			retry = time.After(d.refreshRetryInterval)
		}
	}
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

// rawASNRange is one iptoasn.com row, and also the stored form of a range.
type rawASNRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
	ASN   int    `json:"asn"`
}

// fetchFilteredASNRanges fetches one family and keeps only the rows for asns.
func fetchFilteredASNRanges(ctx context.Context, fetch func(context.Context, asnFamily) ([]byte, error), family asnFamily, asns map[int]bool) ([]rawASNRange, error) {
	data, err := fetch(ctx, family)
	if err != nil {
		return nil, err
	}
	return filterASNGzip(data, asns)
}

// fetchASNDataset returns one gzipped iptoasn.com dataset: from the /var/tmp
// cache when it is younger than asnCacheMaxAge, else downloaded and cached,
// else a stale cache as a last resort. The cache only avoids re-downloading
// within one container's life -- /var/tmp is empty after every recreate. What
// survives a deploy is the filtered storage object; see LoadASNRanges.
func fetchASNDataset(ctx context.Context, family asnFamily) ([]byte, error) {
	url, cachePath := family.source()

	if info, err := os.Stat(cachePath); err == nil && time.Since(info.ModTime()) < asnCacheMaxAge {
		if data, err := os.ReadFile(cachePath); err == nil {
			return data, nil
		}
	}

	data, err := downloadASNDataset(ctx, url)
	if err != nil {
		if cached, cacheErr := os.ReadFile(cachePath); cacheErr == nil {
			return cached, nil
		}
		return nil, err
	}

	// Cache to disk (non-fatal if it fails -- data is already in memory)
	_ = os.WriteFile(cachePath, data, 0644)
	return data, nil
}

func downloadASNDataset(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("creating request: %w", err)
	}
	resp, err := asnHTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("downloading ASN data: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("downloading ASN data: unexpected status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response: %w", err)
	}
	return data, nil
}

// filterASNGzip decompresses an iptoasn.com TSV and returns only the rows
// whose ASN is in asns, streaming, so the rest are never held.
//
// A dataset with no routed rows at all is an error, not an empty answer.
// Recorded as "these ASNs own nothing", it would mark coverage complete over
// an empty table, report every carrier address as not-CGNAT, and be persisted.
func filterASNGzip(data []byte, asns map[int]bool) ([]rawASNRange, error) {
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("decompressing: %w", err)
	}
	defer gz.Close()

	var (
		ranges []rawASNRange
		routed int
	)
	scanner := bufio.NewScanner(gz)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "\t", 4)
		if len(parts) < 3 {
			continue
		}
		asn, err := strconv.Atoi(parts[2])
		if err != nil || asn == 0 {
			continue // Skip unrouted ranges
		}
		routed++
		if asns[asn] {
			ranges = append(ranges, rawASNRange{Start: parts[0], End: parts[1], ASN: asn})
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("reading dataset: %w", err)
	}
	if routed == 0 {
		return nil, errors.New("dataset has no routed rows")
	}
	return ranges, nil
}

// validateASNRows refuses a family's rows unless every one is a well-formed
// range of that family: both endpoints parse, both belong to the family, and
// start <= end. convertToRanges4/6 skip or mis-file a malformed row, so without
// this the family would be marked covered with that range missing -- every
// address in it answered not-CGNAT, and the table persisted.
func validateASNRows(family asnFamily, rows []rawASNRange) error {
	wantV4 := family == asnFamilyV4
	for _, r := range rows {
		start, end := net.ParseIP(r.Start), net.ParseIP(r.End)
		switch {
		case start == nil || end == nil:
			return fmt.Errorf("AS%d row %q-%q: unparseable endpoint", r.ASN, r.Start, r.End)
		case (start.To4() != nil) != wantV4 || (end.To4() != nil) != wantV4:
			return fmt.Errorf("AS%d row %s-%s is not an IP%s range", r.ASN, r.Start, r.End, family)
		case bytes.Compare(start.To16(), end.To16()) > 0:
			return fmt.Errorf("AS%d row %s-%s is reversed", r.ASN, r.Start, r.End)
		}
	}
	return nil
}

func convertToRanges4(raw []rawASNRange) []asnRange4 {
	ranges := make([]asnRange4, 0, len(raw))
	for _, r := range raw {
		startIP := net.ParseIP(r.Start)
		endIP := net.ParseIP(r.End)
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
			ASN:   r.ASN,
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
		startIP := net.ParseIP(r.Start)
		endIP := net.ParseIP(r.End)
		if startIP == nil || endIP == nil {
			continue
		}
		var start, end [16]byte
		copy(start[:], startIP.To16())
		copy(end[:], endIP.To16())
		ranges = append(ranges, asnRange6{
			Start: start,
			End:   end,
			ASN:   r.ASN,
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
