package service

import (
	"context"
	"encoding/json"

	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	DocumentStorageCollection = "GameDocuments"
)

// loginRequest handles the login request from the client.
func (p *EvrPipeline) loginRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	request := in.(*evr.LoginRequest)

	if m := ServiceSettings().DisableLoginMessage; m != "" {
		return session.SendEVR(Envelope{
			ServiceType: ServiceTypeLogin,
			Messages: []evr.Message{
				evr.NewLoginFailure(request.XPID, "System is Temporarily Unavailable:\n"+m),
			},
			State: RequireStateUnrequired,
		})
	}

	// Start a timer to add to the metrics
	timer := time.Now()

	// Load the session parameters.
	// This is normally empty, unless this is a shared socket.
	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	// Set the basic parameters
	params.loginSession = session
	params.xpID = request.XPID
	params.loginPayload = &request.Payload

	logger = logger.With(zap.String("xp_id", request.XPID.String()))

	// Process the login request and populate the session parameters.
	if err := p.processLoginRequest(ctx, logger, session, &params); err != nil {

		discordID := ""
		if userID, err := GetUserIDByDeviceID(ctx, p.db, request.XPID.String()); err == nil {
			discordID = p.discordCache.UserIDToDiscordID(userID)
		} else if !errors.Is(err, &DeviceNotLinkedError{}) {
			logger.Debug("Failed to get user ID by device ID", zap.Error(err))
		}

		return session.SendEVR(Envelope{
			ServiceType: ServiceTypeLogin,
			Messages: []evr.Message{
				evr.NewLoginFailure(request.XPID, formatLoginErrorMessage(request.XPID, discordID, err)),
			},
			State: RequireStateUnrequired,
		})
	}

	StoreParams(ctx, &params)

	tags := params.MetricsTags()
	tags["cpu_model"] = strings.TrimSpace(params.loginPayload.SystemInfo.CPUModel)
	tags["gpu_model"] = strings.TrimSpace(params.loginPayload.SystemInfo.VideoCard)
	tags["network_type"] = params.loginPayload.SystemInfo.NetworkType
	tags["total_memory"] = strconv.FormatInt(params.loginPayload.SystemInfo.MemoryTotal, 10)
	tags["num_logical_cores"] = strconv.FormatInt(params.loginPayload.SystemInfo.NumLogicalCores, 10)
	tags["num_physical_cores"] = strconv.FormatInt(params.loginPayload.SystemInfo.NumPhysicalCores, 10)
	tags["driver_version"] = strings.TrimSpace(params.loginPayload.SystemInfo.DriverVersion)
	tags["headset_type"] = normalizeHeadsetType(params.loginPayload.SystemInfo.HeadsetType)
	tags["build_number"] = strconv.FormatInt(int64(params.loginPayload.BuildNumber), 10)
	tags["app_id"] = strconv.FormatInt(int64(params.loginPayload.AppId), 10)
	tags["publisher_lock"] = strings.TrimSpace(params.loginPayload.PublisherLock)

	p.metrics.CustomCounter("login_success", tags, 1)
	p.metrics.CustomTimer("login_process_latency", params.MetricsTags(), time.Since(timer))

	// Set the game settings based on the service settings
	gameSettings := evr.NewDefaultGameSettings()
	if params.enableAllRemoteLogs {
		gameSettings.RemoteLogSocial = true
		gameSettings.RemoteLogWarnings = true
		gameSettings.RemoteLogErrors = true
		gameSettings.RemoteLogRichPresence = true
		gameSettings.RemoteLogMetrics = true
	}

	return session.SendEVR(Envelope{
		ServiceType: ServiceTypeLogin,
		Messages: []evr.Message{
			evr.NewLoginSuccess(session.id, request.XPID),
			&evr.STcpConnectionUnrequireEvent{},
			evr.NewDefaultGameSettings(),
		},
	})
}

// normalizes all the meta headset types to a common format
var headsetMappings = func() map[string]string {

	mappings := map[string][]string{
		"Meta Quest 1":   {"Quest", "Oculus Quest"},
		"Meta Quest 2":   {"Quest 2", "Oculus Quest2"},
		"Meta Quest Pro": {"Quest Pro"},
		"Meta Quest 3":   {"Quest 3", "Oculus Quest3"},
		"Meta Quest 3S":  {"Quest 3S", "Oculus Quest3S"},

		"Meta Quest 1 (Link)":   {"Quest (Link)", "Oculus Quest (Link)"},
		"Meta Quest 2 (Link)":   {"Quest 2 (Link)", "Oculus Quest2 (Link)"},
		"Meta Quest Pro (Link)": {"Quest Pro (Link)", "Oculus Quest Pro (Link)", "Meta Quest Pro (Link)"},
		"Meta Quest 3 (Link)":   {"Quest 3 (Link)", "Oculus Quest3 (Link)"},
		"Meta Quest 3S (Link)":  {"Quest 3S (Link)", "Oculus Quest3S (Link)"},

		"Meta Rift CV1":    {"Oculus Rift CV1"},
		"Meta Rift S":      {"Oculus Rift S"},
		"HTC Vive Elite":   {"Vive Elite"},
		"HTC Vive MV":      {"Vive MV", "Vive. MV"},
		"Bigscreen Beyond": {"Beyond"},
		"Valve Index":      {"Index"},
		"Potato Potato 4K": {"Potato VR"},
	}

	// Create a reverse mapping
	reverse := make(map[string]string)
	for k, v := range mappings {
		for _, s := range v {
			reverse[s] = k
		}
	}

	return reverse
}()

