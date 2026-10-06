package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/sbimasena/TubesAAT-15/services/client-api/internal/authclient"
)

type accessValidator interface {
	Validate(context.Context, string, string) (authclient.Identity, error)
}

type requestIdentity struct{ authclient.Identity }
type identityKey struct{}

func authenticate(validator accessValidator, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(r.Header.Values("Authorization")) != 1 || len(parts) != 2 ||
			!strings.EqualFold(parts[0], "Bearer") || len(parts[1]) > 4096 {
			w.Header().Set("WWW-Authenticate", "Bearer")
			writeError(w, http.StatusUnauthorized, "invalid_access_token", "invalid access token")
			return
		}
		if validator == nil {
			writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "Auth unavailable")
			return
		}
		id, _ := r.Context().Value(correlationKey{}).(string)
		identity, err := validator.Validate(r.Context(), parts[1], id)
		if r.Context().Err() != nil {
			return
		}
		if err != nil {
			if errors.Is(err, authclient.ErrInvalid) {
				w.Header().Set("WWW-Authenticate", "Bearer")
				writeError(w, http.StatusUnauthorized, "invalid_access_token", "invalid access token")
			} else {
				writeError(w, http.StatusServiceUnavailable, "auth_unavailable", "Auth unavailable")
			}
			return
		}
		info := r.Context().Value(identityKey{}).(*requestIdentity)
		info.Identity = identity
		next(w, r)
	}
}
