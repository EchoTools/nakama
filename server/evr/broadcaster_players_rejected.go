package evr

import (
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
)

// Nakama -> Game Server: player sessions that are to be kicked/rejected.
type BroadcasterPlayersRejected struct {
	ErrorCode      PlayerRejectionReason
	PlayerSessions []uuid.UUID
}

func (m *BroadcasterPlayersRejected) Token() string {
	return "ERGameServerPlayersRejected"
}

func (m *BroadcasterPlayersRejected) Symbol() Symbol {
	return 0x7777777777770700
}

// PlayerRejectionReason represents the reason for player rejection.
type PlayerRejectionReason byte

const (
	PlayerRejectionReasonInternal         PlayerRejectionReason = iota // Internal server error
	PlayerRejectionReasonBadRequest                                    // Bad request from the player
	PlayerRejectionReasonTimeout                                       // Player connection timeout
	PlayerRejectionReasonDuplicate                                     // Duplicate player session
	PlayerRejectionReasonLobbyLocked                                   // Lobby is locked
	PlayerRejectionReasonLobbyFull                                     // Lobby is full
	PlayerRejectionReasonLobbyEnding                                   // Lobby is ending
	PlayerRejectionReasonKickedFromServer                              // Player was kicked from the server
	PlayerRejectionReasonDisconnected                                  // Player was disconnected
	PlayerRejectionReasonInactive                                      // Player is inactive
)

// NewGameServerPlayersRejected initializes a new GameServerPlayersRejected message with the provided arguments.
func NewBroadcasterPlayersRejected(errorCode PlayerRejectionReason, playerSessions ...uuid.UUID) *BroadcasterPlayersRejected {
	return &BroadcasterPlayersRejected{
		ErrorCode:      errorCode,
		PlayerSessions: playerSessions,
	}
}

// Stream encodes or decodes the GameServerPlayersRejected message.
func (m *BroadcasterPlayersRejected) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error {
			errorCode := byte(m.ErrorCode)
			return s.StreamByte(&errorCode)
		},
		func() error {
			// Consume/Produce all UUIDs until the end of the packet.
			if s.Mode == DecodeMode {
				endpointCount := (s.r.Len() - s.Position()) / 16
				m.PlayerSessions = make([]uuid.UUID, endpointCount)
			}
			for i := 0; i < len(m.PlayerSessions); i++ {
				if err := s.StreamGuid(&m.PlayerSessions[i]); err != nil {
					return err
				}
			}
			return nil
		},
	})
}

func (m *BroadcasterPlayersRejected) String() string {
	sessions := make([]string, len(m.PlayerSessions))
	for i, session := range m.PlayerSessions {
		sessions[i] = session.String()
	}
	return fmt.Sprintf("BroadcasterPlayersRejected(error_code=%v, player_sessions=[%s])", m.ErrorCode, strings.Join(sessions, ", "))
}
