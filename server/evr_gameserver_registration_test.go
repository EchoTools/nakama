package server

import (
	"testing"

	rtapi "buf.build/gen/go/echotools/nevr-api/protocolbuffers/go/gameservice/v1"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

// urlSessionParams is the state a server's WebSocket URL leaves behind, after
// session_ws.go has merged required features into supported features.
func urlSessionParams() *SessionParameters {
	return &SessionParameters{
		serverRegions:       []string{"us-east"},
		serverGuilds:        []string{"111"},
		serverTags:          []string{"urltag"},
		supportedFeatures:   []string{"urlfeat", "urlreq"},
		requiredFeatures:    []string{"urlreq"},
		defaultRegion:       "url-default",
		geoHashPrecision:    8,
		externalServerAddr:  "203.0.113.5:6792",
		enableAllRemoteLogs: false,
		relayOutgoing:       false,
	}
}

func TestApplyRegistrationMessage_UnsetKeepsURLParams(t *testing.T) {
	p := urlSessionParams()
	applyRegistrationMessage(p, &rtapi.GameServerRegistrationMessage{})

	assert.Equal(t, []string{"us-east"}, p.serverRegions)
	assert.Equal(t, []string{"111"}, p.serverGuilds)
	assert.Equal(t, []string{"urltag"}, p.serverTags)
	assert.Equal(t, []string{"urlfeat", "urlreq"}, p.supportedFeatures)
	assert.Equal(t, []string{"urlreq"}, p.requiredFeatures)
	assert.Equal(t, "url-default", p.defaultRegion)
	assert.Equal(t, 8, p.geoHashPrecision)
	assert.Equal(t, "203.0.113.5:6792", p.externalServerAddr)
	assert.False(t, p.enableAllRemoteLogs)
	assert.False(t, p.relayOutgoing)
}

func TestApplyRegistrationMessage_SetValuesWinOverURL(t *testing.T) {
	p := urlSessionParams()
	applyRegistrationMessage(p, &rtapi.GameServerRegistrationMessage{
		Regions:       []string{"eu-west"},
		Guilds:        []string{"any"},
		Tags:          []string{"msgtag"},
		Features:      []string{"msgfeat"},
		Requires:      []string{"msgreq"},
		DefaultRegion: "eu-west",
		GeoPrecision:  5,
		ServerAddress: "198.51.100.9:6792",
		Debug:         true,
		Verbose:       true,
	})

	assert.Equal(t, []string{"eu-west"}, p.serverRegions)
	assert.Equal(t, []string{"any"}, p.serverGuilds)
	assert.Equal(t, []string{"msgtag"}, p.serverTags)
	assert.Equal(t, []string{"msgfeat", "msgreq"}, p.supportedFeatures)
	assert.Equal(t, []string{"msgreq"}, p.requiredFeatures)
	assert.Equal(t, "eu-west", p.defaultRegion)
	assert.Equal(t, 5, p.geoHashPrecision)
	assert.Equal(t, "198.51.100.9:6792", p.externalServerAddr)
	assert.True(t, p.enableAllRemoteLogs)
	assert.True(t, p.relayOutgoing)
}

func TestApplyRegistrationMessage_RequiresOnlyKeepsURLFeaturesSupported(t *testing.T) {
	p := urlSessionParams()
	applyRegistrationMessage(p, &rtapi.GameServerRegistrationMessage{Requires: []string{"msgreq"}})

	assert.Equal(t, []string{"msgreq"}, p.requiredFeatures)
	assert.Equal(t, []string{"msgreq", "urlfeat", "urlreq"}, p.supportedFeatures)
}

func TestApplyRegistrationMessage_InvalidValuesAreSanitized(t *testing.T) {
	p := urlSessionParams()
	applyRegistrationMessage(p, &rtapi.GameServerRegistrationMessage{
		Regions:       []string{"bad region!", "eu-west"}, // first item fails regionPattern
		Guilds:        []string{"abc"},                    // guildPattern wants digits or "any"
		Tags:          []string{"ok", "no,comma"},         // comma is not in tagsPattern
		DefaultRegion: "bad region",                       // fails regionPattern: URL value stands
		GeoPrecision:  99,
	})

	assert.Equal(t, []string{"eu-west"}, p.serverRegions)
	assert.Equal(t, []string{"111"}, p.serverGuilds, "every guild item failed, so the URL value stands")
	assert.Equal(t, []string{"ok"}, p.serverTags)
	assert.Equal(t, "url-default", p.defaultRegion)
	assert.Equal(t, 12, p.geoHashPrecision, "geo_precision is clamped to 0-12")
}

func TestApplyRegistrationMessage_NegativeGeoPrecisionClampsToZero(t *testing.T) {
	p := urlSessionParams()
	applyRegistrationMessage(p, &rtapi.GameServerRegistrationMessage{GeoPrecision: -3})
	assert.Equal(t, 0, p.geoHashPrecision)
}

func TestInstalledLevelsFromRegistration(t *testing.T) {
	arena := evr.ToSymbol("mpl_arena_a")
	fission := evr.ToSymbol("mpl_combat_fission")

	assert.Nil(t, installedLevelsFromRegistration(nil), "a server that sends no levels reports none")
	assert.Equal(t, []evr.Symbol{arena, fission}, installedLevelsFromRegistration([]uint64{uint64(arena), uint64(fission)}))
}

func TestGameServerPresenceCanHostLevel(t *testing.T) {
	arena := evr.ToSymbol("mpl_arena_a")
	fission := evr.ToSymbol("mpl_combat_fission")

	tests := []struct {
		name      string
		installed []evr.Symbol
		level     evr.Symbol
		want      bool
	}{
		{name: "server has the level", installed: []evr.Symbol{fission, arena}, level: arena, want: true},
		{name: "server lacks the level", installed: []evr.Symbol{fission}, level: arena, want: false},
		{name: "server reports no levels", installed: nil, level: arena, want: true},
		{name: "server reports an empty list", installed: []evr.Symbol{}, level: arena, want: true},
		{name: "match has no level (unspecified)", installed: []evr.Symbol{fission}, level: evr.LevelUnspecified, want: true},
		{name: "match has no level (unloaded)", installed: []evr.Symbol{fission}, level: evr.LevelUnloaded, want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			g := &GameServerPresence{InstalledLevels: tc.installed}
			assert.Equal(t, tc.want, g.CanHostLevel(tc.level))
		})
	}
}
