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
	} {
		if value := strings.TrimSpace(os.Getenv(name)); value != "" {
			number, err := strconv.ParseInt(value, 10, 64)
			if err != nil || number <= 0 || number > int64((1<<63-1)/int64(time.Millisecond)) {
				return config, fmt.Errorf("%s must be positive milliseconds within duration limits", name)
			}
			*target = time.Duration(number) * time.Millisecond
		}
	}
	if value := strings.TrimSpace(os.Getenv("PANTAI_PAGE_LIMIT")); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 1000 {
			return config, fmt.Errorf("PANTAI_PAGE_LIMIT must be between 1 and 1000")
		}
		config.PageLimit = limit
	}
	return config, nil
}
