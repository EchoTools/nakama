package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

// Documentation-range peers are used throughout: 203.0.113.0/24 and 2001:db8::/32
// stand in for "Cloudflare", and 198.51.100.0/24 / 2001:db9::/32 for "anything else".
const (
	testCFv4Body = "203.0.113.0/24\n192.0.2.0/25\n"
	testCFv6Body = "2001:db8::/32\n"
)

type memCFStore struct {
	mu   sync.Mutex
	data []byte
	puts int
}

func (m *memCFStore) Read(ctx context.Context) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]byte(nil), m.data...), nil
}

func (m *memCFStore) Write(ctx context.Context, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data = append([]byte(nil), data...)
	m.puts++
	return nil
}

// cfServer serves the two lists; the bodies and status are swappable per test.
type cfServer struct {
	*httptest.Server
	mu      sync.Mutex
	v4, v6  string
	status  int
	hits    atomic.Int64
	blockCh chan struct{} // when non-nil, handlers wait for it to close
	enterCh chan struct{} // receives one value per request that started
}

func newCFServer(t *testing.T) *cfServer {
	t.Helper()
	s := &cfServer{v4: testCFv4Body, v6: testCFv6Body, status: http.StatusOK}
	mux := http.NewServeMux()
	handler := func(get func() string) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			s.hits.Add(1)
			s.mu.Lock()
			block, enter, status, body := s.blockCh, s.enterCh, s.status, get()
			s.mu.Unlock()
			if enter != nil {
				enter <- struct{}{}
			}
			if block != nil {
				select {
				case <-block:
				case <-r.Context().Done():
					return
				}
			}
			w.WriteHeader(status)
			_, _ = w.Write([]byte(body))
		}
	}
	mux.HandleFunc("/ips-v4", handler(func() string { return s.v4 }))
	mux.HandleFunc("/ips-v6", handler(func() string { return s.v6 }))
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	return s
}

func (s *cfServer) set(v4, v6 string, status int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.v4, s.v6, s.status = v4, v6, status
}

func (s *cfServer) ranges(t *testing.T, logger *zap.Logger, store cloudflareRangeStore) *CloudflareRanges {
	t.Helper()
	return NewCloudflareRanges(logger, store, s.Client(), s.URL+"/ips-v4", s.URL+"/ips-v6")
}

func containsStr(t *testing.T, c *CloudflareRanges, addr string) bool {
	t.Helper()
	return c.Contains(netip.MustParseAddr(addr))
}

func cfRequest(peer string, header string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/ws?format=evr", nil)
	r.RemoteAddr = peer
	if header != "" {
		r.Header.Set("CF-Connecting-IP", header)
	}
	return r
}

func refreshedRanges(t *testing.T) (*CloudflareRanges, *cfServer, *memCFStore) {
	t.Helper()
	srv := newCFServer(t)
	store := &memCFStore{}
	c := srv.ranges(t, zap.NewNop(), store)
	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	return c, srv, store
}

// 1. A peer outside the Cloudflare ranges cannot name its own client address.
func TestResolveSessionClientIP_PeerNotCloudflareKeepsRealAddress(t *testing.T) {
	c, _, _ := refreshedRanges(t)
	for _, peer := range []string{"198.51.100.7:51234", "[2001:db9::5]:51234"} {
		req := cfRequest(peer, "1.2.3.4")
		peerIP := strings.TrimSuffix(strings.TrimPrefix(peer[:strings.LastIndex(peer, ":")], "["), "]")
		got := resolveSessionClientIP(zap.NewNop(), req, peerIP, c)
		if got != peerIP {
			t.Errorf("peer %s: client_ip=%q, want the peer address %q", peer, got, peerIP)
		}
	}
}

// 2. A peer inside a Cloudflare range (v4, v6, v4-mapped v6) is a proxy; its header is used.
func TestResolveSessionClientIP_CloudflarePeerUsesHeader(t *testing.T) {
	c, _, _ := refreshedRanges(t)
	for _, tc := range []struct{ name, peer, peerIP, header, want string }{
		{"v4", "203.0.113.9:443", "203.0.113.9", "1.2.3.4", "1.2.3.4"},
		{"v6", "[2001:db8::9]:443", "2001:db8::9", "1.2.3.4", "1.2.3.4"},
		{"v6 client", "[2001:db8::9]:443", "2001:db8::9", "2001:4860::1", "2001:4860::1"},
		{"v4-mapped peer", "[::ffff:203.0.113.9]:443", "::ffff:203.0.113.9", "1.2.3.4", "1.2.3.4"},
		{"malformed header from proxy keeps peer", "203.0.113.9:443", "203.0.113.9", "[^x]*", "203.0.113.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveSessionClientIP(zap.NewNop(), cfRequest(tc.peer, tc.header), tc.peerIP, c)
			if got != tc.want {
				t.Fatalf("client_ip=%q, want %q", got, tc.want)
			}
		})
	}
}

