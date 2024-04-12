package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"

	"github.com/muesli/reflow/wordwrap"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	HMDSerialOverrideUrlParam   = "hmdserial"
	DisplayNameOverrideUrlParam = "displayname"
	UserPasswordUrlParam        = "password"
	DiscordIdUrlParam           = "discordid"
	EvrIdOverrideUrlParam       = "evrid"

	EvrIDStorageIndex            = "EvrIDs_Index"
	GameClientSettingsStorageKey = "clientSettings"
	GamePlayerSettingsStorageKey = "playerSettings"
	DocumentStorageCollection    = "GameDocuments"
	GameProfileStorageCollection = "GameProfiles"
	GameProfileStorageKey        = "gameProfile"
	RemoteLogStorageCollection   = "RemoteLogs"
)

// errWithEvrIdFn prefixes an error with the EchoVR Id.
func errWithEvrIdFn(evrId evr.EvrId, format string, a ...interface{}) error {
	return fmt.Errorf("%s: %w", evrId.Token(), fmt.Errorf(format, a...))
}

// msgFailedLoginFn sends a LoginFailure message to the client.
// The error message is word-wrapped to 60 characters, 4 lines long.
func msgFailedLoginFn(session *sessionWS, evrId evr.EvrId, err error) error {
	// Format the error message
	s := fmt.Sprintf("%s: %s", evrId.Token(), err.Error())

	// Replace ": " with ":\n" for better readability
	s = strings.Replace(s, ": ", ":\n", 2)

	// Word wrap the error message
	errMessage := wordwrap.String(s, 60)

	// Create a slice of messages
	messages := []evr.Message{
		evr.NewLoginFailure(evrId, errMessage),
		evr.NewSTcpConnectionUnrequireEvent(),
	}

	// Send the messages
	if err := session.SendEvr(messages); err != nil {
		// If there's an error, prefix it with the EchoVR Id
		return errWithEvrIdFn(evrId, "send LoginFailure failed: %w", err)
	}

	return nil
}

// TODO FIXME This could use some optimization, or at least some benchmarking.
// Since all of these messages for the login step happen predictably, it might be worth preloading the user's profile.

// loginRequest handles the login request from the client.
func (p *EvrPipeline) loginRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.LoginRequest)

	// Start a timer to add to the metrics
	timer := time.Now()
	defer func() { p.metrics.CustomTimer("login", nil, time.Since(timer)) }()

	// TODO At some point EVR-ID's should be assigned, not accepted.

	// Validate the user identifier
	if !request.EvrId.Valid() {
		return msgFailedLoginFn(session, request.EvrId, status.Error(codes.InvalidArgument, "invalid EVR ID"))
	}

	payload := request.LoginData

	// Construct the device auth token from the login payload
	deviceId := &DeviceId{
		AppId:           payload.AppId,
		EvrId:           request.EvrId,
		HmdSerialNumber: payload.HmdSerialNumber,
	}

	// Providing a discord ID and password avoids the need to link the device to the account.
	// Server Hosts use this method to authenticate.
	userPassword, _ := ctx.Value(ctxPasswordKey{}).(string)
	discordId, _ := ctx.Value(ctxDiscordIdKey{}).(string)

	// Authenticate the connection
	loginSettings, err := p.processLogin(ctx, session, request.EvrId, deviceId, discordId, userPassword, payload)
	if err != nil {
		st := status.Convert(err)
		return msgFailedLoginFn(session, request.EvrId, errors.New(st.Message()))
	}

	// Let the client know that the login was successful.
	// Send the login success message and the login settings.
	messages := []evr.Message{
		evr.NewLoginSuccess(session.id, request.EvrId),
		evr.NewSTcpConnectionUnrequireEvent(),
		evr.NewSNSLoginSettings(loginSettings),
	}

	return session.SendEvr(messages)
}

