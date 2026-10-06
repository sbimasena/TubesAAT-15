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
