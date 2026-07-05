package server

import (
	"errors"
	"fmt"

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

// MatchmakerRemoveAll cancels all active matchmaking tickets for this party.
// Used when a late arrival requires the ticket to be rebuilt with the full
// party. Returns nil if the party handler is nil or has no active tickets.
func (g *LobbyGroup) MatchmakerRemoveAll() error {
	if g.ph == nil {
		return nil
	}
	return g.ph.matchmaker.RemovePartyAll(g.ph.IDStr)
}

// HasSessionOnTicket reports whether sessionID is included on any active
// matchmaking ticket for this party. Returns false when the party handler
// is nil or has no active tickets.
func (g *LobbyGroup) HasSessionOnTicket(sessionID string) bool {
	if g.ph == nil {
		return false
	}
	return g.ph.matchmaker.HasSessionOnPartyTicket(g.ph.IDStr, sessionID)
}

// TicketRebuildCh returns a channel that is signalled when party membership
// changes require the active matchmaking ticket to be rebuilt. Returns nil
// when the LobbyGroup or party handler is nil.
func (g *LobbyGroup) TicketRebuildCh() <-chan struct{} {
	if g == nil || g.ph == nil {
		return nil
	}
	return g.ph.ticketRebuildCh
}

// SignalTicketRebuild sends a non-blocking signal on the ticket rebuild
// channel, notifying the leader's matchmaking loop that the party
// membership has changed and the ticket must be rebuilt.
func (g *LobbyGroup) SignalTicketRebuild() {
	if g == nil || g.ph == nil {
		return
	}
	select {
	case g.ph.ticketRebuildCh <- struct{}{}:
	default:
		// Already signalled; the leader will pick it up.
	}
}

// MatchmakerRemoveSessionAll removes all matchmaking tickets associated
// with the given sessionID, regardless of party affiliation. This is
// necessary when the leader submitted a solo ticket (no party ID) and a
// late arrival needs to cancel it.
func (g *LobbyGroup) MatchmakerRemoveSessionAll(sessionID string) error {
	if g == nil || g.ph == nil {
		return nil
	}
	return g.ph.matchmaker.RemoveSessionAll(sessionID)
}

func JoinPartyGroup(session *sessionWS, groupName string, currentMatchID MatchID) (*LobbyGroup, bool, error) {

	// Fail fast: if the session is already dying (its context is canceled),
	// refuse the join before creating a party or adding any member. This is the
	// common source of party "ghost members": the social-find loop re-invokes
	// JoinPartyGroup after the session's connection has dropped. Without this
	// guard, JoinRequest would insert the session into ph.members but the
	// subsequent tracker.Track would fail (LocalTracker.Track guards on
	// ctx.Err()), leaving a member with no party-stream presence that the
	// stream-driven disconnect-eviction path can never evict.
	if err := session.Context().Err(); err != nil {
		return nil, false, fmt.Errorf("session context done, refusing party join: %w", err)
	}

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

	// addedMember records whether THIS call inserted the session into
	// ph.members via JoinRequest. It gates the rollback below so we only ever
	// remove state this call created.
	addedMember := false
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
			addedMember = true
		}
	}

	// Track the session on the party stream. This must be transactional: if
	// Track fails, roll back any state this call created so that ph.members
	// never retains a session that is not tracked on the party stream (the
	// ghost-member invariant). The just-added member (if any) is guaranteed not
	// to be the party leader here — leader assignment only happens via the
	// tracker Join event, which requires a successful Track — so removing it
	// directly from ph.members is the precise inverse of JoinRequest.
	if success, isNew := session.pipeline.tracker.Track(session.Context(), session.ID(), ph.Stream, session.UserID(), presenceMeta); !success {
		if addedMember {
			ph.members.Leave([]*Presence{&presence})
		}
		// Only delete the party if THIS call created it AND it is still empty.
		// `created` alone is not sufficient: two sessions can converge on the
		// same LobbyGroupName, with one creating the party (created=true) and a
		// second concurrently joining it (created=false) — the second's
		// JoinRequest commits it into ph.members before this creator's async
		// tracker Join resolves. The creator never self-adds via JoinRequest
		// (only via the tracker Join event that just failed), so on Track
		// failure the creator is not in ph.members and Size() reflects only
		// other, legitimate members. Deleting a party another session has
		// already joined would evict a real member and orphan their party.
		//
		// Size()==0-then-Delete carries a strictly narrower residual window
		// than the bug it fixes (see commit notes); it cannot recreate the
		// evict-a-committed-member failure this guards against.
		if created && ph.members.Size() == 0 {
			session.pipeline.partyRegistry.Delete(ph.ID)
		}
		_ = session.Send(&rtapi.Envelope{Message: &rtapi.Envelope_Error{Error: &rtapi.Error{
			Code:    int32(rtapi.Error_RUNTIME_EXCEPTION),
			Message: "Error tracking party membership",
		}}}, true)
		return nil, false, errors.New("failed to track party membership")
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
