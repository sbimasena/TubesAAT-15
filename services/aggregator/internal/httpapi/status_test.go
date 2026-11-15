package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestInternalStatusRendersRegisteredSections(t *testing.T) {
	t.Setenv("NODE_NAME", "node-b")
	s := &Server{}
	s.AddStatus("storage", func(context.Context) any { return map[string]int{"under_replicated_partitions": 7} })
	s.AddStatus("leader", func(ctx context.Context) any {
		if _, ok := ctx.Deadline(); !ok {
			t.Error("sections must get a bounded context")
		}
		return nil
	})
	recorder := httptest.NewRecorder()
	s.internalStatus(recorder, httptest.NewRequest(http.MethodGet, "/internal/v1/status", nil))
	var body map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil || recorder.Code != http.StatusOK {
		t.Fatalf("code %d body %s", recorder.Code, recorder.Body)
	}
	storage, _ := body["storage"].(map[string]any)
	if body["node"] != "node-b" || storage["under_replicated_partitions"] != 7.0 {
		t.Fatalf("body %v", body)
	}
	if _, ok := body["leader"]; !ok {
		t.Fatal("a registered section must appear even when it returns null")
	}
}
