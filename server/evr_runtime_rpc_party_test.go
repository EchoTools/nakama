package server

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

// mockPartyNK implements the subset of runtime.NakamaModule needed for party RPCs.
type mockPartyNK struct {
	runtime.NakamaModule
	storage map[string]map[string]string // collection -> key -> value
}

func newMockPartyNK() *mockPartyNK {
	return &mockPartyNK{
		storage: make(map[string]map[string]string),
	}
}

func (m *mockPartyNK) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	var results []*api.StorageObject
	for _, r := range reads {
		col, ok := m.storage[r.Collection]
		if !ok {
			continue
		}
		val, ok := col[r.Key]
		if !ok {
			continue
		}
		results = append(results, &api.StorageObject{
			Collection: r.Collection,
			Key:        r.Key,
			UserId:     r.UserID,
			Value:      val,
		})
	}
	return results, nil
}

func (m *mockPartyNK) StorageWrite(ctx context.Context, writes []*runtime.StorageWrite) ([]*api.StorageObjectAck, error) {
	var acks []*api.StorageObjectAck
	for _, w := range writes {
		if _, ok := m.storage[w.Collection]; !ok {
			m.storage[w.Collection] = make(map[string]string)
		}
		m.storage[w.Collection][w.Key] = w.Value
		acks = append(acks, &api.StorageObjectAck{
			Collection: w.Collection,
			Key:        w.Key,
			UserId:     w.UserID,
		})
	}
	return acks, nil
}

func (m *mockPartyNK) StorageDelete(ctx context.Context, deletes []*runtime.StorageDelete) error {
	for _, d := range deletes {
		if col, ok := m.storage[d.Collection]; ok {
			delete(col, d.Key)
		}
	}
	return nil
}

// partyCtxWithUser creates a context with user info for testing.
func partyCtxWithUser(userID, username string) context.Context {
	ctx := context.Background()
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_USER_ID, userID)
	ctx = context.WithValue(ctx, runtime.RUNTIME_CTX_USERNAME, username)
	return ctx
}

// partyCtxNoUser creates a context without user info.
func partyCtxNoUser() context.Context {
	return context.Background()
}

// partyLogger returns a mock logger (reuses existing mockLogger from evr_runtime_rpc_middleware_test.go).
func partyLogger() runtime.Logger {
	return &mockLogger{}
}

// ============================================================================
// StoredParty struct tests
// ============================================================================

func TestStoredPartyJSON(t *testing.T) {
	party := StoredParty{
		PartyID:  "test-party-id",
		LeaderID: "user-1",
		Members: []PartyMember{
			{UserID: "user-1", Username: "alice"},
			{UserID: "user-2", Username: "bob"},
		},
		MaxSize:   4,
		Open:      true,
		CreatedAt: time.Date(2026, 3, 23, 12, 0, 0, 0, time.UTC),
	}

	data, err := json.Marshal(party)
	if err != nil {
		t.Fatal(err)
	}

	var decoded StoredParty
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}

	if decoded.PartyID != party.PartyID {
		t.Errorf("PartyID = %q, want %q", decoded.PartyID, party.PartyID)
	}
	if decoded.LeaderID != party.LeaderID {
		t.Errorf("LeaderID = %q, want %q", decoded.LeaderID, party.LeaderID)
	}
	if len(decoded.Members) != 2 {
		t.Errorf("Members count = %d, want 2", len(decoded.Members))
	}
	if decoded.MaxSize != 4 {
		t.Errorf("MaxSize = %d, want 4", decoded.MaxSize)
	}
	if !decoded.Open {
		t.Error("Open should be true")
	}
}

// ============================================================================
// Storage helper tests
// ============================================================================

