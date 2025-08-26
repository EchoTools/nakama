package service

import (
	"context"
	"net"

	"github.com/heroiclabs/nakama/v3/server"
	"go.uber.org/zap"
)

type IPInfoProvider interface {
	Name() string
	Get(ctx context.Context, ip string) (IPInfo, error)
}

type IPInfoCache struct {
	ctx      context.Context
	cancelFn context.CancelFunc

	logger  *zap.Logger
	metrics server.Metrics

	clients []IPInfoProvider
}

func NewIPInfoCache(logger *zap.Logger, metrics server.Metrics, clients ...IPInfoProvider) (*IPInfoCache, error) {
	ctx, cancelFn := context.WithCancel(context.Background())

	ipqs := IPInfoCache{
		ctx:      ctx,
		cancelFn: cancelFn,

		logger:  logger,
		metrics: metrics,

		clients: clients,
	}

	return &ipqs, nil
}

func (s *IPInfoCache) Get(ctx context.Context, ip string) (IPInfo, error) {

	// ignore reserved IPs
	if ip := net.ParseIP(ip); ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsPrivate() {
		return &StubIPInfo{}, nil
	}

	errored := make(map[string]error, 0)

	for _, client := range s.clients {
		result, err := client.Get(ctx, ip)
		if err != nil {
			errored[client.Name()] = err
			continue
		}
		if result != nil {
			return result, nil
		}
	}
	return nil, nil
}

func (s *IPInfoCache) IsVPN(ip string) bool {

	result, err := s.Get(s.ctx, ip)
	if err != nil {
		s.logger.Warn("Failed to get IPQS details, failing open.", zap.Error(err))
		return false
	}
	if result == nil {
		return false
	}
	return result.IsVPN()
}
