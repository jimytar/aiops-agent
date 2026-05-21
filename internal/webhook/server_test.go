package webhook

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

type mockSender struct {
	mu   sync.Mutex
	msgs []string
}

func (m *mockSender) SendToChat(_ int64, text string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.msgs = append(m.msgs, text)
}

func (m *mockSender) last() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if len(m.msgs) == 0 {
		return ""
	}
	return m.msgs[len(m.msgs)-1]
}

func newTestServer(token string, chatID int64) (*Server, *mockSender) {
	sender := &mockSender{}
	return NewServer(token, chatID, sender), sender
}

// --- /healthz ---

func TestHandleHealth(t *testing.T) {
	srv, _ := newTestServer("", 0)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	srv.handleHealth(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body = %q, want %q", w.Body.String(), "ok")
	}
}

// --- /webhook method check ---

func TestHandleWebhookMethodNotAllowed(t *testing.T) {
	srv, _ := newTestServer("", 1)
	req := httptest.NewRequest(http.MethodGet, "/webhook", nil)
	w := httptest.NewRecorder()
	srv.handleWebhook(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("GET should return 405, got %d", w.Code)
	}
}

// --- token validation ---

func TestHandleWebhookNoTokenRequired(t *testing.T) {
	srv, _ := newTestServer("", 1)
	body, _ := json.Marshal(map[string]interface{}{"title": "Test"})
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.handleWebhook(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("without token configured, should accept any request; got %d", w.Code)
	}
}

func TestHandleWebhookValidHeaderToken(t *testing.T) {
	srv, _ := newTestServer("secret-token", 1)
	body, _ := json.Marshal(map[string]interface{}{"title": "Alert"})
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Token", "secret-token")
	w := httptest.NewRecorder()
	srv.handleWebhook(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("valid header token should return 202, got %d", w.Code)
	}
}

func TestHandleWebhookValidQueryToken(t *testing.T) {
	srv, _ := newTestServer("secret-token", 1)
	body, _ := json.Marshal(map[string]interface{}{"title": "Alert"})
	req := httptest.NewRequest(http.MethodPost, "/webhook?token=secret-token", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleWebhook(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("valid query token should return 202, got %d", w.Code)
	}
}

func TestHandleWebhookWrongToken(t *testing.T) {
	srv, _ := newTestServer("secret-token", 1)
	body, _ := json.Marshal(map[string]interface{}{"title": "Alert"})
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	req.Header.Set("X-Webhook-Token", "wrong-token")
	w := httptest.NewRecorder()
	srv.handleWebhook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("wrong token should return 401, got %d", w.Code)
	}
}

func TestHandleWebhookMissingToken(t *testing.T) {
	srv, _ := newTestServer("secret-token", 1)
	body, _ := json.Marshal(map[string]interface{}{"title": "Alert"})
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	// No token provided
	w := httptest.NewRecorder()
	srv.handleWebhook(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("missing token should return 401, got %d", w.Code)
	}
}

// --- defaultChatID = 0 ---

func TestHandleWebhookNoDefaultChatID(t *testing.T) {
	srv, sender := newTestServer("", 0)
	body, _ := json.Marshal(map[string]interface{}{"title": "Alert"})
	req := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(body))
	w := httptest.NewRecorder()
	srv.handleWebhook(w, req)

	if w.Code != http.StatusAccepted {
		t.Errorf("no chatID should still return 202, got %d", w.Code)
	}
	if sender.last() != "" {
		t.Error("should not forward message when no defaultChatID configured")
	}
}

// --- formatPayload ---

func TestFormatPayloadGrafana(t *testing.T) {
	payload := map[string]interface{}{
		"title":   "High CPU",
		"message": "CPU usage above 90%",
		"state":   "alerting",
	}
	out := formatPayload(payload, "grafana")
	if !strings.Contains(out, "High CPU") {
		t.Errorf("Grafana format missing title:\n%s", out)
	}
	if !strings.Contains(out, "CPU usage above 90%") {
		t.Errorf("Grafana format missing message:\n%s", out)
	}
	if !strings.Contains(out, "alerting") {
		t.Errorf("Grafana format missing state:\n%s", out)
	}
	if !strings.Contains(out, "grafana") {
		t.Errorf("Grafana format should include source:\n%s", out)
	}
}

func TestFormatPayloadGrafanaBodyField(t *testing.T) {
	payload := map[string]interface{}{
		"title": "Alert",
		"body":  "Body field content",
	}
	out := formatPayload(payload, "")
	if !strings.Contains(out, "Body field content") {
		t.Errorf("formatPayload should use 'body' field:\n%s", out)
	}
}

func TestFormatPayloadGrafanaNoSource(t *testing.T) {
	payload := map[string]interface{}{"title": "Alert", "message": "msg"}
	out := formatPayload(payload, "")
	if strings.Contains(out, "from") {
		t.Errorf("no source should not include 'from':\n%s", out)
	}
}

func TestFormatPayloadAlertmanager(t *testing.T) {
	payload := map[string]interface{}{
		"alerts": []interface{}{
			map[string]interface{}{
				"status": "firing",
				"labels": map[string]interface{}{"alertname": "DiskFull"},
			},
			map[string]interface{}{
				"status": "firing",
				"labels": map[string]interface{}{"alertname": "HighMemory"},
			},
		},
	}
	out := formatPayload(payload, "alertmanager")
	if !strings.Contains(out, "DiskFull") {
		t.Errorf("Alertmanager format missing alertname:\n%s", out)
	}
	if !strings.Contains(out, "2 alert") {
		t.Errorf("Alertmanager format should show count:\n%s", out)
	}
}

func TestFormatPayloadAlertmanagerMoreThan3(t *testing.T) {
	alerts := make([]interface{}, 5)
	for i := 0; i < 5; i++ {
		alerts[i] = map[string]interface{}{
			"status": "firing",
			"labels": map[string]interface{}{"alertname": "Alert"},
		}
	}
	payload := map[string]interface{}{"alerts": alerts}
	out := formatPayload(payload, "")
	if !strings.Contains(out, "and 2 more") {
		t.Errorf("should show '...and N more' for >3 alerts:\n%s", out)
	}
}

func TestFormatPayloadFallbackJSON(t *testing.T) {
	payload := map[string]interface{}{
		"foo": "bar",
		"baz": 42,
	}
	out := formatPayload(payload, "custom")
	if !strings.Contains(out, "Webhook received") {
		t.Errorf("fallback should say 'Webhook received':\n%s", out)
	}
	if !strings.Contains(out, "custom") {
		t.Errorf("fallback should include source:\n%s", out)
	}
	if !strings.Contains(out, "```json") {
		t.Errorf("fallback should include JSON code block:\n%s", out)
	}
	if !strings.Contains(out, "bar") {
		t.Errorf("fallback should include JSON content:\n%s", out)
	}
}

func TestFormatPayloadFallbackNoSource(t *testing.T) {
	payload := map[string]interface{}{"key": "value"}
	out := formatPayload(payload, "")
	if strings.Contains(out, "from") {
		t.Errorf("no source should not include 'from':\n%s", out)
	}
}
