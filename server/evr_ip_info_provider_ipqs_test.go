package server

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

type ipqsFailingTransport struct{ err error }

func (t ipqsFailingTransport) RoundTrip(*http.Request) (*http.Response, error) { return nil, t.err }

// TestIPQSTransportErrorLogDoesNotContainKey drives a transport error through
// Get and asserts that no logged field carries the API key (#600).
func TestIPQSTransportErrorLogDoesNotContainKey(t *testing.T) {
	const apiKey = "SECRETKEY0123456789abcdef"

	core, logs := observer.New(zapcore.DebugLevel)
	client, err := NewIPQSClient(zap.New(core), &testMetrics{}, nil, apiKey)
	if err != nil {
		t.Fatalf("NewIPQSClient: %v", err)
	}
	client.httpClient = &http.Client{Transport: ipqsFailingTransport{err: context.DeadlineExceeded}}

	info, err := client.Get(context.Background(), "5.133.46.233")
	if info != nil || err != nil {
		t.Fatalf("Get = (%v, %v), want fail-open (nil, nil)", info, err)
	}

	entries := logs.FilterMessage("IPQS circuit breaker opened, failing open with backoff").All()
	if len(entries) != 1 {
		t.Fatalf("breaker warn lines = %d, want 1 (all logs: %v)", len(entries), logs.All())
	}
	for _, e := range logs.All() {
		if strings.Contains(e.Message, apiKey) {
			t.Fatalf("log message holds the API key: %q", e.Message)
		}
		for _, f := range e.Context {
			if strings.Contains(f.String, apiKey) {
				t.Fatalf("log field %q holds the API key: %q", f.Key, f.String)
			}
			if f.Interface != nil {
				if fe, ok := f.Interface.(error); ok && strings.Contains(fe.Error(), apiKey) {
					t.Fatalf("log field %q holds the API key: %q", f.Key, fe.Error())
				}
			}
		}
	}
	// The line keeps the cause.
	var cause error
	for _, f := range entries[0].Context {
		if e, ok := f.Interface.(error); ok {
			cause = e
		}
	}
	if cause == nil || !errors.Is(cause, context.DeadlineExceeded) {
		t.Fatalf("logged error = %v, want it to keep the cause %v", cause, context.DeadlineExceeded)
	}
}
