package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
)

const (
	PartyStorageCollection = "Parties"
	PartyMaxDefaultSize    = 4

	partyOKResponse = `{"ok":true}`
)

// StoredParty represents the persistent state of a party in Nakama storage.
type StoredParty struct {
	PartyID   string        `json:"party_id"`
	LeaderID  string        `json:"leader_id"`
	Members   []PartyMember `json:"members"`
	MaxSize   int           `json:"max_size"`
	Open      bool          `json:"open"`
	CreatedAt time.Time     `json:"created_at"`
}

// PartyMember represents a single member of a party.
type PartyMember struct {
	UserID   string `json:"user_id"`
	Username string `json:"username"`
}

// removeMemberByUserID filters out the member with the given userID and returns
// the remaining members and whether the member was found.
func removeMemberByUserID(members []PartyMember, userID string) ([]PartyMember, bool) {
	remaining := make([]PartyMember, 0, len(members))
	found := false
	for _, m := range members {
		if m.UserID == userID {
			found = true
			continue
		}
		remaining = append(remaining, m)
	}
	return remaining, found
}

func loadParty(ctx context.Context, nk runtime.NakamaModule, partyID string) (*StoredParty, error) {
	objs, err := nk.StorageRead(ctx, []*runtime.StorageRead{
		{
			Collection: PartyStorageCollection,
			Key:        partyID,
			UserID:     SystemUserID,
		},
	})
	if err != nil {
		return nil, err
	}
	if len(objs) == 0 {
		return nil, nil
	}

	var party StoredParty
	if err := json.Unmarshal([]byte(objs[0].Value), &party); err != nil {
		return nil, err
	}
	return &party, nil
}

func storeParty(ctx context.Context, nk runtime.NakamaModule, party *StoredParty) error {
	data, err := json.Marshal(party)
	if err != nil {
		return err
	}

	_, err = nk.StorageWrite(ctx, []*runtime.StorageWrite{
		{
			Collection:      PartyStorageCollection,
			Key:             party.PartyID,
			UserID:          SystemUserID,
			Value:           string(data),
			PermissionRead:  0,
			PermissionWrite: 0,
		},
	})
	return err
}

func deleteParty(ctx context.Context, nk runtime.NakamaModule, partyID string) error {
	return nk.StorageDelete(ctx, []*runtime.StorageDelete{
		{
			Collection: PartyStorageCollection,
			Key:        partyID,
			UserID:     SystemUserID,
		},
	})
}

// PartyCreateRPC creates a new party with the caller as leader and sole member.
func PartyCreateRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", StatusUnauthenticated)
	}
	username, _ := ctx.Value(runtime.RUNTIME_CTX_USERNAME).(string)

	partyID := uuid.Must(uuid.NewV4()).String()

	party := &StoredParty{
		PartyID:  partyID,
		LeaderID: userID,
		Members: []PartyMember{
			{UserID: userID, Username: username},
		},
		MaxSize:   PartyMaxDefaultSize,
		Open:      true,
		CreatedAt: time.Now().UTC(),
	}

	if err := storeParty(ctx, nk, party); err != nil {
		logger.Error("failed to store party: %v", err)
		return "", runtime.NewError("failed to create party", StatusInternalError)
	}

	resp, err := json.Marshal(map[string]string{"party_id": partyID})
	if err != nil {
		return "", runtime.NewError("failed to marshal response", StatusInternalError)
	}
	return string(resp), nil
}

// PartyJoinRPC adds the caller to an existing party.
func PartyJoinRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", StatusUnauthenticated)
	}
	username, _ := ctx.Value(runtime.RUNTIME_CTX_USERNAME).(string)

	var req struct {
		PartyID string `json:"party_id"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil || req.PartyID == "" {
		return "", runtime.NewError("party_id is required", StatusInvalidArgument)
	}

	party, err := loadParty(ctx, nk, req.PartyID)
	if err != nil {
		logger.Error("failed to load party: %v", err)
		return "", runtime.NewError("failed to load party", StatusInternalError)
	}
	if party == nil {
		return "", runtime.NewError("party not found", StatusNotFound)
	}

	if !party.Open {
		return "", runtime.NewError("party is not open", StatusPermissionDenied)
	}

	if len(party.Members) >= party.MaxSize {
		return "", runtime.NewError("party is full", StatusResourceExhausted)
	}

	for _, m := range party.Members {
		if m.UserID == userID {
			return "", runtime.NewError("already a member of this party", StatusAlreadyExists)
		}
	}

	party.Members = append(party.Members, PartyMember{UserID: userID, Username: username})

	if err := storeParty(ctx, nk, party); err != nil {
		logger.Error("failed to store party: %v", err)
		return "", runtime.NewError("failed to join party", StatusInternalError)
	}

	return partyOKResponse, nil
}

// PartyLeaveRPC removes the caller from a party. If the caller is the leader,
// leadership is promoted to the next member, or the party is deleted if empty.
func PartyLeaveRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", StatusUnauthenticated)
	}

	var req struct {
		PartyID string `json:"party_id"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil || req.PartyID == "" {
		return "", runtime.NewError("party_id is required", StatusInvalidArgument)
	}

	party, err := loadParty(ctx, nk, req.PartyID)
	if err != nil {
		logger.Error("failed to load party: %v", err)
		return "", runtime.NewError("failed to load party", StatusInternalError)
	}
	if party == nil {
		return "", runtime.NewError("party not found", StatusNotFound)
	}

	remaining, found := removeMemberByUserID(party.Members, userID)
	if !found {
		return "", runtime.NewError("not a member of this party", StatusNotFound)
	}

	if len(remaining) == 0 {
		if err := deleteParty(ctx, nk, party.PartyID); err != nil {
			logger.Error("failed to delete party: %v", err)
			return "", runtime.NewError("failed to delete party", StatusInternalError)
		}
	} else {
		party.Members = remaining
		if party.LeaderID == userID {
			party.LeaderID = remaining[0].UserID
		}
		if err := storeParty(ctx, nk, party); err != nil {
			logger.Error("failed to store party: %v", err)
			return "", runtime.NewError("failed to update party", StatusInternalError)
		}
	}

	return partyOKResponse, nil
}

