package evr

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The fixtures under testdata/remotelog are real POST_MATCH_* remote logs from
// combat matches, taken read-only from the RemoteLogs journal with the userid
// and session uuid zeroed. They carry no player names.
//
// json.Unmarshal drops any key that has no matching field, without an error.
// That is how every Combat* stat the game sent was lost before these structs
// named them, so the tests below fail on a dropped key, not just a wrong value.

func readRemoteLogFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "remotelog", name))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return data
}

// fixtureStatsObject returns the numeric object under key in a raw remote log,
// exactly as the game sent it.
func fixtureStatsObject(t *testing.T, raw []byte, key string) map[string]float64 {
	t.Helper()
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	var stats map[string]float64
	if err := json.Unmarshal(top[key], &stats); err != nil {
		t.Fatalf("decode fixture %q object: %v", key, err)
	}
	if len(stats) == 0 {
		t.Fatalf("fixture %q object is empty", key)
	}
	return stats
}
