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
	"sort"
	"strconv"
	"strings"
	"time"

	anyascii "github.com/anyascii/go"
	"github.com/gofrs/uuid/v5"
	jwt "github.com/golang-jwt/jwt/v4"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"github.com/samber/lo"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

const (
	SystemUserID = "00000000-0000-0000-0000-000000000000"

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
	VRMLStorageCollection        = "VRML"

	// The Application ID for Echo VR
	NoOvrAppId uint64 = 0x0
	QuestAppId uint64 = 0x7de88f07bd07a
	PcvrAppId  uint64 = 0x4dd2b684a47fa
)

var (
	DisplayNameFilterRegex       = regexp.MustCompile(`[^-0-9A-Za-z_\[\] ]`)
	DisplayNameMatchRegex        = regexp.MustCompile(`[A-Za-z]`)
	DisplayNameFilterScoreSuffix = regexp.MustCompile(`\s\(\d+\)\s\[\d+\.\d+%]`)
)

type SessionVars struct {
	AppID           string `json:"app_id"`
	EvrID           string `json:"evr_id"`
	ClientIP        string `json:"client_ip"`
	HeadsetType     string `json:"headset_type"`
	HMDSerialNumber string `json:"hmd_serial_number"`
}

func NewSessionVars(appID uint64, evrID evr.EvrId, clientIP, headsetType, hmdSerialNumber string) *SessionVars {
	return &SessionVars{
		AppID:           strconv.FormatUint(appID, 10),
		EvrID:           evrID.Token(),
		ClientIP:        clientIP,
		HeadsetType:     headsetType,
		HMDSerialNumber: hmdSerialNumber,
	}
}

func (s *SessionVars) Vars() map[string]string {
	var m map[string]string
	b, _ := json.Marshal(s)
	_ = json.Unmarshal(b, &m)
	return m
}

func SessionVarsFromMap(m map[string]string) *SessionVars {
	b, _ := json.Marshal(m)
	var s SessionVars
	_ = json.Unmarshal(b, &s)
	return &s
}

func (s *SessionVars) DeviceID() *DeviceAuth {
	appID, _ := strconv.ParseUint(s.AppID, 10, 64)
	evrID, _ := evr.ParseEvrId(s.EvrID)
	return NewDeviceAuth(appID, *evrID, s.HMDSerialNumber, s.ClientIP)
}

// The data used to generate the Device ID authentication string.
type DeviceAuth struct {
	AppID           uint64    // The application ID for the game
	EvrID           evr.EvrId // The xplatform ID string
	HMDSerialNumber string    // The HMD serial number
	ClientIP        string    // The client address
}

func NewDeviceAuth(appID uint64, evrID evr.EvrId, hmdSerialNumber, clientAddr string) *DeviceAuth {
	return &DeviceAuth{
		AppID:           appID,
		EvrID:           evrID,
		HMDSerialNumber: hmdSerialNumber,
		ClientIP:        clientAddr,
	}
}

// Generate the string used for device authentication.
// WARNING: If this is changed, then device "links" will be invalidated.
func (d DeviceAuth) Token() string {
	components := []string{
		strconv.FormatUint(d.AppID, 10),
		d.EvrID.Token(),
		d.HMDSerialNumber,
		d.ClientIP,
	}
	return invalidCharsRegex.ReplaceAllString(strings.Join(components, ":"), "")
}

func (d DeviceAuth) WildcardToken() string {
	d.ClientIP = "*"
	return d.Token()
}

