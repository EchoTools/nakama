package service

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

var (
	ErrDeviceNotLinked         = errors.New("device not linked to an account")
	ErrDeviceLinkMismatch      = errors.New("device linked to a different account")
	ErrMissingDiscordID        = errors.New("missing Discord ID for authentication")
	ErrMissingPassword         = errors.New("missing password for Discord authentication")
	ErrGetAccount              = errors.New("error getting account")
	ErrFailedLinkDevice        = errors.New("failed to link device")
	ErrLinkTicket              = errors.New("error creating link ticket")
	ErrDeviceLinkedToOther     = errors.New("device linked to a different account")
	ErrFailedLinkEmail         = errors.New("failed to link email")
	ErrAccountRequiresPassword = errors.New("account requires password authentication")
)

// AuthenticateDiscordPassword authenticates a user using their Discord ID and password.
// It also links a device (xpID) to the user's account if it is not already linked.
// It will set a password for the account, if linked to a device and does not have one set.
func AuthenticateDiscordPassword(ctx context.Context, logger *zap.Logger, db *sql.DB, config server.Config, statusRegistry server.StatusRegistry, xpID evr.XPID, discordID, password string) (userID string, username string, newLink bool, err error) {
	userID, username, err = GetUserIDByDiscordID(ctx, db, discordID)
	if err == server.ErrAccountNotFound {
		// No account found for the given Discord ID.
		return "", "", false, nil
	} else if err != nil {
		// Some other error occurred while trying to get the user ID.
		return "", "", false, fmt.Errorf("error getting user ID by Discord ID: %w", err)
	}

	// Check if the device is linked to the account.
	ownerID, ownerUsername, _, err := server.AuthenticateDevice(ctx, logger, db, xpID.String(), "", false)
	if status.Code(err) == codes.NotFound {
		// Device is not linked to an account. Link it to this one
		if err := server.LinkDevice(ctx, logger, db, uuid.FromStringOrNil(userID), xpID.String()); err != nil {
			return "", "", false, fmt.Errorf("failed to link device: %w", err)
		}
		return userID, username, true, nil
	} else if err != nil {
		return "", "", false, fmt.Errorf("error authenticating device: %w", err)
	} else if ownerID != userID {
		// Device is linked to a different account.
		logger.Warn("Device is linked to a different account.", zap.String("device_user_id", ownerID), zap.String("session_user_id", userID))
		return ownerID, ownerUsername, false, ErrDeviceLinkMismatch
	}

	// Get the account
	account, err := server.GetAccount(ctx, logger, db, statusRegistry, uuid.FromStringOrNil(userID))
	if err != nil {
		return "", "", false, fmt.Errorf("error getting account: %w", err)
	} else if account.Email == "" {
		// Account does not have a password set, set it.
		placeholderEmail := config.GetRuntime().Environment[EnvVarPlaceholderEmailDomain]
		if err := server.LinkEmail(ctx, logger, db, uuid.FromStringOrNil(userID), userID+"@"+placeholderEmail, password); err != nil {
			return "", "", false, fmt.Errorf("failed to link email: %w", err)
		}
		return userID, username, false, nil
	}

	// Account has a password set. Authenticate to verify.
	if _, err := server.AuthenticateUsername(ctx, logger, db, username, password); err != nil {
		logger.Warn("Username/password authentication failed.", zap.Error(err), zap.String("discord_id", discordID))
		return "", "", false, err
	}

	return userID, username, false, nil
}

func AuthenticateXPID(ctx context.Context, logger *zap.Logger, db *sql.DB, statusRegistry server.StatusRegistry, xpID evr.XPID) (userID string, username string, err error) {
	// Check if the device is linked to an account.
	userID, username, _, err = server.AuthenticateDevice(ctx, logger, db, xpID.String(), "", false)
	if status.Code(err) == codes.NotFound {
		// Device is not linked to an account.
		return "", "", ErrDeviceNotLinked
	} else if err != nil {
		return "", "", fmt.Errorf("error authenticating device: %w", err)
	}
	return userID, username, nil
}

type AuthenticationError struct {
	Tag     string
	Message string
	Wrapped error
}

func (e *AuthenticationError) Error() string {
	return fmt.Sprintf("%s: %s", e.Tag, e.Message)
}

func (e *AuthenticationError) Unwrap() error {
	return e.Wrapped
}

