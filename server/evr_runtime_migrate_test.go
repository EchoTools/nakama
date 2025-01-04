package server

import (
	"reflect"
	"testing"
)

func TestWalletMigrateVRML(t *testing.T) {
	tests := []struct {
		name     string
		wallet   map[string]int64
		expected map[string]int64
	}{
		{
			name:     "Empty wallet",
			wallet:   map[string]int64{},
			expected: map[string]int64{},
		},
		{
			name: "Single season with positive balance",
			wallet: map[string]int64{
				"VRML Season 1": 1,
			},
			expected: map[string]int64{
				"VRML Season 1":             -1,
				"rwd_tag_s1_vrml_s1":        1,
				"rwd_medal_s1_vrml_s1_user": 1,
			},
		},
		{
			name: "Multiple seasons with positive balance",
			wallet: map[string]int64{
				"VRML Season 1": 1,
				"VRML Season 2": 1,
			},
			expected: map[string]int64{
				"VRML Season 1":             -1,
				"rwd_tag_s1_vrml_s1":        1,
				"rwd_medal_s1_vrml_s1_user": 1,
				"VRML Season 2":             -1,
				"rwd_tag_s1_vrml_s2":        1,
				"rwd_medal_s1_vrml_s2":      1,
			},
		},
		{
			name: "Season with zero balance",
			wallet: map[string]int64{
				"VRML Season 1": 0,
			},
			expected: map[string]int64{},
		},
		{
			name: "Season with negative balance",
			wallet: map[string]int64{
				"VRML Season 1": -1,
			},
			expected: map[string]int64{},
		},
		{
			name: "Season with existing rewards",
			wallet: map[string]int64{
				"VRML Season 1":             1,
				"rwd_tag_s1_vrml_s1":        1,
				"rwd_medal_s1_vrml_s1_user": 1,
			},
			expected: map[string]int64{
				"VRML Season 1": -1,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := MigrationUserVRMLEntitlementsToWallet{}.migrateWallet(tt.wallet)
			if !reflect.DeepEqual(result, tt.expected) {
				t.Errorf("WalletMigrateVRML() = %v, expected %v", result, tt.expected)
			}
		})
	}
}
