package server

import (
	"context"
	"encoding/json"

	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
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

// loginRequest handles the login request from the client.
func (p *EvrPipeline) loginRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.LoginRequest)

	if s := ServiceSettings(); s.DisableLoginMessage != "" {
		if err := session.SendEvrUnrequire(evr.NewLoginFailure(request.XPID, "System is Temporarily Unavailable:\n"+s.DisableLoginMessage)); err != nil {
			// If there's an error, prefix it with the EchoVR Id
			return fmt.Errorf("failed to send LoginFailure: %w", err)
		}
	}

	// Start a timer to add to the metrics
	timer := time.Now()
	var err error

	// Load the session parameters.
	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	// Set the basic parameters related to client hardware and software
	params.loginSession = session
	params.xpID = request.XPID
	params.loginPayload = &request.Payload

	if err = p.authenticateSession(ctx, logger, session, &params); err == nil {
		if err = MigrateUser(ctx, logger, p.runtimeModule, p.db, session.userID.String()); err == nil {
			account, err := p.runtimeModule.AccountGetId(ctx, session.userID.String())
			if err != nil {
				return fmt.Errorf("failed to get account: %w", err)
			}
			params.account = account

			if err = p.authorizeSession(ctx, logger, session, &params); err == nil {
				if err = p.initializeSession(ctx, logger, session, &params); err == nil {

					StoreParams(ctx, &params)
					tags := params.MetricsTags()
					tags["cpu_model"] = strings.Trim(params.loginPayload.SystemInfo.CPUModel, " ")
					tags["gpu_model"] = strings.Trim(params.loginPayload.SystemInfo.VideoCard, " ")
					tags["network_type"] = params.loginPayload.SystemInfo.NetworkType
					tags["total_memory"] = strconv.FormatInt(params.loginPayload.SystemInfo.MemoryTotal, 10)
					tags["num_logical_cores"] = strconv.FormatInt(params.loginPayload.SystemInfo.NumLogicalCores, 10)
					tags["num_physical_cores"] = strconv.FormatInt(params.loginPayload.SystemInfo.NumPhysicalCores, 10)
					tags["driver_version"] = strings.Trim(params.loginPayload.SystemInfo.DriverVersion, " ")
					tags["headset_type"] = params.loginPayload.SystemInfo.HeadsetType
					tags["build_number"] = strconv.FormatInt(int64(params.loginPayload.BuildNumber), 10)
					tags["app_id"] = strconv.FormatInt(int64(params.loginPayload.AppId), 10)
					tags["publisher_lock"] = params.loginPayload.PublisherLock

					// Remove blank tags
					for k, v := range tags {
						if v == "" {
							delete(tags, k)
						}
					}

					p.metrics.CustomCounter("login_success", tags, 1)
					p.metrics.CustomTimer("login_process_latency", params.MetricsTags(), time.Since(timer))

					p.runtimeModule.Event(ctx, &api.Event{
						Name: EventUserLogin,
						Properties: map[string]string{
							"user_id": session.userID.String(),
						},
						External: true,
					})

					return session.SendEvr(
						evr.NewLoginSuccess(session.id, request.XPID),
						unrequireMessage,
						evr.NewDefaultGameSettings(),
						unrequireMessage,
					)
				}
			}
		}
	}

	err = errors.New(status.Convert(err).Message())

	// Format the error message
	errMessage := fmt.Sprintf("%s: %s", request.XPID.String(), err.Error())

	// Replace ": " with ":\n" for better readability
	errMessage = strings.Replace(errMessage, ": ", ":\n", 2)

	// Word wrap the error message
	errMessage = wordwrap.String(errMessage, 60)

	// Send the messages
	if err := session.SendEvrUnrequire(evr.NewLoginFailure(request.XPID, errMessage)); err != nil {
		// If there's an error, prefix it with the EchoVR Id
		return fmt.Errorf("failed to send LoginFailure: %w", err)
	}

	// Let the client know that the login was successful.
	// Send the login success message and the login settings.
	return nil
}

