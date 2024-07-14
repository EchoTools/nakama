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
	HMDSerialOverrideUrlParam             = "hmdserial"
	DisplayNameOverrideUrlParam           = "displayname"
	UserPasswordUrlParam                  = "password"
	DiscordIdUrlParam                     = "discordid"
	EvrIdOverrideUrlParam                 = "evrid"
	BroadcasterEncryptionDisabledUrlParam = "disable_encryption"
	BroadcasterHMACDisabledUrlParam       = "disable_hmac"
	FeaturesURLParam                      = "features"
	RequiredFeaturesURLParam              = "required_features"

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

	// Send the messages
	if err := session.SendEvr(
		evr.NewLoginFailure(evrId, errMessage),
		evr.NewSTcpConnectionUnrequireEvent(),
	); err != nil {
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

	// Check for an HMD serial override
	hmdsn, ok := ctx.Value(ctxHMDSerialOverrideKey{}).(string)
	if !ok {
		hmdsn = payload.HmdSerialNumber
	}

	// Construct the device auth token from the login payload
	deviceId := NewDeviceAuth(payload.AppId, request.EvrId, hmdsn, session.clientIP)

	// Providing a discord ID and password avoids the need to link the device to the account.
	// Server Hosts use this method to authenticate.
	userPassword, _ := ctx.Value(ctxPasswordKey{}).(string)
	discordId, _ := ctx.Value(ctxDiscordIdKey{}).(string)

	// Authenticate the connection
	loginSettings, err := p.processLogin(ctx, logger, session, request.EvrId, deviceId, discordId, userPassword, payload)
	if err != nil {
		st := status.Convert(err)
		return msgFailedLoginFn(session, request.EvrId, errors.New(st.Message()))
	}

	// Let the client know that the login was successful.
	// Send the login success message and the login settings.
	return session.SendEvr(
		evr.NewLoginSuccess(session.id, request.EvrId),
		evr.NewSTcpConnectionUnrequireEvent(),
		evr.NewSNSLoginSettings(loginSettings),
	)
}

