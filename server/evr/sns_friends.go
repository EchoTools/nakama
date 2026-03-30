package evr

import (
	"encoding/binary"
	"fmt"
)

// SNS Friends messages: client → server requests and server → client responses.
//
// Three binary payload formats exist for outgoing (client → server):
//
//   0x28-byte "SNSFriendsActionPayload" (RoutingID + LocalUserUUID + SessionGUID + TargetUserID):
//     - SNSFriendInviteRequest (may append a name string for add-by-name)
//     - SNSFriendAcceptRequest
//     - SNSFriendRemoveRequest
//
// Three inbound (server → client) payload formats:
//
//   0x20-byte (Header + 5×uint32 counts + reserved):
//     - SNSFriendListResponse
//
//   0x18-byte (Header + FriendID + StatusCode):
//     - SNSFriendStatusNotify, SNSFriendInviteFailure,
//       SNSFriendAcceptSuccess, SNSFriendAcceptFailure, SNSFriendAcceptNotify
//
//   0x10-byte (Header + FriendID):
//     - SNSFriendInviteSuccess, SNSFriendInviteNotify,
//       SNSFriendRemoveResponse, SNSFriendRemoveNotify,
//       SNSFriendWithdrawnNotify, SNSFriendRejectNotify

// ---------------------------------------------------------------------------
// Error code enumerations
// ---------------------------------------------------------------------------

// FriendInviteError codes returned in SNSFriendInviteFailure.StatusCode.
const (
	FriendInviteErrorBadRequest uint8 = 0
	FriendInviteErrorNotFound   uint8 = 1
	FriendInviteErrorSelf       uint8 = 2
	FriendInviteErrorAlready    uint8 = 3
	FriendInviteErrorPending    uint8 = 4
	FriendInviteErrorFull       uint8 = 5
)

// FriendAcceptError codes returned in SNSFriendAcceptFailure.StatusCode.
const (
	FriendAcceptErrorNotFound  uint8 = 0
	FriendAcceptErrorAlready   uint8 = 1
	FriendAcceptErrorWithdrawn uint8 = 2
	FriendAcceptErrorFull      uint8 = 3
)

// ---------------------------------------------------------------------------
// 0x28-byte outgoing messages (client → server)
// ---------------------------------------------------------------------------

// SNSFriendInviteRequest is sent to invite a user as a friend (by ID or by name).
// When adding by name, TargetUserID is 0 and the name string is appended after
// the fixed payload.
type SNSFriendInviteRequest struct {
	RoutingID     uint64
	LocalUserUUID [16]byte
	SessionGUID   uint64
	TargetUserID  uint64
}

func (m SNSFriendInviteRequest) Token() string   { return "SNSFriendInviteRequest" }
func (m *SNSFriendInviteRequest) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendInviteRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoutingID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetUserID) },
	})
}

func (m SNSFriendInviteRequest) String() string {
	return fmt.Sprintf("SNSFriendInviteRequest(routing=%016x, user=%x, session=%016x, target=%016x)",
		m.RoutingID, m.LocalUserUUID, m.SessionGUID, m.TargetUserID)
}

// SNSFriendAcceptRequest is sent to accept a pending friend request.
type SNSFriendAcceptRequest struct {
	RoutingID     uint64
	LocalUserUUID [16]byte
	SessionGUID   uint64
	TargetUserID  uint64
}

func (m SNSFriendAcceptRequest) Token() string   { return "SNSFriendAcceptRequest" }
func (m *SNSFriendAcceptRequest) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendAcceptRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoutingID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetUserID) },
	})
}

func (m SNSFriendAcceptRequest) String() string {
	return fmt.Sprintf("SNSFriendAcceptRequest(routing=%016x, user=%x, session=%016x, target=%016x)",
		m.RoutingID, m.LocalUserUUID, m.SessionGUID, m.TargetUserID)
}

// SNSFriendRemoveRequest is sent to remove a friend, reject a request, or block a user.
// The server differentiates the action by the current friendship state.
type SNSFriendRemoveRequest struct {
	RoutingID     uint64
	LocalUserUUID [16]byte
	SessionGUID   uint64
	TargetUserID  uint64
}

func (m SNSFriendRemoveRequest) Token() string   { return "SNSFriendRemoveRequest" }
func (m *SNSFriendRemoveRequest) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendRemoveRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoutingID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TargetUserID) },
	})
}

