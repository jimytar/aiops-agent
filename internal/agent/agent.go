package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/jimytar/aiops-agent/internal/config"
	"github.com/jimytar/aiops-agent/internal/executor"
	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TierFor exports the tool tier so the bot layer can gate confirmations.
func TierFor(name string) toolTier { return toolTierFor(name) }

// PendingTool holds a mutating tool call Claude requested but not yet executed.
type PendingTool struct {
	ID    string
	Name  string
	Input json.RawMessage
	Tier  toolTier
}

// Executors bundles all operation executors.
type Executors struct {
	Kubectl *executor.KubectlExecutor
	SSH     *executor.SSHExecutor
	Git     *executor.GitExecutor
	Helm    *executor.HelmExecutor
	Flux    *executor.FluxExecutor
	File    *executor.FileExecutor
}

// Agent runs Claude conversations with tool_use.
type Agent struct {
	client       anthropic.Client
	cfg          *config.Config
	executors    *Executors
	tools        []anthropic.ToolUnionParam
	betaTools    []anthropic.BetaToolUnionParam
	mcpServers   []anthropic.BetaRequestMCPServerURLDefinitionParam
	systemPrompt string
	limiter      *rate.Limiter
}

func New(cfg *config.Config, execs *Executors, clusterNames []string) *Agent {
	// Apply tier overrides before building the tool list.
	applyToolsConfig(cfg.Tools)

	// 1 request/second sustained, burst of 3 — stays well within Anthropic limits.
	limiter := rate.NewLimiter(rate.Every(time.Second), 3)

	a := &Agent{
		client:       anthropic.NewClient(),
		cfg:          cfg,
		executors:    execs,
		tools:        buildTools(clusterNames, cfg.Tools),
		systemPrompt: cfg.SystemPrompt,
		limiter:      limiter,
	}
	a.betaTools = convertToolsToBeta(a.tools)
	a.mcpServers = buildMCPServers(cfg.MCPServers)
	return a
}

// TurnResult is returned by RunTurn.
type TurnResult struct {
	AssistantText string
	Messages      []json.RawMessage
	PendingTool   *PendingTool
}

// RunTurn sends the conversation history to Claude, auto-executes read-only
// tools, and stops at the first mutating tool to await confirmation.
// When MCP servers are configured it uses the Beta API transparently.
func (a *Agent) RunTurn(
	ctx context.Context,
	messages []json.RawMessage,
	chatID int64,
	username string,
	statusUpdate func(string),
) (*TurnResult, error) {
	if len(a.mcpServers) > 0 {
		return a.runTurnBeta(ctx, messages, chatID, username, statusUpdate)
	}
	return a.runTurnStd(ctx, messages, chatID, username, statusUpdate)
}

// ExecuteTool runs a confirmed mutating/destructive tool, appends the result,
// then runs one more Claude turn to get the summary.
func (a *Agent) ExecuteTool(
	ctx context.Context,
	pending *PendingTool,
	messages []json.RawMessage,
	chatID int64,
	username string,
	nonce string,
	statusUpdate func(string),
) (*TurnResult, error) {
	start := time.Now()
	result, execErr := a.dispatchTool(ctx, pending.Name, pending.Input)
	duration := time.Since(start).Milliseconds()
	audit(chatID, username, pending.Name, pending.Input, true, nonce, duration, execErr)

	content := result
	if execErr != nil {
		content = fmt.Sprintf("Error: %v", execErr)
	}
	content = truncate(content, a.cfg.MaxToolOutputBytes)

	userMsg := anthropic.NewUserMessage(
		anthropic.NewToolResultBlock(pending.ID, content, execErr != nil),
	)
	raw, _ := json.Marshal(userMsg)
	msgs := append(messages, raw)
	return a.RunTurn(ctx, msgs, chatID, username, statusUpdate)
}

// ── Rate-limit retry helpers ─────────────────────────────────────────────────

const (
	maxRetries  = 4
	retryBaseMs = 2000
)