// processLogin handles the authentication of the login connection.
func (p *EvrPipeline) processLogin(ctx context.Context, session *sessionWS, evrId evr.EvrId, deviceId *DeviceId, discordId string, userPassword string, loginProfile evr.LoginProfile) (settings evr.EchoClientSettings, err error) {
	// Authenticate the account.
	account, err := p.authenticateAccount(ctx, session, deviceId, discordId, userPassword, loginProfile)
	if err != nil {
		return settings, err
	}

	user := account.GetUser()
	userId := user.GetId()

	// Check that this EVR-ID is only used by this userID
	otherLogins, err := p.checkEvrIDOwner(ctx, evrId)
	if err != nil {
		return settings, fmt.Errorf("failed to check EVR-ID owner: %w", err)
	}

	if len(otherLogins) > 0 {
		// Check if the user is the owner of the EVR-ID
		if otherLogins[0].UserID != uuid.FromStringOrNil(userId) {
			session.logger.Warn("EVR-ID is already in use", zap.String("evrId", evrId.Token()), zap.String("userId", userId))
		}
	}

	// If user ID is not empty, write out the login payload to storage.
	if userId != "" {
		if err := writeAuditObjects(ctx, session, userId, evrId.Token(), loginProfile); err != nil {
			session.logger.Warn("Failed to write audit objects", zap.Error(err))
		}
	}

	noVR := loginProfile.SystemInfo.HeadsetType == "No VR"

	// Initialize the full session
	if err := session.LoginSession(userId, user.GetUsername(), evrId, deviceId, noVR); err != nil {
		return settings, fmt.Errorf("failed to login: %w", err)
	}
	ctx = session.Context()

	go func() {
		p.loginSessionByEvrID.Store(evrId.Token(), session)
		// Create a goroutine to clear the session info when the login session is closed.
		<-session.Context().Done()
		session.evrPipeline.loginSessionByEvrID.Delete(evrId.String())
	}()

	// Load the user's profile
	profile, err := p.profileRegistry.GetSessionProfile(ctx, session, loginProfile)
	if err != nil || profile == nil {
		session.logger.Error("failed to load game profiles", zap.Error(err))
		return evr.DefaultGameSettingsSettings, fmt.Errorf("failed to load game profiles")
	}

	// TODO Add the settings to the user profile
	settings = evr.DefaultGameSettingsSettings
	return settings, nil

}

type EvrIDHistory struct {
	Created time.Time
	Updated time.Time
	UserID  uuid.UUID
}

