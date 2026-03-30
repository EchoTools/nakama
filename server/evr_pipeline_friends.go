package server

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// resolveEvrIDToUserID looks up a Nakama user UUID from an EvrId AccountId.
// It constructs the full EvrId using the caller's PlatformCode (from session params),
// then queries the user_device table where the EvrId string is stored as a device ID.
func (p *EvrPipeline) resolveEvrIDToUserID(ctx context.Context, platformCode evr.PlatformCode, accountID uint64) (uuid.UUID, error) {
	evrID := evr.EvrId{PlatformCode: platformCode, AccountId: accountID}
	deviceID := evrID.String()

	var dbUserID string
	err := p.db.QueryRowContext(ctx, "SELECT user_id FROM user_device WHERE id = $1", deviceID).Scan(&dbUserID)
	if err != nil {
		if err == sql.ErrNoRows {
			return uuid.Nil, fmt.Errorf("user not found for device %s", deviceID)
		}
		return uuid.Nil, fmt.Errorf("device lookup: %w", err)
	}

	return uuid.FromStringOrNil(dbUserID), nil
}

// sendEVRMessageByUserID sends an EVR message to a user's login session if they're online.
func (p *EvrPipeline) sendEVRMessageByUserID(ctx context.Context, logger *zap.Logger, userID uuid.UUID, messages ...evr.Message) error {
	presences, err := p.nk.StreamUserList(StreamModeService, userID.String(), "", StreamLabelLoginService, false, true)
	if err != nil {
		return fmt.Errorf("stream list: %w", err)
	}

	for _, presence := range presences {
		if presence.GetUserId() != userID.String() {
			continue
		}

		sessionID := uuid.FromStringOrNil(presence.GetSessionId())
		if sessionID == uuid.Nil {
			continue
		}

		session := p.nk.sessionRegistry.Get(sessionID)
		if session == nil {
			continue
		}

		if err := SendEVRMessages(session, false, messages...); err != nil {
			logger.Warn("Failed to send EVR message to user",
				zap.String("target_uid", userID.String()),
				zap.Error(err))
			continue
		}
		return nil
	}

	// User not online — not an error, notifications are best-effort.
	return nil
}

// snsFriendInviteRequest handles a client request to send a friend invitation.
func (p *EvrPipeline) snsFriendInviteRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	msg := in.(*evr.SNSFriendInviteRequest)

	params, ok := LoadParams(ctx)
	if !ok {
		logger.Error("Failed to load session parameters")
		return nil
	}

	userID := session.UserID()
	targetUserID, err := p.resolveEvrIDToUserID(ctx, params.xpID.PlatformCode, msg.TargetUserID)
	if err != nil || targetUserID == uuid.Nil {
		logger.Info("Friend invite target not found",
			zap.Uint64("target_account_id", msg.TargetUserID),
			zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSFriendInviteFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendInviteErrorNotFound,
		})
	}

	if targetUserID == userID {
		return SendEVRMessages(session, false, &evr.SNSFriendInviteFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendInviteErrorSelf,
		})
	}

	// Check if already friends or pending.
	friendsList, err := ListFriends(ctx, logger, p.db, p.nk.statusRegistry, userID, 1000, nil, "")
	if err != nil {
		logger.Error("Failed to list friends", zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSFriendInviteFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendInviteErrorBadRequest,
		})
	}

	for _, f := range friendsList.Friends {
		if f.User.Id == targetUserID.String() {
			switch f.State.Value {
			case FriendStateFriends:
				return SendEVRMessages(session, false, &evr.SNSFriendInviteFailure{
					FriendID:   msg.TargetUserID,
					StatusCode: evr.FriendInviteErrorAlready,
				})
			case FriendInvitationSent:
				return SendEVRMessages(session, false, &evr.SNSFriendInviteFailure{
					FriendID:   msg.TargetUserID,
					StatusCode: evr.FriendInviteErrorPending,
				})
			case FriendStateBlocked:
				return SendEVRMessages(session, false, &evr.SNSFriendInviteFailure{
					FriendID:   msg.TargetUserID,
					StatusCode: evr.FriendInviteErrorBadRequest,
				})
			}
		}
	}

	err = AddFriends(ctx, logger, p.db, p.nk.tracker, p.nk.router, userID, session.Username(), []string{targetUserID.String()}, "{}")
	if err != nil {
		logger.Error("Failed to add friend", zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSFriendInviteFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendInviteErrorBadRequest,
		})
	}

	// Check if AddFriends actually accepted an existing incoming invite (mutual add).
	// Re-read the edge to see if state is now 0 (friends).
	updatedFriends, err := ListFriends(ctx, logger, p.db, p.nk.statusRegistry, userID, 1, wrapperspb.Int32(FriendStateFriends), "")
	if err == nil {
		for _, f := range updatedFriends.Friends {
			if f.User.Id == targetUserID.String() {
				// Mutual add — both users are now friends.
				if err := SendEVRMessages(session, false, &evr.SNSFriendAcceptSuccess{
					FriendID: msg.TargetUserID,
				}); err != nil {
					logger.Warn("Failed to send accept success", zap.Error(err))
				}
				// Notify the other user.
				_ = p.sendEVRMessageByUserID(ctx, logger, targetUserID, &evr.SNSFriendAcceptNotify{
					FriendID: params.xpID.AccountId,
				})
				return nil
			}
		}
	}

	// Normal invite sent.
	if err := SendEVRMessages(session, false, &evr.SNSFriendInviteSuccess{
		FriendID: msg.TargetUserID,
	}); err != nil {
		return err
	}

	// Notify the target user they have a pending invite.
	_ = p.sendEVRMessageByUserID(ctx, logger, targetUserID, &evr.SNSFriendInviteNotify{
		FriendID: params.xpID.AccountId,
	})

	return nil
}

