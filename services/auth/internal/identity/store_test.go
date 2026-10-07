package identity

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestIdentitiesAndSaltedArgon2id(t *testing.T) {
	env := map[string]string{
		"MEDIA_CLIENT_ID": "media", "MEDIA_CLIENT_PASSWORD": "test-media-password",
		"FIELD_TEAM_CLIENT_ID": "field-team", "FIELD_TEAM_CLIENT_PASSWORD": "test-field-password",
		"INTERNAL_OPS_CLIENT_ID": "internal-ops", "INTERNAL_OPS_CLIENT_PASSWORD": "test-ops-password",
	}
	store, err := NewFromEnv(func(key string) string { return env[key] })
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ id, password, scope string }{
		{"media", "test-media-password", "media"},
		{"field-team", "test-field-password", "field-team"},
		{"internal-ops", "test-ops-password", "internal-ops"},
	} {
		value, err := store.Verify(context.Background(), test.id, test.password)
		if err != nil || value.ID != test.id || value.Scope != test.scope {
			t.Fatalf("identity %s failed: %v", test.id, err)
		}
		hash := store.entries[test.id].passwordHash
		if !strings.HasPrefix(hash, "$argon2id$v=19$m=65536,t=3,p=4$") || strings.Contains(hash, test.password) {
			t.Fatal("password was not stored as an Argon2id hash")
		}
	}
	for _, test := range []struct{ id, password string }{
		{"media", "wrong"}, {"media", "test-field-password"}, {"unknown", "test-media-password"},
	} {
		if _, err := store.Verify(context.Background(), test.id, test.password); !errors.Is(err, ErrCredentials) {
			t.Fatalf("invalid credentials for %s were accepted: %v", test.id, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Verify(ctx, "media", "test-media-password"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled verification returned %v", err)
	}
	first, _ := hashPassword("same-test-password")
	second, _ := hashPassword("same-test-password")
	if first.passwordHash == second.passwordHash {
		t.Fatal("same password reused a salt")
	}
}

func TestRejectsDuplicateIdentityIDs(t *testing.T) {
	_, err := NewFromEnv(func(key string) string {
		if strings.HasSuffix(key, "_CLIENT_ID") {
			return "duplicate"
		}
		return "test-password"
	})
	if err == nil {
		t.Fatal("duplicate IDs accepted")
	}
}
