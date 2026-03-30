package evr

import (
	"encoding/binary"
	"fmt"
)

// SNS Party messages: client → server requests and server → client responses.
//
// Two binary payload formats exist:
//
// 0x28-byte "SNSPartyPayload" (RoutingID + LocalUserUUID + SessionGUID + TargetParam):
//   - SNSPartyJoinRequest
//   - SNSPartyLeaveRequest
//   - SNSPartySendInviteRequest
//   - SNSPartyLockRequest
//   - SNSPartyUnlockRequest
//
// 0x30-byte "SNSPartyTargetPayload" (LocalUserUUID + TargetUserUUID + SessionGUID + Param + Reserved):
//   - SNSPartyKickRequest
//   - SNSPartyPassOwnershipRequest
//   - SNSPartyRespondToInviteRequest

// ---------------------------------------------------------------------------
// 0x28-byte payload messages
// ---------------------------------------------------------------------------

// SNSPartyJoinRequest is sent by a client to join a party.
type SNSPartyJoinRequest struct {
	RoutingID     uint64
	LocalUserUUID [16]byte
	SessionGUID   uint64
	PartyID       uint64
}

func (m SNSPartyJoinRequest) Token() string  { return "SNSPartyJoinRequest" }
func (m *SNSPartyJoinRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyJoinRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoutingID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PartyID) },
	})
}

func (m SNSPartyJoinRequest) String() string {
	return fmt.Sprintf("SNSPartyJoinRequest(routing=%016x, user=%x, session=%016x, party=%016x)",
		m.RoutingID, m.LocalUserUUID, m.SessionGUID, m.PartyID)
}

// SNSPartyLeaveRequest is sent by a client to leave its current party.
type SNSPartyLeaveRequest struct {
	RoutingID     uint64
	LocalUserUUID [16]byte
	SessionGUID   uint64
	TargetParam   uint64
}

func (m SNSPartyLeaveRequest) Token() string  { return "SNSPartyLeaveRequest" }
func (m *SNSPartyLeaveRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyLeaveRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoutingID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetParam) },
	})
}

func (m SNSPartyLeaveRequest) String() string {
	return fmt.Sprintf("SNSPartyLeaveRequest(routing=%016x, user=%x, session=%016x, target=%016x)",
		m.RoutingID, m.LocalUserUUID, m.SessionGUID, m.TargetParam)
}

// SNSPartySendInviteRequest is sent by a client to invite another user to the party.
type SNSPartySendInviteRequest struct {
	RoutingID     uint64
	LocalUserUUID [16]byte
	SessionGUID   uint64
	TargetUserID  uint64
}

func (m SNSPartySendInviteRequest) Token() string  { return "SNSPartyInviteRequest" }
func (m *SNSPartySendInviteRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartySendInviteRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoutingID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetUserID) },
	})
}

func (m SNSPartySendInviteRequest) String() string {
	return fmt.Sprintf("SNSPartySendInviteRequest(routing=%016x, user=%x, session=%016x, target_user=%016x)",
		m.RoutingID, m.LocalUserUUID, m.SessionGUID, m.TargetUserID)
}

// SNSPartyLockRequest is sent by a client to lock the party.
type SNSPartyLockRequest struct {
	RoutingID     uint64
	LocalUserUUID [16]byte
	SessionGUID   uint64
	TargetParam   uint64
}

func (m SNSPartyLockRequest) Token() string  { return "SNSPartyLockRequest" }
func (m *SNSPartyLockRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyLockRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoutingID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetParam) },
	})
}

func (m SNSPartyLockRequest) String() string {
	return fmt.Sprintf("SNSPartyLockRequest(routing=%016x, user=%x, session=%016x, target=%016x)",
		m.RoutingID, m.LocalUserUUID, m.SessionGUID, m.TargetParam)
}

// SNSPartyUnlockRequest is sent by a client to unlock the party.
type SNSPartyUnlockRequest struct {
	RoutingID     uint64
	LocalUserUUID [16]byte
	SessionGUID   uint64
	TargetParam   uint64
}