func normalizeHeadsetType(headset string) string {
	if headset == "" {
		return "Unknown"
	}

	if v, ok := headsetMappings[headset]; ok {
		return v
	}

	return headset
}

func (p *EvrPipeline) processLoginRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, params *SessionParameters) error {

	var err error

	// Validate the payload of the login message
	err = p.validateLoginPayload(ctx, logger, session, params)
	if err != nil {
		return session.SendEVR(Envelope{
			ServiceType: ServiceTypeLogin,
			Messages: []evr.Message{
				evr.NewLoginFailure(params.xpID, formatLoginErrorMessage(params.xpID, "", err)),
			},
			State: RequireStateUnrequired,
		})
	}

	if err = p.authenticateSession(ctx, logger, session, params); err != nil {
		return err
	}

	loginMigrations := []UserMigrater{}
	if err = MigrateUser(ctx, server.NewRuntimeGoLogger(logger), p.nk, p.db, session.userID.String(), loginMigrations); err != nil {
		return fmt.Errorf("failed to migrate user: %w", err)
	}

	if params.profile, err = EVRProfileLoad(ctx, p.nk, params.profile.ID()); err != nil {
		return fmt.Errorf("failed to get account: %w", err)
	}

	if err = p.authorizeSession(ctx, logger, session, params); err != nil {
		return err
	}

	if err = p.initializeSession(ctx, logger, session, params); err != nil {
		return err
	}

	return nil
}

func (p *EvrPipeline) validateLoginPayload(ctx context.Context, logger *zap.Logger, session *sessionEVR, params *SessionParameters) error {
	// Validate the XPID
	if !params.xpID.IsValid() {
		return newLoginError("invalid_xpid", "invalid XPID: %s", params.xpID.String())
	}
	// Validate the HMD Serial Number
	if sn := params.loginPayload.HMDSerialNumber; strings.Contains(sn, ":") {
		return newLoginError("invalid_sn", "Invalid HMD Serial Number: %s", sn)
	}
	// Check the build number
	if params.loginPayload.BuildNumber != 0 && !slices.Contains(evr.KnownBuilds, params.loginPayload.BuildNumber) {
		logger.Warn("Unknown build version", zap.Int64("build", int64(params.loginPayload.BuildNumber)))
	}

	return nil
}

// authenticateSession handles the authentication of the login connection.
func (p *EvrPipeline) authenticateSession(ctx context.Context, logger *zap.Logger, session *sessionEVR, params *SessionParameters) (err error) {

	metricsTags := params.MetricsTags()
	defer func() {
		p.nk.MetricsCounterAdd("session_authenticate", metricsTags, 1)
	}()

	// Retrieve the account for this XPID
	params.profile, err = AccountGetDeviceID(ctx, p.db, p.nk, params.xpID.String())
	code := status.Code(err)
	if code == codes.NotFound {

		// If the session is authenticated, link the device to the account.
		if !session.userID.IsNil() {
			if err := p.nk.LinkDevice(ctx, session.UserID().String(), params.xpID.String()); err != nil {
				return newLoginError("link_device_failed", "failed to link device: %s", err.Error())
			}
			params.profile, err = AccountGetDeviceID(ctx, p.db, p.nk, params.xpID.String())
			if err != nil {
				return newLoginError("get_account_failed", "failed to get account after linking device: %s", err.Error())
			}
			metricsTags["device_linked"] = "true"
		} else {

			// If the session is not authenticated, create a link ticket.
			// This will be used to link the device to the account later.
			// The link ticket will be sent to the client in a DeviceNotLinkedError.
			// The client will then display the link code to the user.
			// The user can then use the link code to link their device to their account via discord
			linkTicket, linkErr := IssueLinkTicket(ctx, p.nk, params.xpID, session.clientIP, params.loginPayload)
			if linkErr != nil {
				return newLoginError("link_ticket_error", "error creating link ticket: %s", linkErr.Error())
			}
			return &DeviceNotLinkedError{
				Code:         linkTicket.Code,
				Instructions: ServiceSettings().LinkInstructions,
			}
		}
	} else if code == codes.OK {
		// The device is linked to an account.

		metricsTags["device_linked"] = "true"
		requiresPasswordAuth := params.profile.HasPasswordSet()
		authenticatedViaSession := !session.userID.IsNil()
		isAccountMismatched := params.profile.ID() != session.userID.String()
		passwordProvided := params.authPassword != ""

		// The account has a password set and requires password authentication.
		if requiresPasswordAuth && !authenticatedViaSession {
			return newLoginError("session_auth_failed", "session authentication failed: account requires password authentication")
		}

		if !requiresPasswordAuth {
			if authenticatedViaSession && isAccountMismatched {
				logger.Warn("Device is linked to a different account.", zap.String("device_user_id", params.profile.ID()), zap.String("session_user_id", session.userID.String()))
				return newLoginError("device_link_mismatch", "device linked to a different account. (%s)", params.profile.Username())
			}

			// The user provided a password, but the account does not have a password set.
			// Link the email to the account.
			// Use a placeholder email based on the user ID.
			if params.profile.account.Email == "" && p.placeholderEmail != "" && params.profile.HasPasswordSet() == false {
				logger.Info("Linking placeholder email to account.", zap.String("email", params.profile.ID()+"@"+p.placeholderEmail))
				if passwordProvided {
					if err := server.LinkEmail(ctx, logger, p.db, uuid.FromStringOrNil(params.profile.ID()), params.profile.ID()+"@"+p.placeholderEmail, params.authPassword); err != nil {
						return newLoginError("link_email_failed", "failed to link email: %w", err)
					}
				}
			}
		}
	}

	if params.profile == nil {
		return errors.New("account is nil")
	}

	session.Lock()
	session.userID = uuid.FromStringOrNil(params.profile.ID())
	session.SetUsername(params.profile.Username())
	session.logger = session.logger.With(
		zap.String("login_sid", session.id.String()),
		zap.String("uid", session.userID.String()),
		zap.String("xp_id", params.xpID.String()),
		zap.String("username", session.Username()),
	)
	session.Unlock()
	return nil
}

