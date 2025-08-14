package server

import (
	"context"
	"encoding/json"
	"os"

	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/muesli/reflow/wordwrap"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	DocumentStorageCollection = "GameDocuments"
)

type DeviceNotLinkedError struct {
	code        string
	botUsername string
}

func (e DeviceNotLinkedError) Error() string {
	return strings.Join([]string{
		fmt.Sprintf("Your Code is: >>> %s <<<", e.code),
		ServiceSettings().LinkInstructions,
	}, "\n")
}

func (e DeviceNotLinkedError) Is(target error) bool {
	_, ok := target.(DeviceNotLinkedError)
	return ok
}

type AccountDisabledError struct {
	message   string
	reportURL string
}

func (e AccountDisabledError) Error() string {
	return strings.Join([]string{
		"Account disabled by EchoVRCE Admins",
		e.message,
		"Report issues at " + e.reportURL,
	}, "\n")
}

func (e AccountDisabledError) Is(target error) bool {
	_, ok := target.(AccountDisabledError)
	return ok
}

type NewLocationError struct {
	guildName   string
	code        string
	botUsername string
	useDMs      bool
}

func (e NewLocationError) Error() string {
	if e.useDMs {
		// DM was successful
		return strings.Join([]string{
			"Please authorize this new location.",
			fmt.Sprintf("Check your Discord DMs from @%s.", e.botUsername),
			fmt.Sprintf("Select code >>> %s <<<", e.code),
		}, "\n")
	} else if e.guildName != "" {
		// DMs were blocked, Use the guild name if it's available
		return strings.Join([]string{
			"Authorize new location:",
			fmt.Sprintf("Go to %s, type /verify", e.guildName),
			fmt.Sprintf("When prompted, select code >>> %s <<<", e.code),
		}, "\n")
	} else if e.botUsername != "" {
		// DMs were blocked, Use the bot username if it's available
		return strings.Join([]string{
			"Authorize this new location by typing:",
			"/verify",
			fmt.Sprintf("and select code >>> %s <<< in a guild with the @%s bot.", e.code, e.botUsername),
		}, "\n")
	} else {
		// DMs were blocked, but no bot username was provided
		return "New location detected. Please contact EchoVRCE support."
	}
}

