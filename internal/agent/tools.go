package agent

import (
	"log"
	"net/http"
	"strings"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/jimytar/aiops-agent/internal/config"
)

type toolTier int

const (
	tierReadonly    toolTier = iota
	tierMutating             // requires nonce confirmation
	tierDestructive          // requires confirmation + name re-entry
)

// defaultTiers is the baseline; overridden at runtime via config.
var defaultTiers = map[string]toolTier{
	"kubectl_get":        tierReadonly,
	"kubectl_describe":   tierReadonly,
	"kubectl_logs":       tierReadonly,
	"kubectl_get_events": tierReadonly,
	"helm_list":          tierReadonly,
	"helm_status":        tierReadonly,
	"git_status":         tierReadonly,
	"git_log":            tierReadonly,
	"ssh_exec_readonly":  tierReadonly,
	"kubectl_restart":    tierMutating,
	"kubectl_scale":      tierMutating,
	"kubectl_rollout":    tierMutating,
	"helm_rollback":      tierMutating,
	"git_pull":           tierMutating,
	"git_push":           tierMutating,
	"git_tag":            tierMutating,
	"ssh_exec":           tierMutating,
	"kubectl_delete":     tierDestructive,
	"flux_reconcile":     tierMutating,
	"kubectl_exec":       tierMutating,
	"list_files":         tierReadonly,
	"read_file":          tierReadonly,
	"git_diff":           tierReadonly,
	"write_file":         tierMutating,
	"git_commit":         tierMutating,
	"frigate_cameras":    tierReadonly,
	"frigate_snapshot":   tierReadonly,
	"frigate_events":     tierReadonly,
}

// effectiveTiers is built at startup by applyToolsConfig and used at runtime.
var effectiveTiers = defaultTiers

func toolTierFor(name string) toolTier {
	t, ok := effectiveTiers[name]
	if !ok {
		return tierReadonly
	}
	return t
}

// applyToolsConfig merges user-supplied tier overrides and HTTP integration
// tiers into effectiveTiers. Call once during agent initialisation.
func applyToolsConfig(cfg config.ToolsConfig, httpIntegrations []config.HTTPIntegrationConfig) {
	// Copy defaults so we don't mutate the package-level map.
	merged := make(map[string]toolTier, len(defaultTiers))
	for k, v := range defaultTiers {
		merged[k] = v
	}
	// Register tiers for HTTP integration tools.
	for _, integration := range httpIntegrations {
		for _, ep := range integration.Endpoints {
			toolName := integration.Name + "_" + ep.Name
			tier := httpEndpointDefaultTier(ep.Method)
			if ep.Tier != "" {
				if t, ok := parseTier(ep.Tier); ok {
					tier = t
				}
			}
			merged[toolName] = tier
		}
	}
	// User overrides take final precedence.
	for tool, tierStr := range cfg.Tiers {
		t, ok := parseTier(tierStr)
		if !ok {
			log.Printf("tools: unknown tier %q for tool %q, ignoring", tierStr, tool)
			continue
		}
		merged[tool] = t
		log.Printf("tools: tier override %s → %s", tool, tierStr)
	}
	effectiveTiers = merged
}

// httpEndpointDefaultTier returns readonly for GET, mutating for everything else.
func httpEndpointDefaultTier(method string) toolTier {
	switch strings.ToUpper(method) {
	case "", "GET":
		return tierReadonly
	case "DELETE":
		return tierDestructive
	default:
		return tierMutating
	}
}

func parseTier(s string) (toolTier, bool) {
	switch s {
	case "readonly":
		return tierReadonly, true
	case "mutating":
		return tierMutating, true
	case "destructive":
		return tierDestructive, true
	}
	return 0, false
}

