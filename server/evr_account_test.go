package server

import (
	"context"
	"errors"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
)

type mockProfileLoadNakamaModule struct {
	runtime.NakamaModule
	account        *api.Account
	storageReadErr error
	storageObjs    []*api.StorageObject
}

func (m *mockProfileLoadNakamaModule) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return m.account, nil
}

func (m *mockProfileLoadNakamaModule) StorageRead(ctx context.Context, reads []*runtime.StorageRead) ([]*api.StorageObject, error) {
	if m.storageReadErr != nil {
		return nil, m.storageReadErr
	}
	return m.storageObjs, nil
}

func TestBuildEVRProfileFromAccount_NullMetadata(t *testing.T) {
	profile, err := BuildEVRProfileFromAccount(&api.Account{User: &api.User{Metadata: "null"}})
	require.NoError(t, err)
	require.NotNil(t, profile)
	require.NotNil(t, profile.InGameNames)
	require.NotNil(t, profile.MutedPlayers)
	require.NotNil(t, profile.GhostedPlayers)
	require.NotNil(t, profile.NewUnlocks)
}

func TestEVRProfileLoad_DoesNotFallbackOnStorageReadError(t *testing.T) {
	nk := &mockProfileLoadNakamaModule{
		account:        &api.Account{User: &api.User{Metadata: `{"active_group_id":"g1"}`}},
		storageReadErr: errors.New("storage unavailable"),
	}

	profile, err := EVRProfileLoad(context.Background(), nk, "user-1")
	require.Error(t, err)
	require.Nil(t, profile)
}

func TestEVRProfileLoad_FallsBackToAccountMetadataOnNotFound(t *testing.T) {
	nk := &mockProfileLoadNakamaModule{
		account: &api.Account{User: &api.User{Metadata: `{"active_group_id":"g1"}`}},
	}

	profile, err := EVRProfileLoad(context.Background(), nk, "user-1")
	require.NoError(t, err)
	require.NotNil(t, profile)
	require.Equal(t, "g1", profile.ActiveGroupID)
}
