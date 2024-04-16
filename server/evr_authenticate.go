package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/dgrijalva/jwt-go"
	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	SystemUserId = "00000000-0000-0000-0000-000000000000"

	LinkTicketCollection         = "LinkTickets"
	LinkTicketIndex              = "Index_" + LinkTicketCollection
	DiscordAccessTokenCollection = "DiscordAccessTokens"
	DiscordAccessTokenKey        = "accessToken"
	SuspensionStatusCollection   = "SuspensionStatus"
	ChannelInfoStorageCollection = "ChannelInfo"
	ChannelInfoStorageKey        = "channelInfo"
	EvrLoginStorageCollection    = "EvrLogins"
	ClientAddrStorageCollection  = "ClientAddrs"
	HmdSerialIndex               = "Index_HmdSerial"
	IpAddressIndex               = "Index_" + EvrLoginStorageCollection
	DisplayNameCollection        = "DisplayNames"
	DisplayNameIndex             = "Index_DisplayName"
	GhostedUsersIndex            = "Index_MutedUsers"
	ActiveSocialGroupIndex       = "Index_SocialGroup"
	ActivePartyGroupIndex        = "Index_PartyGroup"
	CacheStorageCollection       = "Cache"
	IPinfoCacheKey               = "IPinfo"
	CosmeticLoadoutCollection    = "CosmeticLoadouts"

	// The Application ID for Echo VR
	NoOvrAppId uint64 = 0x0
	QuestAppId uint64 = 0x7de88f07bd07a
	PcvrAppId  uint64 = 0x4dd2b684a47fa
)

var (
	DisplayNameFilterRegex = regexp.MustCompile(`[^-0-9A-Za-z_\[\] ]`)
	DisplayNameMatchRegex  = regexp.MustCompile(`[A-Za-z0-9]{2}`)
)

// The data used to generate the Device ID authentication string.
type DeviceId struct {
	AppId           uint64    // The application ID for the game
	EvrId           evr.EvrId // The xplatform ID string
	HmdSerialNumber string    // The HMD serial number
}

// Generate the string used for device authentication.
// WARNING: If this is changed, then device "links" will be invalidated.
func (d *DeviceId) Token() string {
	return fmt.Sprintf("%d:%s:%s", d.AppId, d.EvrId.String(), d.HmdSerialNumber)
}

// ParseDeviceId parses a device ID token into its components.
func ParseDeviceId(token string) (*DeviceId, error) {
	const minTokenLength = 8
	const expectedParts = 3

	if token == "" {
		return nil, errors.New("empty device ID token")
	}
	if len(token) < minTokenLength {
		return nil, fmt.Errorf("token too short: %s", token)
	}
	parts := strings.SplitN(token, ":", expectedParts)
	if len(parts) != expectedParts {
		return nil, fmt.Errorf("invalid device ID token: %s", token)
	}

	appId, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid app ID in device ID token: %s", token)
	}

	evrId, err := evr.ParseEvrId(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid xplatform ID in device ID token: %s", token)
	}

	return &DeviceId{
		AppId:           appId,
		EvrId:           *evrId,
		HmdSerialNumber: parts[2],
	}, nil
}

// LinkTicket represents a ticket used for linking accounts to Discord.
// It contains the link code, xplatform ID string, and HMD serial number.
// TODO move this to evr-common
type LinkTicket struct {
	Code            string `json:"link_code"`            // the code the user will exchange to link the account
	DeviceAuthToken string `json:"nk_device_auth_token"` // the device ID token to be linked

	// NOTE: The UserIDToken has an index that is created in the InitModule function
	UserIDToken string `json:"evrid_token"` // the xplatform ID used by EchoVR as a UserID

	LoginRequest *evr.LoginProfile `json:"game_login_request"` // the login request payload that generated this link ticket
}

