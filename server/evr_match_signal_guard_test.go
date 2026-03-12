package server

import (
	"context"
	"strings"
	"testing"
)

func TestMatchSignalBlockedNakamaModule_BlocksNestedSignals(t *testing.T) {
	guard := matchSignalBlockedNakamaModule{}

	_, err := guard.MatchSignal(context.Background(), "mid", "payload")
	if err == nil {
		t.Fatal("expected nested MatchSignal to be blocked")
	}
	if !strings.Contains(err.Error(), "blocked") {
		t.Fatalf("unexpected error: %v", err)
	}
}
