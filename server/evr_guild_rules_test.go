package server

import (
	"strings"
	"testing"
)

func TestGuildRulesLine(t *testing.T) {
	cases := []struct {
		name, desc, rules, want string
	}{
		{"set", "Desc", "No cheating", "Desc\nNo cheating"},
		{"unset omits the rules line", "Desc", "", "Desc"},
		{"legacy placeholder is not shown", "Desc", legacyRulesPlaceholder, "Desc"},
		{"whitespace only is unset", "Desc", " \n\t", "Desc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := guildRulesLine(c.desc, c.rules); got != c.want {
				t.Fatalf("guildRulesLine(%q,%q) = %q, want %q", c.desc, c.rules, got, c.want)
			}
		})
	}
}

func TestMigrateRulesText(t *testing.T) {
	// A guild that had a real #rules topic keeps it.
	s := &GuildGroupState{RulesText: "Be kind."}
	migrateRulesText(s)
	if s.RulesText != "Be kind." {
		t.Fatalf("real rules must survive migration, got %q", s.RulesText)
	}
	// A guild that only ever had the placeholder is cleared.
	s = &GuildGroupState{RulesText: legacyRulesPlaceholder}
	migrateRulesText(s)
	if s.RulesText != "" {
		t.Fatalf("placeholder must be cleared, got %q", s.RulesText)
	}
}

func TestApplyRulesTextUpdate(t *testing.T) {
	gg := &GuildGroup{State: &GuildGroupState{RulesText: "old"}}

	// Absent field: untouched.
	if changed, err := applyRulesTextUpdate(gg, &GuildGroupUpdateRequest{GroupID: "g"}); err != nil || changed || gg.State.RulesText != "old" {
		t.Fatalf("absent rules_text: changed=%v err=%v text=%q", changed, err, gg.State.RulesText)
	}

	// Set.
	txt := "1. Be kind"
	if changed, err := applyRulesTextUpdate(gg, &GuildGroupUpdateRequest{RulesText: &txt}); err != nil || !changed || gg.State.RulesText != txt {
		t.Fatalf("set: changed=%v err=%v text=%q", changed, err, gg.State.RulesText)
	}

	// Explicit empty string clears.
	empty := ""
	if changed, err := applyRulesTextUpdate(gg, &GuildGroupUpdateRequest{RulesText: &empty}); err != nil || !changed || gg.State.RulesText != "" {
		t.Fatalf("clear: changed=%v err=%v text=%q", changed, err, gg.State.RulesText)
	}

	// Oversized is rejected and leaves state alone.
	big := strings.Repeat("x", maxGuildRulesTextLen+1)
	gg.State.RulesText = "keep"
	if _, err := applyRulesTextUpdate(gg, &GuildGroupUpdateRequest{RulesText: &big}); err == nil || gg.State.RulesText != "keep" {
		t.Fatalf("oversized must error without mutating: err=%v text=%q", err, gg.State.RulesText)
	}
}
