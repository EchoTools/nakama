package server

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	StorageCollectionSuspensionProfile = "SuspensionProfile"
	StorageKeySuspensionProfile        = "profile"
	StorageIndexSuspensionProfile      = "StorageIndexSuspensionProfile"
)

// SuspensionProfileRecord represents a single suspension in the user-readable profile format
// This is optimized for portal display and frontend consumption
type SuspensionProfileRecord struct {
	// Identifiers
	ID      string `json:"id"`
	GroupID string `json:"group_id"`

	// Suspension details (user-facing)
	UserNotice   string `json:"user_notice"`
	AuditorNotes string `json:"auditor_notes,omitempty"` // May be empty if user doesn't have access

	// Timing information
	CreatedAt  time.Time `json:"created_at"`
	ExpiryAt   time.Time `json:"expiry_at"`
	IsLifetime bool      `json:"is_lifetime"`
	Duration   string    `json:"duration"` // Human-readable format (e.g., "7d", "lifetime")

	// Scope information
	//
	// AffectedModes lists the game modes this suspension applies to, as symbol
	// tokens. There is no mode field on the underlying enforcement record -- the
	// set is derived by EnforcementRecordAffectedModes, the same helper the
	// authoritative check uses, so the projection and the gate agree.
	//
	// This matters because both suspension gates key on mode:
	// evr_lobby_joinentrant_enforce.go:60 and evr_lobby_joinentrant.go:372-375.
	AffectedModes       []string `json:"affected_modes"`
	AllowPrivateLobbies bool     `json:"allow_private_lobbies"`

	// Enforcement information
	EnforcerUserID    string `json:"enforcer_user_id"`
	EnforcerDiscordID string `json:"enforcer_discord_id"`

	// Edit information
	LastEditedBy string    `json:"last_edited_by,omitempty"`
	LastEditedAt time.Time `json:"last_edited_at,omitempty"`

	// Void information.
	//
	// DEPRECATED as an output. SyncFromJournal no longer emits voided records
	// at all (they are dropped), so these are always zero on a freshly synced
	// profile. They are retained only so that profiles written before that
	// change still round-trip through json.Unmarshal without data loss.
	//
	// Do not reintroduce "annotate but still emit": it makes correctness
	// opt-in for every consumer. See TestSyncFromJournal_ExcludesVoidedRecords.
	VoidedAt  time.Time `json:"voided_at,omitempty"`
	VoidedBy  string    `json:"voided_by,omitempty"`
	VoidNotes string    `json:"void_notes,omitempty"`
}

// SuspensionProfile is the user-readable version of the enforcement journal.
// Permission: OWNER_READ (400) - Only the player can read their own profile.
// This is maintained in sync with GuildEnforcementJournal for portal display.
//
// ============================ SCOPE CONTRACT ============================
//
// THIS TYPE IS A DISPLAY PROJECTION. IT IS NOT AN AUTHORIZATION SOURCE.
// DO NOT USE IT TO DECIDE WHETHER A PLAYER MAY JOIN A MATCH.
//
// It is deliberately narrower than the authoritative check in three ways, and
// each omission is load-bearing rather than an oversight:
//
//  1. SELF ONLY -- no alternate accounts. The authoritative check runs over
//     params.enforcementUserIDs, which is self PLUS alts and is rebuilt on
//     every login from dynamically-discovered alternates
//     (evr_pipeline_login.go:602-606). Alt membership therefore changes with
//     no enforcement write occurring; baking it in would require fanning a
//     write out to every alt each time alt detection changed its mind.
//
//  2. NO GUILD INHERITANCE. The authoritative check fans a suspension out to
//     child guilds via ggRegistry.InheritanceByParentGroupID(). That map is an
//     atomic a registry rebuild REPLACES wholesale
//     (evr_guild_group_registry.go:121), so re-parenting a guild changes the
//     correct answer for every already-written profile with, again, no
//     enforcement write to trigger a resync.
//
//  3. SERVED FROM A NODE-LOCAL INDEX. The declared index is IndexOnly, so
//     reads through it never touch the database. Index population happens only
//     on the node performing the write (core_storage.go:838) and at boot
//     (storage_index.go:396). A ban issued on node A is invisible to node B
//     until node B restarts, and evicted entries are never reloaded -- so an
//     absent entry reads as "not banned", which is fail-OPEN.
//
// Callers that need a correct suspension decision MUST use
// EnforcementJournalsLoad + CheckEnforcementSuspensions against the database,
// as evr_lobby_joinentrant_enforce.go:41-48 and evr_lobby_joinentrant.go:356-361
// do. Both of those are fail-closed by design; this type cannot be.
//
// Pinned by TestSyncFromJournal_DoesNotApplyInheritance and
// TestSyncFromJournal_IsSelfOnly.
//
// ========================================================================
type SuspensionProfile struct {
	UserID      string                    `json:"user_id"`
	Suspensions []SuspensionProfileRecord `json:"suspensions"`
	UpdatedAt   time.Time                 `json:"updated_at"`
	version     string
}

