package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/mux"
)

type queueClock struct{ now time.Time }

func (c *queueClock) Now() time.Time { return c.now }

func newQueueRouter(est *MatchmakerQueueEstimator) *mux.Router {
	r := mux.NewRouter()
	r.HandleFunc(matchmakerQueuePath, NewMatchmakerQueueHandler(est)).Methods(http.MethodPost, http.MethodGet)
	return r
}

type queueReply struct {
	Status            string `json:"status"`
	QueuePosition     *int   `json:"queue_position"`
	EstimatedWaitTime *int   `json:"estimated_wait_time"`
}

func queueDo(t *testing.T, r *mux.Router, method, action, token, queue string) (int, queueReply) {
	t.Helper()
	url := "/ready_at_dawn/" + action + "?access_token=" + token + "&environment=live&queue_id=" + queue
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(method, url, nil))
	var reply queueReply
	if rr.Code == http.StatusOK {
		if got := rr.Header().Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q", got)
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &reply); err != nil {
			t.Fatalf("body %q: %v", rr.Body.String(), err)
		}
	}
	return rr.Code, reply
}

func TestMatchmakerQueueJoinAnswersQueuedWithTheFallbackEstimate(t *testing.T) {
	clock := &queueClock{now: time.Unix(1_000_000, 0)}
	r := newQueueRouter(NewMatchmakerQueueEstimator(clock.Now))
	code, reply := queueDo(t, r, http.MethodPost, "join_queue", "tok", "arena")
	if code != http.StatusOK || reply.Status != "queued" {
		t.Fatalf("join = %d %+v", code, reply)
	}
	if reply.EstimatedWaitTime == nil || *reply.EstimatedWaitTime != int(defaultQueueEstimate/time.Second) {
		t.Errorf("estimate = %v, want %d", reply.EstimatedWaitTime, int(defaultQueueEstimate/time.Second))
	}
	if reply.QueuePosition != nil {
		t.Errorf("queue_position must be absent (the game reads absent as unknown), got %d", *reply.QueuePosition)
	}
}

func TestMatchmakerQueuePollCountsDownAndNeverGoesBelowZero(t *testing.T) {
	clock := &queueClock{now: time.Unix(1_000_000, 0)}
	r := newQueueRouter(NewMatchmakerQueueEstimator(clock.Now))
	queueDo(t, r, http.MethodPost, "join_queue", "tok", "arena")
	clock.now = clock.now.Add(20 * time.Second)
	_, reply := queueDo(t, r, http.MethodPost, "poll_queue_position", "tok", "arena")
	if reply.Status != "queued" || reply.EstimatedWaitTime == nil || *reply.EstimatedWaitTime != 40 {
		t.Fatalf("poll after 20 s = %+v, want queued, 40", reply)
	}
	clock.now = clock.now.Add(10 * time.Minute)
	_, reply = queueDo(t, r, http.MethodPost, "poll_queue_position", "tok", "arena")
	if reply.EstimatedWaitTime == nil || *reply.EstimatedWaitTime != 0 {
		t.Fatalf("poll after the estimate = %+v, want 0", reply)
	}
}

func TestMatchmakerQueueEstimateIsTheMedianOfCompletedWaitsPerQueue(t *testing.T) {
	clock := &queueClock{now: time.Unix(1_000_000, 0)}
	est := NewMatchmakerQueueEstimator(clock.Now)
	r := newQueueRouter(est)
	for i, wait := range []time.Duration{10 * time.Second, 30 * time.Second, 50 * time.Second} {
		tok := "t" + string(rune('a'+i))
		queueDo(t, r, http.MethodPost, "join_queue", tok, "arena")
		clock.now = clock.now.Add(wait)
		code, reply := queueDo(t, r, http.MethodPost, "leave_queue", tok, "arena")
		if code != http.StatusOK || reply.Status != "none" || reply.EstimatedWaitTime != nil {
			t.Fatalf("leave = %d %+v", code, reply)
		}
	}
	// Another queue's long waits must not leak into this one.
	for i := 0; i < 3; i++ {
		tok := "c" + string(rune('a'+i))
		queueDo(t, r, http.MethodPost, "join_queue", tok, "combat")
		clock.now = clock.now.Add(900 * time.Second)
		queueDo(t, r, http.MethodPost, "leave_queue", tok, "combat")
	}
	_, reply := queueDo(t, r, http.MethodPost, "join_queue", "fresh", "arena")
	if reply.EstimatedWaitTime == nil || *reply.EstimatedWaitTime != 30 {
		t.Errorf("estimate after samples 10/30/50 = %v, want the median 30", reply.EstimatedWaitTime)
	}
	// A queue with no samples gets the fallback.
	_, other := queueDo(t, r, http.MethodPost, "join_queue", "fresh", "news")
	if other.EstimatedWaitTime == nil || *other.EstimatedWaitTime != int(defaultQueueEstimate/time.Second) {
		t.Errorf("unsampled queue estimate = %v, want the fallback", other.EstimatedWaitTime)
	}
}

