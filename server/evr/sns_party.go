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

// ---------------------------------------------------------------------------
// Additional client → server request types
// ---------------------------------------------------------------------------

// SNSPartyCreateRequest is sent by a client to create a new party.
// Uses the standard 0x28-byte payload.
type SNSPartyCreateRequest struct {
	RoutingID     uint64
	LocalUserUUID [16]byte
	SessionGUID   uint64
	TargetParam   uint64
}

func (m SNSPartyCreateRequest) Token() string  { return "SNSPartyCreateRequest" }
func (m *SNSPartyCreateRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyCreateRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoutingID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetParam) },
	})
}

func (m SNSPartyCreateRequest) String() string {
	return fmt.Sprintf("SNSPartyCreateRequest(routing=%016x, user=%x, session=%016x, target=%016x)",
		m.RoutingID, m.LocalUserUUID, m.SessionGUID, m.TargetParam)
}

// SNSPartyUpdateRequest is sent by a client to update party metadata.
type SNSPartyUpdateRequest struct {
	RoutingID     uint64
	LocalUserUUID [16]byte
	SessionGUID   uint64
	TargetParam   uint64
}

func (m SNSPartyUpdateRequest) Token() string  { return "SNSPartyUpdateRequest" }
func (m *SNSPartyUpdateRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyUpdateRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoutingID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetParam) },
	})
}

func (m SNSPartyUpdateRequest) String() string {
	return fmt.Sprintf("SNSPartyUpdateRequest(routing=%016x, user=%x, session=%016x, target=%016x)",
		m.RoutingID, m.LocalUserUUID, m.SessionGUID, m.TargetParam)
}

// SNSPartyUpdateMemberRequest is sent by a client to update its own member data.
type SNSPartyUpdateMemberRequest struct {
	RoutingID     uint64
	LocalUserUUID [16]byte
	SessionGUID   uint64
	TargetParam   uint64
}

func (m SNSPartyUpdateMemberRequest) Token() string  { return "SNSPartyUpdateMemberRequest" }
func (m *SNSPartyUpdateMemberRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyUpdateMemberRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoutingID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetParam) },
	})
}

func (m SNSPartyUpdateMemberRequest) String() string {
	return fmt.Sprintf("SNSPartyUpdateMemberRequest(routing=%016x, user=%x, session=%016x, target=%016x)",
		m.RoutingID, m.LocalUserUUID, m.SessionGUID, m.TargetParam)
}

// SNSPartyInviteListRefreshRequest is sent to request an updated invite list.
type SNSPartyInviteListRefreshRequest struct {
	RoutingID     uint64
	LocalUserUUID [16]byte
	SessionGUID   uint64
	TargetParam   uint64
}

func (m SNSPartyInviteListRefreshRequest) Token() string  { return "SNSPartyInviteListRefreshRequest" }
func (m *SNSPartyInviteListRefreshRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyInviteListRefreshRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoutingID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetParam) },
	})
}

func (m SNSPartyInviteListRefreshRequest) String() string {
	return fmt.Sprintf("SNSPartyInviteListRefreshRequest(routing=%016x, user=%x, session=%016x, target=%016x)",
		m.RoutingID, m.LocalUserUUID, m.SessionGUID, m.TargetParam)
}

// ---------------------------------------------------------------------------
// Additional server → client success/failure/notify types
// ---------------------------------------------------------------------------

// SNSPartyCreateSuccess is sent when a party is created successfully.
type SNSPartyCreateSuccess struct {
	PartyID uint64
	OwnerID uint64
}

func (m SNSPartyCreateSuccess) Token() string  { return "SNSPartyCreateSuccess" }
func (m *SNSPartyCreateSuccess) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyCreateSuccess) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PartyID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.OwnerID) },
	})
}

func (m SNSPartyCreateSuccess) String() string {
	return fmt.Sprintf("SNSPartyCreateSuccess(party=%016x, owner=%016x)", m.PartyID, m.OwnerID)
}

// SNSPartyCreateFailure is sent when party creation fails.
type SNSPartyCreateFailure struct {
	ErrorCode uint8
}

func (m SNSPartyCreateFailure) Token() string  { return "SNSPartyCreateFailure" }
func (m *SNSPartyCreateFailure) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyCreateFailure) Stream(s *EasyStream) error {
	return s.StreamByte(&m.ErrorCode)
}

func (m SNSPartyCreateFailure) String() string {
	return fmt.Sprintf("SNSPartyCreateFailure(error=%d)", m.ErrorCode)
}

// SNSPartyJoinNotify is broadcast when a new member joins a party.
type SNSPartyJoinNotify struct {
	PartyID  uint64
	MemberID uint64
}

func (m SNSPartyJoinNotify) Token() string  { return "SNSPartyJoinNotify" }
func (m *SNSPartyJoinNotify) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyJoinNotify) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PartyID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.MemberID) },
	})
}

func (m SNSPartyJoinNotify) String() string {
	return fmt.Sprintf("SNSPartyJoinNotify(party=%016x, member=%016x)", m.PartyID, m.MemberID)
}