func TestStorageHelpers(t *testing.T) {
	ctx := context.Background()
	nk := newMockPartyNK()

	party := &StoredParty{
		PartyID:   "party-123",
		LeaderID:  "user-1",
		Members:   []PartyMember{{UserID: "user-1", Username: "alice"}},
		MaxSize:   4,
		Open:      true,
		CreatedAt: time.Now(),
	}

	// Store
	if err := storeParty(ctx, nk, party); err != nil {
		t.Fatalf("storeParty: %v", err)
	}

	// Load
	loaded, err := loadParty(ctx, nk, "party-123")
	if err != nil {
		t.Fatalf("loadParty: %v", err)
	}
	if loaded == nil {
		t.Fatal("loadParty returned nil")
	}
	if loaded.PartyID != "party-123" {
		t.Errorf("PartyID = %q, want %q", loaded.PartyID, "party-123")
	}
	if loaded.LeaderID != "user-1" {
		t.Errorf("LeaderID = %q, want %q", loaded.LeaderID, "user-1")
	}

	// Load nonexistent
	missing, err := loadParty(ctx, nk, "nonexistent")
	if err != nil {
		t.Fatalf("loadParty nonexistent: %v", err)
	}
	if missing != nil {
		t.Error("expected nil for nonexistent party")
	}

	// Delete
	if err := deleteParty(ctx, nk, "party-123"); err != nil {
		t.Fatalf("deleteParty: %v", err)
	}
	deleted, _ := loadParty(ctx, nk, "party-123")
	if deleted != nil {
		t.Error("party should be deleted")
	}
}

// ============================================================================
// RPC tests — full lifecycle
// ============================================================================

func TestPartyCreateRPC(t *testing.T) {
	ctx := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, err := PartyCreateRPC(ctx, logger, nil, nk, `{"max_size":4,"open":true}`)
	if err != nil {
		t.Fatalf("PartyCreateRPC: %v", err)
	}

	var result struct {
		PartyID string `json:"party_id"`
	}
	if err := json.Unmarshal([]byte(resp), &result); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if result.PartyID == "" {
		t.Error("party_id should not be empty")
	}

	// Verify party was stored
	party, err := loadParty(ctx, nk, result.PartyID)
	if err != nil {
		t.Fatalf("loadParty: %v", err)
	}
	if party == nil {
		t.Fatal("party should exist in storage")
	}
	if party.LeaderID != "user-1" {
		t.Errorf("LeaderID = %q, want %q", party.LeaderID, "user-1")
	}
	if len(party.Members) != 1 {
		t.Errorf("Members count = %d, want 1", len(party.Members))
	}
	if party.Members[0].Username != "alice" {
		t.Errorf("Member username = %q, want %q", party.Members[0].Username, "alice")
	}
}

func TestPartyCreateRPC_NoAuth(t *testing.T) {
	ctx := partyCtxNoUser()
	nk := newMockPartyNK()
	logger := partyLogger()

	_, err := PartyCreateRPC(ctx, logger, nil, nk, `{}`)
	if err == nil {
		t.Error("expected error for unauthenticated request")
	}
}

func TestPartyCreateRPC_DefaultValues(t *testing.T) {
	ctx := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	// Empty payload should use defaults
	resp, err := PartyCreateRPC(ctx, logger, nil, nk, `{}`)
	if err != nil {
		t.Fatalf("PartyCreateRPC: %v", err)
	}

	var result struct {
		PartyID string `json:"party_id"`
	}
	json.Unmarshal([]byte(resp), &result)

	party, _ := loadParty(ctx, nk, result.PartyID)
	if party.MaxSize != PartyMaxDefaultSize {
		t.Errorf("MaxSize = %d, want default %d", party.MaxSize, PartyMaxDefaultSize)
	}
}

