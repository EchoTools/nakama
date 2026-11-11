package server

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"go.uber.org/zap"
)

// ownerFallbackLabel names the owner when their username cannot be resolved.
// The DM must always say something the player can read; it never says "unknown".
const ownerFallbackLabel = "another player"

// displayNameNotice is one "this name is taken" DM owed to a player.
type displayNameNotice struct {
	DisplayName string
	OwnerUserID string
}

// displayNameNoticePlayer identifies the player a notice is addressed to.
type displayNameNoticePlayer struct {
	UserID    string
	DiscordID string
	Username  string // the name the player falls back to
}

// displayNameNoticeDeps are the side effects sendDisplayNameNotices performs,
// injected so the notice path can be tested without a live Discord session.
type displayNameNoticeDeps struct {
	// OwnerUsername returns the owner's username, or "" if it cannot be resolved.
	OwnerUsername func(ctx context.Context, ownerUserID string) string
	SendDM        func(ctx context.Context, discordID, message string) error
}

// pruneDisplayNamesOwnedByOthers removes from profile every per-guild in-game
// name that another account owns, and returns the notices owed for it.
//
// A name is pruned from every guild it appears in, but is owed ONE notice: at
// most one per (player, name, owner), ever. history remembers the notices
// already sent, so the same nick on the next login, or in a second guild on this
// one, owes nothing. The notice is recorded in history here, before the send,
// because the caller stores history once, right after this; that makes the
// promise at-most-once -- a send that then fails is logged, not retried.
// Nothing is recorded when notify is off, so enabling it later still notifies.
//
// It is the login loop's prune step, extracted so the decision can be tested
// directly; initializeSession needs a live session and is not reachable from a
// unit test.
func pruneDisplayNamesOwnedByOthers(profile *EVRProfile, history *DisplayNameHistory, ownerMap map[string][]string, notify bool, now time.Time) (ownedByOthers []string, notices []displayNameNotice) {
	if history == nil {
		history = NewDisplayNameHistory()
	}
	for _, dn := range profile.DisplayNamesByGroupID() {
		ownerIDs, ok := ownerMap[dn]
		if !ok || slices.Contains(ownerIDs, profile.ID()) {
			continue
		}
		// This display name is owned by someone else.
		ownedByOthers = append(ownedByOthers, dn)
		for gID, gn := range profile.DisplayNamesByGroupID() {
			if strings.EqualFold(gn, dn) {
				profile.DeleteGroupDisplayName(gID)
			}
		}
		if notify && !history.InUseNoticeSent(dn, ownerIDs[0]) {
			history.RecordInUseNotice(dn, ownerIDs[0], now)
			notices = append(notices, displayNameNotice{DisplayName: dn, OwnerUserID: ownerIDs[0]})
		}
	}
	return ownedByOthers, notices
}

// displayNameInUseMessage is the DM text. It names the owner by username rather
// than by mention: a mention renders as "@unknown-user" for a recipient whose
// client cannot resolve the owner (they share no server), which told the player
// nothing they could act on (#777).
func displayNameInUseMessage(displayName, ownerUsername, fallbackDisplayName string) string {
	if ownerUsername == "" {
		ownerUsername = ownerFallbackLabel
	}
	return fmt.Sprintf("The display name `%s` is already in use/reserved by `%s`. Your in-game name will be your username: `%s`", displayName, ownerUsername, fallbackDisplayName)
}

// sendDisplayNameNotices sends each notice as a DM to player, and logs every
// attempt: Info for a send, Warn for a failure.
func sendDisplayNameNotices(ctx context.Context, logger *zap.Logger, deps displayNameNoticeDeps, player displayNameNoticePlayer, notices []displayNameNotice) {
	for _, n := range notices {
		message := displayNameInUseMessage(n.DisplayName, deps.OwnerUsername(ctx, n.OwnerUserID), player.Username)
		fields := []zap.Field{
			zap.String("user_id", player.UserID),
			zap.String("owner_user_id", n.OwnerUserID),
			zap.String("display_name", n.DisplayName),
			zap.String("trigger", "login"),
		}
		if err := deps.SendDM(ctx, player.DiscordID, message); err != nil {
			if IsDiscordErrorCode(err, discordgo.ErrCodeCannotSendMessagesToThisUser) {
				fields = append(fields, zap.String("reason", "recipient_dms_closed"))
			}
			logger.Warn("Failed to send display name in use notification", append(fields, zap.Error(err))...)
			continue
		}
		logger.Info("Sent display name in use notification", fields...)
	}
}

// displayNameNoticeDeps wires the notice path to this integrator's Discord session.
func (d *DiscordIntegrator) displayNameNoticeDeps() displayNameNoticeDeps {
	return displayNameNoticeDeps{
		OwnerUsername: d.ownerUsername,
		SendDM: func(ctx context.Context, discordID, message string) error {
			_, err := SendUserMessage(ctx, d.dg, discordID, message)
			return err
		},
	}
}

// ownerUsername returns the username of the account ownerUserID, or "" if the
// account cannot be read.
func (d *DiscordIntegrator) ownerUsername(ctx context.Context, ownerUserID string) string {
	account, err := d.nk.AccountGetId(ctx, ownerUserID)
	if err != nil || account.GetUser() == nil {
		return ""
	}
	return account.GetUser().GetUsername()
}
