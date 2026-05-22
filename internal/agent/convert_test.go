package agent

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/jimytar/aiops-agent/internal/config"
)

func TestConvertToolsToBetaPreservesAll(t *testing.T) {
	tools := buildTools([]string{"bastion"}, config.ToolsConfig{}, "https://nvr-int.jimytar.com")
	beta := convertToolsToBeta(tools)

	if len(beta) != len(tools) {
		t.Errorf("convertToolsToBeta: got %d beta tools, want %d", len(beta), len(tools))
	}

	frigateNames := map[string]bool{"frigate_cameras": false, "frigate_snapshot": false, "frigate_events": false}
	for _, bt := range beta {
		if bt.OfTool != nil {
			if _, ok := frigateNames[bt.OfTool.Name]; ok {
				frigateNames[bt.OfTool.Name] = true
			}
		}
	}
	for name, found := range frigateNames {
		if !found {
			t.Errorf("convertToolsToBeta: frigate tool %q lost in conversion", name)
		}
	}
}

func TestConvertToolsToBetaRoundTrip(t *testing.T) {
	tools := buildTools([]string{"bastion"}, config.ToolsConfig{}, "https://nvr-int.jimytar.com")

	for _, tool := range tools {
		if tool.OfTool == nil {
			t.Errorf("standard tool has nil OfTool: %+v", tool)
		}
	}

	data, err := json.Marshal(tools)
	if err != nil {
		t.Fatalf("marshal tools: %v", err)
	}

	var beta []anthropic.BetaToolUnionParam
	if err := json.Unmarshal(data, &beta); err != nil {
		t.Fatalf("unmarshal beta tools: %v", err)
	}

	for i, bt := range beta {
		if bt.OfTool == nil {
			t.Errorf("beta tool[%d] has nil OfTool after round-trip (standard name: %s)", i, tools[i].OfTool.Name)
		}
	}
}

// --- system prompt Frigate section ---

func TestFrigateSystemPromptSectionAddedWhenConfigured(t *testing.T) {
	cfg := &config.Config{
		ClaudeModel:    "claude-haiku-4-5-20251001",
		SystemPrompt:   "base prompt",
		FrigateURL:     "https://nvr.example.com",
		AllowedChatIDs: []int64{1},
	}
	sysPrompt := cfg.SystemPrompt
	if cfg.FrigateURL != "" {
		sysPrompt += frigateSystemPromptSection
	}
	if !strings.Contains(sysPrompt, "frigate_cameras") {
		t.Error("system prompt should mention frigate_cameras when FrigateURL is set")
	}
	if !strings.Contains(sysPrompt, "frigate_snapshot") {
		t.Error("system prompt should mention frigate_snapshot when FrigateURL is set")
	}
	if !strings.Contains(sysPrompt, "frigate_events") {
		t.Error("system prompt should mention frigate_events when FrigateURL is set")
	}
}

func TestFrigateSystemPromptSectionAbsentWhenNotConfigured(t *testing.T) {
	cfg := &config.Config{
		ClaudeModel:    "claude-haiku-4-5-20251001",
		SystemPrompt:   "base prompt",
		FrigateURL:     "",
		AllowedChatIDs: []int64{1},
	}
	sysPrompt := cfg.SystemPrompt
	if cfg.FrigateURL != "" {
		sysPrompt += frigateSystemPromptSection
	}
	if strings.Contains(sysPrompt, "frigate_cameras") {
		t.Error("system prompt should NOT mention frigate_cameras when FrigateURL is empty")
	}
}
