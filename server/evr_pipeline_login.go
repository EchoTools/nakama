package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	EvrIDStorageIndex            = "EvrIDs_Index"
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
	if err := session.SendEvr(
		evr.NewLoginFailure(evrId, errMessage),
		evr.NewSTcpConnectionUnrequireEvent(),
	); err != nil {
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
		return msgFailedLoginFn(session, request.EvrId, errors.New(st.Message()))
	}

	// Let the client know that the login was successful.
	// Send the login success message and the login settings.
	return session.SendEvr(
		evr.NewLoginSuccess(session.id, request.EvrId),
		evr.NewSTcpConnectionUnrequireEvent(),
		gameSettings,
	)
}

// processLogin handles the authentication of the login connection.
func (p *EvrPipeline) processLogin(ctx context.Context, logger *zap.Logger, session *sessionWS, request *evr.LoginRequest) (settings *evr.GameSettings, err error) {

	paramsPtr, ok := LoadParams(ctx)
	if !ok {
		return nil, errors.New("session parameters not found")
	}
	params := *paramsPtr

	payload := request.LoginData

	hmdsn := request.LoginData.HmdSerialNumber

	if params.HMDSerialOverride != "" {
		hmdsn = payload.HmdSerialNumber
	}

	// Providing a discord ID and password avoids the need to link the device to the account.
	// Server Hosts use this method to authenticate.
	evrID := request.GetEvrID()
	params.LoginSession = session
	params.EvrID = evrID

	var account *api.Account
	if session.userID.IsNil() {
		// Construct the device auth token from the login payload
		deviceId := NewDeviceAuth(payload.AppId, request.EvrId, hmdsn, session.clientIP)

		account, err = p.authenticateAccount(ctx, logger, session, deviceId, params.AuthDiscordID, params.AuthPassword, payload)
		if err != nil {
			return settings, err
		}
	} else {
		account, err = GetAccount(ctx, logger, p.db, session.statusRegistry, session.userID)
		if err != nil {
			return settings, fmt.Errorf("failed to get account: %w", err)
		}
	}

	if account == nil {
		return settings, fmt.Errorf("account not found")
	}
	user := account.GetUser()
	userID := user.GetId()
	uid := uuid.FromStringOrNil(user.GetId())

	params.DiscordID = p.discordCache.UserIDToDiscordID(userID)

	// Get the user's metadata
	metadata, err := GetAccountMetadata(ctx, p.runtimeModule, userID)
	if err != nil {
		return settings, fmt.Errorf("failed to get user metadata: %w", err)
	}
	params.AccountMetadata = metadata

	// Check that this EVR-ID is only used by this userID
	otherLogins, err := p.checkEvrIDOwner(ctx, evrID)
	if err != nil {
		return settings, fmt.Errorf("failed to check EVR-ID owner: %w", err)
	}

	if len(otherLogins) > 0 {
		// Check if the user is the owner of the EVR-ID
		if otherLogins[0].UserID != uuid.FromStringOrNil(userID) {
			session.logger.Warn("EVR-ID is already in use", zap.String("evrId", evrID.Token()), zap.String("userId", userID))
		}
	}

	// If user ID is not empty, write out the login payload to storage.
	if userID != "" {
		if err := writeAuditObjects(ctx, session, userID, evrID.Token(), payload); err != nil {
			session.logger.Warn("Failed to write audit objects", zap.Error(err))
		}
	}

	params.IsVR = payload.SystemInfo.HeadsetType != "No VR"

	// Check if this user is required to use 2FA
	if found, err := CheckSystemGroupMembership(ctx, p.db, uid.String(), GroupGlobalRequire2FA); err != nil {
		if found {
			allowed := make(chan bool)
			go func() {
				ok, err := p.discordCache.CheckUser2FA(ctx, uid)
				if err != nil {
					logger.Warn("Failed to check 2FA", zap.Error(err))
					allowed <- true
				}
				allowed <- ok
			}()
			// Check if this user has 2FA enabled
			select {
			case <-ctx.Done():
				return settings, fmt.Errorf("context cancelled")
			case ok := <-allowed:
				if !ok {
					return settings, fmt.Errorf("you must enable 2FA on your Discord account to play")
				}
			case <-time.After(time.Second * 2):
			}
		}
	}

	// Load the user's profile
	profile, err := p.profileRegistry.GameProfile(ctx, logger, uuid.FromStringOrNil(userID), request.LoginData, evrID)
	if err != nil {
		session.logger.Error("failed to load game profiles", zap.Error(err))
		return evr.NewDefaultGameSettings(), fmt.Errorf("failed to load game profiles")
	}

	// Get the GroupID from the user's metadata
	groupID := metadata.GetActiveGroupID()
	// Validate that the user is in the group
	if groupID == uuid.Nil {
		// Get it from the profile
		groupID = profile.GetChannel()
	}
	metadata.SetActiveGroupID(groupID)

	// Queue a full account sync.
	p.discordCache.Queue(userID, groupID.String())
	p.discordCache.Queue(userID, "")

	memberships, err := GetGuildGroupMemberships(ctx, p.runtimeModule, userID)
	if err != nil {
		return settings, fmt.Errorf("failed to get guild groups: %w", err)
	}
	if len(memberships) == 0 {
		return settings, fmt.Errorf("user is not in any guild groups")
	}

	var found bool

	membershipMap := make(map[string]GuildGroupMembership)
	for id, m := range memberships {
		membershipMap[id] = m
		if id == groupID.String() {
			found = true
		}
	}

	if !found {
		// Get a list of the user's guild memberships and set to the largest one
		guildGroups := p.guildGroupCache.GuildGroups()
		if guildGroups == nil {
			return settings, fmt.Errorf("guild groups not found")
		}

		logger.Warn("User is not in the active group", zap.String("userId", userID), zap.String("groupID", groupID.String()))

		groupIDs := make([]string, 0, len(membershipMap))
		for id := range membershipMap {
			groupIDs = append(groupIDs, id)
		}

		// Sort the groups by the edgecount
		slices.SortStableFunc(groupIDs, func(a, b string) int {
			return int(guildGroups[a].Group.EdgeCount - guildGroups[b].Group.EdgeCount)
		})
		slices.Reverse(groupIDs)
		groupID = uuid.FromStringOrNil(groupIDs[0])

		logger.Debug("Setting active group", zap.String("groupID", groupID.String()))
		metadata.SetActiveGroupID(groupID)
	}
	params.Memberships = membershipMap

	questTypes := []string{
		"Quest",
		"Quest 2",
		"Quest 3",
		"Quest Pro",
	}

	params.IsPCVR = true

	for _, t := range questTypes {
		if strings.Contains(strings.ToLower(request.LoginData.SystemInfo.HeadsetType), strings.ToLower(t)) {
			params.IsPCVR = false
			break
		}
	}

	if err := p.runtimeModule.AccountUpdateId(ctx, userID, "", metadata.MarshalMap(), "", "", "", "", ""); err != nil {
		return settings, fmt.Errorf("failed to update user metadata: %w", err)
	}

	UpdateParams(ctx, &params)
	// Initialize the full session
	if err := session.SetIdentity(uuid.FromStringOrNil(userID), evrID, user.GetUsername()); err != nil {
		return settings, fmt.Errorf("failed to login: %w", err)
	}
	ctx = session.Context()

	profile.SetEvrID(evrID)
	profile.SetChannel(evr.GUID(metadata.GetActiveGroupID()))
	profile.UpdateDisplayName(metadata.GetActiveGroupDisplayName())

	p.profileRegistry.Save(ctx, session.userID, profile)
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

		userId = p.discordCache.DiscordIDToUserID(discordId)
		if userId != "" {
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
	linkTicket, err := p.linkTicket(ctx, logger, deviceId, &payload)
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

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	groupID := params.AccountMetadata.GetActiveGroupID()

	key := fmt.Sprintf("channelInfo,%s", groupID.String())

	// Check the cache first
	message := p.GetCachedMessage(key)

	if message == nil {

		guildGroup, found := p.guildGroupCache.GuildGroup(groupID.String())
		if !found {
			return fmt.Errorf("guild group not found: %s", groupID.String())
		}

		g := guildGroup.Group

		resource := evr.NewChannelInfoResource()

		resource.Groups = make([]evr.ChannelGroup, 4)
		for i := range resource.Groups {
			resource.Groups[i] = evr.ChannelGroup{
				ChannelUuid:  strings.ToUpper(g.Id),
				Name:         g.Name,
				Description:  g.Description,
				Rules:        g.Description + "\n" + guildGroup.Metadata.RulesText,
				RulesVersion: 1,
				Link:         fmt.Sprintf("https://discord.gg/channel/%s", guildGroup.Metadata.GuildID),
				Priority:     uint64(i),
				RAD:          true,
			}
		}

		if resource == nil {
			resource = evr.NewChannelInfoResource()
		}

		message = evr.NewSNSChannelInfoResponse(resource)
		p.CacheMessage(key, message)
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

	if params.EvrID != request.EvrID {
		return fmt.Errorf("evrId mismatch: %s != %s", params.EvrID.Token(), request.EvrID.Token())
	}

	profile, err := p.profileRegistry.Load(ctx, session.userID)
	if err != nil {
		return session.SendEvr(evr.NewLoggedInUserProfileFailure(request.EvrID, 400, "failed to load game profiles"))
	}
	profile.SetEvrID(request.EvrID)

	gg, found := p.guildGroupCache.GuildGroup(params.AccountMetadata.GetActiveGroupID().String())
	if !found {
		return fmt.Errorf("guild group not found: %s", params.AccountMetadata.GetActiveGroupID().String())
	}

	// Check if the user is required to go through community values
	if !gg.Metadata.hasCompletedCommunityValues(session.userID.String()) {
		profile.Client.Social.CommunityValuesVersion = 0
	}

	return session.SendEvr(evr.NewLoggedInUserProfileSuccess(request.EvrID, profile.Client, profile.Server))
}

func (p *EvrPipeline) updateClientProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.UpdateClientProfile)

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	evrID := request.ClientProfile.EvrID

	if params.EvrID != evrID {
		return fmt.Errorf("evrId mismatch: %s != %s", params.EvrID.Token(), evrID.Token())
	}

	go func() {
		if err := p.handleClientProfileUpdate(ctx, logger, session, evrID, request.ClientProfile); err != nil {
			if err := session.SendEvr(evr.NewUpdateProfileFailure(evrID, uint64(400), err.Error())); err != nil {
				logger.Error("Failed to send UpdateProfileFailure", zap.Error(err))
			}
		}
	}()

	// Send the profile update to the client
	return session.SendEvrUnrequire(evr.NewSNSUpdateProfileSuccess(&evrID))
}

