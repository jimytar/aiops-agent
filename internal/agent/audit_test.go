package agent

import (
	"encoding/json"
	"testing"
	"time"
)

func TestAuditEntryMarshal(t *testing.T) {
	errMsg := "something failed"
	entry := AuditEntry{
		Timestamp:  time.Now().UTC(),
		Level:      "audit",
		ChatID:     42,
		Username:   "testuser",
		Tool:       "kubectl_get",
		Input:      json.RawMessage(`{"cluster":"bastion"}`),
		Confirmed:  true,
		Nonce:      "ABC123",
		DurationMs: 150,
		Error:      &errMsg,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal AuditEntry: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	checks := map[string]interface{}{
		"level":       "audit",
		"chat_id":     float64(42),
		"username":    "testuser",
		"tool":        "kubectl_get",
		"confirmed":   true,
		"nonce":       "ABC123",
		"duration_ms": float64(150),
	}
	for key, want := range checks {
		if got, ok := decoded[key]; !ok || got != want {
			t.Errorf("decoded[%q] = %v (ok=%v), want %v", key, got, ok, want)
		}
	}
	if errField, ok := decoded["error"]; !ok || errField != "something failed" {
		t.Errorf("decoded[error] = %v", decoded["error"])
	}
}

func TestAuditEntryNoError(t *testing.T) {
	entry := AuditEntry{
		Timestamp: time.Now().UTC(),
		Level:     "audit",
		Tool:      "kubectl_logs",
		Input:     json.RawMessage(`{}`),
		Error:     nil,
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// error field should be omitted when nil (omitempty).
	if _, ok := decoded["error"]; ok {
		t.Error("error field should be omitted when nil")
	}
}

func TestAuditEntryNoNonce(t *testing.T) {
	entry := AuditEntry{
		Timestamp: time.Now().UTC(),
		Level:     "audit",
		Tool:      "kubectl_get",
		Input:     json.RawMessage(`{}`),
		Nonce:     "",
	}

	data, err := json.Marshal(entry)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	// nonce field should be omitted when empty (omitempty).
	if _, ok := decoded["nonce"]; ok {
		t.Error("nonce field should be omitted when empty")
	}
}

func TestAuditDoesNotPanic(t *testing.T) {
	// audit() writes to os.Stdout. Verify it doesn't panic with various inputs.
	audit(1, "user", "kubectl_get", json.RawMessage(`{"cluster":"test"}`), true, "ABC123", 100, nil)
	audit(2, "", "kubectl_delete", json.RawMessage(`{}`), false, "", 0, nil)

	errVal := func() error {
		return &testError{"test error"}
	}()
	audit(3, "user", "ssh_exec", json.RawMessage(`{"host":"node1"}`), true, "XYZ", 50, errVal)
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }
