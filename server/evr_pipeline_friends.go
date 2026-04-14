package server

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// resolveEvrIDToUserID looks up a Nakama user UUID from an EvrId AccountId.
// It tries the caller's PlatformCode first, then falls back to all known platforms
// to support cross-platform friend operations.
func (p *EvrPipeline) resolveEvrIDToUserID(ctx context.Context, platformCode evr.PlatformCode, accountID uint64) (uuid.UUID, error) {
	// Try the caller's platform first, then all others.
	platforms := []evr.PlatformCode{platformCode}
	for _, pc := range []evr.PlatformCode{evr.DSC, evr.OVR, evr.OVR_ORG, evr.STM, evr.DMO, evr.XBX, evr.BOT} {
		if pc != platformCode {
			platforms = append(platforms, pc)
		}
	}

	for _, pc := range platforms {
		evrID := evr.EvrId{PlatformCode: pc, AccountId: accountID}
		deviceID := evrID.String()

		var dbUserID string
		err := p.db.QueryRowContext(ctx, "SELECT user_id FROM user_device WHERE id = $1", deviceID).Scan(&dbUserID)
		if err == sql.ErrNoRows {
			continue
		}
		if err != nil {
			return uuid.Nil, fmt.Errorf("device lookup: %w", err)
		}

		uid, err := uuid.FromString(dbUserID)
		if err != nil {
			return uuid.Nil, fmt.Errorf("corrupt user_id in user_device for %s: %w", deviceID, err)
		}
		return uid, nil
	}

	return uuid.Nil, fmt.Errorf("user not found for account id %d", accountID)
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
	msg, ok := in.(*evr.SNSFriendInviteRequest)
	if !ok {
		return fmt.Errorf("expected *evr.SNSFriendInviteRequest, got %T", in)
	}

	params, ok := LoadParams(ctx)
	if !ok {
		_ = SendEVRMessages(session, false, &evr.SNSFriendInviteFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendInviteErrorBadRequest,
		})
		return fmt.Errorf("failed to load session parameters")
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

	// Check if a relationship already exists with the target.
	var existingState int32 = -1
	err = p.db.QueryRowContext(ctx,
		"SELECT state FROM user_edge WHERE source_id = $1 AND destination_id = $2",
		userID, targetUserID).Scan(&existingState)
	if err != nil && err != sql.ErrNoRows {
		logger.Error("Failed to query friend state", zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSFriendInviteFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendInviteErrorBadRequest,
		})
	}

	switch existingState {
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

	err = AddFriends(ctx, logger, p.db, p.nk.tracker, p.nk.router, userID, session.Username(), []string{targetUserID.String()}, "{}")
	if err != nil {
		logger.Error("Failed to add friend", zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSFriendInviteFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendInviteErrorBadRequest,
		})
	}

	// Check if AddFriends actually accepted an existing incoming invite (mutual add).
	// Query the specific edge between these two users to see if state is now 0 (friends).
	var edgeState int32
	err = p.db.QueryRowContext(ctx,
		"SELECT state FROM user_edge WHERE source_id = $1 AND destination_id = $2",
		userID, targetUserID).Scan(&edgeState)
	if err == nil && edgeState == FriendStateFriends {
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

// snsFriendAcceptRequest handles the SNSFriendAcceptRequest message (hash 0x1bbcb7e810af4620).
//
// Despite the token name, this is the wire message for remove_friend in the
// original client (pnsrad). The client sends this with routing_id = 0xFFFFFFFFFFFFFFFF
// to remove an established friend. The server determines the action by current state:
//   - FriendStateFriends → remove friend, notify target with RemoveNotify
//   - FriendInvitationSent → withdraw sent invite, notify target with WithdrawnNotify
//   - No relationship → acknowledge silently
func (p *EvrPipeline) snsFriendAcceptRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	msg, ok := in.(*evr.SNSFriendAcceptRequest)
	if !ok {
		return fmt.Errorf("expected *evr.SNSFriendAcceptRequest, got %T", in)
	}

	params, ok := LoadParams(ctx)
	if !ok {
		_ = SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
			FriendID: msg.TargetUserID,
		})
		return fmt.Errorf("failed to load session parameters")
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

	// Determine current relationship state.
	var currentState int32 = -1
	err = p.db.QueryRowContext(ctx,
		"SELECT state FROM user_edge WHERE source_id = $1 AND destination_id = $2",
		userID, targetUserID).Scan(&currentState)
	if err != nil && err != sql.ErrNoRows {
		logger.Error("Failed to query friend state", zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
			FriendID: msg.TargetUserID,
		})
	}

	targetIDStr := targetUserID.String()

	switch currentState {
	case FriendStateFriends:
		// Remove an established friend.
		if err := DeleteFriends(ctx, logger, p.db, userID, []string{targetIDStr}); err != nil {
			logger.Error("Failed to delete friend", zap.Error(err))
			return SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
				FriendID: msg.TargetUserID,
			})
		}
		if err := SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
			FriendID: msg.TargetUserID,
		}); err != nil {
			return err
		}
		_ = p.sendEVRMessageByUserID(ctx, logger, targetUserID, &evr.SNSFriendRemoveNotify{
			FriendID: params.xpID.AccountId,
		})

	case FriendInvitationSent:
		// Withdraw a sent invite.
		if err := DeleteFriends(ctx, logger, p.db, userID, []string{targetIDStr}); err != nil {
			logger.Error("Failed to withdraw invite", zap.Error(err))
			return SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
				FriendID: msg.TargetUserID,
			})
		}
		if err := SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
			FriendID: msg.TargetUserID,
		}); err != nil {
			return err
		}
		_ = p.sendEVRMessageByUserID(ctx, logger, targetUserID, &evr.SNSFriendWithdrawnNotify{
			FriendID: params.xpID.AccountId,
		})

	default:
		return SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
			FriendID: msg.TargetUserID,
		})
	}

	return nil
}