func (p *EvrPipeline) authorizeSession(ctx context.Context, logger *zap.Logger, session *sessionEVR, params *SessionParameters) error {

	var err error
	metricsTags := params.MetricsTags()

	// Get the IPQS Data
	if p.ipInfoCache != nil {
		if params.ipInfo, err = p.ipInfoCache.Get(ctx, session.clientIP); err != nil {
			logger.Debug("Failed to get IPQS details", zap.Error(err))
		}
	}
	// The account is now authenticated. Authorize the session.
	if params.profile.IsDisabled() {

		p.nk.MetricsCounterAdd("login_attempt_banned_account", nil, 1)

		logger.Info("Attempted login to banned account.",
			zap.String("xp_id", params.xpID.Token()),
			zap.String("client_ip", session.clientIP),
			zap.String("uid", params.profile.ID()),
			zap.Any("login_payload", params.loginPayload))

		return AccountDisabledError{
			message:   "Account Disabled",
			reportURL: ServiceSettings().ReportURL,
		}
	}

	// Check if the IP is on the deny list.
	if userIDs, err := LoginDeniedClientIPAddressSearch(ctx, p.nk, session.clientIP); err != nil {
		metricsTags["error"] = "failed_ip_denylist_search"
		return fmt.Errorf("failed to search for denied client address: %w", err)
	} else if len(userIDs) > 0 {
		// The IP is on the deny list.
		logger.Info("Attempted login with IP address that is on the deny list.",
			zap.String("xp_id", params.xpID.Token()),
			zap.String("client_ip", session.clientIP),
			zap.String("uid", params.profile.ID()),
			zap.Any("denied_ip_owner", userIDs),
			zap.Any("login_payload", params.loginPayload))

		metricsTags["error"] = "ip_deny_list"

		return AccountDisabledError{
			message:   "Account Disabled",
			reportURL: ServiceSettings().ReportURL,
		}
	}

	loginHistory := NewLoginHistory(params.profile.ID())
	if err := StorableReadNk(ctx, p.nk, params.profile.ID(), loginHistory, true); err != nil {
		return fmt.Errorf("failed to load login history: %w", err)
	}

	// Require IP verification, if the session is not authenticated.
	if !params.IsWebsocketAuthenticated && !loginHistory.IsAuthorizedIP(session.clientIP) {

		// IP is not authorized. Add a pending authorization entry.
		entry := loginHistory.AddPendingAuthorizationIP(params.xpID, session.clientIP, params.loginPayload)
		if err := StorableWriteNk(ctx, p.nk, params.profile.ID(), loginHistory); err != nil {
			return fmt.Errorf("failed to load login history: %w", err)
		}

		// Use the last two digits of the nanos seconds as the 2FA code.
		twoFactorCode := fmt.Sprintf("%02d", entry.CreatedAt.Nanosecond()%100)
		metricsTags["error"] = "ip_verification_required"
		if p.appBot != nil && p.appBot.dg != nil && p.appBot.dg.State != nil && p.appBot.dg.State.User != nil {
			botUsername := p.appBot.dg.State.User.Username
			if err := p.appBot.SendIPApprovalRequest(ctx, params.profile.ID(), entry, params.ipInfo); err == nil {
				return NewLocationError{
					code:        twoFactorCode,
					botUsername: botUsername,
					useDMs:      true,
				}
			} else {
				if !IsDiscordErrorCode(err, discordgo.ErrCodeCannotSendMessagesToThisUser) {
					metricsTags["error"] = "failed_send_ip_approval_request"
					return fmt.Errorf("failed to send IP approval request: %w", err)
				}
				// The user has DMs from non-friends disabled. Tell them to use the slash command instead.
				if guildID := p.discordCache.GroupIDToGuildID(params.profile.ActiveGroupID); guildID != "" {
					// Use the guild name
					if guild, err := p.discordCache.dg.Guild(guildID); err == nil {
						return NewLocationError{
							guildName: guild.Name,
							code:      twoFactorCode,
						}
					}
				} else if p.appBot.dg.State.User.Username != "" {
					// Use the bot name
					return NewLocationError{
						botUsername: p.appBot.dg.State.User.Username,
						code:        twoFactorCode,
					}
				} else {
					// Just return an error, since there's no way to verify the user.
					metricsTags["error"] = "ip_verification_failed"
					return NewLocationError{
						code: twoFactorCode,
					}
				}
			}
		}
	}

	params.ignoreDisabledAlternates = loginHistory.IgnoreDisabledAlternates
	firstIDs, _ := loginHistory.AlternateIDs()
	if params.gameModeSuspensionsByGroupID, err = CheckEnforcementSuspensions(ctx, p.nk, p.guildGroupRegistry, params.profile.ID(), firstIDs); err != nil {
		metricsTags["error"] = "failed_check_suspensions"
		return fmt.Errorf("failed to check suspensions: %w", err)
	}

	metricsTags["error"] = "nil"

	SendEvent(ctx, p.nk, &EventUserAuthenticated{
		UserID:                   params.profile.ID(),
		XPID:                     params.xpID,
		ClientIP:                 session.clientIP,
		LoginPayload:             params.loginPayload,
		IsWebSocketAuthenticated: params.IsWebsocketAuthenticated,
	})

	return nil
}

