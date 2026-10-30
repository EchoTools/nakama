package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

const (
	cloudflareIPv4URL = "https://www.cloudflare.com/ips-v4"
	cloudflareIPv6URL = "https://www.cloudflare.com/ips-v6"

	// cloudflareRangesRefreshInterval is how often the published lists are downloaded.
	// It is also the interval of the "no list available" warning.
	cloudflareRangesRefreshInterval = 6 * time.Hour

	// cloudflareRangesDownloadTimeout bounds one HTTP request.
	cloudflareRangesDownloadTimeout = 30 * time.Second

	// cloudflareRangesMaxBody bounds a downloaded list. The real lists are under 1 KiB.
	cloudflareRangesMaxBody = 64 << 10

	// CloudflareRangesStorageKey is the key of the system-owned storage object, in
	// CacheStorageCollection, holding the last good list.
	CloudflareRangesStorageKey = "CloudflareIPRanges"
)

// cloudflareRangeStore persists the last good list as a single object.
type cloudflareRangeStore interface {
	// Read returns the stored bytes, or nil if there is no object yet.
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, data []byte) error
}

// nakamaCloudflareRangeStore keeps the list in a storage object owned by the
// system user, readable and writable by no client.
type nakamaCloudflareRangeStore struct {
	nk runtime.NakamaModule
}

// NewNakamaCloudflareRangeStore stores the list under SystemUserID.
func NewNakamaCloudflareRangeStore(nk runtime.NakamaModule) cloudflareRangeStore {
	return &nakamaCloudflareRangeStore{nk: nk}
}

func (s *nakamaCloudflareRangeStore) Read(ctx context.Context) ([]byte, error) {
	objs, err := s.nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: CacheStorageCollection,
		Key:        CloudflareRangesStorageKey,
		UserID:     SystemUserID,
	}})
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, nil
	}
	return []byte(objs[0].Value), nil
}

func (s *nakamaCloudflareRangeStore) Write(ctx context.Context, data []byte) error {
	_, err := s.nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      CacheStorageCollection,
		Key:             CloudflareRangesStorageKey,
		UserID:          SystemUserID,
		Value:           string(data),
		PermissionRead:  0,
		PermissionWrite: 0,
	}})
	return err
}

// cloudflareRangesDocument is the stored form.
type cloudflareRangesDocument struct {
	IPv4      []string  `json:"ipv4"`
	IPv6      []string  `json:"ipv6"`
	UpdatedAt time.Time `json:"updated_at"`
}

// cloudflareSnapshot is an immutable list; it is replaced whole, never edited.
type cloudflareSnapshot struct {
	prefixes []netip.Prefix
	v4, v6   int
}

// CloudflareRanges holds the published Cloudflare proxy address ranges and
// answers whether a TCP peer is one of them.
//
// Lookups read an atomically published snapshot and never block on a download
// or on storage. A refresh builds a complete new snapshot and swaps it in only
// after every line validated; any failure leaves the previous snapshot, in
// memory and in storage, untouched.
type CloudflareRanges struct {
	logger *zap.Logger
	store  cloudflareRangeStore
	client *http.Client
	v4URL  string
	v6URL  string

	snapshot atomic.Pointer[cloudflareSnapshot]
	// lastNoListWarn is the unix-nano time of the last "no list" warning.
	lastNoListWarn atomic.Int64

	refreshMu sync.Mutex // serialises Refresh and Load
	cancel    context.CancelFunc
	done      chan struct{}
}

// NewCloudflareRanges builds an empty component. Call Load or Start to populate it.
func NewCloudflareRanges(logger *zap.Logger, store cloudflareRangeStore, client *http.Client, v4URL, v6URL string) *CloudflareRanges {
	if client == nil {
		client = &http.Client{Timeout: cloudflareRangesDownloadTimeout}
	}
	return &CloudflareRanges{
		logger: logger.With(zap.String("component", "cloudflare_ranges")),
		store:  store,
		client: client,
		v4URL:  v4URL,
		v6URL:  v6URL,
	}
}

// NewCloudflareRangesFromNakama is the production constructor: Cloudflare's
// published lists, persisted in a system-owned storage object.
func NewCloudflareRangesFromNakama(logger *zap.Logger, nk runtime.NakamaModule) *CloudflareRanges {
	return NewCloudflareRanges(logger, NewNakamaCloudflareRangeStore(nk), nil, cloudflareIPv4URL, cloudflareIPv6URL)
}

// parseCloudflareList validates one downloaded list. Every non-blank line must
// be a CIDR of the wanted family; an empty result is an error.
func parseCloudflareList(lines []string, want4 bool) ([]netip.Prefix, error) {
	prefixes := make([]netip.Prefix, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, _, err := net.ParseCIDR(line); err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", line, err)
		}
		prefix, err := netip.ParsePrefix(line)
		if err != nil {
			return nil, fmt.Errorf("invalid CIDR %q: %w", line, err)
		}
		prefix = prefix.Masked()
		if prefix.Addr().Is4() != want4 {
			return nil, fmt.Errorf("CIDR %q is in the wrong address family", line)
		}
		prefixes = append(prefixes, prefix)
	}
	if len(prefixes) == 0 {
		return nil, errors.New("list is empty")
	}
	return prefixes, nil
}

func buildCloudflareSnapshot(v4Lines, v6Lines []string) (*cloudflareSnapshot, error) {
	v4, err := parseCloudflareList(v4Lines, true)
	if err != nil {
		return nil, fmt.Errorf("ipv4: %w", err)
	}
	v6, err := parseCloudflareList(v6Lines, false)
	if err != nil {
		return nil, fmt.Errorf("ipv6: %w", err)
	}
	return &cloudflareSnapshot{prefixes: append(v4, v6...), v4: len(v4), v6: len(v6)}, nil
}

