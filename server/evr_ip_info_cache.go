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

// IsConfigured reports whether any IP intelligence provider is wired up.
//
// Providers are only constructed inside `if redisClient != nil` in
// server/evr_pipeline.go, and redisClient is nil unless REDIS_URI is set — so a
// deployment without Redis has none at all and Get returns (nil, nil) for every
// public IP permanently, rather than transiently. Callers that report a degraded
// VPN gate use this to tell a standing configuration gap apart from an outage;
// without it, a per-authorize alert on the degraded gate fires forever and
// carries no information.
//
// Nil-safe so a caller can classify before dereferencing.
func (s *IPInfoCache) IsConfigured() bool {
	return s != nil && len(s.clients) > 0
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
			//
			// Both dependencies are optional: NewIPInfoCache does not require
			// them and existing callers pass nil (server/evr_bugfix_test.go).
			// Observability must never be the thing that panics a login path.
			if s.metrics != nil {
				s.metrics.CustomCounter("ip_info_provider_error", map[string]string{"provider": client.Name()}, 1)
			}
			if s.logger != nil {
				s.logger.Debug("IP info provider failed, failing open.",
					zap.String("provider", client.Name()), zap.Error(err))
			}
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
