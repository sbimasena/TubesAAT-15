package httpapi

import (
	"context"
	"crypto/subtle"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

// EnableAudit mounts /internal/audit/{events,checksum,location} (kontrak-m2.md §7).
func (s *Server) EnableAudit(audit store.Store, token string) {
	s.audit, s.auditToken = audit, token
}

func (s *Server) requireAuditToken(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, []byte("Bearer "+s.auditToken)) != 1 {
			writeAuditError(w, http.StatusUnauthorized, "unauthorized", "token tidak valid")
			return
		}
		next(w, r)
	}
}

type unprocessedReader interface {
	Unprocessed(context.Context, string, string, int) (store.UnprocessedPage, error)
}

func (s *Server) auditUnprocessed(w http.ResponseWriter, r *http.Request) {
	q, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		writeAuditError(w, http.StatusBadRequest, "bad_request", "query tidak valid")
		return
	}
	for key, values := range q {
		if len(values) != 1 || (key != "source" && key != "limit" && key != "cursor") {
			writeAuditError(w, http.StatusBadRequest, "bad_request", "query tidak valid")
			return
		}
	}
	source, cursor := q.Get("source"), q.Get("cursor")
	switch source {
	case "", "BMKG", "PVMBG", "SATELIT", "SENSOR_PANTAI", "FIELD_REPORT":
	default:
		writeAuditError(w, http.StatusBadRequest, "bad_request", "source tidak valid")
		return
	}
	limit := 100
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 1000 {
			writeAuditError(w, http.StatusBadRequest, "bad_request", "limit harus 1..1000")
			return
		}
	}
	if len(cursor) > 700 {
		writeAuditError(w, http.StatusBadRequest, "bad_request", "cursor tidak valid")
		return
	}
	reader, ok := s.audit.(unprocessedReader)
	if !ok {
		writeAuditError(w, http.StatusServiceUnavailable, "unavailable", "DLQ storage tidak tersedia")
		return
	}
	page, err := reader.Unprocessed(r.Context(), source, cursor, limit)
	if errors.Is(err, store.ErrInvalidCursor) {
		writeAuditError(w, http.StatusBadRequest, "bad_request", "cursor tidak valid")
		return
	}
	if err != nil {
		s.auditUnavailable(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func writeAuditError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func (s *Server) auditUnavailable(w http.ResponseWriter, r *http.Request, err error) {
	s.logger.Error("audit query failed", "correlation_id", r.Header.Get("X-Correlation-ID"), "error", err)
	writeAuditError(w, http.StatusServiceUnavailable, "unavailable", "storage tidak terjangkau")
}

func orEmpty(units []string) []string {
	if units == nil {
		return []string{}
	}
	return units
}

func (s *Server) auditEvents(w http.ResponseWriter, r *http.Request) {
	query := r.URL.Query()
	filter := store.Filter{Source: query.Get("source"), Limit: 100}
	if value := query.Get("since"); value != "" {
		since, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			writeAuditError(w, http.StatusBadRequest, "bad_request", "since harus ISO 8601")
			return
		}
		filter.IngestedSince = since
	}
	if value := query.Get("limit"); value != "" {
		limit, err := strconv.Atoi(value)
		if err != nil || limit < 1 || limit > 1000 {
			writeAuditError(w, http.StatusBadRequest, "bad_request", "limit harus 1..1000")
			return
		}
		filter.Limit = limit
	}
	page, err := s.audit.Query(r.Context(), filter, query.Get("cursor"))
	if errors.Is(err, store.ErrInvalidCursor) {
		writeAuditError(w, http.StatusBadRequest, "bad_request", "cursor tidak valid")
		return
	}
	if err != nil {
		s.auditUnavailable(w, r, err)
		return
	}
	for i := range page.Items {
		page.Items[i].OccurredAt = page.Items[i].OccurredAt.UTC()
		page.Items[i].IngestedAt = page.Items[i].IngestedAt.UTC()
	}
	if page.Items == nil {
		page.Items = []domain.HazardEvent{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": page.Items, "next_cursor": page.NextCursor,
		"partial": page.Partial, "unavailable_units": orEmpty(page.UnavailableUnits),
	})
}

// auditChecksum covers every source when source is omitted (kontrak-m2.md §7.3 leaves it open).
func (s *Server) auditChecksum(w http.ResponseWriter, r *http.Request) {
	sum, err := s.audit.Checksum(r.Context(), r.URL.Query().Get("source"))
	if err != nil {
		s.auditUnavailable(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"source": sum.Source, "count": sum.Count, "checksum": sum.Checksum, "algorithm": "sha256",
		"as_of": sum.AsOf.UTC(), "partial": sum.Partial, "unavailable_units": orEmpty(sum.UnavailableUnits),
	})
}

func (s *Server) auditLocation(w http.ResponseWriter, r *http.Request) {
	hazardID := r.URL.Query().Get("hazard_id")
	if hazardID == "" {
		writeAuditError(w, http.StatusBadRequest, "bad_request", "hazard_id wajib")
		return
	}
	locations, partial, err := s.audit.Locate(r.Context(), hazardID)
	if err != nil {
		s.auditUnavailable(w, r, err)
		return
	}
	// Not found but a replica did not answer: unknown, so 200 partial rather than 404.
	if len(locations) == 0 && !partial {
		writeAuditError(w, http.StatusNotFound, "not_found", "hazard_id tidak ditemukan")
		return
	}
	out := make([]map[string]string, 0, len(locations))
	for _, l := range locations {
		out = append(out, map[string]string{"node": l.Node, "unit": l.Unit, "role": l.Role})
	}
	writeJSON(w, http.StatusOK, map[string]any{"hazard_id": hazardID, "locations": out, "partial": partial})
}