// ParseDeviceAuthToken parses a device ID token into its components.
func ParseDeviceAuthToken(token string) (*DeviceAuth, error) {
	const minTokenLength = 8
	const expectedParts = 4

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

	appID, err := strconv.ParseUint(parts[0], 10, 64)
	if err != nil {
		return nil, fmt.Errorf("invalid app ID in device ID token: %s", token)
	}

	evrID, err := evr.ParseEvrId(parts[1])
	if err != nil {
		return nil, fmt.Errorf("invalid xplatform ID in device ID token: %s", token)
	}
	hmdsn := parts[2]
	if strings.Contains(hmdsn, ":") {
		return nil, fmt.Errorf("invalid HMD serial number in device ID token: %s", token)
	}

	clientAddr := parts[3]

	return &DeviceAuth{
		AppID:           appID,
		EvrID:           *evrID,
		HMDSerialNumber: hmdsn,
		ClientIP:        clientAddr,
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
func (p *EvrPipeline) linkTicket(session *sessionWS, logger *zap.Logger, deviceID *DeviceAuth, loginData *evr.LoginProfile) (*LinkTicket, error) {
	ctx := session.Context()

	// Check if a link ticket already exists for the provided xplatformId and hmdSerialNumber
	// Escape dots

	objectIds, err := session.storageIndex.List(ctx, uuid.Nil, LinkTicketIndex, fmt.Sprintf("+value.evrid_token:%s", deviceID.EvrID.Token()), 1)
	if err != nil {
		return nil, fmt.Errorf("error listing link tickets: `%q`  %v", deviceID.Token(), err)
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
			DeviceAuthToken: deviceID.Token(),
			UserIDToken:     deviceID.EvrID.String(),
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
			<-time.After(time.Millisecond * 100)
			// If the link code already exists, try again
			logger.Warn("LinkTicket: link code already exists", zap.String("linkCode", linkTicket.Code))
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
func ExchangeLinkCode(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, linkCode string) (*DeviceAuth, error) {
	// Normalize the link code to uppercase.
	linkCode = strings.ToUpper(linkCode)

	// Define the storage read request.
	readReq := []*runtime.StorageRead{
		{
			Collection: LinkTicketCollection,
			Key:        linkCode,
			UserID:     SystemUserID,
		},
	}

	// Retrieve the link ticket from storage.
	objects, err := nk.StorageRead(ctx, readReq)
	if err != nil {
		return nil, runtime.NewError("failed to read link ticket from storage", StatusInternalError)
	}

	// Check if the link ticket was found.
	if len(objects) == 0 {
		return nil, runtime.NewError("link ticket not found", StatusNotFound)
	}

	// Parse the link ticket.
	var linkTicket LinkTicket
	if err := json.Unmarshal([]byte(objects[0].Value), &linkTicket); err != nil {
		return nil, runtime.NewError("failed to unmarshal link ticket", StatusInternalError)
	}

	// Delete the used link ticket from storage.
	defer func() {
		deleteReq := []*runtime.StorageDelete{
			{
				Collection: LinkTicketCollection,
				Key:        linkCode,
				UserID:     SystemUserID,
			},
		}
		if err := nk.StorageDelete(ctx, deleteReq); err != nil {
			logger.WithField("error", err).WithField("linkTicket", linkCode).Error("Unable to delete link ticket")
		}
	}()
	token, err := ParseDeviceAuthToken(linkTicket.DeviceAuthToken)
	if err != nil {
		return nil, runtime.NewError("failed to parse device auth token", StatusInternalError)
	}
	return token, nil
}

// verifyJWT parses and verifies a JWT token using the provided key function.
// It returns the parsed token if it is valid, otherwise it returns an error.
// Nakama JWT's are signed by the `session.session_encryption_key` in the Nakama config.
func verifySignedJWT(rawToken string, secret string) (*jwt.Token, error) {
	token, err := jwt.Parse(rawToken, func(token *jwt.Token) (interface{}, error) {
		// Don't forget to validate the alg is what you expect:
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(secret), nil
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

type EVRLoginRecord struct {
	EvrID        evr.EvrId
	LoginProfile *evr.LoginProfile
	CreateTime   time.Time
	UpdateTime   time.Time
}

func GetEVRRecords(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) (map[evr.EvrId]EVRLoginRecord, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, EvrLoginStorageCollection, 100, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}

	records := make(map[evr.EvrId]EVRLoginRecord, len(listRecords))

	for _, record := range listRecords {
		var loginProfile evr.LoginProfile
		if err := json.Unmarshal([]byte(record.Value), &loginProfile); err != nil {
			return nil, fmt.Errorf("error unmarshalling login profile for %s: %v", record.GetKey(), err)
		}
		evrID, err := evr.ParseEvrId(record.Key)
		if err != nil {
			return nil, fmt.Errorf("error parsing evrID: %v", err)
		}
		records[*evrID] = EVRLoginRecord{
			EvrID:        *evrID,
			LoginProfile: &loginProfile,
			CreateTime:   record.CreateTime.AsTime(),
			UpdateTime:   record.UpdateTime.AsTime(),
		}
	}

	return records, nil
}

func GetDisplayNameRecords(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, DisplayNameCollection, 150, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

func GetAddressRecords(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, ClientAddrStorageCollection, 100, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

func GetUserIpAddresses(ctx context.Context, logger runtime.Logger, nk runtime.NakamaModule, userId string) ([]*api.StorageObject, error) {
	listRecords, _, err := nk.StorageList(ctx, SystemUserID, userId, IpAddressIndex, 100, "")
	if err != nil {
		logger.WithField("err", err).Error("Storage list error.")
		return nil, fmt.Errorf("storage list error: %v", err)
	}
	return listRecords, nil
}

type DisplayNameHistory struct {
	DisplayName string `json:"display_name"`
	Timestamp   int64  `json:"timestamp"`
}

// SelectDisplayNameByPriority sets the displayName for the account based on the priority of the options.
func SelectDisplayNameByPriority(ctx context.Context, nk runtime.NakamaModule, userId, username string, options []string) (displayName string, err error) {

	// Sanitize the options
	options = lo.Map(options, func(s string, _ int) string { return sanitizeDisplayName(s) })

	// Remove blanks
	options = lo.Filter(options, func(s string, _ int) bool { return s != "" })

	filter := make([]string, 0, len(options))

	// Filter usernames of other players
	users, err := nk.UsersGetUsername(ctx, options)
	if err != nil {
		return "", fmt.Errorf("error getting users by username: %w", err)
	}
	for _, u := range users {
		if u.Id == userId {
			continue
		}
		filter = append(filter, u.Username)
	}

	// Filter displayNames of other players
	ops := make([]*runtime.StorageRead, len(options))
	for i, option := range options {
		if option == "" {
			continue
		}
		ops[i] = &runtime.StorageRead{
			Collection: DisplayNameCollection,
			Key:        strings.ToLower(option),
		}
	}
	result, err := nk.StorageRead(ctx, ops)
	if err != nil {
		return "", fmt.Errorf("error reading displayNames: %w", err)
	}

	for _, o := range result {
		if o.UserId == userId {
			continue
		}
		filter = append(filter, o.Key)
	}

	// Filter the options
	for i, o := range options {
		if lo.Contains(filter, strings.ToLower(o)) {
			continue
		}
		return options[i], nil
	}
	// No options available
	return username, nil
}

type GroupMetadata struct {
	GuildID           string                 `json:"guild_id"`            // The guild ID
	RulesText         string                 `json:"rules_text"`          // The rules text displayed on the main menu
	MemberRole        string                 `json:"member_role"`         // The role that has access to create lobbies/matches and join social lobbies
	ModeratorRole     string                 `json:"moderator_role"`      // The rules that have access to moderation tools
	ServerHostRole    string                 `json:"serverhost_role"`     // The rules that have access to serverdb
	AllocatorRole     string                 `json:"allocator_role"`      // The rules that have access to reserve servers
	SuspensionRole    string                 `json:"suspension_role"`     // The roles that have users suspended
	ServerHostUserIDs []string               `json:"serverhost_user_ids"` // The broadcaster hosts
	AllocatorUserIDs  []string               `json:"allocator_user_ids"`  // The allocator hosts
	DebugChannel      string                 `json:"debug_channel_id"`    // The debug channel
	Unhandled         map[string]interface{} `json:"-"`
}

type AccountUserMetadata struct {
	DisplayNameOverride string           `json:"display_name_override"` // The display name override
	GlobalBanReason     string           `json:"global_ban_reason"`     // The global ban reason
	ActiveGroupID       string           `json:"active_group_id"`       // The active group ID
	Cosmetics           AccountCosmetics `json:"cosmetics"`             // The loadout
}

func (a *AccountUserMetadata) GetActiveGroupID() uuid.UUID {
	return uuid.FromStringOrNil(a.ActiveGroupID)
}

func (a *AccountUserMetadata) SetActiveGroupID(id uuid.UUID) {
	a.ActiveGroupID = id.String()
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

type AccountCosmetics struct {
	JerseyNumber int64               `json:"number"`           // The loadout number (jersey number)
	Loadout      evr.CosmeticLoadout `json:"cosmetic_loadout"` // The loadout
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

func SetDisplayNameByChannelBySession(ctx context.Context, nk runtime.NakamaModule, logger *zap.Logger, discordRegistry DiscordRegistry, session *sessionWS, groupID string) (displayName string, err error) {

	// Priority order from least to most preferred
	options := make([]string, 0, 6)
	userID := session.UserID().String()

	var account *api.Account
	// Get the account
	if account, err = nk.AccountGetId(ctx, userID); err != nil {
		return "", fmt.Errorf("error getting account: %w", err)
	}
	user := account.GetUser()
	username := account.GetUser().GetUsername()
	displayName = user.GetDisplayName()

	// If the account has an override, use that
	md := AccountUserMetadata{}
	if err = json.Unmarshal([]byte(user.GetMetadata()), &md); err != nil {
		return displayName, fmt.Errorf("error unmarshalling account metadata: %w", err)
	}
	if md.DisplayNameOverride != "" {
		displayName = md.DisplayNameOverride
		if err = nk.AccountUpdateId(ctx, userID, "", nil, md.DisplayNameOverride, "", "", "", ""); err != nil {
			return displayName, fmt.Errorf("error updating account: %w", err)
		}
		return displayName, nil
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
	options = append(options, displayName)

	gid := uuid.FromStringOrNil(groupID)
	// Get the guild
	if uuid.FromStringOrNil(groupID) != uuid.Nil {
		// Get the guild by the user's primary guild
		guild, err := discordRegistry.GetGuildByGroupId(ctx, gid.String())
		if err != nil {
			logger.Warn("Error getting guild by group id.", zap.String("group_id", groupID), zap.Error(err))
			return displayName, nil
		}
		if guild != nil {
			// Get the guild member
			guildMember, err := discordRegistry.GetGuildMember(ctx, guild.ID, discordID)
			if err != nil {
				return displayName, fmt.Errorf("error getting guild member %s for guild %s: %w", discordID, guild.ID, err)
			}
			if guildMember != nil {
				options = append(options, guildMember.Nick)
			}
		}
	}

	// Reverse the options
	options = lo.Reverse(options)
	logger.Debug("SetDisplayNameByChannelBySession", zap.String("gid", gid.String()), zap.String("options", strings.Join(options, ",")))
	displayName, err = SelectDisplayNameByPriority(ctx, nk, account.GetUser().GetId(), account.GetUser().GetUsername(), options)
	if err != nil {
		return "", fmt.Errorf("error selecting display name by priority: %w", err)
	}

	// Only update the account if something has changed
	if displayName == user.GetDisplayName() && discordUser.Username == user.GetUsername() {
		return displayName, nil
	}

	// Purge old display names
	records, err := GetDisplayNameRecords(ctx, NewRuntimeGoLogger(logger), nk, userID)
	if err != nil {
		return "", fmt.Errorf("error getting display names: %w", err)
	}
	storageDeletes := []*runtime.StorageDelete{}
	if len(records) > 2 {
		// Sort the records by create time
		sort.SliceStable(records, func(i, j int) bool {
			return records[i].CreateTime.Seconds > records[j].CreateTime.Seconds
		})
		// Delete all but the first two
		for i := 2; i < len(records); i++ {
			storageDeletes = append(storageDeletes, &runtime.StorageDelete{
				Collection: DisplayNameCollection,
				Key:        records[i].Key,
				UserID:     userID,
			})
		}
	}

	// Update the account
	accountUpdates := []*runtime.AccountUpdate{
		{
			UserID:      userID,
			Username:    username,
			DisplayName: displayName,
		},
	}
	storageWrites := []*runtime.StorageWrite{
		{
			Collection: DisplayNameCollection,
			Key:        displayName,
			UserID:     userID,
			Value:      "{}",
			Version:    "",
		},
	}

	walletUpdates := []*runtime.WalletUpdate{}
	updateLedger := true
	if _, _, err = nk.MultiUpdate(ctx, accountUpdates, storageWrites, storageDeletes, walletUpdates, updateLedger); err != nil {
		return "", fmt.Errorf("error updating account: %w", err)
	}

	// Add [BOT] to the display name if the user is a bot
	if flags, ok := ctx.Value(ctxFlagsKey{}).(int); ok {
		if flags&FlagNoVR != 0 {
			displayName = fmt.Sprintf("%s [BOT]", displayName)
		}
	}

	return displayName, nil
}

// sanitizeDisplayName filters the provided displayName to ensure it is valid.
func sanitizeDisplayName(displayName string) string {

	// Removes the discord score (i.e. ` (71) [62.95%]`) suffix from display names
	displayName = DisplayNameFilterScoreSuffix.ReplaceAllLiteralString(displayName, "")

	// Treat the unicode NBSP as a terminator
	displayName, _, _ = strings.Cut(displayName, "\u00a0")

	// Convert unicode characters to their closest ascii representation
	displayName = anyascii.Transliterate(displayName)

	// Filter the string using the regular expression
	displayName = DisplayNameFilterRegex.ReplaceAllLiteralString(displayName, "")

	// twenty characters maximum
	if len(displayName) > 20 {
		displayName = displayName[:20]
	}

	if !DisplayNameMatchRegex.MatchString(displayName) {
		return ""
	}
	// Trim spaces from both ends
	displayName = strings.TrimSpace(displayName)
	return displayName
}
