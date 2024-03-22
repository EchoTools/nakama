package evr

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/gofrs/uuid/v5"
)

// A configuration for the EVR client (config.json)
type ServiceConfig interface {
	// Getters for URIs
	GetAPI() string
	GetConfig() string
	GetLogin() string
	GetMatching() string
	GetServerDB() string
	GetTransaction() string
	GetPublisherLock() string

	Validate() []error
	String() string
}

var _ = ServiceConfig(&BaseServiceConfig{})

// The default implementation of the EVR client configuration (config.json)
type BaseServiceConfig struct {
	API           url.URL `json:"apiservice_host,omitempty"`
	Config        url.URL `json:"configservice_host,omitempty"`
	Login         url.URL `json:"loginservice_host,omitempty"`
	Matching      url.URL `json:"matchingservice_host,omitempty"`
	ServerDB      url.URL `json:"serverDB_host,omitempty"`
	Transaction   url.URL `json:"transactionservice_host,omitempty"`
	PublisherLock string  `json:"publisher_lock,omitempty"`
}

func (s *BaseServiceConfig) GetAPI() string {
	return s.API.String()
}

func (s *BaseServiceConfig) GetConfig() string {
	return s.Config.String()
}

func (s *BaseServiceConfig) GetLogin() string {
	return s.Login.String()
}

func (s *BaseServiceConfig) GetMatching() string {
	return s.Matching.String()
}

func (s *BaseServiceConfig) GetServerDB() string {
	return s.ServerDB.String()
}

func (s *BaseServiceConfig) GetTransaction() string {
	return s.Transaction.String()
}

func (s *BaseServiceConfig) GetPublisherLock() string {
	return s.PublisherLock
}

func (s *BaseServiceConfig) String() string {
	b, err := json.Marshal(s)
	if err != nil {
		return ""
	}
	return string(b)
}

// Validate the Service URLs configuration
func (e *BaseServiceConfig) Validate() []error {
	// Check if the publisher lock is 16 characters or less, and only contains alphanumeric characters, underscore or hyphen

	// Check if the URLs are valid
	urls := []url.URL{e.API, e.Config, e.Login, e.Matching,
		e.ServerDB, e.Transaction}

	errs := make([]error, 0)

	nilURL := url.URL{}
	// Validate each URL
	for i, u := range urls {

		// Reference the json tag for the URL in error messages
		fieldType := reflect.TypeOf(*e)
		fieldName := fieldType.Field(i).Tag.Get("json")
		if fieldName == "" {
			panic("Field name not found")
		}

		// A function that returns an error with the field name
		fieldErr := func(s string) error { return fmt.Errorf("`%s`: %s", fieldName, s) }

		// Only allow the serverDB URL to be omitted
		if i != 4 && u == nilURL {
			errs = append(errs, fieldErr("URL cannot be omitted"))
			continue
		}

		// Check if the URL is valid
		_, err := url.ParseRequestURI(u.String())
		if err != nil {
			errs = append(errs, fieldErr(err.Error()))
			continue
		}

		// Check that API has http(s):// and the rest have ws(s)://
		if i == 0 && !strings.HasPrefix(u.String(), "http://") && !strings.HasPrefix(u.String(), "https://") {
			errs = append(errs, fieldErr("URL must start with http:// or https://"))
		} else if i != 0 && !strings.HasPrefix(u.String(), "ws://") && !strings.HasPrefix(u.String(), "wss://") {
			errs = append(errs, fieldErr("URL must start with ws:// or wss://"))
		}

		// Check that the URL does not have authentication
		if u.User != nil {
			errs = append(errs, fieldErr("URL must not have authentication"))
		}

		// Check that the URL has a host
		if u.Hostname() == "" {
			errs = append(errs, fieldErr("URL must have a host"))
		}

		// Check that if the host is an IP Address, it is a valid IPv4 address
		ip := net.ParseIP(u.Hostname())
		if ip == nil || ip.To4() == nil {
			errs = append(errs, fieldErr("Host must be a valid IPv4 address"))
		}

		// Check that the URL has a valid port
		if u.Port() == "" {
			errs = append(errs, fieldErr("URL must have a port"))
		}

		// Check that the port is a valid port
		port, err := strconv.ParseUint(u.Port(), 10, 16)
		if err != nil || port < 1 || port > 65535 {
			errs = append(errs, fieldErr("Port must be a valid port"))
		}
	}

	// Check if the publisher lock is 16 characters or less, and only contains alphanumeric characters, underscore or hyphen
	if len(e.PublisherLock) > 16 || !strings.ContainsAny(e.PublisherLock, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789_-") {
		errs = append(errs, errors.New("publisher lock must be 16 characters or less, and only contain alphanumeric characters, underscore or hyphen"))
	}

	return errs
}

var _ = ServiceConfig(&NakamaServiceConfig{})

// A struct that represents the configuration for the EVR client (config.json)
type NakamaServiceConfig struct {
	BaseServiceConfig

	UserID    uuid.UUID
	Username  string
	DiscordID string
	Password  string
	Guilds    []string
	Channels  []uuid.UUID
	HttpKey   string
	ServerKey string
}

