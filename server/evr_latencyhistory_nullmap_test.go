package server

// Characterization tests for LatencyHistory.UnmarshalJSON's handling of a
// null/absent map, measured against the reflection decode it replaced.
//
// The explicit UnmarshalJSON is allowed exactly two documented divergences from
// stock encoding/json (see the comment on UnmarshalJSON): a null map preserves
// the receiver's existing entries, and a failed decode is all-or-nothing.
// "Whether the map is nil" is NOT on that list, and it is observable: a nil map
// marshals to `null` and an empty non-nil map marshals to `{}`, so allocating on
// a null decode rewrites a stored record's shape on the next write.
//
// stockDecodeMap is the reference implementation: latencyHistoryData is the
// exact struct the reflection decode operated on, so decoding into it IS what
// encoding/json used to do to the receiver's field.

import (
	"encoding/json"
	"net"
	"testing"
	"time"
)

// stockDecodeMap reports what plain encoding/json does to the map field for a
// given document, starting from the given receiver map.
func stockDecodeMap(t *testing.T, doc string, receiver map[string][]LatencyHistoryItem) map[string][]LatencyHistoryItem {
	t.Helper()
	stock := latencyHistoryData{GameServerLatencies: receiver}
	if err := json.Unmarshal([]byte(doc), &stock); err != nil {
		t.Fatalf("stock decode of %s: %v", doc, err)
	}
	return stock.GameServerLatencies
}

// TestLatencyHistory_UnmarshalNullMapIntoNilReceiverStaysNil is the case
// Copilot flagged. A fresh &LatencyHistory{} is what every StorableRead decodes
// into, and `{"game_server_latencies":null}` is a document that is genuinely
// stored (a nil-map history serializes to exactly that). Allocating an empty map
// here is an undocumented third divergence, and unlike the other two it is
// visible in the persisted record.
func TestLatencyHistory_UnmarshalNullMapIntoNilReceiverStaysNil(t *testing.T) {
	const doc = `{"game_server_latencies":null}`

	// The reference: stock encoding/json leaves a nil receiver map nil.
	if got := stockDecodeMap(t, doc, nil); got != nil {
		t.Fatalf("premise broken: stock encoding/json produced %#v for a null map into a nil receiver, want nil", got)
	}

	h := &LatencyHistory{}
	if err := json.Unmarshal([]byte(doc), h); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if h.GameServerLatencies != nil {
		t.Errorf("decoding %s into a nil-map receiver produced a non-nil map (%#v); stock encoding/json leaves it nil, and the difference is persisted: a nil map marshals to `null`, an allocated one to `{}`",
			doc, h.GameServerLatencies)
	}
}

// TestLatencyHistory_UnmarshalAbsentMapIntoNilReceiverStaysNil covers the same
// divergence reached through an absent key rather than an explicit null.
func TestLatencyHistory_UnmarshalAbsentMapIntoNilReceiverStaysNil(t *testing.T) {
	const doc = `{}`

	if got := stockDecodeMap(t, doc, nil); got != nil {
		t.Fatalf("premise broken: stock encoding/json produced %#v for an absent map, want nil", got)
	}

	h := &LatencyHistory{}
	if err := json.Unmarshal([]byte(doc), h); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if h.GameServerLatencies != nil {
		t.Errorf("decoding %s into a nil-map receiver produced a non-nil map (%#v); stock encoding/json leaves it nil", doc, h.GameServerLatencies)
	}
}

// TestLatencyHistory_NullMapRecordRoundTrips is the consequence Copilot named:
// a stored `{"game_server_latencies":null}` record, read and written back
// unchanged, must still be `null`. This is the read-modify-write StorableRead ->
// StorableWrite performs, and a shape change here rewrites every such record in
// storage the first time it is touched.
func TestLatencyHistory_NullMapRecordRoundTrips(t *testing.T) {
	const stored = `{"game_server_latencies":null}`

	h := &LatencyHistory{}
	if err := json.Unmarshal([]byte(stored), h); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	out, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != stored {
		t.Errorf("a stored null-map record round-tripped to %s, want %s; reading a record and writing it back unchanged must not rewrite its shape", out, stored)
	}
}

// TestLatencyHistory_UnmarshalEmptyObjectMapStillAllocates is the guard on the
// other side of the fix: `{}` for the map is NOT null, and stock encoding/json
// allocates for it. An early return keyed on "decoded map is nil" must not
// swallow this case, or an explicitly-empty stored record would read back as nil
// and marshal to `null`.
func TestLatencyHistory_UnmarshalEmptyObjectMapStillAllocates(t *testing.T) {
	const doc = `{"game_server_latencies":{}}`

	if got := stockDecodeMap(t, doc, nil); got == nil {
		t.Fatal("premise broken: stock encoding/json left the map nil for an explicit empty object")
	}

	h := &LatencyHistory{}
	if err := json.Unmarshal([]byte(doc), h); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if h.GameServerLatencies == nil {
		t.Error("an explicitly empty map decoded to a nil map; it must allocate, as stock encoding/json does, so the record round-trips as `{}` and not `null`")
	}
	out, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(out) != doc {
		t.Errorf("an empty-object record round-tripped to %s, want %s", out, doc)
	}
}

// TestLatencyHistory_UnmarshalNullMapStillPreservesPopulatedReceiver re-pins the
// ONE divergence that is intended, so the fix above cannot be implemented by
// simply restoring stock behavior. A null map must still not wipe a receiver
// that holds pending samples — that is what protects writeWithRetry's OCC
// re-read.
func TestLatencyHistory_UnmarshalNullMapStillPreservesPopulatedReceiver(t *testing.T) {
	const doc = `{"game_server_latencies":null}`

	// The reference behavior this one deliberately does NOT follow.
	seed := map[string][]LatencyHistoryItem{"10.0.0.2": {{RTT: 99, Timestamp: time.Time{}}}}
	if got := stockDecodeMap(t, doc, seed); got != nil {
		t.Fatalf("premise broken: stock encoding/json produced %#v for a null map into a populated receiver, want nil", got)
	}

	h := NewLatencyHistory()
	h.Add(net.ParseIP("10.0.0.2"), 99, 25, time.Time{})

	if err := json.Unmarshal([]byte(doc), h); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if h.GameServerLatencies == nil {
		t.Fatal("a null map nil'd a populated receiver; pending samples would be lost on the OCC re-read")
	}
	if _, ok := h.GameServerLatencies["10.0.0.2"]; !ok {
		t.Errorf("a null map dropped the receiver's own key: %#v", h.GameServerLatencies)
	}
}
