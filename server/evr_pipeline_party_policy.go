package server

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// SNS party join policies, numbered as the game numbers them (R15NetPartySetJoinPolicyNode, social
// slot 16; pnsovr 0x180092470 mapped them to the Oculus room policies invite-only, friends,
// friends-of-members and everyone). A party with no policy set admits everyone, as SNS parties always
// have (snsPartyCreateRequest creates them open).
const (
	snsPartyPolicyInviteOnly       uint8 = 0
	snsPartyPolicyFriends          uint8 = 1
	snsPartyPolicyFriendsOfMembers uint8 = 2
	snsPartyPolicyEveryone         uint8 = 3
)

// snsPartyPolicyAdmits is the join rule. An invited player is always admitted: being invited is what
// every policy allows. Otherwise invite only refuses, friends needs a mutual friendship with the
// leader, friends of members one with any member (the leader included), everyone admits.
func snsPartyPolicyAdmits(policy uint8, invited, friendOfLeader, friendOfMember bool) bool {
	if invited {
		return true
	}
	switch policy {
	case snsPartyPolicyInviteOnly:
		return false
	case snsPartyPolicyFriends:
		return friendOfLeader
	case snsPartyPolicyFriendsOfMembers:
		return friendOfLeader || friendOfMember
	default:
		return true
	}
}

// snsPartyPolicy is the party's join policy, everyone when none was set.
func (p *EvrPipeline) snsPartyPolicy(partyUUID uuid.UUID) uint8 {
	if policy, ok := p.snsPartyPolicies.Load(partyUUID); ok {
		return policy
	}
	return snsPartyPolicyEveryone
}

// mutualFriendsAmong returns which of `others` have a mutual friendship (user_edge state 0, both
// directions are written as 0 once accepted) with userID.
func mutualFriendsAmong(ctx context.Context, db *sql.DB, userID uuid.UUID, others []uuid.UUID) (map[uuid.UUID]bool, error) {
	out := make(map[uuid.UUID]bool, len(others))
	if len(others) == 0 {
		return out, nil
	}
	ids := make([]string, 0, len(others))
	for _, id := range others {
		ids = append(ids, id.String())
	}
	rows, err := db.QueryContext(ctx,
		"SELECT destination_id FROM user_edge WHERE source_id = $1 AND destination_id = ANY($2::UUID[]) AND state = 0",
		userID, ids)
	if err != nil {
		return nil, fmt.Errorf("friend lookup: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id uuid.UUID
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("friend lookup: %w", err)
		}
		out[id] = true
	}
	return out, rows.Err()
}

// snsPartyJoinAllowed applies the party's join policy to a player asking to join it by id (not by an
// invite's accept, which is always admitted). It reports the policy and why, for the caller's log.
func (p *EvrPipeline) snsPartyJoinAllowed(ctx context.Context, joiner uuid.UUID, partyUUID uuid.UUID, ph *PartyHandler) (bool, uint8, error) {
	policy := p.snsPartyPolicy(partyUUID)
	invited := false
	if list, ok := p.snsPartyInvites.Load(joiner); ok && list.FindByParty(partyUUID) != nil {
		invited = true
	}
	if invited || policy == snsPartyPolicyEveryone || policy == snsPartyPolicyInviteOnly {
		return snsPartyPolicyAdmits(policy, invited, false, false), policy, nil
	}
	ph.RLock()
	var leaderID uuid.UUID
	if ph.leader != nil && ph.leader.UserPresence != nil {
		leaderID = uuid.FromStringOrNil(ph.leader.UserPresence.UserId)
	}
	ph.RUnlock()
	members := []uuid.UUID{}
	for _, m := range ph.members.List() {
		if m.UserPresence != nil {
			if id := uuid.FromStringOrNil(m.UserPresence.UserId); id != uuid.Nil {
				members = append(members, id)
			}
		}
	}
	friends, err := mutualFriendsAmong(ctx, p.db, joiner, members)
	if err != nil {
		return false, policy, err
	}
	friendOfMember := false
	for _, id := range members {
		if friends[id] {
			friendOfMember = true
		}
	}
	return snsPartyPolicyAdmits(policy, false, friends[leaderID], friendOfMember), policy, nil
}

// snsPartySetJoinPolicyRequest stores the join policy the party leader's game set. Members are told
// the party changed (SNSPartyUpdateNotify); only the leader may set it.
func (p *EvrPipeline) snsPartySetJoinPolicyRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	msg, ok := in.(*evr.SNSPartySetJoinPolicyRequest)
	if !ok {
		return fmt.Errorf("expected *evr.SNSPartySetJoinPolicyRequest, got %T", in)
	}
	params, ok := LoadParams(ctx)
	if !ok || params.currentPartyID == uuid.Nil {
		logger.Info("Party join policy refused: not in a party", zap.Uint64("policy", msg.TargetParam))
		return SendEVRMessages(session, false, &evr.SNSPartyUpdateFailure{ErrorCode: 1})
	}
	ph, ok := p.nk.partyRegistry.Get(params.currentPartyID)
	if !ok {
		return SendEVRMessages(session, false, &evr.SNSPartyUpdateFailure{ErrorCode: 1})
	}
	ph.RLock()
	isLeader := ph.leader != nil && ph.leader.UserPresence != nil && ph.leader.UserPresence.SessionId == session.ID().String()
	ph.RUnlock()
	if !isLeader || msg.TargetParam > uint64(snsPartyPolicyEveryone) {
		logger.Info("Party join policy refused", zap.Bool("leader", isLeader), zap.Uint64("policy", msg.TargetParam))
		return SendEVRMessages(session, false, &evr.SNSPartyUpdateFailure{ErrorCode: 2})
	}
	policy := uint8(msg.TargetParam)
	p.snsPartyPolicies.Store(params.currentPartyID, policy)
	logger.Info("Party join policy set", zap.String("party", params.currentPartyID.String()),
		zap.Uint64("sns_party_id", params.currentSNSPartyID), zap.Uint8("policy", policy))
	p.sendEVRMessageToPartyMembers(logger, params.currentPartyID, session.ID(), &evr.SNSPartyUpdateNotify{PartyID: params.currentSNSPartyID})
	return SendEVRMessages(session, false, &evr.SNSPartyUpdateSuccess{PartyID: params.currentSNSPartyID})
}

// snsPartyLeaveForJoin leaves the session's current party once it has been admitted to another, as an
// explicit leave does (the other members are told, any reservation is cleared, the stream untracked).
// A player whose join or accept fails stays in the party they were in.
func (p *EvrPipeline) snsPartyLeaveForJoin(ctx context.Context, logger *zap.Logger, session *sessionWS, params *SessionParameters, joined uuid.UUID) {
	old := params.currentPartyID
	if old == uuid.Nil || old == joined {
		return
	}
	p.sendEVRMessageToPartyMembers(logger, old, session.ID(), &evr.SNSPartyLeaveNotify{
		PartyID:  params.currentSNSPartyID,
		MemberID: p.sessionAccountID(ctx, session, params),
	})
	p.clearMemberReservation(ctx, logger, session, old)
	p.snsPartyLeaveCleanup(ctx, logger, session, params)
	logger.Info("Left party for the one joined", zap.String("left", old.String()), zap.String("joined", joined.String()))
}
