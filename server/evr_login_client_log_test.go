package server

import (
	"strings"
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// The "Login client" record is where a login's declared build, app and headset are read from production
// logs. Each case drives the real logging helper through an observer logger and asserts the record that
// comes out, so a field dropped or renamed fails here rather than in a log nobody is reading.
func TestLoginClientRecordCarriesDeclaredBuildAppAndHeadset(t *testing.T) {
	nevr := &evr.NevrIdentity{Version: "4.2.0", Commit: "abc", Build: "v4.2.0-5-gabc", BuildType: "Release"}

	for _, tc := range []struct {
		name    string
		payload evr.LoginProfile
		want    map[string]any
	}{
		{
			name: "quest",
			payload: evr.LoginProfile{
				BuildNumber:  evr.StandaloneBuildNumber,
				AppId:        QuestAppId,
				SystemInfo:   evr.SystemInfo{HeadsetType: "Oculus Quest 2"},
				NevrIdentity: nevr,
			},
			want: map[string]any{
				"buildversion":         int64(630783),
				"appid":                QuestAppId,
				"headset_type":         "Oculus Quest 2",
				"nevr_runtime_build":   "v4.2.0-5-gabc",
				"nevr_runtime_version": "4.2.0",
				"nevr_runtime_commit":  "abc",
			},
		},
		{
			name: "pc",
			payload: evr.LoginProfile{
				BuildNumber:  evr.PCVRBuild,
				AppId:        PcvrAppId,
				SystemInfo:   evr.SystemInfo{HeadsetType: "Oculus Rift S"},
				NevrIdentity: nevr,
			},
			want: map[string]any{
				"buildversion":         int64(631547),
				"appid":                PcvrAppId,
				"headset_type":         "Oculus Rift S",
				"nevr_runtime_build":   "v4.2.0-5-gabc",
				"nevr_runtime_version": "4.2.0",
				"nevr_runtime_commit":  "abc",
			},
		},
		{
			name: "stock client declares no nevr_identity",
			payload: evr.LoginProfile{
				BuildNumber: evr.PCVRBuild,
				AppId:       PcvrAppId,
				SystemInfo:  evr.SystemInfo{HeadsetType: "Oculus Rift S"},
			},
			want: map[string]any{
				"buildversion":         int64(631547),
				"appid":                PcvrAppId,
				"headset_type":         "Oculus Rift S",
				"nevr_runtime_build":   "",
				"nevr_runtime_version": "",
				"nevr_runtime_commit":  "",
			},
		},
		{
			name: "headset_type is capped",
			payload: evr.LoginProfile{
				BuildNumber: evr.StandaloneBuildNumber,
				AppId:       QuestAppId,
				SystemInfo:  evr.SystemInfo{HeadsetType: strings.Repeat("h", 200)},
			},
			want: map[string]any{
				"headset_type": strings.Repeat("h", maxLoginHeadsetLogChars),
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			core, logs := observer.New(zapcore.InfoLevel)
			payload := tc.payload
			logLoginClient(zap.New(core), &SessionParameters{loginPayload: &payload})

			entries := logs.All()
			require.Len(t, entries, 1, "one Login client record per login")
			require.Equal(t, "Login client", entries[0].Message)
			require.Equal(t, zapcore.InfoLevel, entries[0].Level)

			fields := entries[0].ContextMap()
			for k, v := range tc.want {
				require.Equal(t, v, fields[k], "field %q", k)
			}
		})
	}
}