// TODO Move this to the evrbackend runtime module
// linkTicket generates a link ticket for the provided xplatformId and hmdSerialNumber.
func (p *EvrPipeline) linkTicket(session *sessionWS, deviceId *DeviceId, loginData *evr.LoginProfile) (*LinkTicket, error) {
	ctx := session.Context()

	// Check if a link ticket already exists for the provided xplatformId and hmdSerialNumber
	objectIds, err := session.storageIndex.List(ctx, uuid.Nil, LinkTicketIndex, fmt.Sprintf("+value.evrid_token:%s", deviceId.EvrId.String()), 1)
	if err != nil {
		return nil, fmt.Errorf(fmt.Sprintf("error listing link tickets: `%q`  %v", deviceId.EvrId.String(), err))
	}
	// Link ticket was found. Return the link ticket.
	if objectIds != nil {
		for _, record := range objectIds.Objects {
			linkTicket := &LinkTicket{}
			err := json.Unmarshal([]byte(record.Value), &linkTicket)
			if err != nil {
				return nil, fmt.Errorf(fmt.Sprintf("error unmarshalling link ticket: %v", err))
			} else {
				return linkTicket, nil
			}
		}
	}
	if loginData == nil {
		// This should't happen. A login request is required to create a link ticket.
		return nil, fmt.Errorf("loginData is nil")
	}
	// Generate a link code and attempt to write it to storage
	for {
		// loop until we have a unique link code
		linkTicket := &LinkTicket{
			Code:            generateLinkCode(),
			DeviceAuthToken: deviceId.Token(),
			UserIDToken:     deviceId.EvrId.String(),
			LoginRequest:    loginData,
		}
		linkTicketJson, err := json.Marshal(linkTicket)
		if err != nil {
			return nil, err
		}
		ops := StorageOpWrites{&StorageOpWrite{
			OwnerID: session.userID.String(),
			Object: &api.WriteStorageObject{
				Collection:      LinkTicketCollection,
				Key:             linkTicket.Code,
				Value:           string(linkTicketJson),
				PermissionRead:  &wrapperspb.Int32Value{Value: int32(0)},
				PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
			},
		}}
		_, _, err = StorageWriteObjects(ctx, session.logger, session.pipeline.db, session.metrics, session.storageIndex, false, ops)
		if err != nil {
			continue
		}
		return linkTicket, nil
	}
}

// generateLinkCode generates a 4 character random link code (excluding homoglyphs, vowels, and numbers).
// The character set .
// The random number generator is seeded with the current time to ensure randomness.
// Returns the generated link code as a string.
// TODO move this to the evrbackend runtime module
func generateLinkCode() string {
	// Define the set of valid validChars for the link code
	validChars := "ACDEFGHIJKLMNPRSTUXYZ"

	// Create a new local random generator with a known seed value
	rng := rand.New(rand.NewSource(time.Now().UnixNano()))

	// Create a byte slice with 4 elements
	code := make([]byte, 4)

	// Randomly select an index from the array and generate the code
	for i := range code {
		code[i] = validChars[rng.Intn(len(validChars))]
	}

	return string(code)
}

func (p *EvrPipeline) evrStorageObjectDefault(session *sessionWS, collection string, key string, defaultFn func() evr.Document) interface{} {
	ctx := session.Context()
	logger := session.logger
	var document evr.Document

	// retrieve the document from storage
	objs, err := StorageReadObjects(ctx, logger, session.pipeline.db, uuid.Nil, []*api.ReadStorageObjectId{
		{
			Collection: collection,
			Key:        key,
			UserId:     uuid.Nil.String(),
		},
	})
	if err != nil {
		logger.Error("SNSDocumentRequest: failed to read objects", zap.Error(err))
		return false
	}

	if (len(objs.Objects)) == 0 {
		// if the document doesn't exist, try to get the default document
		document = defaultFn()
		jsonBytes, err := json.Marshal(document)
		if err != nil {
			logger.Error("error marshalling document: %v", zap.Error(err))
			return false
		}
		// write the document to storage
		ops := StorageOpWrites{
			{
				OwnerID: uuid.Nil.String(),
				Object: &api.WriteStorageObject{
					Collection:      collection,
					Key:             key,
					Value:           string(jsonBytes),
					PermissionRead:  &wrapperspb.Int32Value{Value: int32(0)},
					PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
				},
			},
		}
		if _, _, err = StorageWriteObjects(ctx, session.logger, session.pipeline.db, session.metrics, session.storageIndex, false, ops); err != nil {
			logger.Error("failed to write objects", zap.Error(err))
			return false
		}

		logger.Error("document not found", zap.String("collection", collection), zap.String("key", key))

	} else {
		// unmarshal the document
		if err := json.Unmarshal([]byte(objs.Objects[0].Value), &document); err != nil {
			logger.Error("error unmarshalling document: %v", zap.Error(err))
			return false
		}
	}
	return document
}

// ExchangeLinkCode exchanges a link code for an auth token.
// It retrieves the link ticket from storage using the link code, unmarshals it,
// and returns the device auth token from the link ticket.
// If any error occurs during these operations, it logs the error and returns it.
// Regardless of the outcome, it deletes the used link ticket from storage.
func ExchangeLinkCode(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, linkCode string) (string, error) {
	// Normalize the link code to uppercase.
	linkCode = strings.ToUpper(linkCode)

	// Define the storage read request.
	readReq := []*runtime.StorageRead{
		{
			Collection: LinkTicketCollection,
			Key:        linkCode,
			UserID:     SystemUserId,
		},
	}

	// Retrieve the link ticket from storage.
	objects, err := nk.StorageRead(ctx, readReq)
	if err != nil {
		return "", runtime.NewError("failed to read link ticket from storage", StatusInternalError)
	}

	// Check if the link ticket was found.
	if len(objects) == 0 {
		return "", runtime.NewError("link ticket not found", StatusNotFound)
	}

	// Parse the link ticket.
	var linkTicket LinkTicket
	if err := json.Unmarshal([]byte(objects[0].Value), &linkTicket); err != nil {
		return "", runtime.NewError("failed to unmarshal link ticket", StatusInternalError)
	}

	// Delete the used link ticket from storage.
	defer func() {
		deleteReq := []*runtime.StorageDelete{
			{
				Collection: LinkTicketCollection,
				Key:        linkCode,
				UserID:     SystemUserId,
			},
		}
		if err := nk.StorageDelete(ctx, deleteReq); err != nil {
			logger.WithField("error", err).WithField("linkTicket", linkCode).Error("Unable to delete link ticket")
		}
	}()

	return linkTicket.DeviceAuthToken, nil
}

// verifyJWT parses and verifies a JWT token using the provided key function.
// It returns the parsed token if it is valid, otherwise it returns an error.
// Nakama JWT's are signed by the `session.session_encryption_key` in the Nakama config.
func verifySignedJwt(tokenString string, hmacSampleSecret []byte) (*jwt.Token, error) {
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return hmacSampleSecret, nil
	})
	if err != nil {
		return nil, err
	}
	return token, nil
}

// DiscordAccessToken represents the Discord access token structure.
type DiscordAccessToken struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"`
	Scope        string `json:"scope"`
}

