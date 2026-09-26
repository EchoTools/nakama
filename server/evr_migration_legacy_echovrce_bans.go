package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	// legacyBansApplyEnvVar gates MigrationLegacyEchoVRCEBans between dry-run
	// (the default -- anything other than exactly "apply", including unset)
	// and a real run that writes journal records and mutates guild metadata.
	legacyBansApplyEnvVar   = "EVR_MIGRATE_LEGACY_BANS"
	legacyBansApplyEnvValue = "apply"

	// legacyBansMigrationStorageCollection/Key mark a completed APPLY run so
	// production does not re-walk every journal touching the service guild and
	// re-save every inheriting guild's metadata on every boot.
	//
	// Deliberately self-contained rather than reusing the generic
	// migrationMarker/MigrationState machinery removed in cf50ed81e: that
	// machinery existed to resume a multi-phase migration mid-run. This
	// migration is a single pass whose own skip rule (an inheriting guild
	// already carrying an active suspension for the user -- see
	// legacyBansJournalHasActiveSuspension) already makes a retry idempotent,
	// so the marker's only job is "don't bother running again", not "resume a
	// partial phase".
	legacyBansMigrationStorageCollection = "MigrationState"
	legacyBansMigrationStorageKey        = "legacy_echovrce_bans"

	// legacyBansAuditorNotePrefix is a fmt.Sprintf format for a copied
	// record's audit notes: the original record's ID, then its own notes.
	legacyBansAuditorNotePrefix = "[legacy EchoVRCE global ban] copied from record %s. %s"

	// legacyBansReplacementNoticeText replaces a small, explicit set of
	// production notice-text variants that all assert a GLOBAL ban -- which
	// stops being true the moment the record is a per-guild copy.
	legacyBansReplacementNoticeText = "Account is Banned"
)

// legacyBansNoticeTextReplacements is an EXACT-match set, not a pattern.
// Only these production strings, byte for byte, are rewritten on a copy.
// Anything else -- including a variant not in this list -- is left exactly as
// written on the original record.
var legacyBansNoticeTextReplacements = map[string]bool{
	"Account is Globally Banned": true,
	"Account Globally Banned":    true,
	"Account Globally Banned.":   true,
	"Account globally banned.":   true,
	"Account Global Banned.":     true,
	"Acount Globally Banned":     true,
	"Global Ban":                 true,
}

// legacyBansNoticeText returns the replacement text for an exact-match
// production variant, or original unchanged.
func legacyBansNoticeText(original string) string {
	if legacyBansNoticeTextReplacements[original] {
		return legacyBansReplacementNoticeText
	}
	return original
}

// legacyBansMigrationMarker records that an APPLY run completed. Its absence,
// or a zero CompletedAt, means the migration has not finished and should run.
type legacyBansMigrationMarker struct {
	CompletedAt time.Time      `json:"completed_at"`
	Summary     map[string]any `json:"summary,omitempty"`
}

func legacyBansMarkerRead(ctx context.Context, nk runtime.NakamaModule) (*legacyBansMigrationMarker, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: legacyBansMigrationStorageCollection,
		Key:        legacyBansMigrationStorageKey,
		UserID:     SystemUserID,
	}})
	if err != nil {
		return nil, fmt.Errorf("read legacy-bans migration marker: %w", err)
	}
	if len(objs) == 0 {
		return nil, nil
	}
	marker := &legacyBansMigrationMarker{}
	if err := json.Unmarshal([]byte(objs[0].GetValue()), marker); err != nil {
		return nil, fmt.Errorf("legacy-bans migration marker is present but unreadable: %w", err)
	}
	return marker, nil
}

func legacyBansMarkerWrite(ctx context.Context, nk runtime.NakamaModule, marker *legacyBansMigrationMarker) error {
	data, err := json.Marshal(marker)
	if err != nil {
		return fmt.Errorf("marshal legacy-bans migration marker: %w", err)
	}
	if _, err := nk.StorageWrite(ctx, []*runtime.StorageWrite{{
		Collection:      legacyBansMigrationStorageCollection,
		Key:             legacyBansMigrationStorageKey,
		UserID:          SystemUserID,
		Value:           string(data),
		PermissionRead:  0,
		PermissionWrite: 0,
	}}); err != nil {
		return fmt.Errorf("write legacy-bans migration marker: %w", err)
	}
	return nil
}