// callWithRetry acquires a global rate-limit token then calls fn,
// retrying with exponential backoff on 429 responses.
func (a *Agent) callWithRetry(ctx context.Context, fn func() error) error {
	for attempt := 0; attempt <= maxRetries; attempt++ {
		if attempt > 0 {
			delay := time.Duration(float64(retryBaseMs)*math.Pow(2, float64(attempt-1))) * time.Millisecond
			log.Printf("rate limit: retry %d/%d after %s", attempt, maxRetries, delay)
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		// Block until the global limiter allows this call.
		if err := a.limiter.Wait(ctx); err != nil {
			return err
		}
		err := fn()
		if err != nil {
			if isRateLimit(err) && attempt < maxRetries {
				continue
			}
			return err
		}
		return nil
	}
	return fmt.Errorf("rate limit: exceeded %d retries", maxRetries)
}

func isRateLimit(err error) bool {
	if err == nil {
		return false
	}
	// anthropic-sdk-go wraps HTTP errors; check for 429 in the error string.
	// The SDK error type exposes StatusCode via apierror.Error.
	type statusCoder interface{ StatusCode() int }
	if sc, ok := err.(statusCoder); ok {
		return sc.StatusCode() == http.StatusTooManyRequests
	}
	return strings.Contains(err.Error(), "429") || strings.Contains(err.Error(), "rate_limit")
}

// ── Standard API (no MCP) ────────────────────────────────────────────────────

func (a *Agent) runTurnStd(
	ctx context.Context,
	raw []json.RawMessage,
	chatID int64,
	username string,
	statusUpdate func(string),
) (*TurnResult, error) {
	msgs := rawToStd(raw)

	for {
		var resp *anthropic.Message
		if err := a.callWithRetry(ctx, func() error {
			var e error
			resp, e = a.client.Messages.New(ctx, anthropic.MessageNewParams{
				Model:     anthropic.Model(a.cfg.ClaudeModel),
				MaxTokens: 4096,
				System:    []anthropic.TextBlockParam{{Type: "text", Text: a.systemPrompt}},
				Messages:  msgs,
				Tools:     a.tools,
			})
			return e
		}); err != nil {
			return nil, fmt.Errorf("claude API: %w", err)
		}

		msgs = append(msgs, responseToParam(resp))

		if resp.StopReason == "end_turn" || !hasToolUseStd(resp.Content) {
			return &TurnResult{AssistantText: extractTextStd(resp.Content), Messages: stdToRaw(msgs)}, nil
		}

		var toolResults []anthropic.ContentBlockParamUnion
		var pendingTool *PendingTool

		for _, block := range resp.Content {
			if block.Type != "tool_use" {
				continue
			}
			tu := block.AsToolUse()
			if toolTierFor(tu.Name) != tierReadonly {
				pendingTool = &PendingTool{ID: tu.ID, Name: tu.Name, Input: tu.Input, Tier: toolTierFor(tu.Name)}
				break
			}
			start := time.Now()
			result, execErr := a.dispatchTool(ctx, tu.Name, tu.Input)
			duration := time.Since(start).Milliseconds()
			audit(chatID, username, tu.Name, tu.Input, true, "", duration, execErr)
			content := truncate(orErr(result, execErr), a.cfg.MaxToolOutputBytes)
			if statusUpdate != nil {
				statusUpdate(fmt.Sprintf("_(called %s)_", tu.Name))
			}
			toolResults = append(toolResults, anthropic.NewToolResultBlock(tu.ID, content, execErr != nil))
		}

		if pendingTool != nil {
			return &TurnResult{AssistantText: extractTextStd(resp.Content), Messages: stdToRaw(msgs), PendingTool: pendingTool}, nil
		}
		if len(toolResults) > 0 {
			msgs = append(msgs, anthropic.NewUserMessage(toolResults...))
		}
	}
}

// ── Beta API (with MCP servers) ──────────────────────────────────────────────

func (a *Agent) runTurnBeta(
	ctx context.Context,
	raw []json.RawMessage,
	chatID int64,
	username string,
	statusUpdate func(string),
) (*TurnResult, error) {
	betaMsgs := rawToBeta(raw)

	for {
		var resp *anthropic.BetaMessage
		if err := a.callWithRetry(ctx, func() error {
			var e error
			resp, e = a.client.Beta.Messages.New(ctx, anthropic.BetaMessageNewParams{
				Model:      anthropic.Model(a.cfg.ClaudeModel),
				MaxTokens:  4096,
				System:     []anthropic.BetaTextBlockParam{{Type: "text", Text: a.systemPrompt}},
				Messages:   betaMsgs,
				Tools:      a.betaTools,
				MCPServers: a.mcpServers,
				Betas:      []anthropic.AnthropicBeta{anthropic.AnthropicBetaMCPClient2025_04_04},
			})
			return e
		}); err != nil {
			return nil, fmt.Errorf("claude beta API: %w", err)
		}

		betaMsgs = append(betaMsgs, betaResponseToParam(resp))

		if resp.StopReason == "end_turn" || !hasToolUseBeta(resp.Content) {
			return &TurnResult{
				AssistantText: extractTextBeta(resp.Content),
				Messages:      betaToRaw(betaMsgs),
			}, nil
		}

		var toolResults []anthropic.BetaContentBlockParamUnion
		var pendingTool *PendingTool

		for _, block := range resp.Content {
			if block.Type != "tool_use" {
				continue
			}
			tu := block.AsToolUse()
			input, _ := json.Marshal(tu.Input)

			if toolTierFor(tu.Name) != tierReadonly {
				pendingTool = &PendingTool{ID: tu.ID, Name: tu.Name, Input: input, Tier: toolTierFor(tu.Name)}
				break
			}
			start := time.Now()
			result, execErr := a.dispatchTool(ctx, tu.Name, input)
			duration := time.Since(start).Milliseconds()
			audit(chatID, username, tu.Name, input, true, "", duration, execErr)
			content := truncate(orErr(result, execErr), a.cfg.MaxToolOutputBytes)
			if statusUpdate != nil {
				statusUpdate(fmt.Sprintf("_(called %s)_", tu.Name))
			}
			toolResults = append(toolResults, anthropic.NewBetaToolResultBlock(tu.ID, content, execErr != nil))
		}

		if pendingTool != nil {
			return &TurnResult{
				AssistantText: extractTextBeta(resp.Content),
				Messages:      betaToRaw(betaMsgs),
				PendingTool:   pendingTool,
			}, nil
		}
		if len(toolResults) > 0 {
			betaMsgs = append(betaMsgs, anthropic.NewBetaUserMessage(toolResults...))
		}
	}
}

// ── Tool dispatch ────────────────────────────────────────────────────────────

func (a *Agent) dispatchTool(ctx context.Context, name string, input json.RawMessage) (string, error) {
	var args map[string]interface{}
	if err := json.Unmarshal(input, &args); err != nil {
		return "", fmt.Errorf("parse tool input: %w", err)
	}
	str := func(key string) string { v, _ := args[key].(string); return v }
	intVal := func(key string, def int64) int64 {
		switch v := args[key].(type) {
		case float64:
			return int64(v)
		case int64:
			return v
		}
		return def
	}

	switch name {
	case "kubectl_get":
		return a.executors.Kubectl.Get(ctx, str("resource"), str("name"), str("namespace"), str("cluster"))
	case "kubectl_describe":
		return a.executors.Kubectl.Describe(ctx, str("resource"), str("name"), str("namespace"), str("cluster"))
	case "kubectl_logs":
		return a.executors.Kubectl.Logs(ctx, str("pod"), str("namespace"), str("cluster"), str("container"), intVal("tail_lines", 100))
	case "kubectl_get_events":
		return a.executors.Kubectl.GetEvents(ctx, str("namespace"), str("cluster"))
	case "kubectl_restart":
		return a.executors.Kubectl.Restart(ctx, str("deployment"), str("namespace"), str("cluster"))
	case "kubectl_scale":
		return a.executors.Kubectl.Scale(ctx, str("deployment"), str("namespace"), str("cluster"), int32(intVal("replicas", 1)))
	case "kubectl_rollout":
		return fmt.Sprintf("Use kubectl_describe on deployment %s/%s to check rollout status.", str("namespace"), str("deployment")), nil
	case "kubectl_delete":
		return a.executors.Kubectl.Delete(ctx, str("resource"), str("name"), str("namespace"), str("cluster"))
	case "helm_list":
		return a.executors.Helm.List(str("namespace"), str("cluster"))
	case "helm_status":
		return a.executors.Helm.Status(str("release"), str("namespace"), str("cluster"))
	case "helm_rollback":
		return a.executors.Helm.Rollback(str("release"), str("namespace"), str("cluster"), int(intVal("revision", 0)))
	case "git_status":
		return a.executors.Git.Status()
	case "git_log":
		return a.executors.Git.Log(str("repo_dir"), int(intVal("limit", 10)))
	case "git_pull":
		return a.executors.Git.Pull(str("repo_dir"))
	case "git_push":
		return a.executors.Git.Push(str("repo_dir"))
	case "ssh_exec_readonly":
		return a.executors.SSH.ExecReadonly(str("host"), str("command"))
	case "ssh_exec":
		return a.executors.SSH.Exec(str("host"), str("command"))
	case "flux_reconcile":
		return a.executors.Flux.Reconcile(ctx, str("kind"), str("name"), str("namespace"), str("cluster"))
	case "kubectl_exec":
		return a.executors.Kubectl.Exec(ctx, str("pod"), str("namespace"), str("container"), str("cluster"), str("command"))
	case "list_files":
		depth := int(intVal("max_depth", 3))
		return a.executors.File.ListFiles(str("dir"), depth)
	case "read_file":
		return a.executors.File.ReadFile(str("path"))
	case "write_file":
		return a.executors.File.WriteFile(str("path"), str("content"))
	case "git_diff":
		return a.executors.Git.Diff(str("repo_dir"))
	case "git_commit":
		return a.executors.Git.Commit(str("repo_dir"), str("message"))
	default:
		return "", fmt.Errorf("unknown tool: %s", name)
	}
}

// ── Conversation summarization ───────────────────────────────────────────────

// SummarizeHistory condenses old messages when the context grows large.
// It keeps the last keepRecent messages verbatim and replaces the older
// ones with a single summary pair [user(summary), assistant(ack)].
func (a *Agent) SummarizeHistory(ctx context.Context, messages []json.RawMessage) ([]json.RawMessage, error) {
	const keepRecent = 10
	if len(messages) <= keepRecent {
		return messages, nil
	}
	older := messages[:len(messages)-keepRecent]
	recent := messages[len(messages)-keepRecent:]

	transcript := transcriptFromRaw(older)

	resp, err := a.client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(a.cfg.ClaudeModel),
		MaxTokens: 512,
		System:    []anthropic.TextBlockParam{{Type: "text", Text: "Summarise the following conversation transcript concisely (3-8 bullet points). Focus on decisions made, operations performed, and important context. Output only the bullet list."}},
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock(transcript)),
		},
	})
	if err != nil {
		return messages, fmt.Errorf("summarize: %w", err)
	}

	summary := "Earlier conversation summary:\n" + extractTextStd(resp.Content)
	condensed := []json.RawMessage{
		mustMarshal(anthropic.NewUserMessage(anthropic.NewTextBlock(summary))),
		mustMarshal(anthropic.NewAssistantMessage(anthropic.NewTextBlock("Got it, I'll keep that context in mind."))),
	}
	return append(condensed, recent...), nil
}