func NewSuspensionProfile(userID string) *SuspensionProfile {
	return &SuspensionProfile{
		UserID:      userID,
		Suspensions: make([]SuspensionProfileRecord, 0),
		UpdatedAt:   time.Now().UTC(),
		version:     "*",
	}
}

func (s *SuspensionProfile) StorageMeta() StorableMetadata {
	return StorableMetadata{
		Collection:      StorageCollectionSuspensionProfile,
		Key:             StorageKeySuspensionProfile,
		PermissionRead:  runtime.STORAGE_PERMISSION_OWNER_READ, // 400 - Only owner can read
		PermissionWrite: runtime.STORAGE_PERMISSION_NO_WRITE,   // Server-managed only
		Version:         s.version,
	}
}

func (s *SuspensionProfile) SetStorageMeta(meta StorableMetadata) {
	s.UserID = meta.UserID
	s.version = meta.Version
}

func (s SuspensionProfile) GetStorageVersion() string {
	return s.version
}

// StorageIndexes declares the index that serves the portal read path.
//
// Fields governs TWO things at once, and both matter here:
//
//  1. What is RETURNED. With IndexOnly: true, List returns idxResult.Value
//     verbatim (storage_index.go:360) and that value is the field-filtered map
//     built in mapIndexStorageFields (storage_index.go:562-570) -- NOT the
//     stored object. A field absent from Fields is discarded before the
//     document is written and can never be read back.
//  2. What is SEARCHABLE. BlugeWalkDocument walks the same already-filtered
//     map (storage_index.go:599), so an absent field is not queryable either.
//
// "suspensions" is therefore mandatory: without it this projection returns
// {"user_id":"..."} and answers no suspension question at all. See
// TestSuspensionProfileIndex_ServesSuspensionData.
//
// "updated_at" is deliberately EXCLUDED. Every stored field costs index memory
// against MaxEntries, and the freshness of the projection is already carried by
// the index's own always-stored update_time field (storage_index.go:579), which
// mapIndexStorageFields adds regardless of Fields.
func (s *SuspensionProfile) StorageIndexes() []StorableIndexMeta {
	return []StorableIndexMeta{{
		Name:       StorageIndexSuspensionProfile,
		Collection: StorageCollectionSuspensionProfile,
		Key:        StorageKeySuspensionProfile,
		Fields:     []string{"user_id", "suspensions"},
		// MaxEntries bounds a CUMULATIVE set, not a concurrent one. This
		// collection holds one entry per user who has ever had an enforcement
		// journal synced -- including users whose suspensions have all expired
		// or been voided, since a zero-suspension profile still carries user_id
		// and is still indexed. It never shrinks.
		//
		// The old value of 10,000 was therefore a ceiling on LIFETIME
		// enforcement history, and exceeding it degrades silently in two
		// directions: eviction (evicted entries are never reloaded) and
		// boot-load truncation (load stops dead at MaxEntries,
		// storage_index.go:503+514). Both make an entry simply absent.
		//
		// Sized to match DisplayNameHistory, the closest structural analogue
		// here: also one entry per user, also cumulative, already sized by this
		// project for EVR's real population. Enforced users are a strict subset
		// of users with a display-name history, so this cap provably cannot
		// bind before that one does.
		//
		// This is an order-of-magnitude judgement, not a measured peak. The
		// accompanying observability -- the entry gauge now published at boot,
		// plus warnings on eviction and load truncation -- is what makes the
		// real number visible so this can be right-sized from evidence.
		MaxEntries: 1000000,
		IndexOnly:  true,
	}}
}

func SuspensionProfileFromStorageObject(obj *api.StorageObject) (*SuspensionProfile, error) {
	profile := &SuspensionProfile{}
	if err := json.Unmarshal([]byte(obj.GetValue()), profile); err != nil {
		return nil, err
	}
	profile.UserID = obj.GetUserId()
	profile.version = obj.GetVersion()
	return profile, nil
}

