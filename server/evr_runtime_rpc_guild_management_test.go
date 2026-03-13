package server

import (
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestBuildGroupUpdateInput_NoChanges(t *testing.T) {
	gg := &GuildGroup{Group: &api.Group{
		Name:        "Guild Name",
		LangTag:     GuildGroupLangTag,
		Description: "Guild Description",
		AvatarUrl:   "https://example.com/avatar.png",
		Open:        wrapperspb.Bool(true),
	}}

	req := &GuildGroupUpdateRequest{GroupID: "gid"}
	if got := buildGroupUpdateInput(gg, req); got != nil {
		t.Fatalf("expected nil update input when no update fields are set, got %+v", got)
	}
}

func TestBuildGroupUpdateInput_DescriptionOnlyPreservesOpen(t *testing.T) {
	gg := &GuildGroup{Group: &api.Group{
		Name:        "Guild Name",
		LangTag:     GuildGroupLangTag,
		Description: "Guild Description",
		AvatarUrl:   "https://example.com/avatar.png",
		Open:        wrapperspb.Bool(true),
	}}

	req := &GuildGroupUpdateRequest{GroupID: "gid", Description: "Updated Description"}
	got := buildGroupUpdateInput(gg, req)
	if got == nil {
		t.Fatal("expected update input")
	}
	if got.Description != "Updated Description" {
		t.Fatalf("expected description to be updated, got %q", got.Description)
	}
	if got.Open != true {
		t.Fatalf("expected open to remain true, got %v", got.Open)
	}
	if got.Name != "Guild Name" {
		t.Fatalf("expected name to be preserved, got %q", got.Name)
	}
}

func TestBuildGroupUpdateInput_ExplicitOpenFalse(t *testing.T) {
	gg := &GuildGroup{Group: &api.Group{
		Name:        "Guild Name",
		LangTag:     GuildGroupLangTag,
		Description: "Guild Description",
		AvatarUrl:   "https://example.com/avatar.png",
		Open:        wrapperspb.Bool(true),
	}}

	open := false
	req := &GuildGroupUpdateRequest{GroupID: "gid", Open: &open}
	got := buildGroupUpdateInput(gg, req)
	if got == nil {
		t.Fatal("expected update input")
	}
	if got.Open != false {
		t.Fatalf("expected open to be false, got %v", got.Open)
	}
}
