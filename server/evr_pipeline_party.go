package server

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/rtapi"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// ---------------------------------------------------------------------------
// Invite storage
// ---------------------------------------------------------------------------

type snsPartyInvite struct {
	PartyUUID  uuid.UUID
	SNSPartyID uint64
	InviterID  uint64 // EvrId.AccountId of inviter
	InviterUID uuid.UUID
	CreatedAt  time.Time
}

type snsPartyInviteList struct {
	sync.RWMutex
	invites []*snsPartyInvite
}

func (l *snsPartyInviteList) Add(inv *snsPartyInvite) {
	l.Lock()
	defer l.Unlock()
	l.invites = append(l.invites, inv)
}

func (l *snsPartyInviteList) RemoveByParty(partyUUID uuid.UUID) {
	l.Lock()
	defer l.Unlock()
	if l.invites == nil {
		return
	}
	filtered := l.invites[:0]
	for _, inv := range l.invites {
		if inv.PartyUUID != partyUUID {
			filtered = append(filtered, inv)
		}
	}
	l.invites = filtered
}

func (l *snsPartyInviteList) FindByParty(partyUUID uuid.UUID) *snsPartyInvite {
	l.RLock()
	defer l.RUnlock()
	for _, inv := range l.invites {
		if inv.PartyUUID == partyUUID {
			return inv
		}
	}
	return nil
}

func (l *snsPartyInviteList) Count() int {
	l.RLock()
	defer l.RUnlock()
	return len(l.invites)
}

// ---------------------------------------------------------------------------
// SNS Party ID mapping helpers
// ---------------------------------------------------------------------------

func (p *EvrPipeline) allocateSNSPartyID(partyUUID uuid.UUID) uint64 {
	id := p.snsPartyIDCounter.Add(1)
	p.snsPartyIDToUUID.Store(id, partyUUID)
	p.snsPartyUUIDToID.Store(partyUUID, id)
	return id
}

func (p *EvrPipeline) lookupPartyUUID(snsID uint64) (uuid.UUID, bool) {
	return p.snsPartyIDToUUID.Load(snsID)
}

func (p *EvrPipeline) lookupSNSPartyID(partyUUID uuid.UUID) (uint64, bool) {
	return p.snsPartyUUIDToID.Load(partyUUID)
}

func (p *EvrPipeline) removeSNSPartyMapping(snsID uint64, partyUUID uuid.UUID) {
	p.snsPartyIDToUUID.Delete(snsID)
	p.snsPartyUUIDToID.Delete(partyUUID)
}

// ---------------------------------------------------------------------------
// EvrId UUID reverse-lookup
// ---------------------------------------------------------------------------

func (p *EvrPipeline) registerEvrUUIDMapping(evrUUID uuid.UUID, nakamaUserID uuid.UUID) {
	p.evrUUIDToUserID.Store(evrUUID, nakamaUserID)
}

func (p *EvrPipeline) resolveEvrUUIDToUserID(evrUUID [16]byte) (uuid.UUID, bool) {
	uid := uuid.UUID(evrUUID)
	return p.evrUUIDToUserID.Load(uid)
}

// ---------------------------------------------------------------------------
// Reverse resolution: Nakama userID -> EvrId.AccountId
// ---------------------------------------------------------------------------

func (p *EvrPipeline) resolveUserIDToAccountID(ctx context.Context, userID uuid.UUID) (uint64, error) {
	var deviceID string
	err := p.db.QueryRowContext(ctx, "SELECT id FROM user_device WHERE user_id = $1 LIMIT 1", userID).Scan(&deviceID)
	if err != nil {
		return 0, fmt.Errorf("device lookup for user %s: %w", userID, err)
	}
	// deviceID is like "OVR-12345" or "DSC-12345" — parse the account ID from after the last dash.
	parsed, err := evr.ParseEvrId(deviceID)
	if err != nil {
		return 0, fmt.Errorf("parse evrid %s: %w", deviceID, err)
	}
	return parsed.AccountId, nil
}

// ---------------------------------------------------------------------------
// Broadcasting to party members
// ---------------------------------------------------------------------------

