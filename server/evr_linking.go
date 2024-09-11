package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	jwt "github.com/golang-jwt/jwt/v4"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

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
				return nil, fmt.Errorf(fmt.Sprintf("error unmarshalling link ticket: %w", err))
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
		return fmt.Errorf("error marshalling access token: %w", err)
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