// No list yet: the header is not trusted.
func TestResolveSessionClientIP_NoListTrustsNothing(t *testing.T) {
	srv := newCFServer(t)
	c := srv.ranges(t, zap.NewNop(), &memCFStore{})
	got := resolveSessionClientIP(zap.NewNop(), cfRequest("203.0.113.9:443", "1.2.3.4"), "203.0.113.9", c)
	if got != "203.0.113.9" {
		t.Fatalf("client_ip=%q with no list, want the peer address", got)
	}
	if got := resolveSessionClientIP(zap.NewNop(), cfRequest("203.0.113.9:443", "1.2.3.4"), "203.0.113.9", nil); got != "203.0.113.9" {
		t.Fatalf("client_ip=%q with nil ranges, want the peer address", got)
	}
}

// 3. A failed refresh keeps the last good list, in memory and in storage.
func TestCloudflareRanges_FailedRefreshKeepsLastList(t *testing.T) {
	for _, tc := range []struct {
		name   string
		v4, v6 string
		status int
	}{
		{"http error", "", "", http.StatusInternalServerError},
		{"empty body", "", "", http.StatusOK},
		{"empty v6 only", testCFv4Body, "\n\n", http.StatusOK},
		{"malformed line", "203.0.113.0/24\nnot-a-cidr\n", testCFv6Body, http.StatusOK},
		{"bare address, not CIDR", "203.0.113.1\n", testCFv6Body, http.StatusOK},
		{"html error page", "<html>blocked</html>", "<html>blocked</html>", http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, srv, store := refreshedRanges(t)
			before, _ := store.Read(context.Background())

			srv.set(tc.v4, tc.v6, tc.status)
			if err := c.Refresh(context.Background()); err == nil {
				t.Fatal("Refresh returned nil for a bad download")
			}
			if !containsStr(t, c, "203.0.113.9") || !containsStr(t, c, "2001:db8::9") {
				t.Fatal("last good list was lost from memory after a failed refresh")
			}
			after, _ := store.Read(context.Background())
			if string(before) != string(after) {
				t.Fatalf("stored list was replaced by a bad download:\nbefore=%s\nafter=%s", before, after)
			}
			v4, v6 := c.Counts()
			if v4 != 2 || v6 != 1 {
				t.Fatalf("counts=%d/%d, want 2/1", v4, v6)
			}
		})
	}
}

// 4. After a restart the list comes from the storage object, with no download.
func TestCloudflareRanges_ListSurvivesRestart(t *testing.T) {
	c, srv, store := refreshedRanges(t)
	_ = c
	hitsAfterFirst := srv.hits.Load()

	dead := newCFServer(t)
	dead.set("", "", http.StatusInternalServerError)
	fresh := dead.ranges(t, zap.NewNop(), store)
	if containsStr(t, fresh, "203.0.113.9") {
		t.Fatal("fresh component trusted a range before Load")
	}
	if err := fresh.Load(context.Background()); err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !containsStr(t, fresh, "203.0.113.9") || !containsStr(t, fresh, "2001:db8::1") {
		t.Fatal("fresh component does not trust the stored ranges after Load")
	}
	if containsStr(t, fresh, "198.51.100.1") {
		t.Fatal("fresh component trusts an address outside the stored ranges")
	}
	if dead.hits.Load() != 0 || srv.hits.Load() != hitsAfterFirst {
		t.Fatalf("Load downloaded: dead=%d first=%d(was %d)", dead.hits.Load(), srv.hits.Load(), hitsAfterFirst)
	}
}

// A corrupt stored object must not be trusted.
func TestCloudflareRanges_CorruptStoredListIgnored(t *testing.T) {
	store := &memCFStore{data: []byte(`{"ipv4":["not-a-cidr"],"ipv6":[]}`)}
	c := newCFServer(t).ranges(t, zap.NewNop(), store)
	_ = c.Load(context.Background())
	if v4, v6 := c.Counts(); v4 != 0 || v6 != 0 {
		t.Fatalf("corrupt stored list was loaded: %d/%d", v4, v6)
	}
}