func buildHTTPTools(integrations []config.HTTPIntegrationConfig) []anthropic.ToolUnionParam {
	var tools []anthropic.ToolUnionParam
	for _, integration := range integrations {
		for _, ep := range integration.Endpoints {
			method := strings.ToUpper(ep.Method)
			if method == "" {
				method = "GET"
			}
			toolName := integration.Name + "_" + ep.Name
			props := map[string]interface{}{}
			if method == http.MethodGet || method == "DELETE" {
				props["query_params"] = map[string]interface{}{
					"type":        "object",
					"description": "Optional query parameters to append to the request URL (e.g. {\"term\": \"Breaking Bad\", \"limit\": 10})",
				}
			} else {
				bodyDesc := "JSON body for the request."
				if len(integration.Defaults) > 0 {
					bodyDesc += " Configured defaults (qualityProfileId, rootFolderPath, etc.) are merged in automatically — only supply fields you want to override."
				}
				props["body"] = map[string]interface{}{
					"type":        "object",
					"description": bodyDesc,
				}
			}
			tools = append(tools, anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
				Name:        toolName,
				Description: anthropic.String(ep.Description),
				InputSchema: anthropic.ToolInputSchemaParam{Type: "object", Properties: props},
			}})
		}
	}
	return tools
}

func buildTools(clusterNames []string, cfg config.ToolsConfig, frigateURL string, httpIntegrations []config.HTTPIntegrationConfig) []anthropic.ToolUnionParam {
	clusterEnum := clusterNames
	if len(clusterEnum) == 0 {
		clusterEnum = []string{"bastion"}
	}

	prop := func(typ, desc string) map[string]interface{} {
		return map[string]interface{}{"type": typ, "description": desc}
	}
	enumProp := func(desc string, values []string) map[string]interface{} {
		return map[string]interface{}{"type": "string", "description": desc, "enum": values}
	}

	clusterProp := enumProp("Target cluster name", clusterEnum)

	tool := func(name, desc string, schema anthropic.ToolInputSchemaParam) anthropic.ToolUnionParam {
		return anthropic.ToolUnionParam{OfTool: &anthropic.ToolParam{
			Name:        name,
			Description: anthropic.String(desc),
			InputSchema: schema,
		}}
	}

	all := []anthropic.ToolUnionParam{
		tool("kubectl_get", "List or get Kubernetes resources (pods, deployments, services, nodes, namespaces, statefulsets).",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"resource":  prop("string", "Resource type: pods, deployments, services, nodes, namespaces, statefulsets"),
					"name":      prop("string", "Optional: specific resource name to filter by"),
					"namespace": prop("string", "Namespace (omit for all namespaces)"),
					"cluster":   clusterProp,
				},
				Required: []string{"resource", "cluster"},
			}),
		tool("kubectl_describe", "Describe a specific Kubernetes resource in detail.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"resource":  prop("string", "Resource type: pods, deployments"),
					"name":      prop("string", "Resource name (required)"),
					"namespace": prop("string", "Namespace"),
					"cluster":   clusterProp,
				},
				Required: []string{"resource", "name", "namespace", "cluster"},
			}),
		tool("kubectl_logs", "Get logs from a pod.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"pod":        prop("string", "Pod name"),
					"namespace":  prop("string", "Namespace"),
					"cluster":    clusterProp,
					"container":  prop("string", "Container name (optional, for multi-container pods)"),
					"tail_lines": map[string]interface{}{"type": "integer", "description": "Number of lines from the end (default 100)"},
				},
				Required: []string{"pod", "namespace", "cluster"},
			}),
		tool("kubectl_get_events", "Get Kubernetes events for a namespace.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"namespace": prop("string", "Namespace (omit for all)"),
					"cluster":   clusterProp,
				},
				Required: []string{"cluster"},
			}),
		tool("kubectl_restart", "Trigger a rolling restart of a deployment. REQUIRES USER CONFIRMATION.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"deployment": prop("string", "Deployment name"),
					"namespace":  prop("string", "Namespace"),
					"cluster":    clusterProp,
				},
				Required: []string{"deployment", "namespace", "cluster"},
			}),
		tool("kubectl_scale", "Scale a deployment to a given number of replicas. REQUIRES USER CONFIRMATION.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"deployment": prop("string", "Deployment name"),
					"namespace":  prop("string", "Namespace"),
					"cluster":    clusterProp,
					"replicas":   map[string]interface{}{"type": "integer", "description": "Desired replica count"},
				},
				Required: []string{"deployment", "namespace", "cluster", "replicas"},
			}),
		tool("kubectl_rollout", "Manage a deployment rollout (undo, pause, or resume). REQUIRES USER CONFIRMATION.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"deployment": prop("string", "Deployment name"),
					"namespace":  prop("string", "Namespace"),
					"cluster":    clusterProp,
					"action":     enumProp("Rollout action", []string{"undo", "pause", "resume", "status"}),
				},
				Required: []string{"deployment", "namespace", "cluster", "action"},
			}),
		tool("kubectl_delete", "Delete a Kubernetes resource. DESTRUCTIVE — REQUIRES CONFIRMATION AND NAME RE-ENTRY.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"resource":  prop("string", "Resource type: pods, deployments"),
					"name":      prop("string", "Resource name"),
					"namespace": prop("string", "Namespace"),
					"cluster":   clusterProp,
				},
				Required: []string{"resource", "name", "namespace", "cluster"},
			}),
		tool("helm_list", "List Helm releases in a cluster or namespace.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"namespace": prop("string", "Namespace (omit for all)"),
					"cluster":   clusterProp,
				},
				Required: []string{"cluster"},
			}),
		tool("helm_status", "Get the status of a Helm release.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"release":   prop("string", "Helm release name"),
					"namespace": prop("string", "Namespace"),
					"cluster":   clusterProp,
				},
				Required: []string{"release", "cluster"},
			}),
		tool("helm_rollback", "Roll back a Helm release to a previous revision. REQUIRES USER CONFIRMATION.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"release":   prop("string", "Helm release name"),
					"namespace": prop("string", "Namespace"),
					"cluster":   clusterProp,
					"revision":  map[string]interface{}{"type": "integer", "description": "Target revision (0 = previous)"},
				},
				Required: []string{"release", "cluster"},
			}),
		tool("git_status", "Show git status of configured repositories.",
			anthropic.ToolInputSchemaParam{
				Type:       "object",
				Properties: map[string]interface{}{},
			}),
		tool("git_log", "Show git log of a repository.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"repo_dir": prop("string", "Repository directory path (uses first configured repo if omitted)"),
					"limit":    map[string]interface{}{"type": "integer", "description": "Number of commits to show (default 10)"},
				},
			}),
		tool("git_pull", "Pull latest changes for a git repository. REQUIRES USER CONFIRMATION.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"repo_dir": prop("string", "Repository directory (uses first configured repo if omitted)"),
				},
			}),
		tool("git_push", "Push local commits to the remote repository (origin HEAD). REQUIRES USER CONFIRMATION.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"repo_dir": prop("string", "Repository directory (uses first configured repo if omitted)"),
				},
			}),
		tool("ssh_exec_readonly", "Run a read-only command on a homelab node via SSH. Commands are allowlisted (systemctl status, journalctl, df, free, uptime, etc.).",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"host":    prop("string", "SSH host alias (from configured hosts)"),
					"command": prop("string", "Command to run (must match allowlist)"),
				},
				Required: []string{"host", "command"},
			}),
		tool("ssh_exec", "Run a mutating command on a homelab node via SSH (systemctl restart/stop/start). REQUIRES USER CONFIRMATION.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"host":    prop("string", "SSH host alias (from configured hosts)"),
					"command": prop("string", "Command to run (must match allowlist)"),
				},
				Required: []string{"host", "command"},
			}),
		tool("flux_reconcile", "Trigger an immediate Flux reconciliation for a Kustomization or HelmRelease. REQUIRES USER CONFIRMATION.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"kind":      enumProp("Resource kind", []string{"kustomization", "helmrelease"}),
					"name":      prop("string", "Name of the Kustomization or HelmRelease"),
					"namespace": prop("string", "Namespace (defaults to flux-system)"),
					"cluster":   clusterProp,
				},
				Required: []string{"kind", "name", "cluster"},
			}),
		tool("kubectl_exec", "Run a command inside a pod (read-only diagnostics only — allowlisted commands). REQUIRES USER CONFIRMATION.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"pod":       prop("string", "Pod name"),
					"namespace": prop("string", "Namespace"),
					"cluster":   clusterProp,
					"container": prop("string", "Container name (optional, for multi-container pods)"),
					"command":   prop("string", "Command to run (must match exec allowlist)"),
				},
				Required: []string{"pod", "namespace", "cluster", "command"},
			}),
		tool("list_files", "List files in a git repository directory (depth-limited, excludes .git).",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"dir":       prop("string", "Directory path to list (must be within a configured git repo)"),
					"max_depth": map[string]interface{}{"type": "integer", "description": "Maximum directory depth (default 3)"},
				},
				Required: []string{"dir"},
			}),
		tool("read_file", "Read the contents of a file within a configured git repository.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"path": prop("string", "Absolute path to the file (must be within a configured git repo dir)"),
				},
				Required: []string{"path"},
			}),
		tool("write_file", "Write or overwrite a file within a configured git repository. REQUIRES USER CONFIRMATION.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"path":    prop("string", "Absolute path to the file (must be within a configured git repo dir)"),
					"content": prop("string", "Full file content to write"),
				},
				Required: []string{"path", "content"},
			}),
		tool("git_diff", "Show git diff of working tree vs HEAD for a repository.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"repo_dir": prop("string", "Repository directory (uses first configured repo if omitted)"),
				},
			}),
		tool("git_commit", "Stage all changes (git add -A) and commit them. REQUIRES USER CONFIRMATION.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"repo_dir": prop("string", "Repository directory (uses first configured repo if omitted)"),
					"message":  prop("string", "Commit message"),
				},
				Required: []string{"message"},
			}),
		tool("git_tag", "Create an annotated git tag and push it to origin. Used to trigger CI release pipelines. REQUIRES USER CONFIRMATION.",
			anthropic.ToolInputSchemaParam{
				Type: "object",
				Properties: map[string]interface{}{
					"repo_dir": prop("string", "Repository directory (uses first configured repo if omitted)"),
					"tag":      prop("string", "Tag name, e.g. v0.3.0"),
					"message":  prop("string", "Annotated tag message (defaults to tag name if omitted)"),
				},
				Required: []string{"tag"},
			}),
	}

	if frigateURL != "" {
		all = append(all,
			tool("frigate_cameras", "List all camera names configured in Frigate NVR.",
				anthropic.ToolInputSchemaParam{
					Type:       "object",
					Properties: map[string]interface{}{},
				}),
			tool("frigate_snapshot", "Fetch the latest camera snapshot from Frigate NVR and visually analyze it.",
				anthropic.ToolInputSchemaParam{
					Type: "object",
					Properties: map[string]interface{}{
						"camera": prop("string", "Camera name (use frigate_cameras to list available cameras)"),
					},
					Required: []string{"camera"},
				}),
			tool("frigate_events", "Query recent detection events from Frigate NVR.",
				anthropic.ToolInputSchemaParam{
					Type: "object",
					Properties: map[string]interface{}{
						"camera": prop("string", "Filter by camera name (optional)"),
						"label":  prop("string", "Filter by object label, e.g. person, car, dog, cat (optional)"),
						"limit":  prop("integer", "Maximum number of events to return (default 10)"),
					},
				}),
		)
	}

	// Append dynamically-configured HTTP integration tools.
	all = append(all, buildHTTPTools(httpIntegrations)...)

	return filterTools(all, cfg.Disabled)
}

// filterTools removes any tool whose name appears in the disabled list.
func filterTools(tools []anthropic.ToolUnionParam, disabled []string) []anthropic.ToolUnionParam {
	if len(disabled) == 0 {
		return tools
	}
	disabledSet := make(map[string]struct{}, len(disabled))
	for _, name := range disabled {
		disabledSet[name] = struct{}{}
		log.Printf("tools: disabled %s", name)
	}
	out := tools[:0:0] // same backing array, zero length
	for _, t := range tools {
		name := ""
		if t.OfTool != nil {
			name = t.OfTool.Name
		}
		if _, skip := disabledSet[name]; !skip {
			out = append(out, t)
		}
	}
	return out
}
