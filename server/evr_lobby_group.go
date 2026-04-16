package server

import (
	"errors"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
)

type LobbyGroup struct {
	name string
	ph   *PartyHandler
}

func (g *LobbyGroup) ID() uuid.UUID {
	if g.ph == nil {
		return uuid.Nil
	}
	return g.ph.ID
}

func (g *LobbyGroup) IDStr() string {
	if g.ph == nil {
		return uuid.Nil.String()
	}
	return g.ph.IDStr
}

func (g *LobbyGroup) GetLeader() *rtapi.UserPresence {
	if g.ph == nil {
		return nil
	}
	g.ph.RLock()
	defer g.ph.RUnlock()
	if g.ph.leader == nil {
		return g.ph.expectedInitialLeader
	}
	return g.ph.leader.UserPresence
}

func (g *LobbyGroup) List() []*PartyPresenceListItem {
	if g.ph == nil {
		return nil
	}
	return g.ph.members.List()
}

func (g *LobbyGroup) Size() int {
	if g.ph == nil {
		return 0
	}
	return g.ph.members.Size()
}

func (g *LobbyGroup) MatchmakerAdd(sessionID, node, query string, minCount, maxCount, countMultiple int, stringProperties map[string]string, numericProperties map[string]float64) (string, []*PresenceID, error) {
	if g.ph == nil {
		return "", nil, errors.New("party handler is nil")
	}
	if settings := ServiceSettings(); settings.Matchmaking.DisableMatchmaker {
		return "", nil, runtime.NewError("matchmaker is disabled", 14) // UNAVAILABLE
	}
	return g.ph.MatchmakerAdd(sessionID, node, query, minCount, maxCount, countMultiple, stringProperties, numericProperties)
}

func JoinPartyGroup(session *sessionWS, groupName string, currentMatchID MatchID) (*LobbyGroup, bool, error) {

	userPresence := &rtapi.UserPresence{
		UserId:    session.UserID().String(),
		SessionId: session.ID().String(),
		Username:  session.Username(),
	}

	presence := Presence{
		ID: PresenceID{
			Node:      session.pipeline.node,
			SessionID: session.ID(),
		},
		// Presence stream not needed.
		UserID: session.UserID(),
		Meta: PresenceMeta{
			Username: session.Username(),
			// Other meta fields not needed.
		},
	}

	presenceMeta := PresenceMeta{
		Format:   session.Format(),
		Username: session.Username(),
		Status:   currentMatchID.String(),
	}

	ph, created, err := session.pipeline.partyRegistry.GetOrCreateByGroupName(groupName, true, 4, userPresence)
	if err != nil {
		return nil, false, err
	}

	if !created {
		isMember := false
		// Check if the player is already a member of the party
		for _, member := range ph.members.List() {
			if member.Presence.GetUserId() == session.UserID().String() {
				isMember = true
				break
			}
		}

		if !isMember {
			// Join the party
			success, err := ph.JoinRequest(&presence)
			if err != nil && err != runtime.ErrPartyJoinRequestAlreadyMember {
				return nil, false, err
			}

			if !success {
				return nil, false, errors.New("failed to join party")
			}
		}
	}

	// If successful, the creator becomes the first user to join the party.
	if success, isNew := session.pipeline.tracker.Track(session.Context(), session.ID(), ph.Stream, session.UserID(), presenceMeta); !success {
		_ = session.Send(&rtapi.Envelope{Message: &rtapi.Envelope_Error{Error: &rtapi.Error{
			Code:    int32(rtapi.Error_RUNTIME_EXCEPTION),
			Message: "Error tracking party creation",
		}}}, true)
		return nil, false, errors.New("failed to track party creation")
	} else if isNew {
		out := &rtapi.Envelope{Message: &rtapi.Envelope_Party{Party: &rtapi.Party{
			PartyId:   ph.IDStr,
			Open:      ph.Open,
			MaxSize:   int32(ph.MaxSize),
			Self:      userPresence,
			Leader:    userPresence,
			Presences: []*rtapi.UserPresence{userPresence},
		}}}
		_ = session.Send(out, true)
	}

	lobbyGroup := &LobbyGroup{
		name: groupName,
		ph:   ph,
	}
	// The player is a member of the party, they will follow the leader to lobbies.
	leader := lobbyGroup.GetLeader()
	isLeader := leader != nil && leader.SessionId == session.id.String()

	return lobbyGroup, isLeader, nil
}
