package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	XPIStorageIndex              = "EvrIDs_Index"
	GameClientSettingsStorageKey = "clientSettings"
	GamePlayerSettingsStorageKey = "playerSettings"
	DocumentStorageCollection    = "GameDocuments"
	GameProfileStorageCollection = "GameProfiles"
	GameProfileStorageKey        = "gameProfile"
	RemoteLogStorageCollection   = "RemoteLogs"
	RemoteLogStorageJournalKey   = "journal"
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
	if err := session.SendEvrUnrequire(evr.NewLoginFailure(evrId, errMessage)); err != nil {
		// If there's an error, prefix it with the EchoVR Id
		return errWithEvrIdFn(evrId, "send LoginFailure failed: %w", err)
	}

	return nil
}

// loginRequest handles the login request from the client.
func (p *EvrPipeline) loginRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.LoginRequest)

	// Start a timer to add to the metrics
	timer := time.Now()
	defer func() { p.metrics.CustomTimer("login_duration", nil, time.Since(timer)) }()

	// Check for an HMD serial override

	// Authenticate the connection
	gameSettings, err := p.processLogin(ctx, logger, session, request)
	if err != nil {
		st := status.Convert(err)
		return msgFailedLoginFn(session, request.XPID, errors.New(st.Message()))
	}

	// Let the client know that the login was successful.
	// Send the login success message and the login settings.
	return session.SendEvr(
		evr.NewLoginSuccess(session.id, request.XPID),
		unrequireMessage,
		gameSettings,
		unrequireMessage,
	)
}

