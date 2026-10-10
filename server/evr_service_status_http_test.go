package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gorilla/mux"
	"github.com/heroiclabs/nakama-common/runtime"
	"go.uber.org/zap"
)

func newStatusLogger() runtime.Logger {
	return NewRuntimeGoLogger(zap.NewNop())
}

// The game's request, as echovr.exe builds it, reaches the handler through the same router
// shape the runtime uses, and the body is the array ServiceStatusRPC produced.
func TestServiceStatusHTTPServesTheGamesRequest(t *testing.T) {
	const want = `[{"serviceid":"services","available":true,"message":""},{"serviceid":"news","available":true,"message":"hello"}]`
	h := NewServiceStatusHTTPHandler(newStatusLogger(), func(context.Context) (string, error) { return want, nil })
	router := mux.NewRouter()
	router.HandleFunc(serviceStatusHTTPPath, h).Methods(http.MethodGet)

	req := httptest.NewRequest(http.MethodGet, "/status/services,news?env=live&projectid=rad14", nil)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if got := rr.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("content type = %q", got)
	}
	if rr.Body.String() != want {
		t.Errorf("body = %q, want %q", rr.Body.String(), want)
	}
}

func TestServiceStatusHTTPOtherPathsAndMethodsAreNotServed(t *testing.T) {
	h := NewServiceStatusHTTPHandler(newStatusLogger(), func(context.Context) (string, error) { return "[]", nil })
	router := mux.NewRouter()
	router.HandleFunc(serviceStatusHTTPPath, h).Methods(http.MethodGet)

	for _, path := range []string{"/status", "/status/", "/status/Services", "/status/services/news"} {
		rr := httptest.NewRecorder()
		router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, rr.Code)
		}
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodPost, "/status/services,news", nil))
	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("POST = %d, want 405", rr.Code)
	}
}

func TestServiceStatusHTTPAFailedFetchIsA500NotAnEmpty200(t *testing.T) {
	h := NewServiceStatusHTTPHandler(newStatusLogger(), func(context.Context) (string, error) {
		return "", errors.New("storage unavailable")
	})
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/status/services,news", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	if rr.Body.Len() > len("internal error\n") {
		t.Errorf("error body leaks detail: %q", rr.Body.String())
	}
}

// A stored status that is not active makes the RPC return ServiceSettings().serviceStatusMessage: plain text.
// The game's parser needs the array; the text becomes the message of one "services" element.
func TestServiceStatusHTTPAlwaysServesAnArray(t *testing.T) {
	cases := []struct {
		name, rpc, wantMessage string
	}{
		{"plain text", "12 players in 3 matches", "12 players in 3 matches"},
		{"empty", "", ""},
		{"whitespace", "  \n", ""},
		{"json object", `{"message":"x"}`, `{"message":"x"}`},
		{"text with quotes", `say "hi" \ there`, `say "hi" \ there`},
	}
	for _, tc := range cases {
		h := NewServiceStatusHTTPHandler(newStatusLogger(), func(context.Context) (string, error) { return tc.rpc, nil })
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/status/services,news", nil))
		var got []ServiceStatusService
		if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
			t.Errorf("%s: body %q is not a [{serviceid,available,message}] array: %v", tc.name, rr.Body.String(), err)
			continue
		}
		if len(got) != 1 || got[0].ServiceID != "services" || !got[0].Available || got[0].Message != tc.wantMessage {
			t.Errorf("%s: got %+v, want one available services element with message %q", tc.name, got, tc.wantMessage)
		}
	}
}

func TestServiceStatusHTTPAnActiveArrayIsServedUnchanged(t *testing.T) {
	const want = `[{"serviceid":"services","available":false,"message":"maintenance"},{"serviceid":"news","available":true,"message":"hi"}]`
	h := NewServiceStatusHTTPHandler(newStatusLogger(), func(context.Context) (string, error) { return "\n" + want + "\n", nil })
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/status/services,news", nil))
	if rr.Body.String() != want {
		t.Errorf("body = %q, want %q", rr.Body.String(), want)
	}
}