// processLogin handles the authentication of the login connection.
func (p *EvrPipeline) processLogin(ctx context.Context, logger *zap.Logger, session *sessionWS, evrId evr.EvrId, deviceId *DeviceAuth, discordId string, userPassword string, loginProfile evr.LoginProfile) (settings evr.EchoClientSettings, err error) {
	// Authenticate the account.
	account, err := p.authenticateAccount(ctx, logger, session, deviceId, discordId, userPassword, loginProfile)
	if err != nil {
		return settings, err
	}
	if account == nil {
		return settings, fmt.Errorf("account not found")
	}
	user := account.GetUser()
	userId := user.GetId()
	uid := uuid.FromStringOrNil(user.GetId())

	updateCtx, cancel := context.WithTimeout(ctx, time.Second*3)
	go func() {
		defer cancel()
		err = p.discordRegistry.UpdateAllGuildGroupsForUser(ctx, NewRuntimeGoLogger(logger), uid)
		if err != nil {
			logger.Warn("Failed to update guild groups", zap.Error(err))
		}
	}()

	// Wait for the context to be done, or the timeout
	<-updateCtx.Done()

	// Get the user's metadata
	var metadata AccountUserMetadata
	if err := json.Unmarshal([]byte(account.User.GetMetadata()), &metadata); err != nil {
		return settings, fmt.Errorf("failed to unmarshal account metadata: %w", err)
	}

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

	flags := 0
	if loginProfile.SystemInfo.HeadsetType == "No VR" {
		flags |= FlagNoVR
	}

	for name, flag := range groupFlagMap {
		if ok, err := checkGroupMembershipByName(ctx, p.runtimeModule, uid.String(), name, SystemGroupLangTag); err != nil {
			return settings, fmt.Errorf("failed to check group membership: %w", err)
		} else if ok {
			flags |= flag
		}
	}

	// Get the GroupID from the user's metadata
	groupID := metadata.GetActiveGroupID()
	// Validate that the user is in the group
	if groupID == uuid.Nil {
		// Get a list of the user's guild memberships and set to the largest one
		memberships, err := p.discordRegistry.GetGuildGroupMemberships(ctx, uid, nil)
		if err != nil {
			return settings, fmt.Errorf("failed to get guild groups: %w", err)
		}
		if len(memberships) == 0 {
			return settings, fmt.Errorf("user is not in any guild groups")
		}
		// Sort the groups by the edgecount
		sort.SliceStable(memberships, func(i, j int) bool {
			return memberships[i].GuildGroup.Size() > memberships[j].GuildGroup.Size()
		})
		groupID = memberships[0].GuildGroup.ID()
	}

	// Initialize the full session
	if err := session.LoginSession(userId, user.GetUsername(), evrId, deviceId, groupID, flags); err != nil {
		return settings, fmt.Errorf("failed to login: %w", err)
	}
	ctx = session.Context()

	go func() {
		p.loginSessionByEvrID.Store(evrId.Token(), session)
		// Create a goroutine to clear the session info when the login session is closed.
		<-session.Context().Done()
		p.loginSessionByEvrID.Delete(evrId.String())
	}()

	// Load the user's profile
	profile, err := p.profileRegistry.GetSessionProfile(ctx, session, loginProfile, evrId)
	if err != nil {
		session.logger.Error("failed to load game profiles", zap.Error(err))
		return evr.DefaultGameSettingsSettings, fmt.Errorf("failed to load game profiles")
	}

	// Set the display name once.
	displayName, err := SetDisplayNameByChannelBySession(ctx, p.runtimeModule, logger, p.discordRegistry, session, groupID.String())
	if err != nil {
		logger.Warn("Failed to set display name", zap.Error(err))
	}

	profile.SetChannel(evr.GUID(groupID))
	profile.UpdateDisplayName(displayName)
	p.profileRegistry.Store(session.userID, profile)

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
func (p *EvrPipeline) authenticateAccount(ctx context.Context, logger *zap.Logger, session *sessionWS, deviceId *DeviceAuth, discordId string, userPassword string, payload evr.LoginProfile) (*api.Account, error) {
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

	userId, _, _, err = AuthenticateDevice(ctx, session.logger, session.pipeline.db, deviceId.Token(), "", false)
	if err != nil && status.Code(err) == codes.NotFound {
		// Try to authenticate the device with a wildcard address.
		userId, _, _, err = AuthenticateDevice(ctx, session.logger, session.pipeline.db, deviceId.WildcardToken(), "", false)
	}
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
	linkTicket, err := p.linkTicket(session, logger, deviceId, &payload)
	if err != nil {
		return account, status.Error(codes.Internal, fmt.Errorf("error creating link ticket: %w", err).Error())
	}
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

	resource, err := p.buildChannelInfo(ctx, logger)
	if err != nil {
		logger.Warn("Error building channel info", zap.Error(err))
	}

	if resource == nil {
		resource = evr.NewChannelInfoResource()
	}

	// send the document to the client
	if err := session.SendEvr(
		evr.NewSNSChannelInfoResponse(resource),
		evr.NewSTcpConnectionUnrequireEvent(),
	); err != nil {
		return fmt.Errorf("failed to send ChannelInfoResponse: %w", err)
	}
	return nil
}

func (p *EvrPipeline) buildChannelInfo(ctx context.Context, logger *zap.Logger) (*evr.ChannelInfoResource, error) {
	resource := evr.NewChannelInfoResource()

	//  Get the current guild Group ID from the Context
	groupID, ok := ctx.Value(ctxGroupIDKey{}).(uuid.UUID)
	if !ok {
		return nil, fmt.Errorf("groupID not found in context")
	}
	md, err := p.discordRegistry.GetGuildGroupMetadata(ctx, groupID.String())
	if err != nil {
		return nil, fmt.Errorf("failed to get guild group metadata: %w", err)
	}
	groups, err := GetGroups(ctx, logger, p.db, []string{groupID.String()})
	if err != nil {
		return nil, fmt.Errorf("failed to get groups: %w", err)
	}
	if len(groups) == 0 {
		return nil, fmt.Errorf("group not found")
	}

	g := groups[0]

	resource.Groups = make([]evr.ChannelGroup, 4)
	for i := range resource.Groups {
		resource.Groups[i] = evr.ChannelGroup{
			ChannelUuid:  strings.ToUpper(groupID.String()),
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

	profile, found := p.profileRegistry.Load(session.userID, evrID)
	if !found {
		return session.SendEvr(evr.NewLoggedInUserProfileFailure(request.EvrId, 400, "failed to load game profiles"))
	}

	return session.SendEvr(evr.NewLoggedInUserProfileSuccess(evrID, profile.Client, profile.Server))
}

func (p *EvrPipeline) updateClientProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.UpdateClientProfile)
	// Ignore the EvrID in the request and use what was authenticated with
	evrId, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return fmt.Errorf("evrId not found in context")
	}
	// Set the EVR ID from the context
	request.ClientProfile.EvrID = evrId

	if _, err := p.profileRegistry.UpdateClientProfile(ctx, logger, session, request.ClientProfile); err != nil {
		code := 400
		if err := session.SendEvr(evr.NewUpdateProfileFailure(evrId, uint64(code), err.Error())); err != nil {
			return fmt.Errorf("send UpdateProfileFailure: %w", err)
		}
		return fmt.Errorf("UpdateProfile: %w", err)
	}

	// Send the profile update to the client
	if err := session.SendEvr(
		evr.NewSNSUpdateProfileSuccess(&evrId),
		evr.NewSTcpConnectionUnrequireEvent(),
	); err != nil {
		logger.Warn("Failed to send UpdateProfileSuccess", zap.Error(err))
	}

	return nil
}