// processLogin handles the authentication of the login connection.
func (p *EvrPipeline) processLogin(ctx context.Context, logger *zap.Logger, session *sessionWS, request *evr.LoginRequest) (settings *evr.GameSettings, err error) {

	// Validate the XPID
	if request.XPID.IsNil() || !request.XPID.IsValid() {
		return settings, fmt.Errorf("invalid xpid: %s", request.XPID.String())
	}

	params, ok := LoadParams(ctx)
	if !ok {
		return nil, errors.New("session parameters not found")
	}

	params.xpID = request.GetEvrID()
	params.loginSession = session
	params.isVR = request.LoginData.SystemInfo.HeadsetType != "No VR"
	params.lobbyVersion = request.LoginData.LobbyVersion
	params.publisherLock = request.LoginData.PublisherLock

	questTypes := []string{
		"Quest",
		"Quest 2",
		"Quest 3",
		"Quest Pro",
	}

	params.isPCVR = true

	for _, t := range questTypes {
		if strings.Contains(strings.ToLower(request.LoginData.SystemInfo.HeadsetType), strings.ToLower(t)) {
			params.isPCVR = false
			break
		}
	}

	var account *api.Account
	var authErr error

	// device authentication
	if session.userID.IsNil() {
		// Construct the device auth token from the login payload

		account, authErr = p.authenticateHeadset(ctx, logger, session, params.xpID, session.clientIP, params.authPassword, request.LoginData)

	} else {

		// Validate the device ID from the authenticated session
		account, authErr = p.validateDeviceID(ctx, logger, session, params.xpID)
	}

	if authErr != nil && account == nil {
		// Headset is not linked to an account.
		return settings, authErr
	}

	if account == nil {
		return settings, fmt.Errorf("account is nil: %w", authErr)
	}

	// add the login attempt to the login history
	loginHistory, err := LoginHistoryLoad(ctx, p.runtimeModule, account.User.Id)
	if err != nil {
		return settings, fmt.Errorf("failed to load login history: %w", err)
	}
	defer loginHistory.Store(ctx, p.runtimeModule)

	params.loginHistory = loginHistory

	loginHistory.UpdateAlternates(ctx, p.runtimeModule)
	loginHistory.Update(params.xpID, session.clientIP, &request.LoginData)

	if session.UserID().IsNil() {
		// Validate the clientIP
		if ok := loginHistory.IsAuthorizedIP(session.ClientIP()); !ok {

			if p.appBot != nil && p.appBot.dg != nil && p.appBot.dg.State != nil && p.appBot.dg.State.User != nil {
				ipqs := p.ipqsClient.IPDetailsWithTimeout(session.ClientIP())
				if err := p.appBot.SendIPApprovalRequest(ctx, account.User.Id, session.ClientIP(), ipqs); err != nil {
					return settings, fmt.Errorf("failed to send IP approval request: %w", err)
				}
				return settings, fmt.Errorf("New location detected.\nPlease check your Discord DMs to accept the \nverification request from @%s.", p.appBot.dg.State.User.Username)
			} else {
				return settings, errors.New("New IP address detected. Please check your Discord DMs for a verification request.")
			}

		}
	} else {
		// Auto-authorize the IP (due to username/password authentication)
		loginHistory.AuthorizeIP(session.clientIP)
	}

	// Common error handling
	if account.GetDisableTime() != nil {
		// The account is banned. log the attempt.
		logger.Info("Attempted login to banned account.",
			zap.String("xpid", params.xpID.Token()),
			zap.String("client_ip", session.clientIP),
			zap.String("uid", account.User.Id),
			zap.Any("login_payload", request.LoginData))

		return settings, fmt.Errorf("User account banned.")
	}

	if authErr != nil {
		return settings, authErr
	}

	params.discordID = account.CustomId

	params.accountMetadata = &AccountMetadata{}
	if err := json.Unmarshal([]byte(account.User.Metadata), params.accountMetadata); err != nil {
		return settings, fmt.Errorf("failed to unmarshal metadata: %w", err)
	}
	params.accountMetadata.account = account
	// Get the GroupID from the user's metadata

	groups, err := UserGuildGroupsList(ctx, p.runtimeModule, account.User.Id)
	if err != nil {
		return settings, fmt.Errorf("failed to get guild groups: %w", err)
	}
	params.guildGroups = groups

	for _, g := range groups {
		p.discordCache.QueueSyncMember(g.GuildID, params.discordID)
	}

	memberships, err := GetGuildGroupMemberships(ctx, p.runtimeModule, account.User.Id)
	if err != nil {
		return settings, fmt.Errorf("failed to get guild groups: %w", err)
	}
	if len(memberships) == 0 {
		return settings, fmt.Errorf("user is not in any guild groups")
	}

	params.memberships = memberships

	if g := params.accountMetadata.ActiveGroupID; g != uuid.Nil.String() {
		if _, ok := memberships[g]; !ok {
			// User is not in the active group
			logger.Warn("User is not in the active group", zap.String("uid", account.User.Id), zap.String("gid", g))
			params.accountMetadata.ActiveGroupID = uuid.Nil.String()
		}
	}

	if params.accountMetadata.ActiveGroupID == uuid.Nil.String() {
		groupIDs := make([]string, 0, len(memberships))
		for id := range memberships {
			groupIDs = append(groupIDs, id)
		}
		// Sort the groups by the edgecount
		slices.SortStableFunc(groupIDs, func(a, b string) int {
			return int(groups[a].Group.EdgeCount - groups[b].Group.EdgeCount)
		})
		slices.Reverse(groupIDs)

		params.accountMetadata.SetActiveGroupID(uuid.FromStringOrNil(groupIDs[0]))
		logger.Debug("Set active group", zap.String("uid", account.User.Id), zap.String("gid", params.accountMetadata.ActiveGroupID))
	}

	if ismember, err := CheckSystemGroupMembership(ctx, p.db, session.userID.String(), GroupGlobalDevelopers); err != nil {
		return settings, fmt.Errorf("failed to check system group membership: %w", err)
	} else if ismember {
		params.isGlobalDeveloper = true
	}

	if params.userDisplayNameOverride != "" {
		// This will be picked up by GetActiveGroupDisplayName and other functions
		params.accountMetadata.DisplayNameOverride = params.userDisplayNameOverride
	}

	// Update the user's metadata
	if err := p.runtimeModule.AccountUpdateId(ctx, account.User.Id, "", params.accountMetadata.MarshalMap(), "", "", "", "", ""); err != nil {
		return settings, fmt.Errorf("failed to update user metadata: %w", err)
	}
	params.account = account

	StoreParams(ctx, &params)
	// Initialize the full session
	if err := session.SetIdentity(uuid.FromStringOrNil(account.User.Id), params.xpID, account.User.Username); err != nil {
		return settings, fmt.Errorf("failed to login: %w", err)
	}
	ctx = session.Context()

	/*
		session.SendEvr(&evr.EarlyQuitConfig{
			SteadyPlayerLevel: 1,
			NumSteadyMatches:  1,
			PenaltyLevel:      1,
			PenaltyTs:         time.Now().Add(12 * time.Hour).Unix(),
			NumEarlyQuits:     1,
		})
	*/
	// TODO Add the settings to the user profile
	settings = evr.NewDefaultGameSettings()
	return settings, nil
}

