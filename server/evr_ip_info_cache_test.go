package server

import (
	"context"
	"os"
	"testing"

	"go.uber.org/zap"
)

// TestIPInfoCacheNoProviders tests that IPInfoCache works correctly with no providers
func TestIPInfoCacheNoProviders(t *testing.T) {
	logger := NewConsoleLogger(os.Stdout, true)
	metrics := &LocalMetrics{}

	// Create cache with no providers
	cache, err := NewIPInfoCache(logger, metrics)
	if err != nil {
		t.Fatalf("NewIPInfoCache failed with no providers: %v", err)
	}

	if cache == nil {
		t.Fatal("NewIPInfoCache returned nil cache")
	}

	ctx := context.Background()

	// Test Get with public IP
	ipInfo, err := cache.Get(ctx, "8.8.8.8")
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	// With no providers, Get should return nil
	if ipInfo != nil {
		t.Errorf("Expected nil ipInfo, got: %v", ipInfo)
	}

	// Test IsVPN with public IP
	isVPN := cache.IsVPN("8.8.8.8")
	if isVPN {
		t.Error("IsVPN should return false when no providers are configured")
	}

	// Test with private IP (should return stub)
	ipInfo, err = cache.Get(ctx, "192.168.1.1")
	if err != nil {
		t.Errorf("Get failed for private IP: %v", err)
	}
	// Private IPs should return StubIPInfo
	if ipInfo == nil {
		t.Error("Expected StubIPInfo for private IP, got nil")
	}
	if ipInfo != nil && ipInfo.DataProvider() != "Dummy" {
		t.Errorf("Expected Dummy provider for private IP, got: %s", ipInfo.DataProvider())
	}

	// Test IsVPN with private IP
	isVPN = cache.IsVPN("192.168.1.1")
	if isVPN {
		t.Error("IsVPN should return false for private IP")
	}
}

// TestIPInfoCacheWithStubProvider tests IPInfoCache with a stub provider
func TestIPInfoCacheWithStubProvider(t *testing.T) {
	logger := NewConsoleLogger(os.Stdout, true)
	metrics := &LocalMetrics{}

	// Create a stub provider
	stubProvider := &stubIPInfoProvider{}
	cache, err := NewIPInfoCache(logger, metrics, stubProvider)
	if err != nil {
		t.Fatalf("NewIPInfoCache failed: %v", err)
	}

	if cache == nil {
		t.Fatal("NewIPInfoCache returned nil cache")
	}

	ctx := context.Background()

	// Test Get with public IP
	ipInfo, err := cache.Get(ctx, "8.8.8.8")
	if err != nil {
		t.Errorf("Get failed: %v", err)
	}
	if ipInfo == nil {
		t.Error("Expected ipInfo from stub provider, got nil")
	}
	if ipInfo != nil && ipInfo.DataProvider() != "Stub" {
		t.Errorf("Expected Stub provider, got: %s", ipInfo.DataProvider())
	}
}

// stubIPInfoProvider is a test provider
type stubIPInfoProvider struct{}

func (s *stubIPInfoProvider) Name() string {
	return "Stub"
}

func (s *stubIPInfoProvider) Get(ctx context.Context, ip string) (IPInfo, error) {
	return &stubProviderIPInfo{}, nil
}

// stubProviderIPInfo implements IPInfo for testing
type stubProviderIPInfo struct{}

func (s *stubProviderIPInfo) DataProvider() string {
	return "Stub"
}

func (s *stubProviderIPInfo) IsVPN() bool {
	return false
}

func (s *stubProviderIPInfo) Latitude() float64 {
	return 37.7749
}

func (s *stubProviderIPInfo) Longitude() float64 {
	return -122.4194
}

func (s *stubProviderIPInfo) City() string {
	return "San Francisco"
}

func (s *stubProviderIPInfo) Region() string {
	return "CA"
}

func (s *stubProviderIPInfo) CountryCode() string {
	return "US"
}

func (s *stubProviderIPInfo) GeoHash(geoPrecision uint) string {
	return "9q8yy"
}

func (s *stubProviderIPInfo) ASN() int {
	return 15169
}

func (s *stubProviderIPInfo) FraudScore() int {
	return 0
}

func (s *stubProviderIPInfo) ISP() string {
	return "Google LLC"
}

func (s *stubProviderIPInfo) Organization() string {
	return "Google LLC"
}