func (t *DiscordAccessToken) Config() *oauth2.Config {
	return &oauth2.Config{
		Endpoint: oauth2.Endpoint{
			AuthURL:   "https://discord.com/api/oauth2/authorize",
			TokenURL:  "https://discord.com/api/oauth2/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
	}
}

func (t *DiscordAccessToken) Refresh(clientId string, clientSecret string) error {
	oauthUrl := "https://discord.com/api/v10/oauth2/token"
	data := url.Values{}
	data.Set("grant_type", "refresh_token")
	data.Set("refresh_token", t.RefreshToken)
	data.Set("client_id", clientId)
	data.Set("client_secret", clientSecret)

	client := &http.Client{}
	req, _ := http.NewRequest("POST", oauthUrl, strings.NewReader(data.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("discord refresh failed: %s", err)
	}
	defer resp.Body.Close()

	if err := json.NewDecoder(resp.Body).Decode(&t); err != nil {
		return err
	}

	return nil
}

func ExchangeCodeForAccessToken(logger runtime.Logger, code string, clientId string, clientSecret string, redirectUrl string) (*DiscordAccessToken, error) {

	conf := &oauth2.Config{
		Endpoint: oauth2.Endpoint{
			AuthURL:   "https://discord.com/api/oauth2/authorize",
			TokenURL:  "https://discord.com/api/oauth2/token",
			AuthStyle: oauth2.AuthStyleInParams,
		},
		Scopes:       []string{"identify"},
		RedirectURL:  redirectUrl,
		ClientID:     clientId,
		ClientSecret: clientSecret,
	}

	token, err := conf.Exchange(context.Background(), code)
	if err != nil {
		return nil, err
	}
	accessToken := &DiscordAccessToken{
		AccessToken:  token.AccessToken,
		RefreshToken: token.RefreshToken,
		TokenType:    token.TokenType,
		ExpiresIn:    token.Expiry.Second(),
	}

	return accessToken, nil
}

func WriteAccessTokenToStorage(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string, accessToken *DiscordAccessToken) error {
	// Create a StorageWrite object to write the access token to storage
	jsonData, err := json.Marshal(accessToken)
	if err != nil {
		logger.WithField("err", err).Error("error marshalling access token")
		return fmt.Errorf("error marshalling access token: %v", err)
	}
	objectIDs := []*runtime.StorageWrite{
		{
			Collection:      DiscordAccessTokenCollection,
			Key:             DiscordAccessTokenKey,
			UserID:          userId,
			Value:           string(jsonData),
			PermissionRead:  0,
			PermissionWrite: 0,
		},
	}
	_, err = nk.StorageWrite(ctx, objectIDs)
	if err != nil {
		logger.WithField("err", err).Error("storage write error.")
	}

	return err
}

func ReadAccessTokenFromStorage(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string, clientId string, clientSecret string) (*DiscordAccessToken, error) {
	// Create a StorageRead object to read the access token from storage
	objectIds := []*runtime.StorageRead{{
		Collection: DiscordAccessTokenCollection,
		Key:        DiscordAccessTokenKey,
		UserID:     userId,
	},
	}

	records, err := nk.StorageRead(ctx, objectIds)
	if err != nil {
		logger.WithField("err", err).Error("storage read error.")
	}
	if len(records) == 0 {
		return nil, nil
	}

	accessToken := &DiscordAccessToken{}
	if err := json.Unmarshal([]byte(records[0].Value), accessToken); err != nil {
		return nil, err
	}

	return accessToken, nil
}

func GetEvrRecords(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserId, userId, EvrLoginStorageCollection, 100, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

func GetDisplayNameRecords(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserId, userId, DisplayNameCollection, 150, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

func GetAddressRecords(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserId, userId, ClientAddrStorageCollection, 100, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

func GetUserIpAddresses(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserId, userId, IpAddressIndex, 100, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

// SetDisplayNameByPriority sets the displayName for the account based on the priority of the options.
func SetDisplayNameByPriority(ctx context.Context, nk runtime.NakamaModule, userId, username string, options []string) (displayName string, err error) {

	// Sanitize the options
	options = lo.Map(options, func(s string, _ int) string { return sanitizeDisplayName(s) })

	// Filter empty strings
	options = lo.Filter(options, func(s string, _ int) bool { return s != "" })

	// Filter existing usernames
	users, err := nk.UsersGetUsername(ctx, options)
	if err != nil {
		return "", fmt.Errorf("error getting users by username: %w", err)
	}
	existingUsernames := lo.Map(users, func(u *api.User, _ int) string { return u.Username })

	// Lookup any existing displayNames
	ops := make([]*runtime.StorageRead, len(options))
	for i, option := range options {
		ops[i] = &runtime.StorageRead{
			Collection: DisplayNameCollection,
			Key:        option,
		}
	}
	result, err := nk.StorageRead(ctx, ops)
	if err != nil {
		return "", fmt.Errorf("error reading displayNames: %w", err)
	}

	// Filter against all the results
	options = lo.Filter(options, func(s string, _ int) bool {
		// Filter empty strings
		if s == "" {
			return false
		}

		// do all comparisons in lowercase
		l := strings.ToLower(s)

		// select the user's username
		if l == strings.ToLower(username) {
			return true
		}

		// Lowercase all the usernames
		existingUsernames = lo.Map(existingUsernames, func(s string, _ int) string { return strings.ToLower(s) })

		// Remove any existingUsernames that match this user's username
		existingUsernames = lo.Filter(existingUsernames, func(s string, _ int) bool { return s != l })

		// Check if this option is already in use as a username
		if lo.Contains(existingUsernames, l) {
			return false
		}
		// Lowercase all the displayNames, and ignore this user's displayNames
		result = lo.Map(result, func(o *api.StorageObject, _ int) *api.StorageObject {
			if o.UserId == userId {
				return nil
			}
			o.Key = strings.ToLower(o.Key)
			return o
		})

		// Filter out any displayNames that are already in use
		for _, r := range result {
			if r.Key == s && r.UserId != userId {
				return false
			}
		}
		// Keep this option
		return true
	})

	// Use the top option, otherwise fallback.
	if len(options) > 0 {
		displayName = options[0]
	} else {
		displayName = username
	}

	// Update the account
	accountUpdates := []*runtime.AccountUpdate{
		{
			UserID:      userId,
			DisplayName: displayName,
		},
	}
	storageWrites := []*runtime.StorageWrite{
		{
			Collection: DisplayNameCollection,
			Key:        displayName,
			UserID:     userId,
			Value:      "{}",
			Version:    "",
		},
	}

	walletUpdates := []*runtime.WalletUpdate{}
	storageDeletes := []*runtime.StorageDelete{}
	updateLedger := true
	if _, _, err = nk.MultiUpdate(ctx, accountUpdates, storageWrites, storageDeletes, walletUpdates, updateLedger); err != nil {
		return "", fmt.Errorf("error updating account: %w", err)
	}
	return displayName, nil
}

// sanitizeDisplayName filters the provided displayName to ensure it is valid.
func sanitizeDisplayName(displayName string) string {

	// Filter the string using the regular expression
	filteredUsername := DisplayNameFilterRegex.ReplaceAllLiteralString(displayName, "")

	/*
		if !DisplayNameMatchRegex.MatchString(filteredUsername) {
			return ""
		}
	*/
	// twenty characters maximum
	if len(filteredUsername) > 20 {
		filteredUsername = filteredUsername[:20]
	}

	return filteredUsername
}

type GroupMetadata struct {
	GuildId                string   `json:"guild_id" validate:"required,uuid"`                  // The guild ID
	RulesText              string   `json:"rules_text" validate:"required,ascii"`               // The rules text displayed on the main menu
	SuspensionRoles        []string `json:"suspension_roles" validate:"dive,numeric"`           // The roles that have users suspended
	ModeratorRole          string   `json:"moderator_role" validate:"required,numeric"`         // The rules that have access to moderation tools
	BroadcasterHostRole    string   `json:"broadcaster_group_role" validate:"required,numeric"` // The rules that have access to serverdb
	ModeratorGroupId       string   `json:"moderator_group_id" validate:"required,uuid"`        // The group UUID that has access to moderation tools
	BroadcasterHostGroupId string   `json:"broadcaster_group_id" validate:"required,uuid"`      // The group UUID that has access to serverdb
}

type AccountUserMetadata struct {
	Suspensions map[string]SuspensionStatus `json:"suspension_reason"` // The reason for the suspension
}

func (a *AccountUserMetadata) MarshalToMap() (map[string]interface{}, error) {
	b, err := json.Marshal(a)
	if err != nil {
		return nil, err
	}
	var m map[string]interface{}
	err = json.Unmarshal(b, &m)
	if err != nil {
		return nil, err
	}
	return m, nil
}

type SuspensionStatus struct {
	GuildId            string        `json:"guild_id"`
	GuildName          string        `json:"guild_name"`
	UserId             string        `json:"userId"`
	UserDiscordId      string        `json:"discordId"`
	ModeratorDiscordId string        `json:"moderatorId"`
	Expiry             time.Time     `json:"expiry"`
	Duration           time.Duration `json:"duration"`
	RoleId             string        `json:"role"`
	RoleName           string        `json:"role_name"`
	Reason             string        `json:"reason"`
}

func (s *SuspensionStatus) Valid() bool {
	/// TODO use validator package
	if s.Expiry.IsZero() || s.Expiry.Before(time.Now()) || s.Duration <= 0 || s.UserId == "" || s.GuildId == "" || s.RoleId == "" {
		return false
	}
	return true
}

func NewGuildGroupMetadata(guildId string, rulesText string, modId string, serverId string) *GroupMetadata {
	return &GroupMetadata{
		GuildId:                guildId,
		RulesText:              rulesText,
		SuspensionRoles:        make([]string, 0),
		ModeratorRole:          "",
		BroadcasterHostRole:    "",
		ModeratorGroupId:       modId,
		BroadcasterHostGroupId: serverId,
	}
}

func (g *GroupMetadata) MarshalToMap() (map[string]interface{}, error) {
	guildGroupBytes, err := json.Marshal(g)
	if err != nil {
		return nil, err
	}

	var guildGroupMap map[string]interface{}
	err = json.Unmarshal(guildGroupBytes, &guildGroupMap)
	if err != nil {
		return nil, err
	}

	return guildGroupMap, nil
}

func UnmarshalGuildGroupMetadataFromMap(guildGroupMap map[string]interface{}) (*GroupMetadata, error) {
	guildGroupBytes, err := json.Marshal(guildGroupMap)
	if err != nil {
		return nil, err
	}

	var g GroupMetadata
	err = json.Unmarshal(guildGroupBytes, &g)
	if err != nil {
		return nil, err
	}

	return &g, nil
}

type RoleGroupMetadata struct {
	GuildId string `json:"guild_id"` // The Discord Guild ID
	Role    string `json:"role_id"`  // The Discord Role ID
}

func NewRoleGroupMetadata(guildId string, roleId string) *RoleGroupMetadata {
	return &RoleGroupMetadata{
		GuildId: guildId,
		Role:    roleId,
	}
}
func (md *RoleGroupMetadata) MarshalToMap() (map[string]interface{}, error) {
	guildGroupBytes, err := json.Marshal(md)
	if err != nil {
		return nil, err
	}

	var guildGroupMap map[string]interface{}
	err = json.Unmarshal(guildGroupBytes, &guildGroupMap)
	if err != nil {
		return nil, err
	}

	return guildGroupMap, nil
}

func SetDisplayNameByChannelBySession(ctx context.Context, nk runtime.NakamaModule, logger *zap.Logger, discordRegistry DiscordRegistry, session *sessionWS, groupId string) (displayName string, err error) {

	// Priority order from least to most preferred
	options := make([]string, 0, 6)

	var account *api.Account
	// Get the account
	if account, err = nk.AccountGetId(ctx, session.UserID().String()); err != nil {
		return "", fmt.Errorf("error getting account: %w", err)
	} else {
		username := account.GetUser().GetUsername()
		// set the fallback
		displayName = username
		// Add the option
		options = append(options, username)
	}

	// set the fallback
	if dn := account.GetUser().GetDisplayName(); dn != "" {
		options = append(options, dn)
		// set the fallback
		displayName = dn
	}

	// Get the discordID from the account's customID
	discordID := account.GetCustomId()
	if discordID == "" {
		return displayName, nil
	}

	// Get the discord user
	discordUser, err := discordRegistry.GetUser(ctx, discordID)
	if err != nil {
		return displayName, fmt.Errorf("error getting discord user: %w", err)
	}
	options = append(options, discordUser.Username)
	options = append(options, discordUser.GlobalName)

	// Get the guild
	if uuid.FromStringOrNil(groupId) != uuid.Nil {
		// Get the guild by the user's primary guild
		guild, err := discordRegistry.GetGuildByGroupId(ctx, groupId)
		if err != nil {
			session.logger.Warn("Error getting guild by group id.", zap.String("group_id", groupId), zap.Error(err))
			return displayName, nil
		}
		if guild != nil {
			// Get the guild member
			guildMember, err := discordRegistry.GetGuildMember(ctx, guild.ID, discordID)
			if err != nil {
				return displayName, fmt.Errorf("error getting guild member %s for guild %s: %w", discordID, guild.ID, err)
			}
			options = append(options, guildMember.Nick)
		}
	}

	// Reverse the options
	options = lo.Reverse(options)
	logger.Debug("SetDisplayNameByChannelBySession", zap.String("options", strings.Join(options, ",")))
	return SetDisplayNameByPriority(ctx, nk, account.GetUser().GetId(), account.GetUser().GetUsername(), options)
}
