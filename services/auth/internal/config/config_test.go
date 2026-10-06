package config

import (
	"strings"
	"testing"
	"time"
)

func validEnv() map[string]string {
	return map[string]string{
		"AUTH_PORT": "8084", "JWT_SIGNING_SECRET": strings.Repeat("test", 8),
		"REFRESH_TOKEN_TTL_SECONDS": "3600",
		"AUTH_INTERNAL_SECRET":      strings.Repeat("internal-test", 3),
		"MEDIA_CLIENT_ID":           "media", "MEDIA_CLIENT_PASSWORD": "media-test-password",
		"FIELD_TEAM_CLIENT_ID": "field-team", "FIELD_TEAM_CLIENT_PASSWORD": "field-test-password",
		"INTERNAL_OPS_CLIENT_ID": "internal-ops", "INTERNAL_OPS_CLIENT_PASSWORD": "ops-test-password",
	}
}

func loadMap(values map[string]string) (Config, error) {
	return Load(func(key string) string { return values[key] })
}

func TestLoadUsesConfigurablePortAndDefaultAccessTTL(t *testing.T) {
	cfg, err := loadMap(validEnv())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Port != "8084" || cfg.AccessTokenTTL != 60*time.Second || cfg.RefreshTokenTTL != time.Hour {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadRejectsMissingSigningSecret(t *testing.T) {
	values := validEnv()
	delete(values, "JWT_SIGNING_SECRET")
	_, err := loadMap(values)
	if err == nil || !strings.Contains(err.Error(), "JWT_SIGNING_SECRET") {
		t.Fatalf("expected missing-secret error, got %v", err)
	}
}

func TestLoadRejectsInvalidRefreshTTL(t *testing.T) {
	values := validEnv()
	values["REFRESH_TOKEN_TTL_SECONDS"] = "0"
	if _, err := loadMap(values); err == nil {
		t.Fatal("expected invalid refresh TTL to be rejected")
	}
}

func TestLoadRejectsWeakOrReusedInternalSecret(t *testing.T) {
	for _, value := range []string{"", "short", strings.Repeat("test", 8)} {
		env := validEnv()
		env["AUTH_INTERNAL_SECRET"] = value
		if _, err := loadMap(env); err == nil {
			t.Fatal("missing/weak/reused internal credential accepted")
		}
	}
}
