package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/coastal"
)

func coastalConfigFromEnv() (coastal.Config, error) {
	config := coastal.DefaultConfig()
	config.BaseURL = strings.TrimSpace(os.Getenv("PANTAI_BASE_URL"))
	config.Token = os.Getenv("PANTAI_AUTH_TOKEN")
	config.InstanceID = os.Getenv("POD_NAME")
	for name, target := range map[string]*time.Duration{
		"PANTAI_REQUEST_TIMEOUT_MS": &config.Timeout, "PANTAI_POLL_INTERVAL_MS": &config.PollInterval,
		"PANTAI_PROGRESS_TIMEOUT_MS": &config.StaleAfter,
		"PANTAI_BREAKER_OPEN_MS":     &config.Resilience.OpenFor,
		"PANTAI_RETRY_WINDOW_MS":     &config.Resilience.RetryWindow,
	} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil || number <= 0 || number > int64((1<<63-1)/int64(time.Millisecond)) {
				return config, fmt.Errorf("%s must be positive milliseconds within duration limits", name)
			}
			*target = time.Duration(number) * time.Millisecond
		}
	}
	for _, field := range []struct {
		name   string
		target *int
		min    int
		max    int
	}{
		{"PANTAI_BREAKER_FAILURE_THRESHOLD", &config.Resilience.FailureThreshold, 1, int(^uint(0) >> 1)},
		{"PANTAI_MAX_ATTEMPTS", &config.Resilience.MaxAttempts, 1, 10},
		{"PANTAI_RETRIES_PER_WINDOW", &config.Resilience.RetriesPerWindow, 0, int(^uint(0) >> 1)},
	} {
		if value := strings.TrimSpace(os.Getenv(field.name)); value != "" {
			number, err := strconv.Atoi(value)
			if err != nil || number < field.min || number > field.max {
				return config, fmt.Errorf("%s must be between %d and %d", field.name, field.min, field.max)
			}
			*field.target = number
		}
	}
	if value := strings.TrimSpace(os.Getenv("PANTAI_PAGE_LIMIT")); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 1000 {
			return config, fmt.Errorf("PANTAI_PAGE_LIMIT must be between 1 and 1000")
		}
		config.PageLimit = limit
	}
	return config, config.Resilience.Validate()
}