func (p *EvrPipeline) remoteLogSetv3(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.RemoteLogSet)

	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		logger.Debug("evrId not found in context")

	}

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
			profile, found := p.profileRegistry.Load(session.userID, evrID)
			if !found {
				return status.Errorf(codes.Internal, "Failed to get player's profile")
			}

			p.profileRegistry.UpdateEquippedItem(&profile, category, name)

			err = p.profileRegistry.Store(session.userID, profile)
			if err != nil {
				return status.Errorf(codes.Internal, "Failed to store player's profile")
			}

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
	evrID, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return fmt.Errorf("evrId not found in context")
	}

	var document evr.Document
	var err error
	switch request.Type {
	case "eula":

		if flags, ok := ctx.Value(ctxFlagsKey{}).(int); ok && flags&FlagNoVR != 0 {
			// Get the version of the EULA from the profile
			profile, found := p.profileRegistry.Load(session.userID, evrID)
			if !found {
				return fmt.Errorf("failed to load game profiles")
			}
			document = evr.NewEULADocument(int(profile.Client.LegalConsents.EulaVersion), int(profile.Client.LegalConsents.GameAdminVersion), request.Language, "https://github.com/EchoTools", "Blank EULA for NoVR clients.")

		} else {

			document, err = p.generateEULA(ctx, logger, request.Language)
			if err != nil {
				return fmt.Errorf("failed to get eula document: %w", err)
			}
		}
	default:
		return fmt.Errorf("unknown document: %s,%s", request.Language, request.Type)
	}

	session.SendEvr(
		evr.NewDocumentSuccess(document),
		evr.NewSTcpConnectionUnrequireEvent(),
	)
	return nil
}