func (p *EvrPipeline) initializeSession(ctx context.Context, logger *zap.Logger, session *sessionEVR, params *SessionParameters) error {
	var err error
	serviceSettings := ServiceSettings()
	// Enable session debugging if the account metadata or global settings have Debug set.
	params.enableAllRemoteLogs = params.enableAllRemoteLogs || params.profile.EnableAllRemoteLogs || serviceSettings.EnableSessionDebug

	metricsTags := params.MetricsTags()
	defer func() {
		p.nk.MetricsCounterAdd("session_initialize", metricsTags, 1)
	}()

	metadataUpdated := false

	params.guildGroups, err = GuildUserGroupsList(ctx, p.nk, p.guildGroupRegistry, params.profile.ID())
	if err != nil {
		metricsTags["error"] = "failed_get_groups"
		return fmt.Errorf("failed to get groups: %w", err)
	}

	if len(params.guildGroups) == 0 {
		// User is not in any groups
		metricsTags["error"] = "user_not_in_any_groups"
		guildID := p.discordCache.GroupIDToGuildID(params.profile.ActiveGroupID)
		p.discordCache.QueueSyncMember(guildID, params.profile.DiscordID(), true)

		return fmt.Errorf("user is not in any groups, try again in 30 seconds")
	}

	if _, ok := params.guildGroups[params.profile.ActiveGroupID]; !ok && params.profile.GetActiveGroupID() != uuid.Nil {
		// User is not in the active group
		logger.Warn("User is not in the active group", zap.String("uid", params.profile.UserID()), zap.String("gid", params.profile.ActiveGroupID))
		params.profile.SetActiveGroupID(uuid.Nil)
	}

	// If the user is not in a group, set the active group to the group with the most members
	if params.profile.GetActiveGroupID() == uuid.Nil {
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

		params.profile.SetActiveGroupID(uuid.FromStringOrNil(groupIDs[0]))
		logger.Debug("Set active group", zap.String("uid", params.profile.UserID()), zap.String("gid", params.profile.ActiveGroupID))
		metadataUpdated = true
	}

	if ismember, err := CheckSystemGroupMembership(ctx, p.db, session.userID.String(), GroupGlobalDevelopers); err != nil {
		metricsTags["error"] = "group_check_failed"
		return fmt.Errorf("failed to check system group membership: %w", err)
	} else if ismember {
		params.isGlobalDeveloper = true
		params.isGlobalOperator = true

	} else if ismember, err := CheckSystemGroupMembership(ctx, p.db, params.profile.UserID(), GroupGlobalOperators); err != nil {
		metricsTags["error"] = "group_check_failed"
		return fmt.Errorf("failed to check system group membership: %w", err)
	} else if ismember {
		params.isGlobalOperator = true
	}

	// Update in-memory account metadata for guilds that the user has
	// the force username role.
	for groupID, gg := range params.guildGroups {
		if gg.HasRole(session.userID.String(), gg.RoleMap.UsernameOnly) {
			params.profile.SetGroupDisplayName(groupID, params.profile.Username())
		}
	}

	latencyHistory := &LatencyHistory{}
	if err := StorableReadNk(ctx, p.nk, session.userID.String(), latencyHistory, true); err != nil {
		metricsTags["error"] = "failed_load_latency_history"
		return fmt.Errorf("failed to load latency history: %w", err)
	}
	params.latencyHistory.Store(latencyHistory)

	// Load the display name history for the player.
	displayNameHistory, err := DisplayNameHistoryLoad(ctx, p.nk, session.userID.String())
	if err != nil {
		logger.Warn("Failed to load display name history", zap.Error(err))
		return fmt.Errorf("failed to load display name history: %w", err)
	}

	// Get/Set the current IGN for each guild group.
	for groupID, gg := range params.guildGroups {
		// Default to the username, or whatever was last used.
		groupIGN := params.profile.GetGroupIGNData(groupID)

		if params.userDisplayNameOverride != "" {
			// If the user has provided a display name override, use that.
			groupIGN.DisplayName = params.userDisplayNameOverride
			groupIGN.IsOverride = true
		}

		if groupIGN.DisplayName == "" {
			// Use the latest in-game name from the display name history.
			if dn, _ := displayNameHistory.LatestGroup(groupID); dn != "" {
				// If the display name history has a name for this group, default to it.
				groupIGN.GroupID = groupID
				groupIGN.DisplayName = sanitizeDisplayName(dn)
				groupIGN.IsOverride = false
			}
		}

		if !groupIGN.IsOverride {
			// Update the in-game name for the guild.
			if member, err := p.discordCache.GuildMember(gg.GuildID, params.profile.DiscordID()); err != nil {
				logger.Warn("Failed to get guild member", zap.String("guild_id", gg.GuildID), zap.String("discord_id", params.profile.DiscordID()), zap.Error(err))
			} else if memberNick := InGameName(member); memberNick != "" {
				// If the member is found, use it as their in-game name.
				groupIGN.DisplayName = memberNick
			} else if memberNick == "" {
				// If the group in-game name is empty, remove it; the active group ID will be used.
				params.profile.DeleteGroupDisplayName(groupID)
			}
		}
		// Use the in-game name from the guild member.
		params.profile.SetGroupIGNData(groupID, groupIGN)
	}

	// Check if any of the player's current in-game names are owned by someone else.
	displayNames := make([]string, 0)
	for _, dn := range params.profile.DisplayNamesByGroupID() {
		displayNames = append(displayNames, dn)
	}

	if ownerMap, err := DisplayNameOwnerSearch(ctx, p.nk, displayNames); err != nil {
		logger.Warn("Failed to check display name owner", zap.Any("display_names", displayNames), zap.Error(err))
	} else {
		// Prune the in-game names that are owned by someone else.
		for _, dn := range params.profile.DisplayNamesByGroupID() {
			if ownerIDs, ok := ownerMap[dn]; ok && !slices.Contains(ownerIDs, params.profile.ID()) {
				// This display name is owned by someone else.
				for gID, gn := range params.profile.DisplayNamesByGroupID() {
					if strings.EqualFold(gn, dn) {
						// This display name is owned by someone else.
						params.profile.DeleteGroupDisplayName(gID)
						if serviceSettings.DisplayNameInUseNotifications {
							// Notify the player that this display name is in use.
							ownerDiscordID := p.discordCache.UserIDToDiscordID(ownerIDs[0])
							go func() {
								if err := p.discordCache.SendDisplayNameInUseNotification(ctx, params.profile.DiscordID(), ownerDiscordID, dn, params.profile.Username()); err != nil {
									logger.Warn("Failed to send display name in use notification", zap.Error(err))
								}
							}()
						}
					}
				}
			}
		}
	}

	// Update the in-game names for the player (in the display name history).
	igns := make([]string, 0, len(params.profile.DisplayNamesByGroupID()))
	for groupID := range params.profile.DisplayNamesByGroupID() {
		igns = append(igns, params.profile.GetGroupIGN(groupID))
	}
	displayNameHistory.ReplaceInGameNames(igns)

	// Update the display name history for the active group, marking this name as an in-game-name.
	// Use the current display name from the profile instead of querying the potentially stale history
	activeGroupDisplayName := params.profile.GetGroupIGN(params.profile.ActiveGroupID)
	displayNameHistory.Update(params.profile.ActiveGroupID, activeGroupDisplayName, params.profile.Username(), true)

	if err := DisplayNameHistoryStore(ctx, p.nk, session.userID.String(), displayNameHistory); err != nil {
		logger.Warn("Failed to store display name history", zap.Error(err))
	}

	if settings, err := LoadMatchmakingSettings(ctx, p.nk, session.userID.String()); err != nil {
		logger.Warn("Failed to load matchmaking settings", zap.Error(err))
		return fmt.Errorf("failed to load matchmaking settings: %w", err)
	} else {
		updated := false
		// If the player account is less than 7 days old, then assign the "green" division to the player.
		if time.Since(params.profile.account.User.CreateTime.AsTime()) < time.Duration(serviceSettings.Matchmaking.GreenDivisionMaxAccountAgeDays)*24*time.Hour {
			if !slices.Contains(settings.Divisions, "green") {
				settings.Divisions = append(settings.Divisions, "green")
				updated = true
			}
			if slices.Contains(settings.ExcludedDivisions, "green") {
				updated = true
				// Remove the "green" division from the excluded divisions.
				for i := 0; i < len(settings.ExcludedDivisions); i++ {
					if settings.ExcludedDivisions[i] == "green" {
						settings.ExcludedDivisions = slices.Delete(settings.ExcludedDivisions, i, i+1)
						i--
					}
				}

			}

		} else {
			if slices.Contains(settings.Divisions, "green") {
				// Remove the "green" division from the divisions.
				updated = true
				for i := 0; i < len(settings.Divisions); i++ {
					// Remove the "green" division from the divisions.
					if settings.Divisions[i] == "green" {
						settings.Divisions = slices.Delete(settings.Divisions, i, i+1)
						i--
					}
				}
			}
			if !slices.Contains(settings.ExcludedDivisions, "green") {
				updated = true
				// Add the "green" division to the excluded divisions.
				settings.ExcludedDivisions = append(settings.ExcludedDivisions, "green")

			}
		}

		if updated {
			if err := StoreMatchmakingSettings(ctx, p.nk, session.userID.String(), settings); err != nil {
				logger.Warn("Failed to save matchmaking settings", zap.Error(err))
				return fmt.Errorf("failed to save matchmaking settings: %w", err)
			}
		}

		params.matchmakingSettings = &settings
	}

	if !params.profile.IgnoreBrokenCosmetics {
		if u := params.profile.FixBrokenCosmetics(); u {
			metadataUpdated = true
		}
	}
	eqconfig := NewEarlyQuitConfig()
	if err := StorableReadNk(ctx, p.nk, params.profile.ID(), eqconfig, true); err != nil {
		logger.Warn("Failed to load early quitter config", zap.Error(err))
	} else {
		params.earlyQuitConfig.Store(eqconfig)
	}

	if metadataUpdated {
		if err := p.nk.AccountUpdateId(ctx, params.profile.ID(), "", params.profile.MarshalMap(), params.profile.GetActiveGroupDisplayName(), "", "", "", ""); err != nil {
			metricsTags["error"] = "failed_update_profile"
			return fmt.Errorf("failed to update user profile: %w", err)
		}
	}

	s := session
	// Register initial status tracking and presence(s) for this session.
	s.statusRegistry.Follow(s.id, map[uuid.UUID]struct{}{s.userID: {}})

	// Both notification and status presence.
	s.tracker.TrackMulti(ctx, s.id, []*server.TrackerOp{
		// EVR packet data stream for the login session by user ID, and service ID, with EVR ID
		{
			Stream: server.PresenceStream{Mode: StreamModeService, Subject: s.userID, Label: StreamLabelLoginService},
			Meta:   server.PresenceMeta{Format: s.Format(), Username: session.Username(), Hidden: false},
		},
		// EVR packet data stream for the login session by session ID and service ID, with EVR ID
		{
			Stream: server.PresenceStream{Mode: StreamModeService, Subject: s.id, Label: StreamLabelLoginService},
			Meta:   server.PresenceMeta{Format: s.Format(), Username: session.Username(), Hidden: false},
		},
		// Notification presence.
		{
			Stream: server.PresenceStream{Mode: server.StreamModeNotifications, Subject: s.userID},
			Meta:   server.PresenceMeta{Format: s.Format(), Username: s.Username(), Hidden: false},
		},

		// Status presence.
		{
			Stream: server.PresenceStream{Mode: server.StreamModeStatus, Subject: s.userID},
			Meta:   server.PresenceMeta{Format: s.Format(), Username: s.Username(), Status: ""},
		},
	}, s.userID)

	return nil
}

