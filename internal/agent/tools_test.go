package agent

import (
	"testing"

	"github.com/jimytar/aiops-agent/internal/config"
)

// --- parseTier ---

func TestParseTierValid(t *testing.T) {
	tests := []struct {
		in   string
		want toolTier
	}{
		{"readonly", tierReadonly},
		{"mutating", tierMutating},
		{"destructive", tierDestructive},
	}
	for _, tt := range tests {
		got, ok := parseTier(tt.in)
		if !ok {
			t.Errorf("parseTier(%q) ok=false", tt.in)
		}
		if got != tt.want {
			t.Errorf("parseTier(%q) = %v, want %v", tt.in, got, tt.want)
		}
	}
}

func TestParseTierInvalid(t *testing.T) {
	invalids := []string{"", "READONLY", "Mutating", "admin", "write"}
	for _, s := range invalids {
		_, ok := parseTier(s)
		if ok {
			t.Errorf("parseTier(%q) should return ok=false", s)
		}
	}
}

// --- defaultTiers ---

func TestDefaultTiersReadonly(t *testing.T) {
	readonlyTools := []string{
		"kubectl_get", "kubectl_describe", "kubectl_logs", "kubectl_get_events",
		"helm_list", "helm_status", "git_status", "git_log", "git_diff",
		"ssh_exec_readonly", "list_files", "read_file",
		"frigate_cameras", "frigate_snapshot", "frigate_events",
	}
	for _, name := range readonlyTools {
		tier, ok := defaultTiers[name]
		if !ok {
			t.Errorf("defaultTiers missing %q", name)
			continue
		}
		if tier != tierReadonly {
			t.Errorf("defaultTiers[%q] = %v, want tierReadonly", name, tier)
		}
	}
}

func TestDefaultTiersMutating(t *testing.T) {
	mutatingTools := []string{
		"kubectl_restart", "kubectl_scale", "kubectl_rollout", "kubectl_exec",
		"helm_rollback", "git_pull", "git_push", "git_commit", "write_file",
		"flux_reconcile", "ssh_exec",
	}
	for _, name := range mutatingTools {
		tier, ok := defaultTiers[name]
		if !ok {
			t.Errorf("defaultTiers missing %q", name)
			continue
		}
		if tier != tierMutating {
			t.Errorf("defaultTiers[%q] = %v, want tierMutating", name, tier)
		}
	}
}

func TestDefaultTiersDestructive(t *testing.T) {
	if tier, ok := defaultTiers["kubectl_delete"]; !ok || tier != tierDestructive {
		t.Errorf("kubectl_delete should be destructive, got %v (ok=%v)", tier, ok)
	}
}

// --- toolTierFor ---

func TestToolTierForKnown(t *testing.T) {
	effectiveTiers = defaultTiers

	if got := toolTierFor("kubectl_get"); got != tierReadonly {
		t.Errorf("toolTierFor(kubectl_get) = %v", got)
	}
	if got := toolTierFor("kubectl_delete"); got != tierDestructive {
		t.Errorf("toolTierFor(kubectl_delete) = %v", got)
	}
}

func TestToolTierForUnknown(t *testing.T) {
	effectiveTiers = defaultTiers
	if got := toolTierFor("nonexistent_tool"); got != tierReadonly {
		t.Errorf("toolTierFor(unknown) = %v, want tierReadonly", got)
	}
}

func TestToolTierForFrigate(t *testing.T) {
	effectiveTiers = defaultTiers
	for _, name := range []string{"frigate_cameras", "frigate_snapshot", "frigate_events"} {
		if got := toolTierFor(name); got != tierReadonly {
			t.Errorf("toolTierFor(%s) = %v, want tierReadonly", name, got)
		}
	}
}

// --- applyToolsConfig ---

func TestApplyToolsConfigOverride(t *testing.T) {
	applyToolsConfig(config.ToolsConfig{
		Tiers: map[string]string{
			"kubectl_restart": "readonly",
		},
	})
	if got := toolTierFor("kubectl_restart"); got != tierReadonly {
		t.Errorf("after override, kubectl_restart = %v, want tierReadonly", got)
	}
	effectiveTiers = defaultTiers
}

func TestApplyToolsConfigInvalidTierIgnored(t *testing.T) {
	applyToolsConfig(config.ToolsConfig{
		Tiers: map[string]string{
			"kubectl_restart": "superuser",
		},
	})
	if got := toolTierFor("kubectl_restart"); got != tierMutating {
		t.Errorf("invalid tier should be ignored; got %v", got)
	}
	effectiveTiers = defaultTiers
}

func TestApplyToolsConfigDoesNotMutateDefaults(t *testing.T) {
	original := defaultTiers["kubectl_get"]
	applyToolsConfig(config.ToolsConfig{
		Tiers: map[string]string{"kubectl_get": "destructive"},
	})
	if defaultTiers["kubectl_get"] != original {
		t.Error("applyToolsConfig should not mutate the defaultTiers package-level map")
	}
	effectiveTiers = defaultTiers
}

// --- filterTools ---

func TestFilterToolsNoneDisabled(t *testing.T) {
	tools := buildTools([]string{"bastion"}, config.ToolsConfig{}, "")
	filtered := filterTools(tools, nil)
	if len(filtered) != len(tools) {
		t.Errorf("no disabled tools: filtered=%d, original=%d", len(filtered), len(tools))
	}
}