func (p *EvrPipeline) handleClientProfileUpdate(ctx context.Context, logger *zap.Logger, session *sessionWS, evrID evr.EvrId, update evr.ClientProfile) error {
	// Set the EVR ID from the context
	update.EvrID = evrID

	params, ok := LoadParams(ctx)
	if !ok {
		return errors.New("session parameters not found")
	}

	// Get the user's metadata
	memberships := params.Memberships

	userID := session.userID.String()
	for groupID, _ := range memberships {

		gg, found := p.guildGroupCache.GuildGroup(groupID)
		if !found {
			return fmt.Errorf("guild group not found: %s", params.AccountMetadata.GetActiveGroupID().String())
		}

		md := gg.Metadata

		if !md.hasCompletedCommunityValues(userID) {
			groupID := gg.ID()

			hasCompleted := update.Social.CommunityValuesVersion != 0

			if hasCompleted {

				md.CommunityValuesUserIDsRemove(session.userID.String())

				if err := p.guildGroupCache.UpdateMetadata(ctx, groupID.String(), md); err != nil {
					logger.Error("Failed to update guild group", zap.Error(err))
				}

				p.appBot.LogMessageToChannel(fmt.Sprintf("User <@%s> has accepted the community values.", params.DiscordID), md.AuditChannelID)
			}
		}
	}

	return p.profileRegistry.UpdateClientProfile(ctx, logger, session, update)
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

		if !params.IsVR {

			eulaVersion, gaVersion, err := GetEULAVersion(ctx, p.db, session.userID.String())
			if err != nil {
				logger.Warn("Failed to get EULA version", zap.Error(err))
				eulaVersion = 1
				gaVersion = 1
			}
			document = evr.NewEULADocument(int(eulaVersion), int(gaVersion), request.Language, "https://github.com/EchoTools", "Blank EULA for NoVR clients.")
			message := evr.NewDocumentSuccess(document)

			return session.SendEvrUnrequire(message)

		}

		message := p.GetCachedMessage("eula:vr:" + request.Language)

		if message == nil {
			document, err = p.generateEULA(ctx, logger, request.Language)
			if err != nil {
				return fmt.Errorf("failed to get eula document: %w", err)
			}

			message = evr.NewDocumentSuccess(document)
		}

		return session.SendEvrUnrequire(message)

	default:
		return fmt.Errorf("unknown document: %s,%s", request.Language, request.Type)
	}
}

