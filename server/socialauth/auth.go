package socialauth

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"

	"github.com/golang-jwt/jwt/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
)

const (
	jwtAuthCookieName = "authenticated_user"
)

func setJWTAuthCookie(w http.ResponseWriter, token string) {
	// Set a cookie to indicate the user is authenticated
	http.SetCookie(w, &http.Cookie{
		Name:     jwtAuthCookieName,
		Value:    token,
		HttpOnly: false, // Prevents JavaScript access to the cookie
		Secure:   true,  // Ensures the cookie is sent over HTTPS only
	})
}

func validateAuthenticatedUserCookie(logger runtime.Logger, r *http.Request, sessionEncryptionKey string) (string, error) {
	cookie, err := r.Cookie(jwtAuthCookieName)
	if err != nil {
		return "", nil
	}

	// Verify the session token and extract the user ID
	token, err := verifySignedJWT(cookie.Value, sessionEncryptionKey)
	if err != nil {
		logger.WithField("err", err).Error("Unable to verify session token")
		return "", runtime.NewError("Unable to verify session token", int(codes.Unauthenticated))
	}

	userID := token.Claims.(jwt.MapClaims)["uid"].(string)
	return userID, nil
}

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

func CheckGroupMembershipByName(ctx context.Context, db *sql.DB, userID, groupName, groupType string) (bool, error) {
	query := `
SELECT ge.state FROM groups g, group_edge ge WHERE g.id = ge.destination_id AND g.lang_tag = $1 AND g.name = $2 
AND ge.source_id = $3 AND ge.state >= 0 AND ge.state <= 2;
`

	params := make([]interface{}, 0, 4)
	params = append(params, groupType)
	params = append(params, groupName)
	params = append(params, userID)
	rows, err := db.QueryContext(ctx, query, params...)
	if err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	defer rows.Close()
	if !rows.Next() {
		return false, nil
	}
	return true, nil
}
