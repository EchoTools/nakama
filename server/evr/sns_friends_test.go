package evr

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// ---------------------------------------------------------------------------
// 0x28-byte outgoing message round-trip tests
// ---------------------------------------------------------------------------

func TestSNSFriendInviteRequest_RoundTrip(t *testing.T) {
	data := buildPayload28(0xDEADBEEF, testUUID1, 0x1122334455667788, 0xAABBCCDD)

	m := &SNSFriendInviteRequest{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if m.RoutingID != 0xDEADBEEF {
		t.Errorf("RoutingID = %x, want %x", m.RoutingID, uint64(0xDEADBEEF))
	}
	if m.LocalUserUUID != testUUID1 {
		t.Errorf("LocalUserUUID mismatch")
	}
	if m.SessionGUID != 0x1122334455667788 {
		t.Errorf("SessionGUID = %x, want %x", m.SessionGUID, uint64(0x1122334455667788))
	}
	if m.TargetUserID != 0xAABBCCDD {
		t.Errorf("TargetUserID = %x, want %x", m.TargetUserID, uint64(0xAABBCCDD))
	}

	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch:\n  got  %x\n  want %x", enc.w.Bytes(), data)
	}
}

func TestSNSFriendAcceptRequest_RoundTrip(t *testing.T) {
	data := buildPayload28(0xFFFFFFFFFFFFFFFF, testUUID2, 0x11, 0x22)

	m := &SNSFriendAcceptRequest{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.RoutingID != 0xFFFFFFFFFFFFFFFF {
		t.Errorf("RoutingID = %x, want sentinel", m.RoutingID)
	}
	if m.TargetUserID != 0x22 {
		t.Errorf("TargetUserID = %x, want %x", m.TargetUserID, 0x22)
	}

	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch")
	}
}

func TestSNSFriendRemoveRequest_RoundTrip(t *testing.T) {
	data := buildPayload28(0x33, testUUID1, 0x44, 0x55)

	m := &SNSFriendRemoveRequest{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.TargetUserID != 0x55 {
		t.Errorf("TargetUserID = %x, want %x", m.TargetUserID, 0x55)
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
// 0x20-byte inbound: SNSFriendListResponse
// ---------------------------------------------------------------------------

func buildFriendListResponse(header uint64, noffline, nbusy, nonline, nsent, nrecv uint32) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, header)
	binary.Write(buf, binary.LittleEndian, noffline)
	binary.Write(buf, binary.LittleEndian, nbusy)
	binary.Write(buf, binary.LittleEndian, nonline)
	binary.Write(buf, binary.LittleEndian, nsent)
	binary.Write(buf, binary.LittleEndian, nrecv)
	return buf.Bytes()
}

func TestSNSFriendListResponse_RoundTrip(t *testing.T) {
	data := buildFriendListResponse(0xAA, 5, 3, 10, 2, 1)

	m := &SNSFriendListResponse{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if m.Header != 0xAA {
		t.Errorf("Header = %x, want %x", m.Header, 0xAA)
	}
	if m.NOffline != 5 {
		t.Errorf("NOffline = %d, want 5", m.NOffline)
	}
	if m.NBusy != 3 {
		t.Errorf("NBusy = %d, want 3", m.NBusy)
	}
	if m.NOnline != 10 {
		t.Errorf("NOnline = %d, want 10", m.NOnline)
	}
	if m.NSent != 2 {
		t.Errorf("NSent = %d, want 2", m.NSent)
	}
	if m.NRecv != 1 {
		t.Errorf("NRecv = %d, want 1", m.NRecv)
	}

	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch:\n  got  %x\n  want %x", enc.w.Bytes(), data)
	}
}

// ---------------------------------------------------------------------------
// 0x18-byte inbound: friend notify payload tests
// ---------------------------------------------------------------------------

func buildFriendNotifyPayload(header, friendID uint64, statusCode uint8) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, header)
	binary.Write(buf, binary.LittleEndian, friendID)
	binary.Write(buf, binary.LittleEndian, statusCode)
	return buf.Bytes()
}

func TestSNSFriendStatusNotify_RoundTrip(t *testing.T) {
	data := buildFriendNotifyPayload(0x01, 0x1234567890ABCDEF, 2)

	m := &SNSFriendStatusNotify{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.FriendID != 0x1234567890ABCDEF {
		t.Errorf("FriendID = %x, want %x", m.FriendID, uint64(0x1234567890ABCDEF))
	}
	if m.StatusCode != 2 {
		t.Errorf("StatusCode = %d, want 2", m.StatusCode)
	}

	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch")
	}
}

func TestSNSFriendInviteFailure_RoundTrip(t *testing.T) {
	data := buildFriendNotifyPayload(0x02, 0xAABB, FriendInviteErrorAlready)

	m := &SNSFriendInviteFailure{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.FriendID != 0xAABB {
		t.Errorf("FriendID = %x, want %x", m.FriendID, 0xAABB)
	}
	if m.StatusCode != FriendInviteErrorAlready {
		t.Errorf("StatusCode = %d, want %d", m.StatusCode, FriendInviteErrorAlready)
	}

	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch")
	}
}

