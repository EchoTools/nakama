package evr

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
)

// StatusUpdateReason describes the reason for the status update in a LobbyStatusNotifyv2 message.
type StatusUpdateReason uint64

const (
	StatusUpdateBanned = StatusUpdateReason(iota)
	StatusUpdateKicked
	StatusUpdateDemoted
	StatusUpdateUnknown
)

// LobbyStatusNotify is a message from server to client notifying them of some status (e.g. the reason they were kicked).
type LobbyStatusNotify struct {
	Channel    uuid.UUID          // The channel which the status notification applies to.
	Message    []byte             // A message describing the status update. This is a maximum of 64 bytes, UTF-8 encoded.
	ExpiryTime int64              // The time the status change takes effect until.
	Reason     StatusUpdateReason // The reason for the status notification.
}

func (m LobbyStatusNotify) String() string {
	return fmt.Sprintf("%T(reason=%d, channel=%s, message=%s, expiry_time=%d)", m, m.Reason, m.Channel.String(), string(m.Message), m.ExpiryTime)
}

func (m LobbyStatusNotify) GetMessage() string {
	// Remove all null terminators from the message.
	message := string(m.Message)
	for i := 0; i < len(message); i++ {
		if message[i] == 0 {
			message = message[:i]
			break
		}
	}
	return message
}

// NewLobbyStatusNotifyv2WithArgs initializes a new LobbyStatusNotifyv2 with the provided arguments.
func NewLobbyStatusNotifyv2(channel uuid.UUID, message string, expiryTime time.Time, reason StatusUpdateReason) *LobbyStatusNotify {
	if len(message) > 64 {
		message = message[:64]
	}
	return &LobbyStatusNotify{
		Channel:    channel,
		Message:    []byte(message),
		ExpiryTime: expiryTime.UTC().Unix(),
		Reason:     reason,
	}
}

func (l *LobbyStatusNotify) Stream(s *EasyStream) error {
	return RunErrorFunctions([]func() error{
		func() error { return s.StreamGUID(&l.Channel) },
		func() error {
			b := make([]byte, 64)
			if s.Mode == EncodeMode {
				copy(b, l.Message)
			}
			if err := s.StreamBytes(&b, 64); err != nil {
				return err
			}
			if s.Mode == DecodeMode {
				l.Message = b
			}
			return nil
		},
		func() error { return s.StreamNumber(binary.LittleEndian, &l.ExpiryTime) },
		func() error { return s.StreamNumber(binary.LittleEndian, &l.Reason) },
	})
}
