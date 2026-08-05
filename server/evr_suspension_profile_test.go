package server

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// newSuspensionProfileIndex builds a LocalStorageIndex registered with the REAL
// production SuspensionProfile index configuration -- whatever
// (*SuspensionProfile).StorageIndexes() currently returns. Nothing here is
// hard-coded, so a regression in the production config surfaces as a test
// failure rather than being papered over by a test-local copy.
//
// No database is required: CreateIndex (storage_index.go:661), Write
// (storage_index.go:81) and the IndexOnly branch of List (storage_index.go:328)
// are all served entirely from the in-process bluge index.
func newSuspensionProfileIndex(t *testing.T) (StorageIndex, StorableIndexMeta) {
	t.Helper()

	indexes := (&SuspensionProfile{}).StorageIndexes()
	require.Len(t, indexes, 1, "SuspensionProfile should declare exactly one storage index")
	meta := indexes[0]

	si, err := NewLocalStorageIndex(logger, nil, &StorageConfig{}, metrics)
	require.NoError(t, err)

	require.NoError(t, si.CreateIndex(context.Background(), meta.Name, meta.Collection, meta.Key,
		meta.Fields, meta.SortableFields, meta.MaxEntries, meta.IndexOnly))

	return si, meta
}

// writeProfileToIndex marshals the profile exactly the way StorableWrite does
// (json.Marshal of the Storable) and pushes it through the index writer exactly
// the way storageIndexWrite does (core_storage.go:838).
func writeProfileToIndex(t *testing.T, si StorageIndex, meta StorableIndexMeta, profile *SuspensionProfile) {
	t.Helper()

	data, err := json.Marshal(profile)
	require.NoError(t, err)

	now := time.Now().UTC()
	si.Write(context.Background(), []*api.StorageObject{{
		Collection:      meta.Collection,
		Key:             meta.Key,
		UserId:          profile.UserID,
		Value:           string(data),
		Version:         "v1",
		PermissionRead:  int32(runtimeStoragePermissionOwnerRead),
		PermissionWrite: 0,
		CreateTime:      timestamppb.New(now),
		UpdateTime:      timestamppb.New(now),
	}})
}

const runtimeStoragePermissionOwnerRead = 1

// suspensionProfileFixture builds a profile carrying one active suspension.
func suspensionProfileFixture(userID, groupID string) *SuspensionProfile {
	profile := NewSuspensionProfile(userID)
	profile.Suspensions = []SuspensionProfileRecord{{
		ID:                "record-1",
		GroupID:           groupID,
		UserNotice:        "Toxic Behavior",
		CreatedAt:         time.Now().UTC().Add(-time.Hour),
		ExpiryAt:          time.Now().UTC().Add(24 * time.Hour),
		Duration:          "25h",
		EnforcerUserID:    "enforcer-1",
		EnforcerDiscordID: "1234",
	}}
	return profile
}

// TestSuspensionProfileIndex_ServesSuspensionData is the headline regression
// test for the index configuration.
//
// SuspensionProfile exists for exactly one reason: to be a pre-compiled
// projection of the enforcement journal that can be served straight out of the
// storage index. With IndexOnly: true, storage_index.go:349 returns
// idxResult.Value verbatim -- and idxResult.Value is the FIELD-FILTERED map
// built at storage_index.go:528-536, not the stored object. So if "suspensions"
// is not in Fields, the array is discarded before the document is ever written,
// and every query returns a profile with zero suspensions no matter how many
// bans the user actually has.
func TestSuspensionProfileIndex_ServesSuspensionData(t *testing.T) {
	userID := uuid.Must(uuid.NewV4()).String()
	groupID := uuid.Must(uuid.NewV4()).String()

	si, meta := newSuspensionProfileIndex(t)
	writeProfileToIndex(t, si, meta, suspensionProfileFixture(userID, groupID))

	query := fmt.Sprintf("+value.user_id:%s", Query.EscapeIndexValue(userID))
	objs, _, err := si.List(context.Background(), uuid.Nil, meta.Name, query, 10, nil, "")
	require.NoError(t, err)
	require.Len(t, objs.Objects, 1, "the suspended user's profile should be findable in the index")

	got, err := SuspensionProfileFromStorageObject(objs.Objects[0])
	require.NoError(t, err, "index payload should unmarshal as a SuspensionProfile; raw payload was: %s", objs.Objects[0].Value)

	require.Len(t, got.Suspensions, 1,
		"index served a profile with NO suspension data; raw payload was: %s", objs.Objects[0].Value)
	assert.Equal(t, "record-1", got.Suspensions[0].ID)
	assert.Equal(t, groupID, got.Suspensions[0].GroupID)
	assert.Equal(t, "Toxic Behavior", got.Suspensions[0].UserNotice)
}

// TestSuspensionProfileIndex_QueryableByGroupID pins the other half of the
// Fields contract: Fields governs what is SEARCHABLE as well as what is
// returned, because BlugeWalkDocument (storage_index.go:560) only walks the
// already-filtered map. A portal that cannot ask "who is suspended in guild X"
// cannot use this projection at all.
func TestSuspensionProfileIndex_QueryableByGroupID(t *testing.T) {
	groupID := uuid.Must(uuid.NewV4()).String()
	otherGroupID := uuid.Must(uuid.NewV4()).String()

	si, meta := newSuspensionProfileIndex(t)

	suspendedUser := uuid.Must(uuid.NewV4()).String()
	writeProfileToIndex(t, si, meta, suspensionProfileFixture(suspendedUser, groupID))
	writeProfileToIndex(t, si, meta, suspensionProfileFixture(uuid.Must(uuid.NewV4()).String(), otherGroupID))

	query := fmt.Sprintf("+value.suspensions.group_id:%s", Query.EscapeIndexValue(groupID))
	objs, _, err := si.List(context.Background(), uuid.Nil, meta.Name, query, 10, nil, "")
	require.NoError(t, err)

	require.Len(t, objs.Objects, 1, "index should be queryable by the group a suspension belongs to")
	assert.Equal(t, suspendedUser, objs.Objects[0].UserId)
}
