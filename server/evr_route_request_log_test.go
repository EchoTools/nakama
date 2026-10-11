package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/mux"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// The two routes the game itself calls (the service-status request and the matchmaker queue) are called by
// game clients that cannot send an HTTP key, so the record of each request is the only attribution there is:
// who (the client address, a label for the token, never the token), what (action, queue), the outcome, the
// latency and the size of the answer.

func observedRouteLogger(t *testing.T) (runtime.Logger, *observer.ObservedLogs) {
	t.Helper()
	core, logs := observer.New(zapcore.InfoLevel)
	return NewRuntimeGoLogger(zap.New(core)), logs
}

func oneRecord(t *testing.T, logs *observer.ObservedLogs, message string) map[string]interface{} {
	t.Helper()
	var found []observer.LoggedEntry
	for _, e := range logs.All() {
		if e.Message == message {
			found = append(found, e)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one %q record, got %d (all: %v)", message, len(found), logs.All())
	}
	return found[0].ContextMap()
}

func tokenLabelOf(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "sha256:" + hex.EncodeToString(sum[:])[:8]
}

func assertNoSecret(t *testing.T, logs *observer.ObservedLogs, secret string) {
	t.Helper()
	for _, e := range logs.All() {
		if strings.Contains(e.Message, secret) {
			t.Errorf("message carries the token: %q", e.Message)
		}
		for k, v := range e.ContextMap() {
			if s, ok := v.(string); ok && strings.Contains(s, secret) {
				t.Errorf("field %s carries the token: %q", k, s)
			}
		}
	}
}

// queueRequest drives the queue handler through the router shape the runtime uses.
func queueRequest(h http.HandlerFunc, method, action, query string, mutate func(*http.Request)) *httptest.ResponseRecorder {
	r := mux.NewRouter()
	r.HandleFunc(matchmakerQueuePath, h).Methods(http.MethodPost, http.MethodGet, http.MethodDelete)
	req := httptest.NewRequest(method, "/ready_at_dawn/"+action+"?"+query, nil)
	if mutate != nil {
		mutate(req)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestMatchmakerQueueRequestIsRecordedWithWhoWhatAndOutcome(t *testing.T) {
	logger, logs := observedRouteLogger(t)
	clock := &queueClock{}
	h := NewMatchmakerQueueHandler(logger, NewMatchmakerQueueEstimator(clock.Now))
	const token = "secret-token-value"
	rr := queueRequest(h, http.MethodPost, "join_queue", "access_token="+token+"&environment=live&queue_id=arena",
		func(r *http.Request) { r.RemoteAddr = "192.0.2.7:5555" })
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d", rr.Code)
	}
	rec := oneRecord(t, logs, "matchmaker queue request")
	want := map[string]interface{}{
		"action": "join_queue", "queue_id": "arena", "token": tokenLabelOf(token),
		"remote": "192.0.2.7", "status": int64(200),
	}
	for k, v := range want {
		if rec[k] != v {
			t.Errorf("field %s = %v (%T), want %v", k, rec[k], rec[k], v)
		}
	}
	if rec["estimated_wait_time"] != int64(defaultQueueEstimate.Seconds()) {
		t.Errorf("estimated_wait_time = %v", rec["estimated_wait_time"])
	}
	if b, ok := rec["bytes"].(int64); !ok || b != int64(rr.Body.Len()) {
		t.Errorf("bytes = %v, want %d", rec["bytes"], rr.Body.Len())
	}
	if _, ok := rec["latency_ms"]; !ok {
		t.Error("no latency_ms field")
	}
	assertNoSecret(t, logs, token)
}

func TestMatchmakerQueueRecordUsesTheForwardedClientAddress(t *testing.T) {
	logger, logs := observedRouteLogger(t)
	h := NewMatchmakerQueueHandler(logger, NewMatchmakerQueueEstimator(nil))
	queueRequest(h, http.MethodPost, "leave_queue", "access_token=t&queue_id=arena", func(r *http.Request) {
		r.RemoteAddr = "10.0.0.2:4000"
		r.Header.Set("X-Forwarded-For", "198.51.100.9, 10.0.0.2")
	})
	rec := oneRecord(t, logs, "matchmaker queue request")
	if rec["remote"] != "198.51.100.9" || rec["action"] != "leave_queue" {
		t.Errorf("record = %v", rec)
	}
	if _, present := rec["estimated_wait_time"]; present {
		t.Error("a leave carries no estimate")
	}
}

func TestMatchmakerQueueRefusedRequestsAreRecordedToo(t *testing.T) {
	logger, logs := observedRouteLogger(t)
	h := NewMatchmakerQueueHandler(logger, NewMatchmakerQueueEstimator(nil))
	rr := queueRequest(h, http.MethodPost, "join_queue", "queue_id=arena", nil) // no token
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", rr.Code)
	}
	rec := oneRecord(t, logs, "matchmaker queue request")
	if rec["status"] != int64(400) || rec["token"] != "none" {
		t.Errorf("record = %v", rec)
	}
	logs.TakeAll()
	rr = queueRequest(h, http.MethodDelete, "join_queue", "access_token=t&queue_id=arena", nil)
	if rr.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d", rr.Code)
	}
	rec = oneRecord(t, logs, "matchmaker queue request")
	if rec["status"] != int64(405) {
		t.Errorf("record = %v", rec)
	}
}

func TestServiceStatusRequestIsRecordedOnSuccess(t *testing.T) {
	logger, logs := observedRouteLogger(t)
	const body = `[{"serviceid":"services","available":true,"message":""}]`
	h := NewServiceStatusHTTPHandler(logger, func(context.Context) (string, error) { return body, nil })
	req := httptest.NewRequest(http.MethodGet, "/status/services,news?env=live&projectid=rad14", nil)
	req.RemoteAddr = "203.0.113.5:61000"
	h.ServeHTTP(httptest.NewRecorder(), req)
	rec := oneRecord(t, logs, "service status request")
	if rec["remote"] != "203.0.113.5" || rec["status"] != int64(200) || rec["bytes"] != int64(len(body)) {
		t.Errorf("record = %v", rec)
	}
	if rec["path"] != "/status/services,news" {
		t.Errorf("path = %v", rec["path"])
	}
	if _, ok := rec["latency_ms"]; !ok {
		t.Error("no latency_ms field")
	}
}

func TestServiceStatusFailureRecordCarriesTheErrorAsAField(t *testing.T) {
	logger, logs := observedRouteLogger(t)
	h := NewServiceStatusHTTPHandler(logger, func(context.Context) (string, error) { return "", errors.New("storage unavailable") })
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/status/services,news", nil))
	rec := oneRecord(t, logs, "service status request failed")
	if rec["error"] != "storage unavailable" || rec["status"] != int64(500) {
		t.Errorf("record = %v", rec)
	}
	for _, e := range logs.All() {
		if strings.Contains(e.Message, "%!") {
			t.Errorf("message is a formatting accident: %q", e.Message)
		}
	}
}
