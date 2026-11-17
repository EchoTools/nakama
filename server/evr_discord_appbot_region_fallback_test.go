package server

import (
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// fallbackInteractionTransport stands in for Discord's REST API. It records every
// request and behaves like Discord once an interaction is acknowledged: a second
// callback answers 400 / 40060.
type fallbackInteractionTransport struct {
	mu       sync.Mutex
	acked    bool
	requests []string // "METHOD path"
	bodies   []string // request bodies, parallel to requests
}

func (f *fallbackInteractionTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()

	body := ""
	if req.Body != nil {
		b, _ := io.ReadAll(req.Body)
		body = string(b)
	}
	f.requests = append(f.requests, req.Method+" "+req.URL.Path)
	f.bodies = append(f.bodies, body)

	status, resp := http.StatusOK, "{}"
	if strings.HasSuffix(req.URL.Path, "/callback") {
		if f.acked {
			status = http.StatusBadRequest
			resp = `{"message": "Interaction has already been acknowledged.", "code": 40060}`
		}
		f.acked = true
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(resp)),
		Request:    req,
	}, nil
}

// reset forgets the recorded requests, keeping the acknowledged state.
func (f *fallbackInteractionTransport) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.requests, f.bodies = nil, nil
}

// TestRegionFallbackOptionsEditsDeferredResponse covers the /allocate and
// /create-match path with no region: the command handler has already sent the
// deferred ACK, so the "No Servers Available" card must arrive as an edit of that
// deferred response, never as a second interaction callback (Discord 40060).
func TestRegionFallbackOptionsEditsDeferredResponse(t *testing.T) {
	const appID = "app-819"
	const token = "token-819"
	ix := &discordgo.InteractionCreate{Interaction: &discordgo.Interaction{
		ID:    "interaction-819",
		AppID: appID,
		Token: token,
		Type:  discordgo.InteractionApplicationCommand,
	}}

	dg, err := discordgo.New("Bot test")
	require.NoError(t, err)
	tr := &fallbackInteractionTransport{}
	dg.Client.Transport = tr

	// The deferred ACK that handleInteractionApplicationCommand sends before allocate runs.
	require.NoError(t, dg.InteractionRespond(ix.Interaction, &discordgo.InteractionResponse{
		Type: discordgo.InteractionResponseDeferredChannelMessageWithSource,
		Data: &discordgo.InteractionResponseData{Flags: discordgo.MessageFlagsEphemeral},
	}))
	tr.reset()

	d := &DiscordAppBot{}
	err = d.presentRegionFallbackOptions(dg, ix, &RegionFallbackInfo{RequestedRegionCode: "default"},
		"allocate-match", "default", evr.Symbol(0), evr.Symbol(0), time.Time{})
	require.NoError(t, err)

	tr.mu.Lock()
	defer tr.mu.Unlock()
	require.Equal(t, []string{"PATCH /api/v9/webhooks/" + appID + "/" + token + "/messages/@original"}, tr.requests,
		"the fallback must be a single edit of the deferred response; no second interaction callback")
	require.Contains(t, tr.bodies[0], "No Servers Available")
}
