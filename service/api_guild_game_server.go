package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/echotools/nevr-common/gen/go/api"
	"github.com/gofrs/uuid/v5"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func (s *ApiServer) ListGuildGameServers(ctx context.Context, in *api.ListGuildGameServersRequest) (*api.GuildGameServerList, error) {

	limit := 10
	if in.GetLimit() != nil {
		if in.GetLimit().Value < 1 || in.GetLimit().Value > 100 {
			return nil, status.Error(codes.InvalidArgument, "Invalid limit - limit must be between 1 and 100.")
		}
		limit = int(in.GetLimit().Value)
	}

	if in.GetGuildGroupId() == "" {
		return nil, status.Error(codes.InvalidArgument, "Invalid guild group ID.")
	}

	if in.Query == nil {
		return nil, status.Error(codes.InvalidArgument, "Invalid query.")
	}
	query := in.Query.GetValue()
	if query == "" {
		query = "*"
	}

	results, nextCursor, err := s.storageIndex.List(ctx, uuid.Nil, StorageIndexGameServer, query, limit, nil, in.GetCursor())
	if err != nil {
		return nil, fmt.Errorf("failed to read storage objects: %w", err)
	}

	servers := make([]*api.GuildGameServerList_GuildGameServer, 0, limit)
	for _, result := range results.GetObjects() {
		var server api.GameServer
		if err := json.Unmarshal([]byte(result.GetValue()), &server); err != nil {
			return nil, fmt.Errorf("failed to unmarshal game server: %w", err)
		}
		servers = append(servers, &api.GuildGameServerList_GuildGameServer{
			GameServer: &server,
			State:      &wrapperspb.Int32Value{Value: int32(api.GuildGameServerList_GuildGameServer_STATE_UNSPECIFIED)},
		})
	}

	return &api.GuildGameServerList{GuildServers: servers, Cursor: nextCursor}, nil
}
