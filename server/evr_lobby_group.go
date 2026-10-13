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
	// tablet is set when the group is the session's tablet (SNS) party (joinLobbyParty,
	// evr_lobby_tablet.go), not a party group.
	tablet bool
	// sessions resolves a tablet party's members to the sessions they matchmake on
	// (memberSessionID). Unset for a party group, whose members are those sessions already.
	sessions SessionRegistry
}

// memberSessionID is the session a party member matchmakes and joins matches on. A party group's
// member is that session already: JoinPartyGroup tracks the find session. A tablet party's member is
// the session that sent the SNS party messages (snsPartyTrackAndJoin). For a client that keeps
// matchmaking on its own connection that is its login session, and its finds, its matchmaking stream
// and its match joins are on its lobby session, so the member is shown as that lobby session. With no
// live lobby session, or one that is the member itself, the member session stands.
func (g *LobbyGroup) memberSessionID(sessionID string) string {
	if g == nil || !g.tablet || g.sessions == nil {
		return sessionID
	}
	s := g.sessions.Get(uuid.FromStringOrNil(sessionID))
	if s == nil {
		return sessionID
	}
	params, ok := LoadParams(s.Context())
	if !ok || params.lobbySession == nil {
		return sessionID
	}
	lobbyID := params.lobbySession.ID()
	if g.sessions.Get(lobbyID) == nil {
		return sessionID // the lobby session has closed
	}
	return lobbyID.String()
}

// rawLeader is the party handler's leader as it stands, or nil.
func (g *LobbyGroup) rawLeader() (presence *rtapi.UserPresence, node string) {
	g.ph.RLock()
	defer g.ph.RUnlock()
	if g.ph.leader == nil {
		return g.ph.expectedInitialLeader, ""
	}
	return g.ph.leader.UserPresence, g.ph.leader.PresenceID.Node
}

// isLeaderSession is whether the session sessionID (whose login session is loginSessionID, "" if
// none) leads the party: the leader is that session, its login session, or is shown as that session
// (memberSessionID).
func (g *LobbyGroup) isLeaderSession(sessionID, loginSessionID string) bool {
	if g == nil || g.ph == nil {
		return false
	}
	leader, _ := g.rawLeader()
	if leader == nil {
		return false
	}
	switch leader.SessionId {
	case sessionID:
		return true
	case loginSessionID:
		if loginSessionID != "" {
			return true
		}
	}
	return g.memberSessionID(leader.SessionId) == sessionID
}

func (g *LobbyGroup) ID() uuid.UUID {
	if g.ph == nil {
		return uuid.Nil
	}
	return g.ph.ID
}

// IsTablet is whether the group is a tablet (SNS) party rather than a party group.
func (g *LobbyGroup) IsTablet() bool {
	return g != nil && g.tablet
}

func (g *LobbyGroup) IDStr() string {
	if g.ph == nil {
		return uuid.Nil.String()
	}
	return g.ph.IDStr
}

// GetLeader is the party's leader, as the session it matchmakes on (memberSessionID).
func (g *LobbyGroup) GetLeader() *rtapi.UserPresence {
	if g.ph == nil {
		return nil
	}
	leader, _ := g.rawLeader()
	if leader == nil || !g.tablet {
		return leader
	}
	sid := g.memberSessionID(leader.SessionId)
	if sid == leader.SessionId {
		return leader
	}
	return &rtapi.UserPresence{UserId: leader.UserId, SessionId: sid, Username: leader.Username,
		Persistence: leader.Persistence, Status: leader.Status}
}

// List is the party's members, each as the session it matchmakes on (memberSessionID).
func (g *LobbyGroup) List() []*PartyPresenceListItem {
	if g.ph == nil {
		return nil
	}
	items := g.ph.members.List()
	if !g.tablet {
		return items
	}
	out := make([]*PartyPresenceListItem, 0, len(items))
	for _, item := range items {
		sid := g.memberSessionID(item.Presence.GetSessionId())
		if sid == item.Presence.GetSessionId() {
			out = append(out, item)
			continue
		}
		presence := &Presence{ID: item.Presence.ID, Stream: item.Presence.Stream, UserID: item.Presence.UserID, Meta: item.Presence.Meta}
		presence.ID.SessionID = uuid.FromStringOrNil(sid)
		presenceID := presence.ID
		up := item.UserPresence
		out = append(out, &PartyPresenceListItem{
			PresenceID: &presenceID,
			Presence:   presence,
			UserPresence: &rtapi.UserPresence{UserId: up.GetUserId(), SessionId: sid, Username: up.GetUsername(),
				Persistence: up.GetPersistence(), Status: up.GetStatus()},
		})
	}
	return out
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
	if g.tablet {
		return g.tabletMatchmakerAdd(sessionID, node, query, minCount, maxCount, countMultiple, stringProperties, numericProperties)
	}
	return g.ph.MatchmakerAdd(sessionID, node, query, minCount, maxCount, countMultiple, stringProperties, numericProperties)
}

