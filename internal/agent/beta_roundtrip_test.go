package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
)

// safeSplitPoint replicates the boundary-finding logic from SummarizeHistory
// so it can be unit-tested without a real API client.
func safeSplitPoint(messages []json.RawMessage, keepRecent int) int {
	recentStart := len(messages) - keepRecent
	for recentStart > 0 {
		var m struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
			} `json:"content"`
		}
		if err := json.Unmarshal(messages[recentStart], &m); err != nil {
			break
		}
		if m.Role != "user" {
			recentStart--
			continue
		}
		hasToolResult := false
		for _, c := range m.Content {
			if c.Type == "tool_result" {
				hasToolResult = true
				break
			}
		}
		if hasToolResult {
			recentStart--
			continue
		}
		break
	}
	return recentStart
}

// TestSummarizeHistorySafeSplitPoint verifies that the boundary-finding algorithm
// never splits an assistant(tool_use) / user(tool_results) pair across the
// condensed/recent boundary — the root cause of the beta API 400 error.
func TestSummarizeHistorySafeSplitPoint(t *testing.T) {
	// Helper to build a plain-text user message.
	plainUser := func(text string) json.RawMessage {
		b, _ := json.Marshal(map[string]interface{}{
			"role":    "user",
			"content": []map[string]string{{"type": "text", "text": text}},
		})
		return b
	}
	// Helper to build an assistant message with text.
	plainAssistant := func(text string) json.RawMessage {
		b, _ := json.Marshal(map[string]interface{}{
			"role":    "assistant",
			"content": []map[string]string{{"type": "text", "text": text}},
		})
		return b
	}
	// Helper to build an assistant message with tool_use blocks.
	toolUseAssistant := func(ids []string) json.RawMessage {
		var content []map[string]string
		for _, id := range ids {
			content = append(content, map[string]string{"type": "tool_use", "id": id, "name": "list_files"})
		}
		b, _ := json.Marshal(map[string]interface{}{"role": "assistant", "content": content})
		return b
	}
	// Helper to build a user message with tool_result blocks.
	toolResultUser := func(ids []string) json.RawMessage {
		var content []map[string]string
		for _, id := range ids {
			content = append(content, map[string]string{"type": "tool_result", "tool_use_id": id})
		}
		b, _ := json.Marshal(map[string]interface{}{"role": "user", "content": content})
		return b
	}

	t.Run("naive boundary falls on tool_result — walks back to safe point", func(t *testing.T) {
		// 12 messages: 10 plain exchanges + 1 tool_use pair at positions 10,11.
		// keepRecent=10 naively starts at index 2, but index 2 is a tool_result
		// (preceded by tool_use at index 1 in older). We want the split to step
		// back far enough that no tool_result is the first message in recent.
		var msgs []json.RawMessage
		// positions 0-9: 5 plain user/assistant pairs
		for i := 0; i < 5; i++ {
			msgs = append(msgs, plainUser("msg"))
			msgs = append(msgs, plainAssistant("reply"))
		}
		// positions 10-11: tool_use pair (would be split by naive keepRecent=10)
		msgs = append(msgs, toolUseAssistant([]string{"toolu_aaa"}))
		msgs = append(msgs, toolResultUser([]string{"toolu_aaa"}))

		split := safeSplitPoint(msgs, 10)
		if split == 0 {
			t.Fatal("split==0 means nothing to condense; expected it to land on a safe boundary")
		}
		// The message at recentStart must NOT be a tool_result user message.
		var m struct {
			Role    string `json:"role"`
			Content []struct{ Type string `json:"type"` } `json:"content"`
		}
		if err := json.Unmarshal(msgs[split], &m); err != nil {
			t.Fatalf("unmarshal split message: %v", err)
		}
		for _, c := range m.Content {
			if c.Type == "tool_result" {
				t.Errorf("split point %d starts with a tool_result block — tool_use/tool_result pair would be orphaned", split)
			}
		}
	})

	t.Run("naive boundary is already clean — no adjustment needed", func(t *testing.T) {
		var msgs []json.RawMessage
		for i := 0; i < 7; i++ {
			msgs = append(msgs, plainUser("msg"))
			msgs = append(msgs, plainAssistant("reply"))
		}
		// 14 messages; keepRecent=10 → recentStart=4, which is a plain user msg.
		split := safeSplitPoint(msgs, 10)
		if split != 4 {
			t.Errorf("expected split=4 (no adjustment needed), got %d", split)
		}
	})
}

