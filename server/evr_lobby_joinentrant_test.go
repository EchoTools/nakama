package server

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