// SNSPartyLeaveFailure is sent when a leave attempt fails.
type SNSPartyLeaveFailure struct {
	ErrorCode uint8
}

func (m SNSPartyLeaveFailure) Token() string  { return "SNSPartyLeaveFailure" }
func (m *SNSPartyLeaveFailure) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyLeaveFailure) Stream(s *EasyStream) error {
	return s.StreamByte(&m.ErrorCode)
}

func (m SNSPartyLeaveFailure) String() string {
	return fmt.Sprintf("SNSPartyLeaveFailure(error=%d)", m.ErrorCode)
}

// SNSPartyKickSuccess is sent when a kick succeeds.
type SNSPartyKickSuccess struct {
	Unused byte
}

func (m SNSPartyKickSuccess) Token() string  { return "SNSPartyKickSuccess" }
func (m *SNSPartyKickSuccess) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyKickSuccess) Stream(s *EasyStream) error {
	return s.StreamByte(&m.Unused)
}

func (m SNSPartyKickSuccess) String() string { return "SNSPartyKickSuccess()" }

// SNSPartyKickFailure is sent when a kick attempt fails.
type SNSPartyKickFailure struct {
	ErrorCode uint8
}

func (m SNSPartyKickFailure) Token() string  { return "SNSPartyKickFailure" }
func (m *SNSPartyKickFailure) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyKickFailure) Stream(s *EasyStream) error {
	return s.StreamByte(&m.ErrorCode)
}

func (m SNSPartyKickFailure) String() string {
	return fmt.Sprintf("SNSPartyKickFailure(error=%d)", m.ErrorCode)
}

// SNSPartyPassSuccess is sent when ownership transfer succeeds.
type SNSPartyPassSuccess struct {
	Unused byte
}

func (m SNSPartyPassSuccess) Token() string  { return "SNSPartyPassSuccess" }
func (m *SNSPartyPassSuccess) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyPassSuccess) Stream(s *EasyStream) error {
	return s.StreamByte(&m.Unused)
}

func (m SNSPartyPassSuccess) String() string { return "SNSPartyPassSuccess()" }

// SNSPartyPassFailure is sent when ownership transfer fails.
type SNSPartyPassFailure struct {
	ErrorCode uint8
}

func (m SNSPartyPassFailure) Token() string  { return "SNSPartyPassFailure" }
func (m *SNSPartyPassFailure) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyPassFailure) Stream(s *EasyStream) error {
	return s.StreamByte(&m.ErrorCode)
}

func (m SNSPartyPassFailure) String() string {
	return fmt.Sprintf("SNSPartyPassFailure(error=%d)", m.ErrorCode)
}

// SNSPartyLockSuccess is sent when a lock succeeds.
type SNSPartyLockSuccess struct {
	PartyID uint64
}

func (m SNSPartyLockSuccess) Token() string  { return "SNSPartyLockSuccess" }
func (m *SNSPartyLockSuccess) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyLockSuccess) Stream(s *EasyStream) error {
	return s.StreamNumber(binary.LittleEndian, &m.PartyID)
}

func (m SNSPartyLockSuccess) String() string {
	return fmt.Sprintf("SNSPartyLockSuccess(party=%016x)", m.PartyID)
}

// SNSPartyLockFailure is sent when a lock attempt fails.
type SNSPartyLockFailure struct {
	ErrorCode uint8
}

func (m SNSPartyLockFailure) Token() string  { return "SNSPartyLockFailure" }
func (m *SNSPartyLockFailure) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyLockFailure) Stream(s *EasyStream) error {
	return s.StreamByte(&m.ErrorCode)
}

func (m SNSPartyLockFailure) String() string {
	return fmt.Sprintf("SNSPartyLockFailure(error=%d)", m.ErrorCode)
}

// SNSPartyUnlockSuccess is sent when an unlock succeeds.
type SNSPartyUnlockSuccess struct {
	PartyID uint64
}

func (m SNSPartyUnlockSuccess) Token() string  { return "SNSPartyUnlockSuccess" }
func (m *SNSPartyUnlockSuccess) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyUnlockSuccess) Stream(s *EasyStream) error {
	return s.StreamNumber(binary.LittleEndian, &m.PartyID)
}

func (m SNSPartyUnlockSuccess) String() string {
	return fmt.Sprintf("SNSPartyUnlockSuccess(party=%016x)", m.PartyID)
}

// SNSPartyUnlockFailure is sent when an unlock attempt fails.
type SNSPartyUnlockFailure struct {
	ErrorCode uint8
}

func (m SNSPartyUnlockFailure) Token() string  { return "SNSPartyUnlockFailure" }
func (m *SNSPartyUnlockFailure) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyUnlockFailure) Stream(s *EasyStream) error {
	return s.StreamByte(&m.ErrorCode)
}

func (m SNSPartyUnlockFailure) String() string {
	return fmt.Sprintf("SNSPartyUnlockFailure(error=%d)", m.ErrorCode)
}

