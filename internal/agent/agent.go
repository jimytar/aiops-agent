package agent

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/jimytar/aiops-agent/internal/config"
	"github.com/jimytar/aiops-agent/internal/executor"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
	"golang.org/x/time/rate"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// TierFor exports the tool tier so the bot layer can gate confirmations.
func TierFor(name string) toolTier { return toolTierFor(name) }

// QueuedTool is a tool_use block that follows the current pending tool in the
// same assistant response. It awaits sequential processing after the current
// tool is confirmed and executed.
type QueuedTool struct {
	ID    string
	Name  string
	Input json.RawMessage
}

// PendingTool holds a mutating tool call Claude requested but not yet executed.
type PendingTool struct {
	ID   string
	Name string
	Input json.RawMessage
	Tier toolTier
	// PartialResults holds serialized tool_result blocks for readonly tools that
	// were auto-executed in the same response turn as this pending tool. They must
	// be included in the combined user message when this tool is finally executed,
	// so every tool_use in the assistant message has a matching tool_result.
	PartialResults []json.RawMessage
	// Queued holds any tool_use blocks that appeared after this one in the same
	// assistant response and have not yet been processed. After this tool is
	// confirmed, ExecuteTool processes Queued: readonly tools run immediately,
	// the next mutating tool becomes a new PendingTool requiring confirmation.
	Queued []QueuedTool
}

// Executors bundles all operation executors.
type Executors struct {
	Kubectl *executor.KubectlExecutor
	SSH     *executor.SSHExecutor
	Git     *executor.GitExecutor
	Helm    *executor.HelmExecutor
	Flux    *executor.FluxExecutor
	File    *executor.FileExecutor
	Frigate *executor.FrigateExecutor         // nil when FrigateURL is not configured
	HTTP    *executor.HTTPIntegrationExecutor // nil when no httpIntegrations configured
}

// toolOutput is returned by dispatchTool. ImageData is non-nil only for snapshot tools.
type toolOutput struct {
	Text      string
	ImageData []byte
	MediaType string
}

// Agent runs Claude conversations with tool_use.
type Agent struct {
	client           anthropic.Client
	cfg              *config.Config
	executors        *Executors
	tools            []anthropic.ToolUnionParam
	betaTools        []anthropic.BetaToolUnionParam
	mcpServers       []anthropic.BetaRequestMCPServerURLDefinitionParam
	systemPrompt     string
	systemBlocks     []anthropic.TextBlockParam
	betaSystemBlocks []anthropic.BetaTextBlockParam
	limiter          *rate.Limiter
	usage            *UsageTracker
}

func New(cfg *config.Config, execs *Executors, clusterNames []string) *Agent {
	// Apply tier overrides before building the tool list.
	applyToolsConfig(cfg.Tools, cfg.HTTPIntegrations)

	// 1 request/second sustained, burst of 3 — stays well within Anthropic limits.
	limiter := rate.NewLimiter(rate.Every(time.Second), 3)

	// For OAuth tokens (sk-ant-oat*), use Bearer auth and explicitly supply an
	// empty API key so the SDK does not fall back to reading ANTHROPIC_API_KEY
	// from the environment and sending a conflicting x-api-key header.
	var clientOpts []option.RequestOption
	if strings.HasPrefix(cfg.AnthropicAPIKey, "sk-ant-oat") {
		clientOpts = []option.RequestOption{
			option.WithAPIKey(""),
			option.WithAuthToken(cfg.AnthropicAPIKey),
		}
	} else {
		clientOpts = []option.RequestOption{option.WithAPIKey(cfg.AnthropicAPIKey)}
	}

	sysPrompt := cfg.SystemPrompt
	if cfg.FrigateURL != "" {
		sysPrompt += frigateSystemPromptSection
	}
	if rb := loadRunbooks(cfg.RunbookDir); rb != "" {
		sysPrompt += rb
	}

	// System prompt and tool definitions are static for the process lifetime.
	// Cache them with a 1h TTL so they are not re-billed on every turn.
	stdCache := anthropic.NewCacheControlEphemeralParam()
	stdCache.TTL = anthropic.CacheControlEphemeralTTLTTL1h
	betaCache := anthropic.NewBetaCacheControlEphemeralParam()
	betaCache.TTL = anthropic.BetaCacheControlEphemeralTTLTTL1h

	tools := buildTools(clusterNames, cfg.Tools, cfg.FrigateURL, cfg.HTTPIntegrations)
	if n := len(tools); n > 0 && tools[n-1].OfTool != nil {
		tools[n-1].OfTool.CacheControl = stdCache
	}

	a := &Agent{
		client:       anthropic.NewClient(clientOpts...),
		cfg:          cfg,
		executors:    execs,
		tools:        tools,
		systemPrompt: sysPrompt,
		systemBlocks: []anthropic.TextBlockParam{
			{Text: sysPrompt, CacheControl: stdCache},
		},
		betaSystemBlocks: []anthropic.BetaTextBlockParam{
			{Text: sysPrompt, CacheControl: betaCache},
		},
		limiter: limiter,
		usage:   newUsageTracker(),
	}
	// convertToolsToBeta JSON-round-trips tools, carrying CacheControl through.
	a.betaTools = convertToolsToBeta(a.tools)
	a.mcpServers = buildMCPServers(cfg.MCPServers)
	// Each MCP server must be referenced by an mcp_toolset entry in tools.
	for _, srv := range a.mcpServers {
		a.betaTools = append(a.betaTools, anthropic.BetaToolUnionParamOfMCPToolset(srv.Name))
	}
	return a
}

