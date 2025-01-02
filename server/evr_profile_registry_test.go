package server

import (
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/google/go-cmp/cmp"
	"go.uber.org/zap"
)

func createTestDiscordGoSession(t *testing.T, logger *zap.Logger) *discordgo.Session {
	return &discordgo.Session{}
}

func createTestProfileRegistry(t *testing.T, logger *zap.Logger) (*ProfileCache, error) {
	runtimeLogger := NewRuntimeGoLogger(logger)

	tracker := &testTracker{}
	db := NewDB(t)
	nk := NewRuntimeGoNakamaModule(logger, db, nil, cfg, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil)

	profileRegistry := NewProfileRegistry(nk, db, runtimeLogger, tracker, metrics)

	return profileRegistry, nil
}

func TestProfileRegistry(t *testing.T) {
	consoleLogger := loggerForTest(t)

	profileRegistry, err := createTestProfileRegistry(t, consoleLogger)
	if err != nil {
		t.Fatalf("error creating test match registry: %v", err)
	}
	_ = profileRegistry
}

func TestConvertWalletToCosmetics(t *testing.T) {
	tests := []struct {
		name     string
		wallet   map[string]int64
		expected map[string]map[string]bool
	}{
		{
			name:     "Empty wallet",
			wallet:   map[string]int64{},
			expected: map[string]map[string]bool{},
		},
		{
			name: "Single cosmetic item",
			wallet: map[string]int64{
				"cosmetics:arena:rwd_tag_s1_vrml_s1": 1,
			},
			expected: map[string]map[string]bool{
				"arena": {
					"rwd_tag_s1_vrml_s1": true,
				},
			},
		},
		{
			name: "Multiple cosmetic items",
			wallet: map[string]int64{
				"cosmetics:arena:rwd_tag_s1_vrml_s1": 1,
				"cosmetics:arena:rwd_tag_s1_vrml_s2": 1,
			},
			expected: map[string]map[string]bool{
				"arena": {
					"rwd_tag_s1_vrml_s1": true,
					"rwd_tag_s1_vrml_s2": true,
				},
			},
		},
		{
			name: "Non-cosmetic items",
			wallet: map[string]int64{
				"noncosmetic:item1":                  1,
				"cosmetics:arena:rwd_tag_s1_vrml_s2": 1,
			},
			expected: map[string]map[string]bool{
				"arena": {
					"rwd_tag_s1_vrml_s2": true,
				},
			},
		},
		{
			name: "Cosmetic item with zero quantity",
			wallet: map[string]int64{
				"cosmetics:arena:rwd_tag_s1_vrml_s1": 0,
			},
			expected: map[string]map[string]bool{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			unlocks := make(map[string]map[string]bool)
			walletToCosmetics(tt.wallet, unlocks)
			if cmp.Diff(unlocks, tt.expected) != "" {
				wantGotDiff(t, tt.expected, unlocks)
			}
		})
	}
}

func wantGotDiff(t *testing.T, want, got interface{}) {
	t.Helper()
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mismatch (-want +got):\n%s", diff)
	}
}

type testProfileRegistry struct {
	ProfileCache
	mockLoadFn func(userID uuid.UUID) (*GameProfileData, bool)
}

func (r *testProfileRegistry) Load(userID uuid.UUID) (*GameProfileData, bool) {
	return r.mockLoadFn(userID)
}