// SNSPartyInviteNotify is sent when a party invite is received.
type SNSPartyInviteNotify struct {
	PartyID   uint64
	InviterID uint64
}

func (m SNSPartyInviteNotify) Token() string  { return "SNSPartyInviteNotify" }
func (m *SNSPartyInviteNotify) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyInviteNotify) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PartyID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.InviterID) },
	})
}

func (m SNSPartyInviteNotify) String() string {
	return fmt.Sprintf("SNSPartyInviteNotify(party=%016x, inviter=%016x)", m.PartyID, m.InviterID)
}

// SNSPartyInviteListResponse is the server's response with the pending invite list.
type SNSPartyInviteListResponse struct {
	Count uint32
}

func (m SNSPartyInviteListResponse) Token() string  { return "SNSPartyInviteListResponse" }
func (m *SNSPartyInviteListResponse) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyInviteListResponse) Stream(s *EasyStream) error {
	return s.StreamNumber(binary.LittleEndian, &m.Count)
}

func (m SNSPartyInviteListResponse) String() string {
	return fmt.Sprintf("SNSPartyInviteListResponse(count=%d)", m.Count)
}

// SNSPartyUpdateSuccess is sent when a party update succeeds.
type SNSPartyUpdateSuccess struct {
	PartyID uint64
}

func (m SNSPartyUpdateSuccess) Token() string  { return "SNSPartyUpdateSuccess" }
func (m *SNSPartyUpdateSuccess) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyUpdateSuccess) Stream(s *EasyStream) error {
	return s.StreamNumber(binary.LittleEndian, &m.PartyID)
}

func (m SNSPartyUpdateSuccess) String() string {
	return fmt.Sprintf("SNSPartyUpdateSuccess(party=%016x)", m.PartyID)
}

// SNSPartyUpdateFailure is sent when a party update fails.
type SNSPartyUpdateFailure struct {
	ErrorCode uint8
}

func (m SNSPartyUpdateFailure) Token() string  { return "SNSPartyUpdateFailure" }
func (m *SNSPartyUpdateFailure) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyUpdateFailure) Stream(s *EasyStream) error {
	return s.StreamByte(&m.ErrorCode)
}

func (m SNSPartyUpdateFailure) String() string {
	return fmt.Sprintf("SNSPartyUpdateFailure(error=%d)", m.ErrorCode)
}

// SNSPartyUpdateNotify is broadcast when party metadata changes.
type SNSPartyUpdateNotify struct {
	PartyID uint64
}

func (m SNSPartyUpdateNotify) Token() string  { return "SNSPartyUpdateNotify" }
func (m *SNSPartyUpdateNotify) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyUpdateNotify) Stream(s *EasyStream) error {
	return s.StreamNumber(binary.LittleEndian, &m.PartyID)
}

func (m SNSPartyUpdateNotify) String() string {
	return fmt.Sprintf("SNSPartyUpdateNotify(party=%016x)", m.PartyID)
}

// SNSPartyUpdateMemberSuccess is sent when a member update succeeds.
type SNSPartyUpdateMemberSuccess struct {
	PartyID uint64
}

func (m SNSPartyUpdateMemberSuccess) Token() string  { return "SNSPartyUpdateMemberSuccess" }
func (m *SNSPartyUpdateMemberSuccess) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyUpdateMemberSuccess) Stream(s *EasyStream) error {
	return s.StreamNumber(binary.LittleEndian, &m.PartyID)
}

func (m SNSPartyUpdateMemberSuccess) String() string {
	return fmt.Sprintf("SNSPartyUpdateMemberSuccess(party=%016x)", m.PartyID)
}

// SNSPartyUpdateMemberFailure is sent when a member update fails.
type SNSPartyUpdateMemberFailure struct {
	ErrorCode uint8
}

func (m SNSPartyUpdateMemberFailure) Token() string  { return "SNSPartyUpdateMemberFailure" }
func (m *SNSPartyUpdateMemberFailure) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyUpdateMemberFailure) Stream(s *EasyStream) error {
	return s.StreamByte(&m.ErrorCode)
}

func (m SNSPartyUpdateMemberFailure) String() string {
	return fmt.Sprintf("SNSPartyUpdateMemberFailure(error=%d)", m.ErrorCode)
}

// SNSPartyUpdateMemberNotify is broadcast when a member's data changes.
type SNSPartyUpdateMemberNotify struct {
	PartyID  uint64
	MemberID uint64
}

func (m SNSPartyUpdateMemberNotify) Token() string  { return "SNSPartyUpdateMemberNotify" }
func (m *SNSPartyUpdateMemberNotify) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSPartyUpdateMemberNotify) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.PartyID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.MemberID) },
	})
}

func (m SNSPartyUpdateMemberNotify) String() string {
	return fmt.Sprintf("SNSPartyUpdateMemberNotify(party=%016x, member=%016x)", m.PartyID, m.MemberID)
}
