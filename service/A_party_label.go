package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/rtapi"
	nkrtapi "github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server"
)

var PartyNotFoundError = fmt.Errorf("party not found")

type PartyLabel struct {
	ID        uuid.UUID             `json:"id,omitempty"`
	SharedKey string                `json:"shared_key,omitempty"`
	Leader    *rtapi.UserPresence   `json:"leader,omitempty"`
	Presences []*rtapi.UserPresence `json:"presences,omitempty"`
}

func (l *PartyLabel) IsLeader(sessionID string) bool {
	if l == nil || l.Leader == nil {
		return false
	}
	return l.Leader.SessionId == sessionID
}

func (l *PartyLabel) LeaderSessionID() string {
	if l == nil || l.Leader == nil {
		return ""
	}
	return l.Leader.SessionId
}

func (l *PartyLabel) Size() int {
	if l == nil || l.Presences == nil {
		return 1
	}
	return len(l.Presences)
}

func (l *PartyLabel) String() string {
	if l == nil {
		return ""
	}
	data, err := json.Marshal(l)
	if err != nil {
		return ""
	}
	return string(data)
}

type PartyGroup struct {
	*api.Party
	SharedKey string `json:"shared_key,omitempty"`
}

func (g *PartyGroup) GetLabel() string {
	if g.Party == nil || g.Party.Label == "" {
		return ""
	}

	data, err := json.Marshal(g.Party.Label)
	if err != nil {
		return ""
	}
	return string(data)
}

func GetPartyGroupBySharedKey(ctx context.Context, partyRegistry server.PartyRegistry, key string) (*api.Party, error) {
	query := fmt.Sprintf("label.shared_key:%s", key)
	parties, _, err := partyRegistry.PartyList(ctx, 1, nil, false, query, "")
	if err != nil {
		return nil, err
	}
	if len(parties) == 0 || parties[0] == nil {
		return nil, PartyNotFoundError
	} else if len(parties) > 1 {
		return nil, fmt.Errorf("multiple parties found with name: %s", key)
	}
	return parties[0], nil
}

func GetPartyGroupByID(ctx context.Context, partyRegistry server.PartyRegistry, id string) (*api.Party, error) {
	query := fmt.Sprintf("label._id:%s", id)
	parties, _, err := partyRegistry.PartyList(ctx, 1, nil, false, query, "")
	if err != nil {
		return nil, err
	}
	if len(parties) == 0 || parties[0] == nil {
		return nil, PartyNotFoundError
	} else if len(parties) > 1 {
		return nil, fmt.Errorf("multiple parties found with name: %s", id)
	}
	return parties[0], nil
}

func (p *Pipeline) updatePartyLeaderFromMessage(session *sessionEVR, in *nkrtapi.Envelope) error {
	ctx := session.Context()
	params, ok := LoadParams(ctx)
	if !ok {
		return nil
	}

	var label *PartyLabel
	var partyID uuid.UUID
	switch msg := in.GetMessage().(type) {
	case *nkrtapi.Envelope_Party:
		party := msg.Party
		if party == nil || party.GetLeader() == nil || party.GetLeader().GetUserId() != session.UserID().String() {
			// Only the party leader can update the label.
			return nil
		}

		label = &PartyLabel{
			ID:        uuid.FromStringOrNil(party.GetPartyId()),
			SharedKey: params.matchmakingSettings.LobbyGroupName,
			Leader:    party.GetLeader(),
			Presences: party.GetPresences(),
		}
		partyID = uuid.FromStringOrNil(party.GetPartyId())

	case *nkrtapi.Envelope_PartyLeader:
		party, err := GetPartyGroupByID(ctx, p.partyRegistry, msg.PartyLeader.GetPartyId())
		if err != nil {
			return err
		}
		label := &PartyLabel{}
		if party.Label != "" {
			if err := json.Unmarshal([]byte(party.Label), label); err != nil {
				return fmt.Errorf("failed to unmarshal party label: %w", err)
			}
		} else {
			label = &PartyLabel{}
		}
		partyID = uuid.FromStringOrNil(msg.PartyLeader.GetPartyId())
		label.Leader = msg.PartyLeader.GetPresence()

	default:
		return nil
	}
	if label != nil {
		data, err := json.Marshal(label)
		if err != nil {
			return err
		}
		// Update the party label in the registry.
		sessionID := session.ID().String()
		if err := p.partyRegistry.PartyUpdate(ctx, partyID, p.node, sessionID, p.node, string(data), true, false); err != nil {
			return err
		}
	}
	return nil
}
