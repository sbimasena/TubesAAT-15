package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/aggregator"
)

type unprocessedReader interface {
	Unprocessed(context.Context, url.Values, string) (aggregator.UnprocessedPage, error)
}

type pvmbgReplayer interface {
	ReplayPVMBG(context.Context, []byte, string) error
}

func serveReplay(replay pvmbgReplayer, pools *rolePools) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		info := r.Context().Value(identityKey{}).(*requestIdentity)
		if info.Scope != "internal-ops" {
			writeError(w, http.StatusForbidden, "forbidden", "DLQ replay requires Internal Ops")
			return
		}
		raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4096))
		if err != nil || !json.Valid(raw) {
			writeError(w, http.StatusBadRequest, "invalid_body", "invalid replay request")
			return
		}
		release, ok := pools.acquire(info.Scope)
		if !ok {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "concurrency_limit", "audit capacity exceeded")
			return
		}
		defer release()
		if replay == nil {
			writeError(w, http.StatusServiceUnavailable, "aggregator_unavailable", "replay unavailable")
			return
		}
		id, _ := r.Context().Value(correlationKey{}).(string)
		if err := replay.ReplayPVMBG(r.Context(), raw, id); err != nil {
			var failure *aggregator.RequestError
			if errors.As(err, &failure) && failure.Kind == aggregator.ErrorUpstreamStatus {
				if failure.Status == http.StatusBadRequest {
					writeError(w, http.StatusBadRequest, "invalid_body", "invalid replay request")
					return
				}
				if failure.Status == http.StatusServiceUnavailable {
					w.Header().Set("Retry-After", "1")
				}
			}
			aggregatorError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "accepted"})
	}
}

func serveUnprocessed(reader unprocessedReader, pools *rolePools) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		info := r.Context().Value(identityKey{}).(*requestIdentity)
		if info.Scope != "internal-ops" {
			writeError(w, http.StatusForbidden, "forbidden", "DLQ inspection requires Internal Ops")
			return
		}
		query, err := url.ParseQuery(r.URL.RawQuery)
		if err != nil {
			writeError(w, http.StatusBadRequest, "invalid_query", "invalid audit query")
			return
		}
		for key, values := range query {
			if len(values) != 1 || (key != "source" && key != "limit" && key != "cursor") {
				writeError(w, http.StatusBadRequest, "invalid_query", "invalid audit query")
				return
			}
		}
		if reader == nil {
			writeError(w, http.StatusServiceUnavailable, "aggregator_unavailable", "audit unavailable")
			return
		}
		release, ok := pools.acquire(info.Scope)
		if !ok {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "concurrency_limit", "audit capacity exceeded")
			return
		}
		defer release()
		id, _ := r.Context().Value(correlationKey{}).(string)
		page, err := reader.Unprocessed(r.Context(), query, id)
		if r.Context().Err() != nil {
			return
		}
		if err != nil {
			var failure *aggregator.RequestError
			if errors.As(err, &failure) && failure.Kind == aggregator.ErrorUpstreamStatus && failure.Status == http.StatusBadRequest {
				writeError(w, http.StatusBadRequest, "invalid_query", "invalid audit query")
				return
			}
			aggregatorError(w, err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(page)
	}
}
