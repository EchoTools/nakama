package server

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gofrs/uuid/v5"
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

// TestEVRProfile_NilAccount_GettersReturnZeroValues exercises every EVRProfile
// getter that accesses the unexported account field, using a profile whose
// account is nil. Each getter must return its zero value without panicking.
func TestEVRProfile_NilAccount_GettersReturnZeroValues(t *testing.T) {
	p := EVRProfile{} // account is nil

	t.Run("UserID", func(t *testing.T) {
		require.Equal(t, "", p.UserID())
	})
	t.Run("ID", func(t *testing.T) {
		require.Equal(t, "", p.ID())
	})
	t.Run("Username", func(t *testing.T) {
		require.Equal(t, "", p.Username())
	})
	t.Run("DisplayName", func(t *testing.T) {
		require.Equal(t, "", p.DisplayName())
	})
	t.Run("Wallet", func(t *testing.T) {
		require.Equal(t, "", p.Wallet())
	})
	t.Run("LangTag", func(t *testing.T) {
		require.Equal(t, "", p.LangTag())
	})
	t.Run("AvatarURL", func(t *testing.T) {
		require.Equal(t, "", p.AvatarURL())
	})
	t.Run("DiscordID", func(t *testing.T) {
		require.Equal(t, "", p.DiscordID())
	})
	t.Run("IsDisabled", func(t *testing.T) {
		require.False(t, p.IsDisabled())
	})
	t.Run("IsLinked", func(t *testing.T) {
		require.False(t, p.IsLinked())
	})
	t.Run("IsOnline", func(t *testing.T) {
		require.False(t, p.IsOnline())
	})
	t.Run("HasPasswordSet", func(t *testing.T) {
		require.False(t, p.HasPasswordSet())
	})
	t.Run("XPIDs", func(t *testing.T) {
		require.Nil(t, p.XPIDs())
	})
	t.Run("LinkedXPIDs", func(t *testing.T) {
		require.Nil(t, p.LinkedXPIDs())
	})
	t.Run("CreatedAt", func(t *testing.T) {
		require.Equal(t, time.Time{}, p.CreatedAt())
	})
	t.Run("UpdatedAt", func(t *testing.T) {
		require.Equal(t, time.Time{}, p.UpdatedAt())
	})
	t.Run("GetActiveGroupID", func(t *testing.T) {
		require.Equal(t, uuid.Nil, p.GetActiveGroupID())
	})
	t.Run("DisplayNamesByGroupID", func(t *testing.T) {
		result := p.DisplayNamesByGroupID()
		require.NotNil(t, result)
		require.Empty(t, result)
	})
}
