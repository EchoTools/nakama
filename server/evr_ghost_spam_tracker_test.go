package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRemoveTriggeredPlayers_NilTriggered(t *testing.T) {
	list := []evr.EvrId{evr.EvrId{PlatformCode: 1, AccountId: 100}, evr.EvrId{PlatformCode: 1, AccountId: 200}}
	result := removeTriggeredPlayers(list, nil)
	assert.Equal(t, list, result)
}

func TestRemoveTriggeredPlayers_EmptyTriggered(t *testing.T) {
	list := []evr.EvrId{evr.EvrId{PlatformCode: 1, AccountId: 100}, evr.EvrId{PlatformCode: 1, AccountId: 200}}
	result := removeTriggeredPlayers(list, map[string]bool{})
	assert.Equal(t, list, result)
}

func TestRemoveTriggeredPlayers_RemovesTarget(t *testing.T) {
	target := evr.EvrId{PlatformCode: 1, AccountId: 100}
	keep := evr.EvrId{PlatformCode: 1, AccountId: 200}
	list := []evr.EvrId{target, keep}
	triggered := map[string]bool{target.String(): true}

	result := removeTriggeredPlayers(list, triggered)
	require.Len(t, result, 1)
	assert.Equal(t, keep, result[0])
}

func TestRemoveTriggeredPlayers_RemovesMultipleTargets(t *testing.T) {
	a := evr.EvrId{PlatformCode: 1, AccountId: 100}
	b := evr.EvrId{PlatformCode: 1, AccountId: 200}
	c := evr.EvrId{PlatformCode: 1, AccountId: 300}
	list := []evr.EvrId{a, b, c}
	triggered := map[string]bool{a.String(): true, c.String(): true}

	result := removeTriggeredPlayers(list, triggered)
	require.Len(t, result, 1)
	assert.Equal(t, b, result[0])
}

func TestRemoveTriggeredPlayers_EmptyList(t *testing.T) {
	result := removeTriggeredPlayers(nil, map[string]bool{"foo": true})
	assert.Empty(t, result)
}

func TestFindNewlyAddedPlayers(t *testing.T) {
	a := evr.EvrId{PlatformCode: 1, AccountId: 100}
	b := evr.EvrId{PlatformCode: 1, AccountId: 200}
	c := evr.EvrId{PlatformCode: 1, AccountId: 300}

	t.Run("empty old list", func(t *testing.T) {
		result := findNewlyAddedPlayers(nil, []evr.EvrId{a, b})
		assert.Len(t, result, 2)
	})

	t.Run("no new players", func(t *testing.T) {
		result := findNewlyAddedPlayers([]evr.EvrId{a, b}, []evr.EvrId{a, b})
		assert.Empty(t, result)
	})

	t.Run("one new player", func(t *testing.T) {
		result := findNewlyAddedPlayers([]evr.EvrId{a}, []evr.EvrId{a, b})
		require.Len(t, result, 1)
		assert.Equal(t, b, result[0])
	})

	t.Run("multiple new players", func(t *testing.T) {
		result := findNewlyAddedPlayers([]evr.EvrId{a}, []evr.EvrId{a, b, c})
		require.Len(t, result, 2)
	})
}
