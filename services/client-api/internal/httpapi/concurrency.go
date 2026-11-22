package httpapi

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"strings"
)

// admission bounds requests before Auth introspection. The shared lane is the M1
// semaphore; Field Team and Internal Ops also get reserved lanes, because a Media flood
// held every shared slot during introspection and the Field Team never reached its
// role pool (P5.5, measured 22 Nov: 39% Field Team success under 400 Media VUs).
type admission struct {
	shared, fieldTeam, internalOps chan struct{}
}

func newAdmission(shared int, pools PoolConfig) *admission {
	return &admission{shared: make(chan struct{}, shared), fieldTeam: make(chan struct{}, pools.FieldTeam),
		internalOps: make(chan struct{}, pools.InternalOps)}
}

// claimedScope reads the scope claim of a bearer JWT without verifying it. It only picks
// an admission lane; introspection still decides access, so a forged claim gains at most
// a brief reserved slot before its 401.
func claimedScope(r *http.Request) string {
	token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	parts := strings.Split(token, ".")
	if !ok || len(parts) != 3 || len(parts[1]) > 4096 {
		return ""
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return ""
	}
	var claims struct {
		Scope string `json:"scope"`
	}
	if json.Unmarshal(payload, &claims) != nil {
		return ""
	}
	return claims.Scope
}

// limitRequests bounds the entire hazard path, including Auth introspection.
// Full capacity is rejected immediately; callers never wait in an internal queue.
func limitRequests(admit *admission, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Err() != nil {
			return
		}
		var reserved chan struct{}
		switch claimedScope(r) {
		case "field-team":
			reserved = admit.fieldTeam
		case "internal-ops":
			reserved = admit.internalOps
		}
		lane := admit.shared
		select {
		case reserved <- struct{}{}: // a nil channel (no reservation) is never ready
			lane = reserved
		default:
			select {
			case lane <- struct{}{}:
			default:
				w.Header().Set("Retry-After", "1")
				writeError(w, http.StatusTooManyRequests, "concurrency_limit", "hazard request capacity exceeded")
				return
			}
		}
		defer func() { <-lane }()
		if r.Context().Err() == nil {
			next(w, r)
		}
	}
}

type PoolConfig struct {
	Media       int
	FieldTeam   int
	InternalOps int
}

func DefaultPools() PoolConfig {
	return PoolConfig{Media: 40, FieldTeam: 20, InternalOps: 4}
}

// rolePools is a bulkhead: each role gets an isolated fetch capacity, so a
// Media flood cannot consume the Field Team reservation. Admission happens
// after Auth introspection (the role is only known then); the introspection
// phase itself stays bounded by the shared limitRequests semaphore.
type rolePools struct {
	media       chan struct{}
	fieldTeam   chan struct{}
	internalOps chan struct{}
}

func newRolePools(config PoolConfig) *rolePools {
	if config.Media < 1 || config.FieldTeam < 1 || config.InternalOps < 1 {
		panic("role pool capacities must be positive")
	}
	return &rolePools{
		media:       make(chan struct{}, config.Media),
		fieldTeam:   make(chan struct{}, config.FieldTeam),
		internalOps: make(chan struct{}, config.InternalOps),
	}
}

// acquire reserves a fetch slot for the role, or reports exhaustion with a
// controlled 429. The caller must release the returned release function.
func (p *rolePools) acquire(scope string) (release func(), ok bool) {
	var pool chan struct{}
	switch scope {
	case "media":
		pool = p.media
	case "field-team":
		pool = p.fieldTeam
	case "internal-ops":
		pool = p.internalOps
	default:
		// Unknown scopes never reach here (introspection validates them), but
		// fail open rather than deadlock an unexpected role.
		return func() {}, true
	}
	select {
	case pool <- struct{}{}:
		return func() { <-pool }, true
	default:
		return nil, false
	}
}