func (c *CloudflareRanges) download(ctx context.Context, url string) ([]string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, cloudflareRangesMaxBody+1))
	if err != nil {
		return nil, err
	}
	if len(body) > cloudflareRangesMaxBody {
		return nil, fmt.Errorf("GET %s: body exceeds %d bytes", url, cloudflareRangesMaxBody)
	}
	return strings.Split(string(body), "\n"), nil
}

// Load reads the stored list into memory. It makes no network request. A
// missing object is not an error; a corrupt one is reported and ignored.
func (c *CloudflareRanges) Load(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	data, err := c.store.Read(ctx)
	if err != nil {
		return fmt.Errorf("read stored Cloudflare IP ranges: %w", err)
	}
	if len(data) == 0 {
		return nil
	}
	var doc cloudflareRangesDocument
	if err := json.Unmarshal(data, &doc); err != nil {
		return fmt.Errorf("stored Cloudflare IP ranges are not valid JSON: %w", err)
	}
	snap, err := buildCloudflareSnapshot(doc.IPv4, doc.IPv6)
	if err != nil {
		return fmt.Errorf("stored Cloudflare IP ranges are invalid: %w", err)
	}
	c.snapshot.Store(snap)
	c.logger.Info("Cloudflare IP ranges loaded from storage",
		zap.Int("ipv4_ranges", snap.v4), zap.Int("ipv6_ranges", snap.v6), zap.Time("updated_at", doc.UpdatedAt))
	return nil
}

// Refresh downloads both lists and, only if both validate, replaces the
// in-memory and stored list. On any failure the last good list is kept.
func (c *CloudflareRanges) Refresh(ctx context.Context) error {
	c.refreshMu.Lock()
	defer c.refreshMu.Unlock()

	snap, v4Lines, v6Lines, err := c.fetch(ctx)
	if err != nil {
		c.logger.Warn("Cloudflare IP ranges refresh failed; keeping the last list",
			zap.Error(err), zap.Bool("have_list", c.snapshot.Load() != nil))
		return err
	}

	c.snapshot.Store(snap)
	c.lastNoListWarn.Store(0)
	c.logger.Info("Cloudflare IP ranges refreshed",
		zap.Int("ipv4_ranges", snap.v4), zap.Int("ipv6_ranges", snap.v6))

	doc, err := json.Marshal(cloudflareRangesDocument{IPv4: trimLines(v4Lines), IPv6: trimLines(v6Lines), UpdatedAt: time.Now().UTC()})
	if err == nil {
		err = c.store.Write(ctx, doc)
	}
	if err != nil {
		// The in-memory list is good; only the restart copy is stale.
		c.logger.Warn("Cloudflare IP ranges could not be written to storage", zap.Error(err))
	}
	return nil
}

func (c *CloudflareRanges) fetch(ctx context.Context) (*cloudflareSnapshot, []string, []string, error) {
	v4Lines, err := c.download(ctx, c.v4URL)
	if err != nil {
		return nil, nil, nil, err
	}
	v6Lines, err := c.download(ctx, c.v6URL)
	if err != nil {
		return nil, nil, nil, err
	}
	snap, err := buildCloudflareSnapshot(v4Lines, v6Lines)
	if err != nil {
		return nil, nil, nil, err
	}
	return snap, v4Lines, v6Lines, nil
}

func trimLines(lines []string) []string {
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		if l = strings.TrimSpace(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// Start loads the stored list synchronously, so it is in force before the
// caller goes on to serve, then refreshes in the background until ctx is done
// or Stop is called. Start does not wait for any download.
func (c *CloudflareRanges) Start(ctx context.Context) {
	if err := c.Load(ctx); err != nil {
		c.logger.Warn("Cloudflare IP ranges could not be loaded from storage", zap.Error(err))
	}

	runCtx, cancel := context.WithCancel(ctx)
	c.cancel = cancel
	c.done = make(chan struct{})

	go func() {
		defer close(c.done)
		ticker := time.NewTicker(cloudflareRangesRefreshInterval)
		defer ticker.Stop()
		for {
			_ = c.Refresh(runCtx) // failure is logged inside Refresh
			select {
			case <-runCtx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// Stop ends the background refresh and waits for it.
func (c *CloudflareRanges) Stop() {
	if c == nil || c.cancel == nil {
		return
	}
	c.cancel()
	<-c.done
}

// Contains reports whether addr is inside a known Cloudflare range. It never
// blocks. With no list it reports false (nothing is trusted) and warns, at most
// once per refresh interval.
func (c *CloudflareRanges) Contains(addr netip.Addr) bool {
	if c == nil {
		return false
	}
	snap := c.snapshot.Load()
	if snap == nil {
		c.warnNoList()
		return false
	}
	addr = addr.Unmap().WithZone("")
	for _, p := range snap.prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

func (c *CloudflareRanges) warnNoList() {
	now := time.Now().UnixNano()
	last := c.lastNoListWarn.Load()
	if last != 0 && now-last < int64(cloudflareRangesRefreshInterval) {
		return
	}
	if c.lastNoListWarn.CompareAndSwap(last, now) {
		c.logger.Warn("No Cloudflare IP range list is available; CF-Connecting-IP is not trusted from any peer until a download succeeds")
	}
}

// Counts returns the number of IPv4 and IPv6 ranges currently held.
func (c *CloudflareRanges) Counts() (v4, v6 int) {
	if c == nil {
		return 0, 0
	}
	if snap := c.snapshot.Load(); snap != nil {
		return snap.v4, snap.v6
	}
	return 0, 0
}