func (p *EvrPipeline) checkEvrIDOwner(ctx context.Context, evrId evr.EvrId) ([]EvrIDHistory, error) {

	// Check the storage index for matching evrIDs
	objectIds, err := p.storageIndex.List(ctx, uuid.Nil, EvrIDStorageIndex, fmt.Sprintf("+value.server.xplatformid:%s", evrId.String()), 1)
	if err != nil {
		return nil, fmt.Errorf("failed to list evrIDs: %w", err)
	}

	history := make([]EvrIDHistory, len(objectIds.Objects))
	for i, obj := range objectIds.Objects {
		history[i] = EvrIDHistory{
			Updated: obj.GetUpdateTime().AsTime(),
			Created: obj.GetCreateTime().AsTime(),
			UserID:  uuid.FromStringOrNil(obj.UserId),
		}
	}

	// sort history by updated time descending
	sort.Slice(history, func(i, j int) bool {
		return history[i].Updated.After(history[j].Updated)
	})

	return history, nil
}
func (p *EvrPipeline) authenticateAccount(ctx context.Context, session *sessionWS, deviceId *DeviceId, discordId string, userPassword string, payload evr.LoginProfile) (*api.Account, error) {
	var err error
	var userId string
	var account *api.Account

	// Discord Authentication
	if discordId != "" {

		if userPassword == "" {
			return nil, status.Error(codes.InvalidArgument, "password required")
		}

		uid, err := p.discordRegistry.GetUserIdByDiscordId(ctx, discordId, false)
		if err == nil {
			userId = uid.String()
			// Authenticate the password.
			userId, _, _, err = AuthenticateEmail(ctx, session.logger, session.pipeline.db, userId+"@"+p.placeholderEmail, userPassword, "", false)
			if err == nil {
				// Complete. Return account.
				return GetAccount(ctx, session.logger, session.pipeline.db, session.statusRegistry, uuid.FromStringOrNil(userId))
			} else if status.Code(err) != codes.NotFound {
				// Possibly banned or other error.
				return account, err
			}
		}
		// TODO FIXME return early if the discordId is non-existant.
		// Account requires discord linking, clear the discordId.
		discordId = ""
	}

	// Device Authentication
	userId, _, _, err = AuthenticateDevice(ctx, session.logger, session.pipeline.db, deviceId.Token(), "", false)
	if err != nil && status.Code(err) != codes.NotFound {
		// Possibly banned or other error.
		return account, err
	} else if err == nil {

		// The account was found.
		account, err = GetAccount(ctx, session.logger, session.pipeline.db, session.statusRegistry, uuid.FromStringOrNil(userId))
		if err != nil {
			return account, status.Error(codes.Internal, fmt.Sprintf("failed to get account: %s", err))
		}

		if account.GetCustomId() == "" {

			// Account requires discord linking.
			userId = ""

		} else if account.GetEmail() != "" {
			// The account has a password, authenticate the password.
			_, _, _, err := AuthenticateEmail(ctx, session.logger, session.pipeline.db, account.Email, userPassword, "", false)
			return account, err

		} else if userPassword != "" {

			// The user provided a password and the account has no password.
			err = LinkEmail(ctx, session.logger, session.pipeline.db, uuid.FromStringOrNil(userId), account.User.Id+"@"+p.placeholderEmail, userPassword)
			if err != nil {
				return account, status.Error(codes.Internal, fmt.Errorf("error linking email: %w", err).Error())
			}

			return account, nil
		} else if status.Code(err) != codes.NotFound {
			// Possibly banned or other error.
			return account, err
		}
	}

	// Account requires discord linking.
	linkTicket, err := p.linkTicket(session, deviceId, &payload)
	if err != nil {
		return account, status.Error(codes.Internal, fmt.Errorf("error creating link ticket: %w", err).Error())
	}
	// Get the user's channels
	msg := fmt.Sprintf("\nEnter this code:\n  \n>>> %s <<<\nusing '/link-headset %s' in the Echo VR Lounge Discord.", linkTicket.Code, linkTicket.Code)
	return account, errors.New(msg)
}

func writeAuditObjects(ctx context.Context, session *sessionWS, userId string, evrIdToken string, payload evr.LoginProfile) error {
	// Write logging/auditing storage objects.
	loginPayloadJson, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("error marshalling login payload: %w", err)
	}
	data := string(loginPayloadJson)
	perm := &wrapperspb.Int32Value{Value: int32(0)}
	ops := StorageOpWrites{
		{
			OwnerID: userId,
			Object: &api.WriteStorageObject{
				Collection:      EvrLoginStorageCollection,
				Key:             evrIdToken,
				Value:           data,
				PermissionRead:  perm,
				PermissionWrite: perm,
				Version:         "",
			},
		},
		{
			OwnerID: userId,
			Object: &api.WriteStorageObject{
				Collection:      ClientAddrStorageCollection,
				Key:             session.clientIP,
				Value:           string(loginPayloadJson),
				PermissionRead:  perm,
				PermissionWrite: perm,
				Version:         "",
			},
		},
	}
	_, _, err = StorageWriteObjects(ctx, session.logger, session.pipeline.db, session.metrics, session.storageIndex, true, ops)
	if err != nil {
		return fmt.Errorf("failed to write objects: %w", err)
	}
	return nil
}