type EvrIDHistory struct {
	Created time.Time
	Updated time.Time
	UserID  uuid.UUID
}

func (p *EvrPipeline) authenticateHeadset(ctx context.Context, logger *zap.Logger, session *sessionWS, xpid evr.EvrId, clientIP string, userPassword string, payload evr.LoginProfile) (*api.Account, error) {
	var err error
	var userId string
	var account *api.Account

	// Lookup the account by the device ID
	account, err = AccountGetDeviceID(ctx, p.db, p.runtimeModule, xpid.String())

	switch status.Code(err) {
	case codes.OK:
		// The account was found.
		userId = account.User.Id
	case codes.NotFound:
		// Continue with linking
	default:
		return account, fmt.Errorf("failed to get account: %w", err)
	}

	if account.GetEmail() != "" {

		// The account has a password, authenticate the password.
		// This is just a failsafe, since this authentication should be handled at the session level.
		_, _, _, err := AuthenticateEmail(ctx, logger, session.pipeline.db, account.Email, userPassword, "", false)
		return account, err

	} else if userPassword != "" {
		// This is the first time the user has authenticate via device id, while providing a password.
		// Set the password on the account.

		err = LinkEmail(ctx, logger, session.pipeline.db, uuid.FromStringOrNil(userId), account.User.Id+"@"+p.placeholderEmail, userPassword)
		if err != nil {
			return account, status.Errorf(codes.Internal, "error linking email: %s", err)
		}

		return account, nil
	}

	// Device requires discord linking.
	linkTicket, err := p.linkTicket(ctx, logger, xpid, clientIP, &payload)
	if err != nil {
		return account, status.Errorf(codes.Internal, "error creating link ticket: %s", err)
	}
	msg := fmt.Sprintf("\nEnter this code:\n  \n>>> %s <<<\nusing '/link-headset %s' in the Echo VR Lounge Discord.", linkTicket.Code, linkTicket.Code)
	return account, errors.New(msg)
}

func (p *EvrPipeline) validateDeviceID(ctx context.Context, logger *zap.Logger, session *sessionWS, xpid evr.EvrId) (*api.Account, error) {
	// Validate the session login

	// The account was found.
	account, err := GetAccount(ctx, logger, session.pipeline.db, session.statusRegistry, session.userID)
	if err != nil {
		return account, status.Error(codes.Internal, fmt.Sprintf("failed to get account: %s", err))
	}

	account, err = AccountGetDeviceID(ctx, session.pipeline.db, p.runtimeModule, xpid.Token())

	switch status.Code(err) {
	case codes.OK:
		if account.User.Id != session.userID.String() {
			logger.Error("Device is already linked to another account", zap.String("device_user_id", account.User.Id), zap.String("session_user_id", session.userID.String()))
			return account, fmt.Errorf("device is already linked to another account (%s)", account.User.Username)
		}
	case codes.NotFound:
		// The account was not found.
		// try to link the device to the account
		if err := p.runtimeModule.LinkDevice(ctx, session.UserID().String(), xpid.Token()); err != nil {
			return account, fmt.Errorf("failed to link device: %w", err)
		}
	default:
		return account, err
	}

	return account, nil
}

func (p *EvrPipeline) channelInfoRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	_ = in.(*evr.ChannelInfoRequest)

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	groupID := uuid.Nil
	if params.accountMetadata == nil {
		logger.Error("AccountMetadata not found")
	} else {
		groupID = params.accountMetadata.GetActiveGroupID()
	}

	key := fmt.Sprintf("channelInfo,%s", groupID.String())

	// Check the cache first
	message := p.MessageCacheLoad(key)

	if message == nil {

		params, ok := LoadParams(ctx)
		if !ok {
			return errors.New("session parameters not found")
		}

		guildGroups := params.guildGroups
		if guildGroups == nil {
			return errors.New("guild groups not found")
		}
		g, ok := guildGroups[groupID.String()]

		resource := evr.NewChannelInfoResource()

		resource.Groups = make([]evr.ChannelGroup, 4)
		for i := range resource.Groups {
			resource.Groups[i] = evr.ChannelGroup{
				ChannelUuid:  strings.ToUpper(g.ID().String()),
				Name:         g.Name(),
				Description:  g.Description(),
				Rules:        g.Description() + "\n" + g.RulesText,
				RulesVersion: 1,
				Link:         fmt.Sprintf("https://discord.gg/channel/%s", g.GuildID),
				Priority:     uint64(i),
				RAD:          true,
			}
		}

		if resource == nil {
			resource = evr.NewChannelInfoResource()
		}

		message = evr.NewSNSChannelInfoResponse(resource)
		p.MessageCacheStore(key, message, time.Minute*3)
	}

	// send the document to the client
	return session.SendEvrUnrequire(message)
}