// authenticateSession handles the authentication of the login connection.
func (p *EvrPipeline) authenticateSession(ctx context.Context, logger *zap.Logger, session *sessionWS, params *SessionParameters) (err error) {

	metricsTags := params.MetricsTags()

	defer func() {
		p.runtimeModule.MetricsCounterAdd("session_authenticate", metricsTags, 1)
	}()

	// Validate the XPID
	if !params.xpID.IsValid() {
		metricsTags["error"] = "invalid_xpid"
		return errors.New("invalid XPID: " + params.xpID.String())
	}

	// Set the basic parameters related to client hardware and software
	if sn := params.loginPayload.HMDSerialNumber; strings.Contains(sn, ":") {
		metricsTags["error"] = "invalid_sn"
		return errors.New("Invalid HMD Serial Number: " + sn)
	}

	if params.loginPayload.BuildNumber != 0 && !slices.Contains(evr.KnownBuilds, params.loginPayload.BuildNumber) {
		logger.Warn("Unknown build version", zap.Int64("build", int64(params.loginPayload.BuildNumber)))
	}

	// Get the user for this device
	params.account, err = AccountGetDeviceID(ctx, p.db, p.runtimeModule, params.xpID.String())
	switch status.Code(err) {
	// The device is not linked to an account.
	case codes.NotFound:

		metricsTags["device_linked"] = "false"

		// the session is authenticated. Automatically link the device.
		if !session.userID.IsNil() {
			if err := p.runtimeModule.LinkDevice(ctx, session.UserID().String(), params.xpID.String()); err != nil {
				metricsTags["error"] = "failed_link_device"
				return fmt.Errorf("failed to link device: %w", err)
			}

			// The session is not authenticated. Create a link ticket.
		} else {

			if linkTicket, err := p.linkTicket(ctx, logger, params.xpID, session.clientIP, params.loginPayload); err != nil {

				metricsTags["error"] = "link_ticket_error"

				return fmt.Errorf("error creating link ticket: %s", err)
			} else {

				return fmt.Errorf("\nEnter this code:\n  \n>>> %s <<<\nusing '/link-headset %s' on the @%s bot.", linkTicket.Code, linkTicket.Code, p.appBot.dg.State.User.Username)
			}
		}

	// The device is linked to an account.
	case codes.OK:

		metricsTags["device_linked"] = "true"

		// if the account has a password, authenticate it.
		if params.account.Email != "" {

			// If this session was already authorized, verify it matches with the device's account.

			if !session.userID.IsNil() && params.account.User.Id != session.userID.String() {
				metricsTags["error"] = "device_link_mismatch"
				logger.Error("Device is linked to a different account.", zap.String("device_user_id", params.account.User.Id), zap.String("session_user_id", session.userID.String()))
				return fmt.Errorf("device linked to a different account. (%s)", params.account.User.Username)
			}
		} else if params.authPassword != "" {

			// Set the provided password on the account, if the user has provided one.

			if err := LinkEmail(ctx, logger, p.db, uuid.FromStringOrNil(params.account.User.Id), params.account.User.Username+"@"+p.placeholderEmail, params.authPassword); err != nil {
				metricsTags["error"] = "failed_link_email"
				return fmt.Errorf("failed to link email: %w", err)
			}
		}
	}

	// Replace the session context with a derived one that includes the login session ID and the EVR ID
	ctx = session.Context()
	session.Lock()

	session.userID = uuid.FromStringOrNil(params.account.User.Id)
	session.SetUsername(params.account.User.Username)
	session.logger = session.logger.With(zap.String("loginsid", session.id.String()), zap.String("uid", session.userID.String()), zap.String("evrid", params.xpID.String()), zap.String("username", session.Username()))

	ctx = context.WithValue(ctx, ctxUserIDKey{}, session.userID)     // apiServer compatibility
	ctx = context.WithValue(ctx, ctxUsernameKey{}, session.Username) // apiServer compatibility
	session.ctx = ctx

	session.Unlock()

	return nil
}

