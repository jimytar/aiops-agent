package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

func mustRaw(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}

func TestTranscriptFromRawTextBlocks(t *testing.T) {
	msgs := []json.RawMessage{
		mustRaw(anthropic.NewUserMessage(anthropic.NewTextBlock("hello"))),
		mustRaw(anthropic.NewAssistantMessage(anthropic.NewTextBlock("world"))),
	}
	out := transcriptFromRaw(msgs)
	if !strings.Contains(out, "User: hello") {
		t.Errorf("transcript missing user message, got:\n%s", out)
	}
	if !strings.Contains(out, "Assistant: world") {
		t.Errorf("transcript missing assistant message, got:\n%s", out)
	}
}

func TestTranscriptFromRawToolUse(t *testing.T) {
	msgs := []json.RawMessage{
		mustRaw(anthropic.NewAssistantMessage(anthropic.NewToolUseBlock("id1", map[string]any{}, "kubectl_get"))),
	}
	out := transcriptFromRaw(msgs)
	if !strings.Contains(out, "[tool: kubectl_get]") {
		t.Errorf("transcript missing tool_use block, got:\n%s", out)
	}
}

func TestTranscriptFromRawSkipsInvalidJSON(t *testing.T) {
	msgs := []json.RawMessage{
		json.RawMessage(`not valid json`),
		mustRaw(anthropic.NewUserMessage(anthropic.NewTextBlock("valid"))),
	}
	out := transcriptFromRaw(msgs)
	// Invalid message should be skipped; valid one should appear.
	if !strings.Contains(out, "User: valid") {
		t.Errorf("transcript should contain valid message, got:\n%s", out)
	}
}

func TestTranscriptFromRawEmpty(t *testing.T) {
	out := transcriptFromRaw(nil)
	if out != "" {
		t.Errorf("expected empty transcript, got %q", out)
	}
}

// --- round-trip helpers ---

func TestRawToStdRoundTrip(t *testing.T) {
	original := []json.RawMessage{
		mustRaw(anthropic.NewUserMessage(anthropic.NewTextBlock("ping"))),
		mustRaw(anthropic.NewAssistantMessage(anthropic.NewTextBlock("pong"))),
	}
	std := rawToStd(original)
	if len(std) != 2 {
		t.Fatalf("rawToStd: got %d messages, want 2", len(std))
	}
	back := stdToRaw(std)
	if len(back) != 2 {
		t.Fatalf("stdToRaw: got %d messages, want 2", len(back))
	}
}

func TestRawToBetaRoundTrip(t *testing.T) {
	original := []json.RawMessage{
		mustRaw(anthropic.NewUserMessage(anthropic.NewTextBlock("ping"))),
	}
	beta := rawToBeta(original)
	if len(beta) != 1 {
		t.Fatalf("rawToBeta: got %d messages, want 1", len(beta))
	}
	back := betaToRaw(beta)
	if len(back) != 1 {
		t.Fatalf("betaToRaw: got %d messages, want 1", len(back))
	}
}

func TestRawToStdPreservesContent(t *testing.T) {
	original := []json.RawMessage{
		mustRaw(anthropic.NewUserMessage(anthropic.NewTextBlock("hello world"))),
	}
	std := rawToStd(original)
	back := stdToRaw(std)

	var m struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if err := json.Unmarshal(back[0], &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(m.Content) == 0 || m.Content[0].Text != "hello world" {
		t.Errorf("round-trip lost message text, got %+v", m)
	}
}
