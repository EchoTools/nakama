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
// Wire layout (0x38 bytes), as the game client reads it: CR15NetGame::EarlyQuitUpdateCB
// (Quest libr15.so 0x126ac50, PC echovr.exe 0x1401618e0) passes these offsets to
// CR15NetGame::SetEarlyQuitPenaltyLevel, which stores them on CR15NetGame (field map and
// evidence: echovr-reconstruction docs/earlyquit_field_analysis.md):
//
//	+0x00  UUID    LoginSession         (not read by the client)
//	+0x10  uint64  PlayerID             (not read)
//	+0x18  int64   Stamp                (penaltyts, +0x64820; the update is applied only if its stamp is newer
//	                                    than the stored one)
//	+0x20  int64   PenaltyExpiry        (the lockout's end, Unix seconds; 0 means none. The game reserves this
//	                                    field for it: ReVault comments at echovr.exe 0x1401ae372 and libr15.so 0x136ac50)
//	+0x28  int32   NumEarlyQuits        (+0x64830)
//	+0x2C  int32   NumSteadyMatches     (+0x64834)
//	+0x30  int32   NumSteadyEarlyQuits  (+0x64838)
//	+0x34  uint8   PenaltyLevel         (+0x64844)
//	+0x35  uint8   SteadyPlayerLevel    (+0x64845)
//	+0x36  [2]byte Padding
//
// There is no lockout-active or show-warning field. The stock client never starts the
// lockout countdown from this message; nevr-runtime clients take PenaltyExpiry as the
// lockout's end.
//
// Every update sent to a player carries a stamp greater than the last one sent to that player.
// The service keeps it at max(previous+1, now), so a client that still reads +0x18 as the expiry
// sees a lockout that is already over and shows no countdown, rather than a wrong one.
//
// The layout is shared with nevr-runtime#907; TestEarlyQuitUpdateNotificationGoldenVector pins it.
type SNSEarlyQuitUpdateNotification struct {
	LoginSession        uuid.UUID // +0x00
	PlayerID            uint64    // +0x10
	Stamp               int64     // +0x18
	PenaltyExpiry       int64     // +0x20
	NumEarlyQuits       int32     // +0x28
	NumSteadyMatches    int32     // +0x2C
	NumSteadyEarlyQuits int32     // +0x30
	PenaltyLevel        uint8     // +0x34
	SteadyPlayerLevel   uint8     // +0x35
	_                   [2]byte   // +0x36
}

func (m SNSEarlyQuitUpdateNotification) Token() string {
	return "SNSEarlyQuitUpdateNotification"
}

func (m *SNSEarlyQuitUpdateNotification) Symbol() Symbol {
	return ToSymbol(m.Token())
}

func (m *SNSEarlyQuitUpdateNotification) String() string {
	return fmt.Sprintf("%s(session=%s, player=0x%x, stamp=%d, penalty=%d, early_quits=%d, steady_matches=%d, steady_quits=%d, steady_level=%d, expires=%d)",
		m.Token(), m.LoginSession, m.PlayerID, m.Stamp, m.PenaltyLevel, m.NumEarlyQuits, m.NumSteadyMatches, m.NumSteadyEarlyQuits,
		m.SteadyPlayerLevel, m.PenaltyExpiry)
}

func (m *SNSEarlyQuitUpdateNotification) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&m.LoginSession) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PlayerID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Stamp) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PenaltyExpiry) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NumEarlyQuits) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NumSteadyMatches) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NumSteadyEarlyQuits) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PenaltyLevel) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SteadyPlayerLevel) },
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

// NewEarlyQuitUpdateNotification creates a notification with the given state. penaltyExpiry is Unix
// seconds; a state with no lockout (0, or the stored -1) is sent as 0. Levels outside a byte are sent
// as 0, as the client's profile loader clamps them (LoadEarlyQuitPenalty).
func NewEarlyQuitUpdateNotification(playerID uint64, stamp, penaltyExpiry int64, numEarlyQuits, numSteadyMatches, numSteadyEarlyQuits, penaltyLevel, steadyPlayerLevel int32) *SNSEarlyQuitUpdateNotification {
	if penaltyExpiry < 0 {
		penaltyExpiry = 0
	}
	return &SNSEarlyQuitUpdateNotification{
		PlayerID:            playerID,
		Stamp:               stamp,
		PenaltyExpiry:       penaltyExpiry,
		NumEarlyQuits:       numEarlyQuits,
		NumSteadyMatches:    numSteadyMatches,
		NumSteadyEarlyQuits: numSteadyEarlyQuits,
		PenaltyLevel:        levelByte(penaltyLevel),
		SteadyPlayerLevel:   levelByte(steadyPlayerLevel),
	}
}

func levelByte(level int32) uint8 {
	if level < 0 || level > 0xFF {
		return 0
	}
	return uint8(level)
}
