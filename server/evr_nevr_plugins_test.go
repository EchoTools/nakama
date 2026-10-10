package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

func paramsWithPlugins(plugins ...evr.NevrPlugin) *SessionParameters {
	return &SessionParameters{loginPayload: &evr.LoginProfile{NevrPlugins: plugins}}
}

// What the login line says about the client's plugins (nevr-runtime#60): the ones that loaded as
// name@ver, and the ones that were enabled and did not, with the reason and whether they were required.
// A disabled plugin is configured and in neither list.
func TestPluginsLoadedAndFailed(t *testing.T) {
	p := paramsWithPlugins(
		evr.NevrPlugin{Name: "example", File: "example.dll", Enabled: true, Loaded: true, Version: "1.0.0", API: 2, Caps: 5},
		evr.NevrPlugin{Name: "bare", File: "bare.dll", Enabled: true, Loaded: true},
		evr.NevrPlugin{Name: "session-unlocker", File: "su.dll", Enabled: true, Required: true, Error: "LoadLibrary failed (126)"},
		evr.NevrPlugin{Name: "optional", File: "o.dll", Enabled: true, Error: "init returned 3"},
		evr.NevrPlugin{Name: "silent", File: "s.dll", Enabled: true},
		evr.NevrPlugin{Name: "off", File: "off.dll", Enabled: false},
	)
	require.Len(t, p.Plugins(), 6)
	require.Equal(t, []string{"example@1.0.0", "bare"}, p.PluginsLoaded())
	require.Equal(t, []string{
		"session-unlocker (required): LoadLibrary failed (126)",
		"optional: init returned 3",
		"silent: (no reason given)",
	}, p.PluginsFailed())
}

func TestPluginsOfASessionWithNoReportAreEmptyNotNil(t *testing.T) {
	for name, p := range map[string]*SessionParameters{
		"no login payload": {},
		"stock client":     {loginPayload: &evr.LoginProfile{}},
		"empty report":     paramsWithPlugins(),
	} {
		t.Run(name, func(t *testing.T) {
			require.Empty(t, p.Plugins())
			require.NotNil(t, p.PluginsLoaded(), "a log field of nil would be null, not []")
			require.NotNil(t, p.PluginsFailed())
			require.Empty(t, p.PluginsLoaded())
			require.Empty(t, p.PluginsFailed())
		})
	}
}

// The "Login client" line carries the plugin report next to the build and social level.
func TestLoginClientLogFields(t *testing.T) {
	p := &SessionParameters{loginPayload: &evr.LoginProfile{
		NevrIdentity: &evr.NevrIdentity{Build: "v4.0.0-145-g09a0ed6"},
		NevrSocial:   1,
		NevrPlugins: evr.NevrPlugins{
			{Name: "example", Enabled: true, Loaded: true, Version: "1.0.0"},
			{Name: "session-unlocker", Enabled: true, Required: true, Error: "LoadLibrary failed (126)"},
			{Name: "off"},
		},
	}}
	enc := zapcore.NewMapObjectEncoder()
	for _, f := range loginClientLogFields(p) {
		f.AddTo(enc)
	}
	require.Equal(t, map[string]any{
		"nevr_runtime_build": "v4.0.0-145-g09a0ed6",
		"nevr_social":        int64(1),
		"plugins_configured": int64(3),
		"plugins_loaded":     []any{"example@1.0.0"},
		"plugins_failed":     []any{"session-unlocker (required): LoadLibrary failed (126)"},
	}, enc.Fields)

	stock := zapcore.NewMapObjectEncoder()
	for _, f := range loginClientLogFields(&SessionParameters{loginPayload: &evr.LoginProfile{}}) {
		f.AddTo(stock)
	}
	require.Equal(t, int64(0), stock.Fields["plugins_configured"])
	require.Equal(t, []any{}, stock.Fields["plugins_loaded"])
	require.Equal(t, []any{}, stock.Fields["plugins_failed"])
}
