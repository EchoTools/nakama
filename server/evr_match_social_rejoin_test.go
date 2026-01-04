package server

import (
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestMatchLabel_OriginSocialID(t *testing.T) {
	// Test that OriginSocialID can be set and retrieved
	originID := uuid.Must(uuid.NewV4())

	label := &MatchLabel{
		Mode:           evr.ModeArenaPrivate,
		LobbyType:      PrivateLobby,
		OriginSocialID: &originID,
	}

	if label.OriginSocialID == nil {
		t.Fatal("OriginSocialID should not be nil")
	}

	if *label.OriginSocialID != originID {
		t.Errorf("OriginSocialID = %v, want %v", *label.OriginSocialID, originID)
	}
}

func TestMatchSettings_OriginSocialID(t *testing.T) {
	// Test that OriginSocialID can be set in MatchSettings
	originID := uuid.Must(uuid.NewV4())
	groupID := uuid.Must(uuid.NewV4())

	settings := &MatchSettings{
		Mode:           evr.ModeArenaPrivate,
		Level:          evr.LevelArena,
		GroupID:        groupID,
		OriginSocialID: &originID,
	}

	if settings.OriginSocialID == nil {
		t.Fatal("OriginSocialID should not be nil")
	}

	if *settings.OriginSocialID != originID {
		t.Errorf("OriginSocialID = %v, want %v", *settings.OriginSocialID, originID)
	}
}

func TestMatchLabel_IsPrivateMatch(t *testing.T) {
	tests := []struct {
		name string
		mode evr.Symbol
		want bool
	}{
		{
			name: "Private Arena",
			mode: evr.ModeArenaPrivate,
			want: true,
		},
		{
			name: "Private Combat",
			mode: evr.ModeCombatPrivate,
			want: true,
		},
		{
			name: "Private Social",
			mode: evr.ModeSocialPrivate,
			want: true,
		},
		{
			name: "Public Arena",
			mode: evr.ModeArenaPublic,
			want: false,
		},
		{
			name: "Public Combat",
			mode: evr.ModeCombatPublic,
			want: false,
		},
		{
			name: "Public Social",
			mode: evr.ModeSocialPublic,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			label := &MatchLabel{
				Mode: tt.mode,
			}
			if got := label.IsPrivateMatch(); got != tt.want {
				t.Errorf("IsPrivateMatch() = %v, want %v", got, tt.want)
			}
		})
	}
}