const frigateSystemPromptSection = `

FRIGATE NVR:
You also have access to Frigate NVR camera tools:
- frigate_cameras: list all configured cameras
- frigate_snapshot: fetch the latest JPEG frame from a camera and visually analyze the scene
- frigate_events: query recent detection events (filter by camera, label such as person/car/dog, limit)
Use these tools directly when the user asks about cameras, snapshots, or motion/object detection events.`

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
	out, execErr := a.dispatchTool(ctx, pending.Name, pending.Input)
	duration := time.Since(start).Milliseconds()
	audit(chatID, username, pending.Name, pending.Input, true, nonce, duration, execErr)

	// Build allContent: readonly PartialResults + result for this tool.
	var allContent []json.RawMessage
	allContent = append(allContent, pending.PartialResults...)
	for _, b := range buildToolResultBlocks(pending.ID, out, execErr, a.cfg.MaxToolOutputBytes) {
		if enc, err := json.Marshal(b); err == nil {
			allContent = append(allContent, enc)
		}
	}

	// Process Queued tools from the same assistant response turn.
	// Readonly tools execute immediately; the next mutating tool becomes a new
	// PendingTool returned to the bot so it can ask for confirmation.
	remaining := pending.Queued
	for len(remaining) > 0 {
		qt := remaining[0]
		remaining = remaining[1:]

		if toolTierFor(qt.Name) != tierReadonly {
			next := &PendingTool{
				ID:             qt.ID,
				Name:           qt.Name,
				Input:          qt.Input,
				Tier:           toolTierFor(qt.Name),
				PartialResults: allContent,
				Queued:         remaining,
			}
			// Return without advancing messages — bot will ask for the next confirmation.
			return &TurnResult{Messages: messages, PendingTool: next}, nil
		}

		// Readonly: execute now and accumulate.
		qstart := time.Now()
		qout, qerr := a.dispatchTool(ctx, qt.Name, qt.Input)
		audit(chatID, username, qt.Name, qt.Input, true, "", time.Since(qstart).Milliseconds(), qerr)
		if statusUpdate != nil {
			statusUpdate(fmt.Sprintf("_(called %s)_", qt.Name))
		}
		for _, b := range buildToolResultBlocks(qt.ID, qout, qerr, a.cfg.MaxToolOutputBytes) {
			if enc, err := json.Marshal(b); err == nil {
				allContent = append(allContent, enc)
			}
		}
	}

	// All tools in this response turn resolved — send combined message and continue.
	type rawMsg struct {
		Role    string            `json:"role"`
		Content []json.RawMessage `json:"content"`
	}
	raw, _ := json.Marshal(rawMsg{Role: "user", Content: allContent})
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
				System:    a.systemBlocks,
				Messages:  msgs,
				Tools:     a.tools,
			})
			return e
		}); err != nil {
			return nil, fmt.Errorf("claude API: %w", err)
		}

		msgs = append(msgs, responseToParam(resp))
		a.usage.add(resp.Usage.InputTokens, resp.Usage.OutputTokens,
			resp.Usage.CacheCreationInputTokens, resp.Usage.CacheReadInputTokens)

		if resp.StopReason == "end_turn" || !hasToolUseStd(resp.Content) {
			return &TurnResult{AssistantText: extractTextStd(resp.Content), Messages: stdToRaw(msgs)}, nil
		}

		var toolResults []anthropic.ContentBlockParamUnion
		var pendingTool *PendingTool

		for i, block := range resp.Content {
			if block.Type != "tool_use" {
				continue
			}
			tu := block.AsToolUse()
			if toolTierFor(tu.Name) != tierReadonly {
				pendingTool = &PendingTool{ID: tu.ID, Name: tu.Name, Input: tu.Input, Tier: toolTierFor(tu.Name)}
				for _, rest := range resp.Content[i+1:] {
					if rest.Type != "tool_use" {
						continue
					}
					rtu := rest.AsToolUse()
					pendingTool.Queued = append(pendingTool.Queued, QueuedTool{ID: rtu.ID, Name: rtu.Name, Input: rtu.Input})
				}
				break
			}
			start := time.Now()
			out, execErr := a.dispatchTool(ctx, tu.Name, tu.Input)
			duration := time.Since(start).Milliseconds()
			audit(chatID, username, tu.Name, tu.Input, true, "", duration, execErr)
			if statusUpdate != nil {
				statusUpdate(fmt.Sprintf("_(called %s)_", tu.Name))
			}
			toolResults = append(toolResults, buildToolResultBlocks(tu.ID, out, execErr, a.cfg.MaxToolOutputBytes)...)
		}

		if pendingTool != nil {
			for _, r := range toolResults {
				if b, err := json.Marshal(r); err == nil {
					pendingTool.PartialResults = append(pendingTool.PartialResults, b)
				}
			}
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
				System:     a.betaSystemBlocks,
				Messages:   betaMsgs,
				Tools:      a.betaTools,
				MCPServers: a.mcpServers,
				Betas:      []anthropic.AnthropicBeta{anthropic.AnthropicBetaMCPClient2025_11_20},
			})
			return e
		}); err != nil {
			return nil, fmt.Errorf("claude beta API: %w", err)
		}

		betaMsgs = append(betaMsgs, betaResponseToParam(resp))
		a.usage.add(resp.Usage.InputTokens, resp.Usage.OutputTokens,
			resp.Usage.CacheCreationInputTokens, resp.Usage.CacheReadInputTokens)

		if resp.StopReason == "end_turn" || !hasToolUseBeta(resp.Content) {
			return &TurnResult{
				AssistantText: extractTextBeta(resp.Content),
				Messages:      betaToRaw(betaMsgs),
			}, nil
		}

		var toolResults []anthropic.BetaContentBlockParamUnion
		var pendingTool *PendingTool

		for i, block := range resp.Content {
			if block.Type != "tool_use" {
				continue
			}
			tu := block.AsToolUse()
			input, _ := json.Marshal(tu.Input)

			if toolTierFor(tu.Name) != tierReadonly {
				pendingTool = &PendingTool{ID: tu.ID, Name: tu.Name, Input: input, Tier: toolTierFor(tu.Name)}
				for _, rest := range resp.Content[i+1:] {
					if rest.Type != "tool_use" {
						continue
					}
					rtu := rest.AsToolUse()
					rinput, _ := json.Marshal(rtu.Input)
					pendingTool.Queued = append(pendingTool.Queued, QueuedTool{ID: rtu.ID, Name: rtu.Name, Input: rinput})
				}
				break
			}
			start := time.Now()
			out, execErr := a.dispatchTool(ctx, tu.Name, input)
			duration := time.Since(start).Milliseconds()
			audit(chatID, username, tu.Name, input, true, "", duration, execErr)
			if statusUpdate != nil {
				statusUpdate(fmt.Sprintf("_(called %s)_", tu.Name))
			}
			toolResults = append(toolResults, buildBetaToolResultBlocks(tu.ID, out, execErr, a.cfg.MaxToolOutputBytes)...)
		}

		if pendingTool != nil {
			for _, r := range toolResults {
				if b, err := json.Marshal(r); err == nil {
					pendingTool.PartialResults = append(pendingTool.PartialResults, b)
				}
			}
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

func (a *Agent) dispatchTool(ctx context.Context, name string, input json.RawMessage) (toolOutput, error) {
	txt := func(s string, err error) (toolOutput, error) { return toolOutput{Text: s}, err }

	var args map[string]interface{}
	if err := json.Unmarshal(input, &args); err != nil {
		return toolOutput{}, fmt.Errorf("parse tool input: %w", err)
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
		return txt(a.executors.Kubectl.Get(ctx, str("resource"), str("name"), str("namespace"), str("cluster")))
	case "kubectl_describe":
		return txt(a.executors.Kubectl.Describe(ctx, str("resource"), str("name"), str("namespace"), str("cluster")))
	case "kubectl_logs":
		return txt(a.executors.Kubectl.Logs(ctx, str("pod"), str("namespace"), str("cluster"), str("container"), intVal("tail_lines", 100)))
	case "kubectl_get_events":
		return txt(a.executors.Kubectl.GetEvents(ctx, str("namespace"), str("cluster")))
	case "kubectl_restart":
		return txt(a.executors.Kubectl.Restart(ctx, str("deployment"), str("namespace"), str("cluster")))
	case "kubectl_scale":
		return txt(a.executors.Kubectl.Scale(ctx, str("deployment"), str("namespace"), str("cluster"), int32(intVal("replicas", 1))))
	case "kubectl_rollout":
		return txt(fmt.Sprintf("Use kubectl_describe on deployment %s/%s to check rollout status.", str("namespace"), str("deployment")), nil)
	case "kubectl_delete":
		return txt(a.executors.Kubectl.Delete(ctx, str("resource"), str("name"), str("namespace"), str("cluster")))
	case "helm_list":
		return txt(a.executors.Helm.List(str("namespace"), str("cluster")))
	case "helm_status":
		return txt(a.executors.Helm.Status(str("release"), str("namespace"), str("cluster")))
	case "helm_rollback":
		return txt(a.executors.Helm.Rollback(str("release"), str("namespace"), str("cluster"), int(intVal("revision", 0))))
	case "git_status":
		return txt(a.executors.Git.Status())
	case "git_log":
		return txt(a.executors.Git.Log(str("repo_dir"), int(intVal("limit", 10))))
	case "git_pull":
		return txt(a.executors.Git.Pull(str("repo_dir")))
	case "git_push":
		return txt(a.executors.Git.Push(str("repo_dir")))
	case "ssh_exec_readonly":
		return txt(a.executors.SSH.ExecReadonly(ctx, str("host"), str("command")))
	case "ssh_exec":
		return txt(a.executors.SSH.Exec(ctx, str("host"), str("command")))
	case "flux_reconcile":
		return txt(a.executors.Flux.Reconcile(ctx, str("kind"), str("name"), str("namespace"), str("cluster")))
	case "kubectl_exec":
		execCtx, execCancel := context.WithTimeout(ctx, time.Duration(a.cfg.ExecTimeoutSeconds)*time.Second)
		defer execCancel()
		return txt(a.executors.Kubectl.Exec(execCtx, str("pod"), str("namespace"), str("container"), str("cluster"), str("command")))
	case "list_files":
		return txt(a.executors.File.ListFiles(str("dir"), int(intVal("max_depth", 3))))
	case "read_file":
		return txt(a.executors.File.ReadFile(str("path")))
	case "write_file":
		return txt(a.executors.File.WriteFile(str("path"), str("content")))
	case "git_diff":
		return txt(a.executors.Git.Diff(str("repo_dir")))
	case "git_commit":
		return txt(a.executors.Git.Commit(str("repo_dir"), str("message")))
	case "git_tag":
		return txt(a.executors.Git.Tag(str("repo_dir"), str("tag"), str("message")))
	case "frigate_cameras":
		if a.executors.Frigate == nil {
			return txt("Frigate is not configured.", nil)
		}
		return txt(a.executors.Frigate.Cameras())
	case "frigate_events":
		if a.executors.Frigate == nil {
			return txt("Frigate is not configured.", nil)
		}
		return txt(a.executors.Frigate.Events(str("camera"), str("label"), int(intVal("limit", 10))))
	case "frigate_snapshot":
		if a.executors.Frigate == nil {
			return txt("Frigate is not configured.", nil)
		}
		imgData, mediaType, err := a.executors.Frigate.Snapshot(str("camera"))
		if err != nil {
			return toolOutput{}, err
		}
		return toolOutput{
			Text:      fmt.Sprintf("Latest snapshot from camera %q:", str("camera")),
			ImageData: imgData,
			MediaType: mediaType,
		}, nil
	default:
		// Dynamic HTTP integration tools: "<integration>_<endpoint>"
		if a.executors.HTTP != nil {
			for _, integration := range a.cfg.HTTPIntegrations {
				prefix := integration.Name + "_"
				if strings.HasPrefix(name, prefix) {
					endpoint := strings.TrimPrefix(name, prefix)
					method := "GET"
					for _, ep := range integration.Endpoints {
						if ep.Name == endpoint {
							method = strings.ToUpper(ep.Method)
							if method == "" {
								method = "GET"
							}
							break
						}
					}
					var qp, body string
					if method == "GET" || method == "DELETE" {
						if v, ok := args["query_params"]; ok {
							if b, err := json.Marshal(v); err == nil {
								qp = string(b)
							}
						}
					} else {
						if v, ok := args["body"]; ok {
							if b, err := json.Marshal(v); err == nil {
								body = string(b)
							}
						}
					}
					return txt(a.executors.HTTP.Call(ctx, integration.Name, endpoint, qp, body, a.cfg.MaxToolOutputBytes))
				}
			}
		}
		return toolOutput{}, fmt.Errorf("unknown tool: %s", name)
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
					fmt.Fprintf(&sb, "%s: %s\n\n", cases.Title(language.Und).String(m.Role), block.Text)
				}
			case "tool_use":
				fmt.Fprintf(&sb, "[tool: %s]\n\n", block.Name)
			}
		}
	}
	return sb.String()
}