func TestFilterToolsDisablesOne(t *testing.T) {
	tools := buildTools([]string{"bastion"}, config.ToolsConfig{}, "")
	filtered := filterTools(tools, []string{"kubectl_delete"})
	for _, t2 := range filtered {
		if t2.OfTool != nil && t2.OfTool.Name == "kubectl_delete" {
			t.Error("kubectl_delete should be filtered out")
		}
	}
	if len(filtered) != len(tools)-1 {
		t.Errorf("expected %d tools after disabling 1, got %d", len(tools)-1, len(filtered))
	}
}

func TestFilterToolsDisablesMultiple(t *testing.T) {
	tools := buildTools([]string{"bastion"}, config.ToolsConfig{}, "")
	disabled := []string{"kubectl_delete", "ssh_exec", "write_file"}
	filtered := filterTools(tools, disabled)
	disabledSet := map[string]bool{"kubectl_delete": true, "ssh_exec": true, "write_file": true}
	for _, t2 := range filtered {
		if t2.OfTool != nil && disabledSet[t2.OfTool.Name] {
			t.Errorf("tool %q should be filtered out", t2.OfTool.Name)
		}
	}
	if len(filtered) != len(tools)-3 {
		t.Errorf("expected %d tools, got %d", len(tools)-3, len(filtered))
	}
}

// --- buildTools ---

func TestBuildToolsAllPresent(t *testing.T) {
	tools := buildTools([]string{"bastion"}, config.ToolsConfig{}, "")
	names := make(map[string]bool)
	for _, t2 := range tools {
		if t2.OfTool != nil {
			names[t2.OfTool.Name] = true
		}
	}
	expected := []string{
		"kubectl_get", "kubectl_describe", "kubectl_logs", "kubectl_get_events",
		"kubectl_restart", "kubectl_scale", "kubectl_rollout", "kubectl_delete",
		"helm_list", "helm_status", "helm_rollback",
		"git_status", "git_log", "git_pull", "git_push", "git_diff", "git_commit",
		"ssh_exec_readonly", "ssh_exec",
		"flux_reconcile", "kubectl_exec",
		"list_files", "read_file", "write_file",
	}
	for _, name := range expected {
		if !names[name] {
			t.Errorf("buildTools missing tool %q", name)
		}
	}
}

func TestBuildToolsDefaultCluster(t *testing.T) {
	tools := buildTools(nil, config.ToolsConfig{}, "")
	if len(tools) == 0 {
		t.Fatal("buildTools with nil clusters should still return tools")
	}
}

func TestBuildToolsWithDisabled(t *testing.T) {
	cfg := config.ToolsConfig{Disabled: []string{"kubectl_delete", "ssh_exec"}}
	tools := buildTools([]string{"bastion"}, cfg, "")
	for _, t2 := range tools {
		if t2.OfTool != nil {
			if t2.OfTool.Name == "kubectl_delete" || t2.OfTool.Name == "ssh_exec" {
				t.Errorf("disabled tool %q should not appear in result", t2.OfTool.Name)
			}
		}
	}
}

func TestBuildToolsCount(t *testing.T) {
	tools := buildTools([]string{"bastion"}, config.ToolsConfig{}, "")
	if len(tools) < 20 {
		t.Errorf("buildTools returned only %d tools, expected at least 20", len(tools))
	}
}

// --- Frigate tool inclusion ---

func TestBuildToolsNoFrigateURLExcludesFrigateTools(t *testing.T) {
	tools := buildTools([]string{"bastion"}, config.ToolsConfig{}, "")
	for _, t2 := range tools {
		if t2.OfTool != nil {
			switch t2.OfTool.Name {
			case "frigate_cameras", "frigate_snapshot", "frigate_events":
				t.Errorf("frigate tool %q should not appear when frigateURL is empty", t2.OfTool.Name)
			}
		}
	}
}

func TestBuildToolsWithFrigateURLIncludesFrigateTools(t *testing.T) {
	tools := buildTools([]string{"bastion"}, config.ToolsConfig{}, "https://nvr.example.com")
	names := make(map[string]bool)
	for _, t2 := range tools {
		if t2.OfTool != nil {
			names[t2.OfTool.Name] = true
		}
	}
	for _, name := range []string{"frigate_cameras", "frigate_snapshot", "frigate_events"} {
		if !names[name] {
			t.Errorf("expected frigate tool %q when frigateURL is set", name)
		}
	}
}

func TestBuildToolsFrigateCountDifference(t *testing.T) {
	without := buildTools([]string{"bastion"}, config.ToolsConfig{}, "")
	with := buildTools([]string{"bastion"}, config.ToolsConfig{}, "https://nvr.example.com")
	if len(with) != len(without)+3 {
		t.Errorf("expected 3 extra tools with Frigate, got %d extra", len(with)-len(without))
	}
}

func TestBuildToolsFrigateCanBeDisabled(t *testing.T) {
	cfg := config.ToolsConfig{Disabled: []string{"frigate_snapshot"}}
	tools := buildTools([]string{"bastion"}, cfg, "https://nvr.example.com")
	for _, t2 := range tools {
		if t2.OfTool != nil && t2.OfTool.Name == "frigate_snapshot" {
			t.Error("frigate_snapshot should be disabled")
		}
	}
}

// --- TierFor (exported) ---

func TestTierForExported(t *testing.T) {
	effectiveTiers = defaultTiers
	if got := TierFor("kubectl_delete"); got != tierDestructive {
		t.Errorf("TierFor(kubectl_delete) = %v", got)
	}
	if got := TierFor("kubectl_get"); got != tierReadonly {
		t.Errorf("TierFor(kubectl_get) = %v", got)
	}
}
