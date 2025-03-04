package server

import (
	"context"
	"crypto"
	"errors"
	"fmt"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/golang-jwt/jwt/v4"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	StorageCollectionDeveloper = "Developer"
	StorageKeyApplications     = "applications"
)

var _ = Storable(&DeveloperApplications{})

type DeveloperApplications struct {
	Applications []DeveloperApplication `json:StorageKeyApplications`
}

func (DeveloperApplications) StorageID() StorageID {
	return StorageID{
		Collection: StorageCollectionDeveloper,
		Key:        StorageKeyApplications,
	}
}

func (DeveloperApplications) StorageIndex() *StorageIndexMeta {
	return &StorageIndexMeta{
		Name:       "developerApplicationTokens",
		Collection: StorageCollectionDeveloper,
		Key:        StorageKeyApplications,
		Fields:     []string{"value.applications.token"},
		MaxEntries: 10000,
		IndexOnly:  true,
	}
}

type DeveloperApplication struct {
	ID            uuid.UUID `json:"id"`
	Name          string    `json:"name"`
	Description   string    `json:"description"`
	Token         string    `json:"token"`
	TokenIssuedAt time.Time `json:"token_issued_at"`
}

func NewDeveloperApplicationToken(app DeveloperApplication, signingKey string) string {
	tokenID := uuid.Must(uuid.NewV4()).String()

	token, _ := generateDeveloperTokenWithExpiry(signingKey, app.ID.String(), tokenID, app.TokenIssuedAt.Unix(), "", "", nil, time.Now().Add(time.Hour*24*365*10))
	return token
}

func ApplicationTokenAuthenticate(ctx context.Context, nk runtime.NakamaModule, token string) (string, error) {
	idx := DeveloperApplications{}.StorageIndex()
	query := fmt.Sprintf("+value.applications.token:%s", Query.Escape(token))
	objs, _, err := nk.StorageIndexList(ctx, SystemUserID, idx.Name, query, 1, nil, "")
	if err != nil {
		return "", err
	}
	if len(objs.Objects) == 0 {
		return "", nil
	}

	return objs.Objects[0].UserId, nil
}

type ApplicationTokenClaims struct {
	TokenID       string            `json:"tid,omitempty"`
	UserID        string            `json:"uid,omitempty"`
	ApplicationID string            `json:"aid,omitempty"`
	Username      string            `json:"usn,omitempty"`
	Vars          map[string]string `json:"vrs,omitempty"`
	ExpiresAt     int64             `json:"exp,omitempty"`
	IssuedAt      int64             `json:"iat,omitempty"`
}

func (stc *ApplicationTokenClaims) Valid() error {
	// Verify expiry.
	if stc.ExpiresAt <= time.Now().UTC().Unix() {
		vErr := new(jwt.ValidationError)
		vErr.Inner = errors.New("Token is expired")
		vErr.Errors |= jwt.ValidationErrorExpired
		return vErr
	}
	return nil
}

func parseApplicationToken(hmacSecretByte []byte, tokenString string) (applicationID uuid.UUID, userID uuid.UUID, username string, vars map[string]string, exp int64, tokenID string, issuedAt int64, ok bool) {
	jwtToken, err := jwt.ParseWithClaims(tokenString, &ApplicationTokenClaims{}, func(token *jwt.Token) (interface{}, error) {
		if s, ok := token.Method.(*jwt.SigningMethodHMAC); !ok || s.Hash != crypto.SHA256 {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return hmacSecretByte, nil
	})
	if err != nil {
		return
	}
	claims, ok := jwtToken.Claims.(*ApplicationTokenClaims)
	if !ok || !jwtToken.Valid {
		return
	}
	applicationID, err = uuid.FromString(claims.ApplicationID)
	if err != nil {
		return
	}

	userID, err = uuid.FromString(claims.UserID)
	if err != nil {
		return
	}
	return applicationID, userID, claims.Username, claims.Vars, claims.ExpiresAt, claims.TokenID, claims.IssuedAt, true
}

func generateDeveloperTokenWithExpiry(signingKey, applicationID string, tokenID string, tokenIssuedAt int64, userID, username string, vars map[string]string, expiry time.Time) (string, int64) {
	exp := expiry.UTC().Unix()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, &ApplicationTokenClaims{
		TokenID:       tokenID,
		ApplicationID: applicationID,
		UserID:        userID,
		Username:      username,
		Vars:          vars,
		ExpiresAt:     exp,
		IssuedAt:      tokenIssuedAt,
	})
	signedToken, _ := token.SignedString([]byte(signingKey))
	return signedToken, exp
}
