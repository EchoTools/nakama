package server

import (
	"context"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// enforceJoinSuspension checks whether the joining player is suspended from the
// match's guild. This runs at the seat — the chokepoint ALL join paths pass
// through — so no path can bypass enforcement regardless of how the player
// arrived (matchmaker, backfill, setnextmatch, spectate, social priority join).
//
// Fail-closed: if enforcement state cannot be loaded, the join is rejected.
func enforceJoinSuspension(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule, ggRegistry *GuildGroupRegistry, label *MatchLabel, session Session) error {
	groupID := label.GetGroupID()
	if groupID.IsNil() {
		return nil
	}

	params, ok := LoadParams(session.Context())
	if !ok {
		// No session params → can't verify → fail closed.
		return NewLobbyError(KickedFromLobbyGroup, "unable to verify suspension status")
	}

	userID := session.UserID().String()
	groupIDStr := groupID.String()

	// 1. Role-based suspension (guild role check — instant, no storage read).
	gg := ggRegistry.Get(groupIDStr)
	if gg != nil && gg.IsSuspended(userID, &params.xpID) {
		return NewLobbyError(KickedFromLobbyGroup, roleSuspensionUserMessage(gg.Name()))
	}

	// 2. Enforcement journal suspension — fresh read from storage to catch
	//    suspensions issued after login.
	journals, err := EnforcementJournalsLoad(ctx, nk, params.enforcementUserIDs)
	if err != nil {
		logger.Error("seat enforcement: failed to load journals",
			zap.String("uid", userID), zap.String("gid", groupIDStr), zap.Error(err))
		return NewLobbyError(KickedFromLobbyGroup, "unable to verify suspension status")
	}

	enforcements, err := CheckEnforcementSuspensions(journals, ggRegistry.InheritanceByParentGroupID())
	if err != nil {
		logger.Error("seat enforcement: failed to check suspensions",
			zap.String("uid", userID), zap.String("gid", groupIDStr), zap.Error(err))
		return NewLobbyError(KickedFromLobbyGroup, "unable to verify suspension status")
	}

	recordsByMode, ok := enforcements[groupIDStr]
	if !ok {
		return nil
	}

	record, ok := recordsByMode[label.Mode]
	if !ok || record.IsExpired() {
		return nil
	}

	// Alt suspension: respect the guild's per-guild toggle.
	if record.UserID != userID {
		if params.ignoreDisabledAlternates {
			return nil
		}
		if gg != nil && !gg.RejectPlayersWithSuspendedAlternates {
			return nil
		}
	}

	// Build the rejection message (same format as lobbyAuthorize).
	reason := record.UserNoticeText
	if record.SuspensionExcludesPrivateLobbies() {
		reason = "Public Access Denied: " + reason
	}
	expires := fmt.Sprintf(" [exp: %s]", FormatDuration(time.Until(record.Expiry)))
	const maxMessageLength = 64
	if len(reason)+len(expires) > maxMessageLength {
		reason = reason[:maxMessageLength-len(expires)-3] + "..."
	}
	reason += expires

	logger.Warn("seat enforcement: rejected suspended player",
		zap.String("uid", userID),
		zap.String("gid", groupIDStr),
		zap.String("mode", label.Mode.String()),
		zap.Bool("is_alt", record.UserID != userID),
	)

	return NewLobbyError(KickedFromLobbyGroup, reason)
}
