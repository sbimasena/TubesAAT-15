package token

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const (
	issuer   = "tubesaat-auth"
	audience = "tubesaat-client-api"
)

type Claims struct {
	Scope      string `json:"scope"`
	SessionID  string `json:"sid"`
	Generation uint64 `json:"generation"`
	jwt.RegisteredClaims
}

func (c Claims) Validate() error {
	if c.Subject == "" || c.ID == "" || c.SessionID == "" || c.Generation == 0 || c.IssuedAt == nil {
		return fmt.Errorf("required access claims missing")
	}
	if c.Scope != "media" && c.Scope != "field-team" && c.Scope != "internal-ops" {
		return fmt.Errorf("invalid scope")
	}
	return nil
}

type Issuer struct {
	key []byte
	ttl time.Duration
	now func() time.Time
}

func New(key []byte, ttl time.Duration) (*Issuer, error) {
	if len(key) < 32 || ttl < time.Second || ttl%time.Second != 0 {
		return nil, fmt.Errorf("signing secret requires at least 32 bytes and TTL requires positive whole seconds")
	}
	return &Issuer{key: append([]byte(nil), key...), ttl: ttl, now: time.Now}, nil
}

func RandomToken() (string, error) {
	value := make([]byte, 32)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(value), nil
}

func (i *Issuer) Issue(subject, scope, sessionID string, generation uint64) (string, error) {
	id, err := RandomToken()
	if err != nil {
		return "", err
	}
	now := i.now().UTC().Truncate(time.Second)
	claims := Claims{Scope: scope, SessionID: sessionID, Generation: generation, RegisteredClaims: jwt.RegisteredClaims{
		Issuer: issuer, Audience: jwt.ClaimStrings{audience}, Subject: subject, ID: id,
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(i.ttl)),
	}}
	if err := claims.Validate(); err != nil {
		return "", err
	}
	return jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(i.key)
}

func (i *Issuer) Parse(value string) (*Claims, error) {
	claims := &Claims{}
	parsed, err := jwt.ParseWithClaims(value, claims, func(_ *jwt.Token) (any, error) { return i.key, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), jwt.WithExpirationRequired(),
		jwt.WithIssuedAt(), jwt.WithIssuer(issuer), jwt.WithAudience(audience), jwt.WithTimeFunc(i.now))
	if err != nil || !parsed.Valid {
		return nil, fmt.Errorf("invalid access token")
	}
	return claims, nil
}
