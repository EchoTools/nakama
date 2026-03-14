package server

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/bwmarrin/discordgo"
	"github.com/heroiclabs/nakama/v3/server/evr"
)

func TestValidateLoadoutToken(t *testing.T) {
	tests := []struct {
		name    string
		value   string
		maxLen  int
		wantErr bool
	}{
		{"valid alphanumeric", "MyLoadout1", 72, false},
		{"valid with underscore", "my_loadout", 72, false},
		{"empty string", "", 72, true},
		{"too long", "aaaaaaaaaaaaaaaaaa", 10, true},
		{"has spaces", "my loadout", 72, true},
		{"has special chars", "my-loadout!", 72, true},
		{"numeric only", "12345", 72, true},
		{"single letter", "a", 72, false},
		{"whitespace only", "   ", 72, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateLoadoutToken(tt.value, tt.maxLen, "test")
			if (err != nil) != tt.wantErr {
				t.Errorf("validateLoadoutToken(%q) error = %v, wantErr %v", tt.value, err, tt.wantErr)
			}
		})
	}
}

func TestIsLoadoutUserAllowed(t *testing.T) {
	tests := []struct {
		name     string
		metadata *GroupMetadata
		userID   string
		username string
		want     bool
	}{
		{
			name:     "nil metadata",
			metadata: nil,
			userID:   "123",
			username: "user",
			want:     false,
		},
		{
			name:     "allowed by user ID",
			metadata: &GroupMetadata{LoadoutCommandUserIDs: []string{"123", "456"}},
			userID:   "123",
			username: "otheruser",
			want:     true,
		},
		{
			name:     "allowed by username (legacy)",
			metadata: &GroupMetadata{LoadoutCommandUsernames: []string{"admin", "mod"}},
			userID:   "999",
			username: "admin",
			want:     true,
		},
		{
			name:     "username case insensitive",
			metadata: &GroupMetadata{LoadoutCommandUsernames: []string{"Admin"}},
			userID:   "999",
			username: "admin",
			want:     true,
		},
		{
			name:     "ID takes priority over username",
			metadata: &GroupMetadata{LoadoutCommandUserIDs: []string{"123"}, LoadoutCommandUsernames: []string{"wrongname"}},
			userID:   "123",
			username: "differentname",
			want:     true,
		},
		{
			name:     "not allowed",
			metadata: &GroupMetadata{LoadoutCommandUserIDs: []string{"456"}, LoadoutCommandUsernames: []string{"otheruser"}},
			userID:   "123",
			username: "user",
			want:     false,
		},
		{
			name:     "empty lists",
			metadata: &GroupMetadata{},
			userID:   "123",
			username: "user",
			want:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsLoadoutUserAllowed(tt.metadata, tt.userID, tt.username)
			if got != tt.want {
				t.Errorf("IsLoadoutUserAllowed() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestApplyLoadoutSlot(t *testing.T) {
	tests := []struct {
		name      string
		slot      string
		value     string
		checkSlot string
		checkVal  string
	}{
		{"emote mirrors to secondemote", "emote", "emote_test", "secondemote", "emote_test"},
		{"decal mirrors to decal_body", "decal", "decal_test", "decal_body", "decal_test"},
		{"pattern mirrors to pattern_body", "pattern", "pattern_test", "pattern_body", "pattern_test"},
		{"tint mirrors to tint_body", "tint", "tint_test", "tint_body", "tint_test"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			loadout := evr.DefaultCosmeticLoadout()
			applyLoadoutSlot(&loadout, tt.slot, tt.value)
			m := loadout.ToMap()
			if m[tt.slot] != tt.value {
				t.Errorf("slot %q = %q, want %q", tt.slot, m[tt.slot], tt.value)
			}
			if m[tt.checkSlot] != tt.checkVal {
				t.Errorf("mirror slot %q = %q, want %q", tt.checkSlot, m[tt.checkSlot], tt.checkVal)
			}
		})
	}
}

func TestMakeChoicesFromStrings(t *testing.T) {
	// Under limit
	values := []string{"a", "b", "c"}
	choices := makeChoicesFromStrings(values)
	if len(choices) != 3 {
		t.Fatalf("expected 3 choices, got %d", len(choices))
	}
	if choices[0].Name != "a" || choices[0].Value != "a" {
		t.Errorf("choice 0: got name=%q value=%v", choices[0].Name, choices[0].Value)
	}

	// Over limit
	bigList := make([]string, 50)
	for i := range bigList {
		bigList[i] = "item"
	}
	choices = makeChoicesFromStrings(bigList)
	if len(choices) != loadoutAutocompleteMax {
		t.Fatalf("expected %d choices (max), got %d", loadoutAutocompleteMax, len(choices))
	}
}

func TestCloneChoices(t *testing.T) {
	original := []*discordgo.ApplicationCommandOptionChoice{
		{Name: "test", Value: "val"},
	}
	cloned := cloneChoices(original)
	if len(cloned) != 1 || cloned[0].Name != "test" {
		t.Fatal("clone mismatch")
	}
	// Mutating clone should not affect original.
	cloned[0].Name = "mutated"
	if original[0].Name != "test" {
		t.Fatal("mutation leaked to original")
	}
}

func TestAutocompleteHTTPHandler_InvalidUserID(t *testing.T) {
	svc := NewLoadoutAutocompleteService(nil, nil)
	handler := &loadoutAutocompleteHTTPHandler{service: svc}

	req := httptest.NewRequest(http.MethodGet, "/discord/loadout/autocomplete?type=name&user_id=not-a-uuid&q=test", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestAutocompleteHTTPHandler_SlotTypeNoUserID(t *testing.T) {
	svc := NewLoadoutAutocompleteService(nil, nil)
	handler := &loadoutAutocompleteHTTPHandler{service: svc}

	// Slot type doesn't require user_id
	req := httptest.NewRequest(http.MethodGet, "/discord/loadout/autocomplete?type=slot&q=", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200 for slot autocomplete, got %d", w.Code)
	}
}

func TestAutocompleteHTTPHandler_MethodNotAllowed(t *testing.T) {
	svc := NewLoadoutAutocompleteService(nil, nil)
	handler := &loadoutAutocompleteHTTPHandler{service: svc}

	req := httptest.NewRequest(http.MethodPost, "/discord/loadout/autocomplete?type=slot", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected 405, got %d", w.Code)
	}
}

func TestAutocompleteHTTPHandler_UnknownType(t *testing.T) {
	svc := NewLoadoutAutocompleteService(nil, nil)
	handler := &loadoutAutocompleteHTTPHandler{service: svc}

	req := httptest.NewRequest(http.MethodGet, "/discord/loadout/autocomplete?type=bogus", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", w.Code)
	}
}

func TestAutocompleteHTTPHandler_NoCORSWildcard(t *testing.T) {
	svc := NewLoadoutAutocompleteService(nil, nil)
	handler := &loadoutAutocompleteHTTPHandler{service: svc}

	req := httptest.NewRequest(http.MethodGet, "/discord/loadout/autocomplete?type=slot&q=", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	cors := w.Header().Get("Access-Control-Allow-Origin")
	if cors == "*" {
		t.Error("CORS should not be wildcard '*'")
	}
}

func TestCacheBounded(t *testing.T) {
	svc := NewLoadoutAutocompleteService(nil, nil)

	// Fill cache beyond max
	for i := 0; i < loadoutAutocompleteCacheMax+100; i++ {
		key := "test|" + string(rune(i))
		svc.setCachedChoices(key, []*discordgo.ApplicationCommandOptionChoice{
			{Name: "x", Value: "x"},
		})
	}

	svc.mu.RLock()
	size := len(svc.cache)
	svc.mu.RUnlock()

	if size > loadoutAutocompleteCacheMax {
		t.Errorf("cache size %d exceeds max %d", size, loadoutAutocompleteCacheMax)
	}
}

func TestSlotChoices(t *testing.T) {
	svc := NewLoadoutAutocompleteService(nil, nil)

	// All slots
	all := svc.SlotChoices("")
	if len(all) == 0 {
		t.Fatal("expected slot choices")
	}

	// Filtered
	filtered := svc.SlotChoices("chassis")
	for _, c := range filtered {
		if c.Name != "chassis" && c.Name != "chassis_body" {
			// Should contain "chassis" substring
			t.Errorf("unexpected filtered slot: %q", c.Name)
		}
	}
}

func TestIsStorageNotFoundError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"not found string", &testError{msg: "key not found"}, true},
		{"other error", &testError{msg: "connection failed"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isStorageNotFoundError(tt.err); got != tt.want {
				t.Errorf("isStorageNotFoundError() = %v, want %v", got, tt.want)
			}
		})
	}
}

type testError struct {
	msg string
}

func (e *testError) Error() string {
	return e.msg
}