func (p *EvrPipeline) loggedInUserProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) (err error) {
	request := in.(*evr.LoggedInUserProfileRequest)
	// Start a timer to add to the metrics
	timer := time.Now()
	defer func() { p.metrics.CustomTimer("loggedInUserProfileRequest", nil, time.Since(timer)) }()

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	serverProfile, err := NewUserServerProfile(ctx, p.db, params.account, params.xpID)
	if err != nil {
		return fmt.Errorf("failed to get server profile: %w", err)
	}

	params.profile.Store(serverProfile)

	go func() {
		// Purge the profile after the session is done
		<-session.Context().Done()
		<-time.After(time.Minute * 1)
		p.profileRegistry.PurgeProfile(params.xpID)
	}()

	clientProfile, err := NewClientProfile(ctx, p.db, p.runtimeModule, &params)
	gg, ok := params.guildGroups[params.accountMetadata.GetActiveGroupID().String()]
	if !ok {
		return fmt.Errorf("guild group not found: %s", params.accountMetadata.GetActiveGroupID().String())
	}

	// Check if the user is required to go through community values
	if !gg.hasCompletedCommunityValues(session.userID.String()) {
		clientProfile.Social.CommunityValuesVersion = 0
	}

	return session.SendEvr(evr.NewLoggedInUserProfileSuccess(request.EvrID, clientProfile, serverProfile))
}

func (p *EvrPipeline) updateClientProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.UpdateClientProfile)

	if !request.EvrId.Equals(request.ClientProfile.EvrID) {
		return fmt.Errorf("xpi mismatch: %s != %s", request.ClientProfile.EvrID.Token(), request.EvrId.Token())
	}

	go func() {
		if err := p.handleClientProfileUpdate(ctx, logger, session, request.EvrId, request.ClientProfile); err != nil {
			if err := session.SendEvr(evr.NewUpdateProfileFailure(request.EvrId, uint64(400), err.Error())); err != nil {
				logger.Error("Failed to send UpdateProfileFailure", zap.Error(err))
			}
		}
	}()

	// Send the profile update to the client
	return session.SendEvrUnrequire(evr.NewSNSUpdateProfileSuccess(&request.EvrId))
}

func (p *EvrPipeline) handleClientProfileUpdate(ctx context.Context, logger *zap.Logger, session *sessionWS, evrID evr.EvrId, update evr.ClientProfile) error {
	// Set the EVR ID from the context
	update.EvrID = evrID

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	// Get the user's metadata
	guildGroups := params.guildGroups

	userID := session.userID.String()
	for groupID := range guildGroups {

		gg, found := guildGroups[groupID]
		if !found {
			return fmt.Errorf("guild group not found: %s", params.accountMetadata.GetActiveGroupID().String())
		}

		if !gg.hasCompletedCommunityValues(userID) {

			hasCompleted := update.Social.CommunityValuesVersion != 0

			if hasCompleted {

				gg.CommunityValuesUserIDsRemove(session.userID.String())

				md, err := gg.MarshalToMap()
				if err != nil {
					return fmt.Errorf("failed to marshal guild group: %w", err)
				}

				if err := p.runtimeModule.GroupUpdate(ctx, gg.ID().String(), SystemUserID, "", "", "", "", "", false, md, 1000000); err != nil {
					return fmt.Errorf("error updating group: %w", err)
				}

				p.appBot.LogMessageToChannel(fmt.Sprintf("User <@%s> has accepted the community values.", params.discordID), gg.AuditChannelID)
			}
		}
	}
	newMetadata := *params.accountMetadata

	newMetadata.TeamName = update.TeamName

	newMetadata.CombatLoadout = CombatLoadout{
		CombatWeapon:       update.CombatWeapon,
		CombatGrenade:      update.CombatGrenade,
		CombatDominantHand: update.CombatDominantHand,
		CombatAbility:      update.CombatAbility,
	}

	newMetadata.GhostedPlayers = update.GhostedPlayers.Players
	newMetadata.MutedPlayers = update.MutedPlayers.Players

	params.accountMetadata = &newMetadata

	StoreParams(ctx, &params)

	if err := p.runtimeModule.AccountUpdateId(ctx, userID, "", newMetadata.MarshalMap(), "", "", "", "", ""); err != nil {
		return fmt.Errorf("failed to update user metadata: %w", err)
	}
	return nil
}

