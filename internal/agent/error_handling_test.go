package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"
	"github.com/jimytar/aiops-agent/internal/config"
	"golang.org/x/time/rate"
)

// TestIsContextTooLarge covers all the error string patterns we detect.
func TestIsContextTooLarge(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"rate limit error", fmt.Errorf("429 rate_limit"), false},
		{"generic error", fmt.Errorf("internal server error"), false},
		{"request_too_large string", fmt.Errorf("400 Bad Request: request_too_large"), true},
		{"context_window_exceeded string", fmt.Errorf("context_window_exceeded: prompt too long"), true},
		{"request_too_large wrapped", fmt.Errorf("claude API: %w", fmt.Errorf("request_too_large")), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContextTooLarge(tc.err); got != tc.want {
				t.Errorf("isContextTooLarge(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// TestIsRateLimit covers the existing rate-limit detection for completeness.
func TestIsRateLimit(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil error", nil, false},
		{"context_too_large", fmt.Errorf("request_too_large"), false},
		{"generic error", fmt.Errorf("timeout"), false},
		{"429 in string", fmt.Errorf("unexpected status 429"), true},
		{"rate_limit in string", fmt.Errorf("error: rate_limit exceeded"), true},
		{"wrapped 429", fmt.Errorf("claude API: %w", fmt.Errorf("429 Too Many Requests")), true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isRateLimit(tc.err); got != tc.want {
				t.Errorf("isRateLimit(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// minimalAgent builds an Agent wired to the given base URL with no tools,
// executors, or system prompt — just enough to call the Anthropic API.
func minimalAgent(t *testing.T, baseURL string) *Agent {
	t.Helper()
	cfg := &config.Config{
		ClaudeModel:        "claude-sonnet-4-6",
		MaxToolOutputBytes: 8192,
	}
	return &Agent{
		client: anthropic.NewClient(
			option.WithAPIKey("test-key"),
			option.WithBaseURL(baseURL),
		),
		cfg:     cfg,
		limiter: rate.NewLimiter(rate.Inf, 1),
		usage:   newUsageTracker(),
	}
}

// validEndTurnResponse returns a minimal JSON body the Anthropic API would
// return for a successful end_turn with a single text block.
func validEndTurnResponse(text string) string {
	return fmt.Sprintf(`{
		"id": "msg_test",
		"type": "message",
		"role": "assistant",
		"stop_reason": "end_turn",
		"model": "claude-sonnet-4-6",
		"usage": {"input_tokens": 10, "output_tokens": 5},
		"content": [{"type": "text", "text": %q}]
	}`, text)
}

// maxTokensResponse returns a minimal JSON body with stop_reason "max_tokens".
func maxTokensResponse() string {
	return `{
		"id": "msg_test",
		"type": "message",
		"role": "assistant",
		"stop_reason": "max_tokens",
		"model": "claude-sonnet-4-6",
		"usage": {"input_tokens": 100, "output_tokens": 4096},
		"content": [{"type": "text", "text": "...truncated"}]
	}`
}

// requestTooLargeResponse returns the error body the Anthropic API sends when
// the context window is exceeded.
func requestTooLargeErrorBody() string {
	return `{"type":"error","error":{"type":"request_too_large","message":"Request body too large"}}`
}

// plainUserMsg builds a single plain-text user message as raw JSON.
func plainUserMsg(text string) json.RawMessage {
	b, _ := json.Marshal(map[string]interface{}{
		"role":    "user",
		"content": []map[string]string{{"type": "text", "text": text}},
	})
	return b
}

// TestRunTurnStd_MaxTokensReturnsError verifies that a max_tokens stop reason
// surfaces a clear error to the caller instead of silently returning empty text.
func TestRunTurnStd_MaxTokensReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, maxTokensResponse())
	}))
	defer srv.Close()

	a := minimalAgent(t, srv.URL)
	msgs := []json.RawMessage{plainUserMsg("write me 10000 words")}

	_, err := a.runTurnStd(context.Background(), msgs, 0, "testuser", nil)
	if err == nil {
		t.Fatal("expected an error for max_tokens stop reason, got nil")
	}
	if !strings.Contains(err.Error(), "max_tokens") {
		t.Errorf("error should mention max_tokens, got: %v", err)
	}
}

// TestRunTurnStd_ContextTooLarge_SummarizesAndRetries verifies that a
// request_too_large API error triggers history summarization and a retry,
// ultimately returning a successful result.
func TestRunTurnStd_ContextTooLarge_SummarizesAndRetries(t *testing.T) {
	callCount := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.Header().Set("Content-Type", "application/json")
		if callCount == 1 {
			// First call: return request_too_large.
			w.WriteHeader(http.StatusBadRequest)
			fmt.Fprint(w, requestTooLargeErrorBody())
			return
		}
		if callCount == 2 {
			// Second call: SummarizeHistory asks Claude to summarize — return summary.
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, validEndTurnResponse("• user asked about endpoints"))
			return
		}
		// Third call: the retried main turn.
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, validEndTurnResponse("Here is the answer after summarization."))
	}))
	defer srv.Close()

	a := minimalAgent(t, srv.URL)

	// Build a history long enough that SummarizeHistory won't return early
	// (keepRecent = 10; we need more than 10 messages).
	var msgs []json.RawMessage
	for i := 0; i < 6; i++ {
		msgs = append(msgs, plainUserMsg(fmt.Sprintf("question %d", i)))
		msgs = append(msgs,
			func() json.RawMessage {
				b, _ := json.Marshal(map[string]interface{}{
					"role":    "assistant",
					"content": []map[string]string{{"type": "text", "text": "answer"}},
				})
				return b
			}(),
		)
	}
	msgs = append(msgs, plainUserMsg("latest question"))

	result, err := a.runTurnStd(context.Background(), msgs, 0, "testuser", nil)
	if err != nil {
		t.Fatalf("expected successful retry after summarization, got error: %v", err)
	}
	if !strings.Contains(result.AssistantText, "after summarization") {
		t.Errorf("unexpected response text: %q", result.AssistantText)
	}
	if callCount != 3 {
		t.Errorf("expected 3 API calls (fail + summarize + retry), got %d", callCount)
	}
}

// TestRunTurnStd_ContextTooLarge_SummarizeFailsReturnsError verifies that when
// both the main call and the summarization call fail, a combined error is returned.
func TestRunTurnStd_ContextTooLarge_SummarizeFailsReturnsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Every call fails with request_too_large.
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, requestTooLargeErrorBody())
	}))
	defer srv.Close()

	a := minimalAgent(t, srv.URL)

	var msgs []json.RawMessage
	for i := 0; i < 6; i++ {
		msgs = append(msgs, plainUserMsg(fmt.Sprintf("q%d", i)))
		b, _ := json.Marshal(map[string]interface{}{
			"role":    "assistant",
			"content": []map[string]string{{"type": "text", "text": "a"}},
		})
		msgs = append(msgs, b)
	}
	msgs = append(msgs, plainUserMsg("latest"))

	_, err := a.runTurnStd(context.Background(), msgs, 0, "testuser", nil)
	if err == nil {
		t.Fatal("expected an error when both main call and summarization fail")
	}
	if !strings.Contains(err.Error(), "summarize also failed") {
		t.Errorf("error should mention summarization failure, got: %v", err)
	}
}

// TestRunTurnStd_NormalTurn_Succeeds is a sanity check that a normal end_turn
// response still works correctly through the updated code paths.
func TestRunTurnStd_NormalTurn_Succeeds(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, validEndTurnResponse("All good."))
	}))
	defer srv.Close()

	a := minimalAgent(t, srv.URL)
	msgs := []json.RawMessage{plainUserMsg("hello")}

	result, err := a.runTurnStd(context.Background(), msgs, 0, "testuser", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.AssistantText != "All good." {
		t.Errorf("got text %q, want %q", result.AssistantText, "All good.")
	}
}