func TestMatchmakerQueueEntriesAreKeyedPerTokenAndNeverLogTheToken(t *testing.T) {
	clock := &queueClock{now: time.Unix(1_000_000, 0)}
	est := NewMatchmakerQueueEstimator(clock.Now)
	r := newQueueRouter(est)
	queueDo(t, r, http.MethodPost, "join_queue", "alice", "arena")
	clock.now = clock.now.Add(25 * time.Second)
	_, bob := queueDo(t, r, http.MethodPost, "join_queue", "bob", "arena")
	if bob.EstimatedWaitTime == nil || *bob.EstimatedWaitTime != 60 {
		t.Errorf("bob's wait started at his own join, got %v", bob.EstimatedWaitTime)
	}
	est.mu.Lock()
	defer est.mu.Unlock()
	for k := range est.joined {
		if strings.Contains(k.tokenDigest, "alice") || strings.Contains(k.tokenDigest, "bob") {
			t.Errorf("a token is held in the clear: %q", k.tokenDigest)
		}
	}
}

func TestMatchmakerQueueRejectsMissingParametersAndOtherActions(t *testing.T) {
	r := newQueueRouter(NewMatchmakerQueueEstimator(nil))
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/ready_at_dawn/join_queue?environment=live", nil))
	if rr.Code != http.StatusBadRequest {
		t.Errorf("missing parameters = %d, want 400", rr.Code)
	}
	rr = httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/ready_at_dawn/accept_queue?access_token=a&queue_id=b", nil))
	if rr.Code != http.StatusNotFound {
		t.Errorf("unknown action = %d, want 404", rr.Code)
	}
	rr = httptest.NewRecorder()
	NewMatchmakerQueueHandler(NewMatchmakerQueueEstimator(nil)).ServeHTTP(rr,
		httptest.NewRequest(http.MethodDelete, "/ready_at_dawn/join_queue?access_token=a&queue_id=b", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("DELETE = %d, want 405", rr.Code)
	}
}

// The game's Find / LoginSettingsChanged take their normal branch while queue.state != 2 (queued and none
// both do); an "accepted" answer is the only one that would change matchmaking (echovr.exe 0x140168500,
// 0x140180550). This server has no queue to accept anyone from, so no request, in any state, may say it.
func TestMatchmakerQueueNeverAnswersAccepted(t *testing.T) {
	clock := &queueClock{now: time.Unix(1_000_000, 0)}
	r := newQueueRouter(NewMatchmakerQueueEstimator(clock.Now))
	allowed := map[string]bool{"queued": true, "none": true}
	check := func(label, action, token, queue string) {
		t.Helper()
		code, reply := queueDo(t, r, http.MethodPost, action, token, queue)
		if code != http.StatusOK {
			t.Fatalf("%s: %s = %d", label, action, code)
		}
		if !allowed[reply.Status] {
			t.Errorf("%s: %s answered status %q", label, action, reply.Status)
		}
	}
	for _, action := range []string{"join_queue", "poll_queue_position", "leave_queue"} {
		check("fresh", action, "never-joined", "arena")
	}
	check("join", "join_queue", "tok", "arena")
	for _, elapsed := range []time.Duration{time.Second, defaultQueueEstimate, 10 * defaultQueueEstimate} {
		clock.now = clock.now.Add(elapsed)
		check("poll after "+elapsed.String(), "poll_queue_position", "tok", "arena")
	}
	check("leave", "leave_queue", "tok", "arena")
	check("after samples", "join_queue", "tok", "arena")
	check("after samples", "poll_queue_position", "tok", "arena")
}

func TestMatchmakerQueueRejectsMalformedQueueIDs(t *testing.T) {
	r := newQueueRouter(NewMatchmakerQueueEstimator(nil))
	long := strings.Repeat("a", queueIDMaxLength+1)
	for _, id := range []string{long, "has space", "a/b", "a%00b", "ünï", "a;b", "<x>"} {
		rr := httptest.NewRecorder()
		url := "/ready_at_dawn/join_queue?access_token=t&queue_id=" + strings.ReplaceAll(id, " ", "%20")
		r.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, url, nil))
		if rr.Code != http.StatusBadRequest {
			t.Errorf("queue_id %q = %d, want 400", id, rr.Code)
		}
	}
	for _, id := range []string{"a", strings.Repeat("a", queueIDMaxLength), "arena_2.0:eu-west"} {
		if code, _ := queueDo(t, r, http.MethodPost, "join_queue", "t", id); code != http.StatusOK {
			t.Errorf("queue_id %q = %d, want 200", id, code)
		}
	}
}