// Validate the config for use with Nakama
func (n *NakamaServiceConfig) Validate() (errs []error) {
	// Call the validate on ServiceConfig first
	errs = append(errs, n.BaseServiceConfig.Validate()...)
	if len(errs) > 0 {
		return
	}
	// Continue with the rest of the validation for NakamaServiceConfig

	// If the any line has a DiscordID and password, then the login line must have the same credentials.
	// If the serverDB line is present, must also have a DiscordID and password
	urlNil := url.URL{}

	// If the serverDB URL is not nil, then this is (also) a broadcaster configuration
	if n.ServerDB != urlNil {

		// Get the DiscordID and password from the login URL
		loginParams, err := url.ParseQuery(n.Login.RawQuery)
		if err != nil {
			errs = append(errs, errors.New("login URL must contain DiscordID and password urlparams"))
		}
		loginDiscordID := loginParams.Get("DiscordID")
		loginPassword := loginParams.Get("password")

		// Get the DiscordID and password from the serverDB URL
		serverDBParams, err := url.ParseQuery(n.ServerDB.RawQuery)
		if err != nil {
			errs = append(errs, errors.New("serverdb URL must contain DiscordID and password urlparams"))
		}
		serverDBDiscordID := serverDBParams.Get("DiscordID")
		serverDBPassword := serverDBParams.Get("password")

		// Check if the DiscordID and password match
		if loginDiscordID != serverDBDiscordID {
			errs = append(errs, errors.New("login and Serverdb URLs must have the same DiscordID urlparam"))
		}
		if loginPassword != serverDBPassword {
			errs = append(errs, errors.New("login and Serverdb URLs must have the same password urlparam"))
		}

		// Check that the DiscordID is a valid DiscordID that is 18-19 digits
		_, err = strconv.ParseUint(loginDiscordID, 10, 64)
		if err != nil {
			errs = append(errs, errors.New("login URL must have a valid DiscordID urlparam"))
		}

		// Check that the password is a valid password that is 8-20 characters
		if len(loginPassword) < 8 || len(loginPassword) > 20 {
			errs = append(errs, errors.New("login URL must have a valid password urlparam"))
		}

		// If the serverDB url has "guild" urlparam, it must be a valid Discord guild ID
		if guild := serverDBParams.Get("guild"); guild != "" {
			_, err := strconv.ParseUint(guild, 10, 64)
			if err != nil {
				errs = append(errs, errors.New("serverDB URL must have a valid guild urlparam"))
			}
		}
		// If the serverDB url has "channel" urlparam, it must be a valid UUID
		if channel := serverDBParams.Get("channel"); channel != "" {
			_, err := uuid.FromString(channel)
			if err != nil {
				errs = append(errs, errors.New("serverDB URL must have a valid channel urlparam"))
			}
		}
	}

	return
}

func NewNakamaServiceConfig(host string, port int, wss bool, https bool, UserID uuid.UUID, Username string, DiscordID string, Password string, Guilds []string, Channels []uuid.UUID, HttpKey string, ServerKey string) *NakamaServiceConfig {
	scheme := "http"
	if https {
		scheme = "https"
	}
	httpURL := url.URL{
		Scheme:   scheme,
		Host:     fmt.Sprintf("%s:%d", host, port),
		Path:     "/v2/rpc/evr/api",
		RawQuery: "unwrap=true&http_key=" + HttpKey,
	}

	scheme = "ws"
	if wss {
		scheme = "wss"
	}

	wsURL := url.URL{
		Scheme:   scheme,
		Host:     fmt.Sprintf("%s:%d", host, port),
		Path:     "/ws",
		RawQuery: "format=evr&token=" + ServerKey,
	}

	if UserID != uuid.Nil {
		wsURL.RawQuery += "&user_id=" + UserID.String()
	}

	if Username != "" {
		wsURL.RawQuery += "&username=" + Username
	}

	if DiscordID != "" {
		wsURL.RawQuery += "&discordid=" + DiscordID
	}
	if Password != "" {
		wsURL.RawQuery += "&password=" + Password
	}

	if len(Guilds) > 0 {
		wsURL.RawQuery += "&guild=" + strings.Join(Guilds, ",")
	}

	if len(Channels) > 0 {
		ids := make([]string, len(Channels))
		for i, id := range Channels {
			ids[i] = id.String()
		}
		wsURL.RawQuery += "&channel=" + strings.Join(ids, ",")
	}

	return &NakamaServiceConfig{
		BaseServiceConfig: BaseServiceConfig{
			API:         httpURL,
			Config:      wsURL,
			Login:       wsURL,
			Matching:    wsURL,
			ServerDB:    wsURL,
			Transaction: wsURL,
		},
		UserID:    UserID,
		Username:  Username,
		DiscordID: DiscordID,
		Password:  Password,
		Guilds:    Guilds,
		Channels:  Channels,
		HttpKey:   HttpKey,
		ServerKey: ServerKey,
	}
}

// The Echo Relay compatible configuration for the EVR client (config.json)
type EchoRelayServiceURLsConfig struct {
	BaseServiceConfig

	AuthToken   string
	DisplayName string
	ApiKey      string
}

func (e *EchoRelayServiceURLsConfig) Validate() (errs []error) {

	// Call the validate on ServiceConfig first
	errs = append(errs, e.BaseServiceConfig.Validate()...)
	if len(errs) > 0 {
		return
	}

	// Continue with the rest of the validation for EchoRelayServiceURLsConfig
	if e.DisplayName == "" {
		errs = append(errs, errors.New("DisplayName cannot be empty"))
	}
	if e.AuthToken == "" {
		errs = append(errs, errors.New("AuthToken cannot be empty"))
	}

	// If there is a serverDB line, there must be an ApiKey
	if e.ServerDB != (url.URL{}) && e.ApiKey == "" {
		errs = append(errs, errors.New("ApiKey cannot be empty"))
	}
	return
}
