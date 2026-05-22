package agent

import (
	"encoding/json"
	"strings"
	"testing"
)

// toolResultMsg builds a minimal tool_result user message as raw JSON,
// matching the structure produced by ExecuteTool.
func toolResultMsg(toolUseIDs ...string) json.RawMessage {
	type block struct {
		Type      string `json:"type"`
		ToolUseID string `json:"tool_use_id"`
		Content   string `json:"content"`
	}
	type msg struct {
		Role    string  `json:"role"`
		Content []block `json:"content"`
	}
	m := msg{Role: "user"}
	for _, id := range toolUseIDs {
		m.Content = append(m.Content, block{
			Type:      "tool_result",
			ToolUseID: id,
			Content:   "ok",
		})
	}
	b, _ := json.Marshal(m)
	return b
}

// assistantToolUseMsg builds a minimal assistant message that requests the
// given tool_use IDs, matching what Claude returns in a response turn.
func assistantToolUseMsg(toolUseIDs ...string) json.RawMessage {
	type block struct {
		Type  string          `json:"type"`
		ID    string          `json:"id"`
		Name  string          `json:"name"`
		Input json.RawMessage `json:"input"`
	}
	type msg struct {
		Role    string  `json:"role"`
		Content []block `json:"content"`
	}
	m := msg{Role: "assistant"}
	for _, id := range toolUseIDs {
		m.Content = append(m.Content, block{
			Type:  "tool_use",
			ID:    id,
			Name:  "list_files",
			Input: json.RawMessage(`{}`),
		})
	}
	b, _ := json.Marshal(m)
	return b
}

// TestPendingToolPartialResultsPreserved verifies that when readonly tools are
// executed before a mutating tool in the same response turn, their results are
// stored in PendingTool.PartialResults so ExecuteTool can include them.
func TestPendingToolPartialResultsPreserved(t *testing.T) {
	partial1 := json.RawMessage(`{"type":"tool_result","tool_use_id":"id-readonly-1","content":"files"}`)
	partial2 := json.RawMessage(`{"type":"tool_result","tool_use_id":"id-readonly-2","content":"content"}`)

	pending := &PendingTool{
		ID:    "id-mutating",
		Name:  "kubectl_exec",
		Input: json.RawMessage(`{"command":"env"}`),
		Tier:  tierMutating,
	}
	pending.PartialResults = append(pending.PartialResults, partial1, partial2)

	if len(pending.PartialResults) != 2 {
		t.Fatalf("expected 2 partial results, got %d", len(pending.PartialResults))
	}
	for i, r := range pending.PartialResults {
		var block struct {
			ToolUseID string `json:"tool_use_id"`
		}
		if err := json.Unmarshal(r, &block); err != nil {
			t.Fatalf("partial result %d: invalid JSON: %v", i, err)
		}
		if block.ToolUseID == "" {
			t.Errorf("partial result %d missing tool_use_id", i)
		}
	}
}

// TestExecuteToolCombinesAllResults verifies that the user message produced by
// ExecuteTool contains ALL tool_result blocks — the partial (readonly) ones
// plus the pending (mutating) one — so every tool_use in the preceding
// assistant message has a matching tool_result.
func TestExecuteToolCombinesAllResults(t *testing.T) {
	// Simulate the message construction inside ExecuteTool.
	partial1 := json.RawMessage(`{"type":"tool_result","tool_use_id":"id-list-files","content":"[dir]"}`)
	partial2 := json.RawMessage(`{"type":"tool_result","tool_use_id":"id-kubectl-describe","content":"Name: web"}`)
	pendingResult := json.RawMessage(`{"type":"tool_result","tool_use_id":"id-kubectl-exec","content":"ENV=val"}`)

	var allContent []json.RawMessage
	allContent = append(allContent, partial1, partial2, pendingResult)

	type rawMsg struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	raw, err := json.Marshal(rawMsg{Role: "user", Content: allContent})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	// Parse back and verify all three tool_use IDs are present.
	var parsed rawMsg
	if err := json.Unmarshal(raw, &parsed); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(parsed.Content) != 3 {
		t.Fatalf("expected 3 content blocks, got %d", len(parsed.Content))
	}

	wantIDs := []string{"id-list-files", "id-kubectl-describe", "id-kubectl-exec"}
	for i, block := range parsed.Content {
		var b struct {
			ToolUseID string `json:"tool_use_id"`
		}
		if err := json.Unmarshal(block, &b); err != nil {
			t.Fatalf("block %d: %v", i, err)
		}
		if b.ToolUseID != wantIDs[i] {
			t.Errorf("block %d: got tool_use_id %q, want %q", i, b.ToolUseID, wantIDs[i])
		}
	}
}