func (p *EvrPipeline) authorizeSession(ctx context.Context, logger *zap.Logger, session *sessionWS, params *SessionParameters) error {

	var err error

	metricsTags := params.MetricsTags()
	defer func() {
		p.runtimeModule.MetricsCounterAdd("session_authorize", metricsTags, 1)
	}()
	// Authentication is complete. Authorize the session.

	// Load the login history for audit purposes.
	loginHistory, err := LoginHistoryLoad(ctx, p.runtimeModule, params.account.User.Id)
	if err != nil {
		metricsTags["error"] = "failed_load_login_history"
		return fmt.Errorf("failed to load login history: %w", err)
	}
	defer loginHistory.Store(ctx, p.runtimeModule)

	// The account is now authenticated. Authorize the session.

	if status.Code(err) == codes.PermissionDenied || params.account.DisableTime != nil {

		p.runtimeModule.MetricsCounterAdd("login_attempt_banned_account", nil, 1)

		logger.Info("Attempted login to banned account.",
			zap.String("xpid", params.xpID.Token()),
			zap.String("client_ip", session.clientIP),
			zap.String("uid", params.account.User.Id),
			zap.Any("login_payload", params.loginPayload))

		metricsTags["error"] = "account_disabled"

		return fmt.Errorf("Account disabled by EchoVRCE Admins.")
	}

	// Require IP verification, if the session is not authenticated.
	if authorized := loginHistory.IsAuthorizedIP(session.clientIP); !authorized {

		// Automatically validate the IP if the session is authenticated.
		if params.IsWebsocketAuthenticated {

			if isNew := loginHistory.AuthorizeIP(session.clientIP); isNew {
				if err := p.appBot.SendIPAuthorizationNotification(params.account.User.Id, session.clientIP); err != nil {
					logger.Warn("Failed to send IP authorization notification", zap.Error(err))
				}
			}
		} else {

			// IP is not authorized. Add a pending authorization entry.
			entry := loginHistory.AddPendingAuthorizationIP(params.xpID, session.clientIP, params.loginPayload)

			if p.appBot != nil && p.appBot.dg != nil && p.appBot.dg.State != nil && p.appBot.dg.State.User != nil {
				ipqs, err := p.ipqsClient.Get(ctx, session.clientIP)
				if err != nil {
					logger.Debug("Failed to get IPQS details", zap.Error(err))
				}

				if err := p.appBot.SendIPApprovalRequest(ctx, params.account.User.Id, entry, ipqs); err != nil {
					// The user has DMs from non-friends disabled. Tell them to use the slash command.
					metricsTags["error"] = "failed_send_ip_approval_request"

					return fmt.Errorf("\nUnrecognized connection location. Please type\n  /verify  \nin a guild with the @%s bot.", p.appBot.dg.State.User.Username)
				}

				metricsTags["error"] = "ip_verification_required"
				return fmt.Errorf("New location detected.\nPlease check your Discord DMs to accept the \nverification request from @%s.", p.appBot.dg.State.User.Username)
			}

			metricsTags["error"] = "ip_verification_failed"
			return fmt.Errorf("New location detected. Please contact EchoVRCE support.")
		}
	} else {
		loginHistory.AuthorizeIP(session.clientIP)
	}

	loginHistory.Update(params.xpID, session.clientIP, params.loginPayload)

	// Common error handling
	if params.account.GetDisableTime() != nil {
		// The account is banned. log the attempt.
		logger.Info("Attempted login to banned account.",
			zap.String("xpid", params.xpID.Token()),
			zap.String("client_ip", session.clientIP),
			zap.String("uid", params.account.User.Id),
			zap.Any("login_payload", params.loginPayload))

		metricsTags["error"] = "account_disabled"

		return fmt.Errorf("Account disabled by EchoVRCE Admins.")
	}

	metricsTags["error"] = "nil"

	return nil
}

