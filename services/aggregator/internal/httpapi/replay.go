package httpapi

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/sbimasena/TubesAAT-15/services/aggregator/internal/store"
)

type replayQueuer interface {
	QueuePVMBGReplay(context.Context, int64, []string) error
}

func (s *Server) auditReplay(w http.ResponseWriter, r *http.Request) {
	reader := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	reader.DisallowUnknownFields()
	var input struct {
		MessageIDs []string `json:"message_ids"`
	}
	if reader.Decode(&input) != nil || len(input.MessageIDs) == 0 || len(input.MessageIDs) > 32 || reader.Decode(new(any)) != io.EOF {
		writeAuditError(w, http.StatusBadRequest, "bad_request", "message_ids wajib (1..32)")
		return
	}
	for _, id := range input.MessageIDs {
		hash, ok := strings.CutPrefix(id, "pvmbg:")
		if _, err := hex.DecodeString(hash); !ok || len(hash) != 64 || err != nil || hash != strings.ToLower(hash) {
			writeAuditError(w, http.StatusBadRequest, "bad_request", "message_id PVMBG tidak valid")
			return
		}
	}
	queue, ok := s.audit.(replayQueuer)
	if !ok || s.writeTerm == nil {
		writeAuditError(w, http.StatusServiceUnavailable, "unavailable", "replay tidak tersedia")
		return
	}
	ctx := r.Context()
	epoch, err := s.writeTerm(ctx)
	if err != nil {
		w.Header().Set("Retry-After", "1")
		writeAuditError(w, http.StatusServiceUnavailable, "not_leader", "ulangi permintaan replay")
		return
	}
	ctx = store.WithWriteVerifier(ctx, func(ctx context.Context) error {
		_, err := s.writeTerm(ctx)
		return err
	})
	if err := queue.QueuePVMBGReplay(ctx, epoch, input.MessageIDs); err != nil {
		s.auditUnavailable(w, r, err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "accepted"})
}
