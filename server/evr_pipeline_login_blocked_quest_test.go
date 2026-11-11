package server

import (
	"errors"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// blockedQuestSettings returns service settings that block the given nevr-runtime versions.
func blockedQuestSettings(versions ...string) *ServiceSettingsData {
	return &ServiceSettingsData{
		BlockedQuestVersions:        versions,
		BlockedQuestVersionsMessage: "blocked for test",
	}
}

// TestBlockedQuestVersionRefusal is the decision alone: which logins are refused, which are admitted, and
// whether the permissions lookup is made at all. The lookup must run only for a login that matches the list,
// so every other login pays nothing.
func TestBlockedQuestVersionRefusal(t *testing.T) {
	t.Parallel()

	errLookup := errors.New("permissions lookup failed")

	tests := []struct {
		name            string
		blocked         []string
		build           evr.BuildNumber
		nevrVersion     string
		exempt          bool
		exemptErr       error
		wantRefused     bool
		wantErr         error
		wantLookupCalls int
	}{
		{
			name:            "quest on a listed version is refused",
			blocked:         []string{"6.14.2"},
			build:           evr.StandaloneBuildNumber,
			nevrVersion:     "6.14.2",
			wantRefused:     true,
			wantLookupCalls: 1,
		},
		{
			name:            "quest reporting the version with a leading v is refused",
			blocked:         []string{"6.14.2"},
			build:           evr.StandaloneBuildNumber,
			nevrVersion:     "v6.14.2",
			wantRefused:     true,
			wantLookupCalls: 1,
		},
		{
			name:            "a list entry with a leading v matches a bare version",
			blocked:         []string{"v6.14.2"},
			build:           evr.StandaloneBuildNumber,
			nevrVersion:     "6.14.2",
			wantRefused:     true,
			wantLookupCalls: 1,
		},
		{
			name:        "quest on an unlisted version is admitted",
			blocked:     []string{"6.14.2"},
			build:       evr.StandaloneBuildNumber,
			nevrVersion: "6.14.1",
		},
		{
			name:        "pcvr on a listed version is admitted",
			blocked:     []string{"6.14.2"},
			build:       evr.PCVRBuild,
			nevrVersion: "6.14.2",
		},
		{
			name:            "global operator on a listed quest version is admitted",
			blocked:         []string{"6.14.2"},
			build:           evr.StandaloneBuildNumber,
			nevrVersion:     "6.14.2",
			exempt:          true,
			wantLookupCalls: 1,
		},
		{
			name:            "lookup failure on a listed quest version refuses the login",
			blocked:         []string{"6.14.2"},
			build:           evr.StandaloneBuildNumber,
			nevrVersion:     "6.14.2",
			exemptErr:       errLookup,
			wantErr:         errLookup,
			wantLookupCalls: 1,
		},
		{
			name:        "no nevr_identity is admitted",
			blocked:     []string{"6.14.2"},
			build:       evr.StandaloneBuildNumber,
			nevrVersion: "",
		},
		{
			name:        "an empty list entry does not match an absent nevr_identity",
			blocked:     []string{""},
			build:       evr.StandaloneBuildNumber,
			nevrVersion: "",
		},
		{
			name:        "an empty list admits everything",
			blocked:     nil,
			build:       evr.StandaloneBuildNumber,
			nevrVersion: "6.14.2",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			lookups := 0
			exempt := func() (bool, error) {
				lookups++
				return tc.exempt, tc.exemptErr
			}

			err := blockedQuestVersionRefusal(zap.NewNop(), blockedQuestSettings(tc.blocked...), "XPID", "203.0.113.7", "uid", tc.build, tc.nevrVersion, exempt)

			require.Equal(t, tc.wantLookupCalls, lookups, "permissions lookups")
			switch {
			case tc.wantErr != nil:
				require.ErrorIs(t, err, tc.wantErr)
			case tc.wantRefused:
				var refused BlockedQuestVersionError
				require.True(t, errors.As(err, &refused), "want BlockedQuestVersionError, got %v", err)
				require.Equal(t, "blocked for test", refused.Error())
			default:
				require.NoError(t, err)
			}
		})
	}
}

// A refused login writes one Info record naming the xpid, client IP, user and the version the client declared.
func TestBlockedQuestVersionRefusalLogsRecord(t *testing.T) {
	t.Parallel()

	core, logs := observer.New(zapcore.InfoLevel)
	noExempt := func() (bool, error) { return false, nil }

	err := blockedQuestVersionRefusal(zap.New(core), blockedQuestSettings("6.14.2"), "XPID-token", "203.0.113.7", "user-id", evr.StandaloneBuildNumber, "v6.14.2", noExempt)
	require.Error(t, err)

	entries := logs.FilterMessage("Rejected login: blocked Quest version").All()
	require.Len(t, entries, 1, "exactly one record for one refused login")
	require.Equal(t, zapcore.InfoLevel, entries[0].Level)
	fields := entries[0].ContextMap()
	require.Equal(t, "XPID-token", fields["xpid"])
	require.Equal(t, "203.0.113.7", fields["client_ip"])
	require.Equal(t, "user-id", fields["uid"])
	require.Equal(t, "v6.14.2", fields["nevr_version"])
}
