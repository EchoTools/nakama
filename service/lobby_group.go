package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server"
)

func (p *Pipeline) JoinPartyGroup(ctx context.Context, session *sessionEVR, lobbyParams *LobbySessionParameters) (*PartyLabel, bool, error) {

	userPresence := &rtapi.UserPresence{
		UserId:    session.UserID().String(),
		SessionId: session.ID().String(),
		Username:  session.Username(),
	}

	presence := &server.Presence{
		ID: server.PresenceID{
			Node:      lobbyParams.Node,
			SessionID: session.ID(),
		},
		//server.Presence stream not needed.
		UserID: session.UserID(),
		Meta: server.PresenceMeta{
			Username: session.Username(),
			// Other meta fields not needed.
		},
	}

	presenceMeta := server.PresenceMeta{
		Format:   session.Format(),
		Username: session.Username(),
		Status:   lobbyParams.CurrentMatchID.String(),
	}
	party, err := GetPartyGroupBySharedKey(ctx, p.partyRegistry, lobbyParams.PartySharedKey)
	if err != nil {
		return nil, false, err
	}
	var partyUUID uuid.UUID
	var isLeader bool
	var label *PartyLabel
	if party != nil {
		// If the party already exists, we can use it.
		partyUUID = uuid.FromStringOrNil(party.GetPartyId())
		isNew, err := p.partyRegistry.PartyJoinRequest(ctx, partyUUID, p.node, presence)
		if err != nil {
			return nil, false, err
		}
		if isNew {
			// New to the party, join it.
			p.partyRegistry.Join(partyUUID, []*server.Presence{presence})
			label = &PartyLabel{}
			if err := json.Unmarshal([]byte(party.GetPartyId()), label); err != nil {
				return nil, false, errors.New("invalid party label")
			}
		}
		// If the party already exists, we can use it.
		return label, false, nil // Already in the party, no need to rejoin.
	}

	stream := server.PresenceStream{
		Mode:    server.StreamModeParty,
		Subject: partyUUID,
		Label:   p.node,
	}
	// If successful, the creator becomes the first user to join the party.
	if success, isNew := p.tracker.Track(session.Context(), session.ID(), stream, session.UserID(), presenceMeta); !success {
		_ = session.Send(&rtapi.Envelope{Message: &rtapi.Envelope_Error{Error: &rtapi.Error{
			Code:    int32(rtapi.Error_RUNTIME_EXCEPTION),
			Message: "Error tracking party creation",
		}}}, true)
		return nil, false, errors.New("failed to track party creation")
	} else if isNew {
		out := &rtapi.Envelope{Message: &rtapi.Envelope_Party{Party: &rtapi.Party{
			PartyId:   party.PartyId,
			Open:      party.Open,
			MaxSize:   int32(party.MaxSize),
			Self:      userPresence,
			Leader:    userPresence,
			Presences: []*rtapi.UserPresence{userPresence},
		}}}
		_ = session.Send(out, true)
		isLeader = true
	}

	return label, isLeader, nil
}
