package server

import (
	"errors"
	"sync"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama-common/runtime"
)

type LobbyGroup struct {
	sync.RWMutex
	session *sessionWS

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
		return 1
	}
	return g.ph.members.Size()
}

func (g *LobbyGroup) MatchmakerAdd(sessionID, node, query string, minCount, maxCount, countMultiple int, stringProperties map[string]string, numericProperties map[string]float64) (string, []*PresenceID, error) {
	return g.ph.MatchmakerAdd(sessionID, node, query, minCount, maxCount, countMultiple, stringProperties, numericProperties)
}

func (g *LobbyGroup) PartyStream() PresenceStream {
	if g.ph == nil {
		return PresenceStream{}
	}
	g.ph.RLock()
	defer g.ph.RUnlock()
	return g.ph.Stream
}

func JoinPartyGroup(session *sessionWS, groupName string, partyID uuid.UUID, currentMatchID MatchID) (*LobbyGroup, bool, error) {

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

	partyRegistry := session.pipeline.partyRegistry.(*LocalPartyRegistry)

	// Check if the party already exists
	ph, found := partyRegistry.parties.Load(partyID)
	if !found {
		maxSize := 4
		open := true
		// Create the party
		ph = NewPartyHandler(partyRegistry.logger, partyRegistry, partyRegistry.matchmaker, partyRegistry.tracker, partyRegistry.streamManager, partyRegistry.router, partyID, partyRegistry.node, open, maxSize, userPresence)
		partyRegistry.parties.Store(partyID, ph)

	} else {
		isMember := false
		// Check if the player is already a member of the party
		for _, member := range ph.members.List() {
			if member.Presence.GetUserId() == session.UserID().String() {
				isMember = true
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
	if ph == nil {
		return nil, false, errors.New("failed to get party handler")
	}

	lobbyGroup := &LobbyGroup{
		session: session,
		name:    groupName,
		ph:      ph,
	}
	// The player is a member of the party, they will follow the leader to lobbies.
	isLeader := lobbyGroup.GetLeader().SessionId == session.id.String()

	return lobbyGroup, isLeader, nil
}