// loginRequest handles the login request from the client.
func (p *EvrPipeline) loginRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.LoginRequest)

	if s := ServiceSettings(); s.DisableLoginMessage != "" {
		if err := session.SendEvrUnrequire(evr.NewLoginFailure(request.XPID, "System is Temporarily Unavailable:\n"+s.DisableLoginMessage)); err != nil {
			// If there's an error, prefix it with the XPID
			return fmt.Errorf("failed to send LoginFailure: %w", err)
		}
	}

	if request.Payload == (evr.LoginProfile{}) {
		return errors.New("login profile is empty")
	}

	// Start a timer to add to the metrics
	timer := time.Now()

	// Load the session parameters.
	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	// Set the basic parameters
	params.loginSession = session
	params.xpID = request.XPID
	params.loginPayload = &request.Payload

	logger = logger.With(zap.String("xpid", request.XPID.String()))

	// Process the login request and populate the session parameters.
	if err := p.processLoginRequest(ctx, logger, session, &params); err != nil {

		discordID := ""
		if userID, err := GetUserIDByDeviceID(ctx, p.db, request.XPID.String()); err == nil {
			discordID = p.discordCache.UserIDToDiscordID(userID)
		} else if !errors.Is(err, DeviceNotLinkedError{}) {
			logger.Debug("Failed to get user ID by device ID", zap.Error(err))
		}

		errMessage := formatLoginErrorMessage(request.XPID, discordID, err)

		return session.SendEvrUnrequire(evr.NewLoginFailure(request.XPID, errMessage))
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

	p.nk.metrics.CustomCounter("login_success", tags, 1)
	p.nk.metrics.CustomTimer("login_process_latency", params.MetricsTags(), time.Since(timer))

	// Set the game settings based on the service settings
	gameSettings := evr.NewDefaultGameSettings()
	if params.enableAllRemoteLogs {
		gameSettings.RemoteLogSocial = true
		gameSettings.RemoteLogWarnings = true
		gameSettings.RemoteLogErrors = true
		gameSettings.RemoteLogRichPresence = true
		gameSettings.RemoteLogMetrics = true
	}

	return session.SendEvr(
		evr.NewLoginSuccess(session.id, request.XPID),
		unrequireMessage,
		gameSettings,
	)
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

func formatLoginErrorMessage(xpID evr.EvrId, discordID string, err error) string {
	errContent := ""
	if e, ok := status.FromError(err); ok {
		errContent = e.Message()
	} else {
		errContent = err.Error()
	}

	// Format the error message with the XPID prefix
	if discordID == "" {
		errContent = fmt.Sprintf("[%s]\n %s", xpID.String(), errContent)
	} else {
		errContent = fmt.Sprintf("[XPID:%s / Discord:%s]\n %s", xpID.String(), discordID, errContent)
	}

	// Replace ": " with ":\n" for better readability
	errContent = strings.Replace(errContent, ": ", ":\n", 2)

	// Word wrap the error message
	errContent = wordwrap.String(errContent, 60)

	return errContent
}

func (p *EvrPipeline) processLoginRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, params *SessionParameters) error {

	var err error
	if err = p.authenticateSession(ctx, logger, session, params); err != nil {
		return err
	}

	loginMigrations := []UserMigrater{}
	if err = MigrateUser(ctx, logger, p.nk, p.db, session.userID.String(), loginMigrations); err != nil {
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

// authenticateSession handles the authentication of the login connection.
func (p *EvrPipeline) authenticateSession(ctx context.Context, logger *zap.Logger, session *sessionWS, params *SessionParameters) (err error) {

	metricsTags := params.MetricsTags()

	defer func() {
		p.nk.MetricsCounterAdd("session_authenticate", metricsTags, 1)
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
	params.profile, err = AccountGetDeviceID(ctx, p.db, p.nk, params.xpID.String())
	switch status.Code(err) {
	// The device is not linked to an account.
	case codes.NotFound:

		metricsTags["device_linked"] = "false"

		// the session is authenticated. Automatically link the device.
		if !session.userID.IsNil() {
			if err := p.nk.LinkDevice(ctx, session.UserID().String(), params.xpID.String()); err != nil {
				metricsTags["error"] = "failed_link_device"
				return fmt.Errorf("failed to link device: %w", err)
			}
			params.profile, err = AccountGetDeviceID(ctx, p.db, p.nk, params.xpID.String())
			if err != nil {
				logger.Warn("Failed to get account after linking device", zap.Error(err))
				return fmt.Errorf("failed to get account after linking device: %w", err)
			}

			// The session is not authenticated. Create a link ticket.
		} else {

			if linkTicket, err := p.linkTicket(ctx, params.xpID, session.clientIP, params.loginPayload); err != nil {

				metricsTags["error"] = "link_ticket_error"

				return fmt.Errorf("error creating link ticket: %s", err)
			} else {

				return DeviceNotLinkedError{
					code:        linkTicket.Code,
					botUsername: p.appBot.dg.State.User.Username,
				}
			}
		}

	// The device is linked to an account.
	case codes.OK:

		metricsTags["device_linked"] = "true"

		var (
			requiresPasswordAuth    = params.profile.HasPasswordSet()
			authenticatedViaSession = !session.userID.IsNil()
			isAccountMismatched     = params.profile.ID() != session.userID.String()
			passwordProvided        = params.authPassword != ""
		)

		if requiresPasswordAuth {

			if !authenticatedViaSession {
				// The session authentication was not successful.
				metricsTags["error"] = "session_auth_failed"
				return errors.New("session authentication failed: account requires password authentication")
			}
		} else {

			if authenticatedViaSession && isAccountMismatched {
				// The device is linked to a different account.
				metricsTags["error"] = "device_link_mismatch"
				logger.Error("Device is linked to a different account.", zap.String("device_user_id", params.profile.ID()), zap.String("session_user_id", session.userID.String()))
				return fmt.Errorf("device linked to a different account. (%s)", params.profile.Username())
			}

			if passwordProvided {
				// This is the first time setting the password.
				if err := LinkEmail(ctx, logger, p.db, uuid.FromStringOrNil(params.profile.ID()), params.profile.ID()+"@"+p.placeholderEmail, params.authPassword); err != nil {
					metricsTags["error"] = "failed_link_email"
					return fmt.Errorf("failed to link email: %w", err)
				}
			}

		}
	}

	// Replace the session context with a derived one that includes the login session ID and the EVR ID
	ctx = session.Context()
	session.Lock()
	if params.profile == nil {
		session.Unlock()
		return errors.New("account is nil")
	}
	session.userID = uuid.FromStringOrNil(params.profile.ID())
	session.SetUsername(params.profile.Username())
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

	p.nk.MetricsCounterAdd("session_authorize", metricsTags, 1)

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
			zap.String("xpid", params.xpID.Token()),
			zap.String("client_ip", session.clientIP),
			zap.String("uid", params.profile.ID()),
			zap.Any("login_payload", params.loginPayload))

		metricsTags["error"] = "account_disabled"

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
			zap.String("xpid", params.xpID.Token()),
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
	if err := StorableRead(ctx, p.nk, params.profile.ID(), loginHistory, true); err != nil {
		return fmt.Errorf("failed to load login history: %w", err)
	}

	// Require IP verification, if the session is not authenticated.
	if !params.IsWebsocketAuthenticated && !loginHistory.IsAuthorizedIP(session.clientIP) {

		// IP is not authorized. Add a pending authorization entry.
		entry := loginHistory.AddPendingAuthorizationIP(params.xpID, session.clientIP, params.loginPayload)
		if err := StorableWrite(ctx, p.nk, params.profile.ID(), loginHistory); err != nil {
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

func (p *EvrPipeline) initializeSession(ctx context.Context, logger *zap.Logger, session *sessionWS, params *SessionParameters) error {
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
	if err := StorableRead(ctx, p.nk, session.userID.String(), latencyHistory, true); err != nil {
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

	if !params.profile.Options.AllowBrokenCosmetics {
		if u := params.profile.FixBrokenCosmetics(); u {
			metadataUpdated = true
		}
	}
	eqconfig := NewEarlyQuitConfig()
	if err := StorableRead(ctx, p.nk, params.profile.ID(), eqconfig, true); err != nil {
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

	groupID := params.profile.GetActiveGroupID()

	if groupID.IsNil() {
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
			Rules:        g.Description() + "\n" + g.State.RulesText,
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
	defer func() { p.nk.metrics.CustomTimer("loggedInUserProfileRequest", nil, time.Since(timer)) }()

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

	serverProfile, err := UserServerProfileFromParameters(ctx, logger, p.db, p.nk, params, groupID, modes, 0)
	if err != nil {
		return fmt.Errorf("failed to get server profile: %w", err)
	}

	p.profileCache.Store(session.id, *serverProfile)

	clientProfile := NewClientProfile(ctx, params.profile, serverProfile)

	// Check if the user is required to go through community values
	journal := NewGuildEnforcementJournal(userID)
	if err := StorableRead(ctx, p.nk, userID, journal, true); err != nil {
		logger.Warn("Failed to search for community values", zap.Error(err))
	} else if journal.CommunityValuesCompletedAt.IsZero() {
		clientProfile.Social.CommunityValuesVersion = 0
	}

	return session.SendEvr(evr.NewLoggedInUserProfileSuccess(request.EvrID, clientProfile, serverProfile))
}

func (p *EvrPipeline) updateClientProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.UpdateClientProfile)

	if err := p.handleClientProfileUpdate(ctx, logger, session, request.XPID, request.Payload); err != nil {
		if err := session.SendEvr(evr.NewUpdateProfileFailure(request.XPID, uint64(400), err.Error())); err != nil {
			logger.Error("Failed to send UpdateProfileFailure", zap.Error(err))
		}
	}

	return session.SendEvrUnrequire(evr.NewUpdateProfileSuccess(&request.XPID))
}

func (p *EvrPipeline) handleClientProfileUpdate(ctx context.Context, logger *zap.Logger, session *sessionWS, evrID evr.EvrId, update evr.ClientProfile) error {
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
		if err := StorableRead(ctx, p.nk, userID, journal, true); err != nil {
			logger.Warn("Failed to search for community values", zap.Error(err))
		} else if journal.CommunityValuesCompletedAt.IsZero() {

			journal.CommunityValuesCompletedAt = time.Now().UTC()

			if err := StorableWrite(ctx, p.nk, userID, journal); err != nil {
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

			eulaVersion := params.profile.LegalConsents.EulaVersion
			gaVersion := params.profile.LegalConsents.GameAdminVersion
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

	var ts time.Time

	if objs, err := p.nk.StorageRead(ctx, []*runtime.StorageRead{{
		Collection: DocumentStorageCollection,
		Key:        key,
		UserID:     SystemUserID,
	}}); err != nil {
		return document, fmt.Errorf("failed to read EULA: %w", err)
	} else if len(objs) > 0 {
		if err := json.Unmarshal([]byte(objs[0].Value), &document); err != nil {
			return document, fmt.Errorf("failed to unmarshal EULA: %w", err)
		}
		ts = objs[0].UpdateTime.AsTime().UTC()
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

func mostRecentThursday() time.Time {
	now := time.Now()
	offset := (int(now.Weekday()) - int(time.Thursday) + 7) % 7
	return now.AddDate(0, 0, -offset).UTC()
}

// A profile update request is sent from the game server's login connection.
// It is sent 45 seconds before the sessionend is sent, right after the match ends.
func (p *EvrPipeline) userServerProfileUpdateRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.UserServerProfileUpdateRequest)

	if data, err := json.MarshalIndent(in, "", "  "); err != nil {
		logger.Warn("Failed to marshal profile update request", zap.Error(err))
	} else if len(data) > 0 {
		logger.Debug("Received user server profile update request", zap.Any("update", in))
		if err := os.WriteFile("/tmp/evr_profile_update.json", data, 0644); err != nil {
			logger.Warn("Failed to write profile update request to file", zap.Error(err))
		}
	}

	if err := session.SendEvr(evr.NewUserServerProfileUpdateSuccess(request.EvrID)); err != nil {
		logger.Warn("Failed to send UserServerProfileUpdateSuccess", zap.Error(err))
	}
	payload := &evr.UpdatePayload{}

	if err := json.Unmarshal(request.Payload, payload); err != nil {
		return fmt.Errorf("failed to unmarshal update payload: %w", err)
	}

	// Ignore anything but statistics updates.
	if payload.Update.Statistics == nil {
		return nil
	}

	mode := evr.Symbol(payload.MatchType)
	if mode == evr.ModeArenaPublic || mode == evr.ModeArenaPrivate {
		// Arena match stats are handled by EventRemoteLogSet
		return nil
	}

	matchID, err := NewMatchID(uuid.UUID(payload.SessionID), p.node)
	if err != nil {
		return fmt.Errorf("failed to generate matchID: %w", err)
	}
	// Validate the player was in the session
	label, err := MatchLabelByID(ctx, p.nk, matchID)
	if err != nil {
		return fmt.Errorf("failed to get match label: %w", err)
	}

	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
		defer cancel()

		if err := p.processUserServerProfileUpdate(ctx, logger, request.EvrID, label, payload); err != nil {
			logger.Error("Failed to process user server profile update", zap.Error(err))
		}
	}()
	return nil
}

func (p *EvrPipeline) processUserServerProfileUpdate(ctx context.Context, logger *zap.Logger, evrID evr.EvrId, label *MatchLabel, payload *evr.UpdatePayload) error {
	// Get the player's information
	playerInfo := label.GetPlayerByEvrID(evrID)

	// If the player isn't in the match, or isn't a player, do not update the stats
	if playerInfo == nil || (playerInfo.Team != BlueTeam && playerInfo.Team != OrangeTeam) {
		return fmt.Errorf("non-player profile update request: %s", evrID.String())
	}
	logger = logger.With(zap.String("player_uid", playerInfo.UserID), zap.String("player_sid", playerInfo.SessionID), zap.String("player_xpid", playerInfo.EvrID.String()))

	var profile *EVRProfile
	// Decrease the early quitter count for the player
	if playerSession := p.nk.sessionRegistry.Get(uuid.FromStringOrNil(playerInfo.SessionID)); playerSession != nil {
		eqconfig := NewEarlyQuitConfig()
		if err := StorableRead(ctx, p.nk, playerInfo.UserID, eqconfig, true); err != nil {
			logger.Warn("Failed to load early quitter config", zap.Error(err))
		} else {
			eqconfig.IncrementCompletedMatches()
			if err := StorableWrite(ctx, p.nk, playerInfo.UserID, eqconfig); err != nil {
				logger.Warn("Failed to store early quitter config", zap.Error(err))
			} else if session := p.sessionRegistry.Get(playerSession.ID()); session != nil {
				if params, ok := LoadParams(session.Context()); ok {
					params.earlyQuitConfig.Store(eqconfig)
				}
			}
		}
	}

	var err error
	if profile == nil {
		// If the player isn't a member of the group, do not update the stats
		profile, err = EVRProfileLoad(ctx, p.nk, playerInfo.UserID)
		if err != nil {
			return fmt.Errorf("failed to get account profile: %w", err)
		}
	}
	groupIDStr := label.GetGroupID().String()

	if _, ok := profile.GetGroupDisplayName(groupIDStr); !ok {
		logger.Warn("Player is not a member of the group", zap.String("uid", playerInfo.UserID), zap.String("gid", groupIDStr))
		return nil
	}

	serviceSettings := ServiceSettings()

	validModes := []evr.Symbol{evr.ModeArenaPublic, evr.ModeCombatPublic}

	if serviceSettings.UseSkillBasedMatchmaking() && slices.Contains(validModes, label.Mode) {

		// Determine winning team
		blueWins := playerInfo.Team == BlueTeam && payload.IsWinner()
		ratings := CalculateNewPlayerRatings(label.Players, blueWins)
		if rating, ok := ratings[playerInfo.SessionID]; ok {
			if err := MatchmakingRatingStore(ctx, p.nk, playerInfo.UserID, playerInfo.DiscordID, playerInfo.DisplayName, groupIDStr, label.Mode, rating); err != nil {
				logger.Warn("Failed to record percentile to leaderboard", zap.Error(err))
			}
		} else {
			logger.Warn("Failed to get player rating", zap.String("sessionID", playerInfo.SessionID))
		}

		// Calculate a new rank percentile
		if rankPercentile, err := CalculateSmoothedPlayerRankPercentile(ctx, logger, p.db, p.nk, playerInfo.UserID, groupIDStr, label.Mode); err != nil {
			logger.Error("Failed to calculate new player rank percentile", zap.Error(err))
			// Store the rank percentile in the leaderboards.
		} else if err := MatchmakingRankPercentileStore(ctx, p.nk, playerInfo.UserID, playerInfo.DisplayName, groupIDStr, label.Mode, rankPercentile); err != nil {
			logger.Warn("Failed to record percentile to leaderboard", zap.Error(err))
		}
	}

	// Update the player's statistics, if the service settings allow it
	if serviceSettings.DisableStatisticsUpdates {
		return nil
	}

	return SendEvent(ctx, p.nk, &EventServerProfileUpdate{
		UserID:      playerInfo.UserID,
		GroupID:     groupIDStr,
		DisplayName: playerInfo.DisplayName,
		Mode:        label.Mode,
		Update:      payload.Update,
	})
}

func (p *EvrPipeline) otherUserProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.OtherUserProfileRequest)

	tags := map[string]string{
		"error": "nil",
	}
	startTime := time.Now()

	defer func() {
		p.nk.metrics.CustomCounter("profile_request_count", tags, 1)
		p.nk.metrics.CustomTimer("profile_request_latency", tags, time.Since(startTime))
	}()

	var ok bool
	var data json.RawMessage

	if data, ok = p.profileCache.Load(request.EvrId); !ok {
		logger.Error("Profile does not exist in cache.", zap.String("evrId", request.EvrId.String()))
		return nil
	}
	/*
		// If this user is an Enforcer let them see how many times that player has been reported in the past week
		params, ok := LoadParams(ctx)
		if !ok {
			return errors.New("session parameters not found")
		}

		userID := session.userID.String()
		groupID := params.profile.GetActiveGroupID().String()

		if gg := p.guildGroupRegistry.Get(groupID); gg != nil {
			if gg.EnableEnforcementCountInNames && gg.IsEnforcer(userID) {
				serverProfile := &evr.ServerProfile{}
				if err := json.Unmarshal(data, serverProfile); err != nil {
					logger.Error("Failed to unmarshal server profile", zap.Error(err))
					return fmt.Errorf("failed to unmarshal server profile: %w", err)
				}

				if targetUserID, err := GetUserIDByDeviceID(ctx, p.db, request.EvrId.String()); err != nil {
					logger.Error("Failed to get user ID by device ID", zap.Error(err))
				} else if targetUserID != "" {
					count := 0
					// Get the number of reports for this user in the last week
					if journals, err := EnforcementJournalSearch(ctx, p.nk, []string{targetUserID}); err != nil {
						logger.Error("Failed to search for enforcement records", zap.Error(err))
					} else if len(journals) > 0 {
						for _, journal := range journals {
							for _, r := range journal.GroupRecords(groupID) {
								if journal.IsVoid(groupID, r.ID) {
									continue
								}
								// Show all reports for the past week
								if r.CreatedAt.After(time.Now().Add(-time.Hour * 24 * 7)) {
									count += 1
								}
							}
						}
					}
					if count > 0 {
						// Add the count to the players display name
						serverProfile.DisplayName = fmt.Sprintf("%s [%d]", serverProfile.DisplayName, count)

						data, err = json.Marshal(serverProfile)
						if err != nil {
							logger.Error("Failed to marshal server profile", zap.Error(err))
							return fmt.Errorf("failed to marshal server profile: %w", err)
						}
					}
				}
			}
		}
	*/

	response := &evr.OtherUserProfileSuccess{
		EvrId:             request.EvrId,
		ServerProfileJSON: data,
	}

	p.nk.metrics.CustomGauge("profile_size_bytes", nil, float64(len(data)))

	if err := session.SendEvr(response); err != nil {
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
