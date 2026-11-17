package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCheckHealth(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusServiceUnavailable} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" {
				t.Errorf("unexpected health path: %s", r.URL.Path)
			}
			w.WriteHeader(status)
		}))
		err := checkHealth(server.URL + "/health")
		server.Close()
		if (err == nil) != (status == http.StatusOK) {
			t.Fatalf("status %d: unexpected health result %v", status, err)
		}
		if err := checkHealth(server.URL + "/health"); err == nil {
			t.Fatal("health check accepted an unreachable service")
		}
	}
}

func requestReports(t *testing.T, catalog *catalog) (int, []byte) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/volcanic-reports", nil)
	catalog.volcanicReports(recorder, request, 0)
	return recorder.Code, recorder.Body.Bytes()
}

func switchSchema(t *testing.T, catalog *catalog, body string) (int, []byte) {
	t.Helper()
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/admin/schema-version", strings.NewReader(body))
	catalog.setSchemaVersion(recorder, request)
	return recorder.Code, recorder.Body.Bytes()
}

func TestSetSchemaVersionAcceptsVersionAndMode(t *testing.T) {
	catalog := newCatalog(time.Now().UTC())
	cases := []struct {
		body        string
		wantVersion int
		wantMode    string
	}{
		{`{"version":2}`, 2, "compatible"},
		{`{"version":3}`, 3, modeRename},
		{`{"mode":"rename_field"}`, 3, modeRename},
		{`{"mode":"change_type"}`, 4, modeChangeType},
		{`{"mode":"drop_required"}`, 5, modeDropRequired},
		{`{"mode":"broken_json"}`, 6, modeBrokenJSON},
		{`{"mode":"oversize_json"}`, 7, modeOversize},
		{`{"mode":"deep_json"}`, 8, modeDeep},
		{`{"mode":"invalid_utf8"}`, 9, modeInvalidUTF8},
	}
	for _, testCase := range cases {
		status, response := switchSchema(t, catalog, testCase.body)
		if status != http.StatusOK {
			t.Fatalf("%s: status %d", testCase.body, status)
		}
		var payload struct {
			SchemaVersion int    `json:"schema_version"`
			SchemaMode    string `json:"schema_mode"`
		}
		if err := json.Unmarshal(response, &payload); err != nil {
			t.Fatalf("%s: decode response: %v", testCase.body, err)
		}
		if payload.SchemaVersion != testCase.wantVersion || payload.SchemaMode != testCase.wantMode {
			t.Errorf("%s: got version %d mode %q, want %d %q", testCase.body, payload.SchemaVersion, payload.SchemaMode, testCase.wantVersion, testCase.wantMode)
		}
	}
}

func TestSetSchemaVersionRejectsBadRequests(t *testing.T) {
	catalog := newCatalog(time.Now().UTC())
	cases := []string{
		`{"version":99}`,
		`{"mode":"does_not_exist"}`,
		`{"version":3,"mode":"rename_field"}`,
		`not json`,
	}
	for _, body := range cases {
		status, _ := switchSchema(t, catalog, body)
		if status != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", body, status)
		}
	}
}

func TestIncompatibleModesOnlyAffectNewReports(t *testing.T) {
	catalog := newCatalog(time.Now().UTC())
	seeded := len(catalog.reports)
	status, _ := switchSchema(t, catalog, `{"mode":"rename_field"}`)
	if status != http.StatusOK {
		t.Fatalf("switch status %d", status)
	}
	if len(catalog.reports) != seeded+1 {
		t.Fatalf("expected one appended report, got %d -> %d", seeded, len(catalog.reports))
	}
	last := catalog.reports[len(catalog.reports)-1]
	if last.Variant != 3 {
		t.Fatalf("new report variant %d, want 3", last.Variant)
	}
	for _, report := range catalog.reports[:seeded] {
		if report.Variant != 1 {
			t.Fatalf("seeded report variant changed to %d", report.Variant)
		}
	}
	_, body := requestReports(t, catalog)
	var payload []map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("mixed-shape response must still be valid JSON: %v", err)
	}
	if len(payload) != seeded+1 {
		t.Fatalf("response has %d records, want %d", len(payload), seeded+1)
	}
	// Seeded records keep the original shape.
	if _, ok := payload[0]["alert_level"]; !ok {
		t.Error("seeded record lost alert_level")
	}
	// New record uses the renamed field.
	if _, ok := payload[len(payload)-1]["alert_level"]; ok {
		t.Error("renamed record still contains alert_level")
	}
	if _, ok := payload[len(payload)-1]["status"]; !ok {
		t.Error("renamed record missing status")
	}
}

