package server

import (
	"context"
	"errors"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/stretchr/testify/require"
)

type matchHistoryCleanupNakamaModule struct {
	runtime.NakamaModule
	deletedKeys []string
}

func (m *matchHistoryCleanupNakamaModule) StorageDelete(ctx context.Context, deletes []*runtime.StorageDelete) error {
	for _, d := range deletes {
		m.deletedKeys = append(m.deletedKeys, d.Key)
	}
	return nil
}

func TestRunWithStoredMatchLabelCleanup_DeletesOnProcessingError(t *testing.T) {
	nk := &matchHistoryCleanupNakamaModule{}
	matchID := MatchID{UUID: uuid.Must(uuid.NewV4()), Node: "node"}
	wantErr := errors.New("processing failed")

	err := runWithStoredMatchLabelCleanup(context.Background(), &mockLogger{}, nk, matchID, func() error {
		return wantErr
	})

	require.ErrorIs(t, err, wantErr)
	require.Equal(t, []string{matchID.UUID.String()}, nk.deletedKeys)
}
