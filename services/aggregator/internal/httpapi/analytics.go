package httpapi

import (
	"context"
	_ "embed"
	"net/http"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

//go:embed dashboard.html
var dashboardHTML []byte

const analyticsBuckets = 60

type analyzer interface {
	Analytics(ctx context.Context, from, to time.Time, n int) (store.Analytics, error)
}

// EnableAnalytics mounts the P1.7 dashboard. Read-only aggregates, unauthenticated like /hazards.
func (s *Server) EnableAnalytics(a analyzer) { s.analytics = a }

func (s *Server) dashboard(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(dashboardHTML)
}

func (s *Server) analyticsData(w http.ResponseWriter, r *http.Request) {
	var bounds [2]time.Time
	for i, key := range []string{"from", "to"} {
		if value := r.URL.Query().Get(key); value != "" {
			parsed, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				writeAuditError(w, http.StatusBadRequest, "bad_request", key+" harus ISO 8601")
				return
			}
			bounds[i] = parsed
		}
	}
	a, err := s.analytics.Analytics(r.Context(), bounds[0], bounds[1], analyticsBuckets)
	if err != nil {
		s.auditUnavailable(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, a)
}