func (m SNSPartyUnlockRequest) Token() string  { return "SNSPartyUnlockRequest" }
func (m *SNSPartyUnlockRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyUnlockRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoutingID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetParam) },
	})
}

func (m SNSPartyUnlockRequest) String() string {
	return fmt.Sprintf("SNSPartyUnlockRequest(routing=%016x, user=%x, session=%016x, target=%016x)",
		m.RoutingID, m.LocalUserUUID, m.SessionGUID, m.TargetParam)
}

// ---------------------------------------------------------------------------
// 0x30-byte payload messages
// ---------------------------------------------------------------------------

// SNSPartyKickRequest is sent by a client to kick a member from the party.
type SNSPartyKickRequest struct {
	LocalUserUUID  [16]byte
	TargetUserUUID [16]byte
	SessionGUID    uint64
	Param          uint32
	Reserved       uint32
}

func (m SNSPartyKickRequest) Token() string  { return "SNSPartyKickRequest" }
func (m *SNSPartyKickRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyKickRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Param) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Reserved) },
	})
}

func (m SNSPartyKickRequest) String() string {
	return fmt.Sprintf("SNSPartyKickRequest(user=%x, target=%x, session=%016x, param=%d)",
		m.LocalUserUUID, m.TargetUserUUID, m.SessionGUID, m.Param)
}

// SNSPartyPassOwnershipRequest is sent by a client to transfer party ownership.
type SNSPartyPassOwnershipRequest struct {
	LocalUserUUID  [16]byte
	TargetUserUUID [16]byte
	SessionGUID    uint64
	Param          uint32
	Reserved       uint32
}

func (m SNSPartyPassOwnershipRequest) Token() string  { return "SNSPartyPassRequest" }
func (m *SNSPartyPassOwnershipRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyPassOwnershipRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Param) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Reserved) },
	})
}

func (m SNSPartyPassOwnershipRequest) String() string {
	return fmt.Sprintf("SNSPartyPassOwnershipRequest(user=%x, target=%x, session=%016x, param=%d)",
		m.LocalUserUUID, m.TargetUserUUID, m.SessionGUID, m.Param)
}

// SNSPartyRespondToInviteRequest is sent by a client to accept or reject a party invite.
// Param: 0 = reject, 1 = accept.
type SNSPartyRespondToInviteRequest struct {
	LocalUserUUID  [16]byte
	TargetUserUUID [16]byte
	SessionGUID    uint64
	Param          uint32
	Reserved       uint32
}

func (m SNSPartyRespondToInviteRequest) Token() string  { return "SNSPartyInviteResponse" }
func (m *SNSPartyRespondToInviteRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyRespondToInviteRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Param) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Reserved) },
	})
}

func (m SNSPartyRespondToInviteRequest) String() string {
	action := "reject"
	if m.Param == 1 {
		action = "accept"
	}
	return fmt.Sprintf("SNSPartyRespondToInviteRequest(user=%x, target=%x, session=%016x, action=%s)",
		m.LocalUserUUID, m.TargetUserUUID, m.SessionGUID, action)
}

// ---------------------------------------------------------------------------
// Server → Client response/notification types
// ---------------------------------------------------------------------------

// SNSPartyJoinSuccess is sent to a client upon successfully joining a party.
type SNSPartyJoinSuccess struct {
	PartyID uint64
	OwnerID uint64
}

func (m SNSPartyJoinSuccess) Token() string  { return "SNSPartyJoinSuccess" }
func (m *SNSPartyJoinSuccess) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyJoinSuccess) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PartyID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.OwnerID) },
	})
}

func (m SNSPartyJoinSuccess) String() string {
	return fmt.Sprintf("SNSPartyJoinSuccess(party=%016x, owner=%016x)", m.PartyID, m.OwnerID)
}

// SNSPartyJoinFailure is sent to a client when a join attempt fails.
type SNSPartyJoinFailure struct {
	PartyID   uint64
	ErrorCode uint8
}