func TestPartyJoinRPC(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	ctx2 := partyCtxWithUser("user-2", "bob")
	nk := newMockPartyNK()
	logger := partyLogger()

	// Create party
	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{"max_size":4,"open":true}`)
	var created struct {
		PartyID string `json:"party_id"`
	}
	json.Unmarshal([]byte(resp), &created)

	// Join party
	_, err := PartyJoinRPC(ctx2, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)
	if err != nil {
		t.Fatalf("PartyJoinRPC: %v", err)
	}

	// Verify member was added
	party, _ := loadParty(ctx1, nk, created.PartyID)
	if len(party.Members) != 2 {
		t.Errorf("Members count = %d, want 2", len(party.Members))
	}
}

func TestPartyJoinRPC_Full(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	// Create party with max_size=1
	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{"max_size":1,"open":true}`)
	var created struct {
		PartyID string `json:"party_id"`
	}
	json.Unmarshal([]byte(resp), &created)

	// Try to join — should fail (party full)
	ctx2 := partyCtxWithUser("user-2", "bob")
	_, err := PartyJoinRPC(ctx2, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)
	if err == nil {
		t.Error("expected error for full party")
	}
}

func TestPartyJoinRPC_NotFound(t *testing.T) {
	ctx := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	_, err := PartyJoinRPC(ctx, logger, nil, nk, `{"party_id":"nonexistent"}`)
	if err == nil {
		t.Error("expected error for nonexistent party")
	}
}

func TestPartyJoinRPC_AlreadyMember(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{"max_size":4,"open":true}`)
	var created struct {
		PartyID string `json:"party_id"`
	}
	json.Unmarshal([]byte(resp), &created)

	// Try to join own party — should succeed idempotently or error
	_, err := PartyJoinRPC(ctx1, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)
	// Either succeeds (idempotent) or fails (already member) — both are acceptable
	_ = err

	// Verify still only 1 member
	party, _ := loadParty(ctx1, nk, created.PartyID)
	if len(party.Members) > 1 {
		t.Errorf("Should not have duplicate members, got %d", len(party.Members))
	}
}

func TestPartyLeaveRPC(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	ctx2 := partyCtxWithUser("user-2", "bob")
	nk := newMockPartyNK()
	logger := partyLogger()

	// Create and join
	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{"max_size":4,"open":true}`)
	var created struct {
		PartyID string `json:"party_id"`
	}
	json.Unmarshal([]byte(resp), &created)
	PartyJoinRPC(ctx2, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)

	// Bob leaves
	_, err := PartyLeaveRPC(ctx2, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)
	if err != nil {
		t.Fatalf("PartyLeaveRPC: %v", err)
	}

	party, _ := loadParty(ctx1, nk, created.PartyID)
	if len(party.Members) != 1 {
		t.Errorf("Members count = %d, want 1", len(party.Members))
	}
}

func TestPartyLeaveRPC_LeaderLeaves_Promotes(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	ctx2 := partyCtxWithUser("user-2", "bob")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{"max_size":4,"open":true}`)
	var created struct {
		PartyID string `json:"party_id"`
	}
	json.Unmarshal([]byte(resp), &created)
	PartyJoinRPC(ctx2, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)

	// Leader leaves — bob should become leader
	_, err := PartyLeaveRPC(ctx1, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)
	if err != nil {
		t.Fatalf("PartyLeaveRPC: %v", err)
	}

	party, _ := loadParty(ctx2, nk, created.PartyID)
	if party == nil {
		t.Fatal("party should still exist")
	}
	if party.LeaderID != "user-2" {
		t.Errorf("LeaderID = %q, want %q (auto-promoted)", party.LeaderID, "user-2")
	}
}

func TestPartyLeaveRPC_LastMember_DeletesParty(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{"max_size":4,"open":true}`)
	var created struct {
		PartyID string `json:"party_id"`
	}
	json.Unmarshal([]byte(resp), &created)

	// Last member leaves — party should be deleted
	_, err := PartyLeaveRPC(ctx1, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)
	if err != nil {
		t.Fatalf("PartyLeaveRPC: %v", err)
	}

	party, _ := loadParty(ctx1, nk, created.PartyID)
	if party != nil {
		t.Error("party should be deleted when last member leaves")
	}
}

