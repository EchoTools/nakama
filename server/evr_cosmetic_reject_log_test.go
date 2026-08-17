package server

import (
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

// debugRejectLines returns every DEBUG event carrying the ownership-reject message.
//
// captureLogger.find (evr_earlyquit_reconnect_test.go) returns only the first match and
// only a bool; a test whose whole subject is "how many lines, and zero on the happy
// path" needs the count and the fields of each, so this reads the shared event slice
// directly. Same package, same single-goroutine usage as find.
func debugRejectLines(l *captureLogger) []captureLogEvent {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]captureLogEvent, 0, 4)
	for _, e := range *l.events {
		if e.level == "debug" && e.msg == cosmeticRejectLogMsg {
			out = append(out, e)
		}
	}
	return out
}

// TestEquipOwnershipReject_RecordsWhoAndWhat is the whole requirement in one table, and
// the two subtests are each other's control.
//
// The owner's ruling (2026-08-16) is that a reject does not tell the player, it just
// logs it. So there are exactly two things to prove and they are inseparable: a reject
// leaves a line naming the player and the item, and an owned equip leaves none. A zero
// asserted by an instrument never shown to fire is not evidence, so both cases run the
// SAME helper (debugRejectLines) against the SAME message constant. The "0 lines"
// subtest is only meaningful because its sibling gets 1 out of the identical call.
//
// The counter is asserted alongside on both cases for the same reason: it independently
// witnesses whether a strip actually happened, so the owned case cannot pass by way of
// an equip that silently did nothing.
func TestEquipOwnershipReject_RecordsWhoAndWhat(t *testing.T) {
	const (
		unownedTag = "rwd_tag_s1_vrml_s1_finalist" // validate:"restricted", no wallet grant below
		ownedTag   = "rwd_tag_s1_a_secondary"      // in cosmeticDefaults(false)
		userID     = "db484900-1111-2222-3333-444444444444"
	)

	wantDefault, ok := evr.DefaultCosmeticLoadout().ToMap()["tag"]
	if !ok {
		t.Fatalf("no default for the tag slot: the OUTCOME field has nothing to name")
	}

	cases := []struct {
		name      string
		item      string
		wantLines int
		wantStrip int64
	}{
		{
			name:      "unowned item is rejected and the reject is recorded",
			item:      unownedTag,
			wantLines: 1,
			wantStrip: 1,
		},
		{
			name:      "owned item is equipped and leaves no line at all",
			item:      ownedTag,
			wantLines: 0,
			wantStrip: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			log := newCaptureLogger()
			rec := &strippedCounterRecorder{}

			_, strips, err := EquipAndSanitize(evr.DefaultCosmeticLoadout(), "tag", tc.item, cosmeticDefaults(false), rec.add)
			if err != nil {
				t.Fatalf("EquipAndSanitize returned error: %v", err)
			}
			logCosmeticStrips(log, userID, sanitizePathEquip, strips)

			if got := rec.total(); got != tc.wantStrip {
				t.Fatalf("counter increments = %d, want %d; the equip did not do what this case assumes. recorded: %+v", got, tc.wantStrip, rec.calls)
			}

			got := debugRejectLines(log)
			if len(got) != tc.wantLines {
				t.Fatalf("reject log lines = %d, want %d; recorded: %+v", len(got), tc.wantLines, got)
			}
			if tc.wantLines == 0 {
				return
			}

			// WHAT / WHERE / IDENTIFIER / OUTCOME. The IDENTIFIER half (user id, item
			// id, slot) is the part the counter cannot carry -- its tags are bounded on
			// purpose -- and is the reason this line exists at all.
			f := got[0].fields
			for _, chk := range []struct{ key, want string }{
				{"user_id", userID},                 // IDENTIFIER: which player
				{"item_id", tc.item},                // IDENTIFIER: which item
				{"slot", "tag"},                     // IDENTIFIER: which slot
				{"path", string(sanitizePathEquip)}, // WHERE
				{"default_id", wantDefault},         // OUTCOME: reset to *which* default
			} {
				if f[chk.key] != chk.want {
					t.Errorf("log field %q = %v, want %q; fields: %+v", chk.key, f[chk.key], chk.want, f)
				}
			}
		})
	}
}