func TestSNSFriendAcceptFailure_RoundTrip(t *testing.T) {
	data := buildFriendNotifyPayload(0x03, 0xCCDD, FriendAcceptErrorWithdrawn)

	m := &SNSFriendAcceptFailure{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.StatusCode != FriendAcceptErrorWithdrawn {
		t.Errorf("StatusCode = %d, want %d", m.StatusCode, FriendAcceptErrorWithdrawn)
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
// 0x10-byte inbound: friend ID payload tests
// ---------------------------------------------------------------------------

func buildFriendIdPayload(header, friendID uint64) []byte {
	buf := new(bytes.Buffer)
	binary.Write(buf, binary.LittleEndian, header)
	binary.Write(buf, binary.LittleEndian, friendID)
	return buf.Bytes()
}

func TestSNSFriendInviteSuccess_RoundTrip(t *testing.T) {
	data := buildFriendIdPayload(0x10, 0xDEAD)

	m := &SNSFriendInviteSuccess{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.FriendID != 0xDEAD {
		t.Errorf("FriendID = %x, want %x", m.FriendID, 0xDEAD)
	}

	enc := NewEasyStream(EncodeMode, []byte{})
	if err := m.Stream(enc); err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !bytes.Equal(enc.w.Bytes(), data) {
		t.Errorf("round-trip mismatch")
	}
}

func TestSNSFriendRemoveNotify_RoundTrip(t *testing.T) {
	data := buildFriendIdPayload(0x20, 0xBEEF)

	m := &SNSFriendRemoveNotify{}
	s := NewEasyStream(DecodeMode, data)
	if err := m.Stream(s); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if m.FriendID != 0xBEEF {
		t.Errorf("FriendID = %x, want %x", m.FriendID, 0xBEEF)
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
// Symbol tests — verify Token() hashes match registered SymbolTypes
// ---------------------------------------------------------------------------

func TestSNSFriendSymbols(t *testing.T) {
	tests := []struct {
		name string
		msg  Message
		hash uint64
	}{
		// Client requests
		{"SNSFriendInviteRequest", &SNSFriendInviteRequest{}, 0x7f0d7a28de3c6f70},
		{"SNSFriendAcceptRequest", &SNSFriendAcceptRequest{}, 0x1bbcb7e810af4620},
		{"SNSFriendRemoveRequest", &SNSFriendRemoveRequest{}, 0x78908988b7fe6db4},
		// Server responses/notifications
		{"SNSFriendListResponse", &SNSFriendListResponse{}, 0xa78aeb2a4e89b10b},
		{"SNSFriendStatusNotify", &SNSFriendStatusNotify{}, 0x26a19dc4d2d5579d},
		{"SNSFriendInviteSuccess", &SNSFriendInviteSuccess{}, 0x7f0c6a3ac83c6f77},
		{"SNSFriendInviteFailure", &SNSFriendInviteFailure{}, 0x7f197e30c72c6e61},
		{"SNSFriendInviteNotify", &SNSFriendInviteNotify{}, 0xca09b0b36bd981b7},
		{"SNSFriendAcceptSuccess", &SNSFriendAcceptSuccess{}, 0x1bbda7fa06af4627},
		{"SNSFriendAcceptFailure", &SNSFriendAcceptFailure{}, 0x1ba8b3f009bf4731},
		{"SNSFriendAcceptNotify", &SNSFriendAcceptNotify{}, 0xc237c84c31d3ae05},
		{"SNSFriendRemoveResponse", &SNSFriendRemoveResponse{}, 0xc2bf83a08ea3a955},
		{"SNSFriendRemoveNotify", &SNSFriendRemoveNotify{}, 0xe06972f49cd72265},
		{"SNSFriendWithdrawnNotify", &SNSFriendWithdrawnNotify{}, 0x191aa30801ec6d03},
		{"SNSFriendRejectNotify", &SNSFriendRejectNotify{}, 0xb9b86c0ce8e8d0c1},
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

func TestSNSFriendNewMessageFromHash(t *testing.T) {
	hashes := []uint64{
		0x7f0d7a28de3c6f70,
		0x1bbcb7e810af4620,
		0x78908988b7fe6db4,
		0xa78aeb2a4e89b10b,
		0x26a19dc4d2d5579d,
		0x7f0c6a3ac83c6f77,
		0x7f197e30c72c6e61,
		0xca09b0b36bd981b7,
		0x1bbda7fa06af4627,
		0x1ba8b3f009bf4731,
		0xc237c84c31d3ae05,
		0xc2bf83a08ea3a955,
		0xe06972f49cd72265,
		0x191aa30801ec6d03,
		0xb9b86c0ce8e8d0c1,
	}

	for _, h := range hashes {
		msg := NewMessageFromHash(h)
		if msg == nil {
			t.Errorf("NewMessageFromHash(0x%016x) returned nil", h)
		}
	}
}