func TestPartyKickRPC(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	ctx2 := partyCtxWithUser("user-2", "bob")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{"max_size":4,"open":true}`)
	var created struct {
		PartyID string `json:"party_id"`
	}
	json.Unmarshal([]byte(resp), &created)
	PartyJoinRPC(ctx2, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)

	// Leader kicks bob
	_, err := PartyKickRPC(ctx1, logger, nil, nk, `{"party_id":"`+created.PartyID+`","target_id":"user-2"}`)
	if err != nil {
		t.Fatalf("PartyKickRPC: %v", err)
	}

	party, _ := loadParty(ctx1, nk, created.PartyID)
	if len(party.Members) != 1 {
		t.Errorf("Members count = %d, want 1 after kick", len(party.Members))
	}
}

func TestPartyKickRPC_NotLeader(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	ctx2 := partyCtxWithUser("user-2", "bob")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{"max_size":4,"open":true}`)
	var created struct {
		PartyID string `json:"party_id"`
	}
	json.Unmarshal([]byte(resp), &created)
	PartyJoinRPC(ctx2, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)

	// Non-leader tries to kick — should fail
	_, err := PartyKickRPC(ctx2, logger, nil, nk, `{"party_id":"`+created.PartyID+`","target_id":"user-1"}`)
	if err == nil {
		t.Error("expected error for non-leader kick")
	}
}

func TestPartyPromoteRPC(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	ctx2 := partyCtxWithUser("user-2", "bob")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{"max_size":4,"open":true}`)
	var created struct {
		PartyID string `json:"party_id"`
	}
	json.Unmarshal([]byte(resp), &created)
	PartyJoinRPC(ctx2, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)

	// Promote bob
	_, err := PartyPromoteRPC(ctx1, logger, nil, nk, `{"party_id":"`+created.PartyID+`","target_id":"user-2"}`)
	if err != nil {
		t.Fatalf("PartyPromoteRPC: %v", err)
	}

	party, _ := loadParty(ctx1, nk, created.PartyID)
	if party.LeaderID != "user-2" {
		t.Errorf("LeaderID = %q, want %q after promote", party.LeaderID, "user-2")
	}
}

func TestPartyPromoteRPC_NotLeader(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	ctx2 := partyCtxWithUser("user-2", "bob")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{"max_size":4,"open":true}`)
	var created struct {
		PartyID string `json:"party_id"`
	}
	json.Unmarshal([]byte(resp), &created)
	PartyJoinRPC(ctx2, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)

	// Non-leader tries to promote — should fail
	_, err := PartyPromoteRPC(ctx2, logger, nil, nk, `{"party_id":"`+created.PartyID+`","target_id":"user-1"}`)
	if err == nil {
		t.Error("expected error for non-leader promote")
	}
}

