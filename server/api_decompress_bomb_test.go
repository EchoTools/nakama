package server

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"testing"

	"go.uber.org/zap"
)

// The production request cap on fortytwo. http.MaxBytesReader applies it to the
// COMPRESSED bytes at api.go:268; these tests prove it also reaches the
// decompressed stream.
const testMaxRequestSizeBytes = 2_048_000

// compressionBomb returns `size` bytes of zeros compressed with the named
// Content-Encoding. Deflate's maximum ratio is 1032:1, so a 2,048,000-byte
// request body — exactly what MaxBytesReader admits — expands to ~2.1 GB.
func compressionBomb(t *testing.T, encoding string, size int) []byte {
	t.Helper()

	var buf bytes.Buffer
	var w io.WriteCloser
	var err error
	switch encoding {
	case "gzip":
		w = gzip.NewWriter(&buf)
	case "deflate":
		w, err = flate.NewWriter(&buf, flate.BestCompression)
		if err != nil {
			t.Fatalf("flate.NewWriter: %v", err)
		}
	default:
		t.Fatalf("unknown encoding %q", encoding)
	}

	zeros := make([]byte, 64<<10)
	for written := 0; written < size; written += len(zeros) {
		n := len(zeros)
		if remaining := size - written; remaining < n {
			n = remaining
		}
		if _, err := w.Write(zeros[:n]); err != nil {
			t.Fatalf("write bomb: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close bomb: %v", err)
	}
	return buf.Bytes()
}

// readAllHandler is the read RpcFuncHttp performs at api_rpc.go:155 — it is the
// allocation the bomb targets.
func readAllHandler(read *int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		b, err := io.ReadAll(r.Body)
		*read = len(b)
		if err != nil {
			// Same branch RpcFuncHttp takes for an over-cap body.
			if err.Error() == "http: request body too large" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

// serveBomb runs one bomb through the real handler chain: MaxBytesReader on the
// compressed body (api.go:266-270) wrapping decompressHandler (api.go:540).
func serveBomb(t *testing.T, encoding string, bomb []byte) (status, read int) {
	t.Helper()

	inner := decompressHandler(zap.NewNop(), readAllHandler(&read), testMaxRequestSizeBytes)

	req := httptest.NewRequest(http.MethodPost, "/v2/rpc/test", bytes.NewReader(bomb))
	req.Header.Set("Content-Encoding", encoding)
	rec := httptest.NewRecorder()

	req.Body = http.MaxBytesReader(rec, req.Body, testMaxRequestSizeBytes)
	inner.ServeHTTP(rec, req)

	return rec.Code, read
}

// TestDecompressedRequestBodyIsBounded is the regression test for the
// 2026-08-12 OOM incident: a client-supplied gzip body passed the compressed
// size cap and then expanded without limit inside io.ReadAll.
//
// Evidence the bound is needed, all from the live incident:
//   - heap-212628.pb.gz, 34s before the 21:27:06Z kill: io.ReadAll held
//     3,383 MB live, 92.5% of the heap, on this exact stack.
//   - allocs-211816.pb.gz: io.ReadAll accounted for 57.5 GB of 94.2 GB
//     cumulative allocation in a 5m48s process lifetime.
//   - journalctl -k on fortytwo: nakama OOM-killed at anon-rss ~6.27 GB every
//     3-5 minutes (21:12:26, 21:15:01, 21:20:03, 21:24:20, 21:27:04, 21:31:45Z).
func TestDecompressedRequestBodyIsBounded(t *testing.T) {
	for _, encoding := range []string{"gzip", "deflate"} {
		t.Run(encoding, func(t *testing.T) {
			// 256 MiB decompressed. The real attack used the full 2,048,000-byte
			// budget for ~2.1 GB; 256 MiB proves the same unbounded path without
			// making the test itself an OOM risk.
			const decompressed = 256 << 20
			bomb := compressionBomb(t, encoding, decompressed)

			var before, after runtime.MemStats
			runtime.GC()
			runtime.ReadMemStats(&before)

			status, read := serveBomb(t, encoding, bomb)

			runtime.ReadMemStats(&after)
			delta := after.TotalAlloc - before.TotalAlloc

			t.Logf("%s: compressed=%d bytes (ratio %.0f:1) read=%d bytes status=%d TotalAlloc delta=%d bytes (%.1f MiB)",
				encoding, len(bomb), float64(decompressed)/float64(len(bomb)), read, status,
				delta, float64(delta)/(1<<20))

			// io.ReadAll grows by doubling, so a body at the cap costs roughly 2x
			// the cap cumulatively. 8x the cap is generous and still 15x below
			// what the unbounded path allocates for this payload.
			const ceiling = 8 * testMaxRequestSizeBytes
			if delta > ceiling {
				t.Errorf("decompressed body unbounded: %d bytes allocated from a %d-byte request (max %d)",
					delta, len(bomb), ceiling)
			}
			if read > testMaxRequestSizeBytes {
				t.Errorf("handler read %d decompressed bytes, cap is %d", read, testMaxRequestSizeBytes)
			}
			if status != http.StatusBadRequest {
				t.Errorf("status = %d, want %d (request body too large)", status, http.StatusBadRequest)
			}
		})
	}
}

// TestDecompressedRequestBodyUnderCapIsUnaffected proves the bound does not
// change behaviour for a legitimate compressed request.
func TestDecompressedRequestBodyUnderCapIsUnaffected(t *testing.T) {
	for _, encoding := range []string{"gzip", "deflate"} {
		t.Run(encoding, func(t *testing.T) {
			const size = 1 << 20 // 1 MiB decompressed, under the 2,048,000 cap.
			body := compressionBomb(t, encoding, size)

			status, read := serveBomb(t, encoding, body)

			if read != size {
				t.Errorf("read %d bytes, want %d", read, size)
			}
			if status != http.StatusOK {
				t.Errorf("status = %d, want %d", status, http.StatusOK)
			}
		})
	}
}
