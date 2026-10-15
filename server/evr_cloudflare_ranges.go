package server

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

const (
	cloudflareIPv4URL = "https://www.cloudflare.com/ips-v4"
	cloudflareIPv6URL = "https://www.cloudflare.com/ips-v6"

	// cloudflareRangesRefreshInterval is how often the published lists are downloaded.
	cloudflareRangesRefreshInterval = 6 * time.Hour

	// CloudflareRangesStorageKey is the key of the system-owned storage object (in
	// CacheStorageCollection) holding the last good list.
	CloudflareRangesStorageKey = "CloudflareIPRanges"
)

// cloudflareRangeStore persists the last good list as a single object.
type cloudflareRangeStore interface {
	Read(ctx context.Context) ([]byte, error)
	Write(ctx context.Context, data []byte) error
}

// CloudflareRanges holds the published Cloudflare proxy address ranges.
// Skeleton: behaviour is added in the following commit.
type CloudflareRanges struct{}

func NewCloudflareRanges(logger *zap.Logger, store cloudflareRangeStore, client *http.Client, v4URL, v6URL string) *CloudflareRanges {
	return &CloudflareRanges{}
}

// NewNakamaCloudflareRangeStore stores the list under SystemUserID.
func NewNakamaCloudflareRangeStore(nk runtime.NakamaModule) cloudflareRangeStore { return nil }

func (c *CloudflareRanges) Load(ctx context.Context) error    { return nil }
func (c *CloudflareRanges) Refresh(ctx context.Context) error { return nil }
func (c *CloudflareRanges) Start(ctx context.Context)         {}
func (c *CloudflareRanges) Stop()                             {}

// Contains reports whether addr is inside a known Cloudflare range.
func (c *CloudflareRanges) Contains(addr netip.Addr) bool { return false }

// Counts returns the number of IPv4 and IPv6 ranges currently held.
func (c *CloudflareRanges) Counts() (v4, v6 int) { return 0, 0 }
