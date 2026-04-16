package server

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"golang.org/x/sync/semaphore"
)

const (
	// maxConcurrentDiscordREST is the maximum number of concurrent Discord REST
	// API requests. This prevents goroutine pileup when Discord rate-limits the
	// bot with long retry_after windows (e.g., 335s on 429 responses). Without
	// this, each blocked goroutine holds session state and buffers in memory,
	// causing OOM during reconnection storms.
	maxConcurrentDiscordREST = 10

	// discordRESTAcquireTimeout is how long a caller will wait to acquire a
	// semaphore slot before giving up. This prevents indefinite blocking when
	// all slots are occupied by rate-limited requests.
	discordRESTAcquireTimeout = 5 * time.Second
)

// ErrDiscordRESTOverloaded is returned when the Discord REST semaphore cannot
// be acquired within the timeout, indicating too many concurrent requests are
// already in flight (likely due to rate limiting).
var ErrDiscordRESTOverloaded = fmt.Errorf("discord REST API overloaded, try again later")

// discordRESTLimiter wraps an http.RoundTripper with a concurrency-limiting
// semaphore. It sits between discordgo's HTTP client and the actual transport,
// gating all outbound REST requests.
type discordRESTLimiter struct {
	inner http.RoundTripper
	sem   *semaphore.Weighted
}

// newDiscordRESTLimiter wraps the given transport (or http.DefaultTransport if
// nil) with a concurrency limiter.
func newDiscordRESTLimiter(inner http.RoundTripper) *discordRESTLimiter {
	if inner == nil {
		inner = http.DefaultTransport
	}
	return &discordRESTLimiter{
		inner: inner,
		sem:   semaphore.NewWeighted(maxConcurrentDiscordREST),
	}
}

// RoundTrip implements http.RoundTripper. It acquires a semaphore slot with a
// timeout before forwarding the request to the inner transport. If the
// semaphore cannot be acquired (all slots occupied by rate-limited requests),
// it returns ErrDiscordRESTOverloaded without making the HTTP call.
func (l *discordRESTLimiter) RoundTrip(req *http.Request) (*http.Response, error) {
	ctx := req.Context()

	// Create a timeout context for semaphore acquisition. This is separate
	// from the request context so we don't cancel the actual HTTP request
	// if the semaphore times out.
	acquireCtx, cancel := context.WithTimeout(ctx, discordRESTAcquireTimeout)
	defer cancel()

	if err := l.sem.Acquire(acquireCtx, 1); err != nil {
		return nil, ErrDiscordRESTOverloaded
	}
	defer l.sem.Release(1)

	return l.inner.RoundTrip(req)
}
