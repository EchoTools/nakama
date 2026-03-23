package evr

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
)

// SNSEarlyQuitUpdateNotification is the on-wire notification of a player's
// early quit state change.
//
// Wire layout (0x38 bytes, confirmed via CTcpBroadcaster::Listen min_size):
//
//	+0x00  UUID    LoginSession   (login session UUID, not read by handler)
//	+0x10  uint64  PlayerID       (XPID / account ID, not read by handler)
//	+0x18  int64   PenaltyExpiry  (unix timestamp, compared against stored value)
//	+0x20  uint64  Reserved       (not read by handler)
//	+0x28  int32   NumEarlyQuits  (current early quit count)
//	+0x2C  int32   PenaltyLevel   (current penalty tier)
//	+0x30  int32   Field30        (unknown, stored to game state)
//	+0x34  uint8   Field34        (unknown, stored to game state)
//	+0x35  uint8   Field35        (unknown, stored to game state)
//	+0x36  [2]byte Padding
//
// Handler: CR15NetGame::EarlyQuitUpdateCB (Quest 0x136ac50)
// Only reads fields at +0x18 onward. Compares PenaltyExpiry against stored
// value; updates game state only if newer. Fires SendComponentEventGlobal
// on change.
type SNSEarlyQuitUpdateNotification struct {
	LoginSession  uuid.UUID // +0x00: login session UUID
	PlayerID      uint64    // +0x10: XPID / account ID
	PenaltyExpiry int64     // +0x18: unix timestamp when penalty expires
	Reserved      uint64    // +0x20: unknown
	NumEarlyQuits int32     // +0x28: current early quit count
	PenaltyLevel  int32     // +0x2C: current penalty tier
	Field30       int32     // +0x30: unknown
	Field34       uint8     // +0x34: unknown
	Field35       uint8     // +0x35: unknown
	_             [2]byte   // +0x36: alignment padding
}

func (m SNSEarlyQuitUpdateNotification) Token() string {
	return "SNSEarlyQuitUpdateNotification"
}

func (m *SNSEarlyQuitUpdateNotification) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *SNSEarlyQuitUpdateNotification) String() string {
	return fmt.Sprintf("%s(session=%s, player=0x%x, quits=%d, penalty=%d, expires=%d)",
		m.Token(), m.LoginSession, m.PlayerID, m.NumEarlyQuits, m.PenaltyLevel, m.PenaltyExpiry)
}

func (m *SNSEarlyQuitUpdateNotification) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.LoginSession) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PlayerID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PenaltyExpiry) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Reserved) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NumEarlyQuits) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PenaltyLevel) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Field30) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Field34) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Field35) },
		func() error {
			pad := make([]byte, 2)
			return s.StreamBytes(&pad, 2)
		},
	})
}

// PenaltyExpiryTime returns the expiry time as a time.Time.
func (m *SNSEarlyQuitUpdateNotification) PenaltyExpiryTime() time.Time {
	return time.Unix(m.PenaltyExpiry, 0)
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
		PenaltyExpiry: penaltyExpiry.Unix(),
	}
}