// transcriptFromRaw builds a plain-text transcript from raw JSON message history.
func transcriptFromRaw(msgs []json.RawMessage) string {
	var sb strings.Builder
	for _, raw := range msgs {
		var m struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
				Name string `json:"name"`
			} `json:"content"`
		}
		if err := json.Unmarshal(raw, &m); err != nil {
			continue
		}
		for _, block := range m.Content {
			switch block.Type {
			case "text":
				if block.Text != "" {
					fmt.Fprintf(&sb, "%s: %s\n\n", strings.Title(m.Role), block.Text) //nolint:staticcheck
				}
			case "tool_use":
				fmt.Fprintf(&sb, "[tool: %s]\n\n", block.Name)
			}
		}
	}
	return sb.String()
}

// ── Status report ────────────────────────────────────────────────────────────

// StatusReport runs a canned health check across all configured clusters and
// returns a digest suitable for sending to Telegram.
func (a *Agent) StatusReport(ctx context.Context) string {
	var buf strings.Builder
	hasIssues := false

	for _, cluster := range a.cfg.Clusters {
		name := cluster.Name
		cs, err := a.executors.Kubectl.ClientFor(name)
		if err != nil {
			fmt.Fprintf(&buf, "⚠️ *%s*: unavailable (%v)\n", name, err)
			hasIssues = true
			continue
		}

		// Deployments not fully ready.
		deploys, err := cs.AppsV1().Deployments(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err != nil {
			fmt.Fprintf(&buf, "⚠️ *%s*: cannot list deployments: %v\n", name, err)
			hasIssues = true
		} else {
			for _, d := range deploys.Items {
				if d.Status.ReadyReplicas < d.Status.Replicas {
					fmt.Fprintf(&buf, "❌ *%s* `%s/%s`: %d/%d ready\n",
						name, d.Namespace, d.Name, d.Status.ReadyReplicas, d.Status.Replicas)
					hasIssues = true
				}
			}
		}

		// Warning events in the last hour.
		events, err := cs.CoreV1().Events(metav1.NamespaceAll).List(ctx, metav1.ListOptions{
			FieldSelector: "type=Warning",
		})
		if err == nil {
			cutoff := time.Now().Add(-1 * time.Hour)
			seen := 0
			for _, ev := range events.Items {
				if ev.LastTimestamp.After(cutoff) && seen < 5 {
					obj := fmt.Sprintf("%s/%s", ev.InvolvedObject.Kind, ev.InvolvedObject.Name)
					evMsg := ev.Message
					if len(evMsg) > 80 {
						evMsg = evMsg[:80] + "…"
					}
					fmt.Fprintf(&buf, "⚠️ *%s* `%s`: %s – %s\n", name, obj, ev.Reason, evMsg)
					hasIssues = true
					seen++
				}
			}
		}

		// Pods with high restart counts.
		pods, err := cs.CoreV1().Pods(metav1.NamespaceAll).List(ctx, metav1.ListOptions{})
		if err == nil {
			for _, p := range pods.Items {
				var restarts int32
				for _, cs2 := range p.Status.ContainerStatuses {
					restarts += cs2.RestartCount
				}
				if restarts > 5 {
					fmt.Fprintf(&buf, "🔄 *%s* `%s/%s`: %d restarts\n",
						name, p.Namespace, p.Name, restarts)
					hasIssues = true
				}
			}
		}
	}

	if !hasIssues {
		return "✅ All clusters healthy — no issues found."
	}
	return buf.String()
}

// ── MCP server construction ──────────────────────────────────────────────────

func buildMCPServers(cfgs []config.MCPServerConfig) []anthropic.BetaRequestMCPServerURLDefinitionParam {
	var out []anthropic.BetaRequestMCPServerURLDefinitionParam
	for _, c := range cfgs {
		srv := anthropic.BetaRequestMCPServerURLDefinitionParam{
			Name: c.Name,
			URL:  c.URL,
		}
		if c.TokenEnv != "" {
			if token := os.Getenv(c.TokenEnv); token != "" {
				srv.AuthorizationToken = anthropic.String(token)
			}
		}

		// Compute effective allowed tool list.
		// AllowedTools acts as an allowlist; DeniedTools removes entries from it.
		// If neither is set the server exposes all its tools.
		allowed := c.AllowedTools
		if len(c.DeniedTools) > 0 && len(allowed) > 0 {
			denied := make(map[string]struct{}, len(c.DeniedTools))
			for _, d := range c.DeniedTools {
				denied[d] = struct{}{}
			}
			filtered := allowed[:0:0]
			for _, a := range allowed {
				if _, skip := denied[a]; !skip {
					filtered = append(filtered, a)
				}
			}
			allowed = filtered
		}
		if len(allowed) > 0 {
			srv.ToolConfiguration = anthropic.BetaRequestMCPServerToolConfigurationParam{
				AllowedTools: allowed,
			}
			log.Printf("mcp: server %q restricted to %d tool(s)", c.Name, len(allowed))
		}

		out = append(out, srv)
	}
	return out
}

// ── Type conversion helpers ──────────────────────────────────────────────────

// convertToolsToBeta converts ToolUnionParam → BetaToolUnionParam via JSON.
func convertToolsToBeta(tools []anthropic.ToolUnionParam) []anthropic.BetaToolUnionParam {
	data, _ := json.Marshal(tools)
	var beta []anthropic.BetaToolUnionParam
	json.Unmarshal(data, &beta) //nolint:errcheck
	return beta
}

// rawToStd unmarshals raw JSON messages into []MessageParam.
func rawToStd(msgs []json.RawMessage) []anthropic.MessageParam {
	data, _ := json.Marshal(msgs)
	var std []anthropic.MessageParam
	json.Unmarshal(data, &std) //nolint:errcheck
	return std
}

// stdToRaw marshals []MessageParam into raw JSON messages.
func stdToRaw(msgs []anthropic.MessageParam) []json.RawMessage {
	data, _ := json.Marshal(msgs)
	var raw []json.RawMessage
	json.Unmarshal(data, &raw) //nolint:errcheck
	return raw
}

// rawToBeta unmarshals raw JSON messages into []BetaMessageParam.
func rawToBeta(msgs []json.RawMessage) []anthropic.BetaMessageParam {
	data, _ := json.Marshal(msgs)
	var beta []anthropic.BetaMessageParam
	json.Unmarshal(data, &beta) //nolint:errcheck
	return beta
}

// betaToRaw marshals []BetaMessageParam into raw JSON messages.
func betaToRaw(msgs []anthropic.BetaMessageParam) []json.RawMessage {
	data, _ := json.Marshal(msgs)
	var raw []json.RawMessage
	json.Unmarshal(data, &raw) //nolint:errcheck
	return raw
}

func mustMarshal(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}

// responseToParam converts a standard *Message to a MessageParam for history.
func responseToParam(resp *anthropic.Message) anthropic.MessageParam {
	var blocks []anthropic.ContentBlockParamUnion
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			tb := block.AsText()
			blocks = append(blocks, anthropic.NewTextBlock(tb.Text))
		case "tool_use":
			tu := block.AsToolUse()
			blocks = append(blocks, anthropic.NewToolUseBlock(tu.ID, tu.Input, tu.Name))
		}
	}
	return anthropic.NewAssistantMessage(blocks...)
}

