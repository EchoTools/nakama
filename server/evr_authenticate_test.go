package server

import (
	"context"
	"testing"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
)

type MockNakamaModule struct {
	runtime.NakamaModule
}

func (m *MockNakamaModule) UsersGetUsername(ctx context.Context, usernames []string) ([]*api.User, error) {
	return []*api.User{
		{
			Username: "OtherUsername1",
		},
		{
			Username: "OtherUsername2",
		},
	}, nil
}

func (m *MockNakamaModule) StorageRead(ctx context.Context, keys []*runtime.StorageRead) ([]*api.StorageObject, error) {
	return []*api.StorageObject{
		{
			UserId:     uuid.Must(uuid.NewV4()).String(),
			Collection: DisplayNameCollection,
			Key:        "OtherDisplayName1",
			Value:      "{}",
		},
		{
			UserId:     uuid.Must(uuid.NewV4()).String(),
			Collection: DisplayNameCollection,
			Key:        "OtherDisplayName2",
			Value:      "{}",
		},
	}, nil
}

func (m *MockNakamaModule) MultiUpdate(ctx context.Context, accountUpdates []*runtime.AccountUpdate, storageWrites []*runtime.StorageWrite, storageDeletes []*runtime.StorageDelete, walletUpdates []*runtime.WalletUpdate, updateLedger bool) ([]*api.StorageObjectAck, []*runtime.WalletUpdateResult, error) {
	return nil, nil, nil
}

func TestSetDisplayNameByPriority(t *testing.T) {
	userId := uuid.Must(uuid.NewV4()).String()
	type args struct {
		ctx      context.Context
		nk       runtime.NakamaModule
		userId   string
		username string
		options  []string
	}
	tests := []struct {
		name    string
		args    args
		want    string
		wantErr bool
	}{
		{
			"Test DisplayName matches Username",
			args{
				context.Background(),
				&MockNakamaModule{},
				userId,
				"TestUsername",
				[]string{"ThisUsername", "Test2"},
			},
			"ThisUsername",
			false,
		},
		{
			"Test Options match other usernames",
			args{
				context.Background(),
				&MockNakamaModule{},
				userId,
				"TestUsername",
				[]string{"", "@!#$!@#$", "!", "WantedDisplayName", "OtherDisplayName1", "OtherDisplayName2", "", "OtherUsername2", "OtherDisplayName1", "WantedDisplayName"},
			},
			"WantedDisplayName",
			false,
		},
		{
			"Test Options numbers in name",
			args{
				context.Background(),
				&MockNakamaModule{},
				userId,
				"TestUsername",
				[]string{"", "015A", "015a", "015a", "015a"},
			},
			"015A",
			false,
		},
		// TODO: Add test cases.
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := SetDisplayNameByPriority(tt.args.ctx, tt.args.nk, tt.args.userId, tt.args.username, tt.args.options)
			if (err != nil) != tt.wantErr {
				t.Errorf("SetDisplayNameByPriority() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("SetDisplayNameByPriority() = %v, want %v", got, tt.want)
			}
		})
	}
}

func Test_sanitizeDisplayName(t *testing.T) {
	type args struct {
		displayName string
	}
	tests := []struct {
		name string
		args args
		want string
	}{
		{
			"Test DisplayName with special characters",
			args{
				"!@#$%^&*()_+",
			},
			"",
		},
		{
			"Test DisplayName with numbers",
			args{
				"1234567890",
			},
			"",
		},
		{
			"Test DisplayName with numbers and letters",
			args{
				"015A",
			},
			"015A",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitizeDisplayName(tt.args.displayName); got != tt.want {
				t.Errorf("sanitizeDisplayName() = %v, want %v", got, tt.want)
			}
		})
	}
}