const (
	StorageCollectionChannelInfo = "ChannelInfo"
)

var (
	channelInfoIndex = StorableIndexMeta{
		Name:       "ChannelInfoIndex",
		Collection: StorageCollectionChannelInfo,
		Fields:     []string{"channeluuid"},
		MaxEntries: 200,
		IndexOnly:  false,
	}
)

func (p *EvrPipeline) channelInfoRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	_ = in.(*evr.ChannelInfoRequest)

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	groupID := params.profile.GetActiveGroupID()

	if groupID.IsNil() {
		return fmt.Errorf("active group is nil")
	}

	// Use the Index
	query := "+key:%s" + groupID.String()
	objs, _, err := p.nk.StorageIndexList(ctx, SystemUserID, channelInfoIndex.Name, query, 1, nil, "")
	if err != nil {
		return fmt.Errorf("failed to query channel info: %w", err)
	}
	if len(objs.Objects) > 0 {
		// send the document to the client
		return session.SendEVR(Envelope{
			ServiceType: ServiceTypeLogin,
			Messages: []evr.Message{
				&evr.ChannelInfoResponse{
					ChannelInfo: json.RawMessage(objs.Objects[0].GetValue()),
				},
			},
			State: RequireStateUnrequired,
		})
	}

	// Get the guild group for the active group ID.
	g, ok := params.guildGroups[groupID.String()]
	if !ok {
		return fmt.Errorf("guild group not found: %s", groupID.String())
	}

	resource := evr.NewChannelInfoResource()

	resource.Groups = [4]evr.ChannelGroup{}
	for i := range resource.Groups {
		resource.Groups[i] = evr.ChannelGroup{
			ChannelUuid:  strings.ToUpper(g.ID().String()),
			Name:         g.Name(),
			Description:  g.Description(),
			Rules:        g.Description() + "\n" + g.State.RulesText,
			RulesVersion: 1,
			Link:         fmt.Sprintf("https://discord.gg/channel/%s", g.GuildID),
			Priority:     uint64(i),
			RAD:          true,
		}
	}

	message := evr.NewSNSChannelInfoResponse(resource)
	data, err := json.Marshal(resource)
	if err != nil {
		return fmt.Errorf("failed to marshal channel info: %w", err)
	}
	// Store the document in the storage
	p.nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      StorageCollectionChannelInfo,
			Key:             groupID.String(),
			UserID:          SystemUserID,
			Value:           string(data),
			PermissionRead:  2,
			PermissionWrite: 0,
		},
	})
	// send the document to the client
	return session.SendEVR(Envelope{
		ServiceType: ServiceTypeLogin,
		Messages: []evr.Message{
			message,
		},
		State: RequireStateUnrequired,
	})
}

