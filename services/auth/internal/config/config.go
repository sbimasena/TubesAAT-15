package config

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Port            string
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
}

// Load validates startup configuration without retaining plaintext credentials.
func Load(getenv func(string) string) (Config, error) {
	port := strings.TrimSpace(getenv("AUTH_PORT"))
	if err := validatePort(port); err != nil {
		return Config{}, err
	}
	for _, name := range []string{
		"JWT_SIGNING_SECRET",
		"AUTH_INTERNAL_SECRET",
		"MEDIA_CLIENT_ID", "MEDIA_CLIENT_PASSWORD",
		"FIELD_TEAM_CLIENT_ID", "FIELD_TEAM_CLIENT_PASSWORD",
		"INTERNAL_OPS_CLIENT_ID", "INTERNAL_OPS_CLIENT_PASSWORD",
	} {
		if strings.TrimSpace(getenv(name)) == "" {
			return Config{}, fmt.Errorf("%s must be set", name)
		}
	}
	if len(getenv("JWT_SIGNING_SECRET")) < 32 {
		return Config{}, fmt.Errorf("JWT_SIGNING_SECRET must contain at least 32 bytes")
	}
	if len(getenv("AUTH_INTERNAL_SECRET")) < 32 || getenv("AUTH_INTERNAL_SECRET") == getenv("JWT_SIGNING_SECRET") {
		return Config{}, fmt.Errorf("AUTH_INTERNAL_SECRET must contain at least 32 bytes and differ from JWT_SIGNING_SECRET")
	}
	accessTTL, err := positiveSeconds(getenv("ACCESS_TOKEN_TTL_SECONDS"), 60, "ACCESS_TOKEN_TTL_SECONDS")
	if err != nil {
		return Config{}, err
	}
	refreshTTL, err := positiveSeconds(getenv("REFRESH_TOKEN_TTL_SECONDS"), 0, "REFRESH_TOKEN_TTL_SECONDS")
	if err != nil {
		return Config{}, err
	}
	return Config{Port: port, AccessTokenTTL: accessTTL, RefreshTokenTTL: refreshTTL}, nil
}

func validatePort(value string) error {
	port, err := strconv.Atoi(value)
	if err != nil || port < 1 || port > 65535 {
		return fmt.Errorf("AUTH_PORT must be an integer from 1 to 65535")
	}
	return nil
}

func positiveSeconds(value string, fallback int64, name string) (time.Duration, error) {
	value = strings.TrimSpace(value)
	if value == "" && fallback > 0 {
		return time.Duration(fallback) * time.Second, nil
	}
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds <= 0 || seconds > math.MaxInt64/int64(time.Second) {
		return 0, fmt.Errorf("%s must be a positive number of seconds", name)
	}
	return time.Duration(seconds) * time.Second, nil
}
