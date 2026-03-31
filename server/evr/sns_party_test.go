package evr

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// helpers to build binary payloads for testing

func buildPayload28(routingID uint64, userUUID [16]byte, sessionGUID, targetParam uint64) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, routingID)
	binary.Write(buf, binary.LittleEndian, userUUID)
	binary.Write(buf, binary.LittleEndian, sessionGUID)
	binary.Write(buf, binary.LittleEndian, targetParam)
	return buf.Bytes()
}

func buildPayload30(localUUID, targetUUID [16]byte, sessionGUID uint64, param, reserved uint32) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, localUUID)
	binary.Write(buf, binary.LittleEndian, targetUUID)
	binary.Write(buf, binary.LittleEndian, sessionGUID)
	binary.Write(buf, binary.LittleEndian, param)
	binary.Write(buf, binary.LittleEndian, reserved)
	return buf.Bytes()
}

var (
	testUUID1 = [16]byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08,
		0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x10}
	testUUID2 = [16]byte{0xa1, 0xa2, 0xa3, 0xa4, 0xa5, 0xa6, 0xa7, 0xa8,
		0xa9, 0xaa, 0xab, 0xac, 0xad, 0xae, 0xaf, 0xb0}
)

// ---------------------------------------------------------------------------
// 0x28-byte message tests
// ---------------------------------------------------------------------------

func TestSNSPartyJoinRequest_UnmarshalMarshalRoundTrip(t *testing.T) {
	data := buildPayload28(0xDEADBEEF, testUUID1, 0x1122334455667788, 0xAABBCCDDEEFF0011)

	m := &SNSPartyJoinRequest{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if m.RoutingID != 0xDEADBEEF {
		t.Errorf("RoutingID = %x, want %x", m.RoutingID, uint64(0xDEADBEEF))
	}
	if m.LocalUserUUID != testUUID1 {
		t.Errorf("LocalUserUUID = %x, want %x", m.LocalUserUUID, testUUID1)
	}
	if m.SessionGUID != 0x1122334455667788 {
		t.Errorf("SessionGUID = %x, want %x", m.SessionGUID, uint64(0x1122334455667788))
	}
	if m.PartyID != 0xAABBCCDDEEFF0011 {
		t.Errorf("PartyID = %x, want %x", m.PartyID, uint64(0xAABBCCDDEEFF0011))
	}

	// Round-trip
	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch:\n  got  %x\n  want %x", enc.w.Bytes(), data)
	}
}

func TestSNSPartyLeaveRequest_UnmarshalMarshalRoundTrip(t *testing.T) {
	data := buildPayload28(0x11, testUUID1, 0x22, 0x33)

	m := &SNSPartyLeaveRequest{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.RoutingID != 0x11 || m.SessionGUID != 0x22 || m.TargetParam != 0x33 {
		t.Errorf("unexpected fields: %+v", m)
	}

	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch")
	}
}

func TestSNSPartySendInviteRequest_UnmarshalMarshalRoundTrip(t *testing.T) {
	data := buildPayload28(0x44, testUUID2, 0x55, 0x66)

	m := &SNSPartySendInviteRequest{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.TargetUserID != 0x66 {
		t.Errorf("TargetUserID = %x, want %x", m.TargetUserID, 0x66)
	}

	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch")
	}
}

func TestSNSPartyLockRequest_UnmarshalMarshalRoundTrip(t *testing.T) {
	data := buildPayload28(0x77, testUUID1, 0x88, 0x99)

	m := &SNSPartyLockRequest{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.TargetParam != 0x99 {
		t.Errorf("TargetParam = %x, want %x", m.TargetParam, 0x99)
	}

	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch")
	}
}

func TestSNSPartyUnlockRequest_UnmarshalMarshalRoundTrip(t *testing.T) {
	data := buildPayload28(0xAA, testUUID2, 0xBB, 0xCC)

	m := &SNSPartyUnlockRequest{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.TargetParam != 0xCC {
		t.Errorf("TargetParam = %x, want %x", m.TargetParam, 0xCC)
	}

	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch")
	}
}

