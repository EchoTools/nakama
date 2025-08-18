package service

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"

	evr "github.com/echotools/nakama/v3/protocol"
	"github.com/gofrs/uuid/v5"
	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
	"golang.org/x/oauth2"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

// LinkTicket represents a ticket used for linking accounts to Discord.
// It contains the link code, xplatform ID string, and HMD serial number.
// TODO move this to evr-common
type LinkTicket struct {
	Code         string            `json:"link_code"`          // the code the user will exchange to link the account
	XPID         evr.EvrId         `json:"xp_id"`              // the xplatform ID used by the client/server
	ClientIP     string            `json:"client_ip"`          // the client IP address that generated this link ticket
	LoginProfile *evr.LoginProfile `json:"game_login_request"` // the login request payload that generated this link ticket
	CreatedAt    time.Time         `json:"created_at"`         // the time the link ticket was created
}

func LoadLinkTickets(ctx context.Context, nk runtime.NakamaModule) (map[string]*LinkTicket, error) {
	linkTickets := make(map[string]*LinkTicket, 1)

	// Load the link ticket storage object
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: AuthorizationCollection,
			Key:        LinkTicketKey,
			UserID:     SystemUserID,
		},
	})
	if err != nil {
		return nil, err
	}
	if len(objs) != 0 {
		// unmarshal the document
		if err := json.Unmarshal([]byte(objs[0].Value), &linkTickets); err != nil {
			return nil, err
		}
	}

	return linkTickets, nil
}

func StoreLinkTickets(ctx context.Context, nk runtime.NakamaModule, linkTickets map[string]*LinkTicket) error {
	data, err := json.Marshal(linkTickets)
	if err != nil {
		return err
	}

	// write the document to storage
	ops := []*runtime.StorageWrite{
		{
			Collection:      AuthorizationCollection,
			Key:             LinkTicketKey,
			UserID:          SystemUserID,
			Value:           string(data),
			PermissionRead:  0,
			PermissionWrite: 0,
		},
	}
	_, err = nk.StorageWrite(ctx, ops)
	return err
}

// linkTicket generates a link ticket for the provided xplatformId and hmdSerialNumber.
func (p *EvrPipeline) linkTicket(ctx context.Context, xpid evr.EvrId, clientIP string, loginData *evr.LoginProfile) (*LinkTicket, error) {

	if loginData == nil {
		// This should't happen. A login request is required to create a link ticket.
		return nil, fmt.Errorf("loginData is nil")
	}

	linkTickets, err := LoadLinkTickets(ctx, p.nk)
	if err != nil {
		return nil, err
	}
	linkTicket := generateLinkTicket(linkTickets, xpid, clientIP, loginData)
	// Store the link ticket
	if err := StoreLinkTickets(ctx, p.nk, linkTickets); err != nil {
		return nil, err
	}

	return linkTicket, nil
}

func generateLinkTicket(linkTickets map[string]*LinkTicket, xpid evr.EvrId, clientIP string, loginData *evr.LoginProfile) *LinkTicket {
	found := true
	var ticket *LinkTicket
	for _, ticket := range linkTickets {
		if ticket.XPID == xpid {
			found = true
			break
		}
	}
	if !found {
		return ticket
	}

	// Generate a unique link code
	var code string
	for {
		code = generateLinkCode()
		if _, ok := linkTickets[code]; !ok {
			break
		}
	}

	// Create a new link ticket
	ticket = &LinkTicket{
		Code:         code,
		XPID:         xpid,
		ClientIP:     clientIP,
		LoginProfile: loginData,
		CreatedAt:    time.Now(),
	}
	linkTickets[ticket.Code] = ticket

	return ticket
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

func (p *EvrPipeline) evrStorageObjectDefault(session *sessionEVR, collection string, key string, defaultFn func() evr.Document) interface{} {
	ctx := session.Context()
	logger := session.logger
	var document evr.Document

	// retrieve the document from storage
	objs, err := server.StorageReadObjects(ctx, logger, p.db, uuid.Nil, []*api.ReadStorageObjectId{
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
		ops := server.StorageOpWrites{
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
		if _, _, err = server.StorageWriteObjects(ctx, session.logger, p.db, session.metrics, session.storageIndex, false, ops); err != nil {
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
func ExchangeLinkCode(ctx context.Context, nk runtime.NakamaModule, logger runtime.Logger, linkCode string) (*LinkTicket, error) {
	// Normalize the link code to uppercase.
	linkCode = strings.ToUpper(linkCode)

	linkTickets, err := LoadLinkTickets(ctx, nk)
	if err != nil {
		return nil, err
	}

	linkTicket, ok := linkTickets[linkCode]
	if !ok {
		return nil, runtime.NewError(fmt.Sprintf("link code `%s` not found", linkCode), StatusNotFound)
	}

	delete(linkTickets, linkCode)

	if err := StoreLinkTickets(ctx, nk, linkTickets); err != nil {
		return nil, err
	}

	return linkTicket, nil
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