// snsFriendAcceptRequest handles a client request to accept a pending friend invitation.
func (p *EvrPipeline) snsFriendAcceptRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	msg := in.(*evr.SNSFriendAcceptRequest)

	params, ok := LoadParams(ctx)
	if !ok {
		logger.Error("Failed to load session parameters")
		return nil
	}

	userID := session.UserID()
	targetUserID, err := p.resolveEvrIDToUserID(ctx, params.xpID.PlatformCode, msg.TargetUserID)
	if err != nil || targetUserID == uuid.Nil {
		logger.Info("Friend accept target not found",
			zap.Uint64("target_account_id", msg.TargetUserID),
			zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSFriendAcceptFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendAcceptErrorNotFound,
		})
	}

	// Verify there's actually a pending incoming invite from this user.
	friendsList, err := ListFriends(ctx, logger, p.db, p.nk.statusRegistry, userID, 1000, wrapperspb.Int32(FriendInvitationReceived), "")
	if err != nil {
		logger.Error("Failed to list friends", zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSFriendAcceptFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendAcceptErrorNotFound,
		})
	}

	found := false
	for _, f := range friendsList.Friends {
		if f.User.Id == targetUserID.String() {
			found = true
			break
		}
	}

	if !found {
		return SendEVRMessages(session, false, &evr.SNSFriendAcceptFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendAcceptErrorNotFound,
		})
	}

	// AddFriends with an existing incoming invite promotes both edges to state=0 (friends).
	err = AddFriends(ctx, logger, p.db, p.nk.tracker, p.nk.router, userID, session.Username(), []string{targetUserID.String()}, "{}")
	if err != nil {
		logger.Error("Failed to accept friend", zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSFriendAcceptFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendAcceptErrorNotFound,
		})
	}

	if err := SendEVRMessages(session, false, &evr.SNSFriendAcceptSuccess{
		FriendID: msg.TargetUserID,
	}); err != nil {
		return err
	}

	// Notify the original inviter that their request was accepted.
	_ = p.sendEVRMessageByUserID(ctx, logger, targetUserID, &evr.SNSFriendAcceptNotify{
		FriendID: params.xpID.AccountId,
	})

	return nil
}

