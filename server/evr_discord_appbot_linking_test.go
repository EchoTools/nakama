package server

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/heroiclabs/nakama-common/api"
	"github.com/heroiclabs/nakama-common/runtime"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// linkingMockNK implements a minimal mock of the NakamaModule for linking tests.
type linkingMockNK struct {
	runtime.NakamaModule

	authenticateCustomFunc func(ctx context.Context, id, username string, create bool) (string, string, bool, error)
	usersGetUsernameFunc   func(ctx context.Context, usernames []string) ([]*api.User, error)
	accountGetIdFunc       func(ctx context.Context, userID string) (*api.Account, error)
	accountUpdateIdFunc    func(ctx context.Context, userID, username string, metadata map[string]interface{}, displayName, timezone, location, langTag, avatarUrl string) error
}

func (m *linkingMockNK) AuthenticateCustom(ctx context.Context, id, username string, create bool) (string, string, bool, error) {
	return m.authenticateCustomFunc(ctx, id, username, create)
}

func (m *linkingMockNK) UsersGetUsername(ctx context.Context, usernames []string) ([]*api.User, error) {
	return m.usersGetUsernameFunc(ctx, usernames)
}

func (m *linkingMockNK) AccountGetId(ctx context.Context, userID string) (*api.Account, error) {
	return m.accountGetIdFunc(ctx, userID)
}

func (m *linkingMockNK) AccountUpdateId(ctx context.Context, userID, username string, metadata map[string]interface{}, displayName, timezone, location, langTag, avatarUrl string) error {
	return m.accountUpdateIdFunc(ctx, userID, username, metadata, displayName, timezone, location, langTag, avatarUrl)
}