// ---------------------------------------------------------------------------
// 0x30-byte message tests
// ---------------------------------------------------------------------------

func TestSNSPartyKickRequest_UnmarshalMarshalRoundTrip(t *testing.T) {
	data := buildPayload30(testUUID1, testUUID2, 0xDEAD, 42, 0)

	m := &SNSPartyKickRequest{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if m.LocalUserUUID != testUUID1 {
		t.Errorf("LocalUserUUID mismatch")
	}
	if m.TargetUserUUID != testUUID2 {
		t.Errorf("TargetUserUUID mismatch")
	}
	if m.SessionGUID != 0xDEAD {
		t.Errorf("SessionGUID = %x, want %x", m.SessionGUID, 0xDEAD)
	}
	if m.Param != 42 {
		t.Errorf("Param = %d, want 42", m.Param)
	}

	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch")
	}
}

func TestSNSPartyPassOwnershipRequest_UnmarshalMarshalRoundTrip(t *testing.T) {
	data := buildPayload30(testUUID2, testUUID1, 0xBEEF, 7, 0)

	m := &SNSPartyPassOwnershipRequest{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if m.LocalUserUUID != testUUID2 {
		t.Errorf("LocalUserUUID mismatch")
	}
	if m.TargetUserUUID != testUUID1 {
		t.Errorf("TargetUserUUID mismatch")
	}
	if m.Param != 7 {
		t.Errorf("Param = %d, want 7", m.Param)
	}

	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch")
	}
}

func TestSNSPartyRespondToInviteRequest_UnmarshalMarshalRoundTrip(t *testing.T) {
	// Test accept (param=1)
	data := buildPayload30(testUUID1, testUUID2, 0xCAFE, 1, 0)

	m := &SNSPartyRespondToInviteRequest{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if m.Param != 1 {
		t.Errorf("Param = %d, want 1 (accept)", m.Param)
	}

	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch")
	}

	// Test reject (param=0)
	data2 := buildPayload30(testUUID1, testUUID2, 0xCAFE, 0, 0)
	m2 := &SNSPartyRespondToInviteRequest{}
	s2 := NewEasyStream(DecodeMode, data2)
	if err := m2.Stream(s2); err != nil {
		t.Fatalf("Unmarshal reject: %v", err)
	}
	if m2.Param != 0 {
		t.Errorf("Param = %d, want 0 (reject)", m2.Param)
	}
}

// ---------------------------------------------------------------------------
// Symbol tests — verify Token() hashes match the registered SymbolTypes
// ---------------------------------------------------------------------------

