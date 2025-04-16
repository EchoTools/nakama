package server

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-redis/redis"
	"github.com/mmcloughlin/geohash"
	"go.uber.org/zap"
)

const (
	ipapiRedisKeyPrefix = "ipapi:"
	ipapiRedisKeyTTL    = time.Hour * 24 * 180
)

type IPAPIResponse struct {
	Query         string  `json:"query"`
	Status        string  `json:"status"`
	Continent     string  `json:"continent"`
	ContinentCode string  `json:"continentCode"`
	Country       string  `json:"country"`
	CountryCode   string  `json:"countryCode"`
	Region        string  `json:"region"`
	RegionName    string  `json:"regionName"`
	City          string  `json:"city"`
	District      string  `json:"district"`
	Zip           string  `json:"zip"`
	Latitude      float64 `json:"lat"`
	Longitude     float64 `json:"lon"`
	Timezone      string  `json:"timezone"`
	Offset        int64   `json:"offset"`
	Currency      string  `json:"currency"`
	ISP           string  `json:"isp"`
	Organization  string  `json:"org"`
	ASNumber      string  `json:"as"`
	Asname        string  `json:"asname"`
	Mobile        bool    `json:"mobile"`
	Proxy         bool    `json:"proxy"`
	Hosting       bool    `json:"hosting"`
}

var _ = IPInfo(&ipapiData{})

type ipapiData struct {
	Response IPAPIResponse `json:"response,omitempty"`
}

func (r *ipapiData) DataProvider() string {
	return "IP-API"
}

func (r *ipapiData) IsVPN() bool {
	return r.Response.Proxy
}

func (r *ipapiData) Latitude() float64 {
	return r.Response.Latitude
}
func (r *ipapiData) Longitude() float64 {
	return r.Response.Longitude
}

func (r *ipapiData) City() string {
	return r.Response.City
}

func (r *ipapiData) Region() string {
	return r.Response.Region
}

func (r *ipapiData) CountryCode() string {
	return r.Response.CountryCode
}

func (r *ipapiData) GeoHash(geoPrecision uint) string {
	return geohash.EncodeWithPrecision(r.Latitude(), r.Longitude(), 2)
}

func (r *ipapiData) ASN() int {
	s, _, _ := strings.Cut(r.Response.ASNumber, " ")
	s = strings.TrimPrefix(s, "AS") // Strip the AS prefix
	asn, _ := strconv.Atoi(s)
	return asn
}

func (r *ipapiData) FraudScore() int {
	if r.IsVPN() {
		return 100
	} else {
		return 0
	}
}

func (r *ipapiData) ISP() string {
	return r.Response.ISP
}

func (r *ipapiData) Organization() string {
	return r.Response.Organization
}

var _ = IPInfoProvider(&ipapiClient{})

type ipapiClient struct {
	ctx      context.Context
	cancelFn context.CancelFunc

	logger  *zap.Logger
	metrics Metrics
	db      *sql.DB

	redisClient *redis.Client
}

func NewIPAPIClient(logger *zap.Logger, metrics Metrics, redisClient *redis.Client) (*ipapiClient, error) {
	ctx, cancelFn := context.WithCancel(context.Background())

	client := ipapiClient{
		ctx:      ctx,
		cancelFn: cancelFn,

		logger:  logger,
		metrics: metrics,

		redisClient: redisClient,
	}

	return &client, nil
}

func (s *ipapiClient) Name() string {
	return "IP-API"
}

func (s *ipapiClient) URL(ip string) string {
	return fmt.Sprintf("http://ip-api.com/json/%s?fields=status,message,continent,continentCode,country,countryCode,region,regionName,city,district,zip,lat,lon,timezone,offset,currency,isp,org,as,asname,proxy,hosting,query", ip)
}

func (s *ipapiClient) load(ip string) (*IPAPIResponse, error) {
	if s == nil {
		return nil, nil
	}
	if s.redisClient == nil {
		return nil, nil
	}
	cachedData, err := s.redisClient.Get(ipapiRedisKeyPrefix + ip).Result()
	if err == redis.Nil {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("failed to get data from redis: %v", err)
	}

	var result IPAPIResponse
	err = json.Unmarshal([]byte(cachedData), &result)
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal cached data: %v", err)
	}

	return &result, nil
}

func (s *ipapiClient) store(ip string, result *IPAPIResponse) error {
	data, err := json.Marshal(result)
	if err != nil {
		return fmt.Errorf("failed to marshal data: %v", err)
	}

	err = s.redisClient.Set(ipapiRedisKeyPrefix+ip, data, ipapiRedisKeyTTL).Err()
	if err != nil {
		s.metrics.CustomCounter("ipapi_cache_store_error", nil, 1)
		return fmt.Errorf("failed to set data in redis: %v", err)
	}

	return nil
}

func (s *ipapiClient) retrieve(ip string) (*IPAPIResponse, error) {

	resp, err := http.Get(s.URL(ip))
	if err != nil {
		return nil, fmt.Errorf("failed to get response: %w", err)
	}
	defer resp.Body.Close()

	var result IPAPIResponse
	err = json.NewDecoder(resp.Body).Decode(&result)
	if err != nil {
		return nil, fmt.Errorf("failed to decode response: %w", err)
	}

	return &result, nil
}

func (s *ipapiClient) Get(ctx context.Context, ip string) (IPInfo, error) {
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
		s.metrics.CustomTimer("ipapi_request_duration", metricsTags, time.Since(startTime))
	}()

	ctx, cancelFn := context.WithTimeout(ctx, time.Second*1)
	defer cancelFn()

	resultCh := make(chan *IPAPIResponse)

	go func() {
		var err error
		var result *IPAPIResponse

		if result, err = s.load(ip); err != nil {
			metricsTags["result"] = "cache_error"

		} else if result != nil {
			metricsTags["result"] = "cache_hit"

		} else {

			if result, err = s.retrieve(ip); err != nil {

				metricsTags["result"] = "request_error"
				s.logger.Warn("Failed to get ipapi details, failing open.", zap.Error(err))

			} else {

				metricsTags["result"] = "cache_miss"

				// cache the result
				if err = s.store(ip, result); err != nil {
					s.logger.Warn("Failed to store ipapi details in cache.", zap.Error(err))
				}
			}
		}

		resultCh <- result
	}()

	select {
	case <-ctx.Done():
		if ctx.Err() == context.DeadlineExceeded {
			metricsTags["result"] = "request_timeout"
			s.logger.Warn("ipapi request timed out, failing open.")
		}
		return nil, fmt.Errorf("ipapi request timed out")
	case result := <-resultCh:

		if result == nil {
			metricsTags["result"] = "request_error"
			return nil, nil
		}
		metricsTags["result"] = "cache_miss"
		return &ipapiData{Response: *result}, nil
	}

}

func (s *ipapiClient) IsVPN(ip string) bool {

	result, err := s.Get(s.ctx, ip)
	if err != nil {
		s.logger.Warn("Failed to get ipapi details, failing open.", zap.Error(err))
		return false
	}
	if result == nil {
		return false
	}
	return result.IsVPN()
}
