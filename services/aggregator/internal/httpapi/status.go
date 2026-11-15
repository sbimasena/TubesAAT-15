package httpapi

import (
	"context"
	"net/http"
	"os"
	"time"
)

// statusTimeout bounds every section, so one stuck dependency cannot hang the endpoint.
const statusTimeout = 2 * time.Second

// AddStatus registers one section of GET /internal/v1/status (context-m2.md §8.2). Each
// owner adds theirs from cmd/server/main.go, e.g. A: "leader", "satellite"; B:
// "deadletter"; C: "storage". The function returns any JSON-encodable value and should
// answer from memory or with a short query. Register before Handler is called.
func (s *Server) AddStatus(name string, section func(context.Context) any) {
	if s.status == nil {
		s.status = map[string]func(context.Context) any{}
	}
	s.status[name] = section
}

// internalStatus is read-only and internal: the Aggregator Service is ClusterIP and the
// public Client API never forwards here.
func (s *Server) internalStatus(w http.ResponseWriter, r *http.Request) {
	body := map[string]any{"instance": os.Getenv("HOSTNAME"), "node": os.Getenv("NODE_NAME")}
	ctx, cancel := context.WithTimeout(r.Context(), statusTimeout)
	defer cancel()
	for name, section := range s.status {
		body[name] = section(ctx)
	}
	writeJSON(w, http.StatusOK, body)
}
