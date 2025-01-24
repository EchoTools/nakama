package server

import (
	"testing"
)

func TestNormalizeHeadsetType(t *testing.T) {
	tests := []struct {
		name     string
		headset  string
		expected string
	}{
		{"Empty headset", "", "Unknown"},
		{"Known headset Meta Quest 1", "Quest", "Meta Quest 1"},
		{"Known headset Meta Quest 2", "Quest 2", "Meta Quest 2"},
		{"Known headset Meta Quest Pro", "Quest Pro", "Meta Quest Pro"},
		{"Known headset Meta Quest 3", "Quest 3", "Meta Quest 3"},
		{"Known headset Meta Quest 3S", "Quest 3S", "Meta Quest 3S"},
		{"Known headset Meta Quest Pro (Link)", "Quest Pro (Link)", "Meta Quest Pro (Link)"},
		{"Known headset Meta Quest 3 (Link)", "Quest 3 (Link)", "Meta Quest 3 (Link)"},
		{"Known headset Meta Quest 3S (Link)", "Quest 3S (Link)", "Meta Quest 3S (Link)"},
		{"Known headset Meta Rift CV1", "Oculus Rift CV1", "Meta Rift CV1"},
		{"Known headset Meta Rift S", "Oculus Rift S", "Meta Rift S"},
		{"Known headset HTC Vive Elite", "Vive Elite", "HTC Vive Elite"},
		{"Known headset HTC Vive MV", "Vive MV", "HTC Vive MV"},
		{"Known headset HTC Vive MV", "Vive. MV", "HTC Vive MV"},
		{"Known headset Bigscreen Beyond", "Beyond", "Bigscreen Beyond"},
		{"Known headset Valve Index", "Index", "Valve Index"},
		{"Known headset Potato Potato 4K", "Potato VR", "Potato Potato 4K"},
		{"Unknown headset", "Unknown Headset", "Unknown Headset"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := normalizeHeadsetType(tt.headset)
			if result != tt.expected {
				t.Errorf("normalizeHeadsetType(%s) = %s; expected %s", tt.headset, result, tt.expected)
			}
		})
	}
}
