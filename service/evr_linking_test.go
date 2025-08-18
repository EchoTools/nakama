package service

import (
	"testing"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/stretchr/testify/assert"
)

func TestGenerateLinkTicket(t *testing.T) {
	evrID, _ := evr.ParseEvrId("OVR-ORG-12345")

	linkTickets := make(map[string]*LinkTicket)

	loginData := &evr.LoginProfile{
		// Populate with necessary fields
	}

	ticket := generateLinkTicket(linkTickets, *evrID, "127.0.0.1", loginData)

	assert.NotNil(t, ticket, "Expected a non-nil link ticket")
	assert.Equal(t, loginData, ticket.LoginProfile, "Expected LoginRequest to match")
	assert.Contains(t, linkTickets, ticket.Code, "Expected linkTickets to contain the generated code")
}

func TestGenerateLinkTicketWithExistingToken(t *testing.T) {

	evrID, _ := evr.ParseEvrId("OVR-ORG-12345")
	linkTickets := map[string]*LinkTicket{
		"existing-code": {
			Code:         "existing-code",
			XPID:         *evrID,
			ClientIP:     "127.0.0.1",
			LoginProfile: &evr.LoginProfile{},
		},
	}

	loginData := &evr.LoginProfile{}

	ticket := generateLinkTicket(linkTickets, *evrID, "127.0.0.1", loginData)

	assert.NotNil(t, ticket, "Expected a non-nil link ticket")

	assert.Equal(t, loginData, ticket.LoginProfile, "Expected LoginRequest to match")
	assert.Contains(t, linkTickets, ticket.Code, "Expected linkTickets to contain the generated code")
	assert.NotEqual(t, "existing-code", ticket.Code, "Expected a new code to be generated")
}
