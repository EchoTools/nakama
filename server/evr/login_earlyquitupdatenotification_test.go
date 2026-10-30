package evr

import (
	"encoding/binary"
	"encoding/hex"
	"testing"
)

// The game client reads SNSEarlyQuitUpdateNotification at fixed offsets
// (CR15NetGame::EarlyQuitUpdateCB, Quest libr15.so 0x126ac50): the stamp at +0x18, the lockout
// expiry at +0x20 (the field the game reserves for it), then
// numearlyquits, numsteadymatches, numsteadyearlyquits as int32 at +0x28/+0x2c/+0x30, and the
// penalty and steady-player levels as bytes at +0x34/+0x35, in a 0x38-byte payload. Each
// field must land at the offset the client reads, or it lands in another field.
func TestEarlyQuitUpdateNotificationLayoutIsWhatTheClientReads(t *testing.T) {
	const stamp, expiry = int64(1_790_976_000), int64(1_790_976_825)
	m := NewEarlyQuitUpdateNotification(0, stamp, expiry, 7, 30, 2, 3, 1)

	s := NewEasyStream(EncodeMode, nil)
	if err := m.Stream(s); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b := s.Bytes()
	if len(b) != 0x38 {
		t.Fatalf("encoded %d bytes, want 0x38", len(b))
	}
	if got := int64(binary.LittleEndian.Uint64(b[0x18:])); got != stamp {
		t.Errorf("+0x18 stamp = %d, want %d", got, stamp)
	}
	if got := int64(binary.LittleEndian.Uint64(b[0x20:])); got != expiry {
		t.Errorf("+0x20 lockout expiry = %d, want %d", got, expiry)
	}
	for _, f := range []struct {
		name string
		off  int
		want int32
	}{{"numearlyquits", 0x28, 7}, {"numsteadymatches", 0x2c, 30}, {"numsteadyearlyquits", 0x30, 2}} {
		if got := int32(binary.LittleEndian.Uint32(b[f.off:])); got != f.want {
			t.Errorf("+0x%x %s = %d, want %d", f.off, f.name, got, f.want)
		}
	}
	if b[0x34] != 3 || b[0x35] != 1 {
		t.Errorf("+0x34 penaltylevel = %d, +0x35 steadyplayerlevel = %d, want 3 and 1", b[0x34], b[0x35])
	}

	var out SNSEarlyQuitUpdateNotification
	if err := out.Stream(NewEasyStream(DecodeMode, b)); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Stamp != stamp || out.PenaltyExpiry != expiry || out.NumEarlyQuits != 7 || out.NumSteadyMatches != 30 ||
		out.NumSteadyEarlyQuits != 2 || out.PenaltyLevel != 3 || out.SteadyPlayerLevel != 1 {
		t.Errorf("round trip = %+v", out)
	}
}

// A level the client could not hold in a byte is sent as 0, as the client's profile loader
// clamps a level of 0x10000 or more to 0.
func TestEarlyQuitUpdateNotificationLevelsOutsideAByteAreZero(t *testing.T) {
	m := NewEarlyQuitUpdateNotification(0, 1, 0, 0, 0, 0, 300, -1)
	if m.PenaltyLevel != 0 || m.SteadyPlayerLevel != 0 {
		t.Errorf("levels = %d, %d, want 0, 0", m.PenaltyLevel, m.SteadyPlayerLevel)
	}
}

// A state with no lockout (the stored -1, or 0) is sent as expiry 0, which the client reads as none.
func TestEarlyQuitUpdateNotificationNoLockoutIsExpiryZero(t *testing.T) {
	for _, expiry := range []int64{-1, 0} {
		if m := NewEarlyQuitUpdateNotification(0, 5, expiry, 0, 0, 0, 0, 0); m.PenaltyExpiry != 0 {
			t.Errorf("expiry %d sent as %d, want 0", expiry, m.PenaltyExpiry)
		}
	}
}

// goldenEarlyQuitUpdateHex is the one fixed update both sides of #712 agree on: this test encodes the
// fixture and asserts the hex, and nevr-runtime's test_early_quit_lockout.cpp parses the same hex
// through the hook's rule. The vector is posted at
// https://github.com/EchoTools/nevr-runtime/issues/907#issuecomment-6393534889 . A layout change on
// either side fails both tests. The stamp (+0x18) and the expiry (+0x20) differ on purpose: a rule
// that reads the stamp as the expiry gets 1790975000, the right one 1790975600.
const goldenEarlyQuitUpdateHex = "000000000000000000000000000000008877665544332211181cc06a00000000701ec06a0000000003000000110000000200000002010000"

func goldenEarlyQuitUpdate() *SNSEarlyQuitUpdateNotification {
	return NewEarlyQuitUpdateNotification(0x1122334455667788, 1790975000, 1790975600, 3, 17, 2, 2, 1)
}

func TestEarlyQuitUpdateNotificationGoldenVector(t *testing.T) {
	s := NewEasyStream(EncodeMode, nil)
	if err := goldenEarlyQuitUpdate().Stream(s); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if got := hex.EncodeToString(s.Bytes()); got != goldenEarlyQuitUpdateHex {
		t.Errorf("encoded update differs from the golden vector shared with nevr-runtime#907\n got: %s\nwant: %s", got, goldenEarlyQuitUpdateHex)
	}

	b, err := hex.DecodeString(goldenEarlyQuitUpdateHex)
	if err != nil || len(b) != 0x38 {
		t.Fatalf("golden hex is %d bytes (err %v), want 56", len(b), err)
	}
	var out SNSEarlyQuitUpdateNotification
	if err := out.Stream(NewEasyStream(DecodeMode, b)); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if out.Stamp != 1790975000 || out.PenaltyExpiry != 1790975600 || out.PlayerID != 0x1122334455667788 ||
		out.NumEarlyQuits != 3 || out.NumSteadyMatches != 17 || out.NumSteadyEarlyQuits != 2 ||
		out.PenaltyLevel != 2 || out.SteadyPlayerLevel != 1 {
		t.Errorf("decoded golden vector = %+v", out)
	}
}
