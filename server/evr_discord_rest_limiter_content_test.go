package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama-common/runtime"
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

				if n := utf8.RuneCountInString(got); n > 2000 || (!tc.untouched && n != 2000) {
					t.Fatalf("content reached Discord with %d runes; want at most 2000 (exactly 2000 when cut)", n)
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

// logRecorder captures what the limiter logs, to prove no token is written.
type logRecorder struct {
	runtime.Logger
	lines []string
}

func (r *logRecorder) WithFields(f map[string]interface{}) runtime.Logger {
	r.lines = append(r.lines, fmt.Sprintf("%v", f))
	return r
}
func (r *logRecorder) Warn(format string, v ...interface{}) {
	r.lines = append(r.lines, fmt.Sprintf(format, v...))
}

// TestDiscordRESTClampsInteractionRepliesThroughRealDiscordgo drives the real
// discordgo client. InteractionRespond nests the text at data.content, and the
// webhook/interaction routes carry the interaction token in the URL path.
func TestDiscordRESTClampsInteractionRepliesThroughRealDiscordgo(t *testing.T) {
	const token = "SECRET-INTERACTION-TOKEN-abc123"
	long := strings.Repeat("あ", 2001)

	ct := &captureTransport{}
	rec := &logRecorder{}
	limiter := newDiscordRESTLimiter(ct)
	limiter.logger = rec

	s, err := discordgo.New("Bot test")
	if err != nil {
		t.Fatal(err)
	}
	s.Client.Transport = limiter

	ix := &discordgo.Interaction{ID: "1", AppID: "2", Token: token}
	check := func(name string, send func() error) {
		t.Helper()
		ct.body = nil
		_ = send() // the fake transport's "{}" reply may not decode; only the wire body matters
		var m map[string]any
		if err := json.Unmarshal(ct.body, &m); err != nil {
			t.Fatalf("%s: wire body not JSON: %v: %q", name, err, ct.body)
		}
		content, _ := m["content"].(string)
		if data, ok := m["data"].(map[string]any); ok {
			content, _ = data["content"].(string)
		}
		if n := utf8.RuneCountInString(content); n != 2000 {
			t.Fatalf("%s: content reached the wire with %d runes, want exactly 2000", name, n)
		}
	}

	check("InteractionRespond", func() error {
		return s.InteractionRespond(ix, &discordgo.InteractionResponse{
			Type: discordgo.InteractionResponseChannelMessageWithSource,
			Data: &discordgo.InteractionResponseData{Content: long},
		})
	})
	check("InteractionResponseEdit", func() error {
		_, err := s.InteractionResponseEdit(ix, &discordgo.WebhookEdit{Content: &long})
		return err
	})
	check("ChannelMessageSend", func() error {
		_, err := s.ChannelMessageSend("123", long)
		return err
	})
	check("FollowupMessageCreate", func() error {
		_, err := s.FollowupMessageCreate(ix, true, &discordgo.WebhookParams{Content: long})
		return err
	})

	if len(rec.lines) == 0 {
		t.Fatal("no warn record was written for the clamped sends")
	}
	for _, l := range rec.lines {
		if strings.Contains(l, token) {
			t.Fatalf("log record contains the interaction token: %s", l)
		}
	}
}

func TestDiscordRESTDoesNotMutateTheCallersRequest(t *testing.T) {
	body, _ := json.Marshal(map[string]any{"content": strings.Repeat("a", 3000)})
	req, _ := http.NewRequestWithContext(t.Context(), http.MethodPost, "https://discord.com/api/v9/channels/1/messages", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	before := req.ContentLength
	if _, err := newDiscordRESTLimiter(&captureTransport{}).RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if req.ContentLength != before {
		t.Fatalf("caller's request was mutated: ContentLength %d -> %d", before, req.ContentLength)
	}
}
