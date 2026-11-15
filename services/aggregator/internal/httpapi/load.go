package httpapi

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/domain"
	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

const maxLoadEvents = 5000

// loadCatalog bulk-loads historical earthquakes (P1.1) through the normal write path, so
// they are partitioned, replicated and fenced like polled events. Body: a JSON array or
// NDJSON of BMKG seismic events.
// shortcut: assumes the assistant's catalog uses BMKG field names; convert it (jq) before
// loading if it does not, or add a mapping here once the format is known (context-m2 §10 #7).
func (s *Server) loadCatalog(w http.ResponseWriter, r *http.Request) {
	seismic, err := decodeSeismic(http.MaxBytesReader(w, r.Body, 16<<20))
	if err != nil {
		writeAuditError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	now := time.Now().UTC()
	events := make([]domain.HazardEvent, len(seismic))
	for i, e := range seismic {
		events[i] = domain.MapSeismic(e, nil, now)
	}
	started := time.Now()
	// Epoch 0 like ingest until internal/leader provides the lease epoch.
	changed, err := s.repository.Upsert(r.Context(), 0, events, r.Header.Get("X-Correlation-ID"))
	if errors.Is(err, store.ErrFenced) {
		writeAuditError(w, http.StatusConflict, "fenced", "epoch usang")
		return
	}
	if err != nil {
		s.auditUnavailable(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"received": len(events), "changed": len(changed),
		"duration_ms": time.Since(started).Milliseconds(),
	})
}

func decodeSeismic(body io.Reader) ([]domain.SeismicEvent, error) {
	reader := bufio.NewReader(body)
	first, err := peekNonSpace(reader)
	if err != nil {
		return nil, fmt.Errorf("body kosong atau tidak terbaca: %w", err)
	}
	var events []domain.SeismicEvent
	decoder := json.NewDecoder(reader)
	if first == '[' {
		err = decoder.Decode(&events)
	} else {
		for err == nil {
			var e domain.SeismicEvent
			if err = decoder.Decode(&e); err == nil {
				events = append(events, e)
			}
		}
		if errors.Is(err, io.EOF) {
			err = nil
		}
	}
	if err != nil {
		return nil, fmt.Errorf("JSON tidak valid setelah %d event: %w", len(events), err)
	}
	if len(events) == 0 || len(events) > maxLoadEvents {
		return nil, fmt.Errorf("jumlah event harus 1..%d per request, dapat %d", maxLoadEvents, len(events))
	}
	for i, e := range events {
		if e.EventID == "" || e.OccurredAt.IsZero() || e.EpicenterLat < -90 || e.EpicenterLat > 90 ||
			e.EpicenterLon < -180 || e.EpicenterLon > 180 {
			return nil, fmt.Errorf("event ke-%d (%q): event_id, occurred_at, dan koordinat wajib valid", i, e.EventID)
		}
	}
	return events, nil
}

func peekNonSpace(r *bufio.Reader) (byte, error) {
	for {
		b, err := r.ReadByte()
		if err != nil {
			return 0, err
		}
		if b != ' ' && b != '\t' && b != '\r' && b != '\n' {
			return b, r.UnreadByte()
		}
	}
}