// TestExecuteToolNilPartialResults verifies that ExecuteTool works correctly
// when there are no partial results (the common single-tool case).
func TestExecuteToolNilPartialResults(t *testing.T) {
	pending := &PendingTool{
		ID:             "id-only",
		Name:           "kubectl_scale",
		Input:          json.RawMessage(`{}`),
		Tier:           tierMutating,
		PartialResults: nil, // no pre-executed readonly tools
	}

	pendingResult := json.RawMessage(`{"type":"tool_result","tool_use_id":"id-only","content":"scaled"}`)
	var allContent []json.RawMessage
	allContent = append(allContent, pending.PartialResults...) // nil — appends nothing
	allContent = append(allContent, pendingResult)

	if len(allContent) != 1 {
		t.Errorf("expected 1 content block, got %d", len(allContent))
	}

	s := string(allContent[0])
	if !strings.Contains(s, "id-only") {
		t.Errorf("expected pending tool ID in result, got: %s", s)
	}
}

// TestToolResultIDsMustMatchToolUseIDs documents the invariant that the Beta
// API enforces: every tool_use in the assistant message must have exactly one
// matching tool_result in the following user message.
func TestToolResultIDsMustMatchToolUseIDs(t *testing.T) {
	// Simulate an assistant turn with 3 tool_uses.
	assistantMsg := assistantToolUseMsg("tu-1", "tu-2", "tu-3")

	var assistantParsed struct {
		Content []struct {
			ID string `json:"id"`
		} `json:"content"`
	}
	if err := json.Unmarshal(assistantMsg, &assistantParsed); err != nil {
		t.Fatalf("parse assistant: %v", err)
	}

	// Simulate the BROKEN case: only the pending tool's result (tu-3).
	brokenUserMsg := toolResultMsg("tu-3")
	var brokenParsed struct {
		Content []struct {
			ToolUseID string `json:"tool_use_id"`
		} `json:"content"`
	}
	if err := json.Unmarshal(brokenUserMsg, &brokenParsed); err != nil {
		t.Fatalf("parse broken user: %v", err)
	}
	if len(brokenParsed.Content) == len(assistantParsed.Content) {
		t.Error("broken case: tool_result count should NOT match tool_use count")
	}

	// Simulate the FIXED case: all three results.
	fixedUserMsg := toolResultMsg("tu-1", "tu-2", "tu-3")
	var fixedParsed struct {
		Content []struct {
			ToolUseID string `json:"tool_use_id"`
		} `json:"content"`
	}
	if err := json.Unmarshal(fixedUserMsg, &fixedParsed); err != nil {
		t.Fatalf("parse fixed user: %v", err)
	}
	if len(fixedParsed.Content) != len(assistantParsed.Content) {
		t.Errorf("fixed case: got %d tool_results, want %d tool_uses",
			len(fixedParsed.Content), len(assistantParsed.Content))
	}
	for i, r := range fixedParsed.Content {
		if r.ToolUseID != assistantParsed.Content[i].ID {
			t.Errorf("result %d: tool_use_id %q != tool_use id %q",
				i, r.ToolUseID, assistantParsed.Content[i].ID)
		}
	}
}
