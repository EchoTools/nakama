package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

// Invite code cleanup (#764).
//
// Every invite code is one object in InviteCodeCollection and nothing deleted it:
// an expired code was only refused when read, and revoke set a flag. The owner
// index in InviteOwnerCollection kept the codes of both. Storage and every scan of
// these collections grew by one object per code ever created.
//
// sweepInviteCodes removes a code once it is dead, and its entry in the owner
// index with it. The first sweep, at startup, clears what has piled up; later
// sweeps keep it bounded: a code is gone at most inviteExpiredRetention plus
// inviteCleanupInterval after it expires, and a revoked one within
// inviteCleanupInterval. A live code is never removed.

const (
	// inviteCleanupInterval is how often the sweep runs.
	inviteCleanupInterval = time.Hour
	// inviteExpiredRetention is how long an expired code stays, so a player who
	// types a code just after it lapsed is still told "expired" and not "invalid".
	// A revoked code is not kept: it was ended on purpose by its owner.
	inviteExpiredRetention = time.Hour
	// inviteCleanupPage is the storage list page size.
	inviteCleanupPage = 100
)

// inviteRemovable reports whether a stored code is dead enough to delete.
func inviteRemovable(inv *StoredInvite, now time.Time) bool {
	return inv.Revoked || !now.Before(inv.ExpiresAt.Add(inviteExpiredRetention))
}

// inviteSweepResult counts what one sweep removed.
type inviteSweepResult struct {
	Codes  int // code objects deleted
	Owners int // owner index objects rewritten or deleted
}

// startInviteCodeCleanup sweeps now and then every inviteCleanupInterval until
// ctx ends.
func startInviteCodeCleanup(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule) {
	go func() {
		ticker := time.NewTicker(inviteCleanupInterval)
		defer ticker.Stop()
		for {
			res, err := sweepInviteCodes(ctx, logger, nk)
			if err != nil {
				logger.Warn("invite code cleanup failed", zap.Error(err))
			} else if res.Codes > 0 || res.Owners > 0 {
				logger.Info("invite code cleanup", zap.Int("codes_removed", res.Codes), zap.Int("owner_indexes_changed", res.Owners))
			}
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

// sweepInviteCodes deletes the dead codes, then prunes the owner indexes of every
// code that is dead or gone. The codes go first: a failure between the two leaves
// an index entry for a missing code, which liveInviteCodes already skips and the
// next sweep prunes. The reverse order could leave a dead code that nothing
// points at.
func sweepInviteCodes(ctx context.Context, logger *zap.Logger, nk runtime.NakamaModule) (inviteSweepResult, error) {
	var res inviteSweepResult
	now := inviteNow()

	cursor := ""
	for {
		objs, next, err := nk.StorageList(ctx, SystemUserID, SystemUserID, InviteCodeCollection, inviteCleanupPage, cursor)
		if err != nil {
			return res, fmt.Errorf("list codes: %w", err)
		}
		for _, obj := range objs {
			inv := &StoredInvite{}
			if err := json.Unmarshal([]byte(obj.Value), inv); err != nil {
				// Not ours to judge: leave what cannot be read.
				logger.Warn("invite code cleanup: unreadable code", zap.Int("code_len", len(obj.Key)), zap.Error(err))
				continue
			}
			if !inviteRemovable(inv, now) {
				continue
			}
			// The version makes the delete lose to any write since the list.
			err := nk.StorageDelete(ctx, []*runtime.StorageDelete{{Collection: InviteCodeCollection, Key: obj.Key, UserID: SystemUserID, Version: obj.Version}})
			switch {
			case err == nil:
				res.Codes++
			case errors.Is(err, runtime.ErrStorageRejectedVersion):
			default:
				return res, fmt.Errorf("delete code: %w", err)
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}

	cursor = ""
	for {
		objs, next, err := nk.StorageList(ctx, SystemUserID, SystemUserID, InviteOwnerCollection, inviteCleanupPage, cursor)
		if err != nil {
			return res, fmt.Errorf("list owners: %w", err)
		}
		for _, obj := range objs {
			changed, err := pruneInviteOwner(ctx, nk, obj.Key, obj.Value, obj.Version, now)
			if err != nil {
				return res, fmt.Errorf("prune owner: %w", err)
			}
			if changed {
				res.Owners++
			}
		}
		if next == "" {
			break
		}
		cursor = next
	}
	return res, nil
}

// pruneInviteOwner drops from one owner's index the codes that are gone or dead,
// and deletes the index when nothing is left. It only ever removes the codes it
// judged, so a code added by invite/create since is kept.
func pruneInviteOwner(ctx context.Context, nk runtime.NakamaModule, ownerID, value, version string, now time.Time) (bool, error) {
	idx := &storedInviteOwner{}
	if err := json.Unmarshal([]byte(value), idx); err != nil {
		return false, nil // unreadable: leave it
	}
	var drop []string
	for _, code := range idx.Codes {
		inv, err := loadInvite(ctx, nk, code)
		if err != nil {
			return false, err
		}
		if inv == nil || inviteRemovable(inv, now) {
			drop = append(drop, code)
		}
	}
	if len(drop) == 0 && len(idx.Codes) > 0 {
		return false, nil
	}
	if len(drop) == len(idx.Codes) { // also an index that holds nothing
		err := nk.StorageDelete(ctx, []*runtime.StorageDelete{{Collection: InviteOwnerCollection, Key: ownerID, UserID: SystemUserID, Version: version}})
		switch {
		case err == nil:
			return true, nil
		case errors.Is(err, runtime.ErrStorageRejectedVersion):
			// invite/create wrote to it since the list; the next sweep looks again.
			return false, nil
		default:
			return false, err
		}
	}
	err := updateInviteOwner(ctx, nk, ownerID, func(idx *storedInviteOwner) {
		for _, code := range drop {
			idx.Codes = removeString(idx.Codes, code)
		}
	})
	return err == nil, err
}