func TestPartyListMembersRPC(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	ctx2 := partyCtxWithUser("user-2", "bob")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{"max_size":4,"open":true}`)
	var created struct {
		PartyID string `json:"party_id"`
	}
	json.Unmarshal([]byte(resp), &created)
	PartyJoinRPC(ctx2, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)

	// List members
	listResp, err := PartyListMembersRPC(ctx1, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)
	if err != nil {
		t.Fatalf("PartyListMembersRPC: %v", err)
	}

	var members struct {
		PartyID  string        `json:"party_id"`
		LeaderID string        `json:"leader_id"`
		Members  []PartyMember `json:"members"`
	}
	if err := json.Unmarshal([]byte(listResp), &members); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(members.Members) != 2 {
		t.Errorf("Members count = %d, want 2", len(members.Members))
	}
	if members.LeaderID != "user-1" {
		t.Errorf("LeaderID = %q, want %q", members.LeaderID, "user-1")
	}
}

func TestPartyListMembersRPC_NotFound(t *testing.T) {
	ctx := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	_, err := PartyListMembersRPC(ctx, logger, nil, nk, `{"party_id":"nonexistent"}`)
	if err == nil {
		t.Error("expected error for nonexistent party")
	}
}

// ============================================================================
// Full lifecycle test
// ============================================================================

func TestPartyFullLifecycle(t *testing.T) {
	nk := newMockPartyNK()
	logger := partyLogger()
	ctx1 := partyCtxWithUser("user-1", "alice")
	ctx2 := partyCtxWithUser("user-2", "bob")
	ctx3 := partyCtxWithUser("user-3", "charlie")

	// 1. Alice creates party
	resp, err := PartyCreateRPC(ctx1, logger, nil, nk, `{"max_size":4,"open":true}`)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	var created struct {
		PartyID string `json:"party_id"`
	}
	json.Unmarshal([]byte(resp), &created)
	partyID := created.PartyID

	// 2. Bob joins
	_, err = PartyJoinRPC(ctx2, logger, nil, nk, `{"party_id":"`+partyID+`"}`)
	if err != nil {
		t.Fatalf("bob join: %v", err)
	}

	// 3. Charlie joins
	_, err = PartyJoinRPC(ctx3, logger, nil, nk, `{"party_id":"`+partyID+`"}`)
	if err != nil {
		t.Fatalf("charlie join: %v", err)
	}

	// 4. Verify 3 members
	party, _ := loadParty(ctx1, nk, partyID)
	if len(party.Members) != 3 {
		t.Fatalf("expected 3 members, got %d", len(party.Members))
	}

	// 5. Alice kicks charlie
	_, err = PartyKickRPC(ctx1, logger, nil, nk, `{"party_id":"`+partyID+`","target_id":"user-3"}`)
	if err != nil {
		t.Fatalf("kick charlie: %v", err)
	}

	// 6. Verify 2 members
	party, _ = loadParty(ctx1, nk, partyID)
	if len(party.Members) != 2 {
		t.Fatalf("expected 2 members after kick, got %d", len(party.Members))
	}

	// 7. Alice promotes bob
	_, err = PartyPromoteRPC(ctx1, logger, nil, nk, `{"party_id":"`+partyID+`","target_id":"user-2"}`)
	if err != nil {
		t.Fatalf("promote bob: %v", err)
	}

	// 8. Bob is now leader
	party, _ = loadParty(ctx1, nk, partyID)
	if party.LeaderID != "user-2" {
		t.Errorf("leader should be bob, got %q", party.LeaderID)
	}

	// 9. Bob kicks alice (bob is leader now)
	_, err = PartyKickRPC(ctx2, logger, nil, nk, `{"party_id":"`+partyID+`","target_id":"user-1"}`)
	if err != nil {
		t.Fatalf("bob kick alice: %v", err)
	}

	// 10. Bob leaves — party should be deleted
	_, err = PartyLeaveRPC(ctx2, logger, nil, nk, `{"party_id":"`+partyID+`"}`)
	if err != nil {
		t.Fatalf("bob leave: %v", err)
	}

	party, _ = loadParty(ctx1, nk, partyID)
	if party != nil {
		t.Error("party should be deleted after last member leaves")
	}
}

// ============================================================================
// Invalid input tests
// ============================================================================

func TestPartyRPCs_EmptyPayload(t *testing.T) {
	ctx := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	// Join with empty party_id
	_, err := PartyJoinRPC(ctx, logger, nil, nk, `{"party_id":""}`)
	if err == nil {
		t.Error("PartyJoinRPC should fail with empty party_id")
	}

	// Kick with missing user_id
	_, err = PartyKickRPC(ctx, logger, nil, nk, `{"party_id":"test"}`)
	if err == nil {
		t.Error("PartyKickRPC should fail with missing user_id")
	}

	// Promote with missing user_id
	_, err = PartyPromoteRPC(ctx, logger, nil, nk, `{"party_id":"test"}`)
	if err == nil {
		t.Error("PartyPromoteRPC should fail with missing user_id")
	}
}

func TestPartyRPCs_BadJSON(t *testing.T) {
	ctx := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	// Join with invalid JSON should fail
	_, err := PartyJoinRPC(ctx, logger, nil, nk, `not json`)
	if err == nil {
		t.Error("PartyJoinRPC should fail on invalid JSON")
	}

	// Kick with invalid JSON should fail
	_, err = PartyKickRPC(ctx, logger, nil, nk, `{broken`)
	if err == nil {
		t.Error("PartyKickRPC should fail on invalid JSON")
	}
}

// --- Additional tests from code review ---

func TestPartyJoinRPC_ClosedParty(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	ctx2 := partyCtxWithUser("user-2", "bob")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{"max_size":4,"open":false}`)
	var created struct{ PartyID string `json:"party_id"` }
	json.Unmarshal([]byte(resp), &created)

	_, err := PartyJoinRPC(ctx2, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)
	if err == nil {
		t.Error("expected error when joining closed party")
	}
}

