package server

import (
	"context"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// friendPresenceText is the text the game shows under a friend's name for the match they are in
// (owner, 2026-10-01: "Social Lobby, Public Arena Match, Private Arena Match, Public Combat Match,
// Private Combat Match, or In Main Menu"; the other modes named plainly). No match is the main menu.
func friendPresenceText(label *MatchLabel) string {
	if label == nil {
		return "In Main Menu"
	}
	switch label.Mode {
	case evr.ModeSocialPublic, evr.ModeSocialPrivate, evr.ModeSocialNPE:
		return "Social Lobby"
	case evr.ModeArenaPublic:
		return "Public Arena Match"
	case evr.ModeArenaPrivate:
		return "Private Arena Match"
	case evr.ModeArenaTournment:
		return "Arena Tournament Match"
	case evr.ModeArenaPublicAI:
		return "Arena Match vs Bots"
	case evr.ModeArenaPracticeAI:
		return "Arena Practice"
	case evr.ModeArenaTutorial:
		return "Arena Tutorial"
	case evr.ModeCombatPublic:
		return "Public Combat Match"
	case evr.ModeCombatPrivate:
		return "Private Combat Match"
	case evr.ModeEchoCombatTournament:
		return "Combat Tournament Match"
	default:
		return "In Main Menu"
	}
}

// userCurrentMatch is the match the user is in now (their match-service presence), nil when none.
func (p *EvrPipeline) userCurrentMatch(ctx context.Context, userID uuid.UUID) *MatchLabel {
	presences, err := p.nk.StreamUserList(StreamModeService, userID.String(), "", StreamLabelMatchService, false, true)
	if err != nil || len(presences) == 0 {
		return nil
	}
	matchID := MatchIDFromStringOrNil(presences[0].GetStatus())
	if matchID.IsNil() {
		return nil
	}
	label, err := MatchLabelByID(ctx, p.nk, matchID)
	if err != nil {
		return nil
	}
	return label
}

// userSessionParams is the session parameters of one of the user's live sessions (their status
// presence), nil when the user has none on this node.
func (p *EvrPipeline) userSessionParams(userID uuid.UUID) *SessionParameters {
	for _, presence := range p.nk.tracker.ListByStream(PresenceStream{Mode: StreamModeStatus, Subject: userID}, true, true) {
		session := p.nk.sessionRegistry.Get(presence.ID.SessionID)
		if session == nil {
			continue
		}
		if params, ok := LoadParams(session.Context()); ok {
			return params
		}
	}
	return nil
}

// friendPartyFor is the friend's SNS party id as this viewer may see it: the party, if the viewer
// could join it now (open, room left, and its join policy admits the viewer, §4); 0 and false
// otherwise. The id is withheld when not joinable, as pnsovr only published it then (0x180093279).
func (p *EvrPipeline) friendPartyFor(ctx context.Context, viewer uuid.UUID, friendParams *SessionParameters) (uint64, bool) {
	if friendParams == nil || friendParams.currentPartyID == uuid.Nil || friendParams.currentSNSPartyID == 0 {
		return 0, false
	}
	ph, ok := p.nk.partyRegistry.Get(friendParams.currentPartyID)
	if !ok || !snsPartyIsOpen(ph) || ph.members.Size() >= ph.MaxSize {
		return 0, false
	}
	allowed, _, err := p.snsPartyJoinAllowed(ctx, viewer, friendParams.currentPartyID, ph)
	if err != nil || !allowed {
		return 0, false
	}
	return friendParams.currentSNSPartyID, true
}

// friendPresence builds one friend's SNSFriendPresenceNotify for the viewer.
func (p *EvrPipeline) friendPresence(ctx context.Context, viewer uuid.UUID, friendUserID uuid.UUID, accountID uint64, online bool) *evr.SNSFriendPresenceNotify {
	notify := &evr.SNSFriendPresenceNotify{FriendID: accountID, StatusCode: friendStatusCode(online)}
	if !online {
		return notify
	}
	notify.Text = []byte(friendPresenceText(p.userCurrentMatch(ctx, friendUserID)))
	if partyID, joinable := p.friendPartyFor(ctx, viewer, p.userSessionParams(friendUserID)); joinable {
		notify.PartyID = partyID
		notify.Joinable = 1
	}
	return notify
}

// sendFriendPresence sends the viewer one SNSFriendPresenceNotify per friend, if their client parses it.
func (p *EvrPipeline) sendFriendPresence(ctx context.Context, logger *zap.Logger, session *sessionWS, friends []friendPresenceTarget) {
	params, ok := LoadParams(ctx)
	if !ok || params.SocialLevel() < 1 {
		return
	}
	for _, f := range friends {
		notify := p.friendPresence(ctx, session.UserID(), f.userID, f.accountID, f.online)
		logger.Debug("Friend presence", zap.Uint64("friend", f.accountID), zap.String("text", string(notify.Text)),
			zap.Uint64("party", notify.PartyID), zap.Uint8("joinable", notify.Joinable))
		if err := SendEVRMessages(session, false, notify); err != nil {
			logger.Warn("Failed to send friend presence", zap.Uint64("friend", f.accountID), zap.Error(err))
		}
	}
	logger.Info("Friend presence sent", zap.Int("friends", len(friends)))
}

// friendPresenceTarget is one confirmed friend to send presence for.
type friendPresenceTarget struct {
	userID    uuid.UUID
	accountID uint64
	online    bool
}
