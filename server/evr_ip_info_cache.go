package server

import (
	"context"
	"net"

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
	metrics Metrics

	clients []IPInfoProvider
}

func NewIPInfoCache(logger *zap.Logger, metrics Metrics, clients ...IPInfoProvider) (*IPInfoCache, error) {
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

// Get returns IP intelligence for ip from the first provider that has it.
//
// The error return is part of the IPInfoProvider shape and is always nil today:
// every provider failure is deliberately swallowed so the caller fails open
// (see SEC-6). Callers must treat a nil IPInfo as "unknown", not as "clean".
func (s *IPInfoCache) Get(ctx context.Context, ip string) (IPInfo, error) {

	// ignore reserved IPs
	if parsedIP := net.ParseIP(ip); parsedIP != nil && (parsedIP.IsLoopback() || parsedIP.IsLinkLocalUnicast() || parsedIP.IsLinkLocalMulticast() || parsedIP.IsMulticast() || parsedIP.IsPrivate()) {
		return &StubIPInfo{}, nil
	}

	for _, client := range s.clients {
		result, err := client.Get(ctx, ip)
		if err != nil {
			// SEC-6: provider errors used to be collected into a map that was
			// never read, so this method could never return a non-nil error and
			// the degradation was invisible. The fail-open policy is unchanged —
			// a failed lookup still yields no IP info rather than a denial — but
			// it is now counted so it can be alerted on.
			s.metrics.CustomCounter("ip_info_provider_error", map[string]string{"provider": client.Name()}, 1)
			s.logger.Debug("IP info provider failed, failing open.",
				zap.String("provider", client.Name()), zap.Error(err))
			continue
		}
		if result != nil {
			return result, nil
		}
	}
	return nil, nil
}

// IsVPN reports whether the IP is a known VPN. It fails open: an unavailable or
// failed lookup yields false. Get never returns a non-nil error (every failure
// path is counted and swallowed above), so there is no error branch here.
func (s *IPInfoCache) IsVPN(ip string) bool {
	result, _ := s.Get(s.ctx, ip)
	if result == nil {
		return false
	}
	return result.IsVPN()
}
