package agent

import (
	"encoding/json"
	"os"
	"time"
)

type AuditEntry struct {
	Timestamp  time.Time       `json:"ts"`
	Level      string          `json:"level"`
	ChatID     int64           `json:"chat_id"`
	Username   string          `json:"username"`
	Tool       string          `json:"tool"`
	Input      json.RawMessage `json:"input"`
	Confirmed  bool            `json:"confirmed"`
	Nonce      string          `json:"nonce,omitempty"`
	DurationMs int64           `json:"duration_ms"`
	Error      *string         `json:"error,omitempty"`
}

var auditEnc = json.NewEncoder(os.Stdout)

func audit(chatID int64, username, tool string, input json.RawMessage, confirmed bool, nonce string, durationMs int64, err error) {
	entry := AuditEntry{
		Timestamp:  time.Now().UTC(),
		Level:      "audit",
		ChatID:     chatID,
		Username:   username,
		Tool:       tool,
		Input:      input,
		Confirmed:  confirmed,
		Nonce:      nonce,
		DurationMs: durationMs,
	}
	if err != nil {
		s := err.Error()
		entry.Error = &s
	}
	// Best-effort; ignore marshal errors.
	_ = auditEnc.Encode(entry)
}
