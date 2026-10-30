package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

// equipSkipNakamaModule serves match labels and reports no stream presences, so a
// log with no match of its own finds no match through the login session either.
type equipSkipNakamaModule struct {
	mockVacateNakamaModule
}

func (m *equipSkipNakamaModule) StreamUserList(mode uint8, subject, subcontext, label string, includeHidden, includeNotHidden bool) ([]runtime.Presence, error) {
	return nil, nil
}

func newEquipSkipModule(t *testing.T, native bool) (*equipSkipNakamaModule, uuid.UUID) {
	t.Helper()
	matchSessionID := uuid.Must(uuid.NewV4())
	matchID, err := NewMatchID(matchSessionID, "testnode")
	if err != nil {
		t.Fatalf("NewMatchID: %v", err)
	}
	nk := &equipSkipNakamaModule{mockVacateNakamaModule{matchLabels: map[string]*MatchLabel{
		matchID.String(): {GameServer: &GameServerPresence{NativeSupport: native}},
	}}}
	return nk, matchSessionID
}

// A late equip from a NEVR match is skipped by the match the log names, with no
// login session left to look the match up through.
func TestSkipRemoteLogEquip_NativeMatchSkippedWithoutSession(t *testing.T) {
	nk, matchSessionID := newEquipSkipModule(t, true)
	if skip, _ := skipRemoteLogEquip(context.Background(), nk, matchSessionID, uuid.Nil, "testnode"); !skip {
		t.Fatal("equip from a NEVR match was not skipped")
	}
}

func TestSkipRemoteLogEquip_LegacyMatchSaved(t *testing.T) {
	nk, matchSessionID := newEquipSkipModule(t, false)
	if skip, reason := skipRemoteLogEquip(context.Background(), nk, matchSessionID, uuid.Nil, "testnode"); skip {
		t.Fatalf("equip from a legacy match was skipped: %s", reason)
	}
}

// An equip whose match is gone cannot be checked, so it is not saved over what a
// NEVR server may already have saved.
func TestSkipRemoteLogEquip_UnknownMatchSkipped(t *testing.T) {
	nk, _ := newEquipSkipModule(t, false)
	skip, reason := skipRemoteLogEquip(context.Background(), nk, uuid.Must(uuid.NewV4()), uuid.Nil, "testnode")
	if !skip || reason == "" {
		t.Fatalf("equip from an unknown match: skip=%v reason=%q, want skipped with a reason", skip, reason)
	}
}

// An equip made outside any match (no match in the log, none for the session) is
// saved.
func TestSkipRemoteLogEquip_NoMatchSaved(t *testing.T) {
	nk, _ := newEquipSkipModule(t, true)
	if skip, reason := skipRemoteLogEquip(context.Background(), nk, uuid.Nil, uuid.Must(uuid.NewV4()), "testnode"); skip {
		t.Fatalf("equip outside a match was skipped: %s", reason)
	}
}
