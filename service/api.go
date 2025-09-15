package service

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"strings"

	"github.com/echotools/nevr-common/gen/go/apigrpc"
	grpcgw "github.com/grpc-ecosystem/grpc-gateway/v2/runtime"
	"github.com/heroiclabs/nakama-common/runtime"
	"github.com/heroiclabs/nakama/v3/server"
	"github.com/heroiclabs/nakama/v3/social"
	"go.uber.org/zap"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"
	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/reflection"
	"google.golang.org/protobuf/encoding/protojson"
)

type ApiServer struct {
	apigrpc.UnimplementedNevrServiceServer
	logger               *zap.Logger
	db                   *sql.DB
	config               server.Config
	version              string
	socialClient         *social.Client
	storageIndex         server.StorageIndex
	leaderboardCache     server.LeaderboardCache
	leaderboardRankCache server.LeaderboardRankCache
	sessionCache         server.SessionCache
	sessionRegistry      server.SessionRegistry
	statusRegistry       server.StatusRegistry
	matchRegistry        server.MatchRegistry
	partyRegistry        server.PartyRegistry
	tracker              server.Tracker
	router               server.MessageRouter
	streamManager        server.StreamManager
	metrics              server.Metrics
	matchmaker           server.Matchmaker
	runtime              *server.Runtime
	grpcServer           *grpc.Server
	grpcGatewayServer    *http.Server
}

func InitApiServer(ctx context.Context, db *sql.DB, config server.Config, logger *zap.Logger, version string,
	socialClient *social.Client, storageIndex server.StorageIndex,
	leaderboardCache server.LeaderboardCache, leaderboardRankCache server.LeaderboardRankCache,
	sessionCache server.SessionCache, sessionRegistry server.SessionRegistry,
	statusRegistry server.StatusRegistry, matchRegistry server.MatchRegistry,
	partyRegistry server.PartyRegistry, tracker server.Tracker,
	router server.MessageRouter, streamManager server.StreamManager,
	metrics server.Metrics, matchmaker server.Matchmaker,
	initializer runtime.Initializer) error {
	// Construct the listener for the apigrpc NevrService over h2c (gRPC over HTTP/1.1)
	grpcServer := grpc.NewServer()
	// Optional: enable reflection in non-prod
	reflection.Register(grpcServer)

	nevrSvc := &ApiServer{
		logger:               logger,
		db:                   db,
		config:               config,
		version:              version,
		socialClient:         socialClient,
		storageIndex:         storageIndex,
		leaderboardCache:     leaderboardCache,
		leaderboardRankCache: leaderboardRankCache,
		sessionCache:         sessionCache,
		sessionRegistry:      sessionRegistry,
		statusRegistry:       statusRegistry,
		matchRegistry:        matchRegistry,
		partyRegistry:        partyRegistry,
		tracker:              tracker,
		router:               router,
		streamManager:        streamManager,
		metrics:              metrics,
		matchmaker:           matchmaker,
		runtime:              nil,
		grpcServer:           grpcServer,
	}

	apigrpc.RegisterNevrServiceServer(grpcServer, nevrSvc)

	grpcGateway := grpcgw.NewServeMux(
		grpcgw.WithRoutingErrorHandler(handleRoutingError),
		grpcgw.WithMetadata(func(ctx context.Context, r *http.Request) metadata.MD {
			// For RPC GET operations pass through any custom query parameters.
			if r.Method != http.MethodGet || !strings.HasPrefix(r.URL.Path, "/v2/rpc/") {
				return metadata.MD{}
			}

			q := r.URL.Query()
			p := make(map[string][]string, len(q))
			for k, vs := range q {
				if k == "http_key" {
					// Skip Nakama's own query params, only process custom ones.
					continue
				}
				p["q_"+k] = vs
			}
			return p
		}),
		grpcgw.WithMarshalerOption(grpcgw.MIMEWildcard, &grpcgw.HTTPBodyMarshaler{
			Marshaler: &grpcgw.JSONPb{
				MarshalOptions: protojson.MarshalOptions{
					UseProtoNames:  true,
					UseEnumNumbers: true,
				},
				UnmarshalOptions: protojson.UnmarshalOptions{
					DiscardUnknown: true,
				},
			},
		}),
	)

	h2Handler := h2c.NewHandler(http.HandlerFunc(grpcServer.ServeHTTP), &http2.Server{})

	// CORS-friendly wrapper for preflight
	nevrAPIHandler := func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodOptions {
			// Minimal preflight response; adjust headers as needed.
			w.WriteHeader(http.StatusNoContent)
			return
		}
		h2Handler.ServeHTTP(w, r)
	}

	if err := initializer.RegisterHttp("/v3/{id:.*}", nevrAPIHandler, http.MethodGet, http.MethodPost); err != nil {
		return fmt.Errorf("unable to register /evr/api service: %w", err)
	}
	nevrSvc.grpcGatewayServer = &http.Server{
		Addr:    fmt.Sprintf("%s:%d", config.GetSocket().GetAddress(), config.GetSocket().GetPort()+1),
		Handler: grpcGateway,
	}

	go func() {
		if err := nevrSvc.grpcGatewayServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Fatal("failed to start nevr api grpc gateway server", zap.Error(err))
		}
	}()

	return nil
}