func (p *EvrPipeline) loggedInUserProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) (err error) {
	request := in.(*evr.LoggedInUserProfileRequest)
	// Start a timer to add to the metrics
	timer := time.Now()
	defer func() { p.metrics.CustomTimer("loggedInUserProfileRequest", nil, time.Since(timer)) }()

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}
	userID := session.userID.String()
	groupID := params.profile.GetActiveGroupID().String()

	modes := []evr.Symbol{
		evr.ModeArenaPublic,
		evr.ModeCombatPublic,
	}

	serverProfile, err := PlayerProfileFromParameters(ctx, p.db, p.nk, params, groupID, modes, 0)
	if err != nil {
		return fmt.Errorf("failed to get server profile: %w", err)
	}

	if err := StorePlayerProfileData(ctx, server.NewRuntimeGoLogger(logger), p.nk, userID, serverProfile); err != nil {
		logger.Warn("Failed to store player profile data", zap.Error(err))
	}

	clientProfile := NewClientProfile(ctx, params.profile, serverProfile)

	// Check if the user is required to go through community values
	journal := NewGuildEnforcementJournal(userID)
	if err := StorableReadNk(ctx, p.nk, userID, journal, true); err != nil {
		logger.Warn("Failed to search for community values", zap.Error(err))
	} else if journal.CommunityValuesCompletedAt.IsZero() {
		clientProfile.Social.CommunityValuesVersion = 0
	}

	return session.SendEVR(Envelope{
		ServiceType: ServiceTypeLogin,
		Messages: []evr.Message{
			evr.NewLoggedInUserProfileSuccess(request.EvrID, clientProfile, serverProfile),
		},
	})
}