// legacyBansGuildGroup is the slice of a guild group's state this migration
// needs: enough to detect inheritance and to round-trip nk.GroupUpdate.
type legacyBansGuildGroup struct {
	groupID  string
	metadata *GroupMetadata
	open     bool
	maxCount int32
}

// legacyBansListGuildGroups walks every "guild" group to full completion --
// never a single page. Production has more guild groups than one GroupsList
// page (131 inheriting guilds alone), so a single-page read (as
// GetGroupIDByGuildIDNK takes) would silently miss both the service guild and
// inheriting guilds past the first 100.
func legacyBansListGuildGroups(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule) ([]legacyBansGuildGroup, error) {
	var (
		out    []legacyBansGuildGroup
		cursor string
	)
	for {
		groups, nextCursor, err := nk.GroupsList(ctx, "", GuildGroupLangTag, nil, nil, 100, cursor)
		if err != nil {
			return nil, fmt.Errorf("list guild groups: %w", err)
		}
		for _, g := range groups {
			md := &GroupMetadata{}
			if err := json.Unmarshal([]byte(g.GetMetadata()), md); err != nil {
				logger.WithFields(map[string]any{"group_id": g.GetId(), "error": err}).Warn("legacy-bans migration: unmarshal guild metadata")
				continue
			}
			out = append(out, legacyBansGuildGroup{
				groupID:  g.GetId(),
				metadata: md,
				open:     g.GetOpen().GetValue(),
				maxCount: g.GetMaxCount(),
			})
		}
		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}
	return out, nil
}

