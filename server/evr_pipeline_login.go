package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"

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

	GameClientSettingsStorageCollection = "GameSettings"
	GameClientSettingsStorageKey        = "gameSettings"
	GamePlayerSettingsStorageKey        = "playerSettings"
	DocumentStorageCollection           = "GameDocuments"
	GameProfileStorageCollection        = "GameProfiles"
	RemoteLogStorageCollection          = "RemoteLogs"
	ServerGameProfileStorageKey         = "server"
	ClientGameProfileStorageKey         = "client"
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
		return status.Error(codes.InvalidArgument, "invalid EVR ID")
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
		evr.NewSNSLoginSettings(*loginSettings),
	}

	return session.SendEvr(messages)
}

// processLogin handles the authentication of the login connection.
func (p *EvrPipeline) processLogin(ctx context.Context, session *sessionWS, evrId evr.EvrId, deviceId *DeviceId, discordId string, userPassword string, payload evr.LoginData) (settings *evr.EchoClientSettings, err error) {
	// Authenticate the account.
	account, err := p.authenticateAccount(ctx, session, deviceId, discordId, userPassword, payload)
	if err != nil {
		return nil, err
	}

	user := account.GetUser()
	userId := user.GetId()

	// If user ID is not empty, write out the login payload to storage.
	if userId != "" {
		go func() {
			if err := writeAuditObjects(ctx, session, userId, evrId.Token(), payload); err != nil {
				session.logger.Warn("Failed to write audit objects", zap.Error(err))
			}
		}()
	}
	// Load game settings in a separate goroutine.

	settings, err = p.loadGameSettings(ctx, user.GetId(), session)
	if err != nil {
		session.logger.Error("failed to load game settings", zap.Error(err))
		// Use the defaults
		settings = evr.DefaultEchoClientSettings()
	}

	noVR := payload.SystemInfo.HeadsetType == "No VR"

	// Initialize the full session
	if err := session.LoginSession(userId, user.GetUsername(), evrId, deviceId, noVR); err != nil {
		return nil, fmt.Errorf("failed to login: %w", err)
	}

	go func() {
		p.loginSessionByEvrID.Store(evrId.String(), session)
		// Create a goroutine to clear the session info when the login session is closed.
		<-session.Context().Done()
		session.evrPipeline.loginSessionByEvrID.Delete(evrId.String())
	}()

	// Update the user from Discord
	// Give discord 5 seconds before moving on

	timerCtx, cancel := context.WithTimeout(ctx, 60*time.Second)

	if discordId == "" {
		// Get the userId
		discordId, err = p.discordRegistry.GetDiscordIdByUserId(ctx, userId)
	}

	go func() {
		defer cancel()
		// Update the account
		if err := p.discordRegistry.UpdateAccount(timerCtx, discordId); err != nil {
			session.logger.Warn("Failed to update account", zap.Error(err))
		}
	}()

	select {
	case <-time.After(5 * time.Second):
		session.logger.Warn("Discord update timed out")
	case <-ctx.Done():
	}

	return settings, nil
}

