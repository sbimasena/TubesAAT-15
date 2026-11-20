package httpapi

import "net/http"

// limitRequests bounds the entire hazard path, including Auth introspection.
// Full capacity is rejected immediately; callers never wait in an internal queue.
func limitRequests(permits chan struct{}, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Err() != nil {
			return
		}
		select {
		case permits <- struct{}{}:
			defer func() { <-permits }()
		default:
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "concurrency_limit", "hazard request capacity exceeded")
			return
		}
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