func (p *EvrPipeline) channelInfoRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	_ = in.(*evr.ChannelInfoRequest)

	resource, err := p.buildChannelInfo(ctx, logger, session)
	if err != nil {
		logger.Warn("Error building channel info", zap.Error(err))
	}

	if resource == nil {
		resource = evr.NewChannelInfoResource()
	}

	// send the document to the client
	if err := session.SendEvr([]evr.Message{
		evr.NewSNSChannelInfoResponse(resource),
		evr.NewSTcpConnectionUnrequireEvent(),
	}); err != nil {
		return fmt.Errorf("failed to send ChannelInfoResponse: %w", err)
	}
	return nil
}

func (p *EvrPipeline) buildChannelInfo(ctx context.Context, logger *zap.Logger, session *sessionWS) (*evr.ChannelInfoResource, error) {
	resource := evr.NewChannelInfoResource()

	// Get the user's groups
	groups, err := p.discordRegistry.GetGuildGroups(ctx, session.userID)
	if err != nil {
		logger.Warn("Failed to get guild groups", zap.Error(err))
	}

	if len(groups) == 0 {
		// TODO FIXME Handle a user that doesn't have access to any guild groups
		return nil, fmt.Errorf("user is not in any guild groups")
	}

	// TODO Allow players to set what is listed for their lobbies

	// Sort the groups by the edgecount
	sort.SliceStable(groups, func(i, j int) bool {
		return groups[i].EdgeCount > groups[j].EdgeCount
	})

	// Limit to 4 results
	if len(groups) > 4 {
		groups = groups[:4]
	}

	// Overwrite the existing channel info
	for i, g := range groups {
		// Get the group metadata
		md := &GroupMetadata{}
		if err := json.Unmarshal([]byte(g.Metadata), md); err != nil {
			return resource, fmt.Errorf("error unmarshalling group metadata: %w", err)
		}

		resource.Groups[i] = evr.ChannelGroup{
			ChannelUuid:  strings.ToUpper(g.GetId()),
			Name:         g.Name,
			Description:  g.Description,
			Rules:        g.Name + "\n" + md.RulesText,
			RulesVersion: 1,
			Link:         fmt.Sprintf("https://discord.gg/channel/%s", g.GetName()),
			Priority:     uint64(i),
			RAD:          true,
		}
	}

	return resource, nil
}

func (p *EvrPipeline) loggedInUserProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) (err error) {
	request := in.(*evr.LoggedInUserProfileRequest)
	// Start a timer to add to the metrics
	timer := time.Now()
	defer func() { p.metrics.CustomTimer("loggedInUserProfileRequest", nil, time.Since(timer)) }()

	// Ignore the request and use what was authenticated with
	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		logger.Error("evrId not found in context")
		// Get it from the request
		evrID = request.EvrId
	}

	profile := p.profileRegistry.GetProfile(session.userID)
	if profile == nil {
		return session.SendEvr([]evr.Message{
			evr.NewLoggedInUserProfileFailure(request.EvrId, 400, "failed to load game profiles"),
		})
	}
	gameProfiles := &evr.GameProfiles{
		Client: profile.Client,
		Server: profile.Server,
	}

	return session.SendEvr([]evr.Message{
		evr.NewLoggedInUserProfileSuccess(evrID, gameProfiles),
	})
}

func (p *EvrPipeline) updateProfile(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.UpdateProfile)
	// Ignore the request and use what was authenticated with
	evrId, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return fmt.Errorf("evrId not found in context")
	}
	// Set the EVR ID from the context
	request.ClientProfile.EchoUserIdToken = evrId.Token()

	if _, err := p.profileRegistry.UpdateSessionProfile(ctx, logger, session, request.ClientProfile); err != nil {
		code := 400
		if err := session.SendEvr([]evr.Message{
			evr.NewUpdateProfileFailure(evrId, uint64(code), err.Error()),
		}); err != nil {
			return fmt.Errorf("send UpdateProfileFailure: %w", err)
		}
		return fmt.Errorf("UpdateProfile: %w", err)
	}

	go func() {
		// Send the profile update to the client
		if err := session.SendEvr([]evr.Message{
			evr.NewSNSUpdateProfileSuccess(&evrId),
			evr.NewSTcpConnectionUnrequireEvent(),
		}); err != nil {
			logger.Warn("Failed to send UpdateProfileSuccess", zap.Error(err))
		}
	}()

	return nil
}

