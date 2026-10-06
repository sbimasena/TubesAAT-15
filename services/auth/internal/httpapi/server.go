package httpapi

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/sbimasena/TubesAAT-15/services/auth/internal/identity"
	"github.com/sbimasena/TubesAAT-15/services/auth/internal/session"
)

type identityKey struct{}

type requestIdentity struct {
	clientID string
	scope    string
}

func NewHandler(logger *slog.Logger, sessions *session.Service, internalSecret string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok", "service": "auth"})
	})
	if sessions != nil {
		mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				ClientID string `json:"client_id"`
				Password string `json:"password"`
			}
			if !decode(w, r, &input) {
				return
			}
			pair, err := sessions.Login(r.Context(), input.ClientID, input.Password)
			if err != nil {
				sessionError(w, err)
				return
			}
			info := r.Context().Value(identityKey{}).(*requestIdentity)
			info.clientID, info.scope = pair.ClientID, pair.Scope
			writeJSON(w, pair)
		})
		mux.HandleFunc("POST /refresh", func(w http.ResponseWriter, r *http.Request) {
			var input struct {
				RefreshToken string `json:"refresh_token"`
			}
			if !decode(w, r, &input) {
				return
			}
			pair, err := sessions.Refresh(r.Context(), input.RefreshToken)
			if err != nil {
				sessionError(w, err)
				return
			}
			info := r.Context().Value(identityKey{}).(*requestIdentity)
			info.clientID, info.scope = pair.ClientID, pair.Scope
			writeJSON(w, pair)
		})
		mux.HandleFunc("POST /internal/v1/introspect", func(w http.ResponseWriter, r *http.Request) {
			given := sha256.Sum256([]byte(r.Header.Get("X-Auth-Service-Key")))
			want := sha256.Sum256([]byte(internalSecret))
			if len(internalSecret) < 32 || len(r.Header.Values("X-Auth-Service-Key")) != 1 || subtle.ConstantTimeCompare(given[:], want[:]) != 1 {
				http.Error(w, "invalid service credential", http.StatusUnauthorized)
				return
			}
			var input struct {
				Token string `json:"token"`
			}
			if !decode(w, r, &input) {
				return
			}
			claims, err := sessions.Inspect(r.Context(), input.Token)
			if errors.Is(err, session.ErrToken) {
				writeJSON(w, map[string]bool{"active": false})
				return
			}
			if err != nil {
				sessionError(w, err)
				return
			}
			info := r.Context().Value(identityKey{}).(*requestIdentity)
			info.clientID, info.scope = claims.Subject, claims.Scope
			if logger != nil {
				logger.Info("token validated", "service", "auth", "client_id", claims.Subject, "scope", claims.Scope,
					"correlation_id", w.Header().Get("X-Correlation-ID"))
			}
			writeJSON(w, map[string]any{"active": true, "client_id": claims.Subject,
				"scope": claims.Scope, "session_id": claims.SessionID})
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.Header.Get("X-Correlation-ID")
		if id == "" || len(id) > 128 {
			id = newCorrelationID()
		}
		w.Header().Set("X-Correlation-ID", id)
		w.Header().Set("Cache-Control", "no-store")
		info := &requestIdentity{}
		r = r.WithContext(context.WithValue(r.Context(), identityKey{}, info))
		wrapped := &statusWriter{ResponseWriter: w}
		started := time.Now()
		mux.ServeHTTP(wrapped, r)
		if logger != nil {
			status := wrapped.status
			if status == 0 {
				status = http.StatusOK
			}
			attrs := []any{"service", "auth", "correlation_id", id,
				"operation", r.URL.Path, "latency_ms", float64(time.Since(started).Microseconds()) / 1000,
				"status", status}
			if info.clientID != "" {
				attrs = append(attrs, "client_id", info.clientID, "scope", info.scope)
			}
			logger.Info("request complete", attrs...)
		}
	})
}

func decode(w http.ResponseWriter, r *http.Request, input any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(input); err != nil {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return false
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		http.Error(w, "invalid JSON request", http.StatusBadRequest)
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func sessionError(w http.ResponseWriter, err error) {
	if errors.Is(err, identity.ErrCredentials) || errors.Is(err, session.ErrToken) {
		http.Error(w, "invalid credentials or token", http.StatusUnauthorized)
		return
	}
	// Do not expose signing, storage, or credential details to callers.
	http.Error(w, "Auth request unavailable", http.StatusServiceUnavailable)
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func newCorrelationID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(value[:])
}
