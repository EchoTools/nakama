package evr

import (
	"encoding/binary"
	"fmt"
)

// SNSRecentlyMetRefreshRequest is sent by a nevr-runtime client (social level >= 1) when the game asks
// for its recently-met users (social slot 57, R15NetRefreshRecentlyMetUsersNode). The 0x20 header the
// friend list refresh uses: RoutingID(8) LocalUserUUID(16) SessionGUID(8).
type SNSRecentlyMetRefreshRequest struct {
	RoutingID     uint64
	LocalUserUUID [16]byte
	SessionGUID   uint64
}

func (m SNSRecentlyMetRefreshRequest) Token() string   { return "SNSRecentlyMetRefreshRequest" }
func (m *SNSRecentlyMetRefreshRequest) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSRecentlyMetRefreshRequest) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.RoutingID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.LocalUserUUID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.SessionGUID) },
	})
}

func (m SNSRecentlyMetRefreshRequest) String() string { return "SNSRecentlyMetRefreshRequest()" }

// RecentlyMetEntry is one recently-met user as the client shows them: the account id (the Discord id),
// their joinable party (0 when none or not joinable for the viewer), the friend status code (0 online,
// 2 offline), the name, and the presence text (empty when offline).
type RecentlyMetEntry struct {
	AccountID uint64
	PartyID   uint64
	Joinable  uint8
	Status    uint8
	Name      []byte
	Text      []byte
}

// SNSRecentlyMetListResponse answers SNSRecentlyMetRefreshRequest with the whole list, newest meeting
// first. Wire format: Count(4), then per entry AccountID(8) PartyID(8) Joinable(1) Status(1)
// Reserved(6) NameLen(2) Name TextLen(2) Text.
type SNSRecentlyMetListResponse struct {
	Entries []RecentlyMetEntry
}

func (m SNSRecentlyMetListResponse) Token() string   { return "SNSRecentlyMetListResponse" }
func (m *SNSRecentlyMetListResponse) Symbol() Symbol { return ToSymbol(m.Token()) }

func (m *SNSRecentlyMetListResponse) Stream(s *EasyStream) error {
	count := uint32(len(m.Entries))
	if err := s.StreamNumber(binary.LittleEndian, &count); err != nil {
		return err
	}
	if s.Mode == DecodeMode {
		if count > 1024 {
			return fmt.Errorf("recently met list of %d entries", count)
		}
		m.Entries = make([]RecentlyMetEntry, count)
	}
	for i := range m.Entries {
		e := &m.Entries[i]
		var reserved [6]byte
		nameLen, textLen := uint16(len(e.Name)), uint16(len(e.Text))
		if err := RunErrorFunctions([]func() error{
			func() error { return s.StreamNumber(binary.LittleEndian, &e.AccountID) },
			func() error { return s.StreamNumber(binary.LittleEndian, &e.PartyID) },
			func() error { return s.StreamByte(&e.Joinable) },
			func() error { return s.StreamByte(&e.Status) },
			func() error { return s.StreamNumber(binary.LittleEndian, &reserved) },
			func() error { return s.StreamNumber(binary.LittleEndian, &nameLen) },
			func() error { return s.StreamBytes(&e.Name, int(nameLen)) },
			func() error { return s.StreamNumber(binary.LittleEndian, &textLen) },
			func() error { return s.StreamBytes(&e.Text, int(textLen)) },
		}); err != nil {
			return err
		}
	}
	return nil
}

func (m SNSRecentlyMetListResponse) String() string {
	return fmt.Sprintf("SNSRecentlyMetListResponse(entries=%d)", len(m.Entries))
}
