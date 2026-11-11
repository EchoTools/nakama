package evr

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestGameSettingsQuestCombatUnlocked(t *testing.T) {
	defaults := map[string]*GameSettings{
		"NewDefaultGameSettings": NewDefaultGameSettings(),
		"NewGameSettings":        NewGameSettings(ConfigData{}, "live", true, "", false, false, false, false, false),
	}
	for name, gs := range defaults {
		b, err := json.Marshal(gs)
		if err != nil {
			t.Fatalf("%s: marshal: %v", name, err)
		}
		if !strings.Contains(string(b), `"quest_combat_unlocked":true`) {
			t.Errorf("%s: login settings JSON lacks quest_combat_unlocked:true: %s", name, b)
		}
	}
}