func (p *EvrPipeline) remoteLogSetv3(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.RemoteLogSet)
	for _, l := range request.Logs {
		// Unmarshal the top-level to check the message type.

		entry := map[string]interface{}{}
		logBytes := []byte(l)
		if err := json.Unmarshal(logBytes, &entry); err != nil {
			if logger.Core().Enabled(zap.DebugLevel) {
				logger.Debug("Non-JSON log entry", zap.String("entry", string(logBytes)))
			}
		}

		s, ok := entry["message"]
		if !ok {
			logger.Warn("RemoteLogSet: missing message property", zap.Any("entry", entry))
		}

		messagetype, ok := s.(string)
		if !ok {
			logger.Debug("RemoteLogSet: message property is not a string", zap.Any("entry", entry))
		}

		switch strings.ToLower(messagetype) {
		case "ghost_user":
			// This is a ghost user message.
			ghostUser := &evr.RemoteLogGhostUser{}
			if err := json.Unmarshal(logBytes, ghostUser); err != nil {
				logger.Error("Failed to unmarshal ghost user", zap.Error(err))
				continue
			}
			_ = ghostUser
		case "game_settings":
			gameSettings := &evr.RemoteLogGameSettings{}
			if err := json.Unmarshal(logBytes, gameSettings); err != nil {
				logger.Error("Failed to unmarshal game settings", zap.Error(err))
				continue
			}

			// Store the game settings (this isn't really used)
			ops := StorageOpWrites{
				{
					OwnerID: session.userID.String(),
					Object: &api.WriteStorageObject{
						Collection:      RemoteLogStorageCollection,
						Key:             GamePlayerSettingsStorageKey,
						Value:           string(logBytes),
						PermissionRead:  &wrapperspb.Int32Value{Value: int32(1)},
						PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
						Version:         "",
					},
				},
			}
			if _, _, err := StorageWriteObjects(ctx, logger, session.pipeline.db, session.metrics, session.storageIndex, true, ops); err != nil {
				logger.Error("Failed to write game settings", zap.Error(err))
				continue
			}

		case "session_started":
			// TODO let the match know the server loaded the session?
			sessionStarted := &evr.RemoteLogSessionStarted{}
			if err := json.Unmarshal(logBytes, sessionStarted); err != nil {
				logger.Error("Failed to unmarshal session started", zap.Error(err))
				continue
			}
		case "customization item preview":
			fallthrough
		case "customization item equip":
			fallthrough
		case "podium interaction":
			fallthrough
		case "interaction_event":
			// Avoid spamming the logs with interaction events.
			if !logger.Core().Enabled(zap.DebugLevel) {
				continue
			}
			event := &evr.RemoteLogInteractionEvent{}
			if err := json.Unmarshal(logBytes, &event); err != nil {
				logger.Error("Failed to unmarshal interaction event", zap.Error(err))
				continue
			}
		case "customization_metrics_payload":
			// Update the server profile with the equipped cosmetic item.
			c := &evr.RemoteLogCustomizationMetricsPayload{}
			if err := json.Unmarshal(logBytes, &c); err != nil {
				logger.Error("Failed to unmarshal customization metrics", zap.Error(err))
				continue
			}

			if c.EventType != "item_equipped" {
				continue
			}
			category, name, err := c.GetEquippedCustomization()
			if err != nil {
				logger.Error("Failed to get equipped customization", zap.Error(err))
				continue
			}
			if category == "" || name == "" {
				logger.Error("Equipped customization is empty")
			}

			profile := p.profileRegistry.GetProfile(session.userID)
			p.profileRegistry.UpdateEquippedItem(profile, category, name)
			p.profileRegistry.SetProfile(session.userID, profile)
		default:
			if logger.Core().Enabled(zap.DebugLevel) {
				// Write the remoteLog to storage.
				ops := StorageOpWrites{
					{
						OwnerID: session.userID.String(),
						Object: &api.WriteStorageObject{
							Collection:      RemoteLogStorageCollection,
							Key:             messagetype,
							Value:           l,
							PermissionRead:  &wrapperspb.Int32Value{Value: int32(1)},
							PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
							Version:         "",
						},
					},
				}
				_, _, err := StorageWriteObjects(ctx, logger, session.pipeline.db, session.metrics, session.storageIndex, true, ops)
				if err != nil {
					logger.Error("Failed to write remote log", zap.Error(err))
				}
			} else {
				//logger.Debug("Received unknown remote log", zap.Any("entry", entry))
			}
		}
	}
	return nil
}