// legacyBansActiveRecords returns every record in groupID's slice of journal
// that is a suspension, not expired, and not voided -- i.e. exactly the
// "ACTIVE" set the migration is scoped to. Sorted by CreatedAt so a user with
// more than one active record gets a deterministic copy order.
func legacyBansActiveRecords(journal *GuildEnforcementJournal, groupID string) []GuildEnforcementRecord {
	var out []GuildEnforcementRecord
	for _, r := range journal.RecordsByGroupID[groupID] {
		if !r.IsSuspension() || r.IsExpired() || journal.IsVoid(groupID, r.ID) {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// legacyBansJournalHasActiveSuspension is the skip check: true if groupID
// already has an active suspension for this journal's user, whether from that
// guild's own moderation or from an earlier, otherwise-incomplete run of this
// same migration. A copied record's CreatedAt and Expiry are byte-identical
// to the original's (see legacyBansCopyRecord), so a completed copy satisfies
// this check on any later run -- which is the whole of this migration's
// idempotency.
func legacyBansJournalHasActiveSuspension(journal *GuildEnforcementJournal, groupID string) bool {
	for _, r := range journal.RecordsByGroupID[groupID] {
		if r.IsSuspension() && !r.IsExpired() && !journal.IsVoid(groupID, r.ID) {
			return true
		}
	}
	return false
}

// legacyBansCopyRecord copies orig into targetGroupID, changing ONLY: a fresh
// ID, the target GroupID, UpdatedAt, AuditorNotes (prefixed with a pointer
// back to the original record), DMNotificationSent (forced true so nothing
// downstream ever attempts to send a fresh DM for this copy), and
// UserNoticeText (only for the exact legacy variants in
// legacyBansNoticeTextReplacements). Every other field -- notably CreatedAt,
// Expiry, RuleViolated, the reporter fields, and every flag -- is
// byte-identical to orig, because those are what the skip check and the
// player-facing record both need to keep describing the same ban.
func legacyBansCopyRecord(orig GuildEnforcementRecord, targetGroupID string) GuildEnforcementRecord {
	cp := orig
	cp.ID = uuid.Must(uuid.NewV4()).String()
	cp.GroupID = targetGroupID
	cp.UpdatedAt = time.Now().UTC()
	cp.AuditorNotes = fmt.Sprintf(legacyBansAuditorNotePrefix, orig.ID, orig.AuditorNotes)
	cp.DMNotificationSent = true
	cp.UserNoticeText = legacyBansNoticeText(orig.UserNoticeText)
	return cp
}

// MigrationLegacyEchoVRCEBans is a one-shot SystemMigrator. EchoVRCE is
// retiring the global-ban service guild (#647 already stops new guilds from
// linking to it): every ACTIVE suspension issued in the service guild is
// copied into each guild that currently inherits suspensions from it, as that
// guild's OWN record, and the inheritance link is then removed.
//
// # Why a copy, not just dropping the link
//
// SuspensionInheritanceGroupIDs today makes an inheriting guild's enforcement
// UI and matchmaking gate READ the service guild's records live (see
// evr_discord_appbot_handlers.go:1093-1094 and
// evr_runtime_rpc_enforcement.go:183-184, 223-224). Removing the link without
// first copying the records would silently un-suspend every affected player
// in every inheriting guild the instant the link is cut.
//
// # Enumeration
//
// Journals are found via the existing StorageIndexEnforcementJournal index
// (query "+value.guild_ids:<service group id>"), the same index and query
// EnforcementJournalListRPC already uses to answer "which journals touch this
// group" -- not a full walk of the Enforcement/journal collection.
//
// # No side effects
//
// This function takes no discordgo client and no session registry, so it has
// no path to a Discord DM, a kick, or an audit-channel post -- those live in
// evr_discord_appbot_enforcement.go and evr_suspension_enforce.go and are
// simply unreachable from here. DMNotificationSent is forced true on every
// copy so nothing downstream later attempts to send one for a record that was
// never a fresh enforcement action.
//
// # Failure handling
//
// Records are copied per user: one GuildEnforcementJournal write (via
// SyncJournalAndProfile) covers every inheriting guild that user needed. If
// that write fails for any user, the run stops immediately, WITHOUT touching
// any guild's SuspensionInheritanceGroupIDs, logs the failure, and returns an
// error -- the marker is left unset, so the next boot retries, and the skip
// rule means nothing already copied is copied twice.
//
// Only once every copy has succeeded does it unlink: for each inheriting
// guild, remove the service group's ID from SuspensionInheritanceGroupIDs and
// save. A guild whose metadata no longer contains the ID (already unlinked by
// an earlier, otherwise-incomplete run) is left alone rather than re-saved.
//
// # Dry run
//
// Controlled by EVR_MIGRATE_LEGACY_BANS: anything other than exactly "apply"
// (including unset) is a dry run. A dry run computes and logs the same
// per-guild counts -- would-copy and would-skip -- without writing a single
// journal, profile, or guild record, and never writes the completion marker.
type MigrationLegacyEchoVRCEBans struct{}

func (m *MigrationLegacyEchoVRCEBans) MigrateSystem(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule) error {
	apply := os.Getenv(legacyBansApplyEnvVar) == legacyBansApplyEnvValue

	marker, err := legacyBansMarkerRead(ctx, nk)
	if err != nil {
		logger.WithField("error", err).Error("legacy-bans migration: marker could not be read; not running")
		return err
	}
	if marker != nil && !marker.CompletedAt.IsZero() {
		logger.WithFields(map[string]any{
			"completed_at": marker.CompletedAt.Format(time.RFC3339),
			"summary":      marker.Summary,
		}).Info("legacy-bans migration: already completed; delete the marker to run it again")
		return nil
	}

	serviceGuildID := ServiceSettings().ServiceGuildID
	if serviceGuildID == "" {
		logger.Info("legacy-bans migration: no service guild configured; nothing to do")
		return nil
	}

	groups, err := legacyBansListGuildGroups(ctx, logger, nk)
	if err != nil {
		return fmt.Errorf("legacy-bans migration: %w", err)
	}

	var serviceGroupID string
	for _, g := range groups {
		if g.metadata.GuildID == serviceGuildID {
			serviceGroupID = g.groupID
			break
		}
	}
	if serviceGroupID == "" {
		logger.WithField("service_guild_id", serviceGuildID).Warn("legacy-bans migration: service guild has no matching group; nothing to do")
		return nil
	}

	inheriting := make(map[string]legacyBansGuildGroup)
	for _, g := range groups {
		for _, parentID := range g.metadata.SuspensionInheritanceGroupIDs {
			if parentID == serviceGroupID {
				inheriting[g.groupID] = g
				break
			}
		}
	}

	if len(inheriting) == 0 {
		logger.WithField("service_group_id", serviceGroupID).Info("legacy-bans migration: no guilds inherit from the service guild; nothing to do")
		if apply {
			if err := legacyBansMarkerWrite(ctx, nk, &legacyBansMigrationMarker{
				CompletedAt: time.Now().UTC(),
				Summary:     map[string]any{"inheriting_guilds": 0},
			}); err != nil {
				logger.WithField("error", err).Error("legacy-bans migration: run completed but the marker could not be written")
			}
		}
		return nil
	}

	query := fmt.Sprintf("+value.guild_ids:%s", Query.EscapeIndexValue(serviceGroupID))
	copiedByGroup := make(map[string]int, len(inheriting))
	skippedByGroup := make(map[string]int, len(inheriting))
	journalsTouched := 0

	var cursor string
	for {
		objs, nextCursor, err := nk.StorageIndexList(ctx, SystemUserID, StorageIndexEnforcementJournal, query, 100, nil, cursor)
		if err != nil {
			return fmt.Errorf("legacy-bans migration: list journals: %w", err)
		}

		for _, obj := range objs.GetObjects() {
			journal, err := GuildEnforcementJournalFromStorageObject(obj)
			if err != nil {
				logger.WithFields(map[string]any{"user_id": obj.GetUserId(), "error": err}).Warn("legacy-bans migration: unmarshal journal")
				continue
			}

			active := legacyBansActiveRecords(journal, serviceGroupID)
			if len(active) == 0 {
				continue
			}

			changed := false
			for groupID := range inheriting {
				if legacyBansJournalHasActiveSuspension(journal, groupID) {
					skippedByGroup[groupID]++
					continue
				}
				for _, orig := range active {
					journal.RecordsByGroupID[groupID] = append(journal.RecordsByGroupID[groupID], legacyBansCopyRecord(orig, groupID))
					copiedByGroup[groupID]++
				}
				changed = true
			}

			if !changed {
				continue
			}
			journalsTouched++

			if !apply {
				continue
			}
			if err := SyncJournalAndProfile(ctx, nk, journal.UserID, journal); err != nil {
				logger.WithFields(map[string]any{"user_id": journal.UserID, "error": err}).Error("legacy-bans migration: copy failed; stopping before any unlink")
				return fmt.Errorf("legacy-bans migration: write journal for %s: %w", journal.UserID, err)
			}
		}

		if nextCursor == "" {
			break
		}
		cursor = nextCursor
	}

	summary := map[string]any{
		"inheriting_guilds": len(inheriting),
		"journals_touched":  journalsTouched,
		"copied_by_group":   copiedByGroup,
		"skipped_by_group":  skippedByGroup,
	}
	logger.WithFields(summary).Info("legacy-bans migration: copy phase complete")

	if !apply {
		logger.Info("legacy-bans migration: dry run; no records written, no guilds unlinked")
		return nil
	}

	// Every copy succeeded -- a failure above already returned before reaching
	// here. Unlink.
	unlinked := 0
	for groupID, g := range inheriting {
		stillLinked := false
		remaining := make([]string, 0, len(g.metadata.SuspensionInheritanceGroupIDs))
		for _, id := range g.metadata.SuspensionInheritanceGroupIDs {
			if id == serviceGroupID {
				stillLinked = true
				continue
			}
			remaining = append(remaining, id)
		}
		if !stillLinked {
			// Already unlinked by an earlier, otherwise-incomplete run.
			continue
		}
		g.metadata.SuspensionInheritanceGroupIDs = remaining

		if err := nk.GroupUpdate(ctx, groupID, "", "", "", "", "", "", g.open, g.metadata.MarshalMap(), int(g.maxCount)); err != nil {
			logger.WithFields(map[string]any{"group_id": groupID, "error": err}).Error("legacy-bans migration: unlink failed")
			return fmt.Errorf("legacy-bans migration: unlink guild %s: %w", groupID, err)
		}
		unlinked++
	}
	summary["unlinked_guilds"] = unlinked

	if err := legacyBansMarkerWrite(ctx, nk, &legacyBansMigrationMarker{CompletedAt: time.Now().UTC(), Summary: summary}); err != nil {
		logger.WithField("error", err).Error("legacy-bans migration: run completed but the marker could not be written; the next boot re-runs it (idempotent via the skip rule)")
	}
	logger.WithFields(summary).Info("legacy-bans migration complete")
	return nil
}