// TestBetaRoundTripPreservesToolUseIDs checks that betaToRaw → rawToBeta correctly
// preserves tool_use blocks in an assistant message so they match the subsequent
// tool_result blocks in the user message when sent to the API.
func TestBetaRoundTripPreservesToolUseIDs(t *testing.T) {
	toolUseID := "toolu_016Me63RUT9zVvMnSh9dqHYp"

	// Simulate betaResponseToParam building an assistant message with one tool_use.
	assistantMsg := anthropic.BetaMessageParam{
		Role: anthropic.BetaMessageParamRoleAssistant,
		Content: []anthropic.BetaContentBlockParamUnion{
			anthropic.NewBetaToolUseBlock(toolUseID, json.RawMessage(`{"dir":"/repos/deployments"}`), "list_files"),
		},
	}

	// Round-trip: betaToRaw → rawToBeta (what happens when session history is serialized).
	betaMsgs := []anthropic.BetaMessageParam{assistantMsg}
	raw := betaToRaw(betaMsgs)

	if len(raw) != 1 {
		t.Fatalf("betaToRaw: expected 1 raw message, got %d", len(raw))
	}
	rawJSON := string(raw[0])
	if !strings.Contains(rawJSON, toolUseID) {
		t.Errorf("betaToRaw: tool_use ID %q missing from raw JSON: %s", toolUseID, rawJSON)
	}
	t.Logf("betaToRaw produced: %s", rawJSON)

	betaMsgs2 := rawToBeta(raw)
	if len(betaMsgs2) != 1 {
		t.Fatalf("rawToBeta: expected 1 message, got %d", len(betaMsgs2))
	}
	if len(betaMsgs2[0].Content) != 1 {
		t.Fatalf("rawToBeta: expected 1 content block, got %d; JSON was: %s", len(betaMsgs2[0].Content), rawJSON)
	}
	block := betaMsgs2[0].Content[0]
	if block.OfToolUse == nil {
		t.Fatalf("rawToBeta: OfToolUse is nil after round-trip; OfText=%v; full block JSON: %s", block.OfText, rawJSON)
	}
	if block.OfToolUse.ID != toolUseID {
		t.Errorf("rawToBeta: ID mismatch: got %q, want %q", block.OfToolUse.ID, toolUseID)
	}
}

// TestBetaResponseToParamPreservesToolUseIDs verifies that betaResponseToParam
// (which converts a BetaMessage response to a BetaMessageParam for history)
// correctly preserves tool_use IDs so subsequent rawToBeta calls see the right IDs.
func TestBetaResponseToParamPreservesToolUseIDs(t *testing.T) {
	// Simulate a real API response via the BetaMessage union type. Because we
	// can't make a real API call, we build the BetaMessage manually by
	// unmarshaling the JSON that the API would return.
	respJSON := `{
		"id": "msg_abc",
		"type": "message",
		"role": "assistant",
		"stop_reason": "tool_use",
		"model": "claude-sonnet-4-6",
		"usage": {"input_tokens": 100, "output_tokens": 50},
		"content": [
			{"type": "tool_use", "id": "toolu_016Me63RUT9zVvMnSh9dqHYp", "name": "list_files", "input": {"dir": "/repos/deployments"}},
			{"type": "tool_use", "id": "toolu_bbb", "name": "read_file", "input": {"path": "/repos/deployments/release.yaml"}},
			{"type": "tool_use", "id": "toolu_ccc", "name": "read_file", "input": {"path": "/repos/deployments/values.yaml"}},
			{"type": "tool_use", "id": "toolu_ddd", "name": "list_files", "input": {"dir": "/repos/aiops-agent"}},
			{"type": "tool_use", "id": "toolu_eee", "name": "read_file", "input": {"path": "/repos/aiops-agent/main.go"}},
			{"type": "tool_use", "id": "toolu_fff", "name": "git_log", "input": {"repo_dir": "/repos/aiops-agent", "limit": 10}}
		]
	}`

	var resp anthropic.BetaMessage
	if err := json.Unmarshal([]byte(respJSON), &resp); err != nil {
		t.Fatalf("unmarshal BetaMessage: %v", err)
	}

	param := betaResponseToParam(&resp)

	if len(param.Content) != 6 {
		t.Fatalf("betaResponseToParam: expected 6 content blocks, got %d", len(param.Content))
	}

	expectedIDs := []string{
		"toolu_016Me63RUT9zVvMnSh9dqHYp",
		"toolu_bbb", "toolu_ccc", "toolu_ddd", "toolu_eee", "toolu_fff",
	}
	for i, block := range param.Content {
		if block.OfToolUse == nil {
			t.Errorf("block[%d]: OfToolUse is nil", i)
			continue
		}
		if block.OfToolUse.ID != expectedIDs[i] {
			t.Errorf("block[%d]: ID=%q, want %q", i, block.OfToolUse.ID, expectedIDs[i])
		}
	}

	// Now simulate the full session: betaToRaw → rawToBeta.
	raw := betaToRaw([]anthropic.BetaMessageParam{param})
	restored := rawToBeta(raw)
	if len(restored[0].Content) != 6 {
		t.Fatalf("after round-trip: expected 6 blocks, got %d; raw: %s", len(restored[0].Content), string(raw[0]))
	}
	for i, block := range restored[0].Content {
		if block.OfToolUse == nil {
			t.Errorf("round-trip block[%d]: OfToolUse is nil; raw: %s", i, string(raw[0]))
			continue
		}
		if block.OfToolUse.ID != expectedIDs[i] {
			t.Errorf("round-trip block[%d]: ID=%q, want %q", i, block.OfToolUse.ID, expectedIDs[i])
		}
	}
}

