package service

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

func (p *EvrPipeline) genericMessage(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	request := in.(*evr.GenericMessage)
	logger.Debug("Received generic message", zap.Any("message", request))

	/*

		msg := evr.NewGenericMessageNotify(request.MessageType, request.Session, request.RoomID, request.PartyData)

		if err := otherSession.SendEvr(msg); err != nil {
			return fmt.Errorf("failed to send generic message: %w", err)
		}

		if err := session.SendEvr(msg); err != nil {
			return fmt.Errorf("failed to send generic message success: %w", err)
		}

	*/
	return nil
}

func mostRecentThursday() time.Time {
	now := time.Now()
	offset := (int(now.Weekday()) - int(time.Thursday) + 7) % 7
	return now.AddDate(0, 0, -offset).UTC()
}

// A profile update request is sent from the game server's login connection.
// It is sent 45 seconds before the sessionend is sent, right after the match ends.
func (p *EvrPipeline) userServerProfileUpdateRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	message := in.(*evr.UserServerProfileUpdateRequest)

	// Get the lobby for the user
	query := fmt.Sprintf("+value.broadcaster.user_id:%s +value.players.evr_id:%s", session.userID.String(), message.EvrID.String())
	label, err := p.nevr.LobbyGet(ctx, query)
	if err != nil {
		logger.Warn("Failed to get lobby for user server profile update", zap.Error(err), zap.String("query", query))
		return fmt.Errorf("failed to get lobby: %w", err)
	}

	if err := session.SendEVR(Envelope{
		ServiceType: ServiceTypeLogin,
		Messages: []evr.Message{
			evr.NewUserServerProfileUpdateSuccess(message.EvrID),
		},
	}); err != nil {
		logger.Warn("Failed to send UserServerProfileUpdateSuccess", zap.Error(err))
	}

	if label.Mode != evr.ModeCombatPublic {
		// Only public combat matches update the profile
		return nil
	}

	payload := &evr.UpdatePayload{}

	if err := json.Unmarshal(message.Payload, payload); err != nil {
		return fmt.Errorf("failed to unmarshal update payload: %w", err)
	}

	// Ignore anything but statistics updates.
	if payload.Update.Statistics == nil || uuid.UUID(payload.SessionID) != label.ID.UUID {
		// Not a statistics update, or the session ID doesn't match.
		return nil
	}
	// Process the profile update in the background
	go p.processUserServerProfileUpdate(ctx, logger, message.EvrID, label, payload.Update.Statistics)
	return nil
}

func (p *EvrPipeline) otherUserProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	var (
		request   = in.(*evr.OtherUserProfileRequest)
		data      json.RawMessage
		err       error
		tags      = map[string]string{"error": "nil"}
		startTime = time.Now()
	)

	defer func() {
		p.metrics.CustomCounter("profile_request_count", tags, 1)
		p.metrics.CustomTimer("profile_request_latency", tags, time.Since(startTime))
		if len(data) > 0 {
			p.metrics.CustomGauge("profile_size_bytes", nil, float64(len(data)))
		}
	}()

	if data, err = PlayerProfileLoad(ctx, server.NewRuntimeGoLogger(logger), p.nk, session.userID.String(), request.XPID); err != nil {
		tags["error"] = "failed_load_profile"
		logger.Warn("Failed to get player profile data", zap.String("evrId", request.XPID.String()))

		// Return a generic profile for the user
		profile := evr.NewServerProfile()
		profile.XPID = request.XPID
		profile.DisplayName = request.XPID.String()
		data, err = json.Marshal(profile)
		if err != nil {
			tags["error"] = "failed_marshal_generic_profile"
			logger.Warn("Failed to marshal generic profile", zap.Error(err))
			return nil
		}
	}

	response := &evr.OtherUserProfileSuccess{
		XPID:        request.XPID,
		ProfileData: data,
	}

	return session.SendEVR(Envelope{
		ServiceType: ServiceTypeLogin,
		Messages: []evr.Message{
			response,
		},
		State: RequireStateUnrequired,
	})
}
