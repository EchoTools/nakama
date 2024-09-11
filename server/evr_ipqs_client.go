package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/gofrs/uuid/v5"
	"github.com/heroiclabs/nakama-common/api"
	"go.uber.org/zap"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

var ipqsCache = &MapOf[string, *IPQSResponse]{}

const (
	IPQSStorageCollection = "IPQS"
	IPQSCacheStorageKey   = "cache"
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

	url        string
	apiKey     string
	parameters map[string]string

	cache *MapOf[string, *IPQSResponse]
}

func NewIPQS(logger *zap.Logger, db *sql.DB, metrics Metrics, storageIndex StorageIndex, apiKey string) (*IPQSClient, error) {
	ctx, cancelFn := context.WithCancel(context.Background())

	ipqs := IPQSClient{
		ctx:      ctx,
		cancelFn: cancelFn,

		logger:       logger,
		metrics:      metrics,
		db:           db,
		storageIndex: storageIndex,

		apiKey: apiKey,
		url:    "https://www.ipqualityscore.com/api/json/ip/" + apiKey,
		parameters: map[string]string{
			"strictness":                 "0",
			"allow_public_access_points": "true",
			"lighter_penalties":          "false",
		},

		cache: ipqsCache,
	}

	if err := ipqs.LoadCache(); err != nil {
		return nil, err
	}

	go func() {
		for {
			select {
			case <-ipqs.ctx.Done():
				return
			case <-time.After(time.Minute * 5):
			}

			if err := ipqs.SaveCache(); err != nil {
				logger.Error("Failed to save IPQS cache", zap.Error(err))
			}
		}
	}()

	return &ipqs, nil
}

func (s *IPQSClient) IPDetails(ip string, useCache bool) (*IPQSResponse, error) {

	if useCache {
		if cached, ok := s.cache.Load(ip); ok {
			return cached, nil
		}
	}

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

func (s *IPQSClient) Score(ip string) int {
	result := s.IPDetailsWithTimeout(ip)
	if result == nil {
		return 0
	}
	return result.FraudScore
}

func (s *IPQSClient) IPDetailsWithTimeout(ip string) *IPQSResponse {
	ctx, cancelFn := context.WithTimeout(s.ctx, time.Second*1)
	defer cancelFn()
	resultCh := make(chan *IPQSResponse)

	go func() {
		result, err := s.IPDetails(ip, true)
		if err != nil {
			s.logger.Warn("Failed to get IPQS details, failing open.", zap.Error(err))
		}
		resultCh <- result
	}()

	select {
	case <-ctx.Done():
		s.logger.Warn("IPQS request timed out, failing open.")
		return nil
	case result := <-resultCh:
		return result
	}
}

func (s *IPQSClient) IsVPN(ip string) bool {
	ctx, cancelFn := context.WithTimeout(s.ctx, time.Second*1)
	defer cancelFn()
	resultCh := make(chan *IPQSResponse)

	go func() {
		result, err := s.IPDetails(ip, true)
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

func (s *IPQSClient) SaveCache() error {

	cachemap := make(map[string]*IPQSResponse)
	s.cache.Range(func(key string, value *IPQSResponse) bool {
		cachemap[key] = value
		return true
	})

	data, err := json.Marshal(cachemap)
	if err != nil {
		return fmt.Errorf("failed to marshal cache: %w", err)
	}

	ops := StorageOpWrites{
		&StorageOpWrite{
			OwnerID: SystemUserID,
			Object: &api.WriteStorageObject{
				Collection:      IPQSStorageCollection,
				Key:             IPQSCacheStorageKey,
				Value:           string(data),
				PermissionRead:  &wrapperspb.Int32Value{Value: int32(0)},
				PermissionWrite: &wrapperspb.Int32Value{Value: int32(0)},
			},
		},
	}
	_, _, err = StorageWriteObjects(s.ctx, s.logger, s.db, s.metrics, s.storageIndex, true, ops)
	if err != nil {
		return fmt.Errorf("failed to write cache: %w", err)
	}

	return nil
}

func (s *IPQSClient) LoadCache() error {

	result, err := StorageReadObjects(s.ctx, s.logger, s.db, uuid.Nil, []*api.ReadStorageObjectId{
		{
			Collection: IPQSStorageCollection,
			Key:        IPQSCacheStorageKey,
			UserId:     SystemUserID,
		},
	})
	if err != nil {
		return fmt.Errorf("failed to read cache: %w", err)
	}

	if result == nil || len(result.Objects) == 0 {
		return nil
	}

	var cachemap map[string]*IPQSResponse
	err = json.Unmarshal([]byte(result.Objects[0].Value), &cachemap)
	if err != nil {
		return fmt.Errorf("failed to unmarshal cache: %w", err)
	}

	for key, value := range cachemap {
		s.cache.Store(key, value)
	}
	s.logger.Info("Loaded IPQS cache", zap.Int("count", len(cachemap)))
	return nil
}
