package evr

import (
	"fmt"
	"strings"

	"github.com/gofrs/uuid/v5"
)

const MaxJoinRejectedEntrants = 16

// Nakama -> Game Server: player sessions that are to be kicked/rejected.
type GameServerJoinRejected struct {
	ErrorCode  PlayerRejectionReason
	EntrantIDs []uuid.UUID
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
func NewGameServerEntrantRejected(errorCode PlayerRejectionReason, playerSessions ...uuid.UUID) *GameServerJoinRejected {
	return &GameServerJoinRejected{
		ErrorCode:  errorCode,
		EntrantIDs: playerSessions,
	}
}

// Stream encodes or decodes the GameServerPlayersRejected message.
func (m *GameServerJoinRejected) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error {
			errorCode := byte(m.ErrorCode)
			if err := s.StreamByte(&errorCode); err != nil {
				return err
			}
			m.ErrorCode = PlayerRejectionReason(errorCode)
			return nil
		},
		func() error {
			if s.Mode == DecodeMode {
				count := s.Len() / 16
				if count > MaxJoinRejectedEntrants {
					return fmt.Errorf("entrant count %d exceeds maximum %d", count, MaxJoinRejectedEntrants)
				}
				m.EntrantIDs = make([]uuid.UUID, count)
			}
			return s.StreamGUIDs(&m.EntrantIDs)
		},
	})
}

func (m *GameServerJoinRejected) String() string {
	sessions := make([]string, len(m.EntrantIDs))
	for i, session := range m.EntrantIDs {
		sessions[i] = session.String()
	}
	return fmt.Sprintf("BroadcasterPlayersRejected(error_code=%v, player_sessions=[%s])", m.ErrorCode, strings.Join(sessions, ", "))
}