// TestBetaRoundTripMultipleTools checks the case from the chat log: 6 readonly tools
// in one assistant response, followed by tool_results in a user message.
func TestBetaRoundTripMultipleTools(t *testing.T) {
	ids := []string{
		"toolu_016Me63RUT9zVvMnSh9dqHYp",
		"toolu_bbb",
		"toolu_ccc",
		"toolu_ddd",
		"toolu_eee",
		"toolu_fff",
	}
	names := []string{"list_files", "read_file", "read_file", "list_files", "read_file", "git_log"}

	// Build assistant message with 6 tool_use blocks.
	var blocks []anthropic.BetaContentBlockParamUnion
	for i, id := range ids {
		blocks = append(blocks, anthropic.NewBetaToolUseBlock(id, json.RawMessage(`{}`), names[i]))
	}
	assistantMsg := anthropic.BetaMessageParam{
		Role:    anthropic.BetaMessageParamRoleAssistant,
		Content: blocks,
	}

	// Build user message with 6 tool_results.
	var resultBlocks []anthropic.BetaContentBlockParamUnion
	for _, id := range ids {
		resultBlocks = append(resultBlocks, anthropic.NewBetaToolResultBlock(id, "result", false))
	}
	userMsg := anthropic.NewBetaUserMessage(resultBlocks...)

	// Simulate the betaMsgs after first API call + tool execution.
	betaMsgs := []anthropic.BetaMessageParam{assistantMsg, userMsg}
	raw := betaToRaw(betaMsgs)

	// Now simulate next turn: rawToBeta on the stored session history.
	restored := rawToBeta(raw)

	if len(restored) != 2 {
		t.Fatalf("expected 2 messages after round-trip, got %d", len(restored))
	}

	// Check assistant message has all 6 tool_use blocks.
	if len(restored[0].Content) != 6 {
		t.Fatalf("assistant message: expected 6 content blocks, got %d; raw: %s", len(restored[0].Content), string(raw[0]))
	}
	for i, block := range restored[0].Content {
		if block.OfToolUse == nil {
			t.Errorf("assistant block[%d]: OfToolUse is nil after round-trip; raw: %s", i, string(raw[0]))
			continue
		}
		if block.OfToolUse.ID != ids[i] {
			t.Errorf("assistant block[%d]: ID mismatch: got %q, want %q", i, block.OfToolUse.ID, ids[i])
		}
	}

	// Check user message has all 6 tool_result blocks.
	if len(restored[1].Content) != 6 {
		t.Fatalf("user message: expected 6 content blocks, got %d; raw: %s", len(restored[1].Content), string(raw[1]))
	}
	for i, block := range restored[1].Content {
		if block.OfToolResult == nil {
			t.Errorf("user block[%d]: OfToolResult is nil after round-trip; raw: %s", i, string(raw[1]))
			continue
		}
		if block.OfToolResult.ToolUseID != ids[i] {
			t.Errorf("user block[%d]: ToolUseID mismatch: got %q, want %q", i, block.OfToolResult.ToolUseID, ids[i])
		}
	}
}