func TestSNSPartySymbols(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		hash uint64
	}{
		// Client requests
		{"SNSPartyJoinRequest", &SNSPartyJoinRequest{}, 0xb57b22cc5352e00c},
		{"SNSPartyLeaveRequest", &SNSPartyLeaveRequest{}, 0xb77b0be7a94a9fb6},
		{"SNSPartySendInviteRequest", &SNSPartySendInviteRequest{}, 0xcf13f934540b5f5e},
		{"SNSPartyLockRequest", &SNSPartyLockRequest{}, 0xc2478aa479f3e16a},
		{"SNSPartyUnlockRequest", &SNSPartyUnlockRequest{}, 0x5a4e99802fa3d704},
		{"SNSPartyKickRequest", &SNSPartyKickRequest{}, 0xfaf57beb59917d64},
		{"SNSPartyPassOwnershipRequest", &SNSPartyPassOwnershipRequest{}, 0x518543cd886a6946},
		{"SNSPartyRespondToInviteRequest", &SNSPartyRespondToInviteRequest{}, 0xe3654a09203555a3},
		// Server responses/notifications
		{"SNSPartyJoinSuccess", &SNSPartyJoinSuccess{}, 0xb57a32de4552e00b},
		{"SNSPartyJoinFailure", &SNSPartyJoinFailure{}, 0xb56f26d44a42e11d},
		{"SNSPartyLeaveSuccess", &SNSPartyLeaveSuccess{}, 0xb77a1bf5bf4a9fb1},
		{"SNSPartyLeaveNotify", &SNSPartyLeaveNotify{}, 0x05315abefc8f804b},
		{"SNSPartyKickNotify", &SNSPartyKickNotify{}, 0x28cb04891f93dc81},
		{"SNSPartyPassNotify", &SNSPartyPassNotify{}, 0x9d946c88d5a8aca5},
		{"SNSPartyLockNotify", &SNSPartyLockNotify{}, 0x93a6b1a6cd4ef8dd},
		{"SNSPartyUnlockNotify", &SNSPartyUnlockNotify{}, 0xd8cfd3795010481f},
		// Additional requests
		{"SNSPartyCreateRequest", &SNSPartyCreateRequest{}, 0x0b7bd21332523994},
		{"SNSPartyUpdateRequest", &SNSPartyUpdateRequest{}, 0xdee761a021a5278a},
		{"SNSPartyUpdateMemberRequest", &SNSPartyUpdateMemberRequest{}, 0x4edeeb8ddecc8736},
		{"SNSPartyInviteListRefreshRequest", &SNSPartyInviteListRefreshRequest{}, 0xd8cbc44959e25da8},
		// Additional responses/notifications
		{"SNSPartyCreateSuccess", &SNSPartyCreateSuccess{}, 0x0b7ac20124523993},
		{"SNSPartyCreateFailure", &SNSPartyCreateFailure{}, 0x0b6fd60b2b423885},
		{"SNSPartyJoinNotify", &SNSPartyJoinNotify{}, 0xcc38103e64879e53},
		{"SNSPartyLeaveFailure", &SNSPartyLeaveFailure{}, 0xb76f0fffb05a9ea7},
		{"SNSPartyKickSuccess", &SNSPartyKickSuccess{}, 0xfaf46bf94f917d63},
		{"SNSPartyKickFailure", &SNSPartyKickFailure{}, 0xfae17ff340817c75},
		{"SNSPartyPassSuccess", &SNSPartyPassSuccess{}, 0x518453df9e6a6941},
		{"SNSPartyPassFailure", &SNSPartyPassFailure{}, 0x519147d5917a6857},
		{"SNSPartyLockSuccess", &SNSPartyLockSuccess{}, 0xc2469ab66ff3e16d},
		{"SNSPartyLockFailure", &SNSPartyLockFailure{}, 0xc2538ebc60e3e07b},
		{"SNSPartyUnlockSuccess", &SNSPartyUnlockSuccess{}, 0x5a4f899239a3d703},
		{"SNSPartyUnlockFailure", &SNSPartyUnlockFailure{}, 0x5a5a9d9836b3d615},
		{"SNSPartyInviteNotify", &SNSPartyInviteNotify{}, 0x218f721f09026dab},
		{"SNSPartyInviteListResponse", &SNSPartyInviteListResponse{}, 0x685a5fb8447b1155},
		{"SNSPartyUpdateSuccess", &SNSPartyUpdateSuccess{}, 0xdee671b237a5278d},
		{"SNSPartyUpdateFailure", &SNSPartyUpdateFailure{}, 0xdef365b838b5269b},
		{"SNSPartyUpdateNotify", &SNSPartyUpdateNotify{}, 0x23c834cb3bc6ecf5},
		{"SNSPartyUpdateMemberSuccess", &SNSPartyUpdateMemberSuccess{}, 0x4edffb9fc8cc8731},
		{"SNSPartyUpdateMemberFailure", &SNSPartyUpdateMemberFailure{}, 0x4ecaef95c7dc8627},
		{"SNSPartyUpdateMemberNotify", &SNSPartyUpdateMemberNotify{}, 0x451eb6ca40dde289},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sym := SymbolOf(tt.msg)
			if uint64(sym) != tt.hash {
				t.Errorf("SymbolOf(%s) = 0x%016x, want 0x%016x", tt.name, uint64(sym), tt.hash)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// NewMessageFromHash tests
// ---------------------------------------------------------------------------

func TestSNSPartyNewMessageFromHash(t *testing.T) {
	hashes := []uint64{
		0xb57b22cc5352e00c,
		0xb77b0be7a94a9fb6,
		0xcf13f934540b5f5e,
		0xc2478aa479f3e16a,
		0x5a4e99802fa3d704,
		0xfaf57beb59917d64,
		0x518543cd886a6946,
		0xe3654a09203555a3,
		0xb57a32de4552e00b,
		0xb56f26d44a42e11d,
		0xb77a1bf5bf4a9fb1,
		0x05315abefc8f804b,
		0x28cb04891f93dc81,
		0x9d946c88d5a8aca5,
		0x93a6b1a6cd4ef8dd,
		0xd8cfd3795010481f,
		// Additional
		0x0b7bd21332523994,
		0xdee761a021a5278a,
		0x4edeeb8ddecc8736,
		0xd8cbc44959e25da8,
		0x0b7ac20124523993,
		0x0b6fd60b2b423885,
		0xcc38103e64879e53,
		0xb76f0fffb05a9ea7,
		0xfaf46bf94f917d63,
		0xfae17ff340817c75,
		0x518453df9e6a6941,
		0x519147d5917a6857,
		0xc2469ab66ff3e16d,
		0xc2538ebc60e3e07b,
		0x5a4f899239a3d703,
		0x5a5a9d9836b3d615,
		0x218f721f09026dab,
		0x685a5fb8447b1155,
		0xdee671b237a5278d,
		0xdef365b838b5269b,
		0x23c834cb3bc6ecf5,
		0x4edffb9fc8cc8731,
		0x4ecaef95c7dc8627,
		0x451eb6ca40dde289,
	}

	for _, h := range hashes {
		msg := NewMessageFromHash(h)
		if msg == nil {
			t.Errorf("NewMessageFromHash(0x%016x) returned nil", h)
		}
	}
}

// ---------------------------------------------------------------------------
// Response type round-trip tests
// ---------------------------------------------------------------------------

func TestSNSPartyJoinSuccess_RoundTrip(t *testing.T) {
	m := &SNSPartyJoinSuccess{PartyID: 0x1234, OwnerID: 0x5678}
	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	m2 := &SNSPartyJoinSuccess{}
	dec := NewEasyStream(DecodeMode, enc.w.Bytes())
	if err := m2.Stream(dec); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m2.PartyID != 0x1234 || m2.OwnerID != 0x5678 {
		t.Errorf("mismatch: %+v", m2)
	}
}

func TestSNSPartyJoinFailure_RoundTrip(t *testing.T) {
	m := &SNSPartyJoinFailure{PartyID: 0xABCD, ErrorCode: 3}
	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	m2 := &SNSPartyJoinFailure{}
	dec := NewEasyStream(DecodeMode, enc.w.Bytes())
	if err := m2.Stream(dec); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m2.PartyID != 0xABCD || m2.ErrorCode != 3 {
		t.Errorf("mismatch: %+v", m2)
	}
}

func TestSNSPartyLeaveNotify_RoundTrip(t *testing.T) {
	m := &SNSPartyLeaveNotify{PartyID: 0x11, MemberID: 0x22}
	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	m2 := &SNSPartyLeaveNotify{}
	dec := NewEasyStream(DecodeMode, enc.w.Bytes())
	if err := m2.Stream(dec); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m2.PartyID != 0x11 || m2.MemberID != 0x22 {
		t.Errorf("mismatch: %+v", m2)
	}
}

func TestSNSPartyLockNotify_RoundTrip(t *testing.T) {
	m := &SNSPartyLockNotify{PartyID: 0xFF}
	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	m2 := &SNSPartyLockNotify{}
	dec := NewEasyStream(DecodeMode, enc.w.Bytes())
	if err := m2.Stream(dec); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m2.PartyID != 0xFF {
		t.Errorf("mismatch: %+v", m2)
	}
}
