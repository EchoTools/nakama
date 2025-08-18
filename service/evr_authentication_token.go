package service

import (
	"math/rand"
	"time"

	"github.com/gofrs/uuid/v5"
	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/heroiclabs/nakama/v3/server"
)

func generateToken(config server.Config, tokenID string, tokenIssuedAt int64, userID, username string, vars map[string]string) (string, int64) {
	exp := time.Now().UTC().Add(time.Duration(config.GetSession().TokenExpirySec) * time.Second).Unix()
	return generateTokenWithExpiry(config.GetSession().EncryptionKey, tokenID, tokenIssuedAt, userID, username, vars, exp)
}

func generateRefreshToken(config server.Config, tokenID string, tokenIssuedAt int64, userID string, username string, vars map[string]string) (string, int64) {
	exp := time.Now().UTC().Add(time.Duration(config.GetSession().RefreshTokenExpirySec) * time.Second).Unix()
	return generateTokenWithExpiry(config.GetSession().RefreshEncryptionKey, tokenID, tokenIssuedAt, userID, username, vars, exp)
}

func generateTokenWithExpiry(signingKey, tokenID string, tokenIssuedAt int64, userID, username string, vars map[string]string, exp int64) (string, int64) {
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, &server.SessionTokenClaims{
		TokenId:   tokenID,
		UserId:    userID,
		Username:  username,
		Vars:      vars,
		ExpiresAt: exp,
		IssuedAt:  tokenIssuedAt,
	})
	signedToken, _ := token.SignedString([]byte(signingKey))
	return signedToken, exp
}

func generateUsername() string {
	const usernameAlphabet = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	b := make([]byte, 10)
	for i := range b {
		b[i] = usernameAlphabet[rand.Intn(len(usernameAlphabet))]
	}
	return string(b)
}

func parseToken(hmacSecretByte []byte, tokenString string) (userID uuid.UUID, username string, vars map[string]string, exp int64, tokenId string, issuedAt int64, ok bool) {
	jwtToken, err := jwt.ParseWithClaims(tokenString, &server.SessionTokenClaims{}, func(token *jwt.Token) (interface{}, error) {
		return hmacSecretByte, nil
	}, jwt.WithExpirationRequired(), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil {
		return
	}
	claims, ok := jwtToken.Claims.(*server.SessionTokenClaims)
	if !ok || !jwtToken.Valid {
		return
	}
	userID, err = uuid.FromString(claims.UserId)
	if err != nil {
		return
	}
	return userID, claims.Username, claims.Vars, claims.ExpiresAt, claims.TokenId, claims.IssuedAt, true
}
