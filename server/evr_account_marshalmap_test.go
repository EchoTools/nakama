package server

import (
	"bytes"
	"encoding/json"
	"math"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
)

// TestEVRProfileMarshalMap_PreservesInt64Precision pins the invariant that the
// account-metadata half of EVRProfileUpdate's write carries the SAME data as the
// storage half.
//
// EVRProfileUpdate marshals the profile twice: once with json.Marshal (exact) for
// the storage row, and once with MarshalMap() for the account metadata. MarshalMap
// used to round-trip through map[string]any, which decodes every JSON number as
// float64 — silently truncating the 64-bit EchoVR item hashes in NewUnlocks past
// 2^53. The two halves of a single "atomic" write then committed different data,
// and BuildEVRProfileFromAccount (the fallback used when the storage row is
// missing) served the corrupted values.
func TestEVRProfileMarshalMap_PreservesInt64Precision(t *testing.T) {
	const bigHash int64 = 1234567890123456789 // > 2^53, not representable as float64

	p := EVRProfile{
		NewUnlocks: []int64{bigHash, 42},
	}

	m, err := p.MarshalMap()
	require.NoError(t, err)

	// The metadata map is handed to nk.MultiUpdate, which serializes it with
	// json.Marshal before it ever reaches the database. Reproduce that step.
	encoded, err := json.Marshal(m)
	require.NoError(t, err)

	var round EVRProfile
	require.NoError(t, json.Unmarshal(encoded, &round))

	require.Equal(t, []int64{bigHash, 42}, round.NewUnlocks,
		"MarshalMap must not lose int64 precision; account metadata and storage value must agree")
}

// TestEVRProfileMarshalMap_Int64Boundaries covers the edges around the float64
// mantissa limit and both signs, not just the headline value.
func TestEVRProfileMarshalMap_Int64Boundaries(t *testing.T) {
	for _, tc := range []struct {
		name string
		v    int64
	}{
		{"max_int64", math.MaxInt64},
		{"min_int64", math.MinInt64},
		{"two_pow_53", 1 << 53},
		{"two_pow_53_plus_one", (1 << 53) + 1},
		{"negative_two_pow_53_minus_one", -((1 << 53) + 1)},
		{"zero", 0},
		{"small", 7},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := EVRProfile{NewUnlocks: []int64{tc.v}}

			m, err := p.MarshalMap()
			require.NoError(t, err)

			encoded, err := json.Marshal(m)
			require.NoError(t, err)

			var round EVRProfile
			require.NoError(t, json.Unmarshal(encoded, &round))
			require.Equal(t, []int64{tc.v}, round.NewUnlocks)
		})
	}
}

// TestEVRProfileMarshalMap_AgreesWithStorageValue is the general form of the bug:
// EVRProfileUpdate writes json.Marshal(md) to storage and MarshalMap(md) to the
// account metadata in one MultiUpdate. Whatever the profile contains, those two
// encodings must describe the same document — otherwise an "atomic" write commits
// two different states. This also covers the uint64 POI-version fields, which have
// the same float64 truncation exposure as NewUnlocks.
func TestEVRProfileMarshalMap_AgreesWithStorageValue(t *testing.T) {
	p := EVRProfile{
		ActiveGroupID: "6f6b1b0a-0000-4000-8000-000000000001",
		TeamName:      "Test Team",
		InGameNames: map[string]GroupInGameName{
			"6f6b1b0a-0000-4000-8000-000000000001": {
				GroupID:     "6f6b1b0a-0000-4000-8000-000000000001",
				DisplayName: "Player One",
				IsOverride:  true,
			},
		},
		NewUnlocks: []int64{
			1234567890123456789,
			math.MaxInt64,
			-9007199254740993,
			42,
		},
		CustomizationPOIs: &evr.Customization{
			BattlePassSeasonPoiVersion: math.MaxUint64,
			NewUnlocksPoiVersion:       9007199254740993,
			StoreEntryPoiVersion:       3,
			ClearNewUnlocksVersion:     1,
		},
		LegalConsents: evr.LegalConsents{EulaVersion: 1, GameAdminVersion: 1},
	}

	storageValue, err := json.Marshal(p)
	require.NoError(t, err)

	m, err := p.MarshalMap()
	require.NoError(t, err)
	metadataValue, err := json.Marshal(m)
	require.NoError(t, err)

	// Compare as generic documents so key ordering does not matter, but with
	// UseNumber so numeric literals are compared textually rather than as float64
	// (a float64 comparison would happily call the corrupted values equal).
	require.Equal(t,
		decodeWithNumbers(t, storageValue),
		decodeWithNumbers(t, metadataValue),
		"account metadata half and storage half of the same write must encode identical data")
}

func decodeWithNumbers(t *testing.T, b []byte) any {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.UseNumber()
	var v any
	require.NoError(t, dec.Decode(&v))
	return v
}
