package identity

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

const (
	memoryKiB = 64 * 1024
	passes    = 3
	threads   = 4
	keyBytes  = 32
)

var ErrCredentials = errors.New("invalid credentials")

type Identity struct {
	ID    string
	Scope string
}

type credential struct {
	Identity
	passwordHash string
	salt         []byte
	hash         []byte
}

type Store struct {
	entries map[string]credential
	dummy   credential
	verify  chan struct{}
}

// NewFromEnv hashes each configured password once and retains only salted hashes.
func NewFromEnv(getenv func(string) string) (*Store, error) {
	type seed struct{ prefix, scope, id string }
	seeds := []seed{{"MEDIA", "media", ""}, {"FIELD_TEAM", "field-team", ""}, {"INTERNAL_OPS", "internal-ops", ""}}
	seen := make(map[string]bool)
	for i := range seeds {
		seeds[i].id = strings.TrimSpace(getenv(seeds[i].prefix + "_CLIENT_ID"))
		if seeds[i].id == "" || seen[seeds[i].id] {
			return nil, fmt.Errorf("client IDs must be nonempty and distinct")
		}
		seen[seeds[i].id] = true
		if strings.TrimSpace(getenv(seeds[i].prefix+"_CLIENT_PASSWORD")) == "" {
			return nil, fmt.Errorf("%s_CLIENT_PASSWORD must be set", seeds[i].prefix)
		}
	}
	store := &Store{entries: make(map[string]credential), verify: make(chan struct{}, 1)}
	for _, seed := range seeds {
		value, err := hashPassword(getenv(seed.prefix + "_CLIENT_PASSWORD"))
		if err != nil {
			return nil, err
		}
		value.Identity = Identity{ID: seed.id, Scope: seed.scope}
		store.entries[seed.id] = value
	}
	dummyPassword := make([]byte, 32)
	if _, err := rand.Read(dummyPassword); err != nil {
		return nil, err
	}
	dummy, err := hashPassword(string(dummyPassword))
	if err != nil {
		return nil, err
	}
	store.dummy = dummy
	return store, nil
}

func hashPassword(password string) (credential, error) {
	salt := make([]byte, 16)
	if _, err := rand.Read(salt); err != nil {
		return credential{}, err
	}
	hash := argon2.IDKey([]byte(password), salt, passes, memoryKiB, threads, keyBytes)
	encoded := fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s", argon2.Version, memoryKiB, passes, threads,
		base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(hash))
	return credential{passwordHash: encoded, salt: salt, hash: hash}, nil
}

// Verify serializes password hashing to bound memory; unknown IDs do the same work.
func (s *Store) Verify(ctx context.Context, id, password string) (Identity, error) {
	select {
	case s.verify <- struct{}{}:
		defer func() { <-s.verify }()
	case <-ctx.Done():
		return Identity{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	value, exists := s.entries[id]
	if !exists {
		value = s.dummy
	}
	actual := argon2.IDKey([]byte(password), value.salt, passes, memoryKiB, threads, keyBytes)
	valid := subtle.ConstantTimeCompare(actual, value.hash) == 1
	if err := ctx.Err(); err != nil {
		return Identity{}, err
	}
	if !exists || !valid {
		return Identity{}, ErrCredentials
	}
	return value.Identity, nil
}