func (p *EvrPipeline) remoteLogSetv3(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.RemoteLogSet)

	go func() {
		if err := p.processRemoteLogSets(ctx, logger, session, request.EvrID, request); err != nil {
			logger.Error("Failed to process remote log set", zap.Error(err))
		}
	}()

	return nil
}

func (p *EvrPipeline) documentRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.DocumentRequest)

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	var document evr.Document
	var err error
	switch request.Type {
	case "eula":

		if !params.isVR {

			eulaVersion := 1
			gaVersion := 1

			document = evr.NewEULADocument(int(eulaVersion), int(gaVersion), request.Language, "https://github.com/EchoTools", "Blank EULA for NoVR clients.")

			return session.SendEvrUnrequire(evr.NewDocumentSuccess(document))

		}

		key := "eula:vr:" + request.Language
		message := p.MessageCacheLoad(key)

		if message == nil {
			document, err = p.generateEULA(ctx, logger, request.Language)
			if err != nil {
				return fmt.Errorf("failed to get eula document: %w", err)
			}
			message = evr.NewDocumentSuccess(document)
			p.MessageCacheStore(key, message, time.Minute*1)
		}

		return session.SendEvrUnrequire(message)

	default:
		return fmt.Errorf("unknown document: %s,%s", request.Language, request.Type)
	}
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

func mostRecentThursday() time.Time {
	now := time.Now()
	offset := (int(now.Weekday()) - int(time.Thursday) + 7) % 7
	return now.AddDate(0, 0, -offset).UTC()
}

// A profile udpate request is sent from the game server's login connection.
// It is sent 45 seconds before the sessionend is sent, right after the match ends.
func (p *EvrPipeline) userServerProfileUpdateRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.UserServerProfileUpdateRequest)

	if err := session.SendEvr(evr.NewUserServerProfileUpdateSuccess(request.EvrID)); err != nil {
		logger.Warn("Failed to send UserServerProfileUpdateSuccess", zap.Error(err))
	}

	// Validate the player was in the session
	matchID, err := NewMatchID(uuid.UUID(request.Payload.SessionID), p.node)
	if err != nil {
		return fmt.Errorf("failed to generate matchID: %w", err)
	}

	label, err := MatchLabelByID(ctx, p.runtimeModule, matchID)
	if err != nil {
		return fmt.Errorf("failed to get match label: %w", err)
	}

	playerInfo := label.GetPlayerByEvrID(request.EvrID)

	if playerInfo == nil {
		return fmt.Errorf("failed to find player in match")
	}

	// Set the player's session to not be an early quitter
	playerSession := p.sessionRegistry.Get(uuid.FromStringOrNil(playerInfo.SessionID))
	if playerSession != nil {
		params, ok := LoadParams(playerSession.Context())
		if !ok {
			return errors.New("session parameters not found")
		}
		params.isEarlyQuitter.Store(false)
	}

	if playerInfo.Team != BlueTeam && playerInfo.Team != OrangeTeam || playerInfo.Team == SocialLobbyParticipant {
		return nil
	}

	// Determine winner
	blueWins := false
	switch label.Mode {
	case evr.ModeArenaPublic:

		// Check which team won
		if stats, ok := request.Payload.Update.StatsGroups["arena"]; !ok {
			return fmt.Errorf("stats group doesn't match mode")

		} else {

			if s, ok := stats["ArenaWins"]; ok && s.Value > 0 && playerInfo.Team == BlueTeam {
				blueWins = true
			}
		}

	case evr.ModeCombatPublic:

		// Check which team won
		if stats, ok := request.Payload.Update.StatsGroups["combat"]; !ok {
			return fmt.Errorf("stats group doesn't match mode")

		} else {

			if s, ok := stats["CombatWins"]; ok && s.Value > 0 && playerInfo.Team == BlueTeam {
				blueWins = true
			}
		}
	}

	// Put the XP in the player's wallet

	// temp disable

	if !serviceSettings.Load().DisableStatisticsUpdates {

		userSettings, err := LoadMatchmakingSettings(ctx, p.runtimeModule, playerInfo.UserID)
		if err != nil {
			logger.Warn("Failed to load matchmaking settings", zap.Error(err))
		} else {

			if rating, err := CalculateNewPlayerRating(request.EvrID, label.Players, label.TeamSize, blueWins); err != nil {
				logger.Error("Failed to calculate new player rating", zap.Error(err))
			} else {
				playerInfo.RatingMu = rating.Mu
				playerInfo.RatingSigma = rating.Sigma
				if err := recordRatingToLeaderboard(ctx, p.runtimeModule, playerInfo.UserID, playerInfo.DisplayName, label.Mode, rating); err != nil {
					logger.Warn("Failed to record percentile to leaderboard", zap.Error(err))
				}
				userSettings.SetRating(label.Mode, rating)
			}

			// Calculate a new rank percentile
			if rankPercentile, err := CalculateSmoothedPlayerRankPercentile(ctx, logger, p.runtimeModule, playerInfo.UserID, label.Mode); err != nil {
				logger.Error("Failed to calculate new player rank percentile", zap.Error(err))
			} else {
				// Add leaderboard for percentile
				if err := recordPercentileToLeaderboard(ctx, p.runtimeModule, playerInfo.UserID, playerInfo.DisplayName, label.Mode, rankPercentile); err != nil {
					logger.Warn("Failed to record percentile to leaderboard", zap.Error(err))
				}
				userSettings.RankPercentile = rankPercentile
			}
			if err := StoreMatchmakingSettings(ctx, p.runtimeModule, playerInfo.UserID, userSettings); err != nil {
				logger.Warn("Failed to save matchmaking settings", zap.Error(err))
			}
		}

		// Process the update into the leaderboard and profile
		err = p.leaderboardRegistry.ProcessProfileUpdate(ctx, logger, playerInfo.UserID, playerInfo.DisplayName, label.Mode, &request.Payload)
		if err != nil {
			logger.Error("Failed to process profile update", zap.Error(err), zap.Any("payload", request.Payload))
			return fmt.Errorf("failed to process profile update: %w", err)
		}
	}

	return nil
}