func (p *EvrPipeline) sendEVRMessageToPartyMembers(logger *zap.Logger, partyUUID uuid.UUID, excludeSessionID uuid.UUID, messages ...evr.Message) {
	stream := PresenceStream{Mode: StreamModeParty, Subject: partyUUID, Label: p.node}
	presences := p.nk.tracker.ListByStream(stream, true, true)
	for _, presence := range presences {
		if presence.ID.SessionID == excludeSessionID {
			continue
		}
		session := p.nk.sessionRegistry.Get(presence.ID.SessionID)
		if session == nil {
			continue
		}
		_ = SendEVRMessages(session, false, messages...)
	}
}

// ---------------------------------------------------------------------------
// Party join/leave helpers (shared by create, join, respond-to-invite)
// ---------------------------------------------------------------------------

func (p *EvrPipeline) snsPartyTrackAndJoin(ctx context.Context, logger *zap.Logger, session *sessionWS, partyUUID uuid.UUID, snsPartyID uint64, params *SessionParameters) error {
	stream := PresenceStream{Mode: StreamModeParty, Subject: partyUUID, Label: p.node}
	success, _ := p.nk.tracker.Track(session.Context(), session.ID(), stream, session.UserID(), PresenceMeta{
		Format:   session.Format(),
		Username: session.Username(),
	})
	if !success {
		return fmt.Errorf("failed to track party presence")
	}

	if p.config.GetSession().SingleParty {
		p.nk.tracker.UntrackLocalByModes(session.ID(), partyStreamMode, stream)
	}

	// Register EvrId UUID mapping for this member.
	p.registerEvrUUIDMapping(params.xpID.UUID(), session.UserID())

	// Update session params.
	params.currentPartyID = partyUUID
	params.currentSNSPartyID = snsPartyID
	StoreParams(session.Context(), params)
	return nil
}

func (p *EvrPipeline) snsPartyLeaveCleanup(ctx context.Context, logger *zap.Logger, session *sessionWS, params *SessionParameters) {
	if params.currentPartyID == uuid.Nil {
		return
	}
	stream := PresenceStream{Mode: StreamModeParty, Subject: params.currentPartyID, Label: p.node}
	p.nk.tracker.Untrack(session.ID(), stream, session.UserID())

	params.currentPartyID = uuid.Nil
	params.currentSNSPartyID = 0
	StoreParams(session.Context(), params)
}

func (p *EvrPipeline) getPartyLeaderAccountID(ctx context.Context, logger *zap.Logger, ph *PartyHandler) uint64 {
	ph.RLock()
	leader := ph.leader
	ph.RUnlock()
	if leader == nil {
		return 0
	}
	leaderUID := uuid.FromStringOrNil(leader.UserPresence.UserId)
	if leaderUID == uuid.Nil {
		return 0
	}
	accountID, err := p.resolveUserIDToAccountID(ctx, leaderUID)
	if err != nil {
		logger.Warn("Failed to resolve leader account ID", zap.Error(err))
		return 0
	}
	return accountID
}

// ---------------------------------------------------------------------------
// Handlers
// ---------------------------------------------------------------------------

// snsPartyCreateRequest creates a new party with the caller as owner.
func (p *EvrPipeline) snsPartyCreateRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	params, ok := LoadParams(ctx)
	if !ok {
		logger.Error("Failed to load session parameters")
		return SendEVRMessages(session, false, &evr.SNSPartyCreateFailure{ErrorCode: 1})
	}

	// Leave any existing party first.
	if params.currentPartyID != uuid.Nil {
		p.snsPartyLeaveCleanup(ctx, logger, session, params)
		// Re-load params after cleanup.
		params, _ = LoadParams(ctx)
	}

	presence := &rtapi.UserPresence{
		UserId:    session.UserID().String(),
		SessionId: session.ID().String(),
		Username:  session.Username(),
	}

	ph := p.nk.partyRegistry.Create(true, 4, presence)
	if ph == nil {
		logger.Error("Failed to create party")
		return SendEVRMessages(session, false, &evr.SNSPartyCreateFailure{ErrorCode: 1})
	}

	snsID := p.allocateSNSPartyID(ph.ID)

	if err := p.snsPartyTrackAndJoin(ctx, logger, session, ph.ID, snsID, params); err != nil {
		logger.Error("Failed to track party creation", zap.Error(err))
		p.nk.partyRegistry.Delete(ph.ID)
		p.removeSNSPartyMapping(snsID, ph.ID)
		return SendEVRMessages(session, false, &evr.SNSPartyCreateFailure{ErrorCode: 1})
	}

	return SendEVRMessages(session, false, &evr.SNSPartyCreateSuccess{
		PartyID: snsID,
		OwnerID: params.xpID.AccountId,
	})
}