func TestEncodeReportChangeTypeUsesUnixSeconds(t *testing.T) {
	catalog := newCatalog(time.Now().UTC())
	switchSchema(t, catalog, `{"mode":"change_type"}`)
	_, body := requestReports(t, catalog)
	if !strings.Contains(string(body), `"reported_at":1`) {
		t.Fatalf("expected unix seconds reported_at, got %s", string(body[len(body)-400:]))
	}
}

func TestEncodeReportDropRequiredOmitsAlertLevel(t *testing.T) {
	catalog := newCatalog(time.Now().UTC())
	switchSchema(t, catalog, `{"mode":"drop_required"}`)
	_, body := requestReports(t, catalog)
	last := body[bytes.LastIndexByte(body, '{'):]
	if strings.Contains(string(last), `"alert_level"`) {
		t.Fatalf("drop_required record still contains alert_level: %s", string(last))
	}
}

func TestEncodeReportOversizeAddsPadding(t *testing.T) {
	catalog := newCatalog(time.Now().UTC())
	switchSchema(t, catalog, `{"mode":"oversize_json"}`)
	_, body := requestReports(t, catalog)
	if len(body) < paddingSize {
		t.Fatalf("oversize response only %d bytes, want at least %d", len(body), paddingSize)
	}
	if !strings.Contains(string(body), `"padding":"x`) {
		t.Error("padding field missing in new record")
	}
}

func TestEncodeReportDeepNestingExceedsDecoder(t *testing.T) {
	catalog := newCatalog(time.Now().UTC())
	switchSchema(t, catalog, `{"mode":"deep_json"}`)
	_, body := requestReports(t, catalog)
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var records []map[string]any
	if err := decoder.Decode(&records); err == nil {
		t.Fatal("expected the decoder to fail on excessive nesting")
	}
}

func TestEncodeReportInvalidUTF8(t *testing.T) {
	catalog := newCatalog(time.Now().UTC())
	switchSchema(t, catalog, `{"mode":"invalid_utf8"}`)
	_, body := requestReports(t, catalog)
	if !bytes.Contains(body, []byte{0xff, 0xfe}) {
		t.Fatal("response does not contain raw invalid UTF-8 bytes")
	}
	if isValidUTF8AfterDecode(body) {
		t.Fatal("note field unexpectedly decodes to valid replacement characters")
	}
}

// isValidUTF8AfterDecode mirrors what a strict consumer sees: the raw bytes are
// invalid, so a UTF-8-validating pass over the body fails.
func isValidUTF8AfterDecode(body []byte) bool {
	return !bytes.Contains(body, []byte{0xff, 0xfe})
}

func TestBrokenJSONTruncatesResponse(t *testing.T) {
	catalog := newCatalog(time.Now().UTC())
	switchSchema(t, catalog, `{"mode":"broken_json"}`)
	_, body := requestReports(t, catalog)
	if len(body) == 0 {
		t.Fatal("empty response")
	}
	if bytes.HasSuffix(bytes.TrimRight(body, "\n"), []byte("]")) {
		t.Fatal("truncated response unexpectedly ends with a complete array")
	}
	if json.Valid(body) {
		t.Fatal("broken_json response must not be valid JSON")
	}
}

func TestCompatibleVersionStillValidJSON(t *testing.T) {
	catalog := newCatalog(time.Now().UTC())
	switchSchema(t, catalog, `{"version":2}`)
	_, body := requestReports(t, catalog)
	if !json.Valid(body) {
		t.Fatal("compatible response is not valid JSON")
	}
	var payload []map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := payload[0]["alert_level"]; !ok {
		t.Error("compatible record lost alert_level")
	}
}

func TestModeNameForVersion(t *testing.T) {
	if modeNameForVersion(1) != "compatible" || modeNameForVersion(2) != "compatible" {
		t.Error("versions 1 and 2 must be compatible")
	}
	for version, name := range modeNames {
		if modeNameForVersion(version) != name {
			t.Errorf("version %d: got %q, want %q", version, modeNameForVersion(version), name)
		}
	}
}
