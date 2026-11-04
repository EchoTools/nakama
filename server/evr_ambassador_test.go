package server

import (
	"encoding/json"
	"strings"
	"testing"
)

// The old /ambassador command and its settings are retired; see the
// new-player ambassador spec. These fail against the code before retirement.

func TestOldAmbassadorCommandRetired(t *testing.T) {
	for _, c := range mainSlashCommands {
		if c.Name == "ambassador" {
			t.Fatal("mainSlashCommands still registers /ambassador")
		}
	}
}

func TestServiceSettings_NoRetiredAmbassadorKeys(t *testing.T) {
	data := &ServiceSettingsData{}
	FixDefaultServiceSettings(nil, data)
	b, err := json.Marshal(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"enable_ambassador_program",
		"ambassador_mu_reduction",
		"ambassador_cooldown_matches",
		"ambassador_min_games_played",
		"ambassador_min_mu",
	} {
		if strings.Contains(string(b), `"`+key+`"`) {
			t.Errorf("service settings still carry retired key %q", key)
		}
	}
}