// snsPartyJoinRequest joins an existing party by SNS party ID.
func (p *EvrPipeline) snsPartyJoinRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	msg, ok := in.(*evr.SNSPartyJoinRequest)
	if !ok {
		return fmt.Errorf("expected *evr.SNSPartyJoinRequest, got %T", in)
	}

	params, ok := LoadParams(ctx)
	if !ok {
		logger.Error("Failed to load session parameters")
		return SendEVRMessages(session, false, &evr.SNSPartyJoinFailure{PartyID: msg.PartyID, ErrorCode: 1})
	}

	partyUUID, ok := p.lookupPartyUUID(msg.PartyID)
	if !ok {
		return SendEVRMessages(session, false, &evr.SNSPartyJoinFailure{PartyID: msg.PartyID, ErrorCode: 1})
	}

	// Leave any existing party first.
	if params.currentPartyID != uuid.Nil {
		p.snsPartyLeaveCleanup(ctx, logger, session, params)
		params, _ = LoadParams(ctx)
	}

	autoJoin, err := p.nk.partyRegistry.PartyJoinRequest(ctx, partyUUID, p.node, &Presence{
		ID:     PresenceID{Node: p.node, SessionID: session.ID()},
		UserID: session.UserID(),
		Meta:   PresenceMeta{Username: session.Username()},
	})
	if err != nil {
		logger.Info("Party join request failed", zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSPartyJoinFailure{PartyID: msg.PartyID, ErrorCode: 2})
	}

	if !autoJoin {
		// Join request is pending approval — do not send success or notify yet.
		logger.Info("Party join request pending approval", zap.Uint64("party_id", msg.PartyID))
		return nil
	}

	if err := p.snsPartyTrackAndJoin(ctx, logger, session, partyUUID, msg.PartyID, params); err != nil {
		logger.Error("Failed to track party join", zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSPartyJoinFailure{PartyID: msg.PartyID, ErrorCode: 1})
	}

	ph, ok := p.nk.partyRegistry.Get(partyUUID)
	if !ok {
		return SendEVRMessages(session, false, &evr.SNSPartyJoinFailure{PartyID: msg.PartyID, ErrorCode: 1})
	}

	ownerAccountID := p.getPartyLeaderAccountID(ctx, logger, ph)

	// Broadcast join notify to other members.
	p.sendEVRMessageToPartyMembers(logger, partyUUID, session.ID(), &evr.SNSPartyJoinNotify{
		PartyID:  msg.PartyID,
		MemberID: params.xpID.AccountId,
	})

	return SendEVRMessages(session, false, &evr.SNSPartyJoinSuccess{
		PartyID: msg.PartyID,
		OwnerID: ownerAccountID,
	})
}

// snsPartyLeaveRequest leaves the current party.
func (p *EvrPipeline) snsPartyLeaveRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	params, ok := LoadParams(ctx)
	if !ok || params.currentPartyID == uuid.Nil {
		return SendEVRMessages(session, false, &evr.SNSPartyLeaveFailure{ErrorCode: 1})
	}

	snsID := params.currentSNSPartyID
	partyUUID := params.currentPartyID

	// Broadcast leave notify before we untrack.
	p.sendEVRMessageToPartyMembers(logger, partyUUID, session.ID(), &evr.SNSPartyLeaveNotify{
		PartyID:  snsID,
		MemberID: params.xpID.AccountId,
	})

	// Untrack triggers partyLeaveListener -> PartyHandler.Leave().
	p.snsPartyLeaveCleanup(ctx, logger, session, params)

	return SendEVRMessages(session, false, &evr.SNSPartyLeaveSuccess{})
}

