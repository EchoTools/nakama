package server

import (
	"context"
	"database/sql"
	"net"
	"net/http"
	"strings"

	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	DEVAPP_CTX_APP_ID = "dev_app_id"
)

type AppAPI struct {
	ctx      context.Context
	logger   runtime.Logger
	db       *sql.DB
	nk       runtime.NakamaModule
	handlers map[string]func(context.Context, http.ResponseWriter, *http.Request)
}

func NewAppAPI(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) *AppAPI {
	return &AppAPI{
		ctx:    ctx,
		logger: logger,
		db:     db,
		nk:     nk,
	}
}

func NewAppAPIAcceptor(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) func(http.ResponseWriter, *http.Request) {
	appAPI := NewAppAPI(ctx, logger, db, nk, initializer)

	_nk := nk.(*RuntimeGoNakamaModule)
	signingKey := _nk.config.GetSession().EncryptionKey
	config := _nk.config
	node := config.GetName()
	env := config.GetRuntime().Environment

	// This handler will be attached to the API Gateway server.
	return func(w http.ResponseWriter, r *http.Request) {

		// Check authentication.
		var token string
		if auth := r.Header["Authorization"]; len(auth) >= 1 {
			// Attempt header based authentication.
			const prefix = "Bearer "
			if !strings.HasPrefix(auth[0], prefix) {
				http.Error(w, "Missing or invalid token", 401)
				return
			}
			token = auth[0][len(prefix):]
		} else {
			// Attempt query parameter based authentication.
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			http.Error(w, "Missing or invalid token", 401)
			return
		}

		applicationID, userID, username, vars, _, _, _, ok := parseApplicationToken([]byte(signingKey), token)
		if !ok {
			http.Error(w, "Missing or invalid token", 401)
			return
		}

		// Parse out the IP and port (IP:port) from the RemoteAddr
		clientIP, clientPort, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			clientIP = r.RemoteAddr
			clientPort = ""
		}

		ctx := NewDeveloperAppContext(r.Context(), node, "", env, r.Header, r.URL.Query(), userID.String(), username, vars, clientIP, clientPort, applicationID.String())

		// Get the api path from the request
		path := r.URL.Path

		// Get the handler function for the api path
		handlerFn, ok := appAPI.handlers[path]
		if !ok {
			http.Error(w, "Not Found", 404)
			return
		}

		// Call the handler function
		handlerFn(ctx, w, r)
	}
}

// func(http.ResponseWriter, *http.Request) handler
func (api *AppAPI) handlerFn(ctx context.Context, w http.ResponseWriter, req *http.Request) {

	// Respond to the request
	w.Write([]byte("Hello, World!"))

}