// PartyKickRPC removes a target user from the party. Only the leader may kick.
func PartyKickRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", StatusUnauthenticated)
	}

	var req struct {
		PartyID  string `json:"party_id"`
		TargetID string `json:"target_id"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil || req.PartyID == "" || req.TargetID == "" {
		return "", runtime.NewError("party_id and target_id are required", StatusInvalidArgument)
	}

	party, err := loadParty(ctx, nk, req.PartyID)
	if err != nil {
		logger.Error("failed to load party: %v", err)
		return "", runtime.NewError("failed to load party", StatusInternalError)
	}
	if party == nil {
		return "", runtime.NewError("party not found", StatusNotFound)
	}

	if party.LeaderID != userID {
		return "", runtime.NewError("only the party leader can kick members", StatusPermissionDenied)
	}

	if req.TargetID == userID {
		return "", runtime.NewError("cannot kick yourself, use party/leave instead", StatusInvalidArgument)
	}

	remaining, found := removeMemberByUserID(party.Members, req.TargetID)
	if !found {
		return "", runtime.NewError("target is not a member of this party", StatusNotFound)
	}

	party.Members = remaining
	if err := storeParty(ctx, nk, party); err != nil {
		logger.Error("failed to store party: %v", err)
		return "", runtime.NewError("failed to update party", StatusInternalError)
	}

	return partyOKResponse, nil
}

// PartyPromoteRPC sets a new leader for the party. Only the current leader may promote.
func PartyPromoteRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	userID, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok || userID == "" {
		return "", runtime.NewError("unauthenticated", StatusUnauthenticated)
	}

	var req struct {
		PartyID  string `json:"party_id"`
		TargetID string `json:"target_id"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil || req.PartyID == "" || req.TargetID == "" {
		return "", runtime.NewError("party_id and target_id are required", StatusInvalidArgument)
	}

	party, err := loadParty(ctx, nk, req.PartyID)
	if err != nil {
		logger.Error("failed to load party: %v", err)
		return "", runtime.NewError("failed to load party", StatusInternalError)
	}
	if party == nil {
		return "", runtime.NewError("party not found", StatusNotFound)
	}

	if party.LeaderID != userID {
		return "", runtime.NewError("only the party leader can promote members", StatusPermissionDenied)
	}

	found := false
	for _, m := range party.Members {
		if m.UserID == req.TargetID {
			found = true
			break
		}
	}
	if !found {
		return "", runtime.NewError("target is not a member of this party", StatusNotFound)
	}

	party.LeaderID = req.TargetID
	if err := storeParty(ctx, nk, party); err != nil {
		logger.Error("failed to store party: %v", err)
		return "", runtime.NewError("failed to update party", StatusInternalError)
	}

	return partyOKResponse, nil
}

// PartyListMembersRPC returns the party state including members and leader.
func PartyListMembersRPC(ctx context.Context, logger runtime.Logger, db *sql.DB, nk runtime.NakamaModule, payload string) (string, error) {
	_, ok := ctx.Value(runtime.RUNTIME_CTX_USER_ID).(string)
	if !ok {
		return "", runtime.NewError("unauthenticated", StatusUnauthenticated)
	}

	var req struct {
		PartyID string `json:"party_id"`
	}
	if err := json.Unmarshal([]byte(payload), &req); err != nil || req.PartyID == "" {
		return "", runtime.NewError("party_id is required", StatusInvalidArgument)
	}

	party, err := loadParty(ctx, nk, req.PartyID)
	if err != nil {
		logger.Error("failed to load party: %v", err)
		return "", runtime.NewError("failed to load party", StatusInternalError)
	}
	if party == nil {
		return "", runtime.NewError("party not found", StatusNotFound)
	}

	resp, err := json.Marshal(struct {
		PartyID  string        `json:"party_id"`
		LeaderID string        `json:"leader_id"`
		Members  []PartyMember `json:"members"`
	}{
		PartyID:  party.PartyID,
		LeaderID: party.LeaderID,
		Members:  party.Members,
	})
	if err != nil {
		return "", runtime.NewError("failed to marshal response", StatusInternalError)
	}
	return string(resp), nil
}