// betaResponseToParam converts a *BetaMessage to a BetaMessageParam for history.
func betaResponseToParam(resp *anthropic.BetaMessage) anthropic.BetaMessageParam {
	var blocks []anthropic.BetaContentBlockParamUnion
	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			tb := block.AsText()
			blocks = append(blocks, anthropic.NewBetaTextBlock(tb.Text))
		case "tool_use":
			tu := block.AsToolUse()
			raw, _ := json.Marshal(tu.Input)
			blocks = append(blocks, anthropic.NewBetaToolUseBlock(tu.ID, json.RawMessage(raw), tu.Name))
		}
	}
	return anthropic.BetaMessageParam{
		Role:    anthropic.BetaMessageParamRoleAssistant,
		Content: blocks,
	}
}

// ── Content helpers ──────────────────────────────────────────────────────────

func hasToolUseStd(content []anthropic.ContentBlockUnion) bool {
	for _, b := range content {
		if b.Type == "tool_use" {
			return true
		}
	}
	return false
}

func hasToolUseBeta(content []anthropic.BetaContentBlockUnion) bool {
	for _, b := range content {
		if b.Type == "tool_use" {
			return true
		}
	}
	return false
}

func extractTextStd(content []anthropic.ContentBlockUnion) string {
	var parts []string
	for _, b := range content {
		if b.Type == "text" {
			tb := b.AsText()
			parts = append(parts, tb.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func extractTextBeta(content []anthropic.BetaContentBlockUnion) string {
	var parts []string
	for _, b := range content {
		if b.Type == "text" {
			tb := b.AsText()
			parts = append(parts, tb.Text)
		}
	}
	return strings.Join(parts, "\n")
}

func orErr(result string, err error) string {
	if err != nil {
		if result != "" {
			return result + "\nError: " + err.Error()
		}
		return "Error: " + err.Error()
	}
	return result
}

func truncate(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	var buf bytes.Buffer
	buf.WriteString(s[:max])
	fmt.Fprintf(&buf, "\n... [truncated %d bytes]", len(s)-max)
	return buf.String()
}
