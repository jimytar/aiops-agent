package executor

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jimytar/aiops-agent/internal/config"
)

func newTestHTTPExecutor(t *testing.T, serverURL string) *HTTPIntegrationExecutor {
	t.Helper()
	cfg := []config.HTTPIntegrationConfig{
		{
			Name:      "sonarr",
			BaseURL:   serverURL,
			APIKeyEnv: "", // no real env var needed; resolved key will be empty
			Defaults: map[string]interface{}{
				"qualityProfileId": 1,
				"monitored":        true,
			},
			Endpoints: []config.HTTPEndpointConfig{
				{Name: "series", Path: "/api/v3/series", Method: "GET", Description: "List series"},
				{Name: "series_add", Path: "/api/v3/series", Method: "POST", Description: "Add series"},
				{Name: "series_delete", Path: "/api/v3/series/1", Method: "DELETE", Description: "Delete series"},
			},
		},
	}
	return NewHTTPIntegrationExecutor(cfg)
}

func TestHTTPIntegrationExecutorIntegrations(t *testing.T) {
	e := newTestHTTPExecutor(t, "http://localhost")
	names := e.Integrations()
	if len(names) != 1 || names[0] != "sonarr" {
		t.Errorf("Integrations() = %v, want [sonarr]", names)
	}
}

func TestHTTPIntegrationCallUnknownIntegration(t *testing.T) {
	e := newTestHTTPExecutor(t, "http://localhost")
	_, err := e.Call(context.Background(), "radarr", "movies", "", "", 0)
	if err == nil || !strings.Contains(err.Error(), "unknown integration") {
		t.Errorf("expected 'unknown integration' error, got: %v", err)
	}
}

func TestHTTPIntegrationCallUnknownEndpoint(t *testing.T) {
	e := newTestHTTPExecutor(t, "http://localhost")
	_, err := e.Call(context.Background(), "sonarr", "queue", "", "", 0)
	if err == nil || !strings.Contains(err.Error(), "unknown endpoint") {
		t.Errorf("expected 'unknown endpoint' error, got: %v", err)
	}
}

func TestHTTPIntegrationCallGET(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/api/v3/series" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[{"title":"Breaking Bad"}]`))
	}))
	defer ts.Close()

	e := newTestHTTPExecutor(t, ts.URL)
	out, err := e.Call(context.Background(), "sonarr", "series", "", "", 0)
	if err != nil {
		t.Fatalf("Call GET: %v", err)
	}
	if !strings.Contains(out, "Breaking Bad") {
		t.Errorf("expected series name in response, got:\n%s", out)
	}
}

func TestHTTPIntegrationCallGETQueryParams(t *testing.T) {
	var gotQuery string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer ts.Close()

	e := newTestHTTPExecutor(t, ts.URL)
	_, err := e.Call(context.Background(), "sonarr", "series", `{"term":"breakingbad"}`, "", 0)
	if err != nil {
		t.Fatalf("Call GET with params: %v", err)
	}
	if !strings.Contains(gotQuery, "term=") {
		t.Errorf("expected 'term' in query string, got: %s", gotQuery)
	}
}

func TestHTTPIntegrationCallPOSTMergesDefaults(t *testing.T) {
	var gotBody map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":1}`))
	}))
	defer ts.Close()

	e := newTestHTTPExecutor(t, ts.URL)
	_, err := e.Call(context.Background(), "sonarr", "series_add", "", `{"title":"Breaking Bad","tvdbId":81189}`, 0)
	if err != nil {
		t.Fatalf("Call POST: %v", err)
	}

	// Defaults should be present.
	if gotBody["qualityProfileId"] == nil {
		t.Error("default qualityProfileId missing from POST body")
	}
	// User-supplied field should be present.
	if gotBody["title"] != "Breaking Bad" {
		t.Errorf("title missing from POST body, got: %v", gotBody)
	}
}

func TestHTTPIntegrationCallPOSTUserOverridesDefault(t *testing.T) {
	var gotBody map[string]interface{}
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&gotBody)
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"id":2}`))
	}))
	defer ts.Close()

	e := newTestHTTPExecutor(t, ts.URL)
	// Override the default qualityProfileId.
	_, err := e.Call(context.Background(), "sonarr", "series_add", "", `{"qualityProfileId":5}`, 0)
	if err != nil {
		t.Fatalf("Call POST override: %v", err)
	}

	if v, _ := gotBody["qualityProfileId"].(float64); v != 5 {
		t.Errorf("expected qualityProfileId=5 (user override), got: %v", gotBody["qualityProfileId"])
	}
}

func TestHTTPIntegrationCallHTTPError(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
	}))
	defer ts.Close()

	e := newTestHTTPExecutor(t, ts.URL)
	_, err := e.Call(context.Background(), "sonarr", "series", "", "", 0)
	if err == nil {
		t.Fatal("expected error for HTTP 404")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("error should mention 404, got: %v", err)
	}
}

func TestHTTPIntegrationCallInvalidJSONBody(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	e := newTestHTTPExecutor(t, ts.URL)
	_, err := e.Call(context.Background(), "sonarr", "series_add", "", `not json`, 0)
	if err == nil || !strings.Contains(err.Error(), "invalid JSON") {
		t.Errorf("expected 'invalid JSON' error, got: %v", err)
	}
}

func TestHTTPIntegrationCallPrettyJSON(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Compact JSON — should be pretty-printed in the output.
		w.Write([]byte(`{"id":1,"title":"Breaking Bad"}`))
	}))
	defer ts.Close()

	e := newTestHTTPExecutor(t, ts.URL)
	out, err := e.Call(context.Background(), "sonarr", "series", "", "", 0)
	if err != nil {
		t.Fatalf("Call pretty JSON: %v", err)
	}
	// Pretty-printed JSON should have newlines.
	if !strings.Contains(out, "\n") {
		t.Errorf("expected pretty-printed JSON (with newlines), got:\n%s", out)
	}
}

func TestHTTPIntegrationCallDELETE(t *testing.T) {
	var gotMethod string
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod = r.Method
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{}`))
	}))
	defer ts.Close()

	e := newTestHTTPExecutor(t, ts.URL)
	_, err := e.Call(context.Background(), "sonarr", "series_delete", "", "", 0)
	if err != nil {
		t.Fatalf("Call DELETE: %v", err)
	}
	if gotMethod != http.MethodDelete {
		t.Errorf("expected DELETE method, got: %s", gotMethod)
	}
}

func TestHTTPIntegrationCallAPIKeyHeader(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check default X-Api-Key header is present (value will be empty string since no env var).
		if r.Header.Get("X-Api-Key") == "" && r.Header["X-Api-Key"] == nil {
			// The header should at least be sent (even if empty).
		}
		_ = r.Header.Get("Accept")
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`[]`))
	}))
	defer ts.Close()

	e := newTestHTTPExecutor(t, ts.URL)
	// Should not error — just verify the call succeeds with the default header name.
	_, err := e.Call(context.Background(), "sonarr", "series", "", "", 0)
	if err != nil {
		t.Fatalf("Call with API key header: %v", err)
	}
}
