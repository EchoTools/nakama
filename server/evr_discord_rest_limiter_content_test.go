package server

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"
)

// captureTransport records the request body that reaches the wire.
type captureTransport struct {
	body        []byte
	contentLen  int64
	headerLen   string
	roundTrips  int
	contentType string
}

func (c *captureTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	c.roundTrips++
	if req.Body != nil {
		c.body, _ = io.ReadAll(req.Body)
	}
	c.contentLen = req.ContentLength
	c.headerLen = req.Header.Get("Content-Length")
	c.contentType = req.Header.Get("Content-Type")
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Request: req}, nil
}

func sendThroughLimiter(t *testing.T, method, contentType string, body []byte) *captureTransport {
	t.Helper()
	ct := &captureTransport{}
	limiter := newDiscordRESTLimiter(ct)
	req, err := http.NewRequestWithContext(t.Context(), method, "https://discord.com/api/v9/webhooks/1/tok/messages/@original", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", contentType)
	if _, err := limiter.RoundTrip(req); err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	return ct
}

func wireContent(t *testing.T, ct *captureTransport) (string, map[string]any) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(ct.body, &m); err != nil {
		t.Fatalf("body on the wire is not JSON: %v: %q", err, ct.body)
	}
	s, _ := m["content"].(string)
	return s, m
}

func TestDiscordRESTContentIsClampedToTwoThousandRunes(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		untouched bool
	}{
		{"ascii exactly 2000 passes untouched", strings.Repeat("a", 2000), true},
		{"ascii 2001 is cut", strings.Repeat("a", 2001), false},
		{"multibyte exactly 2000 runes (6000 bytes) passes untouched", strings.Repeat("あ", 2000), true},
		{"multibyte 2001 runes is cut", strings.Repeat("あ", 2001), false},
		{"astral exactly 2000 runes passes untouched", strings.Repeat("😀", 2000), true},
		{"allocate-sized label reply is cut", "```json\n" + strings.Repeat(`  "k": "value",`+"\n", 400) + "```\nhttps://echo.taxi/spark://c/X", false},
	}
	for _, tc := range cases {
		for _, method := range []string{http.MethodPost, http.MethodPatch} {
			t.Run(tc.name+" "+method, func(t *testing.T) {
				body, _ := json.Marshal(map[string]any{"content": tc.content, "flags": 64})
				ct := sendThroughLimiter(t, method, "application/json", body)
				got, m := wireContent(t, ct)

				if n := utf8.RuneCountInString(got); n > 2000 {
					t.Fatalf("content reached Discord with %d runes; limit is 2000", n)
				}
				if !utf8.ValidString(got) {
					t.Fatalf("content was cut mid-rune")
				}
				if tc.untouched && got != tc.content {
					t.Fatalf("content at/below the limit was modified (got %d runes)", utf8.RuneCountInString(got))
				}
				if !tc.untouched && !strings.Contains(got, "truncated") {
					t.Fatalf("cut content carries no marker; tail: %q", got[max(0, len(got)-60):])
				}
				if m["flags"] != float64(64) {
					t.Fatalf("other fields were disturbed: %v", m)
				}
				if int64(len(ct.body)) != ct.contentLen {
					t.Fatalf("ContentLength %d does not match body length %d", ct.contentLen, len(ct.body))
				}
			})
		}
	}
}

func TestDiscordRESTClampLeavesOtherRequestsAlone(t *testing.T) {
	big := strings.Repeat("a", 5000)

	t.Run("no content field", func(t *testing.T) {
		body := []byte(`{"embeds":[{"title":"x"}]}`)
		ct := sendThroughLimiter(t, http.MethodPost, "application/json", body)
		if !bytes.Equal(ct.body, body) {
			t.Fatalf("body changed: %q", ct.body)
		}
	})
	t.Run("null content", func(t *testing.T) {
		body := []byte(`{"content":null}`)
		ct := sendThroughLimiter(t, http.MethodPatch, "application/json", body)
		if !bytes.Equal(ct.body, body) {
			t.Fatalf("body changed: %q", ct.body)
		}
	})
	t.Run("non-JSON content type is not parsed", func(t *testing.T) {
		body := []byte(`{"content":"` + big + `"}`)
		ct := sendThroughLimiter(t, http.MethodPost, "multipart/form-data; boundary=x", body)
		if !bytes.Equal(ct.body, body) {
			t.Fatalf("multipart body was rewritten")
		}
	})
	t.Run("GET is untouched", func(t *testing.T) {
		ct := sendThroughLimiter(t, http.MethodGet, "application/json", nil)
		if ct.roundTrips != 1 {
			t.Fatalf("round trips = %d", ct.roundTrips)
		}
	})
	t.Run("malformed JSON is forwarded as-is", func(t *testing.T) {
		body := []byte(`{"content": "` + big)
		ct := sendThroughLimiter(t, http.MethodPost, "application/json", body)
		if !bytes.Equal(ct.body, body) {
			t.Fatalf("malformed body was rewritten")
		}
	})
}

func TestAllocateReplyFitsAndKeepsTheJoinLink(t *testing.T) {
	id := "6B2E2B8E-0000-4000-8000-000000000000"
	for _, label := range []string{"{}", strings.Repeat("あ", 5000), strings.Repeat(`  "k": "v",`+"\n", 600)} {
		got := allocateReply(label, id)
		if n := utf8.RuneCountInString(got); n > 2000 {
			t.Fatalf("reply is %d runes", n)
		}
		if !strings.HasSuffix(got, "```\nhttps://echo.taxi/spark://c/"+id) {
			t.Fatalf("closing fence/join link lost: %q", got[max(0, len(got)-80):])
		}
	}
}
