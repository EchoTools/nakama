package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/go-redis/redis"
	"github.com/mmcloughlin/geohash"
	"go.uber.org/zap"
)

const (
	IPQSRedisKeyPrefix = "ipqs:"
	IPQSRedisKeyTTL    = time.Hour * 24 * 180
)

type IPQSTransactionDetails struct {
	ValidBillingAddress       bool     `json:"valid_billing_address,omitempty"`
	ValidShippingAddress      bool     `json:"valid_shipping_address,omitempty"`
	ValidBillingEmail         bool     `json:"valid_billing_email,omitempty"`
	ValidShippingEmail        bool     `json:"valid_shipping_email,omitempty"`
	RiskyBillingPhone         bool     `json:"risky_billing_phone,omitempty"`
	RiskyShippingPhone        bool     `json:"risky_shipping_phone,omitempty"`
	BillingPhoneCarrier       string   `json:"billing_phone_carrier,omitempty"`
	ShippingPhoneCarrier      string   `json:"shipping_phone_carrier,omitempty"`
	BillingPhoneLineType      string   `json:"billing_phone_line_type,omitempty"`
	ShippingPhoneLineType     string   `json:"shipping_phone_line_type,omitempty"`
	BillingPhoneCountry       string   `json:"billing_phone_country,omitempty"`
	BillingPhoneCountryCode   string   `json:"billing_phone_country_code,omitempty"`
	ShippingPhoneCountry      string   `json:"shipping_phone_country,omitempty"`
	ShippingPhoneCountryCode  string   `json:"shipping_phone_country_code,omitempty"`
	FraudulentBehavior        bool     `json:"fraudulent_behavior,omitempty"`
	BinCountry                string   `json:"bin_country,omitempty"`
	BinType                   string   `json:"bin_type,omitempty"`
	BinBankName               string   `json:"bin_bank_name,omitempty"`
	RiskScore                 int      `json:"risk_score,omitempty"`
	RiskFactors               []string `json:"risk_factors,omitempty"`
	IsPrepaidCard             bool     `json:"is_prepaid_card,omitempty"`
	RiskyUsername             bool     `json:"risky_username,omitempty"`
	ValidBillingPhone         bool     `json:"valid_billing_phone,omitempty"`
	ValidShippingPhone        bool     `json:"valid_shipping_phone,omitempty"`
	LeakedBillingEmail        bool     `json:"leaked_billing_email,omitempty"`
	LeakedShippingEmail       bool     `json:"leaked_shipping_email,omitempty"`
	LeakedUserData            bool     `json:"leaked_user_data,omitempty"`
	UserActivity              string   `json:"user_activity,omitempty"`
	PhoneNameIdentityMatch    string   `json:"phone_name_identity_match,omitempty"`
	PhoneEmailIdentityMatch   string   `json:"phone_email_identity_match,omitempty"`
	PhoneAddressIdentityMatch string   `json:"phone_address_identity_match,omitempty"`
	EmailNameIdentityMatch    string   `json:"email_name_identity_match,omitempty"`
	NameAddressIdentityMatch  string   `json:"name_address_identity_match,omitempty"`
	AddressEmailIdentityMatch string   `json:"address_email_identity_match,omitempty"`
}