// ── Status report ────────────────────────────────────────────────────────────

// UsageReport returns a formatted token usage and estimated cost report.
func (a *Agent) UsageReport() string {
	return a.usage.report(a.cfg.ClaudeModel)
}

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
	data, err := json.Marshal(tools)
	if err != nil {
		panic(fmt.Sprintf("convertToolsToBeta marshal: %v", err))
	}
	var beta []anthropic.BetaToolUnionParam
	if err := json.Unmarshal(data, &beta); err != nil {
		panic(fmt.Sprintf("convertToolsToBeta unmarshal: %v", err))
	}
	return beta
}

// rawToStd unmarshals raw JSON messages into []MessageParam.
func rawToStd(msgs []json.RawMessage) []anthropic.MessageParam {
	data, err := json.Marshal(msgs)
	if err != nil {
		panic(fmt.Sprintf("rawToStd marshal: %v", err))
	}
	var std []anthropic.MessageParam
	if err := json.Unmarshal(data, &std); err != nil {
		panic(fmt.Sprintf("rawToStd unmarshal: %v", err))
	}
	return std
}

// stdToRaw marshals []MessageParam into raw JSON messages.
func stdToRaw(msgs []anthropic.MessageParam) []json.RawMessage {
	data, err := json.Marshal(msgs)
	if err != nil {
		panic(fmt.Sprintf("stdToRaw marshal: %v", err))
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		panic(fmt.Sprintf("stdToRaw unmarshal: %v", err))
	}
	return raw
}