func (p *EvrPipeline) authenticateAccount(ctx context.Context, session *sessionWS, deviceId *DeviceId, discordId string, userPassword string, payload evr.LoginData) (*api.Account, error) {
	var err error
	var userId string
	var account *api.Account

	// Discord Authentication
	if discordId != "" {

		if userPassword == "" {
			return nil, status.Error(codes.InvalidArgument, "password required")
		}

		userId, err := p.discordRegistry.GetUserIdByDiscordId(ctx, discordId, false)
		if err == nil {
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

func (p *EvrPipeline) loadGameSettings(ctx context.Context, userId string, session *sessionWS) (*evr.EchoClientSettings, error) {
	// TODO move this off the pipeline and to evr_authenticate.go
	logger := session.logger

	// retrieve the document from storage
	objs, err := StorageReadObjects(ctx, logger, session.pipeline.db, uuid.Nil, []*api.ReadStorageObjectId{
		{
			Collection: GameClientSettingsStorageCollection,
			Key:        GameClientSettingsStorageKey,
			UserId:     userId,
		},
		{
			Collection: GameClientSettingsStorageCollection,
			Key:        GameClientSettingsStorageKey,
			UserId:     SystemUserId,
		},
	})
	if err != nil {
		return nil, fmt.Errorf("client profile storage read: %s", err)
	}
	var loginSettings evr.EchoClientSettings

	if len(objs.Objects) == 0 {
		// if the document doesn't exist, try to get the default document
		loginSettings := evr.DefaultEchoClientSettings()

		defer func() {
			jsonBytes, err := json.Marshal(loginSettings)
			if err != nil {
				logger.Error("error marshalling game settings", zap.Error(err))
			}
			// write the document to storage
			ops := StorageOpWrites{
				{
					OwnerID: uuid.Nil.String(),
					Object: &api.WriteStorageObject{
						Collection:      GameClientSettingsStorageCollection,
						Key:             GameClientSettingsStorageKey,
						Value:           string(jsonBytes),
						PermissionRead:  &wrapperspb.Int32Value{Value: int32(0)},
						PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
					},
				},
			}

			if _, _, err = StorageWriteObjects(context.Background(), session.logger, session.pipeline.db, session.metrics, session.storageIndex, true, ops); err != nil {
				logger.Error("failed to write objects", zap.Error(err))
			}
		}()
	}
	for _, record := range objs.Objects {
		if record.Key == GameClientSettingsStorageKey {
			err = json.Unmarshal([]byte(record.Value), &loginSettings)
			if err != nil {
				return nil, err
			}
		}
	}

	return &loginSettings, nil
}

func writeAuditObjects(ctx context.Context, session *sessionWS, userId string, evrIdToken string, payload evr.LoginData) error {
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
	groups, err := p.discordRegistry.GetGuildGroups(ctx, session.userID.String())
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

func (p *EvrPipeline) loggedInUserProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
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

	// TODO FIXME move the rest of the code to a seperate func and just return errors here and handle the response packet here.
	errFailure := func(e error, code int) error {
		return session.SendEvr([]evr.Message{
			evr.NewLoggedInUserProfileFailure(request.EvrId, uint64(code), e.Error()),
		})
	}

	gameProfiles, err := p.loadGameProfiles(ctx, session, evrID)
	if err != nil {
		logger.Warn("Failed to load game profiles", zap.Error(err))
		// Keep going if the profiles are not nil
		if gameProfiles == nil || gameProfiles.Server == nil {
			return errFailure(fmt.Errorf("failed to load game profiles: %w", err), 500)
		}
	}

	// Validate the user's social group (channel)
	groupID, err := p.validateSocialGroup(ctx, logger, session, gameProfiles.Client.Social.Group)
	if err != nil {
		logger.Warn("Failed to validate social group", zap.Error(err))
		// try to continue
	} else {
		gameProfiles.Client.Social.Group = groupID
	}

	gameProfiles.Server.Social.Group = gameProfiles.Client.Social.Group

	// Apply any unlocks based on the user's groups
	gameProfiles, err = p.setProfileBasedOnCosmeticGroups(ctx, logger, session, evrID, gameProfiles)
	if err != nil {
		logger.Warn("failed to set profile based on groups", zap.Error(err))
	}

	go func() {
		err = p.storeGameProfile(ctx, logger, session, gameProfiles)
		if err != nil {
			logger.Warn("Failed to store game profile", zap.Error(err))
		}
	}()

	return session.SendEvr([]evr.Message{
		evr.NewLoggedInUserProfileSuccess(evrID, gameProfiles),
	})
}

func (p *EvrPipeline) validateSocialGroup(ctx context.Context, logger *zap.Logger, session *sessionWS, groupID evr.GUID) (evr.GUID, error) {
	groups, err := p.discordRegistry.GetGuildGroups(ctx, session.userID.String())
	if err != nil {
		return groupID, fmt.Errorf("failed to get guild groups: %w", err)
	}

	if len(groups) == 0 {
		return groupID, fmt.Errorf("user is not in any groups")
	}

	groupIds := lo.Map(groups, func(g *api.Group, _ int) string { return g.GetId() })
	if lo.Contains(groupIds, groupID.String()) {
		// User is in the group
		return groupID, nil
	}

	// If the user is not in the group, find the group with the most members
	sort.Slice(groups, func(i, j int) bool {
		return groups[i].EdgeCount > groups[j].EdgeCount
	})

	return evr.GUID(uuid.FromStringOrNil(groups[0].GetId())), nil

}

// Set the user's profile based on their groups
func (p *EvrPipeline) setProfileBasedOnCosmeticGroups(ctx context.Context, logger *zap.Logger, session *sessionWS, evrId evr.EvrId, gameProfiles *evr.GameProfiles) (*evr.GameProfiles, error) {
	// Get the user's groups
	// Check if the user has any groups that would grant them cosmetics
	userGroups, _, err := p.runtimeModule.UserGroupsList(ctx, session.userID.String(), 100, nil, "")
	if err != nil {
		return gameProfiles, fmt.Errorf("failed to get user groups: %w", err)
	}

	for i, group := range userGroups {
		// If the user is not a member of the group, remove it
		if group.GetState().GetValue() > int32(api.GroupUserList_GroupUser_MEMBER) {
			// Remove the group
			userGroups = append(userGroups[:i], userGroups[i+1:]...)
		}
	}

	// Set the user's unlocked cosmetics based on their groups
	unlocks := &gameProfiles.Server.UnlockedCosmetics.Arena
	for _, group := range userGroups {
		name := group.GetGroup().GetName()
		if name[:5] == "VRML" {
			unlocks.DecalVRML = true
			unlocks.EmoteVRMLA = true
		}

		// Set VRML tags and medals based on the user's groups
		unlocks.DecalVRML = true
		unlocks.EmoteVRMLA = true
		switch name {
		default:
			unlocks.DecalVRML = false
			unlocks.EmoteVRMLA = false
		case "VRML Season Preseason":
			unlocks.TagVrmlPreseason = true
			unlocks.MedalVrmlPreseason = true

		case "VRML Season 1 Champion":
			unlocks.MedalVrmlS1Champion = true
			unlocks.TagVrmlS1Champion = true
			fallthrough
		case "VRML Season 1 Finalist":
			unlocks.MedalVrmlS1Finalist = true
			unlocks.TagVrmlS1Finalist = true
			fallthrough
		case "VRML Season 1":
			unlocks.TagVrmlS1 = true
			unlocks.MedalVrmlS1 = true

		case "VRML Season 2 Champion":
			unlocks.MedalVrmlS2Champion = true
			unlocks.TagVrmlS2Champion = true
			fallthrough
		case "VRML Season 2 Finalist":
			unlocks.MedalVrmlS2Finalist = true
			unlocks.TagVrmlS2Finalist = true
			fallthrough
		case "VRML Season 2":
			unlocks.TagVrmlS2 = true
			unlocks.MedalVrmlS2 = true

		case "VRML Season 3 Champion":
			unlocks.MedalVrmlS3Champion = true
			unlocks.TagVrmlS3Champion = true
			fallthrough
		case "VRML Season 3 Finalist":
			unlocks.MedalVrmlS3Finalist = true
			unlocks.TagVrmlS3Finalist = true
			fallthrough
		case "VRML Season 3":
			unlocks.MedalVrmlS3 = true
			unlocks.TagVrmlS3 = true

		case "VRML Season 4 Champion":
			unlocks.TagVrmlS4Champion = true
			fallthrough
		case "VRML Season 4 Finalist":
			unlocks.TagVrmlS4Finalist = true
			fallthrough
		case "VRML Season 4":
			unlocks.TagVrmlS4 = true

		case "VRML Season 5 Champion":
			unlocks.TagVrmlS5Champion = true
			fallthrough
		case "VRML Season 5 Finalist":
			unlocks.TagVrmlS5Finalist = true
			fallthrough
		case "VRML Season 5":
			unlocks.TagVrmlS5 = true

		case "VRML Season 6 Champion":
			unlocks.TagVrmlS6Champion = true
			fallthrough

		case "VRML Season 6 Finalist":
			unlocks.TagVrmlS6Finalist = true
			fallthrough
		case "VRML Season 6":
			unlocks.TagVrmlS6 = true

		case "VRML Season 7 Champion":
			unlocks.TagVrmlS7Champion = true
			fallthrough
		case "VRML Season 7 Finalist":
			unlocks.TagVrmlS7Finalist = true
			fallthrough
		case "VRML Season 7":
			unlocks.TagVrmlS7 = true
		}

		// Other group-based unlocks
		switch name {

		case "Global Developers":
			unlocks.TagDeveloper = true
			gameProfiles.Server.DeveloperFeatures = &evr.DeveloperFeatures{
				DisableAfkTimeout: true,
			}
			fallthrough
		case "Global Moderators":
			unlocks.TagGameAdmin = true
			fallthrough
		case "Global Testers":
			unlocks.DecalOneYearA = true
			unlocks.RWDEmoteGhost = true
		}
	}

	return gameProfiles, nil
}

func (p *EvrPipeline) retrieveGameProfiles(ctx context.Context, session *sessionWS, userID uuid.UUID, evrID *evr.EvrId) (*evr.GameProfiles, *evr.LoginData, error) {

	// Retrieve the default and player server game profile.
	ops := []*api.ReadStorageObjectId{

		{
			Collection: GameProfileStorageCollection,
			Key:        ServerGameProfileStorageKey,
			UserId:     userID.String(),
		},
		{
			Collection: GameProfileStorageCollection,
			Key:        ClientGameProfileStorageKey,
			UserId:     userID.String(),
		},
	}

	if evrID != nil {
		ops = append(ops, &api.ReadStorageObjectId{
			Collection: GameProfileStorageCollection,
			Key:        evrID.Token(),
			UserId:     userID.String(),
		})
	}

	objs, err := StorageReadObjects(ctx, session.logger, session.pipeline.db, uuid.Nil, ops)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read game profile: %w", err)
	}

	gameProfiles := &evr.GameProfiles{}
	loginData := &evr.LoginData{}

	for _, record := range objs.Objects {
		switch record.Key {
		case ClientGameProfileStorageKey:
			gameProfiles.Client = &evr.ClientProfile{}
			if err = json.Unmarshal([]byte(record.Value), gameProfiles.Client); err != nil {
				return nil, nil, fmt.Errorf("error unmarshalling client profile: %w", err)
			}

		case ServerGameProfileStorageKey:
			gameProfiles.Server = &evr.ServerProfile{}
			if err = json.Unmarshal([]byte(record.Value), gameProfiles.Server); err != nil {
				return nil, nil, fmt.Errorf("error unmarshalling server profile: %w", err)
			}

		case evrID.Token():
			loginData = &evr.LoginData{}
			if err = json.Unmarshal([]byte(record.Value), loginData); err != nil {
				return nil, nil, fmt.Errorf("error unmarshalling client profile: %w", err)
			}
		}
	}
	return gameProfiles, loginData, nil
}

func (p *EvrPipeline) loadGameProfiles(ctx context.Context, session *sessionWS, evrID evr.EvrId) (*evr.GameProfiles, error) {
	// Get the user's profiles, or set the default profiles
	gameProfiles, loginData, err := p.retrieveGameProfiles(ctx, session, session.userID, &evrID)
	if err != nil {
		return nil, err
	}

	// If either of the profiles is nil, get the default profiles
	if gameProfiles.Client == nil || gameProfiles.Server == nil {
		// Retrieve the defaults
		defaultProfiles, _, err := p.retrieveGameProfiles(ctx, session, uuid.Nil, nil)
		if err != nil {
			return nil, err
		}

		if gameProfiles.Client == nil {
			gameProfiles.Client = defaultProfiles.Client
		}
		if gameProfiles.Server == nil {
			gameProfiles.Server = defaultProfiles.Server

			// Apply a community "designed" loadout to the new user
			loadout, err := p.retrieveStarterLoadout(ctx, session)
			if err != nil {
				session.logger.Warn("Failed to retrieve starter loadout", zap.Error(err))
			}
			if loadout != nil {
				gameProfiles.Server.EquippedCosmetics.Instances.Unified.Slots = *loadout
			}

		}
	}
	// Unlock if the user has been a quest user.
	if strings.Contains(loginData.SystemInfo.HeadsetType, "Quest") {
		gameProfiles.Server.UnlockedCosmetics.Arena.DecalQuestLaunchA = true
		gameProfiles.Server.UnlockedCosmetics.Arena.PatternQuestA = true
		gameProfiles.Server.UnlockedCosmetics.Arena.PatternQuestA = true
	}
	currentTimestamp := time.Now().UTC().Unix()

	// Set sane defaults
	gameProfiles.Client.SetDefaults()
	gameProfiles.Server.SetDefaults()

	now := time.Now().UTC().Unix()
	// Set the basic profile data
	gameProfiles.Server.PublisherLock = loginData.PublisherLock
	gameProfiles.Server.LobbyVersion = loginData.LobbyVersion
	gameProfiles.Client.EchoUserIdToken = evrID.Token()
	gameProfiles.Client.ModifyTime = now - 30

	gameProfiles.Server.EchoUserIdToken = evrID.Token()
	gameProfiles.Server.LoginTime = currentTimestamp
	gameProfiles.Server.UpdateTime = now - 30

	displayName := ""
	if gameProfiles.Client.DisplayName != "" {
		displayName = gameProfiles.Client.DisplayName
	} else if gameProfiles.Server.DisplayName != "" {
		displayName = gameProfiles.Server.DisplayName
	} else {
		// Get the account.
		account, err := GetAccount(ctx, session.logger, session.pipeline.db, session.statusRegistry, session.userID)
		if err != nil {
			return nil, fmt.Errorf("failed to get account: %w", err)
		}
		displayName = account.GetUser().GetDisplayName()
	}
	gameProfiles.Client.DisplayName = displayName
	gameProfiles.Server.DisplayName = displayName

	return gameProfiles, nil
}

func (p *EvrPipeline) retrieveStarterLoadout(ctx context.Context, session *sessionWS) (*evr.CosmeticLoadout, error) {
	// Retrieve the default and player server game profile.
	ids, err := StorageListObjectsUser(ctx, session.logger, session.pipeline.db, true, uuid.Nil, CosmeticLoadoutCollection, 200, "", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to list objects: %w", err)
	}

	if len(ids.Objects) == 0 {
		session.logger.Warn("No starter loadouts available.")
		return nil, nil
	}
	// pick a random id
	obj := ids.Objects[rand.Intn(len(ids.Objects))]

	loadout := &StoredCosmeticLoadout{}
	if err = json.Unmarshal([]byte(obj.Value), loadout); err != nil {
		return nil, fmt.Errorf("error unmarshalling client profile: %w", err)
	}

	return loadout.Loadout, nil
}

func (p *EvrPipeline) storeGameProfile(ctx context.Context, logger *zap.Logger, session *sessionWS, gameProfiles *evr.GameProfiles) error {
	// Write the profile data to storage
	clientProfileJson, err := json.Marshal(gameProfiles.Client)
	if err != nil {
		return fmt.Errorf("error marshalling client profile: %w", err)
	}

	serverProfileJson, err := json.Marshal(gameProfiles.Server)
	if err != nil {
		return fmt.Errorf("error marshalling server profile: %w", err)
	}

	userId := session.userID.String()
	// Write the latest profile data to storage
	ops := StorageOpWrites{
		{
			OwnerID: userId,
			Object: &api.WriteStorageObject{
				Collection:      GameProfileStorageCollection,
				Key:             ClientGameProfileStorageKey,
				Value:           string(clientProfileJson),
				PermissionRead:  &wrapperspb.Int32Value{Value: int32(1)},
				PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
				Version:         "",
			},
		},
		{
			OwnerID: userId,
			Object: &api.WriteStorageObject{
				Collection:      GameProfileStorageCollection,
				Key:             ServerGameProfileStorageKey,
				Value:           string(serverProfileJson),
				PermissionRead:  &wrapperspb.Int32Value{Value: int32(1)},
				PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
				Version:         "",
			},
		},
	}
	_, _, err = StorageWriteObjects(ctx, session.logger, session.pipeline.db, session.metrics, session.storageIndex, true, ops)
	if err != nil {
		return fmt.Errorf("SNSLoggedInUserProfileRequest: failed to write objects: %w", err)
	}
	return nil
}

func (p *EvrPipeline) updateProfile(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.UpdateProfile)
	// Ignore the request and use what was authenticated with
	evrId, ok := ctx.Value(ctxEvrIDKey{}).(evr.EvrId)
	if !ok {
		return fmt.Errorf("evrId not found in context")
	}

	errFailure := func(e error, code int) error {
		if err := session.SendEvr([]evr.Message{
			evr.NewUpdateProfileFailure(evrId, uint64(code), e.Error()),
		}); err != nil {
			return fmt.Errorf("send UpdateProfileFailure: %w", err)
		}
		return fmt.Errorf("UpdateProfile: %w", e)
	}

	// Validate the client profile.
	// TODO FIXME Validate the profile data
	if errs := evr.ValidateStruct(request.ClientProfile); errs != nil {
		return errFailure(fmt.Errorf("invalid client profile: %s", errs), 400)
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
	// Update the displayname based on the user's selected channel.
	groupId := uuid.UUID(request.ClientProfile.Social.Group)
	if groupId != uuid.Nil {
		displayName, err := SetDisplayNameByChannelBySession(ctx, p.runtimeModule, p.discordRegistry, session, groupId.String())
		if err != nil {
			logger.Error("Failed to set display name.", zap.Error(err))
		} else {
			request.ClientProfile.DisplayName = displayName
		}
	}
	// Write the profile to storage.
	requestProfileJson, err := json.Marshal(request.ClientProfile)
	if err != nil {
		return errFailure(fmt.Errorf("error marshalling client profile: %w", err), 500)
	}

	// Write the latest profile data to storage
	ops := StorageOpWrites{
		{
			OwnerID: session.userID.String(),
			Object: &api.WriteStorageObject{
				Collection:      GameProfileStorageCollection,
				Key:             ClientGameProfileStorageKey,
				Value:           string(requestProfileJson),
				PermissionRead:  &wrapperspb.Int32Value{Value: int32(1)},
				PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
				Version:         "",
			},
		},
	}
	_, _, err = StorageWriteObjects(ctx, session.logger, session.pipeline.db, session.metrics, session.storageIndex, true, ops)
	if err != nil {
		return fmt.Errorf("SNSLoggedInUserProfileRequest: failed to write objects: %w", err)
	}

	return nil
}

func validateArenaUnlockByName(i interface{}, itemName string) (bool, error) {
	// Lookup the field name by it's item name (json key)
	fieldName, found := evr.ArenaUnlocksFieldByKey[itemName]
	if !found {
		return false, fmt.Errorf("unknown item name: %s", itemName)
	}

	// Lookup the field value by it's field name
	value := reflect.ValueOf(i)
	typ := value.Type()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Name == fieldName {
			return value.FieldByName(fieldName).Bool(), nil
		}
	}

	return false, fmt.Errorf("unknown unlock field name: %s", fieldName)
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
						Collection:      GameClientSettingsStorageCollection,
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

			if c.EventType != "item_equipped" || c.ItemName == "" {
				continue
			}

			var category string
			if c.ItemName[:4] == "rwd_" {
				// Reward Item.
				s := strings.SplitN(c.ItemName, "_", 3)
				if len(s) != 3 {
					logger.Error("Failed to parse reward item name", zap.String("item_name", c.ItemName))
					continue
				}
				category = s[1]
			} else {
				// Standard Item.
				s := strings.SplitN(c.ItemName, "_", 2)
				if len(s) != 2 {
					logger.Error("Failed to parse item name", zap.String("item_name", c.ItemName))
					continue
				}
				category = s[0]
			}

			// Load the server profile
			objs, err := StorageReadObjects(ctx, logger, session.pipeline.db, session.userID, []*api.ReadStorageObjectId{
				{
					Collection: GameProfileStorageCollection,
					Key:        ServerGameProfileStorageKey,
					UserId:     session.UserID().String(),
				},
			})
			if err != nil {
				logger.Error("Failed to read server profile", zap.Error(err))
				continue
			}
			if len(objs.Objects) == 0 {
				logger.Error("Server profile not found")
				continue
			}
			serverProfile := evr.ServerProfile{}
			if err := json.Unmarshal([]byte(objs.Objects[0].Value), &serverProfile); err != nil {
				logger.Error("Failed to unmarshal server profile", zap.Error(err))
				continue
			}

			// Validate that this user has the item unlocked.
			if unlocked, err := validateArenaUnlockByName(serverProfile.UnlockedCosmetics.Arena, c.ItemName); err != nil {
				logger.Error("Failed to validate arena unlock", zap.Error(err))
				continue
			} else if !unlocked {
				logger.Warn("User equipped an item that is not unlocked.", zap.String("item_name", c.ItemName))
			}

			// Equip the item
			s := &serverProfile.EquippedCosmetics.Instances.Unified.Slots
			switch category {
			case "secondemote":
				s.SecondEmote = c.ItemName
			case "emote":
				s.Emote = c.ItemName
			case "decal":
				s.Decal = c.ItemName
				fallthrough
			case "decal_body":
				s.DecalBody = c.ItemName
			case "tint":
				// Equipping tint_chassis_default to heraldry tint causes every heraldry equip to be pitch black.
				if c.ItemName != "tint_chassis_default" {
					s.Tint = c.ItemName
				}
				fallthrough
			case "tint_body":
				s.TintBody = c.ItemName
			case "pattern":
				s.Pattern = c.ItemName
			case "pattern_body":
				s.PatternBody = c.ItemName
			case "decalback":
				s.Pip = c.ItemName
			case "chassis":
				s.Chassis = c.ItemName
			case "bracer":
				s.Bracer = c.ItemName
			case "booster":
				s.Booster = c.ItemName
			case "title":
				s.Title = c.ItemName
			case "tag":
				s.Tag = c.ItemName
			case "banner":
				s.Banner = c.ItemName
			case "medal":
				s.Medal = c.ItemName
			case "goal_fx":
				s.GoalFx = c.ItemName
			case "emissive":
				s.Emissive = c.ItemName
			case "tent_alignment_a":
				s.TintAlignmentA = c.ItemName
				// TODO FIXME validate tint alignment
			case "tint_alignment_b":
				// TODO FIXME validate tint alignment
				s.TintAlignmentB = c.ItemName
			case "pip":
				s.Pip = c.ItemName
			default:
				logger.Error("Unknown item category", zap.String("category", category))
				continue
			}
			/*
				TODO This might be alot of storage operations. It might be wise to queue changes.
				     Maybe a context that is cancelled after a few seconds, and then the queue is processed.
				     Or if there's a remotelog that is a "commit" at the podeum, then the queue is processed.
			*/

			// Update the timestamp
			now := time.Now().UTC().Unix()
			serverProfile.UpdateTime = now
			// Write the profile data to storage
			serverProfileJson, err := json.Marshal(serverProfile)
			if err != nil {
				logger.Error("Failed to marshal server profile", zap.Error(err))
				continue
			}
			_, _, err = StorageWriteObjects(ctx, logger, session.pipeline.db, session.metrics, session.storageIndex, true, StorageOpWrites{
				{
					OwnerID: session.userID.String(),
					Object: &api.WriteStorageObject{
						Collection:      GameProfileStorageCollection,
						Key:             ServerGameProfileStorageKey,
						Value:           string(serverProfileJson),
						PermissionRead:  &wrapperspb.Int32Value{Value: int32(1)},
						PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
						Version:         "",
					},
				},
			})
			if err != nil {
				logger.Error("Failed to write server profile", zap.Error(err))
				continue
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
			channel, err := p.getPlayersCurrentChannel(ctx, session)
			if err != nil {
				return status.Errorf(codes.Internal, "Failed to get players current channel: %v", err)
			}
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
				groups, err := p.discordRegistry.GetGuildGroups(ctx, session.userID.String())
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

func (p *EvrPipeline) getPlayersCurrentChannel(ctx context.Context, session *sessionWS) (uuid.UUID, error) {

	// Retrieve the player's client game profile.
	objs, err := StorageReadObjects(ctx, session.logger, session.pipeline.db, uuid.Nil, []*api.ReadStorageObjectId{
		{
			Collection: GameProfileStorageCollection,
			Key:        ClientGameProfileStorageKey,
			UserId:     session.UserID().String(),
		},
	})
	if err != nil || len(objs.Objects) == 0 {
		return uuid.Nil, fmt.Errorf("failed to read game profile: %w", err)
	}

	// Unmarshal the document
	var document evr.ClientProfile
	if err := json.Unmarshal([]byte(objs.Objects[0].Value), &document); err != nil {
		return uuid.Nil, fmt.Errorf("error unmarshalling document: %w", err)
	}

	return uuid.UUID(document.Social.Group), nil
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
