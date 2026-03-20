package evr

import (
	"encoding/binary"
	"fmt"
	"time"
)

// SNSEarlyQuitUpdateNotification is the on-wire notification of a player's
// early quit state change.
//
// Wire layout (0x20 bytes):
//   +0x00  uint64  PlayerID       (XUID / account ID)
//   +0x08  int32   NumEarlyQuits  (current early quit count)
//   +0x0C  int32   PenaltyLevel   (current penalty tier)
//   +0x10  uint64  PenaltyExpiry  (unix timestamp when penalty expires)
//   +0x18  uint64  Reserved
type SNSEarlyQuitUpdateNotification struct {
	PlayerID      uint64
	NumEarlyQuits int32
	PenaltyLevel  int32
	PenaltyExpiry uint64
	Reserved      uint64
}

func (m SNSEarlyQuitUpdateNotification) Token() string {
	return "SNSEarlyQuitUpdateNotification"
}

func (m *SNSEarlyQuitUpdateNotification) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *SNSEarlyQuitUpdateNotification) String() string {
	return fmt.Sprintf("%s(player=0x%x, quits=%d, penalty=%d, expires=%d)",
		m.Token(), m.PlayerID, m.NumEarlyQuits, m.PenaltyLevel, m.PenaltyExpiry)
}

func (m *SNSEarlyQuitUpdateNotification) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PlayerID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NumEarlyQuits) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PenaltyLevel) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PenaltyExpiry) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Reserved) },
	})
}

// PenaltyExpiryTime returns the expiry time as a time.Time.
func (m *SNSEarlyQuitUpdateNotification) PenaltyExpiryTime() time.Time {
	return time.Unix(int64(m.PenaltyExpiry), 0)
}

// RemainingSeconds returns seconds until penalty expires (0 if already expired).
func (m *SNSEarlyQuitUpdateNotification) RemainingSeconds() int32 {
	remaining := time.Until(m.PenaltyExpiryTime()).Seconds()
	if remaining < 0 {
		return 0
	}
	return int32(remaining)
}

// NewEarlyQuitUpdateNotification creates a notification with the given state.
func NewEarlyQuitUpdateNotification(playerID uint64, numEarlyQuits int32, penaltyLevel int32, penaltyExpiry time.Time) *SNSEarlyQuitUpdateNotification {
	return &SNSEarlyQuitUpdateNotification{
		PlayerID:      playerID,
		NumEarlyQuits: numEarlyQuits,
		PenaltyLevel:  penaltyLevel,
		PenaltyExpiry: uint64(penaltyExpiry.Unix()),
	}
}
