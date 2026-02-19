// gen_cosmetic_hashes generates a JSON file mapping every cosmetic item string
// (from ArenaUnlocks and CombatUnlocks in core_account.go) to its int64 CSymbol64 hash value.
//
// Usage: go run ./server/evr/gen_cosmetic_hashes/ -o cosmetic_hashes.json
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"reflect"
	"sort"
	"strings"

	"github.com/heroiclabs/nakama/v3/server/evr"
)

func main() {
	output := flag.String("o", "cosmetic_hashes.json", "Output JSON file path")
	flag.Parse()

	// Collect all JSON tag values from ArenaUnlocks and CombatUnlocks via reflection.
	cosmetics := collectCosmeticStrings()

	// Compute hash for each string.
	result := make(map[string]int64, len(cosmetics))
	for _, s := range cosmetics {
		sym := evr.ToSymbol(s)
		result[s] = int64(sym) //nolint:gosec -- Symbol is a CRC hash; signed truncation is intentional and expected
	}

	// Write JSON.
	data, err := marshalSorted(result)
	if err != nil {
		log.Fatalf("failed to marshal JSON: %v", err)
	}

	if err := os.WriteFile(*output, data, 0644); err != nil {
		log.Fatalf("failed to write %s: %v", *output, err)
	}

	fmt.Printf("Wrote %d cosmetic hashes to %s\n", len(result), *output)
}

// collectCosmeticStrings returns every unique non-empty json tag string from
// ArenaUnlocks and CombatUnlocks.
func collectCosmeticStrings() []string {
	seen := make(map[string]struct{})
	for _, v := range []any{evr.ArenaUnlocks{}, evr.CombatUnlocks{}} {
		collectFromStruct(reflect.TypeOf(v), seen)
	}
	out := make([]string, 0, len(seen))
	for s := range seen {
		out = append(out, s)
	}
	sort.Strings(out)
	return out
}

func collectFromStruct(t reflect.Type, seen map[string]struct{}) {
	for t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return
	}
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		tag := field.Tag.Get("json")
		if tag == "" || tag == "-" {
			collectFromStruct(field.Type, seen)
			continue
		}
		name := strings.Split(tag, ",")[0]
		if name != "" && name != "-" {
			seen[name] = struct{}{}
		}
		// Recurse into nested structs.
		collectFromStruct(field.Type, seen)
	}
}

// marshalSorted produces JSON with keys in sorted order.
func marshalSorted(m map[string]int64) ([]byte, error) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf strings.Builder
	buf.WriteString("{\n")
	for i, k := range keys {
		line, err := json.Marshal(k)
		if err != nil {
			return nil, err
		}
		if i < len(keys)-1 {
			fmt.Fprintf(&buf, "  %s: %d,\n", line, m[k])
		} else {
			fmt.Fprintf(&buf, "  %s: %d\n", line, m[k])
		}
	}
	buf.WriteString("}\n")
	return []byte(buf.String()), nil
}
