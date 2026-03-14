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

func TestBuildGroupUpdateInput_DescriptionOnlyLeavesOthersEmpty(t *testing.T) {
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
	// Unchanged fields should be empty to avoid overwriting concurrent changes.
	if got.Name != "" {
		t.Fatalf("expected name to be empty (unchanged), got %q", got.Name)
	}
	if got.AvatarURL != "" {
		t.Fatalf("expected avatar to be empty (unchanged), got %q", got.AvatarURL)
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

func TestBuildGroupUpdateInput_NilInputs(t *testing.T) {
	if got := buildGroupUpdateInput(nil, &GuildGroupUpdateRequest{}); got != nil {
		t.Fatal("expected nil for nil GuildGroup")
	}
	if got := buildGroupUpdateInput(&GuildGroup{}, &GuildGroupUpdateRequest{}); got != nil {
		t.Fatal("expected nil for nil Group inside GuildGroup")
	}
	gg := &GuildGroup{Group: &api.Group{Open: wrapperspb.Bool(false)}}
	if got := buildGroupUpdateInput(gg, nil); got != nil {
		t.Fatal("expected nil for nil request")
	}
}

func TestBuildGroupUpdateInput_AllFieldsChanged(t *testing.T) {
	gg := &GuildGroup{Group: &api.Group{
		Name:        "Old Name",
		LangTag:     GuildGroupLangTag,
		Description: "Old Desc",
		AvatarUrl:   "https://example.com/old.png",
		Open:        wrapperspb.Bool(false),
	}}

	open := true
	req := &GuildGroupUpdateRequest{
		GroupID:     "gid",
		Name:        "New Name",
		Description: "New Desc",
		AvatarURL:   "https://example.com/new.png",
		Open:        &open,
	}
	got := buildGroupUpdateInput(gg, req)
	if got == nil {
		t.Fatal("expected update input")
	}
	if got.Name != "New Name" {
		t.Fatalf("expected name %q, got %q", "New Name", got.Name)
	}
	if got.Description != "New Desc" {
		t.Fatalf("expected description %q, got %q", "New Desc", got.Description)
	}
	if got.AvatarURL != "https://example.com/new.png" {
		t.Fatalf("expected avatar %q, got %q", "https://example.com/new.png", got.AvatarURL)
	}
	if got.Open != true {
		t.Fatalf("expected open true, got %v", got.Open)
	}
}
