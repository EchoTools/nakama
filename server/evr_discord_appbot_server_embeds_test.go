package server

import (
	"testing"

	"github.com/gofrs/uuid/v5"
)

func TestNormalizeRegionCode(t *testing.T) {
	if got := normalizeRegionCode("  Us-East  "); got != "us-east" {
		t.Fatalf("expected us-east, got %q", got)
	}
}

func TestSelectRegionMatches(t *testing.T) {
	gid := uuid.Must(uuid.NewV4())
	mk := func(countryCode, region string) *MatchLabel {
		return &MatchLabel{
			GroupID: &gid,
			GameServer: &GameServerPresence{
				CountryCode: countryCode,
				Region:      region,
			},
		}
	}

	labels := []*MatchLabel{
		mk("US", "East"),
		mk("US", "West"),
		mk("CA", "Ontario"),
		{GroupID: &gid}, // no gameserver, ignored
	}

	requestedRegion := normalizeRegionCode(labels[0].GameServer.LocationRegionCode(false, false))
	matches, available, exists := selectRegionMatches(labels, requestedRegion)
	if !exists {
		t.Fatal("expected region to exist")
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches for requested region, got %d", len(matches))
	}
	for _, match := range matches {
		if got := normalizeRegionCode(match.GameServer.LocationRegionCode(false, false)); got != requestedRegion {
			t.Fatalf("expected matched region %s, got %q", requestedRegion, got)
		}
	}

	if len(available) != 2 {
		t.Fatalf("expected 2 available regions, got %d", len(available))
	}
	if available[0] != "ca" || available[1] != requestedRegion {
		t.Fatalf("unexpected available regions order/content: %#v", available)
	}
}

func TestSelectRegionMatchesUnknownRegion(t *testing.T) {
	gid := uuid.Must(uuid.NewV4())
	labels := []*MatchLabel{{
		GroupID: &gid,
		GameServer: &GameServerPresence{
			CountryCode: "US",
			Region:      "East",
		},
	}}

	matches, available, exists := selectRegionMatches(labels, "ap-south")
	if exists {
		t.Fatal("expected region not to exist")
	}
	if len(matches) != 0 {
		t.Fatalf("expected 0 matches, got %d", len(matches))
	}
	if len(available) != 1 || available[0] != "us" {
		t.Fatalf("expected available regions [us], got %#v", available)
	}
}

func TestSelectRegionMatchesAllRegions(t *testing.T) {
	gid := uuid.Must(uuid.NewV4())
	labels := []*MatchLabel{
		{
			GroupID: &gid,
			GameServer: &GameServerPresence{
				CountryCode: "US",
				Region:      "East",
			},
		},
		{
			GroupID: &gid,
			GameServer: &GameServerPresence{
				CountryCode: "DE",
				Region:      "Frankfurt",
			},
		},
	}

	matches, available, exists := selectRegionMatches(labels, "all")
	if !exists {
		t.Fatal("expected all regions mode to exist")
	}
	if len(matches) != 2 {
		t.Fatalf("expected 2 matches, got %d", len(matches))
	}
	if len(available) != 2 {
		t.Fatalf("expected 2 available regions, got %d", len(available))
	}
}