func (p *EvrPipeline) updateClientProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	request := in.(*evr.UpdateClientProfile)

	if err := p.handleClientProfileUpdate(ctx, logger, session, request.XPID, request.Payload); err != nil {
		return session.SendEVR(Envelope{
			ServiceType: ServiceTypeLogin,
			Messages: []evr.Message{
				evr.NewUpdateProfileFailure(request.XPID, uint64(400), err.Error()),
			},
		})
	}

	return session.SendEVR(Envelope{
		ServiceType: ServiceTypeLogin,
		Messages: []evr.Message{
			evr.NewUpdateProfileSuccess(&request.XPID),
		},
		State: RequireStateUnrequired,
	})
}

func (p *EvrPipeline) handleClientProfileUpdate(ctx context.Context, logger *zap.Logger, session *sessionEVR, evrID evr.XPID, update evr.ClientProfile) error {
	// Set the EVR ID from the context
	update.EvrID = evrID

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}
	userID := session.userID.String()
	groupID := params.profile.GetActiveGroupID().String()
	gg := p.guildGroupRegistry.Get(groupID)
	if gg == nil {
		return fmt.Errorf("guild group not found: %s", groupID)
	}

	hasCompleted := update.Social.CommunityValuesVersion != 0

	if hasCompleted {

		// Check if the user is required to go through community values
		journal := NewGuildEnforcementJournal(userID)
		if err := StorableReadNk(ctx, p.nk, userID, journal, true); err != nil {
			logger.Warn("Failed to search for community values", zap.Error(err))
		} else if journal.CommunityValuesCompletedAt.IsZero() {

			journal.CommunityValuesCompletedAt = time.Now().UTC()

			if err := StorableWriteNk(ctx, p.nk, userID, journal); err != nil {
				logger.Warn("Failed to write community values", zap.Error(err))
			}

			// Log the audit message
			if _, err := p.appBot.LogAuditMessage(ctx, groupID, fmt.Sprintf("User <@%s> (%s) has accepted the community values.", params.DiscordID(), params.profile.Username()), false); err != nil {
				logger.Warn("Failed to log audit message", zap.Error(err))
			}
		}

	}

	profile, err := EVRProfileLoad(ctx, p.nk, userID)
	if err != nil {
		return fmt.Errorf("failed to load account profile: %w", err)
	}

	profile.TeamName = update.TeamName

	profile.CombatLoadout = CombatLoadout{
		CombatWeapon:       update.CombatWeapon,
		CombatGrenade:      update.CombatGrenade,
		CombatDominantHand: update.CombatDominantHand,
		CombatAbility:      update.CombatAbility,
	}

	profile.LegalConsents = update.LegalConsents
	profile.GhostedPlayers = update.GhostedPlayers.Players
	profile.MutedPlayers = update.MutedPlayers.Players
	profile.NewUnlocks = update.NewUnlocks
	profile.CustomizationPOIs = update.Customization

	if err := EVRProfileUpdate(ctx, p.nk, userID, profile); err != nil {
		return fmt.Errorf("failed to update account profile: %w", err)
	}

	params.profile = profile
	StoreParams(ctx, &params)
	return nil
}