func (p *EvrPipeline) documentRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.DocumentRequest)
	var document evr.Document

	key := request.Language + "," + request.Name

	userId := session.UserID()
	switch key {
	case "en,eula":
		document = &evr.EulaDocument{}
	default:
		return fmt.Errorf("unknown document: %s", key)
	}

	// If this is a NoVR user, then use hte original EULA with version 1.
	// Get the NoVR from the ctx

	noVr, ok := ctx.Value(ctxNoVRKey{}).(bool)
	if ok && noVr {
		document = evr.NewEulaDocument(1, 1, "")

		session.SendEvr([]evr.Message{
			evr.NewSNSDocumentSuccess(document),
			evr.NewSTcpConnectionUnrequireEvent(),
		})

	}

	// Then always return a default document with a version of 1.
	// This is to prevent headless clients from hanging waiting for the
	// button interaction to get past the EULA dialog.

	// retrieve the document from storage
	objs, err := StorageReadObjects(ctx, logger, session.pipeline.db, uuid.Nil, []*api.ReadStorageObjectId{
		{
			Collection: DocumentStorageCollection,
			Key:        key,
			UserId:     userId.String(),
		},
		{
			Collection: DocumentStorageCollection,
			Key:        key,
			UserId:     uuid.Nil.String(),
		},
	})
	if err != nil {
		return fmt.Errorf("SNSDocumentRequest: failed to read objects: %w", err)
	}

	if (len(objs.Objects)) == 0 {
		// if the document doesn't exist, try to get the default document
		switch key {
		case "en,eula":
			document = evr.NewEulaDocument(1, 1, "")

		}
		jsonBytes, err := json.Marshal(document)
		if err != nil {
			return fmt.Errorf("error marshalling document: %w", err)
		}
		// write the document to storage
		ops := StorageOpWrites{
			{
				OwnerID: uuid.Nil.String(),
				Object: &api.WriteStorageObject{
					Collection:      DocumentStorageCollection,
					Key:             key,
					Value:           string(jsonBytes),
					PermissionRead:  &wrapperspb.Int32Value{Value: int32(0)},
					PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
				},
			},
		}
		if _, _, err = StorageWriteObjects(ctx, session.logger, session.pipeline.db, session.metrics, session.storageIndex, false, ops); err != nil {
			return fmt.Errorf("failed to write objects: %w", err)
		}

		logger.Error("document not found", zap.String("collection", DocumentStorageCollection), zap.String("key", key))

	} else {
		// Select the one owned by the user over the system
		var data string

		if len(objs.Objects) > 1 {
			for _, obj := range objs.Objects {
				if obj.UserId == userId.String() {
					data = obj.Value
					break
				}
			}
		}
		if data == "" {
			data = objs.Objects[0].Value
		}
		// unmarshal the document
		if err := json.Unmarshal([]byte(data), &document); err != nil {
			return fmt.Errorf("error unmarshalling document %s: %w: %s", key, err, data)
		}
		// Set the version to 1

		// If the channel is nil, then check everything.

		// Get the players current suspensions
		if key == "en,eula" {
			eula, ok := document.(*evr.EulaDocument)
			if !ok {
				return fmt.Errorf("failed to cast document to EulaDocument")
			}
			eula.Version = 1

			// Get the players current channel
			profile := p.profileRegistry.GetProfile(session.userID)
			if profile == nil {
				return status.Errorf(codes.Internal, "Failed to get player's profile")
			}
			channel := profile.GetChannel()
			// FIXME make sure the use a valid channel so the user's channelInfo comes through.
			suspensions := make([]*SuspensionStatus, 0)
			if channel != uuid.Nil {
				// Check the suspension status for this channel (and if they are suspended, check the other guilds)
				suspensions, err = p.checkSuspensionStatus(ctx, logger, session.UserID().String(), channel)
				if err != nil {
					return fmt.Errorf("failed to check suspension status: %w", err)
				}

			} else {
				// The user is not in a channel, so check all of their guilds.
				groups, err := p.discordRegistry.GetGuildGroups(ctx, session.userID)
				if err != nil {
					return fmt.Errorf("error getting guild groups: %w", err)
				}
				for _, g := range groups {
					suspensions, err = p.checkSuspensionStatus(ctx, logger, session.UserID().String(), uuid.FromStringOrNil(g.GetId()))
					if err != nil {
						return fmt.Errorf("failed to check suspension status: %w", err)
					}
				}
			}

			if len(suspensions) > 0 {
				// Inject it into the document
				eula.Text = generateSuspensionNotice(suspensions)
				eula.Version = time.Now().Unix()
				document = eula
			}
		}
	}
	// send the document to the client
	session.SendEvr([]evr.Message{
		evr.NewSNSDocumentSuccess(document),
		evr.NewSTcpConnectionUnrequireEvent(),
	})
	return nil
}