func (p *EvrPipeline) generateEULA(ctx context.Context, logger *zap.Logger, language string) (evr.EULADocument, error) {
	// Retrieve the contents from storage
	key := fmt.Sprintf("eula,%s", language)
	document := evr.DefaultEULADocument(language)
	ts, err := p.StorageLoadOrStore(ctx, logger, uuid.Nil, DocumentStorageCollection, key, &document)
	if err != nil {
		return document, fmt.Errorf("failed to load or store EULA: %w", err)
	}

	msg := document.Text
	maxLineCount := 7
	maxLineLength := 28
	// Split the message by newlines

	// trim the final newline
	msg = strings.TrimRight(msg, "\n")

	// Limit the EULA to 7 lines, and add '...' to the end of any line that is too long.
	lines := strings.Split(msg, "\n")
	if len(lines) > maxLineCount {
		logger.Warn("EULA too long", zap.Int("lineCount", len(lines)))
		lines = lines[:maxLineCount]
		lines = append(lines, "...")
	}

	// Cut lines at 18 characters
	for i, line := range lines {
		if len(line) > maxLineLength {
			logger.Warn("EULA line too long", zap.String("line", line), zap.Int("length", len(line)))
			lines[i] = line[:maxLineLength-3] + "..."
		}
	}
	msg = strings.Join(lines, "\n") + "\n"

	document.Version = ts.Unix()
	document.VersionGameAdmin = ts.Unix()

	document.Text = msg
	return document, nil
}

