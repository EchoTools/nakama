package server

import (
	"net"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama/v3/server/evr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

func registeredLogFixture() *GameServerPresence {
	return &GameServerPresence{
		ServerID:   4242,
		OperatorID: uuid.FromStringOrNil("eeeeeeee-0000-0000-0000-00000000000e"),
		Endpoint: evr.Endpoint{
			InternalIP: net.ParseIP("10.0.0.5"),
			ExternalIP: net.ParseIP("203.0.113.9"),
			Port:       6792,
		},
	}
}

// A validated registration logs one Info line with who, where, how long the
// ping took and how many tries it needed.
func TestLogGameServerRegistered_Validated(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	logGameServerRegistered(zap.New(core), registeredLogFixture(), 37*time.Millisecond, 2, false)

	entries := logs.FilterMessage("Game server registered").All()
	if len(entries) != 1 {
		t.Fatalf("got %d registration lines, want 1", len(entries))
	}
	e := entries[0]
	if e.Level != zapcore.InfoLevel {
		t.Errorf("level = %v, want info", e.Level)
	}
	f := e.ContextMap()
	want := map[string]any{
		"server_id":          uint64(4242),
		"operator_id":        "eeeeeeee-0000-0000-0000-00000000000e",
		"internal_endpoint":  "10.0.0.5:6792",
		"external_endpoint":  "203.0.113.9:6792",
		"port":               uint16(6792),
		"ping_rtt":           37 * time.Millisecond,
		"ping_attempts":      int64(2),
		"validation_skipped": false,
	}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("%s = %#v, want %#v", k, f[k], v)
		}
	}
}

// A novalidation registration logs the same line, marked as skipped.
func TestLogGameServerRegistered_ValidationSkipped(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	logGameServerRegistered(zap.New(core), registeredLogFixture(), 0, 0, true)
	entries := logs.FilterMessage("Game server registered").All()
	if len(entries) != 1 {
		t.Fatalf("got %d registration lines, want 1", len(entries))
	}
	f := entries[0].ContextMap()
	if f["validation_skipped"] != true || f["ping_attempts"] != int64(0) {
		t.Errorf("fields = %v", f)
	}
}