// tabletMatchmakerAdd is PartyHandler.MatchmakerAdd for a tablet party, with the members as the
// sessions they matchmake on (List): the ticket must name the sessions the match is joined on, and
// the leader files it from its find session, which may be its lobby session (memberSessionID).
func (g *LobbyGroup) tabletMatchmakerAdd(sessionID, node, query string, minCount, maxCount, countMultiple int, stringProperties map[string]string, numericProperties map[string]float64) (string, []*PresenceID, error) {
	g.ph.RLock()
	stopped := g.ph.stopped
	g.ph.RUnlock()
	if stopped {
		return "", nil, runtime.ErrPartyClosed
	}
	leader, leaderNode := g.rawLeader()
	if leader == nil || leaderNode != node {
		return "", nil, runtime.ErrPartyNotLeader
	}
	leaderSID := g.memberSessionID(leader.SessionId)
	if sessionID != leader.SessionId && sessionID != leaderSID {
		return "", nil, runtime.ErrPartyNotLeader
	}

	members := g.List()
	presences := make([]*MatchmakerPresence, 0, len(members))
	memberPresenceIDs := make([]*PresenceID, 0, len(members))
	for _, member := range members {
		presences = append(presences, &MatchmakerPresence{
			UserId:    member.UserPresence.GetUserId(),
			SessionId: member.UserPresence.GetSessionId(),
			Username:  member.UserPresence.GetUsername(),
			Node:      member.PresenceID.Node,
			SessionID: member.PresenceID.SessionID,
		})
		if member.UserPresence.GetSessionId() == leaderSID {
			continue
		}
		memberPresenceIDs = append(memberPresenceIDs, member.PresenceID)
	}

	ticket, _, err := g.ph.matchmaker.Add(g.ph.ctx, presences, "", g.ph.IDStr, query, minCount, maxCount, countMultiple, stringProperties, numericProperties)
	if err != nil {
		return "", nil, err
	}
	return ticket, memberPresenceIDs, nil
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

			// Already being a member is success, not failure. This branch
			// already meant to say so, but could never take effect: JoinRequest
			// returns (false, ErrPartyJoinRequestAlreadyMember), so tolerating
			// the error only fell through to the !success check below and
			// returned "failed to join party" anyway.
			//
			// It also could not be reached before now -- for an open party,
			// which is the only kind EVR creates, JoinRequest returned
			// ErrPartyFull without ever consulting identity. With that fixed, a
			// reconnecting member reaches this branch, and it has to mean what
			// it says or the reconnect still fails.
			alreadyMember := errors.Is(err, runtime.ErrPartyJoinRequestAlreadyMember)
			if err != nil && !alreadyMember {
				return nil, false, err
			}

			if !success && !alreadyMember {
				return nil, false, errors.New("failed to join party")
			}
			// Only a genuine join needs rolling back if the Track below fails;
			// a member who was already there was not added by this call.
			addedMember = !alreadyMember
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

	// Make currentPartyID party-type-agnostic (D2): group-party members now
	// carry the party UUID exactly like SNS members do, so the reservation
	// trigger (evr_pipeline_lobby.go lobbyEntrantConnected) and the
	// matchmaker-cancel path (lobbyPendingSessionCancel) fire for group parties.
	// Placed AFTER the transactional Track (PR #510): a rolled-back join returns
	// before reaching here, so we never set currentPartyID for a non-member.
	if params, ok := LoadParams(session.Context()); ok {
		params.currentPartyID = ph.ID
		// currentSNSPartyID stays 0 for group parties: every reader of it is
		// inside an sns* message handler that group-party users never invoke.
		StoreParams(session.Context(), params)
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