func (p *EvrPipeline) genericMessage(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.GenericMessage)
	logger.Debug("Received generic message", zap.Any("message", request))

	// Find online user with EvrId of request.OtherEvrId
	otherSession, found := p.loginSessionByEvrID.Load(request.OtherEvrID.Token())
	if !found {
		return fmt.Errorf("failure to find user by EvrID: %s", request.OtherEvrID.Token())
	}

	msg := evr.NewGenericMessageNotify(request.MessageType, request.Session, request.RoomID, request.PartyData)

	if err := otherSession.SendEvr([]evr.Message{msg}); err != nil {
		return fmt.Errorf("failed to send generic message: %w", err)
	}

	if err := session.SendEvr([]evr.Message{msg}); err != nil {
		return fmt.Errorf("failed to send generic message success: %w", err)
	}

	return nil
}

func generateSuspensionNotice(statuses []*SuspensionStatus) string {
	msgs := []string{
		"Current Suspensions:",
	}
	for _, s := range statuses {
		// The user is suspended from this channel.
		// Get the message from the suspension
		msgs = append(msgs, s.GuildName)
	}
	// Ensure that every line is padded to 40 characters on the right.
	for i, m := range msgs {
		msgs[i] = fmt.Sprintf("%-40s", m)
	}
	msgs = append(msgs, "\n\nContact the Guild's moderators for more information.")
	return strings.Join(msgs, "\n")
}

