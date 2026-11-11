package server

import (
	"fmt"
	"strings"
)

// legacyRulesPlaceholder is what the removed #rules-channel scrape wrote into
// every guild that had no channel named "rules". It is recognised only so it can
// be erased (see migrateRulesText); nothing writes it any more.
const legacyRulesPlaceholder = "No #rules channel found. Please create the channel and set the topic to the rules."

// maxGuildRulesTextLen bounds the rules a guild admin can store. The old source
// was a channel topic (1024 chars); this leaves room for website-authored text
// while keeping the in-game channel-info message bounded.
const maxGuildRulesTextLen = 4096

// guildRulesLine builds the in-game rules text: the guild description, then the
// rules when the guild has set any. An unset (or legacy-placeholder) value adds
// no rules line at all.
func guildRulesLine(description, rulesText string) string {
	if strings.TrimSpace(rulesText) == "" || rulesText == legacyRulesPlaceholder {
		return description
	}
	return description + "\n" + rulesText
}

// migrateRulesText clears the legacy placeholder and leaves any real text (a
// topic copied from a #rules channel by an earlier sync) untouched, so existing
// guilds keep their current rules. It reports whether it changed anything.
func migrateRulesText(s *GuildGroupState) bool {
	if s != nil && s.RulesText == legacyRulesPlaceholder {
		s.RulesText = ""
		return true
	}
	return false
}

// applyRulesTextUpdate applies req.RulesText to the guild's state. A nil field
// means "not provided"; an empty string clears the rules.
func applyRulesTextUpdate(gg *GuildGroup, req *GuildGroupUpdateRequest) (bool, error) {
	if gg == nil || gg.State == nil || req == nil || req.RulesText == nil {
		return false, nil
	}
	if len(*req.RulesText) > maxGuildRulesTextLen {
		return false, fmt.Errorf("rules_text exceeds %d bytes", maxGuildRulesTextLen)
	}
	gg.State.RulesText = *req.RulesText
	return true, nil
}