func (p *EvrPipeline) authorizeSession(ctx context.Context, logger *zap.Logger, xpID evr.XPID, loginPayload *evr.LoginProfile, profile *EVRProfile, ipInfo IPInfo, clientIP, systemUnavailableMessage, reportURL string, isPasswordAuthenticated bool) error {

	// Check if the system is temporarily unavailable.
	if msg := ServiceSettings().DisableLoginMessage; msg != "" {
		return &AuthenticationError{
			Tag:     "system_unavailable",
			Message: "System is Temporarily Unavailable:\n" + msg,
		}
	}
	// Load the login history for audit purposes.
	loginHistory := NewLoginHistory(profile.ID())
	if err := StorableReadNk(ctx, p.nk, profile.ID(), loginHistory, true); err != nil {
		return fmt.Errorf("failed to load login history: %w", err)
	}

	// Check if the IP is on the deny list.
	if userIDs, err := LoginDeniedClientIPAddressSearch(ctx, p.nk, clientIP); err != nil {
		return &AuthenticationError{
			Tag:     "failed_ip_denylist_search",
			Message: "Internal server error",
			Wrapped: fmt.Errorf("failed to search for denied client address: %w", err),
		}
	} else if len(userIDs) > 0 {
		return &AuthenticationError{
			Tag: "ip_deny_list",
			Message: strings.Join([]string{
				"Account disabled by EchoVRCE Admins",
				"Report issues to " + reportURL,
			}, "\n"),
		}
	}

	// The account is now authenticated. Authorize the session.
	if profile.IsDisabled() {
		return &AuthenticationError{
			Tag: "account_disabled",
			Message: strings.Join([]string{
				"Account Disabled",
				"Report issues to " + reportURL,
			}, "\n"),
		}
	}

	// If IP is already authorized or user is password authenticated, allow access
	if authorized := loginHistory.IsAuthorizedIP(clientIP); authorized || isPasswordAuthenticated {
		// Update the last used time for this IP
		if isNew := loginHistory.AuthorizeIP(clientIP); isNew {
			if err := SendIPAuthorizationNotification(p.discordCache.dg, profile.ID(), clientIP); err != nil {
				// Log the error, but don't return it as it's not critical
				logger.Warn("Failed to send IP authorization notification", zap.Error(err))
			}
		}
		return nil
	}

	// Require IP verification, if the session is not authenticated.
	if !loginHistory.IsAuthorizedIP(clientIP) {
		// IP is not authorized. Add a pending authorization entry.
		entry := loginHistory.AddPendingAuthorizationIP(xpID, clientIP, loginPayload)
		if err := StorableWriteNk(ctx, p.nk, profile.ID(), loginHistory); err != nil {
			return fmt.Errorf("failed to load login history: %w", err)
		}

		// Use the last two digits of the nanos seconds as the 2FA code.
		activeGroupID := profile.GetActiveGroupID().String()
		discordID := profile.DiscordID()
		return p.SendIPApprovalRequest(ctx, profile.ID(), discordID, entry, ipInfo, activeGroupID)
	}

	SendEvent(ctx, p.nk, &EventUserAuthenticated{
		UserID:                   profile.ID(),
		XPID:                     xpID,
		ClientIP:                 clientIP,
		LoginPayload:             loginPayload,
		IsWebSocketAuthenticated: isPasswordAuthenticated,
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
	if err := p.nevr.StorableRead(ctx, session.userID.String(), latencyHistory, true); err != nil {
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

	if err := p.nevr.StorableWrite(ctx, session.userID.String(), displayNameHistory); err != nil {
		return fmt.Errorf("error writing display name history: %w", err)
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
	if err := p.nevr.StorableRead(ctx, params.profile.ID(), eqconfig, true); err != nil {
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

	return nil
}

// GetJSONFieldName returns the JSON property name of a struct field.
func GetJSONFieldName(structType interface{}, fieldName string) (string, error) {
	val := reflect.ValueOf(structType)
	if val.Kind() != reflect.Struct {
		return "", fmt.Errorf("expected a struct, got %s", val.Kind())
	}

	field, ok := val.Type().FieldByName(fieldName)
	if !ok {
		return "", fmt.Errorf("no such field: %s", fieldName)
	}

	jsonTag := field.Tag.Get("json")
	if jsonTag == "" {
		return field.Name, nil // Return the field name if no JSON tag is present
	}

	// The JSON tag may contain options, split by comma and return the first part
	return jsonTag, nil
}
