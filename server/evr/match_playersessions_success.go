package evr

import (
	"encoding/binary"
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
)

type LobbyEntrant struct {
	LobbyID    uuid.UUID   // Unk1, The matching-related session token for the current matchmaker operation.
	EntrantIDs []uuid.UUID // Unk1, The player session token obtained for the requested player user identifier.

	Unk0      byte      // V2, V3
	EvrID     EvrId     // V2, V3
	EntrantID uuid.UUID // V2, V3
	TeamIndex int16     // V3
	GenIndex  uint16    // V3 - Generation index for entrant state versioning/validation
	Reserved  uint32    // V3 - Reserved/padding (always 0)
}

func NewLobbyEntrant(evrId EvrId, matchingSession uuid.UUID, playerSession uuid.UUID, playerSessions []uuid.UUID, teamIndex int16) *LobbyEntrant {
	return &LobbyEntrant{
		LobbyID:    matchingSession,
		EntrantIDs: playerSessions,
		Unk0:       0xFF,
		EvrID:      evrId,
		EntrantID:  playerSession,
		TeamIndex:  teamIndex,
		GenIndex:   0,
		Reserved:   0,
	}
}

func (m LobbyEntrant) VersionU() *LobbyEntrantsV0 {
	s := LobbyEntrantsV0(m)
	return &s
}

func (m LobbyEntrant) Version2() *LobbyEntrantsV2 {
	s := LobbyEntrantsV2(m)
	return &s
}

func (m LobbyEntrant) Version3() *LobbyEntrantsV3 {
	s := LobbyEntrantsV3(m)
	return &s
}

const MaxLobbyEntrants = 16

type LobbyEntrantsV0 LobbyEntrant

func (m *LobbyEntrantsV0) Stream(s *EasyStream) error {
	count := uint64(len(m.EntrantIDs))

	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &count) },
		func() error { return s.StreamGUID(&m.LobbyID) },
		func() error {
			if s.Mode == DecodeMode {
				if count > MaxLobbyEntrants {
					return fmt.Errorf("entrant count %d exceeds maximum %d", count, MaxLobbyEntrants)
				}
				m.EntrantIDs = make([]uuid.UUID, count)
			}
			return s.StreamGUIDs(&m.EntrantIDs)
		},
	})
}

func (m *LobbyEntrantsV0) String() string {
	sessions := make([]string, len(m.EntrantIDs))
	for i, session := range m.EntrantIDs {
		sessions[i] = session.String()
	}
	return fmt.Sprintf("%T(lobby_id=%s, entrant_ids=[%s])", m, m.LobbyID, strings.Join(sessions, ", "))
}

type LobbyEntrantsV3 LobbyEntrant

func (m *LobbyEntrantsV3) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk0) },
		func() error { return s.StreamStruct(&m.EvrID) },
		func() error { return s.StreamGUID(&m.EntrantID) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.TeamIndex) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.GenIndex) },
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Reserved) },
	})
}

func (m *LobbyEntrantsV3) String() string {
	return fmt.Sprintf("%T(unk0=%d, evr_id=%v, entrant_id=%s, team_index=%d, gen_index=%d, reserved=%d)",
		m, m.Unk0, m.EvrID, m.EntrantID, m.TeamIndex, m.GenIndex, m.Reserved)
}

type LobbyEntrantsV2 LobbyEntrant

// Stream streams the message data in/out based on the streaming mode set.
func (m *LobbyEntrantsV2) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamNumber(binary.LittleEndian, &m.Unk0) },
		func() error { return s.StreamStruct(&m.EvrID) },
		func() error { return s.StreamGUID(&m.EntrantID) },
	})
}

// String returns a string representation of the LobbyPlayerSessionsSuccessv2 message.
func (m LobbyEntrantsV2) String() string {
	return fmt.Sprintf("%T(unk0=%d, evr_id=%v, entrant_id=%s)", m, m.Unk0, m.EvrID, m.EntrantID)
}
