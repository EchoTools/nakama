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
// Wire layout (0x38 bytes, confirmed via CTcpBroadcaster::Listen min_size
// and PlayerEarlyQuitState struct mapping from reconstruction):
//
//	+0x00  UUID    LoginSession            (not read by handler)
//	+0x10  uint64  PlayerID                (not read by handler)
//	+0x18  int64   PenaltyExpiry           (→ state.penalty_timestamp)
//	+0x20  uint64  Reserved                (not read by handler)
//	+0x28  int32   NumSteadyEarlyQuits     (→ state.num_steady_early_quits)
//	+0x2C  int32   PenaltyLevel            (→ state.penalty_level)
//	+0x30  int32   SteadyPlayerLevel       (→ state.steady_player_level)
//	+0x34  uint8   ShowEarlyQuitWarning    (→ expression flag)
//	+0x35  uint8   LockoutCountdownActive  (→ expression flag, has prev tracking)
//	+0x36  [2]byte Padding
//
// Note: Does NOT carry num_early_quits or num_steady_matches — those come
// from the profile JSON at login only.
//
// Handler: CR15NetGame::EarlyQuitUpdateCB (Quest 0x136ac50)
type SNSEarlyQuitUpdateNotification struct {
	LoginSession           uuid.UUID // +0x00
	PlayerID               uint64    // +0x10
	PenaltyExpiry          int64     // +0x18
	Reserved               uint64    // +0x20
	NumSteadyEarlyQuits    int32     // +0x28
	PenaltyLevel           int32     // +0x2C
	SteadyPlayerLevel      int32     // +0x30
	ShowEarlyQuitWarning   uint8     // +0x34
	LockoutCountdownActive uint8     // +0x35
	_                      [2]byte   // +0x36
}

func (m SNSEarlyQuitUpdateNotification) Token() string {
	return "SNSEarlyQuitUpdateNotification"
}

func (m *SNSEarlyQuitUpdateNotification) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *SNSEarlyQuitUpdateNotification) String() string {
	return fmt.Sprintf("%s(session=%s, player=0x%x, penalty=%d, steady_quits=%d, steady_level=%d, expires=%d)",
		m.Token(), m.LoginSession, m.PlayerID, m.PenaltyLevel, m.NumSteadyEarlyQuits, m.SteadyPlayerLevel, m.PenaltyExpiry)
}

func (m *SNSEarlyQuitUpdateNotification) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.LoginSession) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PlayerID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PenaltyExpiry) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Reserved) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NumSteadyEarlyQuits) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PenaltyLevel) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SteadyPlayerLevel) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.ShowEarlyQuitWarning) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LockoutCountdownActive) },
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
func NewEarlyQuitUpdateNotification(playerID uint64, penaltyExpiry time.Time, numSteadyEarlyQuits, penaltyLevel, steadyPlayerLevel int32, showWarning, lockoutActive bool) *SNSEarlyQuitUpdateNotification {
	var warn, lock uint8
	if showWarning {
		warn = 1
	}
	if lockoutActive {
		lock = 1
	}
	return &SNSEarlyQuitUpdateNotification{
		PlayerID:               playerID,
		PenaltyExpiry:          penaltyExpiry.Unix(),
		NumSteadyEarlyQuits:    numSteadyEarlyQuits,
		PenaltyLevel:           penaltyLevel,
		SteadyPlayerLevel:      steadyPlayerLevel,
		ShowEarlyQuitWarning:   warn,
		LockoutCountdownActive: lock,
	}
}