// TestFullSessionRoundTripViaBetaResponseToParam simulates the exact sequence from
// the bug report: betaResponseToParam (tool_use response) + buildBetaToolResultBlocks
// stored in session, then rawToBeta in next turn.
func TestFullSessionRoundTripViaBetaResponseToParam(t *testing.T) {
	// Simulate the actual response the API would return with 6 tool_use blocks.
	respJSON := `{
		"id": "msg_abc",
		"type": "message",
		"role": "assistant",
		"stop_reason": "tool_use",
		"model": "claude-sonnet-4-6",
		"usage": {"input_tokens": 100, "output_tokens": 50},
		"content": [
			{"type": "tool_use", "id": "toolu_016Me63RUT9zVvMnSh9dqHYp", "name": "list_files", "input": {"dir": "/repos/deployments", "max_depth": 2}},
			{"type": "tool_use", "id": "toolu_bbb", "name": "read_file", "input": {"path": "/repos/deployments/infra/aiops-agent/release.yaml"}},
			{"type": "tool_use", "id": "toolu_ccc", "name": "read_file", "input": {"path": "/repos/deployments/infra/aiops-agent/values.yaml"}},
			{"type": "tool_use", "id": "toolu_ddd", "name": "list_files", "input": {"dir": "/repos/aiops-agent"}},
			{"type": "tool_use", "id": "toolu_eee", "name": "read_file", "input": {"path": "/repos/aiops-agent/go.mod"}},
			{"type": "tool_use", "id": "toolu_fff", "name": "git_log", "input": {"repo_dir": "/repos/aiops-agent", "limit": 5}}
		]
	}`
	var resp anthropic.BetaMessage
	if err := json.Unmarshal([]byte(respJSON), &resp); err != nil {
		t.Fatalf("unmarshal BetaMessage: %v", err)
	}

	// Build assistant message via betaResponseToParam (exact production code path).
	assistantParam := betaResponseToParam(&resp)

	// Build tool_result user message via buildBetaToolResultBlocks (exact production code path).
	ids := []string{"toolu_016Me63RUT9zVvMnSh9dqHYp", "toolu_bbb", "toolu_ccc", "toolu_ddd", "toolu_eee", "toolu_fff"}
	var toolResults []anthropic.BetaContentBlockParamUnion
	for _, block := range resp.Content {
		if block.Type != "tool_use" {
			continue
		}
		tu := block.AsToolUse()
		results := buildBetaToolResultBlocks(tu.ID, toolOutput{Text: "mock output"}, nil, 8192)
		toolResults = append(toolResults, results...)
	}
	userParam := anthropic.NewBetaUserMessage(toolResults...)

	// This is what betaMsgs looks like after the first turn (simplified: no prior user msg).
	betaMsgs := []anthropic.BetaMessageParam{assistantParam, userParam}

	// betaToRaw → stored in sess.messages.
	raw := betaToRaw(betaMsgs)
	t.Logf("stored assistant raw: %s", string(raw[0]))
	t.Logf("stored user raw: %s", string(raw[1]))

	// Next turn: rawToBeta on the stored session history.
	restored := rawToBeta(raw)
	if len(restored) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(restored))
	}

	// Verify all 6 tool_use IDs are preserved in the assistant message.
	if len(restored[0].Content) != 6 {
		t.Fatalf("assistant: expected 6 content blocks, got %d; raw: %s", len(restored[0].Content), string(raw[0]))
	}
	for i, block := range restored[0].Content {
		if block.OfToolUse == nil {
			t.Errorf("assistant block[%d]: OfToolUse is nil; raw: %s", i, string(raw[0]))
			continue
		}
		if block.OfToolUse.ID != ids[i] {
			t.Errorf("assistant block[%d]: ID=%q, want %q", i, block.OfToolUse.ID, ids[i])
		}
	}

	// Verify all 6 tool_use_ids are preserved in the user message.
	if len(restored[1].Content) != 6 {
		t.Fatalf("user: expected 6 content blocks, got %d; raw: %s", len(restored[1].Content), string(raw[1]))
	}
	for i, block := range restored[1].Content {
		if block.OfToolResult == nil {
			t.Errorf("user block[%d]: OfToolResult is nil; raw: %s", i, string(raw[1]))
			continue
		}
		if block.OfToolResult.ToolUseID != ids[i] {
			t.Errorf("user block[%d]: ToolUseID=%q, want %q", i, block.OfToolResult.ToolUseID, ids[i])
		}
	}
}
