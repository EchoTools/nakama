package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/go-redis/redis"
	"go.uber.org/zap"
)

const (
	IPQSRedisDatabase = 16
)

type IPQSTransactionDetails struct {
	ValidBillingAddress       bool     `json:"valid_billing_address"`
	ValidShippingAddress      bool     `json:"valid_shipping_address"`
	ValidBillingEmail         bool     `json:"valid_billing_email"`
	ValidShippingEmail        bool     `json:"valid_shipping_email"`
	RiskyBillingPhone         bool     `json:"risky_billing_phone"`
	RiskyShippingPhone        bool     `json:"risky_shipping_phone"`
	BillingPhoneCarrier       string   `json:"billing_phone_carrier"`
	ShippingPhoneCarrier      string   `json:"shipping_phone_carrier"`
	BillingPhoneLineType      string   `json:"billing_phone_line_type"`
	ShippingPhoneLineType     string   `json:"shipping_phone_line_type"`
	BillingPhoneCountry       string   `json:"billing_phone_country"`
	BillingPhoneCountryCode   string   `json:"billing_phone_country_code"`
	ShippingPhoneCountry      string   `json:"shipping_phone_country"`
	ShippingPhoneCountryCode  string   `json:"shipping_phone_country_code"`
	FraudulentBehavior        bool     `json:"fraudulent_behavior"`
	BinCountry                string   `json:"bin_country"`
	BinType                   string   `json:"bin_type"`
	BinBankName               string   `json:"bin_bank_name"`
	RiskScore                 int      `json:"risk_score"`
	RiskFactors               []string `json:"risk_factors"`
	IsPrepaidCard             bool     `json:"is_prepaid_card"`
	RiskyUsername             bool     `json:"risky_username"`
	ValidBillingPhone         bool     `json:"valid_billing_phone"`
	ValidShippingPhone        bool     `json:"valid_shipping_phone"`
	LeakedBillingEmail        bool     `json:"leaked_billing_email"`
	LeakedShippingEmail       bool     `json:"leaked_shipping_email"`
	LeakedUserData            bool     `json:"leaked_user_data"`
	UserActivity              string   `json:"user_activity"`
	PhoneNameIdentityMatch    string   `json:"phone_name_identity_match"`
	PhoneEmailIdentityMatch   string   `json:"phone_email_identity_match"`
	PhoneAddressIdentityMatch string   `json:"phone_address_identity_match"`
	EmailNameIdentityMatch    string   `json:"email_name_identity_match"`
	NameAddressIdentityMatch  string   `json:"name_address_identity_match"`
	AddressEmailIdentityMatch string   `json:"address_email_identity_match"`
}

type IPQSResponse struct {
	Message            string                 `json:"message"`
	Success            bool                   `json:"success"`
	Proxy              bool                   `json:"proxy"`
	ISP                string                 `json:"ISP"`
	Organization       string                 `json:"organization"`
	ASN                int                    `json:"ASN"`
	Host               string                 `json:"host"`
	CountryCode        string                 `json:"country_code"`
	City               string                 `json:"city"`
	Region             string                 `json:"region"`
	IsCrawler          bool                   `json:"is_crawler"`
	ConnectionType     string                 `json:"connection_type"`
	Latitude           float64                `json:"latitude"`
	Longitude          float64                `json:"longitude"`
	ZipCode            string                 `json:"zip_code"`
	Timezone           string                 `json:"timezone"`
	VPN                bool                   `json:"vpn"`
	Tor                bool                   `json:"tor"`
	ActiveVPN          bool                   `json:"active_vpn"`
	ActiveTor          bool                   `json:"active_tor"`
	RecentAbuse        bool                   `json:"recent_abuse"`
	FrequentAbuser     bool                   `json:"frequent_abuser"`
	HighRiskAttacks    bool                   `json:"high_risk_attacks"`
	AbuseVelocity      string                 `json:"abuse_velocity"`
	BotStatus          bool                   `json:"bot_status"`
	SharedConnection   bool                   `json:"shared_connection"`
	DynamicConnection  bool                   `json:"dynamic_connection"`
	SecurityScanner    bool                   `json:"security_scanner"`
	TrustedNetwork     bool                   `json:"trusted_network"`
	Mobile             bool                   `json:"mobile"`
	FraudScore         int                    `json:"fraud_score"`
	OperatingSystem    string                 `json:"operating_system"`
	Browser            string                 `json:"browser"`
	DeviceModel        string                 `json:"device_model"`
	DeviceBrand        string                 `json:"device_brand"`
	TransactionDetails IPQSTransactionDetails `json:"transaction_details"`
	RequestID          string                 `json:"request_id"`
}