// TestServePathRegenerationEmitsNoRejectLine covers the flood falsifier.
//
// The serve path runs on every profile regeneration -- login, lobby join, and every
// equip event -- against the stored loadout, not against a player action. An
// already-poisoned account therefore strips on EVERY regeneration. A line per
// regeneration would be a flood, not a record, and would bury the ~3/day the equip path
// is expected to produce.
//
// Structural half: equippedCosmeticsForProfile takes no logger, so it cannot emit. That
// is the actual guarantee and it is enforced by the compiler.
// Behavioural half, below: three regenerations of the same poisoned profile move the
// counter three times -- proving strips really are happening on this path, so a zero
// here is not the zero of nothing occurring -- while the reject log stays empty.
func TestServePathRegenerationEmitsNoRejectLine(t *testing.T) {
	poisoned := func() *EVRProfile {
		p := &EVRProfile{account: &api.Account{Wallet: "{}"}}
		p.LoadoutCosmetics.Loadout = evr.DefaultCosmeticLoadout()
		p.LoadoutCosmetics.Loadout.Tag = "rwd_tag_s1_vrml_s1_finalist"
		return p
	}

	log := newCaptureLogger()
	rec := &strippedCounterRecorder{}

	const regenerations = 3
	for i := 0; i < regenerations; i++ {
		if _, _, err := equippedCosmeticsForProfile(poisoned(), rec.add); err != nil {
			t.Fatalf("equippedCosmeticsForProfile returned error: %v", err)
		}
	}

	if got := rec.countFor("tag", string(sanitizePathServe)); got != regenerations {
		t.Fatalf("serve-path strips = %d, want %d: this test is not exercising the strip it claims to", got, regenerations)
	}
	if got := debugRejectLines(log); len(got) != 0 {
		t.Errorf("serve path emitted %d reject line(s) across %d regenerations; that is a flood, not a record: %+v", len(got), regenerations, got)
	}
}

// TestLogCosmeticStrips_OneLinePerStrippedSlot pins the shape of the record when a
// single equip event strips more than one slot, which a poisoned stored loadout does:
// the sanitize is over the whole loadout, not only the slot the player touched.
//
// One line per slot rather than one line per event, because the field contract requires
// item id and slot in the IDENTIFIER, and a single line naming two items cannot be
// grepped for either one.
func TestLogCosmeticStrips_OneLinePerStrippedSlot(t *testing.T) {
	const userID = "db484900-5555-6666-7777-888888888888"

	strips := []cosmeticStrip{
		{Slot: "tag", ItemID: "rwd_tag_s1_vrml_s1_finalist", DefaultID: "rwd_tag_s1_a_secondary"},
		{Slot: "medal", ItemID: "rwd_medal_0006", DefaultID: "rwd_medal_s1_vrml_s1_user"},
	}

	log := newCaptureLogger()
	logCosmeticStrips(log, userID, sanitizePathEquip, strips)

	got := debugRejectLines(log)
	if len(got) != len(strips) {
		t.Fatalf("reject log lines = %d, want %d (one per stripped slot): %+v", len(got), len(strips), got)
	}
	for i, want := range strips {
		f := got[i].fields
		if f["slot"] != want.Slot || f["item_id"] != want.ItemID || f["default_id"] != want.DefaultID {
			t.Errorf("line %d = %+v, want slot=%q item_id=%q default_id=%q", i, f, want.Slot, want.ItemID, want.DefaultID)
		}
		if f["user_id"] != userID {
			t.Errorf("line %d user_id = %v, want %q", i, f["user_id"], userID)
		}
	}
}

// TestLogCosmeticStrips_NoStripsNoLines is the degenerate case stated on its own so it
// cannot be lost inside a table: the report-the-change split is only worth anything if
// the no-op input produces zero downstream effect.
func TestLogCosmeticStrips_NoStripsNoLines(t *testing.T) {
	log := newCaptureLogger()
	logCosmeticStrips(log, "db484900-9999-0000-0000-000000000000", sanitizePathEquip, nil)
	if got := debugRejectLines(log); len(got) != 0 {
		t.Errorf("an empty strip report produced %d line(s): %+v", len(got), got)
	}
}
