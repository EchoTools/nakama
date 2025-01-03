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
	DocumentStorageCollection = "GameDocuments"
)

// msgFailedLoginFn sends a LoginFailure message to the client.
// The error message is word-wrapped to 60 characters, 4 lines long.
func msgFailedLoginFn(session *sessionWS, evrId evr.EvrId, err error) error {
	// Format the error message
	s := fmt.Sprintf("%s: %s", evrId.String(), err.Error())

	// Replace ": " with ":\n" for better readability
	s = strings.Replace(s, ": ", ":\n", 2)

	// Word wrap the error message
	errMessage := wordwrap.String(s, 60)

	// Send the messages
	if err := session.SendEvrUnrequire(evr.NewLoginFailure(evrId, errMessage)); err != nil {
		// If there's an error, prefix it with the EchoVR Id
		return fmt.Errorf("failed to send LoginFailure: %w", err)
	}

	return nil
}

// loginRequest handles the login request from the client.
func (p *EvrPipeline) loginRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.LoginRequest)

	// Start a timer to add to the metrics
	timer := time.Now()

	// Authenticate the connection
	if err := p.processLogin(ctx, logger, session, request); err != nil {
		st := status.Convert(err)
		return msgFailedLoginFn(session, request.XPID, errors.New(st.Message()))
	}

	p.metrics.CustomTimer("login_duration", nil, time.Since(timer))
	// Let the client know that the login was successful.
	// Send the login success message and the login settings.
	return session.SendEvr(
		evr.NewLoginSuccess(session.id, request.XPID),
		unrequireMessage,
		evr.NewDefaultGameSettings(),
		unrequireMessage,
	)
}

