package agent

import (
	"testing"

	"github.com/jimytar/aiops-agent/internal/config"
)

func TestBuildMCPServersEmpty(t *testing.T) {
	out := buildMCPServers(nil)
	if len(out) != 0 {
		t.Errorf("expected 0 servers, got %d", len(out))
	}
}

func TestBuildMCPServersBasic(t *testing.T) {
	cfgs := []config.MCPServerConfig{
		{Name: "home-assistant", URL: "https://ha.example.com/mcp"},
	}
	out := buildMCPServers(cfgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 server, got %d", len(out))
	}
	if out[0].Name != "home-assistant" {
		t.Errorf("Name = %q", out[0].Name)
	}
	if out[0].URL != "https://ha.example.com/mcp" {
		t.Errorf("URL = %q", out[0].URL)
	}
}

func TestBuildMCPServersAllowedTools(t *testing.T) {
	cfgs := []config.MCPServerConfig{
		{
			Name:         "ha",
			URL:          "https://ha.example.com/mcp",
			AllowedTools: []string{"light.turn_on", "light.turn_off"},
		},
	}
	out := buildMCPServers(cfgs)
	if len(out) != 1 {
		t.Fatalf("expected 1 server, got %d", len(out))
	}
	tc := out[0].ToolConfiguration
	if len(tc.AllowedTools) != 2 {
		t.Errorf("AllowedTools = %v, want 2 entries", tc.AllowedTools)
	}
}

func TestBuildMCPServersDeniedToolsFilterAllowed(t *testing.T) {
	cfgs := []config.MCPServerConfig{
		{
			Name:         "ha",
			URL:          "https://ha.example.com/mcp",
			AllowedTools: []string{"light.turn_on", "light.turn_off", "alarm.arm"},
			DeniedTools:  []string{"alarm.arm"},
		},
	}
	out := buildMCPServers(cfgs)
	tc := out[0].ToolConfiguration
	if len(tc.AllowedTools) != 2 {
		t.Errorf("AllowedTools after deny = %v, want 2 entries", tc.AllowedTools)
	}
	for _, name := range tc.AllowedTools {
		if name == "alarm.arm" {
			t.Errorf("denied tool 'alarm.arm' still present in AllowedTools")
		}
	}
}

func TestBuildMCPServersDeniedOnlyNoEffect(t *testing.T) {
	// DeniedTools without AllowedTools has no effect (all tools remain available).
	cfgs := []config.MCPServerConfig{
		{
			Name:        "ha",
			URL:         "https://ha.example.com/mcp",
			DeniedTools: []string{"alarm.arm"},
		},
	}
	out := buildMCPServers(cfgs)
	// No AllowedTools → ToolConfiguration is empty (server exposes everything).
	if len(out[0].ToolConfiguration.AllowedTools) != 0 {
		t.Errorf("expected empty AllowedTools when no AllowedTools configured, got %v", out[0].ToolConfiguration.AllowedTools)
	}
}

func TestBuildMCPServersMultiple(t *testing.T) {
	cfgs := []config.MCPServerConfig{
		{Name: "ha", URL: "https://ha.example.com/mcp"},
		{Name: "grafana", URL: "https://grafana.example.com/mcp"},
	}
	out := buildMCPServers(cfgs)
	if len(out) != 2 {
		t.Fatalf("expected 2 servers, got %d", len(out))
	}
	if out[0].Name != "ha" || out[1].Name != "grafana" {
		t.Errorf("server names = %q %q", out[0].Name, out[1].Name)
	}
}

// TestBetaToolsMCPToolsetEntries verifies that buildTools + convertToolsToBeta
// does NOT add mcp_toolset entries (those are added in New() from mcpServers).
// This guards against accidentally double-adding them.
func TestBetaToolsMCPToolsetEntries(t *testing.T) {
	tools := buildTools([]string{"test-cluster"}, config.ToolsConfig{}, "")
	beta := convertToolsToBeta(tools)
	for i, bt := range beta {
		if bt.OfMCPToolset != nil {
			t.Errorf("beta tool[%d] unexpectedly has OfMCPToolset set; mcp_toolset entries should only come from mcpServers", i)
		}
	}
}