// snsFriendRemoveRequest handles the SNSFriendRemoveRequest message (hash 0x78908988b7fe6db4).
//
// Despite the token name, this is the wire message for accept_friend, reject_friend_request,
// and block_user in the original client (pnsrad). All three share the same hash — the
// server differentiates by the current relationship state:
//   - FriendInvitationReceived → accept the pending invite (AddFriends promotes to friends)
//   - FriendStateFriends → block the user (BlockFriends sets state=3)
//   - FriendInvitationSent → reject/block (DeleteFriends + optional block)
//   - No relationship → block the user
func (p *EvrPipeline) snsFriendRemoveRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	msg, ok := in.(*evr.SNSFriendRemoveRequest)
	if !ok {
		return fmt.Errorf("expected *evr.SNSFriendRemoveRequest, got %T", in)
	}

	params, ok := LoadParams(ctx)
	if !ok {
		_ = SendEVRMessages(session, false, &evr.SNSFriendAcceptFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendAcceptErrorNotFound,
		})
		return fmt.Errorf("failed to load session parameters")
	}

	userID := session.UserID()
	targetUserID, err := p.resolveEvrIDToUserID(ctx, params.xpID.PlatformCode, msg.TargetUserID)
	if err != nil || targetUserID == uuid.Nil {
		logger.Info("Friend action target not found",
			zap.Uint64("target_account_id", msg.TargetUserID))
		return SendEVRMessages(session, false, &evr.SNSFriendAcceptFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendAcceptErrorNotFound,
		})
	}

	// Determine current relationship state.
	var currentState int32 = -1
	err = p.db.QueryRowContext(ctx,
		"SELECT state FROM user_edge WHERE source_id = $1 AND destination_id = $2",
		userID, targetUserID).Scan(&currentState)
	if err != nil && err != sql.ErrNoRows {
		logger.Error("Failed to query friend state", zap.Error(err))
		return SendEVRMessages(session, false, &evr.SNSFriendAcceptFailure{
			FriendID:   msg.TargetUserID,
			StatusCode: evr.FriendAcceptErrorNotFound,
		})
	}

	targetIDStr := targetUserID.String()

	switch currentState {
	case FriendInvitationReceived:
		// Accept a pending incoming invite.
		err = AddFriends(ctx, logger, p.db, p.nk.tracker, p.nk.router, userID, session.Username(), []string{targetIDStr}, "{}")
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
		_ = p.sendEVRMessageByUserID(ctx, logger, targetUserID, &evr.SNSFriendAcceptNotify{
			FriendID: params.xpID.AccountId,
		})

	case FriendInvitationSent:
		// Current user sent the invite — withdraw it and block the target.
		if err := DeleteFriends(ctx, logger, p.db, userID, []string{targetIDStr}); err != nil {
			logger.Error("Failed to withdraw invite", zap.Error(err))
			return SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
				FriendID: msg.TargetUserID,
			})
		}
		if err := BlockFriends(ctx, logger, p.db, p.nk.tracker, userID, []string{targetIDStr}); err != nil {
			logger.Error("Failed to block user after withdraw", zap.Error(err))
		}
		if err := SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
			FriendID: msg.TargetUserID,
		}); err != nil {
			return err
		}
		_ = p.sendEVRMessageByUserID(ctx, logger, targetUserID, &evr.SNSFriendWithdrawnNotify{
			FriendID: params.xpID.AccountId,
		})

	case FriendStateFriends:
		// Block an established friend.
		if err := BlockFriends(ctx, logger, p.db, p.nk.tracker, userID, []string{targetIDStr}); err != nil {
			logger.Error("Failed to block friend", zap.Error(err))
			return SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
				FriendID: msg.TargetUserID,
			})
		}
		if err := SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
			FriendID: msg.TargetUserID,
		}); err != nil {
			return err
		}
		_ = p.sendEVRMessageByUserID(ctx, logger, targetUserID, &evr.SNSFriendRemoveNotify{
			FriendID: params.xpID.AccountId,
		})

	default:
		// No prior relationship — block the user.
		if err := BlockFriends(ctx, logger, p.db, p.nk.tracker, userID, []string{targetIDStr}); err != nil {
			logger.Error("Failed to block user", zap.Error(err))
		}
		return SendEVRMessages(session, false, &evr.SNSFriendRemoveResponse{
			FriendID: msg.TargetUserID,
		})
	}

	return nil
}

// snsFriendListResponse builds and sends the friend list counts to the client.
func (p *EvrPipeline) snsFriendListSubscribeRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	logger.Info("Friend list subscribe request received")
	return p.sendFriendListResponse(ctx, logger, session)
}

func (p *EvrPipeline) snsFriendListRefreshRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	logger.Info("Friend list refresh request received")
	return p.sendFriendListResponse(ctx, logger, session)
}

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
