package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"time"

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
		handlers: map[string]func(context.Context, http.ResponseWriter, *http.Request){
			"/": func(ctx context.Context, w http.ResponseWriter, r *http.Request) {
				// Respond to the request
				w.Write([]byte("Hello, World!"))
			},
		},
	}
}

func NewAppAPIAcceptor(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, initializer runtime.Initializer) func(http.ResponseWriter, *http.Request) {
	appAPI := NewAppAPI(ctx, logger, db, nk, initializer)

	_nk := nk.(*RuntimeGoNakamaModule)
	signingKey := _nk.config.GetSession().EncryptionKey
	config := _nk.config
	node := config.GetName()
	env := config.GetRuntime().Environment

	var authErrorBody string

	if data, err := json.Marshal(APIErrorMessage{Code: 0, Message: "Missing or invalid token"}); err != nil {
		panic(err)
	} else {
		authErrorBody = string(data)
	}

	// This handler will be attached to the API Gateway server.
	return func(w http.ResponseWriter, r *http.Request) {

		// Check authentication.
		var token string
		if auth := r.Header["Authorization"]; len(auth) >= 1 {
			// Attempt header based authentication.
			const prefix = "Bearer "
			if !strings.HasPrefix(auth[0], prefix) {
				http.Error(w, authErrorBody, 401)
				return
			}
			token = auth[0][len(prefix):]
		} else {
			// Attempt query parameter based authentication.
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			http.Error(w, authErrorBody, 401)
			return
		}
		userID, username, vars, expiry, _, _, ok := parseToken([]byte(signingKey), token)
		if !ok || expiry < time.Now().Unix() {
			http.Error(w, authErrorBody, 401)
			return
		}

		// Parse out the IP and port (IP:port) from the RemoteAddr
		clientIP, clientPort, err := net.SplitHostPort(r.RemoteAddr)
		if err != nil {
			clientIP = r.RemoteAddr
			clientPort = ""
		}
		appID, ok := vars[DEVAPP_CTX_APP_ID]
		if !ok {
			http.Error(w, authErrorBody, 401)
		}
		ctx := NewDeveloperAppContext(r.Context(), node, "", env, r.Header, r.URL.Query(), userID.String(), username, vars, clientIP, clientPort, appID)

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
