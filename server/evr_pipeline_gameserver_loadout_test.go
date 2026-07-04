package server

import (
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// COSMETIC-1: evr_runtime_event_remotelogset.go's equip handler explicitly skips its own
// persistence for NativeSupport game servers ("NEVR (non-legacy) servers already persist
// loadout equips themselves"), which makes gameServerSaveLoadoutRequest (via
// sanitizeGameServerLoadout) the *only* place that validates ownership for those matches.
// These tests guard that path independently of the RemoteLogSet fix.

func TestSanitizeGameServerLoadout_RejectsUnownedVRMLTag(t *testing.T) {
	profile := &EVRProfile{account: &api.Account{Wallet: "{}"}} // empty wallet

	loadout := evr.DefaultCosmeticLoadout()
	loadout.Tag = "rwd_tag_s1_vrml_s1_finalist"

	result, err := sanitizeGameServerLoadout(loadout, profile)
	if err != nil {
		t.Fatalf("sanitizeGameServerLoadout returned error: %v", err)
	}

	def := evr.DefaultCosmeticLoadout().Tag
	if result.Tag == "rwd_tag_s1_vrml_s1_finalist" {
		t.Errorf("unowned VRML finalist tag persisted; got %q, want default %q", result.Tag, def)
	}
	if result.Tag != def {
		t.Errorf("tag slot not reset to default; got %q, want %q", result.Tag, def)
	}
}

func TestSanitizeGameServerLoadout_AllowsOwnedVRMLTag(t *testing.T) {
	profile := &EVRProfile{
		account: &api.Account{Wallet: `{"cosmetic:arena:rwd_tag_s1_vrml_s1_finalist":1}`},
	}

	loadout := evr.DefaultCosmeticLoadout()
	loadout.Tag = "rwd_tag_s1_vrml_s1_finalist"

	result, err := sanitizeGameServerLoadout(loadout, profile)
	if err != nil {
		t.Fatalf("sanitizeGameServerLoadout returned error: %v", err)
	}
	if result.Tag != "rwd_tag_s1_vrml_s1_finalist" {
		t.Errorf("owned VRML tag was stripped; got %q, want %q", result.Tag, "rwd_tag_s1_vrml_s1_finalist")
	}
}

func TestSanitizeGameServerLoadout_AllowsDefaultItem(t *testing.T) {
	profile := &EVRProfile{account: &api.Account{Wallet: "{}"}}

	loadout := evr.DefaultCosmeticLoadout()
	loadout.Tag = "rwd_tag_s1_a_secondary"

	result, err := sanitizeGameServerLoadout(loadout, profile)
	if err != nil {
		t.Fatalf("sanitizeGameServerLoadout returned error: %v", err)
	}
	if result.Tag != "rwd_tag_s1_a_secondary" {
		t.Errorf("default tag was altered; got %q, want %q", result.Tag, "rwd_tag_s1_a_secondary")
	}
}