func (s SuspensionProfile) MarshalJSON() ([]byte, error) {
	s.UpdatedAt = time.Now().UTC()
	type Alias SuspensionProfile
	b, err := json.Marshal(&struct {
		*Alias
	}{
		Alias: (*Alias)(&s),
	})
	if err != nil {
		return nil, err
	}
	return b, nil
}

// SyncFromJournal creates a SuspensionProfile from a GuildEnforcementJournal
// This builds the user-readable profile from the internal journal
func (s *SuspensionProfile) SyncFromJournal(journal *GuildEnforcementJournal) {
	s.UserID = journal.UserID
	s.Suspensions = make([]SuspensionProfileRecord, 0)

	// Iterate through all records in the journal
	for groupID, records := range journal.RecordsByGroupID {
		for _, record := range records {
			// Voided records are DROPPED, not annotated.
			//
			// This used to copy VoidedAt/VoidedBy/VoidNotes onto the projected
			// record and emit it anyway, which made correctness opt-in: every
			// consumer had to remember to check VoidedAt, and one that forgot
			// would enforce a ban a moderator had explicitly retracted.
			//
			// The authoritative path has no such trap -- ActiveSuspensions
			// skips voided records outright -- so the projection matches it.
			if journal.IsVoid(groupID, record.ID) {
				continue
			}

			// Derive the affected mode set using the same helper the
			// authoritative check uses, so the two cannot drift.
			modes := EnforcementRecordAffectedModes(record)
			affectedModes := make([]string, 0, len(modes))
			for _, m := range modes {
				affectedModes = append(affectedModes, m.String())
			}

			// Build the profile record
			profileRec := SuspensionProfileRecord{
				ID:                  record.ID,
				GroupID:             groupID,
				UserNotice:          record.UserNoticeText,
				AuditorNotes:        record.AuditorNotes,
				CreatedAt:           record.CreatedAt,
				ExpiryAt:            record.Expiry,
				IsLifetime:          record.IsLifetime(),
				Duration:            FormatDuration(record.Expiry.Sub(record.CreatedAt)),
				AffectedModes:       affectedModes,
				AllowPrivateLobbies: record.AllowPrivateLobbies,
				EnforcerUserID:      record.EnforcerUserID,
				EnforcerDiscordID:   record.EnforcerDiscordID,
			}

			// Add editor information if available
			if len(record.EditLog) > 0 {
				lastEdit := record.EditLog[len(record.EditLog)-1]
				profileRec.LastEditedBy = lastEdit.EditorUserID
				profileRec.LastEditedAt = lastEdit.EditedAt
			}

			s.Suspensions = append(s.Suspensions, profileRec)
		}
	}

	s.UpdatedAt = time.Now().UTC()
}

// SyncJournalAndProfile updates both the enforcement journal and suspension
// profile in storage, ATOMICALLY.
//
// The journal is the authority; the profile is a projection of it. Previously
// this issued two separate StorableWrite calls, i.e. two independent
// transactions. If the second one failed, the journal had already advanced
// while the projection still described the previous state -- and it lagged in
// the PERMISSIVE direction, with a freshly-issued suspension present in the
// authority but absent from the projection, and no trace that they disagreed.
//
// Both objects now go through StorableWriteMany, which puts them in a single
// ExecuteInTxPgx transaction (core_storage.go:587). The pair cannot half-apply:
// if the projection write fails, the journal write fails with it and the caller
// gets an error naming both objects.
func SyncJournalAndProfile(ctx context.Context, nk runtime.NakamaModule, userID string, journal *GuildEnforcementJournal) error {
	profile := NewSuspensionProfile(userID)
	// create=false: a missing profile is not an error here. Creating it via the
	// read would be a THIRD separate write, defeating the point; the fresh
	// profile already carries version "*", so the atomic write below inserts it.
	if err := StorableRead(ctx, nk, userID, profile, false); err != nil && status.Code(err) != codes.NotFound {
		return err
	}
	profile.SyncFromJournal(journal)

	return StorableWriteMany(ctx, nk, userID, journal, profile)
}