func (p *EvrPipeline) userServerProfileUpdateRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	message := in.(*evr.UserServerProfileUpdateRequest)

	if session.userID == uuid.Nil {
		logger.Warn("UserServerProfileUpdateRequest: user not logged in")
	}

	logger.Debug("UserServerProfileUpdateRequest", zap.String("evrid", message.EvrId.Token()), zap.Any("update", message.UpdateInfo))

	// These messages are just ignored.
	// Check that this is the broadcaster for that match

	/*
		match, err := p.runtimeModule.MatchGet(ctx, matchId+"."+p.node)
		if err != nil {
			return fmt.Errorf("failed to get match: %w", err)
		}

		session, found := p.loginSessionByEvrID.Load(message.EvrId.Token())
		if !found {
			return fmt.Errorf("failed to find user by EvrID: %s", message.EvrId.Token())
		}
		matchId, found := p.matchByEvrId.Load(message.EvrId.Token())
		if !found {
			return fmt.Errorf("failed to find match by EvrID: %s", message.EvrId.Token())
		}
		// Get the label for match
		match, err := p.runtimeModule.MatchGet(ctx, matchId)
		if err != nil {
			return fmt.Errorf("failed to get match: %w", err)
		}
		// unmarshal the label
		var state EvrMatchState
		if err := json.Unmarshal([]byte(match.GetLabel().String()), &state); err != nil {
			return fmt.Errorf("failed to unmarshal match label: %w", err)
		}

		// check that the update request is coming from the broadcaster of the match that the user is in
		if state.Broadcaster.UserID != session.userID.String() {
			return fmt.Errorf("requestor is not the broadcaster of the match")
		}

		profile := p.profileRegistry.GetProfile(session.userID)
		if profile == nil {
			return fmt.Errorf("failed to load game profiles")
		}

		// TODO FIXME put this into the session registry.

		p.profileRegistry.SetProfile(session.userID, profile)
		if err := sendMessagesToStream(ctx, nk, in.GetSessionId(), svcLoginID.String(), evr.NewUserServerProfileUpdateSuccess(message.EvrId)); err != nil {
			return state, fmt.Errorf("failed to send message: %w", err)
		}
		return state, nil
	*/

	if err := session.SendEvr([]evr.Message{
		evr.NewUserServerProfileUpdateSuccess(message.EvrId),
	}); err != nil {
		return fmt.Errorf("failed to send UserServerProfileUpdateSuccess: %w", err)
	}
	return nil
}

func (p *EvrPipeline) otherUserProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	message := in.(*evr.OtherUserProfileRequest)

	// Get the other user's matchID
	matchID, found := p.matchByEvrId.Load(message.EvrId.Token())
	if !found {
		return fmt.Errorf("failed to find match by (other) EvrID: %s", message.EvrId.Token())
	}
	matchComponents := strings.SplitN(matchID, ".", 2)
	if len(matchComponents) != 2 {
		return fmt.Errorf("failed to split matchID: %s", matchID)
	}

	// Check if this user is in the match.
	// If this is a broadcaster, Check if this user is also in the match
	presences, err := p.runtimeModule.StreamUserList(StreamModeMatchAuthoritative, matchComponents[0], "", matchComponents[1], true, true)
	if err != nil {
		return fmt.Errorf("failed to get presence: %w", err)
	}

	found = false
	userID := session.UserID().String()
	for _, p := range presences {
		if p.GetUserId() == userID {
			// The user is in the match
			found = true
			break
		}
	}

	// Get the session ID of the target user
	otherSession, ok := p.loginSessionByEvrID.Load(message.EvrId.Token())
	if !ok {
		return fmt.Errorf("failed to find user by EvrID: %s", message.EvrId.Token())
	}

	// Get the user's profile
	profile := p.profileRegistry.GetProfile(otherSession.userID)
	if profile == nil {
		return fmt.Errorf("failed to load game profiles")
	}
	profile.Lock()
	defer profile.Unlock()

	// Send the profile to the client
	if err := session.SendEvr([]evr.Message{
		evr.NewOtherUserProfileSuccess(message.EvrId, profile.Server),
	}); err != nil {
		return fmt.Errorf("failed to send OtherUserProfileSuccess: %w", err)
	}
	return nil
}
