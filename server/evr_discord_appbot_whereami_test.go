package server

import (
	"strings"
	"testing"
)

func TestServerIssueCustomIDLength(t *testing.T) {
	tests := []struct {
		name       string
		matchID    string
		serverIP   string
		serverPort uint16
		regionCode string
	}{
		{
			name:       "typical IPv4 match",
			matchID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890.nakama-0",
			serverIP:   "192.168.1.100",
			serverPort: 6792,
			regionCode: "us-east-1",
		},
		{
			name:       "long node name",
			matchID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890.nakama-production-node-12",
			serverIP:   "203.0.113.255",
			serverPort: 65535,
			regionCode: "ap-southeast-2",
		},
		{
			name:       "IPv6 address",
			matchID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890.nakama-0",
			serverIP:   "2001:0db8:85a3:0000:0000:8a2e:0370:7334",
			serverPort: 6792,
			regionCode: "eu-west-1",
		},
		{
			name:       "worst case: long everything",
			matchID:    "a1b2c3d4-e5f6-7890-abcd-ef1234567890.nakama-production-long-node-name",
			serverIP:   "2001:0db8:85a3:0000:0000:8a2e:0370:7334",
			serverPort: 65535,
			regionCode: "ap-southeast-2-extra",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ctx := buildServerIssueContext(tc.matchID, tc.serverIP, tc.serverPort, tc.regionCode)

			// Button custom IDs
			lagID := "rsi:lag:" + ctx
			otherID := "rsi:other:" + ctx
			// Modal custom ID
			modalID := "sim:other:" + ctx

			for _, pair := range []struct {
				label string
				id    string
			}{
				{"lag button", lagID},
				{"other button", otherID},
				{"modal", modalID},
			} {
				if len(pair.id) > 100 {
					t.Errorf("%s custom_id exceeds 100 chars (%d): %s", pair.label, len(pair.id), pair.id)
				}
			}
		})
	}
}

func TestBuildServerIssueContextStripsNodeName(t *testing.T) {
	matchID := "a1b2c3d4-e5f6-7890-abcd-ef1234567890.nakama-production-0"
	ctx := buildServerIssueContext(matchID, "1.2.3.4", 6792, "us-east-1")

	// Should only contain the UUID portion, not the node name
	if strings.Contains(ctx, "nakama") {
		t.Errorf("server context should not contain node name: %s", ctx)
	}
	if !strings.Contains(ctx, "a1b2c3d4-e5f6-7890-abcd-ef1234567890") {
		t.Errorf("server context should contain match UUID: %s", ctx)
	}
}

func TestBuildServerIssueContextRoundTrip(t *testing.T) {
	matchID := "a1b2c3d4-e5f6-7890-abcd-ef1234567890.nakama-0"
	ctx := buildServerIssueContext(matchID, "10.0.0.1", 6792, "us-west-2")

	parts := strings.SplitN(ctx, ":", 4)
	if len(parts) != 4 {
		t.Fatalf("expected 4 parts, got %d: %v", len(parts), parts)
	}
	if parts[0] != "a1b2c3d4-e5f6-7890-abcd-ef1234567890" {
		t.Errorf("matchID = %q, want UUID only", parts[0])
	}
	if parts[1] != "10.0.0.1" {
		t.Errorf("serverIP = %q, want 10.0.0.1", parts[1])
	}
	if parts[2] != "6792" {
		t.Errorf("serverPort = %q, want 6792", parts[2])
	}
	if parts[3] != "us-west-2" {
		t.Errorf("regionCode = %q, want us-west-2", parts[3])
	}
}
