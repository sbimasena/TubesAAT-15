package config

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

const responseWriteMargin = 5 * time.Second

type Config struct {
	Port                     string
	AggregatorBaseURL        string
	AggregatorRequestTimeout time.Duration
	AuthBaseURL              string
	AuthRequestTimeout       time.Duration
	// AuthIntrospectionCacheTTL of 0 disables caching (M1 semantics). Enabled
	// via AUTH_INTROSPECTION_CACHE_TTL_MS for load tests and game day; the
	// trade-off is documented in services/client-api/README.md.
	AuthIntrospectionCacheTTL time.Duration
	WriteTimeout              time.Duration
	EnableProvisionalHazards  bool
	MaxConcurrentRequests     int
	// Role pools are a DRAFT bulkhead (P5.5); defaults 40/20/4, not final
	// until the P2/P5 load tests review them.
	MediaPool       int
	FieldTeamPool   int
	InternalOpsPool int
	// BrokerURL and FieldReportPublishTimeout serve POST /field-reports
	// (D-FR): 202 is sent only after the broker durably confirms the report.
	BrokerURL                 string
	FieldReportPublishTimeout time.Duration
}

func Load(getenv func(string) string) (Config, error) {
	port := strings.TrimSpace(getenv("CLIENT_API_PORT"))
	if port == "" {
		port = "8080"
	}
	if parsed, err := strconv.Atoi(port); err != nil || parsed < 1 || parsed > 65535 {
		return Config{}, fmt.Errorf("CLIENT_API_PORT must be an integer from 1 to 65535")
	}
	baseURL := strings.TrimSpace(getenv("AGGREGATOR_BASE_URL"))
	parsedURL, err := url.Parse(baseURL)
	if err != nil || (parsedURL.Scheme != "http" && parsedURL.Scheme != "https") || parsedURL.Host == "" || parsedURL.User != nil || parsedURL.Path != "" || parsedURL.RawQuery != "" || parsedURL.Fragment != "" {
		return Config{}, fmt.Errorf("AGGREGATOR_BASE_URL must be an HTTP(S) origin")
	}
	milliseconds, err := strconv.ParseInt(strings.TrimSpace(getenv("AGGREGATOR_REQUEST_TIMEOUT_MS")), 10, 64)
	if err != nil || milliseconds <= 0 || milliseconds > (math.MaxInt64-int64(responseWriteMargin))/int64(time.Millisecond) {
		return Config{}, fmt.Errorf("AGGREGATOR_REQUEST_TIMEOUT_MS must be a positive number of milliseconds")
	}
	authURL := strings.TrimSpace(getenv("AUTH_BASE_URL"))
	parsedAuth, err := url.Parse(authURL)
	if err != nil || (parsedAuth.Scheme != "http" && parsedAuth.Scheme != "https") || parsedAuth.Host == "" ||
		parsedAuth.User != nil || parsedAuth.Path != "" || parsedAuth.RawQuery != "" || parsedAuth.Fragment != "" {
		return Config{}, fmt.Errorf("AUTH_BASE_URL must be an HTTP(S) origin")
	}
	if len(getenv("AUTH_INTERNAL_SECRET")) < 32 {
		return Config{}, fmt.Errorf("AUTH_INTERNAL_SECRET must contain at least 32 bytes")
	}
	authMS, err := strconv.ParseInt(strings.TrimSpace(getenv("AUTH_REQUEST_TIMEOUT_MS")), 10, 64)
	if err != nil || authMS <= 0 || authMS > (math.MaxInt64-int64(responseWriteMargin))/int64(time.Millisecond)-milliseconds {
		return Config{}, fmt.Errorf("AUTH_REQUEST_TIMEOUT_MS must be positive and fit the combined request deadline")
	}
	enabled := false
	maxConcurrent := 64
	if value := strings.TrimSpace(getenv("CLIENT_API_MAX_CONCURRENT_REQUESTS")); value != "" {
		maxConcurrent, err = strconv.Atoi(value)
		if err != nil || maxConcurrent < 1 {
			return Config{}, fmt.Errorf("CLIENT_API_MAX_CONCURRENT_REQUESTS must be a positive integer")
		}
	}
	if value := strings.TrimSpace(getenv("ENABLE_PROVISIONAL_HAZARD_ENDPOINT")); value != "" {
		enabled, err = strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("ENABLE_PROVISIONAL_HAZARD_ENDPOINT must be true or false")
		}
	}
	cacheTTL := time.Duration(0)
	if value := strings.TrimSpace(getenv("AUTH_INTROSPECTION_CACHE_TTL_MS")); value != "" {
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil || parsed < 0 || parsed > math.MaxInt64/int64(time.Millisecond) {
			return Config{}, fmt.Errorf("AUTH_INTROSPECTION_CACHE_TTL_MS must be a non-negative number of milliseconds")
		}
		cacheTTL = time.Duration(parsed) * time.Millisecond
	}
	// DRAFT bulkhead defaults (P5.5); not final until the P2/P5 load tests.
	mediaPool, fieldTeamPool, internalOpsPool := 40, 20, 4
	if value := strings.TrimSpace(getenv("CLIENT_API_MEDIA_POOL")); value != "" {
		mediaPool, err = strconv.Atoi(value)
		if err != nil || mediaPool < 1 {
			return Config{}, fmt.Errorf("CLIENT_API_MEDIA_POOL must be a positive integer")
		}
	}
	if value := strings.TrimSpace(getenv("CLIENT_API_FIELD_TEAM_POOL")); value != "" {
		fieldTeamPool, err = strconv.Atoi(value)
		if err != nil || fieldTeamPool < 1 {
			return Config{}, fmt.Errorf("CLIENT_API_FIELD_TEAM_POOL must be a positive integer")
		}
	}
	if value := strings.TrimSpace(getenv("CLIENT_API_INTERNAL_OPS_POOL")); value != "" {
		internalOpsPool, err = strconv.Atoi(value)
		if err != nil || internalOpsPool < 1 {
			return Config{}, fmt.Errorf("CLIENT_API_INTERNAL_OPS_POOL must be a positive integer")
		}
	}
	// Field-reports publishing (D-FR); timeout default 5s, positive values only.
	fieldReportMS := int64(5000)
	if value := strings.TrimSpace(getenv("FIELD_REPORT_PUBLISH_TIMEOUT_MS")); value != "" {
		fieldReportMS, err = strconv.ParseInt(value, 10, 64)
		if err != nil || fieldReportMS <= 0 || fieldReportMS > math.MaxInt64/int64(time.Millisecond) {
			return Config{}, fmt.Errorf("FIELD_REPORT_PUBLISH_TIMEOUT_MS must be a positive number of milliseconds")
		}
	}
	brokerURL := strings.TrimSpace(getenv("BROKER_URL"))
	if _, parseErr := amqp.ParseURI(brokerURL); parseErr != nil {
		return Config{}, fmt.Errorf("BROKER_URL must be a valid AMQP URL")
	}
	return Config{
		Port: port, AggregatorBaseURL: baseURL,
		AggregatorRequestTimeout: time.Duration(milliseconds) * time.Millisecond,
		AuthBaseURL:              authURL, AuthRequestTimeout: time.Duration(authMS) * time.Millisecond,
		AuthIntrospectionCacheTTL: cacheTTL,
		WriteTimeout:              max(15*time.Second, time.Duration(milliseconds+authMS)*time.Millisecond+responseWriteMargin),
		EnableProvisionalHazards:  enabled,
		MaxConcurrentRequests:     maxConcurrent,
		MediaPool:                 mediaPool,
		FieldTeamPool:             fieldTeamPool,
		InternalOpsPool:           internalOpsPool,
		BrokerURL:                 brokerURL,
		FieldReportPublishTimeout: time.Duration(fieldReportMS) * time.Millisecond,
	}, nil
}
