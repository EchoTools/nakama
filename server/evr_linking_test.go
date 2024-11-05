package server

import (
	"testing"

	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/stretchr/testify/assert"
)

func TestGenerateLinkTicket(t *testing.T) {
	evrID, _ := evr.ParseEvrId("OVR-ORG-12345")

	linkTickets := make(map[string]*LinkTicket)
	deviceID := &DeviceAuth{
		AppID:           12345,
		EvrID:           *evrID,
		HMDSerialNumber: "test-hmd-serial",
		ClientIP:        "127.0.0.1",
	}
	loginData := &evr.LoginProfile{
		// Populate with necessary fields
	}

	ticket := generateLinkTicket(linkTickets, deviceID, loginData)

	assert.NotNil(t, ticket, "Expected a non-nil link ticket")
	assert.Equal(t, deviceID.Token(), ticket.DeviceAuthToken, "Expected DeviceAuthToken to match")
	assert.Equal(t, deviceID.EvrID.String(), ticket.UserIDToken, "Expected UserIDToken to match")
	assert.Equal(t, loginData, ticket.LoginRequest, "Expected LoginRequest to match")
	assert.Contains(t, linkTickets, ticket.Code, "Expected linkTickets to contain the generated code")
}

func TestGenerateLinkTicketWithExistingToken(t *testing.T) {
	existingToken := "existing-token"

	evrID, _ := evr.ParseEvrId("OVR-ORG-12345")
	linkTickets := map[string]*LinkTicket{
		"existing-code": {
			Code:            "existing-code",
			DeviceAuthToken: existingToken,
			UserIDToken:     "existing-user-id",
			LoginRequest:    &evr.LoginProfile{},
		},
	}
	deviceID := &DeviceAuth{
		AppID:           12345,
		EvrID:           *evrID,
		HMDSerialNumber: "test-hmd-serial",
		ClientIP:        "127.0.0.1",
	}
	loginData := &evr.LoginProfile{
		// Populate with necessary fields
	}

	ticket := generateLinkTicket(linkTickets, deviceID, loginData)

	assert.NotNil(t, ticket, "Expected a non-nil link ticket")
	assert.Equal(t, deviceID.Token(), ticket.DeviceAuthToken, "Expected DeviceAuthToken to match")
	assert.Equal(t, deviceID.EvrID.String(), ticket.UserIDToken, "Expected UserIDToken to match")
	assert.Equal(t, loginData, ticket.LoginRequest, "Expected LoginRequest to match")
	assert.Contains(t, linkTickets, ticket.Code, "Expected linkTickets to contain the generated code")
	assert.NotEqual(t, "existing-code", ticket.Code, "Expected a new code to be generated")
}