// snsPartySendInviteRequest sends a party invite to another user.
func (p *EvrPipeline) snsPartySendInviteRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	msg, ok := in.(*evr.SNSPartySendInviteRequest)
	if !ok {
		return fmt.Errorf("expected *evr.SNSPartySendInviteRequest, got %T", in)
	}

	params, ok := LoadParams(ctx)
	if !ok || params.currentPartyID == uuid.Nil {
		logger.Warn("Cannot send invite: not in a party")
		return nil
	}

	targetUserID, err := p.resolveEvrIDToUserID(ctx, params.xpID.PlatformCode, msg.TargetUserID)
	if err != nil || targetUserID == uuid.Nil {
		logger.Info("Invite target not found", zap.Uint64("target", msg.TargetUserID))
		return nil
	}

	// Store the invite.
	inviteList, _ := p.snsPartyInvites.LoadOrStore(targetUserID, &snsPartyInviteList{})
	inviteList.Add(&snsPartyInvite{
		PartyUUID:  params.currentPartyID,
		SNSPartyID: params.currentSNSPartyID,
		InviterID:  params.xpID.AccountId,
		InviterUID: session.UserID(),
		CreatedAt:  time.Now(),
	})

	// Notify the target.
	_ = p.sendEVRMessageByUserID(ctx, logger, targetUserID, &evr.SNSPartyInviteNotify{
		PartyID:   params.currentSNSPartyID,
		InviterID: params.xpID.AccountId,
	})

	return nil
}

// snsPartyLockRequest locks the party (prevents new joins).
func (p *EvrPipeline) snsPartyLockRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	params, ok := LoadParams(ctx)
	if !ok || params.currentPartyID == uuid.Nil {
		return SendEVRMessages(session, false, &evr.SNSPartyLockFailure{ErrorCode: 1})
	}

	ph, ok := p.nk.partyRegistry.Get(params.currentPartyID)
	if !ok {
		return SendEVRMessages(session, false, &evr.SNSPartyLockFailure{ErrorCode: 1})
	}

	ph.Lock()
	ph.Open = false
	ph.Unlock()

	snsID := params.currentSNSPartyID
	p.sendEVRMessageToPartyMembers(logger, params.currentPartyID, uuid.Nil, &evr.SNSPartyLockNotify{
		PartyID: snsID,
	})

	return SendEVRMessages(session, false, &evr.SNSPartyLockSuccess{PartyID: snsID})
}

// snsPartyUnlockRequest unlocks the party (allows joins).
func (p *EvrPipeline) snsPartyUnlockRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	params, ok := LoadParams(ctx)
	if !ok || params.currentPartyID == uuid.Nil {
		return SendEVRMessages(session, false, &evr.SNSPartyUnlockFailure{ErrorCode: 1})
	}

	ph, ok := p.nk.partyRegistry.Get(params.currentPartyID)
	if !ok {
		return SendEVRMessages(session, false, &evr.SNSPartyUnlockFailure{ErrorCode: 1})
	}

	ph.Lock()
	ph.Open = true
	ph.Unlock()

	snsID := params.currentSNSPartyID
	p.sendEVRMessageToPartyMembers(logger, params.currentPartyID, uuid.Nil, &evr.SNSPartyUnlockNotify{
		PartyID: snsID,
	})

	return SendEVRMessages(session, false, &evr.SNSPartyUnlockSuccess{PartyID: snsID})
}

// snsPartyKickRequest kicks a member from the party (owner only).
func (p *EvrPipeline) snsPartyKickRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	msg, ok := in.(*evr.SNSPartyKickRequest)
	if !ok {
		return fmt.Errorf("expected *evr.SNSPartyKickRequest, got %T", in)
	}

	params, ok := LoadParams(ctx)
	if !ok || params.currentPartyID == uuid.Nil {
		return SendEVRMessages(session, false, &evr.SNSPartyKickFailure{ErrorCode: 1})
	}

	// Resolve target from EvrId UUID.
	targetUserID, ok := p.resolveEvrUUIDToUserID(msg.TargetUserUUID)
	if !ok {
		return SendEVRMessages(session, false, &evr.SNSPartyKickFailure{ErrorCode: 2})
	}

	// Find the target's session to build a full UserPresence.
	targetPresence := p.findPartyMemberPresence(params.currentPartyID, targetUserID)
	if targetPresence == nil {
		return SendEVRMessages(session, false, &evr.SNSPartyKickFailure{ErrorCode: 2})
	}

	err := p.nk.partyRegistry.PartyRemove(ctx, params.currentPartyID, p.node, session.ID().String(), p.node, targetPresence)
	if err != nil {
		logger.Info("Party kick failed", zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSPartyKickFailure{ErrorCode: 1})
	}

	snsID := params.currentSNSPartyID
	targetAccountID, _ := p.resolveUserIDToAccountID(ctx, targetUserID)

	p.sendEVRMessageToPartyMembers(logger, params.currentPartyID, uuid.Nil, &evr.SNSPartyKickNotify{
		PartyID: snsID,
		KickID:  targetAccountID,
	})

	return SendEVRMessages(session, false, &evr.SNSPartyKickSuccess{})
}

