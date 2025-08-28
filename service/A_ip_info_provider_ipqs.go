package service

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/go-redis/redis"
	"github.com/heroiclabs/nakama/v3/server"
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
	return r.Response.VPN
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
	metrics server.Metrics

	redisClient *redis.Client

	apiKey     string
	parameters string
}

func NewIPQSClient(logger *zap.Logger, metrics server.Metrics, redisClient *redis.Client, apiKey string) (*IPQSClient, error) {
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
	if ip := net.ParseIP(ip); ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsPrivate() {
		return &StubIPInfo{}, nil
	}
	startTime := time.Now()
	metricsTags := map[string]string{"result": "cache_hit"}

	defer func() {
		s.metrics.CustomTimer("ipqs_request_duration", metricsTags, time.Since(startTime))
	}()

	ctx, cancelFn := context.WithTimeout(ctx, time.Second*1)
	defer cancelFn()

	resultCh := make(chan *IPQSResponse)

	go func() {
		var err error
		var result *IPQSResponse

		if result, err = s.load(ip); err != nil {
			metricsTags["result"] = "cache_error"

		} else if result != nil && result.Success {
			metricsTags["result"] = "cache_hit"

		} else {

			if result, err = s.retrieve(ip); err != nil || !result.Success {

				metricsTags["result"] = "request_error"
				s.logger.Warn("Failed to get IPQS details, failing open.", zap.Error(err))

			} else {

				metricsTags["result"] = "cache_miss"

				// cache the result
				if err = s.store(ip, result); err != nil {
					s.logger.Warn("Failed to store IPQS details in cache.", zap.Error(err))
				}
			}
		}

		resultCh <- result
	}()

	select {
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			metricsTags["result"] = "request_timeout"
			s.logger.Warn("IPQS request timed out, failing open.")
		}
		return nil, fmt.Errorf("IPQS request timed out")
	case result := <-resultCh:

		if result == nil {
			metricsTags["result"] = "request_error"
			return nil, nil
		}
		metricsTags["result"] = "cache_miss"
		return &IPQSData{Response: *result}, nil
	}

}

func (s *IPQSClient) retrieve(ip string) (*IPQSResponse, error) {

	resp, err := http.Get(s.url(ip))
	if err != nil {
		return nil, fmt.Errorf("failed to get response: %w", err)
	}
	defer resp.Body.Close()

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