func (p *EvrPipeline) initializeSession(ctx context.Context, logger *zap.Logger, session *sessionWS, params *SessionParameters) error {

	var err error

	metricsTags := params.MetricsTags()
	defer func() {
		p.runtimeModule.MetricsCounterAdd("session_initialize", metricsTags, 1)
	}()
	params.accountMetadata = &AccountMetadata{}

	// Get the user's metadata from the storage

	if err := json.Unmarshal([]byte(params.account.User.Metadata), params.accountMetadata); err != nil {
		metricsTags["error"] = "failed_unmarshal_metadata"
		return fmt.Errorf("failed to unmarshal metadata: %w", err)
	}
	params.accountMetadata.account = params.account

	// Get the GroupID from the user's metadata
	params.guildGroups, err = GuildUserGroupsList(ctx, p.runtimeModule, params.account.User.Id)
	if err != nil {
		metricsTags["error"] = "failed_get_guild_groups"
		return fmt.Errorf("failed to get guild groups: %w", err)
	}

	if len(params.guildGroups) == 0 {
		// User is not in any groups
		metricsTags["error"] = "user_not_in_any_groups"
		guildID := p.discordCache.GroupIDToGuildID(params.accountMetadata.ActiveGroupID)
		p.discordCache.QueueSyncMember(guildID, params.account.CustomId)

		return fmt.Errorf("user is not in any groups. try again in 30 seconds.")
	}

	if _, ok := params.guildGroups[params.accountMetadata.ActiveGroupID]; !ok {
		// User is not in the active group
		logger.Warn("User is not in the active group", zap.String("uid", params.account.User.Id), zap.String("gid", params.accountMetadata.ActiveGroupID))
		params.accountMetadata.ActiveGroupID = uuid.Nil.String()
	}

	// If the user is not in a group, set the active group to the group with the most members
	if params.accountMetadata.ActiveGroupID == "" || params.accountMetadata.ActiveGroupID == uuid.Nil.String() {
		// Active group is not set.

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

		if err := p.runtimeModule.AccountUpdateId(ctx, params.account.User.Id, "", params.accountMetadata.MarshalMap(), params.accountMetadata.GetActiveGroupDisplayName(), "", "", "", ""); err != nil {
			metricsTags["error"] = "failed_update_metadata"
			return fmt.Errorf("failed to update user metadata: %w", err)
		}
	}

	if ismember, err := CheckSystemGroupMembership(ctx, p.db, session.userID.String(), GroupGlobalDevelopers); err != nil {
		metricsTags["error"] = "group_check_failed"
		return fmt.Errorf("failed to check system group membership: %w", err)
	} else if ismember {
		params.isGlobalDeveloper = true
		params.isGlobalModerator = true

	} else if ismember, err := CheckSystemGroupMembership(ctx, p.db, session.userID.String(), GroupGlobalModerators); err != nil {
		metricsTags["error"] = "group_check_failed"
		return fmt.Errorf("failed to check system group membership: %w", err)
	} else if ismember {
		params.isGlobalModerator = true
	}

	if params.userDisplayNameOverride != "" {
		// This will be picked up by GetActiveGroupDisplayName and other functions
		params.accountMetadata.sessionDisplayNameOverride = params.userDisplayNameOverride
	}
	params.displayNames, err = DisplayNameHistoryLoad(ctx, p.runtimeModule, session.userID.String())
	if err != nil {
		logger.Warn("Failed to load display name history", zap.Error(err))
	}
	params.displayNames.Update(params.accountMetadata.ActiveGroupID, params.accountMetadata.GetActiveGroupDisplayName(), params.account.User.Username, true)

	if err := DisplayNameHistoryStore(ctx, p.runtimeModule, session.userID.String(), params.displayNames); err != nil {
		logger.Warn("Failed to store display name history", zap.Error(err))
	}

	s := session
	// Register initial status tracking and presence(s) for this session.
	s.statusRegistry.Follow(s.id, map[uuid.UUID]struct{}{s.userID: {}})

	// Both notification and status presence.
	s.tracker.TrackMulti(ctx, s.id, []*TrackerOp{
		// EVR packet data stream for the login session by user ID, and service ID, with EVR ID
		{
			Stream: PresenceStream{Mode: StreamModeService, Subject: s.userID, Label: StreamLabelLoginService},
			Meta:   PresenceMeta{Format: s.format, Username: session.Username(), Hidden: false},
		},
		// EVR packet data stream for the login session by session ID and service ID, with EVR ID
		{
			Stream: PresenceStream{Mode: StreamModeService, Subject: s.id, Label: StreamLabelLoginService},
			Meta:   PresenceMeta{Format: s.format, Username: session.Username(), Hidden: false},
		},
		// Notification presence.
		{
			Stream: PresenceStream{Mode: StreamModeNotifications, Subject: s.userID},
			Meta:   PresenceMeta{Format: s.format, Username: s.Username(), Hidden: false},
		},

		// Status presence.
		{
			Stream: PresenceStream{Mode: StreamModeStatus, Subject: s.userID},
			Meta:   PresenceMeta{Format: s.format, Username: s.Username(), Status: ""},
		},
	}, s.userID)

	return nil
}

