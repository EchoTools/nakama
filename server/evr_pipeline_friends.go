package server

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
)

// discordAccountID converts a Discord snowflake into the uint64 the wire carries as an EvrId
// AccountId. A user's Discord id is their identity on the friends and party wire: the platform
// part of an EvrId is ignored, so the same person has the same id no matter which platform
// their client logged in as.
func discordAccountID(discordID string) (uint64, bool) {
	id, err := strconv.ParseUint(discordID, 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}

// sessionAccountID is the AccountId this session's own user goes by in friend and party messages:
// the Discord id, the same value every other client is shown for them. It falls back to the
// EvrId the session logged in with only when the user has no Discord id.
func (p *EvrPipeline) sessionAccountID(ctx context.Context, session *sessionWS, params *SessionParameters) uint64 {
	if accountID, err := p.resolveUserIDToAccountID(ctx, session.UserID()); err == nil {
		return accountID
	}
	return params.xpID.AccountId
}

// resolveEvrIDToUserID looks up a Nakama user UUID from an EvrId AccountId. The AccountId is a
// Discord id, so the user is found by it directly and the platform is ignored. Ids that are not
// a Discord id (an Oculus or Steam account id from a client that predates this) fall back to the
// device table: the caller's PlatformCode first, then all known platforms.
func (p *EvrPipeline) resolveEvrIDToUserID(ctx context.Context, platformCode evr.PlatformCode, accountID uint64) (uuid.UUID, error) {
	if userID, err := GetUserIDByDiscordID(ctx, p.db, strconv.FormatUint(accountID, 10)); err == nil {
		if uid := uuid.FromStringOrNil(userID); uid != uuid.Nil {
			return uid, nil
		}
	}

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
func (p *EvrPipeline) sendEVRMessageByUserID(_ context.Context, logger *zap.Logger, userID uuid.UUID, messages ...evr.Message) error {
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
			FriendID: p.sessionAccountID(ctx, session, params),
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
		FriendID: p.sessionAccountID(ctx, session, params),
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
			FriendID: p.sessionAccountID(ctx, session, params),
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
			FriendID: p.sessionAccountID(ctx, session, params),
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
			FriendID: p.sessionAccountID(ctx, session, params),
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
			FriendID: p.sessionAccountID(ctx, session, params),
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
			FriendID: p.sessionAccountID(ctx, session, params),
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

// friendStatusCode maps a friend's online state to the wire StatusCode consumed by
// pnsrad's CNSRADFriends::StatusNotifyCB (echovr.exe/pnsrad.dll, confirmed via ReVault
// 2026-09-13 — see AddFriend @ pnsrad.dll/libpnsrad.so, param_5 < 3 gate, 3-way bucket).
// The only UI consumer found (R15NETFRIENDSEXPRESSION, echovr-reconstruction
// scripts/e9b0db765f1eb096.cpp:611-619) exposes just "nonline"/"noffline" — no "nbusy"
// output exists there, so the busy slot (1) is deliberately left unused rather than
// guessed at; only the online/offline split, which we can source truthfully from
// Nakama's own presence tracking, is asserted here.
func friendStatusCode(online bool) uint8 {
	if online {
		return 0
	}
	return 2
}

// friendStatusNotification is one (FriendID, StatusCode) pair to send as an
// SNSFriendStatusNotify.
type friendStatusNotification struct {
	FriendID   uint64
	StatusCode uint8
}

// friendStatusNotifications selects the confirmed friends (State ==
// FriendStateFriends) out of friends, resolves each to its wire FriendID via
// resolveAccountID, and returns the notifications to send. Friends the
// resolver can't place (e.g. no matching user_device row) are silently
// skipped by the resolver returning ok=false — logging that is the caller's
// job, not this function's, so it stays pure and independent of *zap.Logger.
//
// Pending invitations (FriendInvitationSent/FriendInvitationReceived) and
// blocks (FriendStateBlocked) are deliberately excluded: SNSFriendStatusNotify
// is what populates the ROSTER (see CNSRADFriends::StatusNotifyCB, confirmed
// via ReVault — it calls AddFriend), and only confirmed friends belong there.
func friendStatusNotifications(friends []*api.Friend, resolveAccountID func(*api.Friend) (accountID uint64, ok bool)) []friendStatusNotification {
	var out []friendStatusNotification
	for _, f := range friends {
		if f == nil || f.State == nil || f.State.Value != FriendStateFriends || f.User == nil {
			continue
		}
		accountID, ok := resolveAccountID(f)
		if !ok {
			continue
		}
		out = append(out, friendStatusNotification{
			FriendID:   accountID,
			StatusCode: friendStatusCode(f.User.Online),
		})
	}
	return out
}

// friendNotifies resolves each confirmed friend's account id once (a database lookup each) and uses it
// for both the status notifies every client is sent and the presence targets only a nevr-runtime
// client is sent, so a stock client costs no lookups beyond the status notifies it always had.
func friendNotifies(friends []*api.Friend, resolveAccountID func(*api.Friend) (accountID uint64, ok bool)) ([]friendStatusNotification, []friendPresenceTarget) {
	var targets []friendPresenceTarget
	notifications := friendStatusNotifications(friends, func(f *api.Friend) (uint64, bool) {
		accountID, ok := resolveAccountID(f)
		if ok {
			targets = append(targets, friendPresenceTarget{userID: uuid.FromStringOrNil(f.User.Id), accountID: accountID, online: f.User.Online})
		}
		return accountID, ok
	})
	return notifications, targets
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

	if err := SendEVRMessages(session, false, &evr.SNSFriendListResponse{
		NOnline:  nOnline,
		NBusy:    nBusy,
		NOffline: nOffline,
		NSent:    nSent,
		NRecv:    nRecv,
	}); err != nil {
		return err
	}

	// SNSFriendListResponse only ever carried aggregate counts (evr/sns_friends.go's
	// documented 0x20-byte wire format has no per-friend fields) — nothing populated
	// the client's actual roster. Confirmed via ReVault: CNSRADFriends::StatusNotifyCB
	// (the ONLY code path that calls AddFriend, i.e. the only thing that inserts a
	// named entry into the client's friend table) is the listener for
	// SNSFriendStatusNotify, which was never sent from here. Emit one per confirmed
	// friend so the roster actually populates.
	notifications, targets := friendNotifies(friends, func(f *api.Friend) (uint64, bool) {
		friendUserID, err := uuid.FromString(f.User.Id)
		if err != nil {
			logger.Warn("Skipping friend status notify — bad user id", zap.String("user_id", f.User.Id), zap.Error(err))
			return 0, false
		}
		accountID, err := p.resolveUserIDToAccountID(ctx, friendUserID)
		if err != nil {
			logger.Warn("Skipping friend status notify — could not resolve account id",
				zap.String("user_id", f.User.Id), zap.Error(err))
			return 0, false
		}
		return accountID, true
	})
	for _, n := range notifications {
		if err := SendEVRMessages(session, false, &evr.SNSFriendStatusNotify{
			FriendID:   n.FriendID,
			StatusCode: n.StatusCode,
		}); err != nil {
			logger.Warn("Failed to send friend status notify", zap.Uint64("friend_id", n.FriendID), zap.Error(err))
		}
	}

	// Each friend's presence (party, joinable, status text) for a client that parses it.
	p.sendFriendPresence(ctx, logger, session, targets)

	return nil
}

// xpIDForDiscordAccount finds an EvrId the user with this Discord id is known by, so their stored
// profile can be loaded for a request that named them by Discord id.
func (p *EvrPipeline) xpIDForDiscordAccount(ctx context.Context, accountID uint64) (evr.EvrId, bool) {
	userID, err := GetUserIDByDiscordID(ctx, p.db, strconv.FormatUint(accountID, 10))
	if err != nil || userID == "" || userID == uuid.Nil.String() {
		return evr.EvrId{}, false
	}
	rows, err := p.db.QueryContext(ctx, "SELECT id FROM user_device WHERE user_id = $1", userID)
	if err != nil {
		return evr.EvrId{}, false
	}
	defer rows.Close()
	for rows.Next() {
		var deviceID string
		if err := rows.Scan(&deviceID); err != nil {
			return evr.EvrId{}, false
		}
		if xpID, err := evr.ParseEvrId(deviceID); err == nil && xpID != nil {
			return *xpID, true
		}
	}
	return evr.EvrId{}, false
}
