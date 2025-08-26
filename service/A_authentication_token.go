package service

import (
	"math/rand"
	"time"

	jwt "github.com/golang-jwt/jwt/v5"
	"github.com/heroiclabs/nakama/v3/server"
)

func generateToken(tokenExpirySecs int64, encryptionKey string, tokenID string, tokenIssuedAt int64, userID, username string, vars map[string]string) (string, int64) {
	exp := time.Now().UTC().Add(time.Duration(tokenExpirySecs) * time.Second).Unix()
	return generateTokenWithExpiry(encryptionKey, tokenID, tokenIssuedAt, userID, username, vars, exp)
}

func generateRefreshToken(refreshTokenExpirySecs int64, encryptionKey string, tokenID string, tokenIssuedAt int64, userID string, username string, vars map[string]string) (string, int64) {
	exp := time.Now().UTC().Add(time.Duration(refreshTokenExpirySecs) * time.Second).Unix()
	return generateTokenWithExpiry(encryptionKey, tokenID, tokenIssuedAt, userID, username, vars, exp)
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
