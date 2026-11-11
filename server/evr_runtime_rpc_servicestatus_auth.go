package server

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	// serviceStatusAuthTimeout bounds the whole auth probe. The endpoint is
	// unauthenticated, so a stuck DB must never be able to hold requests open.
	serviceStatusAuthTimeout = 3 * time.Second
	// serviceStatusAuthCacheTTL caps how often the probe touches the DB, however
	// often the endpoint is polled.
	serviceStatusAuthCacheTTL = 5 * time.Second
	serviceStatusAuthCacheKey = "server-status-auth"

	statusUp   = "up"
	statusDown = "down"
)

// StatusComponent is one server-computed health component. Reason is a fixed
// string chosen by the probe -- never an error message, which could carry a DSN
// or user data.
type StatusComponent struct {
	Status string `json:"status"`
	Reason string `json:"reason"`
}

// serviceStatusWithComponents is the response when components are requested.
// Services carries the existing response unchanged.
type serviceStatusWithComponents struct {
	Services   json.RawMessage            `json:"services"`
	Components map[string]StatusComponent `json:"components"`
}

type dbPinger interface {
	PingContext(ctx context.Context) error
}

// requestedStatusComponents reads ?components=a,b (repeatable) from the RPC's query parameters.
func requestedStatusComponents(ctx context.Context) map[string]bool {
	params, _ := ctx.Value(runtime.RUNTIME_CTX_QUERY_PARAMS).(map[string][]string)
	requested := make(map[string]bool)
	for _, v := range params["components"] {
		for _, name := range strings.Split(v, ",") {
			if name = strings.TrimSpace(name); name != "" {
				requested[name] = true
			}
		}
	}
	return requested
}

// checkAuthComponent reports whether authentication can work: the database
// answers, and a session token can be signed and verified with the server's
// session key. The token is built in memory, checked, and dropped -- it is never
// stored (unlike nk.AuthenticateTokenGenerate, which adds it to the session
// cache) and never returned. It always returns within timeout.
func checkAuthComponent(ctx context.Context, db dbPinger, signingKey string, timeout time.Duration) StatusComponent {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// Buffered so the goroutine can finish and exit after a timeout.
	done := make(chan StatusComponent, 1)
	go func() { done <- probeAuth(ctx, db, signingKey, signingKey) }()

	select {
	case c := <-done:
		return c
	case <-ctx.Done():
		return StatusComponent{Status: statusDown, Reason: "timeout"}
	}
}

// probeAuth signs with signKey and verifies with verifyKey. They are the same key
// in production; they are separate parameters so the verification failure is reachable.
func probeAuth(ctx context.Context, db dbPinger, signKey, verifyKey string) StatusComponent {
	if db == nil {
		return StatusComponent{Status: statusDown, Reason: "database unavailable"}
	}
	if err := db.PingContext(ctx); err != nil {
		return StatusComponent{Status: statusDown, Reason: "database unavailable"}
	}
	if signKey == "" || verifyKey == "" {
		return StatusComponent{Status: statusDown, Reason: "no signing key"}
	}

	now := time.Now().UTC()
	userID := uuid.Must(uuid.NewV4())
	token, _ := generateTokenWithExpiry(signKey, uuid.Must(uuid.NewV4()).String(), now.Unix(), userID.String(), "status-probe", nil, now.Add(time.Minute).Unix())
	if token == "" {
		return StatusComponent{Status: statusDown, Reason: "token signing failed"}
	}
	gotID, _, _, _, _, _, ok := parseToken([]byte(verifyKey), token)
	if !ok || gotID != userID {
		return StatusComponent{Status: statusDown, Reason: "token verification failed"}
	}
	return StatusComponent{Status: statusUp, Reason: "ok"}
}

// serviceStatusWithRequestedComponents wraps the existing response with the
// requested components. Components are computed outside the 60s response cache,
// on their own short cache, so "down" is seen within seconds.
func (h *RPCHandler) serviceStatusWithRequestedComponents(ctx context.Context, logger runtime.Logger, db dbPinger, base string, requested map[string]bool) (string, error) {
	components := make(map[string]StatusComponent)

	if requested["auth"] {
		c, cached := StatusComponent{}, false
		if v, _, found := h.responseCache.Get(serviceStatusAuthCacheKey); found {
			c, cached = v.(StatusComponent)
		}
		if !cached {
			c = checkAuthComponent(ctx, db, h.sessionKey, serviceStatusAuthTimeout)
			h.responseCache.Set(serviceStatusAuthCacheKey, c, serviceStatusAuthCacheTTL)
			if c.Status != statusUp {
				logger.WithField("component", "auth").WithField("status", c.Status).WithField("reason", c.Reason).Warn("servicestatus component is down")
			}
		}
		components["auth"] = c
	}

	services := json.RawMessage(base)
	if !json.Valid([]byte(base)) {
		// The default response is plain text when no status is active.
		quoted, err := json.Marshal(base)
		if err != nil {
			return "", err
		}
		services = quoted
	}

	out, err := json.Marshal(serviceStatusWithComponents{Services: services, Components: components})
	if err != nil {
		return "", err
	}
	return string(out), nil
}