// snsPartyPassOwnershipRequest transfers party leadership (owner only).
func (p *EvrPipeline) snsPartyPassOwnershipRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	msg, ok := in.(*evr.SNSPartyPassOwnershipRequest)
	if !ok {
		return fmt.Errorf("expected *evr.SNSPartyPassOwnershipRequest, got %T", in)
	}

	params, ok := LoadParams(ctx)
	if !ok || params.currentPartyID == uuid.Nil {
		return SendEVRMessages(session, false, &evr.SNSPartyPassFailure{ErrorCode: 1})
	}

	targetUserID, ok := p.resolveEvrUUIDToUserID(msg.TargetUserUUID)
	if !ok {
		return SendEVRMessages(session, false, &evr.SNSPartyPassFailure{ErrorCode: 2})
	}

	targetPresence := p.findPartyMemberPresence(params.currentPartyID, targetUserID)
	if targetPresence == nil {
		return SendEVRMessages(session, false, &evr.SNSPartyPassFailure{ErrorCode: 2})
	}

	err := p.nk.partyRegistry.PartyPromote(ctx, params.currentPartyID, p.node, session.ID().String(), p.node, targetPresence)
	if err != nil {
		logger.Info("Party promote failed", zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSPartyPassFailure{ErrorCode: 1})
	}

	snsID := params.currentSNSPartyID
	targetAccountID, _ := p.resolveUserIDToAccountID(ctx, targetUserID)

	p.sendEVRMessageToPartyMembers(logger, params.currentPartyID, uuid.Nil, &evr.SNSPartyPassNotify{
		PartyID:    snsID,
		NewOwnerID: targetAccountID,
	})

	return SendEVRMessages(session, false, &evr.SNSPartyPassSuccess{})
}

// snsPartyRespondToInviteRequest accepts or rejects a party invite.
func (p *EvrPipeline) snsPartyRespondToInviteRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	msg, ok := in.(*evr.SNSPartyRespondToInviteRequest)
	if !ok {
		return fmt.Errorf("expected *evr.SNSPartyRespondToInviteRequest, got %T", in)
	}

	params, ok := LoadParams(ctx)
	if !ok {
		logger.Error("Failed to load session parameters")
		return nil
	}

	userID := session.UserID()

	// Find the invite for this user.
	inviteList, ok := p.snsPartyInvites.Load(userID)
	if !ok {
		logger.Info("No pending invites for user")
		return nil
	}

	// The TargetUserUUID is the inviter's EvrId UUID. Find the invite from that party.
	inviterUserID, _ := p.resolveEvrUUIDToUserID(msg.TargetUserUUID)
	var invite *snsPartyInvite
	if inviterUserID != uuid.Nil {
		// Search by inviter.
		inviteList.RLock()
		for _, inv := range inviteList.invites {
			if inv.InviterUID == inviterUserID {
				invite = inv
				break
			}
		}
		inviteList.RUnlock()
	}

	if invite == nil {
		// Fall back: use the first available invite.
		inviteList.RLock()
		if len(inviteList.invites) > 0 {
			invite = inviteList.invites[0]
		}
		inviteList.RUnlock()
	}

	if invite == nil {
		logger.Info("No matching invite found")
		return nil
	}

	partyUUID := invite.PartyUUID
	snsPartyID := invite.SNSPartyID

	// Remove the invite regardless of accept/reject.
	inviteList.RemoveByParty(partyUUID)

	if msg.Param == 0 {
		// Reject — just remove the invite (already done).
		return nil
	}

	// Accept — join the party.
	if params.currentPartyID != uuid.Nil {
		p.snsPartyLeaveCleanup(ctx, logger, session, params)
		params, _ = LoadParams(ctx)
	}

	autoJoin, err := p.nk.partyRegistry.PartyJoinRequest(ctx, partyUUID, p.node, &Presence{
		ID:     PresenceID{Node: p.node, SessionID: session.ID()},
		UserID: session.UserID(),
		Meta:   PresenceMeta{Username: session.Username()},
	})
	if err != nil {
		logger.Info("Party join via invite failed", zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSPartyJoinFailure{PartyID: snsPartyID, ErrorCode: 2})
	}

	if !autoJoin {
		// Join request is pending approval — do not send success or notify yet.
		logger.Info("Party join via invite pending approval", zap.Uint64("party_id", snsPartyID))
		return nil
	}

	if err := p.snsPartyTrackAndJoin(ctx, logger, session, partyUUID, snsPartyID, params); err != nil {
		return SendEVRMessages(session, false, &evr.SNSPartyJoinFailure{PartyID: snsPartyID, ErrorCode: 1})
	}

	ph, ok := p.nk.partyRegistry.Get(partyUUID)
	if !ok {
		return SendEVRMessages(session, false, &evr.SNSPartyJoinFailure{PartyID: snsPartyID, ErrorCode: 1})
	}

	ownerAccountID := p.getPartyLeaderAccountID(ctx, logger, ph)

	p.sendEVRMessageToPartyMembers(logger, partyUUID, session.ID(), &evr.SNSPartyJoinNotify{
		PartyID:  snsPartyID,
		MemberID: params.xpID.AccountId,
	})

	return SendEVRMessages(session, false, &evr.SNSPartyJoinSuccess{
		PartyID: snsPartyID,
		OwnerID: ownerAccountID,
	})
}

