package bot

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jimytar/aiops-agent/internal/agent"
)

func TestNewNonceLength(t *testing.T) {
	n := newNonce()
	if len(n) != 6 {
		t.Errorf("nonce length = %d, want 6", len(n))
	}
}

func TestNewNonceCharset(t *testing.T) {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	for i := 0; i < 100; i++ {
		n := newNonce()
		for _, c := range n {
			if !strings.ContainsRune(chars, c) {
				t.Errorf("nonce %q contains invalid char %q", n, c)
			}
		}
	}
}

func TestNewNonceUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 200; i++ {
		n := newNonce()
		seen[n] = true
	}
	// With 6 chars from 32-char alphabet, collisions in 200 samples should be rare.
	if len(seen) < 180 {
		t.Errorf("too many collisions: only %d unique nonces in 200 draws", len(seen))
	}
}

func TestNewNonceNoConfusingChars(t *testing.T) {
	for i := 0; i < 200; i++ {
		n := newNonce()
		for _, bad := range []string{"O", "I", "0", "1"} {
			if strings.Contains(n, bad) {
				t.Errorf("nonce %q contains confusing char %q", n, bad)
			}
		}
	}
}

func TestConfirmStoreSetGet(t *testing.T) {
	s := newConfirmStore()
	c := &pendingConfirmation{
		ChatID:    42,
		Nonce:     "ABC123",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	s.set(c)

	got, ok := s.get(42)
	if !ok {
		t.Fatal("expected to find pending confirmation")
	}
	if got.Nonce != "ABC123" {
		t.Errorf("Nonce = %q", got.Nonce)
	}
}

func TestConfirmStoreGetMissing(t *testing.T) {
	s := newConfirmStore()
	_, ok := s.get(999)
	if ok {
		t.Fatal("expected no confirmation for unknown chatID")
	}
}

func TestConfirmStoreExpiry(t *testing.T) {
	s := newConfirmStore()
	c := &pendingConfirmation{
		ChatID:    1,
		Nonce:     "EXP123",
		ExpiresAt: time.Now().Add(-1 * time.Second), // already expired
	}
	s.set(c)

	_, ok := s.get(1)
	if ok {
		t.Fatal("expired confirmation should not be returned")
	}
}

func TestConfirmStoreClear(t *testing.T) {
	s := newConfirmStore()
	c := &pendingConfirmation{
		ChatID:    5,
		Nonce:     "CLR999",
		ExpiresAt: time.Now().Add(5 * time.Minute),
	}
	s.set(c)
	s.clear(5)

	_, ok := s.get(5)
	if ok {
		t.Fatal("cleared confirmation should not be returned")
	}
}

func TestConfirmStoreReplaces(t *testing.T) {
	s := newConfirmStore()
	s.set(&pendingConfirmation{ChatID: 7, Nonce: "FIRST1", ExpiresAt: time.Now().Add(time.Minute)})
	s.set(&pendingConfirmation{ChatID: 7, Nonce: "SECND2", ExpiresAt: time.Now().Add(time.Minute)})

	got, ok := s.get(7)
	if !ok {
		t.Fatal("expected confirmation")
	}
	if got.Nonce != "SECND2" {
		t.Errorf("expected latest nonce, got %q", got.Nonce)
	}
}

func makePendingTool(name string, input map[string]interface{}) *agent.PendingTool {
	raw, _ := json.Marshal(input)
	return &agent.PendingTool{
		ID:    "tool-id",
		Name:  name,
		Input: raw,
	}
}

func TestToolSummaryNameOnly(t *testing.T) {
	pt := makePendingTool("kubectl_get", map[string]interface{}{})
	s := toolSummary(pt)
	if s != "kubectl_get" {
		t.Errorf("toolSummary = %q", s)
	}
}

func TestToolSummaryWithArgs(t *testing.T) {
	pt := makePendingTool("kubectl_restart", map[string]interface{}{
		"deployment": "my-app",
		"namespace":  "default",
		"cluster":    "bastion",
	})
	s := toolSummary(pt)
	if !strings.HasPrefix(s, "kubectl_restart") {
		t.Errorf("toolSummary should start with tool name, got %q", s)
	}
	if !strings.Contains(s, "deployment=my-app") {
		t.Errorf("toolSummary missing deployment arg, got %q", s)
	}
	if !strings.Contains(s, "namespace=default") {
		t.Errorf("toolSummary missing namespace arg, got %q", s)
	}
	if !strings.Contains(s, "cluster=bastion") {
		t.Errorf("toolSummary missing cluster arg, got %q", s)
	}
}

func TestToolSummaryKeyOrder(t *testing.T) {
	// namespace and cluster should appear after the primary keys (deployment etc.)
	pt := makePendingTool("kubectl_delete", map[string]interface{}{
		"name":      "my-pod",
		"namespace": "kube-system",
		"cluster":   "prod",
		"resource":  "pods",
	})
	s := toolSummary(pt)
	// resource, name appear before namespace, cluster
	resourceIdx := strings.Index(s, "resource=")
	namespaceIdx := strings.Index(s, "namespace=")
	if resourceIdx < 0 || namespaceIdx < 0 {
		t.Fatalf("toolSummary = %q", s)
	}
	if resourceIdx > namespaceIdx {
		t.Errorf("resource should appear before namespace in %q", s)
	}
}

func TestConfirmPrompt(t *testing.T) {
	c := &pendingConfirmation{
		Summary: "kubectl_restart deployment=my-app",
		Nonce:   "XYZ789",
	}
	prompt := confirmPrompt(c)
	if !strings.Contains(prompt, "XYZ789") {
		t.Errorf("prompt should contain nonce, got %q", prompt)
	}
	if !strings.Contains(prompt, "kubectl_restart deployment=my-app") {
		t.Errorf("prompt should contain summary, got %q", prompt)
	}
	if !strings.Contains(prompt, "5 min") {
		t.Errorf("prompt should mention expiry, got %q", prompt)
	}
}