// snsFriendRemoveRequest handles friend removal, rejection, and blocking.
// The server differentiates the action by the current relationship state.
func (p *EvrPipeline) snsFriendRemoveRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	msg := in.(*evr.SNSFriendRemoveRequest)

	params, ok := LoadParams(ctx)
	if !ok {
		logger.Error("Failed to load session parameters")
		return nil
	}

	userID := session.UserID()
	targetUserID, err := p.resolveEvrIDToUserID(ctx, params.xpID.PlatformCode, msg.TargetUserID)
	if err != nil || targetUserID == uuid.Nil {
		logger.Info("Friend remove target not found",
			zap.Uint64("target_account_id", msg.TargetUserID))
		return SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
			FriendID: msg.TargetUserID,
		})
	}

	// Determine current relationship state to decide action and notification type.
	friendsList, err := ListFriends(ctx, logger, p.db, p.nk.statusRegistry, userID, 1000, nil, "")
	if err != nil {
		logger.Error("Failed to list friends", zap.Error(err))
		return nil
	}

	var currentState int32 = -1
	for _, f := range friendsList.Friends {
		if f.User.Id == targetUserID.String() {
			currentState = f.State.Value
			break
		}
	}

	targetIDStr := targetUserID.String()

	switch currentState {
	case FriendStateFriends:
		// Remove an established friend.
		if err := DeleteFriends(ctx, logger, p.db, userID, []string{targetIDStr}); err != nil {
			logger.Error("Failed to delete friend", zap.Error(err))
		}
		if err := SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
			FriendID: msg.TargetUserID,
		}); err != nil {
			return err
		}
		// Notify the other user they were removed.
		_ = p.sendEVRMessageByUserID(ctx, logger, targetUserID, &evr.SNSFriendRemoveNotify{
			FriendID: params.xpID.AccountId,
		})

	case FriendInvitationSent:
		// Withdraw a sent invite.
		if err := DeleteFriends(ctx, logger, p.db, userID, []string{targetIDStr}); err != nil {
			logger.Error("Failed to withdraw invite", zap.Error(err))
		}
		if err := SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
			FriendID: msg.TargetUserID,
		}); err != nil {
			return err
		}
		// Notify the target that the invite was withdrawn.
		_ = p.sendEVRMessageByUserID(ctx, logger, targetUserID, &evr.SNSFriendWithdrawnNotify{
			FriendID: params.xpID.AccountId,
		})

	case FriendInvitationReceived:
		// Reject an incoming invite.
		if err := DeleteFriends(ctx, logger, p.db, userID, []string{targetIDStr}); err != nil {
			logger.Error("Failed to reject invite", zap.Error(err))
		}
		if err := SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
			FriendID: msg.TargetUserID,
		}); err != nil {
			return err
		}
		// Notify the inviter that their request was rejected.
		_ = p.sendEVRMessageByUserID(ctx, logger, targetUserID, &evr.SNSFriendRejectNotify{
			FriendID: params.xpID.AccountId,
		})

	default:
		// No relationship or already blocked — just acknowledge.
		return SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
			FriendID: msg.TargetUserID,
		})
	}

	return nil
}

// snsFriendListResponse builds and sends the friend list counts to the client.
func (p *EvrPipeline) sendFriendListResponse(ctx context.Context, logger *zap.Logger, session *sessionWS) error {
	userID := session.UserID()

	friends, err := ListPlayerFriends(ctx, logger, p.db, p.nk.statusRegistry, userID)
	if err != nil {
		logger.Error("Failed to list friends for counts", zap.Error(err))
		return nil
	}

	var nOnline, nOffline, nBusy, nSent, nRecv uint32
	for _, f := range friends {
		switch f.State.Value {
		case FriendStateFriends:
			if f.User.Online {
				nOnline++
			} else {
				nOffline++
			}
		case FriendInvitationSent:
			nSent++
		case FriendInvitationReceived:
			nRecv++
		}
	}

	return SendEVRMessages(session, false, &evr.SNSFriendListResponse{
		NOnline:  nOnline,
		NBusy:    nBusy,
		NOffline: nOffline,
		NSent:    nSent,
		NRecv:    nRecv,
	})
}