// SyncJournalAndProfileWithRetry syncs journal and profile with retry logic for
// concurrent writes, handling version conflicts with exponential backoff.
//
// ATOMICITY. Every attempt writes the journal and the projection in ONE
// nk.MultiUpdate, so they share a transaction and cannot half-apply. This is
// the more exposed of the two sync functions -- five call sites to
// SyncJournalAndProfile's three -- and it previously issued two separate
// StorableWrite calls, i.e. two independent transactions. A failure between
// them advanced the authority while leaving the projection describing the
// previous state, in the PERMISSIVE direction.
//
// Neither read creates its object. A read-through create is itself an unpaired
// single-object write, which would defeat the very atomicity this function
// provides; a freshly constructed object already carries version "*", so the
// atomic write below inserts it.
//
// WHAT GETS RE-WRITTEN ON A CONFLICT. The invariant is that this function must
// never report success after discarding somebody else's mutation. The original
// loop broke it in the worst possible way: it re-read the stored journal,
// adopted ONLY the winner's storage VERSION, wrote the caller's stale in-memory
// contents on top, and returned nil -- destroying a concurrent moderator's ban
// record while reporting success.
//
// The fix is NOT to adopt the version alone, and it is NOT to blindly resubmit
// the stale payload either. It is GuildEnforcementJournal.mergeStored: the
// stored journal is re-read and the caller's pending records and voids are
// merged INTO it, so the payload the retry submits is the union of both writers
// rather than either one's snapshot. Adopting the winner's version is sound
// only as part of that merge -- it is what lets the union land on top of the
// winner -- and is a data-destroying bug without it. The merge is sound because
// the journal is append-structured; see mergeStored for the per-field rules,
// including why CommunityValuesCompletedAt is NOT resolved by "later wins".
//
// If the post-conflict re-read itself fails, the sync ABORTS. Without the
// winner's state there is nothing safe to write, and re-writing the unchanged
// stale journal can only conflict again. Losing a race and saying so is
// correct; succeeding while discarding data is not.
//
// The profile needs no such care: it is a pure projection of the journal
// (SyncFromJournal), so it is simply re-read for its current version and
// re-derived from the merged journal on every attempt.
func SyncJournalAndProfileWithRetry(ctx context.Context, nk runtime.NakamaModule, userID string, journal *GuildEnforcementJournal) error {
	const maxAttempts = 3
	var lastErr error

	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			// Reconcile the journal with whoever won the previous attempt
			// BEFORE the projection is derived below, so the profile reflects
			// the merged record set and not just the caller's snapshot.
			stored := NewGuildEnforcementJournal(userID)
			if err := StorableRead(ctx, nk, userID, stored, false); err != nil {
				if status.Code(err) != codes.NotFound {
					return fmt.Errorf("SyncJournalAndProfileWithRetry: re-read after conflict: %w", err)
				}
				// The object was removed between attempts. There is no
				// concurrent state to reconcile with, so do NOT merge: retry
				// the caller's own journal under the create-only version.
				//
				// Merging against a stand-in empty journal would be wrong in
				// both directions. mergeStored treats stored as authoritative
				// for CommunityValuesCompletedAt, and an empty journal's zero
				// value means "the player must accept community values" — it
				// would gate a player whose journal does not even exist.
				meta := journal.StorageMeta()
				meta.UserID = userID
				meta.Version = "*"
				journal.SetStorageMeta(meta)
			} else {
				journal.mergeStored(stored)
			}
		}

		// Re-read the profile to pick up its current version. This is safe to
		// re-derive unconditionally: it is a projection of the journal, not an
		// independent mutation. A missing profile is not an error; the fresh
		// one carries version "*" and the atomic write below inserts it.
		profile := NewSuspensionProfile(userID)
		if err := StorableRead(ctx, nk, userID, profile, false); err != nil && status.Code(err) != codes.NotFound {
			lastErr = err
			profile = NewSuspensionProfile(userID)
		}
		profile.SyncFromJournal(journal)

		// Write both in a single transaction.
		err := StorableWriteMany(ctx, nk, userID, journal, profile)
		if err == nil {
			return nil
		}
		lastErr = err

		if !isVersionConflictError(err) || attempt == maxAttempts-1 {
			return err
		}

		// Back off, then reconcile at the top of the next attempt. Only a
		// version conflict reaches here -- MultiUpdate is all-or-nothing, so
		// nothing was written and either object may have been the one rejected.
		// A profile conflict is resolved by the unconditional re-read; a journal
		// conflict is resolved by mergeStored, which is the only thing that
		// makes refreshing the journal's version safe. Never refresh that
		// version without merging first: writing the caller's stale contents at
		// the winner's version is precisely the bug that silently destroyed a
		// concurrent moderator's ban record.
		backoff := time.Duration(10*(1<<uint(attempt))) * time.Millisecond
		select {
		case <-time.After(backoff):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	return lastErr
}