func (p *EvrPipeline) channelInfoRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	_ = in.(*evr.ChannelInfoRequest)

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	groupID := uuid.Nil
	if groupID = params.accountMetadata.GetActiveGroupID(); groupID.IsNil() {
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

	modes := []evr.Symbol{
		evr.ModeArenaPublic,
		evr.ModeCombatPublic,
	}
	serverProfile, err := NewUserServerProfile(ctx, p.db, params.account, params.xpID, params.accountMetadata.GetActiveGroupID().String(), modes, false)
	if err != nil {
		return fmt.Errorf("failed to get server profile: %w", err)
	}

	params.profile.Store(serverProfile)

	clientProfile, err := NewClientProfile(ctx, params.accountMetadata, params.xpID)
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

	metadata, err := AccountMetadataLoad(ctx, p.runtimeModule, session.userID.String())
	if err != nil {
		return fmt.Errorf("failed to load account metadata: %w", err)
	}

	metadata.TeamName = update.TeamName

	metadata.CombatLoadout = CombatLoadout{
		CombatWeapon:       update.CombatWeapon,
		CombatGrenade:      update.CombatGrenade,
		CombatDominantHand: update.CombatDominantHand,
		CombatAbility:      update.CombatAbility,
	}
	metadata.LegalConsents = update.LegalConsents
	metadata.GhostedPlayers = update.GhostedPlayers.Players
	metadata.MutedPlayers = update.MutedPlayers.Players

	if err := AccountMetadataSet(ctx, p.runtimeModule, session.userID.String(), metadata); err != nil {
		return fmt.Errorf("failed to update account metadata: %w", err)
	}

	params.accountMetadata = metadata
	StoreParams(ctx, &params)
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

		if !params.IsVR() {

			eulaVersion := params.accountMetadata.LegalConsents.EulaVersion
			gaVersion := params.accountMetadata.LegalConsents.GameAdminVersion
			document = evr.NewEULADocument(int(eulaVersion), int(gaVersion), request.Language, "https://github.com/EchoTools", "Blank EULA for NoVR clients. You should only see this once.")
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

// A profile update request is sent from the game server's login connection.
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

	// If the player isn't in the match, or isn't a player, do not update the stats
	if playerInfo == nil || playerInfo.Team != BlueTeam && playerInfo.Team != OrangeTeam {
		return fmt.Errorf("non-player profile update request: %s", request.EvrID.String())
	}

	// Set the player's session to not be an early quitter
	if playerSession := p.sessionRegistry.Get(uuid.FromStringOrNil(playerInfo.SessionID)); playerSession != nil {
		if params, ok := LoadParams(playerSession.Context()); ok {
			params.isEarlyQuitter.Store(false)
		} else {
			logger.Warn("Failed to load session parameters", zap.String("sessionID", playerInfo.SessionID))
		}
	}

	// If the player isn't a member of the group, do not update the stats
	metadata, err := AccountMetadataLoad(ctx, p.runtimeModule, playerInfo.UserID)
	if err != nil {
		return fmt.Errorf("failed to get account metadata: %w", err)
	}

	groupIDStr := label.GetGroupID().String()

	if !serviceSettings.Load().DisableStatisticsUpdates && metadata.GroupDisplayNames[groupIDStr] != "" {

		userSettings, err := LoadMatchmakingSettings(ctx, p.runtimeModule, playerInfo.UserID)
		if err != nil {
			logger.Warn("Failed to load matchmaking settings", zap.Error(err))
		} else {

			// Determine winning team
			blueWins := playerInfo.Team == BlueTeam && request.Payload.IsWinner()

			if rating, err := CalculateNewPlayerRating(playerInfo.EvrID, label.Players, label.TeamSize, blueWins); err != nil {
				logger.Error("Failed to calculate new player rating", zap.Error(err))
			} else {
				playerInfo.RatingMu = rating.Mu
				playerInfo.RatingSigma = rating.Sigma
				if err := MatchmakingRatingStore(ctx, p.runtimeModule, playerInfo.UserID, playerInfo.DisplayName, groupIDStr, label.Mode, rating); err != nil {
					logger.Warn("Failed to record percentile to leaderboard", zap.Error(err))
				}
			}

			// Calculate a new rank percentile
			if rankPercentile, err := CalculateSmoothedPlayerRankPercentile(ctx, logger, p.runtimeModule, playerInfo.UserID, groupIDStr, label.Mode); err != nil {
				logger.Error("Failed to calculate new player rank percentile", zap.Error(err))
			} else if err := MatchmakingRankPercentileStore(ctx, p.runtimeModule, playerInfo.UserID, playerInfo.DisplayName, groupIDStr, label.Mode, rankPercentile); err != nil {
				logger.Warn("Failed to record percentile to leaderboard", zap.Error(err))
			}

			if err := StoreMatchmakingSettings(ctx, p.runtimeModule, playerInfo.UserID, userSettings); err != nil {
				logger.Warn("Failed to save matchmaking settings", zap.Error(err))
			}
		}

		var stats evr.Statistics
		switch label.Mode {
		case evr.ModeArenaPublic:
			stats = &request.Payload.Update.Statistics.Arena
		case evr.ModeCombatPublic:
			stats = &request.Payload.Update.Statistics.Combat
		}

		// Get the players existing statistics
		prevPlayerStats, _, err := PlayerStatisticsGetID(ctx, p.db, playerInfo.UserID, groupIDStr, []evr.Symbol{label.Mode}, false)
		if err != nil {
			return fmt.Errorf("failed to get player statistics: %w", err)
		}
		g := evr.StatisticsGroup{
			Mode:          label.Mode,
			ResetSchedule: evr.ResetScheduleAllTime,
		}

		prevStats, ok := prevPlayerStats[g]
		if !ok {
			prevStats = evr.NewServerProfile().Statistics[g]
		}

		entries, err := StatisticsToEntries(playerInfo.UserID, playerInfo.DisplayName, label.GetGroupID().String(), label.Mode, prevStats, stats)

		p.statisticsQueue.Add(entries)
	}

	return nil
}

func (p *EvrPipeline) otherUserProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.OtherUserProfileRequest)

	tags := make(map[string]string, 0)
	startTime := time.Now()

	defer func() {
		tags["error"] = "nil"
		p.metrics.CustomCounter("profile_request_count", tags, 1)
		p.metrics.CustomTimer("profile_request_latency", tags, time.Since(startTime))
	}()

	var ok bool
	var data json.RawMessage

	if data, ok = p.profileCache.Load(request.EvrId); !ok {
		logger.Error("Profile does not exist in cache.", zap.String("evrId", request.EvrId.String()))
		return nil
	}

	response := &evr.OtherUserProfileSuccess{
		EvrId:             request.EvrId,
		ServerProfileJSON: data,
	}

	p.metrics.CustomGauge("profile_size_bytes", nil, float64(len(data)))

	if err := session.SendEvrUnrequire(response); err != nil {
		tags["error"] = "failed_send_profile"
		logger.Warn("Failed to send OtherUserProfileSuccess", zap.Error(err))
	}

	return nil
}

func MatchIDsByEvrID(ctx context.Context, nk runtime.NakamaModule, evrID evr.EvrId) ([]MatchID, error) {
	presences, err := nk.StreamUserList(StreamModeService, evrID.UUID().String(), "", StreamLabelMatchService, false, true)
	if err != nil {
		return nil, fmt.Errorf("failed to get user stream: %w", err)
	}

	matchIDs := make([]MatchID, 0, len(presences))
	for _, presence := range presences {
		if matchID, err := MatchIDFromString(presence.GetStatus()); err != nil {
			continue
		} else {
			matchIDs = append(matchIDs, matchID)
		}
	}
	return matchIDs, nil
}
