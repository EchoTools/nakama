package server

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"go.uber.org/zap"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestLoginHistory_IgnoreSuspensionsOfAltAccounts_Migration(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		payload string
		want    bool
	}{
		{"old key true migrates to new", `{"ignore_disabled_alternates":true}`, true},
		{"new key only", `{"ignore_suspensions_of_alt_accounts":true}`, true},
		{"both true", `{"ignore_disabled_alternates":true,"ignore_suspensions_of_alt_accounts":true}`, true},
		{"old true, new explicitly false: a true is never turned into false", `{"ignore_disabled_alternates":true,"ignore_suspensions_of_alt_accounts":false}`, true},
		{"old false", `{"ignore_disabled_alternates":false}`, false},
		{"old false, new true", `{"ignore_disabled_alternates":false,"ignore_suspensions_of_alt_accounts":true}`, true},
		{"neither key", `{}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			h := NewLoginHistory(uuid.Must(uuid.NewV4()).String())
			if err := json.Unmarshal([]byte(tt.payload), h); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if h.IgnoreSuspensionsOfAltAccounts != tt.want {
				t.Fatalf("IgnoreSuspensionsOfAltAccounts = %v, want %v", h.IgnoreSuspensionsOfAltAccounts, tt.want)
			}
			if h.IgnoreDisabledAlternates {
				t.Fatal("deprecated IgnoreDisabledAlternates must be cleared after load")
			}

			out, err := json.Marshal(h)
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if strings.Contains(string(out), "ignore_disabled_alternates") {
				t.Fatalf("re-marshalled record still carries the old key: %s", out)
			}
			if !strings.Contains(string(out), `"ignore_suspensions_of_alt_accounts"`) {
				t.Fatalf("re-marshalled record lacks the new key: %s", out)
			}
		})
	}
}

// A stored record that carries only the OLD key (every record written before
// the rename, and any moderator console edit that still uses it) must behave
// exactly as before: the alt's suspension is ignored, the player's own is not.
func TestLoginHistory_OldKeyRecordStillIgnoresAltSuspensions(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name       string
		stored     string
		wantReject bool
	}{
		{"old key true", `{"ignore_disabled_alternates":true}`, false},
		{"new key true", `{"ignore_suspensions_of_alt_accounts":true}`, false},
		{"no key (control)", `{}`, true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			groupID := uuid.Must(uuid.NewV4()).String()
			userID := uuid.Must(uuid.NewV4()).String()
			altID := uuid.Must(uuid.NewV4()).String()
			nk := newSeatTestNK()
			writeSuspension(t, nk, altID, groupID, time.Now().Add(24*time.Hour), "alt suspended")

			h := NewLoginHistory(userID)
			if err := json.Unmarshal([]byte(tt.stored), h); err != nil {
				t.Fatal(err)
			}

			session := newSeatTestSession(uuid.FromStringOrNil(userID), []string{userID, altID})
			// What the login pipeline does with the loaded history.
			params, _ := LoadParams(session.Context())
			params.ignoreSuspensionsOfAltAccounts = h.IgnoreSuspensionsOfAltAccounts

			ggReg := seatTestGuildGroupRegistry(map[string]*GuildGroup{
				groupID: seatTestGuildGroup(groupID, "StrictGuild", true),
			})
			err := enforceJoinSuspension(context.Background(), zap.NewNop(), nk, ggReg,
				makeLabel(groupID, evr.ModeArenaPublic), session)
			if tt.wantReject && err == nil {
				t.Fatal("expected the alt's suspension to reject the player, got nil")
			}
			if !tt.wantReject && err != nil {
				t.Fatalf("expected the alt's suspension to be ignored, got: %v", err)
			}
		})
	}
}