func TestLinkAuthenticateWithUsernameConflict(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name           string
		discordID      string
		username       string
		mock           *linkingMockNK
		wantUserID     string
		wantUsername   string
		wantErr        bool
		wantErrContain string
	}{
		{
			name:      "no conflict - succeeds normally",
			discordID: "discord123",
			username:  "alice",
			mock: &linkingMockNK{
				authenticateCustomFunc: func(ctx context.Context, id, username string, create bool) (string, string, bool, error) {
					return "user-abc", "alice", true, nil
				},
			},
			wantUserID:   "user-abc",
			wantUsername: "alice",
		},
		{
			name:      "username conflict - existing account renamed, retry succeeds",
			discordID: "discord-new",
			username:  "alice",
			mock: func() *linkingMockNK {
				callCount := 0
				return &linkingMockNK{
					authenticateCustomFunc: func(ctx context.Context, id, username string, create bool) (string, string, bool, error) {
						callCount++
						if callCount == 1 {
							return "", "", false, status.Error(codes.AlreadyExists, "Username is already in use.")
						}
						return "new-user-id", "alice", true, nil
					},
					usersGetUsernameFunc: func(ctx context.Context, usernames []string) ([]*api.User, error) {
						return []*api.User{{Id: "conflict-user-id", Username: "alice"}}, nil
					},
					accountGetIdFunc: func(ctx context.Context, userID string) (*api.Account, error) {
						return &api.Account{
							User:     &api.User{Id: "conflict-user-id", Username: "alice"},
							CustomId: "discord-other",
						}, nil
					},
					accountUpdateIdFunc: func(ctx context.Context, userID, username string, metadata map[string]interface{}, displayName, timezone, location, langTag, avatarUrl string) error {
						if userID != "conflict-user-id" {
							t.Errorf("expected rename of conflict-user-id, got %s", userID)
						}
						if username != "alice_conf" {
							t.Errorf("expected renamed username alice_conf, got %s", username)
						}
						return nil
					},
				}
			}(),
			wantUserID:   "new-user-id",
			wantUsername: "alice",
		},
		{
			name:      "conflicting account has same discord custom_id - returns existing account",
			discordID: "discord-same",
			username:  "alice",
			mock: &linkingMockNK{
				authenticateCustomFunc: func() func(ctx context.Context, id, username string, create bool) (string, string, bool, error) {
					callCount := 0
					return func(ctx context.Context, id, username string, create bool) (string, string, bool, error) {
						callCount++
						if callCount == 1 {
							return "", "", false, status.Error(codes.AlreadyExists, "Username is already in use.")
						}
						return "existing-user-id", "alice", false, nil
					}
				}(),
				usersGetUsernameFunc: func(ctx context.Context, usernames []string) ([]*api.User, error) {
					return []*api.User{{Id: "existing-user-id", Username: "alice"}}, nil
				},
				accountGetIdFunc: func(ctx context.Context, userID string) (*api.Account, error) {
					return &api.Account{
						User:     &api.User{Id: "existing-user-id", Username: "alice"},
						CustomId: "discord-same",
					}, nil
				},
				accountUpdateIdFunc: func(ctx context.Context, userID, username string, metadata map[string]interface{}, displayName, timezone, location, langTag, avatarUrl string) error {
					t.Error("AccountUpdateId should not be called when conflict is same discord user")
					return nil
				},
			},
			wantUserID:   "existing-user-id",
			wantUsername: "alice",
		},
		{
			name:      "rename fails - returns actionable error",
			discordID: "discord-new",
			username:  "alice",
			mock: &linkingMockNK{
				authenticateCustomFunc: func(ctx context.Context, id, username string, create bool) (string, string, bool, error) {
					return "", "", false, status.Error(codes.AlreadyExists, "Username is already in use.")
				},
				usersGetUsernameFunc: func(ctx context.Context, usernames []string) ([]*api.User, error) {
					return []*api.User{{Id: "conflict-user-id", Username: "alice"}}, nil
				},
				accountGetIdFunc: func(ctx context.Context, userID string) (*api.Account, error) {
					return &api.Account{
						User:     &api.User{Id: "conflict-user-id", Username: "alice"},
						CustomId: "discord-other",
					}, nil
				},
				accountUpdateIdFunc: func(ctx context.Context, userID, username string, metadata map[string]interface{}, displayName, timezone, location, langTag, avatarUrl string) error {
					return fmt.Errorf("database error")
				},
			},
			wantErr:        true,
			wantErrContain: "failed to resolve username conflict",
		},
		{
			name:      "conflict lookup returns no results - retry without rename",
			discordID: "discord-new",
			username:  "alice",
			mock: func() *linkingMockNK {
				callCount := 0
				return &linkingMockNK{
					authenticateCustomFunc: func(ctx context.Context, id, username string, create bool) (string, string, bool, error) {
						callCount++
						if callCount == 1 {
							return "", "", false, status.Error(codes.AlreadyExists, "Username is already in use.")
						}
						return "new-user-id", "alice", true, nil
					},
					usersGetUsernameFunc: func(ctx context.Context, usernames []string) ([]*api.User, error) {
						return []*api.User{}, nil
					},
				}
			}(),
			wantUserID:   "new-user-id",
			wantUsername: "alice",
		},
		{
			name:      "retry after rename still fails - returns actionable error",
			discordID: "discord-new",
			username:  "alice",
			mock: &linkingMockNK{
				authenticateCustomFunc: func(ctx context.Context, id, username string, create bool) (string, string, bool, error) {
					return "", "", false, status.Error(codes.AlreadyExists, "Username is already in use.")
				},
				usersGetUsernameFunc: func(ctx context.Context, usernames []string) ([]*api.User, error) {
					return []*api.User{{Id: "conflict-user-id", Username: "alice"}}, nil
				},
				accountGetIdFunc: func(ctx context.Context, userID string) (*api.Account, error) {
					return &api.Account{
						User:     &api.User{Id: "conflict-user-id", Username: "alice"},
						CustomId: "discord-other",
					}, nil
				},
				accountUpdateIdFunc: func(ctx context.Context, userID, username string, metadata map[string]interface{}, displayName, timezone, location, langTag, avatarUrl string) error {
					return nil
				},
			},
			wantErr:        true,
			wantErrContain: "failed to create account",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userID, username, err := authenticateOrResolveConflict(ctx, tt.mock, tt.discordID, tt.username)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected error, got nil")
				}
				if tt.wantErrContain != "" && !strings.Contains(err.Error(), tt.wantErrContain) {
					t.Errorf("error %q does not contain %q", err.Error(), tt.wantErrContain)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if userID != tt.wantUserID {
				t.Errorf("userID = %q, want %q", userID, tt.wantUserID)
			}
			if username != tt.wantUsername {
				t.Errorf("username = %q, want %q", username, tt.wantUsername)
			}
		})
	}
}
