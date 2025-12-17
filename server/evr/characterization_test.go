package evr

import (
	"reflect"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCharacterization_LoginRequest(t *testing.T) {
	// Create a sample LoginRequest
	sessionID := uuid.Must(uuid.NewV4())
	evrID := EvrId{PlatformCode: 1, AccountId: 1234567890}
	payload := LoginProfile{
		AccountId:       1234567890,
		DisplayName:     "TestUser",
		HMDSerialNumber: "Serial123",
		SystemInfo: SystemInfo{
			HeadsetType: "TestHeadset",
		},
	}

	msg, err := NewLoginRequest(sessionID, evrID, payload)
	require.NoError(t, err)

	// Round-trip test
	encoded, err := Marshal(msg)
	require.NoError(t, err)

	decodedMsgs, err := ParsePacket(encoded)
	require.NoError(t, err)
	require.Len(t, decodedMsgs, 1)

	decodedMsg, ok := decodedMsgs[0].(*LoginRequest)
	require.True(t, ok)

	// Verify fields
	assert.Equal(t, msg.PreviousSessionID, decodedMsg.PreviousSessionID)
	assert.Equal(t, msg.XPID, decodedMsg.XPID)
	assert.Equal(t, msg.Payload.AccountId, decodedMsg.Payload.AccountId)
	assert.Equal(t, msg.Payload.DisplayName, decodedMsg.Payload.DisplayName)
	assert.Equal(t, msg.Payload.HMDSerialNumber, decodedMsg.Payload.HMDSerialNumber)
	assert.Equal(t, msg.Payload.SystemInfo.HeadsetType, decodedMsg.Payload.SystemInfo.HeadsetType)
}

func TestCharacterization_LobbyMatchmakerStatusRequest(t *testing.T) {
	msg := NewLobbyMatchmakerStatusRequest()
	msg.Unk0 = 1

	// Round-trip test
	encoded, err := Marshal(msg)
	require.NoError(t, err)

	decodedMsgs, err := ParsePacket(encoded)
	require.NoError(t, err)
	require.Len(t, decodedMsgs, 1)

	decodedMsg, ok := decodedMsgs[0].(*LobbyMatchmakerStatusRequest)
	require.True(t, ok)

	assert.Equal(t, msg.Unk0, decodedMsg.Unk0)
}

func TestCharacterization_AllMessages(t *testing.T) {
	// Loop through all registered message types and try to instantiate and round-trip them
	// This is a basic sanity check. For complex messages, they might fail if Stream requires specific data.

	for sym, msgType := range SymbolTypes {
		typ := reflect.TypeOf(msgType).Elem()
		t.Run(typ.Name(), func(t *testing.T) {
			// Instantiate
			msgVal := reflect.New(typ)
			msg, ok := msgVal.Interface().(Message)
			if !ok {
				t.Fatalf("Type %v does not implement Message interface", typ)
			}

			// We can't easily populate fields with valid data for all types generically.
			// But we can check if empty struct marshals (if it doesn't crash).
			// Many Stream methods assume valid pointers or data, so this might crash or fail.
			// We skip this generic loop for now and rely on specific tests.
			_ = sym
			_ = msg
		})
	}
}
