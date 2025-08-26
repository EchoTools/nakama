package service

import (
	"context"
	"errors"
	"fmt"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func authSocket(ctx context.Context, logger *zap.Logger, pipeline *EvrPipeline, vars map[string]string, in *evr.LoginRequest, clientIP string) (profile *EVRProfile, err error) {
	db := pipeline.db
	nk := pipeline.nk
	tags := map[string]string{}
	authDiscordID := vars["auth_discord_id"]
	authPassword := vars["auth_password"]
	userID := uuid.Nil
	// Authenticate the user if a Discord ID is provided.
	if authDiscordID != "" {
		if _, identifiedUsername, _, err := server.AuthenticateCustom(ctx, logger, db, authDiscordID, "", false); err != nil {
			logger.Warn("Failed to authenticate user by Discord ID", zap.Error(err), zap.String("discord_id", authDiscordID))
		} else if userIDStr, err := server.AuthenticateUsername(ctx, logger, db, identifiedUsername, authPassword); err != nil {
			logger.Warn("Failed to authenticate user by Discord ID", zap.Error(err), zap.String("discord_id", authDiscordID))
		} else {
			// Once the user has been authenticated with a deviceID, their password will be set.
			userID = uuid.FromStringOrNil(userIDStr)
		}
	}

	// Get the account for this device
	profile, err = AccountGetDeviceID(ctx, db, nk, in.XPID.String())
	switch status.Code(err) {
	// The device is not linked to an account.
	case codes.NotFound:

		tags["device_linked"] = "false"

		// the session is authenticated. Automatically link the device.
		if !userID.IsNil() {
			if err := nk.LinkDevice(ctx, userID.String(), in.XPID.String()); err != nil {
				tags["error"] = "failed_link_device"
				nk.MetricsCounterAdd("login_failure", tags, 1)
				return nil, fmt.Errorf("failed to link device: %w", err)
			}

			// The session is not authenticated. Create a link ticket.
		} else {

			if linkTicket, err := IssueLinkTicket(ctx, nk, in.XPID, clientIP, &in.Payload); err != nil {

				tags["error"] = "link_ticket_error"
				nk.MetricsCounterAdd("login_failure", tags, 1)

				return nil, fmt.Errorf("error creating link ticket: %s", err)
			} else {
				return nil, &DeviceNotLinkedError{
					Code:         linkTicket.Code,
					Instructions: ServiceSettings().LinkInstructions,
				}
			}
		}

	// The device is linked to an account.
	case codes.OK:

		tags["device_linked"] = "true"

		var (
			requiresPasswordAuth    = profile.HasPasswordSet()
			authenticatedViaSession = !userID.IsNil()
			isAccountMismatched     = profile.ID() != userID.String()
			passwordProvided        = authPassword != ""
		)

		if requiresPasswordAuth {

			if !authenticatedViaSession {
				// The session authentication was not successful.
				tags["error"] = "session_auth_failed"
				nk.MetricsCounterAdd("login_failure", tags, 1)
				return nil, errors.New("session authentication failed: account requires password authentication")
			}
		} else {

			if authenticatedViaSession && isAccountMismatched {
				// The device is linked to a different account.
				tags["error"] = "device_link_mismatch"
				logger.Error("Device is linked to a different account.", zap.String("device_user_id", profile.ID()), zap.String("session_user_id", userID.String()))
				nk.MetricsCounterAdd("login_failure", tags, 1)
				return nil, fmt.Errorf("device linked to a different account. (%s)", profile.Username())
			}

			if passwordProvided {
				// This is the first time setting the password.
				if err := server.LinkEmail(ctx, logger, db, userID, profile.ID()+"@"+pipeline.placeholderEmail, authPassword); err != nil {
					tags["error"] = "failed_link_email"
					nk.MetricsCounterAdd("login_failure", tags, 1)
					return nil, fmt.Errorf("failed to link email: %w", err)
				}
			}

		}
	}

	// The session is authenticated.
	return profile, nil
}