func (p *EvrPipeline) otherUserProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.OtherUserProfileRequest)

	go func() {
		var data json.RawMessage
		var err error
		/*
			// if the requester is a dev, modify the display name
			if params, ok := LoadParams(ctx); ok && params.IsGlobalDeveloper.Load() {
				profile := evr.ServerProfile{}

				bytes, _ := p.profileRegistry.GetCached(ctx, request.EvrId)

				if err = json.Unmarshal(*bytes, &profile); err != nil {
					logger.Error("Failed to unmarshal cached profile", zap.Error(err))
					return
				}

				// Get the match the current player is in
				if matchID, _, err := GetMatchIDBySessionID(p.runtimeModule, session.id); err == nil {

					if label, err := MatchLabelByID(ctx, p.runtimeModule, matchID); err == nil && label != nil {

						// Add asterisk if the player is backfill
						if player := label.GetPlayerByEvrID(request.EvrId); player != nil {
							// If the player is backfill, add a note to the display name

							if player.IsBackfill() {
								profile.DisplayName = fmt.Sprintf("%s*", profile.DisplayName)
							}

							percentile := int(player.RankPercentile * 100)
							rating := int(player.RatingMu)
							sigma := int(player.RatingSigma)

							profile.DisplayName = fmt.Sprintf("%s|%d/%d:%d", profile.DisplayName, percentile, rating, sigma)
						}
					}
				}
				var rawMessage json.RawMessage
				if rawMessage, err = json.Marshal(profile); err != nil {
					data = &rawMessage
				}
			}
		*/
		if data == nil {
			data, err = p.profileRegistry.GetCached(ctx, request.EvrId)
			if err != nil {
				logger.Warn("Failed to get cached profile", zap.Error(err))
				j, err := json.Marshal(evr.NewServerProfile())
				if err != nil {
					logger.Error("Failed to marshal empty profile", zap.Error(err))
				}
				data = json.RawMessage(j)
			}
		}
		// Construct the response
		response := &evr.OtherUserProfileSuccess{
			EvrId:             request.EvrId,
			ServerProfileJSON: data,
		}

		// Send the profile to the client
		if err := session.SendEvrUnrequire(response); err != nil {
			logger.Warn("Failed to send OtherUserProfileSuccess", zap.Error(err))
		}
	}()
	return nil
}