// rawToBeta unmarshals raw JSON messages into []BetaMessageParam.
func rawToBeta(msgs []json.RawMessage) []anthropic.BetaMessageParam {
	data, err := json.Marshal(msgs)
	if err != nil {
		panic(fmt.Sprintf("rawToBeta marshal: %v", err))
	}
	var beta []anthropic.BetaMessageParam
	if err := json.Unmarshal(data, &beta); err != nil {
		panic(fmt.Sprintf("rawToBeta unmarshal: %v", err))
	}
	return beta
}

// betaToRaw marshals []BetaMessageParam into raw JSON messages.
func betaToRaw(msgs []anthropic.BetaMessageParam) []json.RawMessage {
	data, err := json.Marshal(msgs)
	if err != nil {
		panic(fmt.Sprintf("betaToRaw marshal: %v", err))
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		panic(fmt.Sprintf("betaToRaw unmarshal: %v", err))
	}
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


// buildToolResultBlocks builds standard tool_result content blocks.
// If out.ImageData is set, the result contains a text block + an image block.
func buildToolResultBlocks(id string, out toolOutput, execErr error, maxBytes int) []anthropic.ContentBlockParamUnion {
	if execErr != nil {
		msg := "Error: " + execErr.Error()
		if out.Text != "" {
			msg = out.Text + "\n" + msg
		}
		return []anthropic.ContentBlockParamUnion{
			anthropic.NewToolResultBlock(id, truncate(msg, maxBytes), true),
		}
	}
	if out.ImageData != nil {
		return []anthropic.ContentBlockParamUnion{
			anthropic.NewToolResultBlock(id, out.Text, false),
			anthropic.NewImageBlockBase64(out.MediaType, base64.StdEncoding.EncodeToString(out.ImageData)),
		}
	}
	return []anthropic.ContentBlockParamUnion{
		anthropic.NewToolResultBlock(id, truncate(out.Text, maxBytes), false),
	}
}

// buildBetaToolResultBlocks builds beta tool_result content blocks.
// When an image is present, it is embedded as a proper BetaImageBlockParam so
// Claude receives it as a vision input (not as character-by-character text).
func buildBetaToolResultBlocks(id string, out toolOutput, execErr error, maxBytes int) []anthropic.BetaContentBlockParamUnion {
	if execErr != nil {
		msg := "Error: " + execErr.Error()
		if out.Text != "" {
			msg = out.Text + "\n" + msg
		}
		return []anthropic.BetaContentBlockParamUnion{
			anthropic.NewBetaToolResultBlock(id, truncate(msg, maxBytes), true),
		}
	}
	if out.ImageData != nil {
		mediaType := anthropic.BetaBase64ImageSourceMediaType(out.MediaType)
		imgBlock := anthropic.BetaImageBlockParam{
			Source: anthropic.BetaImageBlockParamSourceUnion{
				OfBase64: &anthropic.BetaBase64ImageSourceParam{
					MediaType: mediaType,
					Data:      base64.StdEncoding.EncodeToString(out.ImageData),
				},
			},
		}
		toolResult := anthropic.BetaToolResultBlockParam{
			ToolUseID: id,
			Content: []anthropic.BetaToolResultBlockParamContentUnion{
				{OfText: &anthropic.BetaTextBlockParam{Text: out.Text}},
				{OfImage: &imgBlock},
			},
		}
		return []anthropic.BetaContentBlockParamUnion{
			{OfToolResult: &toolResult},
		}
	}
	return []anthropic.BetaContentBlockParamUnion{
		anthropic.NewBetaToolResultBlock(id, truncate(out.Text, maxBytes), false),
	}
}

// loadRunbooks reads every *.md file from dir and returns them concatenated as
// a system prompt appendix. Returns an empty string if dir is absent or empty.
func loadRunbooks(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	var buf strings.Builder
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		data, err := os.ReadFile(dir + "/" + e.Name())
		if err != nil {
			log.Printf("runbooks: skipping %s: %v", e.Name(), err)
			continue
		}
		if buf.Len() == 0 {
			buf.WriteString("\n\nRUNBOOKS AND OPERATIONAL KNOWLEDGE:\n")
		}
		fmt.Fprintf(&buf, "\n---\n%s", data)
	}
	return buf.String()
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