type IPQSResponse struct {
	Message            string                 `json:"message,omitempty"`
	Success            bool                   `json:"success,omitempty"`
	Proxy              bool                   `json:"proxy,omitempty"`
	ISP                string                 `json:"ISP,omitempty"`
	Organization       string                 `json:"organization,omitempty"`
	ASN                int                    `json:"ASN,omitempty"`
	Host               string                 `json:"host,omitempty"`
	CountryCode        string                 `json:"country_code,omitempty"`
	City               string                 `json:"city,omitempty"`
	Region             string                 `json:"region,omitempty"`
	IsCrawler          bool                   `json:"is_crawler,omitempty"`
	ConnectionType     string                 `json:"connection_type,omitempty"`
	Latitude           float64                `json:"latitude,omitempty"`
	Longitude          float64                `json:"longitude,omitempty"`
	ZipCode            string                 `json:"zip_code,omitempty"`
	Timezone           string                 `json:"timezone,omitempty"`
	VPN                bool                   `json:"vpn,omitempty"`
	Tor                bool                   `json:"tor,omitempty"`
	ActiveVPN          bool                   `json:"active_vpn,omitempty"`
	ActiveTor          bool                   `json:"active_tor,omitempty"`
	RecentAbuse        bool                   `json:"recent_abuse,omitempty"`
	FrequentAbuser     bool                   `json:"frequent_abuser,omitempty"`
	HighRiskAttacks    bool                   `json:"high_risk_attacks,omitempty"`
	AbuseVelocity      string                 `json:"abuse_velocity,omitempty"`
	BotStatus          bool                   `json:"bot_status,omitempty"`
	SharedConnection   bool                   `json:"shared_connection,omitempty"`
	DynamicConnection  bool                   `json:"dynamic_connection,omitempty"`
	SecurityScanner    bool                   `json:"security_scanner,omitempty"`
	TrustedNetwork     bool                   `json:"trusted_network,omitempty"`
	Mobile             bool                   `json:"mobile,omitempty"`
	FraudScore         int                    `json:"fraud_score,omitempty"`
	OperatingSystem    string                 `json:"operating_system,omitempty"`
	Browser            string                 `json:"browser,omitempty"`
	DeviceModel        string                 `json:"device_model,omitempty"`
	DeviceBrand        string                 `json:"device_brand,omitempty"`
	TransactionDetails IPQSTransactionDetails `json:"transaction_details,omitempty"`
	RequestID          string                 `json:"request_id,omitempty"`
}

var _ = IPInfo(&IPQSData{})

type IPQSData struct {
	Response IPQSResponse `json:"response,omitempty"`
}

func (r *IPQSData) DataProvider() string {
	return "IPQS"
}

func (r *IPQSData) IsVPN() bool {
	if r.IsSharedIP() {
		return false
	}
	return r.Response.VPN
}

func (r *IPQSData) IsSharedIP() bool {
	return isKnownSharedIPProvider(r.Response.ISP, r.Response.Organization)
}

func (r *IPQSData) Latitude() float64 {
	return r.Response.Latitude
}
func (r *IPQSData) Longitude() float64 {
	return r.Response.Longitude
}

func (r *IPQSData) City() string {
	return r.Response.City
}

func (r *IPQSData) Region() string {
	return r.Response.Region
}

func (r *IPQSData) CountryCode() string {
	return r.Response.CountryCode
}

func (r *IPQSData) GeoHash(geoPrecision uint) string {
	return geohash.EncodeWithPrecision(r.Latitude(), r.Longitude(), 2)
}

func (r *IPQSData) ASN() int {
	return r.Response.ASN
}

func (r *IPQSData) FraudScore() int {
	return r.Response.FraudScore
}

func (r *IPQSData) ISP() string {
	return r.Response.ISP
}

func (r *IPQSData) Organization() string {
	return r.Response.Organization
}

var _ = IPInfoProvider(&IPQSClient{})

type IPQSClient struct {
	ctx      context.Context
	cancelFn context.CancelFunc

	logger  *zap.Logger
	metrics Metrics

	redisClient *redis.Client

	apiKey     string
	parameters string

	mu              sync.Mutex
	lastFailureTime time.Time
	backoffDuration     time.Duration
	circuitOpen         bool
}

const (
	ipqsInitialBackoff = 5 * time.Second
	ipqsMaxBackoff     = 5 * time.Minute
)

func NewIPQSClient(logger *zap.Logger, metrics Metrics, redisClient *redis.Client, apiKey string) (*IPQSClient, error) {
	ctx, cancelFn := context.WithCancel(context.Background())

	// Build the URL format
	q := url.Values{}
	for k, v := range map[string]string{
		"strictness":                 "0",
		"allow_public_access_points": "true",
		"lighter_penalties":          "false",
	} {
		q.Set(k, v)
	}

	ipqs := IPQSClient{
		ctx:      ctx,
		cancelFn: cancelFn,

		logger:  logger,
		metrics: metrics,

		redisClient: redisClient,
		parameters:  q.Encode(),
		apiKey:      apiKey,
	}

	return &ipqs, nil
}

func (s *IPQSClient) url(ip string) string {
	return "https://www.ipqualityscore.com/api/json/ip/" + s.apiKey + "/" + ip + "?" + s.parameters
}

func (s *IPQSClient) Name() string {
	return "IPQS"
}

