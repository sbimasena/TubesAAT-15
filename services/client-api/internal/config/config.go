package config

import (
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const responseWriteMargin = 5 * time.Second

type Config struct {
	Port                     string
	AggregatorBaseURL        string
	AggregatorRequestTimeout time.Duration
	WriteTimeout             time.Duration
	EnableProvisionalHazards bool
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
	enabled := false
	if value := strings.TrimSpace(getenv("ENABLE_PROVISIONAL_HAZARD_ENDPOINT")); value != "" {
		enabled, err = strconv.ParseBool(value)
		if err != nil {
			return Config{}, fmt.Errorf("ENABLE_PROVISIONAL_HAZARD_ENDPOINT must be true or false")
		}
	}
	return Config{
		Port: port, AggregatorBaseURL: baseURL,
		AggregatorRequestTimeout: time.Duration(milliseconds) * time.Millisecond,
		WriteTimeout:             max(15*time.Second, time.Duration(milliseconds)*time.Millisecond+responseWriteMargin),
		EnableProvisionalHazards: enabled,
	}, nil
}
