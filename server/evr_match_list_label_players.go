package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// visibleLabel is a match label whose player list has been through
// filterLabelPlayers. The list label builders take this type, so a label cannot
// reach a list caller without passing the filter.
type visibleLabel struct {
	*MatchLabel
}

func isModeratorRole(team TeamIndex) bool {
	return int(team) == evr.TeamModerator
}

// hasModeratorEntries reports whether the label lists a moderator, as a player
// row or as a team alignment.
func (l *MatchLabel) hasModeratorEntries() bool {
	for i := range l.Players {
		if isModeratorRole(l.Players[i].Team) {
			return true
		}
	}
	for _, role := range l.TeamAlignments {
		if role == evr.TeamModerator {
			return true
		}
	}
	return false
}

// filterLabelPlayers is the one place the players a list caller may see are
// decided. With showModerators false it returns a copy of the label without the
// moderator rows and team alignments, and with Size and ReservationCount reduced
// by the seats those rows held. The label passed in is never modified.
func filterLabelPlayers(label *MatchLabel, showModerators bool) visibleLabel {
	if label == nil || showModerators || !label.hasModeratorEntries() {
		return visibleLabel{label}
	}

	out := *label
	out.Players = make([]PlayerInfo, 0, len(label.Players))

	// A moderator that is connected holds one row for its presence, plus one more
	// for a reservation it also holds, which is how seatedInLabel reads the label.
	rowsBySession := make(map[string]int)
	connected := make(map[string]bool)
	for i := range label.Players {
		p := label.Players[i]
		if !isModeratorRole(p.Team) {
			out.Players = append(out.Players, p)
			continue
		}
		key := p.SessionID
		if key == "" {
			key = fmt.Sprintf("row-%d", i)
		}
		rowsBySession[key]++
		if !p.IsReservation {
			connected[key] = true
		}
	}
	for key, rows := range rowsBySession {
		if connected[key] || rows > 1 {
			out.Size--
			out.ReservationCount -= rows - 1
		} else {
			out.ReservationCount -= rows
		}
	}
	out.Size = max(out.Size, 0)
	out.ReservationCount = max(out.ReservationCount, 0)

	if label.TeamAlignments != nil {
		out.TeamAlignments = make(map[string]int, len(label.TeamAlignments))
		for userID, role := range label.TeamAlignments {
			if role != evr.TeamModerator {
				out.TeamAlignments[userID] = role
			}
		}
		if len(out.TeamAlignments) == 0 {
			out.TeamAlignments = nil
		}
	}
	return visibleLabel{&out}
}

// sizeAfterFilter is the match's presence count for a caller whose label went
// through filterLabelPlayers: the count the match reports, less what the filter
// took out of the label.
func sizeAfterFilter(matchSize int32, original *MatchLabel, filtered visibleLabel) int32 {
	if original == nil || filtered.MatchLabel == nil {
		return matchSize
	}
	return max(matchSize-int32(original.Size-filtered.Size), 0)
}

// moderatorViewer answers, per guild, whether a caller may see the moderators of
// that guild's matches: a global operator, an auditor of the guild, or one of its
// admins. Answers are kept for the life of the viewer, so a list of matches asks
// the guild admin check once per guild.
type moderatorViewer struct {
	userID           string
	isGlobalOperator bool
	loadGuild        func(groupID string) *GuildGroup
	isGuildAdmin     func(gg *GuildGroup) bool
	answers          map[string]bool
}

func newModeratorViewer(userID string, isGlobalOperator bool, loadGuild func(groupID string) *GuildGroup, isGuildAdmin func(gg *GuildGroup) bool) *moderatorViewer {
	return &moderatorViewer{
		userID:           userID,
		isGlobalOperator: isGlobalOperator,
		loadGuild:        loadGuild,
		isGuildAdmin:     isGuildAdmin,
		answers:          make(map[string]bool),
	}
}

// showsModeratorsOf reports whether the label may be shown with its moderators.
// A label that lists none needs no answer, so no guild is loaded for it.
func (v *moderatorViewer) showsModeratorsOf(label *MatchLabel) bool {
	if label == nil || !label.hasModeratorEntries() {
		return true
	}
	return v.canSee(matchGroupIDString(label))
}

func (v *moderatorViewer) canSee(groupID string) bool {
	if v == nil || v.userID == "" {
		return false
	}
	if v.isGlobalOperator {
		return true
	}
	if answer, ok := v.answers[groupID]; ok {
		return answer
	}
	answer := false
	if gg := v.loadGuild(groupID); gg != nil {
		answer = gg.IsAuditor(v.userID) || v.isGuildAdmin(gg)
	}
	v.answers[groupID] = answer
	return answer
}

// newRPCModeratorViewer builds the viewer for a caller of an RPC: operator status
// from the request's permissions, else the global operators group; guilds loaded on
// first use. A lookup that fails counts as "not allowed".
func newRPCModeratorViewer(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, userID string) *moderatorViewer {
	if userID == "" {
		return nil
	}
	isOperator := false
	if perms := PermissionsFromContext(ctx); perms != nil {
		isOperator = perms.IsGlobalOperator
	} else if db != nil {
		if member, err := CheckSystemGroupMembership(ctx, db, userID, GroupGlobalOperators); err == nil {
			isOperator = member
		}
	}
	return newModeratorViewer(userID, isOperator,
		func(groupID string) *GuildGroup {
			if groupID == "" {
				return nil
			}
			gg, err := GuildGroupLoad(ctx, nk, groupID)
			if err != nil {
				return nil
			}
			return gg
		},
		func(gg *GuildGroup) bool {
			return canManageGuild(ctx, logger, nk, guildPermissionSourceOrNil(), gg, userID)
		})
}

// matchGroupIDString is the label's guild id, or "" when it has none.
func matchGroupIDString(label *MatchLabel) string {
	if label == nil || label.GroupID == nil {
		return ""
	}
	return label.GroupID.String()
}

// labelJSONForViewer returns raw, a match label as the match handler published
// it, with the moderators removed unless viewer may see them. A label with
// nothing to remove is returned as it came.
func labelJSONForViewer(raw string, viewer *moderatorViewer) (string, error) {
	label := &MatchLabel{}
	if err := json.Unmarshal([]byte(raw), label); err != nil {
		return "", fmt.Errorf("failed to unmarshal match label: %w", err)
	}
	if !label.hasModeratorEntries() {
		return raw, nil
	}
	visible := filterLabelPlayers(label, viewer.showsModeratorsOf(label))
	if visible.MatchLabel == label {
		return raw, nil
	}
	data, err := json.Marshal(visible.MatchLabel)
	if err != nil {
		return "", fmt.Errorf("failed to marshal match label: %w", err)
	}
	return string(data), nil
}