func (s *IPQSClient) load(ip string) (*IPQSResponse, error) {
	if s == nil {
		return nil, nil
	}
	if s.redisClient == nil {
		return nil, nil
	}
	cachedData, err := s.redisClient.Get(IPQSRedisKeyPrefix + ip).Result()
	if err == redis.Nil {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to get data from redis: %v", err)
	}

	var result IPQSResponse
	err = json.Unmarshal([]byte(cachedData), &result)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal cached data: %v", err)
	}

	return &result, nil
}

func (s *IPQSClient) store(ip string, result *IPQSResponse) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %v", err)
	}

	err = s.redisClient.Set(IPQSRedisKeyPrefix+ip, data, IPQSRedisKeyTTL).Err()
	if err != nil {
		s.metrics.CustomCounter("ipqs_cache_store_error", nil, 1)
		return fmt.Errorf("failed to set data in redis: %v", err)
	}

	return nil
}

func (s *IPQSClient) Get(ctx context.Context, ip string) (IPInfo, error) {
	if s == nil {
		return nil, nil
	}
	// ignore reserved IPs
	if parsedIP := net.ParseIP(ip); parsedIP != nil && (parsedIP.IsLoopback() || parsedIP.IsLinkLocalUnicast() || parsedIP.IsLinkLocalMulticast() || parsedIP.IsMulticast() || parsedIP.IsPrivate()) {
		return &StubIPInfo{}, nil
	}
	startTime := time.Now()
	metricsTags := map[string]string{"result": "cache_hit"}

	defer func() {
		s.metrics.CustomTimer("ipqs_request_duration", metricsTags, time.Since(startTime))
	}()

	if result, err := s.load(ip); err != nil {
		metricsTags["result"] = "cache_error"
	} else if result != nil && result.Success {
		metricsTags["result"] = "cache_hit"
		return &IPQSData{Response: *result}, nil
	}

	if !s.allowRequest() {
		metricsTags["result"] = "circuit_open"
		s.metrics.CustomCounter("ipqs_circuit_breaker_open", nil, 1)
		return nil, nil
	}

	reqCtx, cancelFn := context.WithTimeout(ctx, time.Second*1)
	defer cancelFn()

	result, err := s.retrieve(reqCtx, ip)
	if err != nil {
		metricsTags["result"] = "request_error"
		s.onFailure(err)
		return nil, nil
	}
	if result == nil || !result.Success {
		metricsTags["result"] = "request_error"
		s.onFailure(fmt.Errorf("ipqs response unsuccessful"))
		return nil, nil
	}

	s.onSuccess()
	metricsTags["result"] = "cache_miss"

	if err = s.store(ip, result); err != nil {
		s.logger.Warn("Failed to store IPQS details in cache.", zap.Error(err))
	}

	return &IPQSData{Response: *result}, nil

}

func (s *IPQSClient) retrieve(ctx context.Context, ip string) (*IPQSResponse, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.url(ip), nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to get response: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status code: %d", resp.StatusCode)
	}

	var result IPQSResponse
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

func (s *IPQSClient) IsVPN(ip string) bool {

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

func (s *IPQSClient) allowRequest() bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.circuitOpen {
		return true
	}

	if time.Since(s.lastFailureTime) < s.backoffDuration {
		return false
	}

	return true
}

func (s *IPQSClient) onFailure(err error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.backoffDuration == 0 {
		s.backoffDuration = ipqsInitialBackoff
	} else {
		s.backoffDuration *= 2
		if s.backoffDuration > ipqsMaxBackoff {
			s.backoffDuration = ipqsMaxBackoff
		}
	}

	s.lastFailureTime = time.Now()
	wasOpen := s.circuitOpen
	s.circuitOpen = true

	if !wasOpen {
		s.logger.Warn("IPQS circuit breaker opened, failing open with backoff", zap.Duration("backoff", s.backoffDuration), zap.Error(err))
	}
}

func (s *IPQSClient) onSuccess() {
	s.mu.Lock()
	defer s.mu.Unlock()

	wasOpen := s.circuitOpen
	s.lastFailureTime = time.Time{}
	s.backoffDuration = 0
	s.circuitOpen = false

	if wasOpen {
		s.logger.Info("IPQS circuit breaker closed after successful request")
	}
}