func (m SNSPartyJoinFailure) Token() string  { return "SNSPartyJoinFailure" }
func (m *SNSPartyJoinFailure) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyJoinFailure) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PartyID) },
		func() error { return s.StreamByte(&m.ErrorCode) },
	})
}

func (m SNSPartyJoinFailure) String() string {
	return fmt.Sprintf("SNSPartyJoinFailure(party=%016x, error=%d)", m.PartyID, m.ErrorCode)
}

// SNSPartyLeaveSuccess is sent to a client upon successfully leaving a party.
type SNSPartyLeaveSuccess struct {
	Unused byte
}

func (m SNSPartyLeaveSuccess) Token() string  { return "SNSPartyLeaveSuccess" }
func (m *SNSPartyLeaveSuccess) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyLeaveSuccess) Stream(s *EasyStream) error {
	return s.StreamByte(&m.Unused)
}

func (m SNSPartyLeaveSuccess) String() string {
	return "SNSPartyLeaveSuccess()"
}

// SNSPartyLeaveNotify is broadcast when a member leaves a party.
type SNSPartyLeaveNotify struct {
	PartyID  uint64
	MemberID uint64
}

func (m SNSPartyLeaveNotify) Token() string  { return "SNSPartyLeaveNotify" }
func (m *SNSPartyLeaveNotify) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyLeaveNotify) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PartyID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.MemberID) },
	})
}

func (m SNSPartyLeaveNotify) String() string {
	return fmt.Sprintf("SNSPartyLeaveNotify(party=%016x, member=%016x)", m.PartyID, m.MemberID)
}

// SNSPartyKickNotify is broadcast when a member is kicked from a party.
type SNSPartyKickNotify struct {
	PartyID uint64
	KickID  uint64
}

func (m SNSPartyKickNotify) Token() string  { return "SNSPartyKickNotify" }
func (m *SNSPartyKickNotify) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyKickNotify) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PartyID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.KickID) },
	})
}

func (m SNSPartyKickNotify) String() string {
	return fmt.Sprintf("SNSPartyKickNotify(party=%016x, kick=%016x)", m.PartyID, m.KickID)
}

// SNSPartyPassNotify is broadcast when party ownership is transferred.
type SNSPartyPassNotify struct {
	PartyID    uint64
	NewOwnerID uint64
}

func (m SNSPartyPassNotify) Token() string  { return "SNSPartyPassNotify" }
func (m *SNSPartyPassNotify) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyPassNotify) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PartyID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NewOwnerID) },
	})
}

func (m SNSPartyPassNotify) String() string {
	return fmt.Sprintf("SNSPartyPassNotify(party=%016x, new_owner=%016x)", m.PartyID, m.NewOwnerID)
}

// SNSPartyLockNotify is broadcast when a party is locked.
type SNSPartyLockNotify struct {
	PartyID uint64
}

func (m SNSPartyLockNotify) Token() string  { return "SNSPartyLockNotify" }
func (m *SNSPartyLockNotify) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyLockNotify) Stream(s *EasyStream) error {
	return s.StreamNumber(binary.LittleEndian, &m.PartyID)
}

func (m SNSPartyLockNotify) String() string {
	return fmt.Sprintf("SNSPartyLockNotify(party=%016x)", m.PartyID)
}

// SNSPartyUnlockNotify is broadcast when a party is unlocked.
type SNSPartyUnlockNotify struct {
	PartyID uint64
}

func (m SNSPartyUnlockNotify) Token() string  { return "SNSPartyUnlockNotify" }
func (m *SNSPartyUnlockNotify) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyUnlockNotify) Stream(s *EasyStream) error {
	return s.StreamNumber(binary.LittleEndian, &m.PartyID)
}

func (m SNSPartyUnlockNotify) String() string {
	return fmt.Sprintf("SNSPartyUnlockNotify(party=%016x)", m.PartyID)
}