// processLogin handles the authentication of the login connection.
func (p *EvrPipeline) processLogin(ctx context.Context, logger *zap.Logger, session *sessionWS, request *evr.LoginRequest) (err error) {

	// Load the session parameters.
	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	// Set the basic parameters related to client hardware and software
	params.loginSession = session
	params.xpID = request.XPID
	params.isVR = request.Payload.SystemInfo.HeadsetType != "No VR"
	params.isPCVR = request.Payload.BuildVersion != evr.StandaloneBuild

	// Validate the XPID
	if !params.xpID.IsValid() {
		return errors.New("invalid XPID: " + params.xpID.String())
	}

	// Set the basic parameters related to client hardware and software
	if sn := request.Payload.HMDSerialNumber; strings.Contains(sn, ":") {
		return errors.New("Invalid HMD Serial Number: " + sn)
	}

	if !slices.Contains(evr.KnownBuilds, request.Payload.BuildVersion) {
		logger.Warn("Unknown build version", zap.Int64("build", int64(request.Payload.BuildVersion)))
	}

	// Get the user for this device
	params.account, err = AccountGetDeviceID(ctx, p.db, p.runtimeModule, params.xpID.String())
	switch status.Code(err) {
	// The device is not linked to an account.
	case codes.NotFound:

		// the session is authenticated. Automatically link the device.
		if !session.userID.IsNil() {
			if err := p.runtimeModule.LinkDevice(ctx, session.UserID().String(), params.xpID.String()); err != nil {
				return fmt.Errorf("failed to link device: %w", err)
			}

			params.account = deviceAccount

		} else {
			if linkTicket, err := p.linkTicket(ctx, logger, params.xpID, session.clientIP, &request.Payload); err != nil {
				return fmt.Errorf("error creating link ticket: %s", err)
			} else {
				return fmt.Errorf("\nEnter this code:\n  \n>>> %s <<<\nusing '/link-headset %s' in the Echo VR Lounge Discord.", linkTicket.Code, linkTicket.Code)
			}
		}

	// The device is linked to an account.
	case codes.OK:

		// if the account has a password, authenticate it.
		if params.account.Email != "" {
			// If this session was already authorized, verify it matches with the device's account.
			if !session.userID.IsNil() && params.account.User.Id != session.userID.String() {
				logger.Error("Device is linked to a different account.", zap.String("device_user_id", params.account.User.Id), zap.String("session_user_id", session.userID.String()))
				return fmt.Errorf("device linked to a different account. (%s)", params.account.User.Username)
			}
		} else if params.authPassword != "" {
			// The account has no password, and the user has provided one.
			// Set the password.
			if err := LinkEmail(ctx, logger, p.db, uuid.FromStringOrNil(params.account.User.Id), params.account.User.Username+"@"+p.placeholderEmail, params.authPassword); err != nil {
				return fmt.Errorf("failed to link email: %w", err)
			}
		}
	}

	session.userID = uuid.FromStringOrNil(params.account.User.Id)
	session.SetUsername(params.account.User.Username)

	if status.Code(err) != codes.PermissionDenied || params.account.DisableTime != nil {
		logger.Info("Attempted login to banned account.",
			zap.String("xpid", params.xpID.Token()),
			zap.String("client_ip", session.clientIP),
			zap.String("uid", params.account.User.Id),
			zap.Any("login_payload", request.Payload))

		// If the user has provided a password for the first time, then set the password on the account.
	} else if params.authPassword != "" && deviceAccount.Email == "" {

		// Set the password on the account.
		if err := LinkEmail(ctx, logger, session.pipeline.db, uuid.FromStringOrNil(deviceAccount.User.Id), deviceAccount.User.Id+"@"+p.placeholderEmail, params.authPassword); err != nil {
			if err != nil {
				return fmt.Errorf("failed to link email: %w", err)
			}
		}
	} else {

		// Some other error occurred.
		return fmt.Errorf("failed to get account: %w", err)
	}

	// Migrate the user's account

	if err := MigrateUser(ctx, NewRuntimeGoLogger(logger), p.runtimeModule, p.db, params.account.User.Id); err != nil {
		return fmt.Errorf("failed to migrate user data: %w", err)
	}

	// Load the login history for audit purposes.
	loginHistory, err := LoginHistoryLoad(ctx, p.runtimeModule, params.account.User.Id)
	if err != nil {
		return fmt.Errorf("failed to load login history: %w", err)
	}

	// Automatically validate the IP if the session is authenticated.
	if !session.userID.IsNil() {

		loginHistory.AuthorizeIP(session.clientIP)

		// Require IP verification, if the session is not authenticated.
	} else if ok := loginHistory.IsAuthorizedIP(session.clientIP); !ok {

		loginHistory.AddPendingAuthorizationIP(session.clientIP)

		if p.appBot != nil && p.appBot.dg != nil && p.appBot.dg.State != nil && p.appBot.dg.State.User != nil {
			ipqs := p.ipqsClient.IPDetailsWithTimeout(session.clientIP)
			if err := p.appBot.SendIPApprovalRequest(ctx, params.account.User.Id, session.clientIP, ipqs); err != nil {
				return fmt.Errorf("failed to send IP approval request: %w", err)
			}
			return fmt.Errorf("New location detected.\nPlease check your Discord DMs to accept the \nverification request from @%s.", p.appBot.dg.State.User.Username)
		}
		return fmt.Errorf("New location detected. Please contact EchoVRCE support.")

	}

	loginHistory.Update(params.xpID, session.clientIP, &request.Payload)

	// If the session is authenticated, auto-validate teh

	session.userID = uuid.FromStringOrNil(params.account.User.Id)
	session.SetUsername(params.account.User.Username)

	// Common error handling
	if params.account.GetDisableTime() != nil {
		// The account is banned. log the attempt.

		return fmt.Errorf("Account disabled by EchoVRCE Admins.")
	}

	params.accountMetadata = &AccountMetadata{}
	if err := json.Unmarshal([]byte(params.account.User.Metadata), params.accountMetadata); err != nil {
		return fmt.Errorf("failed to unmarshal metadata: %w", err)
	}
	params.accountMetadata.account = params.account

	// Get the GroupID from the user's metadata
	params.guildGroups, err = GuildUserGroupsList(ctx, p.runtimeModule, params.account.User.Id)
	if err != nil {
		return fmt.Errorf("failed to get guild groups: %w", err)
	}

	if g, ok := params.guildGroups[params.accountMetadata.ActiveGroupID]; !ok {
		// User is not in the active group
		logger.Warn("User is not in the active group", zap.String("uid", params.account.User.Id), zap.String("gid", g.Group.Id))
		params.accountMetadata.ActiveGroupID = uuid.Nil.String()
	}

	// If the user is not in a group, set the active group to the group with the most members
	if params.accountMetadata.ActiveGroupID == uuid.Nil.String() {
		groupIDs := make([]string, 0, len(params.guildGroups))
		for id := range params.guildGroups {
			groupIDs = append(groupIDs, id)
		}
		// Sort the groups by the edgecount
		slices.SortStableFunc(groupIDs, func(a, b string) int {
			return int(params.guildGroups[a].Group.EdgeCount - params.guildGroups[b].Group.EdgeCount)
		})
		slices.Reverse(groupIDs)

		params.accountMetadata.SetActiveGroupID(uuid.FromStringOrNil(groupIDs[0]))
		logger.Debug("Set active group", zap.String("uid", params.account.User.Id), zap.String("gid", params.accountMetadata.ActiveGroupID))

		if err := p.runtimeModule.AccountUpdateId(ctx, params.account.User.Id, "", params.accountMetadata.MarshalMap(), "", "", "", "", ""); err != nil {
			return fmt.Errorf("failed to update user metadata: %w", err)
		}
	}

	if ismember, err := CheckSystemGroupMembership(ctx, p.db, session.userID.String(), GroupGlobalDevelopers); err != nil {
		return fmt.Errorf("failed to check system group membership: %w", err)
	} else if ismember {
		params.isGlobalDeveloper = true
		params.isGlobalModerator = true

	} else if ismember, err := CheckSystemGroupMembership(ctx, p.db, session.userID.String(), GroupGlobalModerators); err != nil {
		return fmt.Errorf("failed to check system group membership: %w", err)
	} else if ismember {
		params.isGlobalModerator = true
	}

	if params.userDisplayNameOverride != "" {
		// This will be picked up by GetActiveGroupDisplayName and other functions
		params.accountMetadata.sessionDisplayNameOverride = params.userDisplayNameOverride
	}

	StoreParams(ctx, &params)
	// Initialize the full session
	if err := session.SetIdentity(uuid.FromStringOrNil(params.account.User.Id), params.xpID, params.account.User.Username); err != nil {
		return fmt.Errorf("failed to login: %w", err)
	}
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

	return nil
}

