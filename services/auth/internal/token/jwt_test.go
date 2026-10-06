package token

import (
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestClaimsExpiryAndSigningKey(t *testing.T) {
	key := []byte(strings.Repeat("test", 8))
	service, err := New(key, 60*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 6, 0, 0, 0, 0, time.UTC)
	service.now = func() time.Time { return now }
	access, err := service.Issue("field-team", "field-team", "test-session", 1)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := service.Parse(access)
	if err != nil || claims.Subject != "field-team" || claims.Scope != "field-team" || claims.SessionID != "test-session" || claims.ID == "" {
		t.Fatalf("unexpected claims: %v", err)
	}
	if claims.ExpiresAt.Sub(claims.IssuedAt.Time) != 60*time.Second {
		t.Fatal("TTL does not match configuration")
	}
	other, _ := New([]byte(strings.Repeat("other-key", 4)), time.Minute)
	other.now = service.now
	if _, err := other.Parse(access); err == nil {
		t.Fatal("different signing key accepted token")
	}
	altered, _ := jwt.NewWithClaims(jwt.SigningMethodHS384, claims).SignedString(key)
	if _, err := service.Parse(altered); err == nil {
		t.Fatal("unapproved signing algorithm accepted")
	}
	now = now.Add(60 * time.Second)
	if _, err := service.Parse(access); err == nil {
		t.Fatal("token valid at expiry boundary")
	}
}

func TestRejectsWeakKeyAndIncompleteClaims(t *testing.T) {
	if _, err := New([]byte("short-test-key"), time.Minute); err == nil {
		t.Fatal("weak signing key accepted")
	}
	service, _ := New([]byte(strings.Repeat("test", 8)), time.Minute)
	if _, err := service.Issue("media", "unknown", "sid", 1); err == nil {
		t.Fatal("unknown scope accepted")
	}
	claims := Claims{Scope: "media", RegisteredClaims: jwt.RegisteredClaims{
		Issuer: issuer, Subject: "media", Audience: jwt.ClaimStrings{audience},
	}}
	access, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(service.key)
	if _, err := service.Parse(access); err == nil {
		t.Fatal("missing expiry and session claims accepted")
	}
}