func GetEULAVersion(ctx context.Context, db *sql.DB, userID string) (int, int, error) {
	query := `
		SELECT (s.value->'client'->'legal'->>'eula_version')::int, (s.value->'client'->'legal'->>'game_admin_version')::int FROM storage s WHERE s.collection = $1 AND s.key = $2 AND s.user_id = $3 LIMIT 1;
	`
	var dbEULAVersion int
	var dbGameAdminVersion int
	var found = true
	if err := db.QueryRowContext(ctx, query, GameProfileStorageCollection, GameProfileStorageKey, userID).Scan(&dbEULAVersion, &dbGameAdminVersion); err != nil {
		if err == sql.ErrNoRows {
			found = false
		} else {
			return 0, 0, status.Error(codes.Internal, "failed to get EULA version")
		}
	}
	if !found {
		return 0, 0, status.Error(codes.NotFound, "server profile not found")
	}
	return dbEULAVersion, dbGameAdminVersion, nil
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

	matchID, err := NewMatchID(uuid.UUID(request.Payload.SessionID), p.node)
	if err != nil {
		return fmt.Errorf("failed to generate matchID: %w", err)
	}

	label, err := MatchLabelByID(ctx, p.runtimeModule, matchID)
	if err != nil || label == nil {
		return fmt.Errorf("failed to get match label: %w", err)
	}

	playerInfo := label.GetPlayerByEvrID(request.EvrID)

	if playerInfo == nil {
		return fmt.Errorf("failed to find player in match")
	}
	if playerInfo.Team != 0 && playerInfo.Team != 1 {
		if playerInfo.Team != 2 {
			// Unless it's a spectator, log the error
			logger.Warn("Player is on a non-player team", zap.String("evrId", request.EvrID.Token()), zap.String("team", playerInfo.Team.String()))
		}

		return nil
	}

	// Update the player's rating, if it's not a backfill player
	if !playerInfo.IsBackfill() {
		if stats, ok := request.Payload.Update.StatsGroups["arena"]; ok {
			if s, ok := stats["ArenaWins"]; ok {
				isWinner := false
				if s.Value > 0 {
					isWinner = true
				}
				rating := CalculateNewPlayerRating(request.EvrID, label.Players, isWinner)
				playerInfo.RatingMu = rating.Mu
				playerInfo.RatingSigma = rating.Sigma

			}
		}
	}

	profile, err := p.profileRegistry.Load(ctx, playerInfo.UUID())
	if err != nil {
		return fmt.Errorf("failed to load game profiles: %w", err)
	}

	profile.EarlyQuits.IncrementCompletedMatches()

	// Process the update into the leaderboard and profile

	err = p.leaderboardRegistry.ProcessProfileUpdate(ctx, logger, playerInfo.UserID, playerInfo.Username, label.Mode, &request.Payload, &profile.Server)
	if err != nil {
		logger.Error("Failed to process profile update", zap.Error(err), zap.Any("payload", request.Payload))
		return fmt.Errorf("failed to process profile update: %w", err)
	}

	// Store the profile
	p.profileRegistry.Save(ctx, playerInfo.UUID(), profile)

	return nil
}

func (p *EvrPipeline) otherUserProfileRequest(ctx context.Context, logger *zap.Logger, session *sessionWS, in evr.Message) error {
	request := in.(*evr.OtherUserProfileRequest)

	go func() {
		data, err := p.profileRegistry.GetCached(ctx, request.EvrId)
		if err != nil {
			logger.Warn("Failed to get cached profile", zap.Error(err))
			return
		}

		// Construct the response
		response := &evr.OtherUserProfileSuccess{
			EvrId:             request.EvrId,
			ServerProfileJSON: data,
		}

		// Send the profile to the client
		if err := session.SendEvr(response); err != nil {
			logger.Warn("Failed to send OtherUserProfileSuccess", zap.Error(err))
		}
	}()
	return nil
}