func (m SNSFriendRemoveRequest) String() string {
	return fmt.Sprintf("SNSFriendRemoveRequest(routing=%016x, user=%x, session=%016x, target=%016x)",
		m.RoutingID, m.LocalUserUUID, m.SessionGUID, m.TargetUserID)
}

// ---------------------------------------------------------------------------
// 0x20-byte inbound: friend list counts
// ---------------------------------------------------------------------------

// SNSFriendListResponse is the server's response with friend list category counts.
// Wire format: 0x20 bytes (Header + 5×uint32 counts + Reserved).
type SNSFriendListResponse struct {
	Header   uint64
	NOffline uint32
	NBusy    uint32
	NOnline  uint32
	NSent    uint32
	NRecv    uint32
	Reserved uint32
}

func (m SNSFriendListResponse) Token() string   { return "SNSFriendListResponse" }
func (m *SNSFriendListResponse) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendListResponse) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Header) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NOffline) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NBusy) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NOnline) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NSent) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.NRecv) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Reserved) },
	})
}

func (m SNSFriendListResponse) String() string {
	return fmt.Sprintf("SNSFriendListResponse(online=%d, busy=%d, offline=%d, sent=%d, recv=%d)",
		m.NOnline, m.NBusy, m.NOffline, m.NSent, m.NRecv)
}

// ---------------------------------------------------------------------------
// 0x18-byte inbound: friend ID + status/error code
// ---------------------------------------------------------------------------

// SNSFriendStatusNotify is a friend online/offline/busy status change notification.
type SNSFriendStatusNotify struct {
	Header     uint64
	FriendID   uint64
	StatusCode uint8
}

func (m SNSFriendStatusNotify) Token() string   { return "SNSFriendStatusNotify" }
func (m *SNSFriendStatusNotify) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendStatusNotify) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Header) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.FriendID) },
		func() error { return s.StreamByte(&m.StatusCode) },
	})
}

func (m SNSFriendStatusNotify) String() string {
	return fmt.Sprintf("SNSFriendStatusNotify(friend=%016x, status=%d)", m.FriendID, m.StatusCode)
}

// SNSFriendInviteFailure is sent when a friend invite fails.
// StatusCode is a FriendInviteError value.
type SNSFriendInviteFailure struct {
	Header     uint64
	FriendID   uint64
	StatusCode uint8
}

func (m SNSFriendInviteFailure) Token() string   { return "SNSFriendInviteFailure" }
func (m *SNSFriendInviteFailure) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendInviteFailure) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Header) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.FriendID) },
		func() error { return s.StreamByte(&m.StatusCode) },
	})
}

func (m SNSFriendInviteFailure) String() string {
	return fmt.Sprintf("SNSFriendInviteFailure(friend=%016x, error=%d)", m.FriendID, m.StatusCode)
}

// SNSFriendAcceptSuccess is sent when a friend accept succeeds.
type SNSFriendAcceptSuccess struct {
	Header     uint64
	FriendID   uint64
	StatusCode uint8
}

func (m SNSFriendAcceptSuccess) Token() string   { return "SNSFriendAcceptSuccess" }
func (m *SNSFriendAcceptSuccess) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendAcceptSuccess) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Header) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.FriendID) },
		func() error { return s.StreamByte(&m.StatusCode) },
	})
}

func (m SNSFriendAcceptSuccess) String() string {
	return fmt.Sprintf("SNSFriendAcceptSuccess(friend=%016x, status=%d)", m.FriendID, m.StatusCode)
}

// SNSFriendAcceptFailure is sent when a friend accept fails.
// StatusCode is a FriendAcceptError value.
type SNSFriendAcceptFailure struct {
	Header     uint64
	FriendID   uint64
	StatusCode uint8
}

func (m SNSFriendAcceptFailure) Token() string   { return "SNSFriendAcceptFailure" }
func (m *SNSFriendAcceptFailure) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendAcceptFailure) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Header) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.FriendID) },
		func() error { return s.StreamByte(&m.StatusCode) },
	})
}

func (m SNSFriendAcceptFailure) String() string {
	return fmt.Sprintf("SNSFriendAcceptFailure(friend=%016x, error=%d)", m.FriendID, m.StatusCode)
}

