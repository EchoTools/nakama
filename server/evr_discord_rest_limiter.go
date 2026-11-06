package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/heroiclabs/nakama-common/runtime"
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

	// acquireTimeout overrides discordRESTAcquireTimeout. Zero means use the
	// constant. It exists so a test can prove the timeout path without
	// spending the production timeout in real time.
	acquireTimeout time.Duration

	// logger receives a record each time a message body is clamped. Optional.
	logger runtime.Logger
}

const (
	// discordMaxContentRunes is Discord's limit on a message's content field.
	// Discord counts characters (code points), not bytes. A request over it is
	// rejected with 400 "Invalid Form Body ... BASE_TYPE_MAX_LENGTH".
	discordMaxContentRunes = 2000

	// discordContentTruncatedMarker replaces the tail of an over-long message.
	discordContentTruncatedMarker = "\n… (truncated)"
)

// truncateDiscordContent returns s unchanged if it is at most
// discordMaxContentRunes characters; otherwise it keeps a prefix and appends a
// marker so the result is exactly discordMaxContentRunes characters. Lengths are
// rune counts, so multibyte text is never cut by byte length or mid-rune.
func truncateDiscordContent(s string) (string, bool) {
	if utf8.RuneCountInString(s) <= discordMaxContentRunes {
		return s, false
	}
	keep := discordMaxContentRunes - utf8.RuneCountInString(discordContentTruncatedMarker)
	end, n := 0, 0
	for n < keep {
		_, size := utf8.DecodeRuneInString(s[end:])
		end += size
		n++
	}
	return s[:end] + discordContentTruncatedMarker, true
}

// clampContent rewrites a JSON message body whose top-level "content" exceeds
// Discord's limit. Every send path in the bot (interaction replies, webhook
// edits, channel messages, DMs) goes through this transport, so none of them can
// reach Discord with an over-long content. Bodies that are not JSON (multipart
// uploads), not parseable, or without a string content are forwarded untouched.
func (l *discordRESTLimiter) clampContent(req *http.Request) {
	if req.Body == nil || (req.Method != http.MethodPost && req.Method != http.MethodPatch && req.Method != http.MethodPut) {
		return
	}
	if mt, _, err := mime.ParseMediaType(req.Header.Get("Content-Type")); err != nil || mt != "application/json" {
		return
	}
	raw, err := io.ReadAll(req.Body)
	_ = req.Body.Close()
	restore := func(b []byte) {
		req.Body = io.NopCloser(bytes.NewReader(b))
		req.GetBody = func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader(b)), nil }
		req.ContentLength = int64(len(b))
		if req.Header.Get("Content-Length") != "" {
			req.Header.Set("Content-Length", fmt.Sprint(len(b)))
		}
	}
	if err != nil {
		restore(raw)
		return
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil {
		restore(raw)
		return
	}
	var content string
	if c, ok := fields["content"]; !ok || json.Unmarshal(c, &content) != nil {
		restore(raw)
		return
	}
	cut, changed := truncateDiscordContent(content)
	if !changed {
		restore(raw)
		return
	}
	fields["content"], _ = json.Marshal(cut)
	out, err := json.Marshal(fields)
	if err != nil {
		restore(raw)
		return
	}
	if l.logger != nil {
		l.logger.WithFields(map[string]interface{}{
			"method":         req.Method,
			"path":           req.URL.Path,
			"original_runes": utf8.RuneCountInString(content),
			"limit_runes":    discordMaxContentRunes,
		}).Warn("Discord message content exceeded the limit and was truncated")
	}
	restore(out)
}

// timeout returns the semaphore acquisition timeout for this limiter.
func (l *discordRESTLimiter) timeout() time.Duration {
	if l.acquireTimeout > 0 {
		return l.acquireTimeout
	}
	return discordRESTAcquireTimeout
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
	acquireCtx, cancel := context.WithTimeout(ctx, l.timeout())
	defer cancel()

	if err := l.sem.Acquire(acquireCtx, 1); err != nil {
		return nil, ErrDiscordRESTOverloaded
	}
	defer l.sem.Release(1)

	l.clampContent(req)
	return l.inner.RoundTrip(req)
}

// allocateReply formats the /allocate reply. The label is shortened, not the
// whole message, so the closing code fence and the join link survive.
func allocateReply(labelJSON, matchID string) string {
	const format = "Match prepared with label ```json\n%s\n```\nhttps://echo.taxi/spark://c/%s"
	reply := fmt.Sprintf(format, labelJSON, matchID)
	if utf8.RuneCountInString(reply) <= discordMaxContentRunes {
		return reply
	}
	overhead := utf8.RuneCountInString(fmt.Sprintf(format, "", matchID)) + utf8.RuneCountInString(discordContentTruncatedMarker)
	runes := []rune(labelJSON)
	return fmt.Sprintf(format, string(runes[:discordMaxContentRunes-overhead])+discordContentTruncatedMarker, matchID)
}
