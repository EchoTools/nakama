package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// TestVPNDegradedWarning_FiresWhenIPQSLookupUnavailable simulates IPQS
// degradation: a guild has BlockVPNUsers enabled but the IPQS lookup is
// unavailable (params.ipInfo is nil), so the VPN gate in lobbyAuthorize
// silently no-ops and VPN users pass with no audit trail (SEC-6).
//
// warnVPNDegraded is the extracted logging path of that degradation case: it
// must fire a warn-level operator alert carrying the client IP, Discord ID,
// and guild ID. The "player still passes" property is structural — the
// degradation branch in lobbyAuthorize contains no return — and is covered by
// the gate being an else-if rather than a rejection path.
func TestVPNDegradedWarning_FiresWhenIPQSLookupUnavailable(t *testing.T) {
	vpnDegradedLogThrottle = newLogThrottle(vpnDegradedLogWindow)

	core, logs := observer.New(zapcore.DebugLevel)
	logger := zap.New(core)

	const (
		clientIP  = "203.0.113.7"
		discordID = "123456789012345678"
		guildID   = "987654321098765432"
	)

	warnVPNDegraded(logger, newRecordingMetrics().CustomCounter, clientIP, discordID, guildID, false)

	entries := logs.FilterMessage("VPN blocking degraded: IPQS lookup unavailable for VPN check").All()
	require.Len(t, entries, 1, "expected exactly one degraded-VPN warning")

	entry := entries[0]
	require.Equal(t, zapcore.WarnLevel, entry.Level, "degradation warning must be warn-level")

	ctx := entry.ContextMap()
	assert.Equal(t, clientIP, ctx["client_ip"])
	assert.Equal(t, discordID, ctx["discord_id"])
	assert.Equal(t, guildID, ctx["guild_id"])
}

func TestSuspensionRoleBased_MessageIncludesGuildName(t *testing.T) {
	tests := []struct {
		name      string
		guildName string
		want      string
	}{
		{
			name:      "includes guild name",
			guildName: "Echo Combat League",
			want:      "You are suspended from Echo Combat League.",
		},
		{
			name:      "guild name with special characters",
			guildName: "Guild <Test> & Friends",
			want:      "You are suspended from Guild <Test> & Friends.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := roleSuspensionUserMessage(tt.guildName)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestSuspensionRoleBased_AuditMessageIncludesGuildName(t *testing.T) {
	tests := []struct {
		name          string
		guildName     string
		suspendedRole string
		wantContains  []string
	}{
		{
			name:          "audit message includes guild and role",
			guildName:     "Echo Combat League",
			suspendedRole: "123456789",
			wantContains:  []string{"Echo Combat League", "<@&123456789>"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := roleSuspensionAuditMessage(tt.guildName, tt.suspendedRole)
			for _, want := range tt.wantContains {
				assert.Contains(t, got, want)
			}
		})
	}
}

func TestSuspensionRoleBased_DMMessageIncludesGuildName(t *testing.T) {
	tests := []struct {
		name      string
		guildName string
		want      string
	}{
		{
			name:      "DM includes guild name",
			guildName: "Echo Combat League",
			want:      "You have been suspended from **Echo Combat League** via a server role. Contact a moderator of that server for more information.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := roleSuspensionDMMessage(tt.guildName)
			assert.Equal(t, tt.want, got)
		})
	}
}