// SNSFriendAcceptNotify is sent when another user accepts your friend request.
type SNSFriendAcceptNotify struct {
	Header     uint64
	FriendID   uint64
	StatusCode uint8
}

func (m SNSFriendAcceptNotify) Token() string   { return "SNSFriendAcceptNotify" }
func (m *SNSFriendAcceptNotify) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendAcceptNotify) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Header) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.FriendID) },
		func() error { return s.StreamByte(&m.StatusCode) },
	})
}

func (m SNSFriendAcceptNotify) String() string {
	return fmt.Sprintf("SNSFriendAcceptNotify(friend=%016x, status=%d)", m.FriendID, m.StatusCode)
}

// ---------------------------------------------------------------------------
// 0x10-byte inbound: friend ID only
// ---------------------------------------------------------------------------

// SNSFriendInviteSuccess is sent when a friend invite is sent successfully.
type SNSFriendInviteSuccess struct {
	Header   uint64
	FriendID uint64
}

func (m SNSFriendInviteSuccess) Token() string   { return "SNSFriendInviteSuccess" }
func (m *SNSFriendInviteSuccess) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendInviteSuccess) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Header) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.FriendID) },
	})
}

func (m SNSFriendInviteSuccess) String() string {
	return fmt.Sprintf("SNSFriendInviteSuccess(friend=%016x)", m.FriendID)
}

// SNSFriendInviteNotify is sent when you receive a friend invite from another user.
type SNSFriendInviteNotify struct {
	Header   uint64
	FriendID uint64
}

func (m SNSFriendInviteNotify) Token() string   { return "SNSFriendInviteNotify" }
func (m *SNSFriendInviteNotify) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendInviteNotify) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Header) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.FriendID) },
	})
}

func (m SNSFriendInviteNotify) String() string {
	return fmt.Sprintf("SNSFriendInviteNotify(friend=%016x)", m.FriendID)
}

// SNSFriendRemoveResponse is sent when a friend remove/block succeeds.
type SNSFriendRemoveResponse struct {
	Header   uint64
	FriendID uint64
}

func (m SNSFriendRemoveResponse) Token() string   { return "SNSFriendRemoveResponse" }
func (m *SNSFriendRemoveResponse) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendRemoveResponse) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Header) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.FriendID) },
	})
}

func (m SNSFriendRemoveResponse) String() string {
	return fmt.Sprintf("SNSFriendRemoveResponse(friend=%016x)", m.FriendID)
}

// SNSFriendRemoveNotify is sent when a friend removes you.
type SNSFriendRemoveNotify struct {
	Header   uint64
	FriendID uint64
}

func (m SNSFriendRemoveNotify) Token() string   { return "SNSFriendRemoveNotify" }
func (m *SNSFriendRemoveNotify) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendRemoveNotify) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Header) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.FriendID) },
	})
}

func (m SNSFriendRemoveNotify) String() string {
	return fmt.Sprintf("SNSFriendRemoveNotify(friend=%016x)", m.FriendID)
}

// SNSFriendWithdrawnNotify is sent when a friend invite is withdrawn.
type SNSFriendWithdrawnNotify struct {
	Header   uint64
	FriendID uint64
}

func (m SNSFriendWithdrawnNotify) Token() string   { return "SNSFriendWithdrawnNotify" }
func (m *SNSFriendWithdrawnNotify) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendWithdrawnNotify) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Header) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.FriendID) },
	})
}

func (m SNSFriendWithdrawnNotify) String() string {
	return fmt.Sprintf("SNSFriendWithdrawnNotify(friend=%016x)", m.FriendID)
}

// SNSFriendRejectNotify is sent when your friend request is rejected.
type SNSFriendRejectNotify struct {
	Header   uint64
	FriendID uint64
}

func (m SNSFriendRejectNotify) Token() string   { return "SNSFriendRejectNotify" }
func (m *SNSFriendRejectNotify) Symbol() Symbol  { return ToSymbol(m.Token()) }

func (m *SNSFriendRejectNotify) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Header) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.FriendID) },
	})
}

func (m SNSFriendRejectNotify) String() string {
	return fmt.Sprintf("SNSFriendRejectNotify(friend=%016x)", m.FriendID)
}
