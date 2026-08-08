package server

import (
	"slices"
	"testing"
)

// A realistic PC fingerprint: eight "::"-joined components, none of them empty.
const pcFingerprint = "Windows::Wired::NVIDIA GeForce RTX 4070::AMD Ryzen 7 5800X::8::16::34359738368::12884901888"

// What every account that logs in without SystemInfo emits. Not a rare
// fingerprint -- a bucket shared by all of them. This is the string that would
// have linked every profile-less account to every other one.
const degenerateFingerprint = "Unknown::::::::0::0::0::0"

// A stock Quest 3, which describes a large share of the player base.
const commodityFingerprint = "Meta Quest 3::Wifi::::::4::8::8589934592::0"

func newTestCGNATDetector(t *testing.T) *CGNATDetector {
	t.Helper()
	d := &CGNATDetector{}
	d.UpdateSettings(CGNATSettings{
		CommodityProfilePrefixes: []string{"Meta Quest 2::", "Meta Quest 3::", "Meta Quest 3S::"},
	})
	return d
}

func TestIsMachineFingerprint(t *testing.T) {
	detector := newTestCGNATDetector(t)

	tests := []struct {
		name string
		item string
		want bool
	}{
		{
			name: "SpecificPCProfile",
			item: pcFingerprint,
			want: true,
		},
		{
			// The bucket that nearly shipped with #551. If this ever returns
			// true, every account with no SystemInfo is a machine match for
			// every other one, and the login gate locks out the lot.
			name: "DegenerateProfileIsNotAFingerprint",
			item: degenerateFingerprint,
			want: false,
		},
		{
			name: "CommodityHeadsetIsNotAFingerprint",
			item: commodityFingerprint,
			want: false,
		},
		{
			name: "ClientIPIsNotAFingerprint",
			item: "203.0.113.7",
			want: false,
		},
		{
			name: "HMDSerialIsNotAFingerprint",
			item: "1PASH9AB12345",
			want: false,
		},
		{
			name: "XPIDIsNotAFingerprint",
			item: "OVR-ORG-3963667097037078",
			want: false,
		},
		{
			name: "EmptyIsNotAFingerprint",
			item: "",
			want: false,
		},
		{
			// Right component count, but it is the wrong shape entirely.
			name: "WrongComponentCount",
			item: "Windows::Wired::GPU",
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isMachineFingerprint(tt.item, detector); got != tt.want {
				t.Errorf("isMachineFingerprint(%q) = %v, want %v", tt.item, got, tt.want)
			}
		})
	}
}

// TestIsMachineFingerprint_NilDetectorNarrows pins that losing the commodity
// classifier makes this match LESS, not more.
//
// The opposite -- treating an unavailable classifier as "nothing is commodity"
// -- would turn every Quest profile into a machine match the moment settings
// failed to load, and this gate rejects logins. An unavailable input must never
// widen what an enforcement rule matches.
func TestIsMachineFingerprint_NilDetectorNarrows(t *testing.T) {
	detector := newTestCGNATDetector(t)

	if !isMachineFingerprint(pcFingerprint, nil) {
		t.Error("a specific PC profile should still be a fingerprint with no detector")
	}

	// With a detector the commodity profile is excluded. Without one the
	// commodity check cannot run -- so this documents the residual honestly
	// rather than pretending it is covered.
	if isMachineFingerprint(commodityFingerprint, detector) {
		t.Error("commodity profile matched despite a detector that classifies it")
	}

	// The degenerate profile must stay excluded even with no detector at all,
	// because that exclusion does not depend on settings.
	if isMachineFingerprint(degenerateFingerprint, nil) {
		t.Error("degenerate profile matched with no detector; the bucket guard must not depend on settings")
	}
}

func TestMachineMatchedAlts(t *testing.T) {
	detector := newTestCGNATDetector(t)

	const (
		machineAlt   = "11111111-1111-1111-1111-111111111111"
		ipOnlyAlt    = "22222222-2222-2222-2222-222222222222"
		commodityAlt = "33333333-3333-3333-3333-333333333333"
		mixedAlt     = "44444444-4444-4444-4444-444444444444"
	)

	history := &LoginHistory{
		AlternateMatches: map[string][]*AlternateSearchMatch{
			machineAlt: {{OtherUserID: machineAlt, Items: []string{pcFingerprint}}},
			// Linked only by an IP. filterStrongAlts would keep this one on a
			// non-CGNAT address; this must not.
			ipOnlyAlt: {{OtherUserID: ipOnlyAlt, Items: []string{"203.0.113.7"}}},
			// A shared stock headset profile is not evidence of a shared machine.
			commodityAlt: {{OtherUserID: commodityAlt, Items: []string{commodityFingerprint}}},
			// Several matches, only one of which is the machine.
			mixedAlt: {
				{OtherUserID: mixedAlt, Items: []string{"203.0.113.9"}},
				{OtherUserID: mixedAlt, Items: []string{"1PASH9AB12345", pcFingerprint}},
			},
		},
	}

	altIDs := []string{machineAlt, ipOnlyAlt, commodityAlt, mixedAlt}
	got := machineMatchedAlts(history, altIDs, detector)

	want := []string{machineAlt, mixedAlt}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("machineMatchedAlts() = %v, want %v", got, want)
	}
}

func TestMachineMatchedAlts_Empty(t *testing.T) {
	detector := newTestCGNATDetector(t)

	if got := machineMatchedAlts(nil, []string{"x"}, detector); got != nil {
		t.Errorf("machineMatchedAlts(nil history) = %v, want nil", got)
	}
	if got := machineMatchedAlts(&LoginHistory{}, nil, detector); got != nil {
		t.Errorf("machineMatchedAlts(no ids) = %v, want nil", got)
	}
	// An ID with no recorded match must not be reported as a machine match.
	history := &LoginHistory{AlternateMatches: map[string][]*AlternateSearchMatch{}}
	if got := machineMatchedAlts(history, []string{"unknown-id"}, detector); got != nil {
		t.Errorf("machineMatchedAlts(unknown id) = %v, want nil", got)
	}
}