// 5. A request never waits on the download.
func TestCloudflareRanges_LookupDoesNotWaitOnDownload(t *testing.T) {
	c, srv, _ := refreshedRanges(t)

	srv.mu.Lock()
	srv.blockCh = make(chan struct{})
	srv.enterCh = make(chan struct{}, 8)
	block, enter := srv.blockCh, srv.enterCh
	srv.mu.Unlock()
	defer close(block)

	done := make(chan error, 1)
	go func() { done <- c.Refresh(context.Background()) }()
	select {
	case <-enter:
	case <-time.After(5 * time.Second):
		t.Fatal("the refresh never started its download")
	}

	lookup := make(chan string, 1)
	go func() {
		lookup <- resolveSessionClientIP(zap.NewNop(), cfRequest("203.0.113.9:443", "1.2.3.4"), "203.0.113.9", c)
	}()
	select {
	case got := <-lookup:
		if got != "1.2.3.4" {
			t.Fatalf("client_ip=%q during a refresh, want 1.2.3.4 from the existing list", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("request path blocked while a download was in flight")
	}
	select {
	case <-done:
		t.Fatal("refresh finished while its download was still blocked; the test did not exercise an in-flight download")
	default:
	}
}

// 6. Log records.
func TestCloudflareRanges_RefreshLogs(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	srv := newCFServer(t)
	c := srv.ranges(t, zap.New(core), &memCFStore{})

	if err := c.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	infos := logs.FilterMessage("Cloudflare IP ranges refreshed").All()
	if len(infos) != 1 {
		t.Fatalf("got %d refresh INFO records, want 1 (all: %v)", len(infos), logs.All())
	}
	if infos[0].Level != zap.InfoLevel {
		t.Fatalf("refresh record level=%v, want info", infos[0].Level)
	}
	f := infos[0].ContextMap()
	if f["ipv4_ranges"] != int64(2) || f["ipv6_ranges"] != int64(1) {
		t.Fatalf("refresh record fields=%v, want ipv4_ranges=2 ipv6_ranges=1", f)
	}

	srv.set("", "", http.StatusBadGateway)
	if err := c.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh succeeded against a 502")
	}
	warns := logs.FilterMessage("Cloudflare IP ranges refresh failed; keeping the last list").All()
	if len(warns) != 1 || warns[0].Level != zap.WarnLevel {
		t.Fatalf("got %d failure WARN records, want 1 (all: %v)", len(warns), logs.All())
	}
	if _, ok := warns[0].ContextMap()["error"]; !ok {
		t.Fatalf("failure record has no error field: %v", warns[0].ContextMap())
	}
}

// The ignored-header line is DEBUG, never INFO or above.
func TestResolveSessionClientIP_IgnoredHeaderLogsDebugOnly(t *testing.T) {
	c, _, _ := refreshedRanges(t)
	core, logs := observer.New(zap.DebugLevel)

	_ = resolveSessionClientIP(zap.New(core), cfRequest("198.51.100.7:51234", "1.2.3.4"), "198.51.100.7", c)

	recs := logs.All()
	if len(recs) != 1 || recs[0].Level != zap.DebugLevel {
		t.Fatalf("want exactly one DEBUG record for an ignored header, got %v", recs)
	}
}

// No list: the WARN fires once per interval, not once per request.
func TestCloudflareRanges_NoListWarnsOncePerInterval(t *testing.T) {
	core, logs := observer.New(zap.WarnLevel)
	c := newCFServer(t).ranges(t, zap.New(core), &memCFStore{})
	for i := 0; i < 50; i++ {
		_ = resolveSessionClientIP(zap.NewNop(), cfRequest("203.0.113.9:443", "1.2.3.4"), "203.0.113.9", c)
	}
	if n := logs.Len(); n != 1 {
		t.Fatalf("got %d WARN records for 50 requests with no list, want exactly 1", n)
	}
}

// Start loads the stored list synchronously, so it is in force before serving.
func TestCloudflareRanges_StartLoadsStoredListBeforeReturning(t *testing.T) {
	_, _, store := refreshedRanges(t)
	dead := newCFServer(t)
	dead.set("", "", http.StatusInternalServerError)
	c := dead.ranges(t, zap.NewNop(), store)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	c.Start(ctx)
	defer c.Stop()
	if !containsStr(t, c, "203.0.113.9") {
		t.Fatal("stored list not in force when Start returned")
	}
}