func (p *EvrPipeline) remoteLogSetv3(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
	request := in.(*evr.RemoteLogSet)

	go func() {
		if err := p.processRemoteLogSets(ctx, logger, session, request.EvrID, request); err != nil {
			logger.Error("Failed to process remote log set", zap.Error(err))
		}
	}()

	return nil
}

func (p *EvrPipeline) documentRequest(ctx context.Context, logger *zap.Logger, session *sessionEVR, in evr.Message) error {
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

			eulaVersion := params.profile.LegalConsents.EulaVersion
			gaVersion := params.profile.LegalConsents.GameAdminVersion
			document = evr.NewEULADocument(int(eulaVersion), int(gaVersion), request.Language, "https://github.com/EchoTools", "Blank EULA for NoVR clients. You should only see this once.")
			return session.SendEVR(Envelope{
				ServiceType: ServiceTypeLogin,
				Messages: []evr.Message{
					evr.NewDocumentSuccess(document),
				},
				State: RequireStateUnrequired,
			})
		}

		document, err = p.generateEULA(ctx, logger, request.Language)
		if err != nil {
			return fmt.Errorf("failed to get eula document: %w", err)
		}
		return session.SendEVR(Envelope{
			ServiceType: ServiceTypeLogin,
			Messages: []evr.Message{
				evr.NewDocumentSuccess(document),
			},
			State: RequireStateUnrequired,
		})

	default:
		return fmt.Errorf("unknown document: %s,%s", request.Language, request.Type)
	}
}

func (p *EvrPipeline) generateEULA(ctx context.Context, logger *zap.Logger, language string) (evr.EULADocument, error) {
	// Retrieve the contents from storage
	key := fmt.Sprintf("eula,%s", language)
	document := evr.DefaultEULADocument(language)

	var ts time.Time

	objs, _, err := p.nk.StorageIndexList(ctx, SystemUserID, DocumentStorageCollection, fmt.Sprintf("+key:%s", key), 1, nil, "")

	if err != nil {
		return document, fmt.Errorf("failed to read EULA: %w", err)
	} else if len(objs.Objects) > 0 {
		if err := json.Unmarshal([]byte(objs.Objects[0].Value), &document); err != nil {
			return document, fmt.Errorf("failed to unmarshal EULA: %w", err)
		}
		ts = objs.Objects[0].UpdateTime.AsTime().UTC()
	} else {
		// If the document doesn't exist, store the object
		jsonBytes, err := json.Marshal(document)
		if err != nil {
			return document, fmt.Errorf("failed to marshal EULA: %w", err)
		}

		if _, err = p.nk.StorageWrite(ctx, []*runtime.StorageWrite{{
			Collection:      DocumentStorageCollection,
			Key:             key,
			Value:           string(jsonBytes),
			PermissionRead:  0,
			PermissionWrite: 0,
		}}); err != nil {
			return document, fmt.Errorf("failed to write EULA: %w", err)
		}
		ts = time.Now().UTC()
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

	if data, err = GetPlayerProfileData(ctx, server.NewRuntimeGoLogger(logger), p.nk, session.userID.String(), request.XPID); err != nil {
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