// StorageLoadOrDefault loads an object from storage or store the given object if it doesn't exist.
func (p *EvrPipeline) StorageLoadOrStore(ctx context.Context, logger *zap.Logger, userID uuid.UUID, collection, key string, dst any) (time.Time, error) {
	ts := time.Now().UTC()
	objs, err := StorageReadObjects(ctx, logger, p.db, uuid.Nil, []*api.ReadStorageObjectId{
		{
			Collection: collection,
			Key:        key,
			UserId:     userID.String(),
		},
	})
	if err != nil {
		return ts, fmt.Errorf("SNSDocumentRequest: failed to read objects: %w", err)
	}

	if len(objs.Objects) > 0 {

		// unmarshal the document
		if err := json.Unmarshal([]byte(objs.Objects[0].Value), dst); err != nil {
			return ts, fmt.Errorf("error unmarshalling document %s: %w", key, err)
		}

		ts = objs.Objects[0].UpdateTime.AsTime()

	} else {

		// If the document doesn't exist, store the object
		jsonBytes, err := json.Marshal(dst)
		if err != nil {
			return ts, fmt.Errorf("error marshalling document: %w", err)
		}

		// write the document to storage
		ops := StorageOpWrites{
			{
				OwnerID: userID.String(),
				Object: &api.WriteStorageObject{
					Collection:      collection,
					Key:             key,
					Value:           string(jsonBytes),
					PermissionRead:  &wrapperspb.Int32Value{Value: int32(0)},
					PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
				},
			},
		}
		if _, _, err = StorageWriteObjects(ctx, logger, p.db, p.metrics, p.storageIndex, false, ops); err != nil {
			return ts, fmt.Errorf("failed to write objects: %w", err)
		}
	}

	return ts, nil
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

	if err := otherSession.SendEvr(msg); err != nil {
		return fmt.Errorf("failed to send generic message: %w", err)
	}

	if err := session.SendEvr(msg); err != nil {
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
	request := in.(*evr.UserServerProfileUpdateRequest)
	// Always send the same response

	if session.userID == uuid.Nil {
		logger.Warn("UserServerProfileUpdateRequest: user not logged in")
		return nil
	}

	defer func() {
		if err := session.SendEvr(evr.NewUserServerProfileUpdateSuccess(request.EvrID)); err != nil {
			logger.Warn("Failed to send UserServerProfileUpdateSuccess", zap.Error(err))
		}
	}()

	// Get the target user's match
	matchID, ok := p.matchByEvrID.Load(request.EvrID.String())
	if !ok {
		logger.Warn("UserServerProfileUpdateRequest: user not in a match")
		return nil
	}
	logger = logger.With(zap.String("matchID", matchID))

	matchComponents := strings.Split(matchID, ".")

	// Check if the requester is a broadcaster in that match
	_, _, statejson, err := p.matchRegistry.GetState(ctx, uuid.FromStringOrNil(matchComponents[0]), matchComponents[1])
	if err != nil {
		logger.Warn("UserServerProfileUpdateRequest: failed to get match", zap.Error(err))
		return nil
	}

	// Check the label for the broadcaster
	state := &EvrMatchState{}
	if err := json.Unmarshal([]byte(statejson), state); err != nil {
		logger.Warn("UserServerProfileUpdateRequest: failed to unmarshal match state", zap.Error(err))
		return nil
	}

	if state.Broadcaster.OperatorID != session.userID.String() {
		logger.Warn("UserServerProfileUpdateRequest: user not broadcaster for match")
		return nil
	}

	// Get the user id for the target
	targetSession, found := p.loginSessionByEvrID.Load(request.EvrID.String())
	if !found {
		logger.Warn("UserServerProfileUpdateRequest: user not found", zap.String("evrId", request.EvrID.Token()))
		return nil
	}

	userID := targetSession.UserID()
	username := targetSession.Username()

	profile, found := p.profileRegistry.Load(userID, request.EvrID)
	if !found {
		logger.Warn("UserServerProfileUpdateRequest: failed to load game profiles")
		return nil
	}
	serverProfile := profile.GetServer()
	matchTypeMap := map[int64]string{
		-3791849610740453517: "echo_arena",
		4421472114608583194:  "echo_combat",
	}

	matchType, ok := matchTypeMap[request.Payload.MatchType]
	if !ok {
		logger.Warn("UserServerProfileUpdateRequest: unknown match type", zap.Int64("matchType", request.Payload.MatchType))
	}

	for groupName, stats := range request.Payload.Update.StatsGroups {

		for statName, stat := range stats {
			record, err := p.leaderboardRegistry.Submission(ctx, userID.String(), request.EvrID.String(), username, request.Payload.SessionID.String(), matchType, groupName, statName, stat.Operand, stat.Value)
			if err != nil {
				logger.Warn("Failed to submit leaderboard", zap.Error(err))
			}
			if record != nil {
				matchStat := evr.MatchStatistic{
					Operand: stat.Operand,
				}
				if stat.Count != nil {
					matchStat.Count = stat.Count
				}
				if stat.IsFloat64() {
					// Combine the record score and the subscore as the decimal value
					matchStat.Value = float64(record.Score) + float64(record.Subscore)/10000
				} else {
					matchStat.Value = record.Score
				}

				// Update the profile
				_, ok := serverProfile.Statistics[groupName]
				if !ok {
					serverProfile.Statistics[groupName] = make(map[string]evr.MatchStatistic)
				}
				serverProfile.Statistics[groupName][statName] = matchStat
			}
		}
	}
	profile.SetServer(serverProfile)
	// Store the profile
	if err := p.profileRegistry.Store(userID, profile); err != nil {
		logger.Warn("UserServerProfileUpdateRequest: failed to store game profiles", zap.Error(err))
	}
	p.profileRegistry.Save(userID)

	return nil
}

func updateStats(profile *GameProfileData, stats evr.StatsUpdate) {
	serverProfile := profile.GetServer()

	for groupName, stats := range stats.StatsGroups {

		for statName, stat := range stats {

			matchStat := evr.MatchStatistic{
				Operand: stat.Operand,
			}
			if stat.Count != nil {
				matchStat.Count = stat.Count
				matchStat.Value = stat.Value
			}

			_, ok := serverProfile.Statistics[groupName]
			if !ok {
				serverProfile.Statistics[groupName] = make(map[string]evr.MatchStatistic)
			}
			serverProfile.Statistics[groupName][statName] = matchStat
		}
	}

	profile.SetServer(serverProfile)
}

func (p *EvrPipeline) otherUserProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.OtherUserProfileRequest)

	// Lookup the the profile
	data, found := p.profileRegistry.GetServerProfileByEvrID(request.EvrId)
	if !found {
		return fmt.Errorf("failed to find profile for %s", request.EvrId.Token())
	}

	// Construct the response
	response := &evr.OtherUserProfileSuccess{
		EvrId:             request.EvrId,
		ServerProfileJSON: data,
	}

	// Send the profile to the client
	if err := session.SendEvr(response); err != nil {
		return fmt.Errorf("failed to send OtherUserProfileSuccess: %w", err)
	}
	return nil
}
