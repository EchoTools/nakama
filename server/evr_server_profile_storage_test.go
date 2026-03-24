package server

import (
	"encoding/json"
	"testing"
)

func TestFilterUnlocksToEquipped_KeepsEquippedItems(t *testing.T) {
	profile := map[string]any{
		"displayname": "TestPlayer",
		"unlocks": map[string]map[string]bool{
			"arena": {
				"rwd_chassis_body_s11_a": true,
				"rwd_booster_default":    true,
				"rwd_bracer_secret":      true, // NOT equipped — should be filtered
				"rwd_banner_rare":        true, // NOT equipped — should be filtered
			},
			"combat": {
				"rwd_chassis_body_s11_a": true,
				"some_combat_only_item":  true, // NOT equipped — should be filtered
			},
		},
		"loadout": map[string]any{
			"instances": map[string]any{
				"unified": map[string]any{
					"slots": map[string]string{
						"chassis": "rwd_chassis_body_s11_a",
						"booster": "rwd_booster_default",
						"decal":   "decal_default",
					},
				},
			},
			"number": 42,
		},
		"stats": map[string]any{},
	}

	data, err := json.Marshal(profile)
	if err != nil {
		t.Fatal(err)
	}

	filtered, err := filterUnlocksToEquipped(data)
	if err != nil {
		t.Fatal(err)
	}

	// Parse the result.
	var result map[string]json.RawMessage
	if err := json.Unmarshal(filtered, &result); err != nil {
		t.Fatal(err)
	}

	var unlocks map[string]map[string]bool
	if err := json.Unmarshal(result["unlocks"], &unlocks); err != nil {
		t.Fatal(err)
	}

	// Equipped items should be present.
	if !unlocks["arena"]["rwd_chassis_body_s11_a"] {
		t.Error("Expected rwd_chassis_body_s11_a to be in arena unlocks (equipped)")
	}
	if !unlocks["arena"]["rwd_booster_default"] {
		t.Error("Expected rwd_booster_default to be in arena unlocks (equipped)")
	}

	// Non-equipped items should be stripped.
	if unlocks["arena"]["rwd_bracer_secret"] {
		t.Error("rwd_bracer_secret should have been filtered (not equipped)")
	}
	if unlocks["arena"]["rwd_banner_rare"] {
		t.Error("rwd_banner_rare should have been filtered (not equipped)")
	}

	// Combat group: only the equipped item should remain.
	if !unlocks["combat"]["rwd_chassis_body_s11_a"] {
		t.Error("Expected rwd_chassis_body_s11_a in combat unlocks (equipped)")
	}
	if unlocks["combat"]["some_combat_only_item"] {
		t.Error("some_combat_only_item should have been filtered (not equipped)")
	}

	// decal_default is equipped but not in unlocks — that's OK, we only filter what exists.

	// Verify loadout is preserved.
	var loadout struct {
		Instances struct {
			Unified struct {
				Slots map[string]string `json:"slots"`
			} `json:"unified"`
		} `json:"instances"`
		Number int `json:"number"`
	}
	if err := json.Unmarshal(result["loadout"], &loadout); err != nil {
		t.Fatal(err)
	}
	if loadout.Instances.Unified.Slots["chassis"] != "rwd_chassis_body_s11_a" {
		t.Error("Loadout chassis should be preserved")
	}
	if loadout.Number != 42 {
		t.Error("Loadout number should be preserved")
	}
}

func TestFilterUnlocksToEquipped_EmptyUnlocks(t *testing.T) {
	profile := map[string]any{
		"unlocks": map[string]map[string]bool{},
		"loadout": map[string]any{
			"instances": map[string]any{
				"unified": map[string]any{
					"slots": map[string]string{"chassis": "rwd_chassis_body_s11_a"},
				},
			},
		},
	}
	data, _ := json.Marshal(profile)

	filtered, err := filterUnlocksToEquipped(data)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(filtered, &result); err != nil {
		t.Fatal(err)
	}

	var unlocks map[string]map[string]bool
	if err := json.Unmarshal(result["unlocks"], &unlocks); err != nil {
		t.Fatal(err)
	}
	if len(unlocks) != 0 {
		t.Errorf("Expected empty unlocks, got %v", unlocks)
	}
}

func TestFilterUnlocksToEquipped_PreservesOtherFields(t *testing.T) {
	profile := map[string]any{
		"displayname":   "TestPlayer",
		"xplatformid":   "OVR-ORG-1234",
		"stats":         map[string]any{"wins": 10},
		"purchasedcombat": 1,
		"unlocks": map[string]map[string]bool{
			"arena": {"item_a": true, "item_b": true},
		},
		"loadout": map[string]any{
			"instances": map[string]any{
				"unified": map[string]any{
					"slots": map[string]string{"chassis": "item_a"},
				},
			},
		},
	}
	data, _ := json.Marshal(profile)

	filtered, err := filterUnlocksToEquipped(data)
	if err != nil {
		t.Fatal(err)
	}

	var result map[string]json.RawMessage
	if err := json.Unmarshal(filtered, &result); err != nil {
		t.Fatal(err)
	}

	// All non-unlocks fields should be present and unchanged.
	if string(result["displayname"]) != `"TestPlayer"` {
		t.Errorf("displayname changed: %s", result["displayname"])
	}
	if string(result["xplatformid"]) != `"OVR-ORG-1234"` {
		t.Errorf("xplatformid changed: %s", result["xplatformid"])
	}

	var stats map[string]int
	if err := json.Unmarshal(result["stats"], &stats); err != nil {
		t.Fatal(err)
	}
	if stats["wins"] != 10 {
		t.Errorf("stats changed: %v", stats)
	}
}

func TestFilterUnlocksToEquipped_FalseValuesFiltered(t *testing.T) {
	// Items with false values should always be stripped, even if equipped.
	profile := map[string]any{
		"unlocks": map[string]map[string]bool{
			"arena": {
				"item_equipped_true":  true,
				"item_equipped_false": false, // false value — should be stripped
			},
		},
		"loadout": map[string]any{
			"instances": map[string]any{
				"unified": map[string]any{
					"slots": map[string]string{
						"chassis": "item_equipped_true",
						"booster": "item_equipped_false",
					},
				},
			},
		},
	}
	data, _ := json.Marshal(profile)

	filtered, err := filterUnlocksToEquipped(data)
	if err != nil {
		t.Fatal(err)
	}

	var result struct {
		Unlocks map[string]map[string]bool `json:"unlocks"`
	}
	if err := json.Unmarshal(filtered, &result); err != nil {
		t.Fatal(err)
	}

	if !result.Unlocks["arena"]["item_equipped_true"] {
		t.Error("item_equipped_true should be kept")
	}
	if result.Unlocks["arena"]["item_equipped_false"] {
		t.Error("item_equipped_false should be stripped (value is false)")
	}
}