type IPQSClient struct {
	ctx      context.Context
	cancelFn context.CancelFunc

	logger       *zap.Logger
	metrics      Metrics
	db           *sql.DB
	storageIndex StorageIndex

	redisClient *redis.Client

	url        string
	apiKey     string
	parameters map[string]string
}

func NewIPQS(logger *zap.Logger, db *sql.DB, metrics Metrics, storageIndex StorageIndex, redisClient *redis.Client, apiKey string) (*IPQSClient, error) {
	ctx, cancelFn := context.WithCancel(context.Background())

	ipqs := IPQSClient{
		ctx:      ctx,
		cancelFn: cancelFn,

		logger:       logger,
		metrics:      metrics,
		db:           db,
		storageIndex: storageIndex,

		redisClient: redisClient,

		apiKey: apiKey,
		url:    "https://www.ipqualityscore.com/api/json/ip/" + apiKey,
		parameters: map[string]string{
			"strictness":                 "0",
			"allow_public_access_points": "true",
			"lighter_penalties":          "false",
		},
	}

	return &ipqs, nil
}

func (s *IPQSClient) load(ip string) (*IPQSResponse, error) {
	if s.redisClient == nil {
		return nil, nil
	}
	cachedData, err := s.redisClient.Get(ip).Result()
	if err == redis.Nil {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to get data from redis: %w", err)
	}

	var result IPQSResponse
	err = json.Unmarshal([]byte(cachedData), &result)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal cached data: %w", err)
	}

	return &result, nil
}

func (s *IPQSClient) store(ip string, result *IPQSResponse) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %w", err)
	}

	err = s.redisClient.Set(ip, data, time.Hour*24*30).Err()
	if err != nil {
		return fmt.Errorf("failed to set data in redis: %w", err)
	}

	return nil
}

func (s *IPQSClient) Get(ctx context.Context, ip string) (*IPQSResponse, error) {

	// ignore reserved IPs
	if ip := net.ParseIP(ip); ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsPrivate() {
		return nil, nil
	}

	var err error
	var result *IPQSResponse
	if result, err = s.load(ip); err != nil {
		return nil, err
	} else if result != nil {
		return result, nil
	}

	ctx, cancelFn := context.WithTimeout(ctx, time.Second*1)
	defer cancelFn()
	resultCh := make(chan *IPQSResponse)

	go func() {
		result, err := s.retrieve(ip)
		if err != nil {
			s.logger.Warn("Failed to get IPQS details, failing open.", zap.Error(err))
		}

		// cache the result
		if err := s.store(ip, result); err != nil {
			s.logger.Warn("Failed to store IPQS details in cache.", zap.Error(err))
		}

		resultCh <- result
	}()

	select {
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			s.logger.Warn("IPQS request timed out, failing open.")
		}
		return nil, ctx.Err()
	case result := <-resultCh:

		if result == nil {
			return nil, nil
		}

		return result, nil
	}

}

func (s *IPQSClient) retrieve(ip string) (*IPQSResponse, error) {

	u, err := url.Parse(s.url + "/" + ip)
	if err != nil {
		return nil, fmt.Errorf("failed to parse URL: %w", err)
	}

	q := u.Query()
	for k, v := range s.parameters {
		q.Set(k, v)
	}
	u.RawQuery = q.Encode()

	resp, err := http.Get(u.String())
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
	ctx, cancelFn := context.WithTimeout(s.ctx, time.Second*1)
	defer cancelFn()
	resultCh := make(chan *IPQSResponse)

	go func() {
		result, err := s.retrieve(ip)
		if err != nil {
			s.logger.Warn("Failed to get IPQS details, failing open.", zap.Error(err))
		}
		resultCh <- result
	}()

	select {
	case <-ctx.Done():
		s.logger.Warn("IPQS request timed out, failing open.")
		return false
	case result := <-resultCh:
		if result == nil {
			return false
		}

		if result.VPN {
			return true
		}
		return false
	}
}
