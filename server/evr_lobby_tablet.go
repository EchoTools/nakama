package server

import (
	"errors"

	"github.com/gofrs/uuid/v5"
	"go.uber.org/zap"
)

// Tablet parties: the in-game (SNS) party a nevr-runtime client makes from the game's tablet. The
// matchmaker was built to take a party as a lobby group; a tablet party is resolved as one at the same
// points a party group is (lobbyFind, the social find), by this rule (owner, 2026-10-02):
//   - a runtime client in a tablet party of 2 or more members matchmakes with it, and its party group
//     name does not apply;
//   - otherwise a party group name, if set, applies as before.
// A tablet party's leader files its ticket without the formation wait (lobbyMatchMakeWithFallback): only
// the leader finds, so there are no members to wait for.
// Every tablet player is in a party of one from login, so "in a tablet party" means 2 or more.

// Which of a find session's identities is in its tablet party (tabletPartyOf).
const (
	// tabletMatchedLobby: the find session itself is the member. A client on one connection sends its
	// SNS party messages and its finds on the same session.
	tabletMatchedLobby = "lobby"
	// tabletMatchedLogin: the find session's login session is the member. A client that keeps
	// matchmaking on its own connection sends its SNS party messages on its login session, so that is
	// the session snsPartyTrackAndJoin put in the party, and finds on a lobby session (LobbySession,
	// evr_session.go), which is never a member.
	tabletMatchedLogin = "login"
)

// loginSessionOf is the find session's login session, or nil when it has none or is its own.
func loginSessionOf(session *sessionWS, params *SessionParameters) *sessionWS {
	if params == nil || params.loginSession == nil || params.loginSession.ID() == session.ID() {
		return nil
	}
	return params.loginSession
}

// tabletPartyOf is the session's tablet party: the SNS party its SNS id names (not currentPartyID,
// which a party group join may have overwritten), with this session or its login session a member and
// 2 or more members. matched is which of the two is the member (tabletMatchedLobby, tabletMatchedLogin).
func (p *EvrPipeline) tabletPartyOf(session *sessionWS, params *SessionParameters) (ph *PartyHandler, matched string, ok bool) {
	if params == nil || params.currentSNSPartyID == 0 || p.snsPartyIDToUUID == nil || p.nk == nil || p.nk.partyRegistry == nil {
		return nil, "", false
	}
	partyUUID, ok := p.snsPartyIDToUUID.Load(params.currentSNSPartyID)
	if !ok {
		return nil, "", false
	}
	ph, ok = p.nk.partyRegistry.Get(partyUUID)
	if !ok || ph.members.Size() < 2 {
		return nil, "", false
	}
	loginSID := ""
	if login := loginSessionOf(session, params); login != nil {
		loginSID = login.ID().String()
	}
	for _, m := range ph.members.List() {
		switch m.Presence.GetSessionId() {
		case session.ID().String():
			return ph, tabletMatchedLobby, true
		case loginSID:
			if loginSID != "" {
				return ph, tabletMatchedLogin, true
			}
		}
	}
	return nil, "", false
}

// tabletMembersFollow is whether every member's client is a nevr-runtime client that reads party data
// (social level >= 1). The game moves a member to the leader's lobby from the leader's party data
// (FollowParty, echovr.exe 0x14016bc20); a stock client is never sent it.
func (p *EvrPipeline) tabletMembersFollow(ph *PartyHandler) bool {
	for _, m := range ph.members.List() {
		session := p.nk.sessionRegistry.Get(uuid.FromStringOrNil(m.Presence.GetSessionId()))
		if session == nil {
			return false
		}
		params, ok := LoadParams(session.Context())
		if !ok || params.SocialLevel() < 1 {
			return false
		}
	}
	return true
}

// tabletParty is the session's tablet party if the rule makes it this session's lobby party, and
// whether the session is in a tablet party at all (which turns its party group off either way).
// matched is which of the session's identities is the member (tabletPartyOf).
func (p *EvrPipeline) tabletParty(session *sessionWS) (ph *PartyHandler, matched string, inTablet bool, usable bool) {
	params, ok := LoadParams(session.Context())
	if !ok {
		return nil, "", false, false
	}
	ph, matched, inTablet = p.tabletPartyOf(session, params)
	if !inTablet {
		return nil, "", false, false
	}
	return ph, matched, true, p.tabletMembersFollow(ph)
}

// lobbyPartyApplies is whether this find has a lobby party: a usable tablet party, or (when the
// session is in no tablet party) a party group name as before.
func (p *EvrPipeline) lobbyPartyApplies(session *sessionWS, lobbyParams *LobbySessionParameters) bool {
	if _, _, inTablet, usable := p.tabletParty(session); inTablet {
		return usable
	}
	return lobbyParams.PartyGroupName != ""
}

// errTabletPartyNotUsable is returned when the session's tablet party cannot be its lobby party (a
// member's client does not follow), so neither it nor the party group applies.
var errTabletPartyNotUsable = errors.New("tablet party has a member whose client does not follow party data")

// joinLobbyParty is the lobby group for this find where a party group was joined before: the tablet
// party when the rule picks it (restoring currentPartyID to it if a party group join had overwritten
// it), else JoinPartyGroup. JoinPartyGroup is never called for a session in a tablet party.
func (p *EvrPipeline) joinLobbyParty(logger *zap.Logger, session *sessionWS, lobbyParams *LobbySessionParameters) (*LobbyGroup, bool, error) {
	ph, matched, inTablet, usable := p.tabletParty(session)
	if !inTablet {
		return JoinPartyGroup(session, lobbyParams.PartyGroupName, lobbyParams.CurrentMatchID)
	}
	if !usable {
		logger.Info("Tablet party not used: a member's client does not follow party data", zap.String("party_id", ph.ID.String()))
		return nil, false, errTabletPartyNotUsable
	}
	params, _ := LoadParams(session.Context())
	if params != nil && params.currentPartyID != ph.ID {
		logger.Warn("Tablet party: currentPartyID was not the tablet party; restored",
			zap.String("was", params.currentPartyID.String()), zap.String("tablet_party", ph.ID.String()))
		params.currentPartyID = ph.ID
		StoreParams(session.Context(), params)
	}
	group := &LobbyGroup{ph: ph, tablet: true, sessions: p.nk.sessionRegistry}
	loginSID := ""
	if params != nil && params.loginSession != nil {
		loginSID = params.loginSession.ID().String()
	}
	// This session leads when the party's leader is this session, its login session, or (as the group
	// shows members, LobbyGroup.memberSessionID) resolves to this session.
	isLeader := group.isLeaderSession(session.ID().String(), loginSID)
	logger.Info("Tablet party is the lobby party", zap.String("party_id", ph.ID.String()),
		zap.Int("party_size", group.Size()), zap.Bool("is_leader", isLeader),
		zap.String("matched_identity", matched),
		zap.String("lobby_sid", session.ID().String()), zap.String("login_sid", loginSID))
	return group, isLeader, nil
}
