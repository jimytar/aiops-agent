package bot

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"

	"github.com/jimytar/aiops-agent/internal/agent"
)

type pendingConfirmation struct {
	ChatID    int64
	Tool      *agent.PendingTool
	Summary   string
	Nonce     string
	ExpiresAt time.Time
}

type confirmStore struct {
	mu      sync.Mutex
	pending map[int64]*pendingConfirmation
}

func newConfirmStore() *confirmStore {
	return &confirmStore{pending: make(map[int64]*pendingConfirmation)}
}

func (s *confirmStore) set(c *pendingConfirmation) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending[c.ChatID] = c
}

func (s *confirmStore) get(chatID int64) (*pendingConfirmation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.pending[chatID]
	if !ok {
		return nil, false
	}
	if time.Now().After(c.ExpiresAt) {
		delete(s.pending, chatID)
		return nil, false
	}
	return c, true
}

func (s *confirmStore) clear(chatID int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.pending, chatID)
}

func newNonce() string {
	const chars = "ABCDEFGHJKLMNPQRSTUVWXYZ23456789"
	b := make([]byte, 6)
	for i := range b {
		b[i] = chars[rand.Intn(len(chars))]
	}
	return string(b)
}

func confirmPrompt(c *pendingConfirmation) string {
	return fmt.Sprintf(
		"⚠️ *Confirmation required*\n\n`%s`\n\nReply with `%s` to proceed (expires in 5 min).",
		c.Summary, c.Nonce,
	)
}

func toolSummary(pt *agent.PendingTool) string {
	var args map[string]interface{}
	_ = json.Unmarshal(pt.Input, &args)

	parts := []string{pt.Name}
	for _, key := range []string{"deployment", "release", "pod", "resource", "name", "host", "command"} {
		if v, ok := args[key]; ok {
			parts = append(parts, fmt.Sprintf("%s=%v", key, v))
		}
	}
	for _, key := range []string{"namespace", "cluster"} {
		if v, ok := args[key]; ok {
			parts = append(parts, fmt.Sprintf("%s=%v", key, v))
		}
	}
	return strings.Join(parts, " ")
}