func (p *EvrPipeline) channelInfoRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	_ = in.(*evr.ChannelInfoRequest)

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	groupID := uuid.Nil
	if groupID := params.accountMetadata.GetActiveGroupID(); groupID == uuid.Nil {
		return fmt.Errorf("active group is nil")
	}

	key := fmt.Sprintf("channelInfo,%s", groupID.String())

	// Check the cache first
	if message := p.MessageCacheLoad(key); message != nil {
		return session.SendEvrUnrequire(message)
	}

	g, ok := params.guildGroups[groupID.String()]
	if !ok {
		return fmt.Errorf("guild group not found: %s", groupID.String())
	}

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

	message := evr.NewSNSChannelInfoResponse(resource)
	p.MessageCacheStore(key, message, time.Minute*3)

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

	clientProfile, err := NewClientProfile(ctx, p.db, p.runtimeModule, params.accountMetadata, params.xpID)
	if err != nil {
		return fmt.Errorf("failed to get client profile: %w", err)
	}

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

	if err := p.handleClientProfileUpdate(ctx, logger, session, request.EvrId, request.ClientProfile); err != nil {
		if err := session.SendEvr(evr.NewUpdateProfileFailure(request.EvrId, uint64(400), err.Error())); err != nil {
			logger.Error("Failed to send UpdateProfileFailure", zap.Error(err))
		}
	}

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

				p.appBot.LogMessageToChannel(fmt.Sprintf("User <@%s> has accepted the community values.", params.DiscordID()), gg.AuditChannelID)
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