func TestPartyKickRPC_SelfKick(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{}`)
	var created struct{ PartyID string `json:"party_id"` }
	json.Unmarshal([]byte(resp), &created)

	_, err := PartyKickRPC(ctx1, logger, nil, nk, `{"party_id":"`+created.PartyID+`","target_id":"user-1"}`)
	if err == nil {
		t.Error("expected error when kicking self")
	}
}

func TestPartyKickRPC_NonMember(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{}`)
	var created struct{ PartyID string `json:"party_id"` }
	json.Unmarshal([]byte(resp), &created)

	_, err := PartyKickRPC(ctx1, logger, nil, nk, `{"party_id":"`+created.PartyID+`","target_id":"user-nonexistent"}`)
	if err == nil {
		t.Error("expected error when kicking non-member")
	}
}

func TestPartyPromoteRPC_NonMember(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{}`)
	var created struct{ PartyID string `json:"party_id"` }
	json.Unmarshal([]byte(resp), &created)

	_, err := PartyPromoteRPC(ctx1, logger, nil, nk, `{"party_id":"`+created.PartyID+`","target_id":"user-ghost"}`)
	if err == nil {
		t.Error("expected error when promoting non-member")
	}
}

func TestPartyCreateRPC_MaxSizeCapped(t *testing.T) {
	ctx := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx, logger, nil, nk, `{"max_size":999}`)
	var created struct{ PartyID string `json:"party_id"` }
	json.Unmarshal([]byte(resp), &created)

	party, _ := loadParty(ctx, nk, created.PartyID)
	if party.MaxSize > 16 {
		t.Errorf("MaxSize should be capped at 16, got %d", party.MaxSize)
	}
}

func TestPartyRPCs_EmptyStringPayload(t *testing.T) {
	ctx := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	// Create with empty string — should use defaults
	resp, err := PartyCreateRPC(ctx, logger, nil, nk, "")
	if err != nil {
		t.Fatalf("Create with empty payload should use defaults: %v", err)
	}
	var created struct{ PartyID string `json:"party_id"` }
	json.Unmarshal([]byte(resp), &created)
	if created.PartyID == "" {
		t.Error("should still create a party")
	}

	// Join with empty string — should fail
	_, err = PartyJoinRPC(ctx, logger, nil, nk, "")
	if err == nil {
		t.Error("Join with empty payload should fail")
	}
}

func TestPartyJoinRPC_AlreadyMember_ReturnsError(t *testing.T) {
	ctx1 := partyCtxWithUser("user-1", "alice")
	nk := newMockPartyNK()
	logger := partyLogger()

	resp, _ := PartyCreateRPC(ctx1, logger, nil, nk, `{}`)
	var created struct{ PartyID string `json:"party_id"` }
	json.Unmarshal([]byte(resp), &created)

	_, err := PartyJoinRPC(ctx1, logger, nil, nk, `{"party_id":"`+created.PartyID+`"}`)
	if err == nil {
		t.Error("expected error when joining party you're already in")
	}
}

func TestPartyListMembersRPC_NoAuth(t *testing.T) {
	ctx := partyCtxNoUser()
	nk := newMockPartyNK()
	logger := partyLogger()

	_, err := PartyListMembersRPC(ctx, logger, nil, nk, `{"party_id":"test"}`)
	if err == nil {
		t.Error("expected error for unauthenticated list members")
	}
}
