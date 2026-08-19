package server

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// errGuardRegistrationRefused is the injected registration failure.
var errGuardRegistrationRefused = errors.New("registration refused")

// guardTestInitializer embeds the production *RuntimeGoInitializer rather than a
// nil runtime.Initializer. AGENTS.md defect class 5: a double that embeds the
// nil interface and implements only what it needs panics the whole test binary
// on the first un-stubbed method, and registerAPIGuards calls 54 of them. The
// production initializer implements all of them for real, so the panic surface
// is zero and the double cannot drift from the interface.
//
// The overrides below shadow the promoted methods. Defect class 1 (embedding
// does not dispatch virtually) does not apply: registerAPIGuards holds this
// value behind the runtime.Initializer interface, which dispatches to the
// outermost type, so the injected failure is reached.
type guardTestInitializer struct {
	*RuntimeGoInitializer
}

func (i *guardTestInitializer) RegisterBeforeAuthenticateSteam(fn func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.AuthenticateSteamRequest) (*api.AuthenticateSteamRequest, error)) error {
	return errGuardRegistrationRefused
}

func (i *guardTestInitializer) RegisterBeforeUnlinkDevice(fn func(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, in *api.AccountDevice) (*api.AccountDevice, error)) error {
	return errGuardRegistrationRefused
}

// newGuardTestInitializer builds the minimum production initializer that
// registerAPIGuards touches: the before-request struct the RegisterBefore*
// methods assign into, and the before-rt map RegisterBeforeRt keys.
func newGuardTestInitializer() *RuntimeGoInitializer {
	return &RuntimeGoInitializer{
		beforeReq: &RuntimeBeforeReqFunctions{},
		beforeRt:  make(map[string]RuntimeBeforeRtFunction),
	}
}

// TestRegisterAPIGuardsFailsClosed pins the fail-closed contract: a
// RestrictAPIFunctionAccess registration that fails leaves that upstream
// endpoint UNRESTRICTED, so registerAPIGuards must report it rather than
// returning success.
func TestRegisterAPIGuardsFailsClosed(t *testing.T) {
	t.Parallel()

	initializer := &guardTestInitializer{RuntimeGoInitializer: newGuardTestInitializer()}

	err := registerAPIGuards(initializer)
	if err == nil {
		t.Fatal("registerAPIGuards returned nil, but two endpoint registrations failed; the endpoints are left unrestricted and the caller is told boot succeeded")
	}

	if !errors.Is(err, errGuardRegistrationRefused) {
		t.Errorf("returned error does not wrap the injected failure: %v", err)
	}

	// Every failure must be reported, not just the first, and each must name its
	// endpoint -- "RestrictAPIFunctionAccess failed" is useless at 3am.
	for _, endpoint := range []string{"RegisterBeforeAuthenticateSteam", "RegisterBeforeUnlinkDevice"} {
		if !strings.Contains(err.Error(), endpoint) {
			t.Errorf("error does not name the failed endpoint %q: %v", endpoint, err)
		}
	}
}

// TestRegisterAPIGuardsSucceedsWhenEveryRegistrationSucceeds is the companion
// half: the fail-closed change must not make a healthy boot report failure.
func TestRegisterAPIGuardsSucceedsWhenEveryRegistrationSucceeds(t *testing.T) {
	t.Parallel()

	if err := registerAPIGuards(newGuardTestInitializer()); err != nil {
		t.Fatalf("registerAPIGuards failed on an initializer that accepts every registration: %v", err)
	}
}
