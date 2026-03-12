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

	matches, available, exists := selectRegionMatches(labels, " US-west ")
	if !exists {
		t.Fatal("expected region to exist")
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 match, got %d", len(matches))
	}
	if got := normalizeRegionCode(matches[0].GameServer.LocationRegionCode(false, false)); got != "us-west" {
		t.Fatalf("expected matched region us-west, got %q", got)
	}

	if len(available) != 3 {
		t.Fatalf("expected 3 available regions, got %d", len(available))
	}
	if available[0] != "ca-ontario" || available[1] != "us-east" || available[2] != "us-west" {
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
	if len(available) != 1 || available[0] != "us-east" {
		t.Fatalf("expected available regions [us-east], got %#v", available)
	}
}