func TestMatchmakerQueueOnlyKeepsSamplesForABoundedNumberOfQueues(t *testing.T) {
	clock := &queueClock{now: time.Unix(1_000_000, 0)}
	est := NewMatchmakerQueueEstimator(clock.Now)
	r := newQueueRouter(est)
	total := queueMaxSampled + 40
	for i := 0; i < total; i++ {
		id := "q" + strconv.Itoa(i)
		queueDo(t, r, http.MethodPost, "join_queue", "t", id)
		clock.now = clock.now.Add(5 * time.Second)
		queueDo(t, r, http.MethodPost, "leave_queue", "t", id)
	}
	est.mu.Lock()
	defer est.mu.Unlock()
	if len(est.samples) != queueMaxSampled || len(est.sampledOrder) != queueMaxSampled {
		t.Fatalf("sampled queues = %d (order %d), want %d", len(est.samples), len(est.sampledOrder), queueMaxSampled)
	}
	if _, ok := est.samples["q0"]; ok {
		t.Error("the oldest queue id was not evicted")
	}
	if _, ok := est.samples["q"+strconv.Itoa(total-1)]; !ok {
		t.Error("the newest queue id was evicted")
	}
}

func TestMatchmakerQueueIgnoresWaitsOutsideTheSaneRange(t *testing.T) {
	clock := &queueClock{now: time.Unix(1_000_000, 0)}
	est := NewMatchmakerQueueEstimator(clock.Now)
	r := newQueueRouter(est)
	for i, wait := range []time.Duration{0, 500 * time.Millisecond, queueMaxSample + time.Second, 24 * time.Hour} {
		tok := "t" + strconv.Itoa(i)
		queueDo(t, r, http.MethodPost, "join_queue", tok, "arena")
		clock.now = clock.now.Add(wait)
		queueDo(t, r, http.MethodPost, "leave_queue", tok, "arena")
	}
	est.mu.Lock()
	n := len(est.samples["arena"])
	est.mu.Unlock()
	if n != 0 {
		t.Fatalf("%d samples kept from out-of-range waits, want 0", n)
	}
	// The bounds themselves are kept.
	for i, wait := range []time.Duration{queueMinSample, queueMaxSample} {
		tok := "b" + strconv.Itoa(i)
		queueDo(t, r, http.MethodPost, "join_queue", tok, "arena")
		clock.now = clock.now.Add(wait)
		queueDo(t, r, http.MethodPost, "leave_queue", tok, "arena")
	}
	est.mu.Lock()
	defer est.mu.Unlock()
	if len(est.samples["arena"]) != 2 {
		t.Errorf("samples = %d, want the two boundary waits", len(est.samples["arena"]))
	}
}