// snsPartyUpdateRequest acknowledges a party metadata update.
func (p *EvrPipeline) snsPartyUpdateRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	params, ok := LoadParams(ctx)
	if !ok || params.currentPartyID == uuid.Nil {
		return SendEVRMessages(session, false, &evr.SNSPartyUpdateFailure{ErrorCode: 1})
	}

	snsID := params.currentSNSPartyID

	p.sendEVRMessageToPartyMembers(logger, params.currentPartyID, session.ID(), &evr.SNSPartyUpdateNotify{
		PartyID: snsID,
	})

	return SendEVRMessages(session, false, &evr.SNSPartyUpdateSuccess{PartyID: snsID})
}

// snsPartyUpdateMemberRequest acknowledges a member data update.
func (p *EvrPipeline) snsPartyUpdateMemberRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	params, ok := LoadParams(ctx)
	if !ok || params.currentPartyID == uuid.Nil {
		return SendEVRMessages(session, false, &evr.SNSPartyUpdateMemberFailure{ErrorCode: 1})
	}

	snsID := params.currentSNSPartyID

	p.sendEVRMessageToPartyMembers(logger, params.currentPartyID, session.ID(), &evr.SNSPartyUpdateMemberNotify{
		PartyID:  snsID,
		MemberID: params.xpID.AccountId,
	})

	return SendEVRMessages(session, false, &evr.SNSPartyUpdateMemberSuccess{PartyID: snsID})
}

// snsPartyInviteListRefreshRequest returns the count of pending invites.
func (p *EvrPipeline) snsPartyInviteListRefreshRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	userID := session.UserID()

	var count uint32
	if inviteList, ok := p.snsPartyInvites.Load(userID); ok {
		count = uint32(inviteList.Count())
	}

	return SendEVRMessages(session, false, &evr.SNSPartyInviteListResponse{Count: count})
}

// ---------------------------------------------------------------------------
// Helper: find a party member's full UserPresence
// ---------------------------------------------------------------------------

func (p *EvrPipeline) findPartyMemberPresence(partyUUID uuid.UUID, targetUserID uuid.UUID) *rtapi.UserPresence {
	stream := PresenceStream{Mode: StreamModeParty, Subject: partyUUID, Label: p.node}
	presences := p.nk.tracker.ListByStream(stream, true, true)
	for _, presence := range presences {
		if presence.UserID == targetUserID {
			session := p.nk.sessionRegistry.Get(presence.ID.SessionID)
			if session == nil {
				continue
			}
			return &rtapi.UserPresence{
				UserId:    targetUserID.String(),
				SessionId: presence.ID.SessionID.String(),
				Username:  session.Username(),
			}
		}
	}
	return nil
}
