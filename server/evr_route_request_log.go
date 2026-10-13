package server

import (
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
)

// The service-status and matchmaker-queue routes are called by game clients, which cannot send an HTTP key.
// Nothing authenticates those requests, so the record each one leaves is its only attribution: the client
// address, a label for the token it carried (never the token), what it asked for, the outcome, the latency
// and the size of the answer. One record per request, written where the handler returns.

// routeRecorder remembers the status and the size of what a handler wrote.
type routeRecorder struct {
	http.ResponseWriter
	status int
	bytes  int
}

func (r *routeRecorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *routeRecorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += n
	return n, err
}

// Status is what the client saw: a handler that wrote nothing answered 200.
func (r *routeRecorder) Status() int {
	if r.status == 0 {
		return http.StatusOK
	}
	return r.status
}

// routeClientAddress is the client's address: the first X-Forwarded-For entry when the request came through
// the proxy, else the peer address without its port.
func routeClientAddress(r *http.Request) string {
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		first, _, _ := strings.Cut(forwarded, ",")
		if first = strings.TrimSpace(first); first != "" {
			return first
		}
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

// routeTokenLabel names a token without revealing it: the first 8 hex characters of its SHA-256, or "none".
func routeTokenLabel(token string) string {
	if token == "" {
		return "none"
	}
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:])[:8]
}

// recordRoute writes the one record of a request. `fields` are what only the handler knows.
func recordRoute(logger runtime.Logger, message string, r *http.Request, rec *routeRecorder, start time.Time, fields map[string]interface{}) {
	all := map[string]interface{}{
		"remote":     routeClientAddress(r),
		"method":     r.Method,
		"path":       r.URL.Path,
		"status":     rec.Status(),
		"bytes":      rec.bytes,
		"latency_ms": time.Since(start).Milliseconds(),
	}
	for k, v := range fields {
		all[k] = v
	}
	logger.WithFields(all).Info("%s", message)
}
