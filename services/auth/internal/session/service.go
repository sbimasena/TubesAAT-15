package session

import (
	"context"
	"crypto/sha256"
	"errors"
	"sync"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/auth/internal/identity"
	"github.com/sbimasena/TubesAAT-15/services/auth/internal/token"
)

var ErrToken = errors.New("invalid token")

type credentialVerifier interface {
	Verify(context.Context, string, string) (identity.Identity, error)
}

type tokenIssuer interface {
	Issue(string, string, string, uint64) (string, error)
	Parse(string) (*token.Claims, error)
}

type Pair struct {
	AccessToken  string `json:"access_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int64  `json:"expires_in"`
	RefreshToken string `json:"refresh_token"`
	// Verified identity metadata is for request logging, not the token response.
	ClientID string `json:"-"`
	Scope    string `json:"-"`
}

type state struct {
	client      identity.Identity
	generation  uint64
	refreshHash [32]byte
	expires     time.Time
	revoked     bool
}

// Service owns PoC session state; an Auth restart requires clients to log in again.
type Service struct {
	mu                    sync.Mutex
	sessions              map[string]state
	refresh               map[[32]byte]string
	identities            credentialVerifier
	issuer                tokenIssuer
	accessTTL, refreshTTL time.Duration
	now                   func() time.Time
	random                func() (string, error)
}

func New(identities credentialVerifier, issuer tokenIssuer, accessTTL, refreshTTL time.Duration) *Service {
	return &Service{sessions: make(map[string]state), refresh: make(map[[32]byte]string),
		identities: identities, issuer: issuer, accessTTL: accessTTL, refreshTTL: refreshTTL,
		now: time.Now, random: token.RandomToken}
}

func (s *Service) Login(ctx context.Context, clientID, password string) (Pair, error) {
	client, err := s.identities.Verify(ctx, clientID, password)
	if err != nil {
		return Pair{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Pair{}, err
	}
	s.cleanExpired()
	sid, err := s.random()
	if err != nil {
		return Pair{}, err
	}
	value := state{client: client, generation: 1, expires: s.now().Add(s.refreshTTL)}
	pair, hash, err := s.prepare(sid, value)
	if err != nil {
		return Pair{}, err
	}
	value.refreshHash = hash
	s.sessions[sid], s.refresh[hash] = value, sid
	return pair, nil
}

func (s *Service) Refresh(ctx context.Context, refreshToken string) (Pair, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return Pair{}, err
	}
	s.cleanExpired()
	hash := sha256.Sum256([]byte(refreshToken))
	sid, exists := s.refresh[hash]
	value := s.sessions[sid]
	if !exists || value.revoked || !s.now().Before(value.expires) || value.generation == ^uint64(0) {
		return Pair{}, ErrToken
	}
	value.generation++
	pair, nextHash, err := s.prepare(sid, value)
	if err != nil {
		return Pair{}, err
	}
	if err := ctx.Err(); err != nil {
		return Pair{}, err
	}
	// Commit only after signing succeeds; concurrent refreshes cannot reuse the old hash.
	delete(s.refresh, hash)
	value.refreshHash = nextHash
	s.sessions[sid], s.refresh[nextHash] = value, sid
	return pair, nil
}

func (s *Service) Inspect(ctx context.Context, accessToken string) (*token.Claims, error) {
	claims, err := s.issuer.Parse(accessToken)
	if err != nil {
		return nil, ErrToken
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	value, exists := s.sessions[claims.SessionID]
	if !exists || value.revoked || !s.now().Before(value.expires) ||
		claims.Generation != value.generation || claims.Subject != value.client.ID || claims.Scope != value.client.Scope {
		return nil, ErrToken
	}
	return claims, nil
}

// Revoke invalidates both token types; no public administration endpoint is exposed in M1.
func (s *Service) Revoke(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if value, exists := s.sessions[sessionID]; exists {
		value.revoked = true
		delete(s.refresh, value.refreshHash)
		s.sessions[sessionID] = value
	}
}

func (s *Service) prepare(sid string, value state) (Pair, [32]byte, error) {
	refresh, err := s.random()
	if err != nil {
		return Pair{}, [32]byte{}, err
	}
	access, err := s.issuer.Issue(value.client.ID, value.client.Scope, sid, value.generation)
	if err != nil {
		return Pair{}, [32]byte{}, err
	}
	return Pair{AccessToken: access, TokenType: "Bearer", ExpiresIn: int64(s.accessTTL / time.Second),
		RefreshToken: refresh, ClientID: value.client.ID, Scope: value.client.Scope}, sha256.Sum256([]byte(refresh)), nil
}

func (s *Service) cleanExpired() {
	for sid, value := range s.sessions {
		if !s.now().Before(value.expires) {
			delete(s.refresh, value.refreshHash)
			delete(s.sessions, sid)
		}
	}
}
