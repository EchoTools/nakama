package evr

import (
	"encoding/binary"
	"testing"
	"time"
)

// The game client reads SNSEarlyQuitUpdateNotification at fixed offsets
// (CR15NetGame::EarlyQuitUpdateCB, Quest libr15.so 0x126ac50): penaltyts at +0x18, then
// numearlyquits, numsteadymatches, numsteadyearlyquits as int32 at +0x28/+0x2c/+0x30, and the
// penalty and steady-player levels as bytes at +0x34/+0x35, in a 0x38-byte payload. Each
// field must land at the offset the client reads, or it lands in another field.
func TestEarlyQuitUpdateNotificationLayoutIsWhatTheClientReads(t *testing.T) {
	expiry := time.Unix(1_790_976_825, 0)
	m := NewEarlyQuitUpdateNotification(0, expiry, 7, 30, 2, 3, 1)

	s := NewEasyStream(EncodeMode, nil)
	if err := m.Stream(s); err != nil {
		t.Fatalf("marshal: %v", err)
	}
	b := s.Bytes()
	if len(b) != 0x38 {
		t.Fatalf("encoded %d bytes, want 0x38", len(b))
	}
	if got := int64(binary.LittleEndian.Uint64(b[0x18:])); got != expiry.Unix() {
		t.Errorf("+0x18 penaltyts = %d, want %d", got, expiry.Unix())
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
	if out.PenaltyExpiry != expiry.Unix() || out.NumEarlyQuits != 7 || out.NumSteadyMatches != 30 ||
		out.NumSteadyEarlyQuits != 2 || out.PenaltyLevel != 3 || out.SteadyPlayerLevel != 1 {
		t.Errorf("round trip = %+v", out)
	}
}

// A level the client could not hold in a byte is sent as 0, as the client's profile loader
// clamps a level of 0x10000 or more to 0.
func TestEarlyQuitUpdateNotificationLevelsOutsideAByteAreZero(t *testing.T) {
	m := NewEarlyQuitUpdateNotification(0, time.Unix(0, 0), 0, 0, 0, 300, -1)
	if m.PenaltyLevel != 0 || m.SteadyPlayerLevel != 0 {
		t.Errorf("levels = %d, %d, want 0, 0", m.PenaltyLevel, m.SteadyPlayerLevel)
	}
}
