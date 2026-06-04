package server

import (
	"errors"
	"fmt"
	"testing"

	"github.com/bwmarrin/discordgo"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// restErr builds a *discordgo.RESTError carrying the given Discord API error code,
// matching what IsDiscordErrorCode inspects.
func restErr(code int) *discordgo.RESTError {
	return &discordgo.RESTError{
		Message: &discordgo.APIErrorMessage{Code: code},
	}
}

// TestIsInteractionTokenExpired verifies that the create-match monitor's
// stop condition fires only for the two Discord webhook-token-expiry codes
// (50027 Invalid Webhook Token, 10015 Unknown Webhook) and nothing else.
func TestIsInteractionTokenExpired(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{name: "invalid webhook token (50027)", err: restErr(50027), want: true},
		{name: "unknown webhook (10015)", err: restErr(10015), want: true},
		{name: "wrapped invalid webhook token", err: fmt.Errorf("edit failed: %w", restErr(50027)), want: true},
		{name: "wrapped unknown webhook", err: fmt.Errorf("edit failed: %w", restErr(10015)), want: true},
		{name: "other REST error (rate limited 429)", err: restErr(20028), want: false},
		{name: "plain error", err: errors.New("boom"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isInteractionTokenExpired(tt.err); got != tt.want {
				t.Fatalf("isInteractionTokenExpired(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

// TestIsExpectedUserError verifies the dispatch-error classifier: expected,
// user-facing failures must be logged at Warn (true), while genuinely
// unexpected failures (DB/storage/internal) stay at Error (false).
func TestIsExpectedUserError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil error", err: nil, want: false},
		{
			name: "grpc ResourceExhausted (rate limit)",
			err:  status.Error(codes.ResourceExhausted, "rate limit exceeded for match creation (1 per minute)"),
			want: true,
		},
		{
			name: "wrapped grpc ResourceExhausted",
			err:  fmt.Errorf("create: %w", status.Error(codes.ResourceExhausted, "rate limit exceeded")),
			want: true,
		},
		{
			name: "no available servers (allocate failure)",
			err:  ErrMatchmakingNoAvailableServers,
			want: true,
		},
		{
			name: "wrapped no available servers",
			err:  fmt.Errorf("allocate: %w", ErrMatchmakingNoAvailableServers),
			want: true,
		},
		{
			name: "player not found",
			err:  ErrPlayerNotFound,
			want: true,
		},
		{
			name: "inline player not found",
			err:  errors.New("player not found"),
			want: true,
		},
		{
			name: "grpc Internal (DB/storage) stays Error",
			err:  status.Error(codes.Internal, "failed to read latency history"),
			want: false,
		},
		{
			name: "generic storage error stays Error",
			err:  errors.New("storage: write failed"),
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := isExpectedUserError(tt.err); got != tt.want {
				t.Fatalf("isExpectedUserError(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}
