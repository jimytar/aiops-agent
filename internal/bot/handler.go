package bot

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/anthropics/anthropic-sdk-go"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jimytar/aiops-agent/internal/agent"
	"github.com/jimytar/aiops-agent/internal/config"
)

// Handler processes Telegram updates.
type Handler struct {
	bot      *tgbotapi.BotAPI
	cfg      *config.Config
	agent    *agent.Agent
	sessions *sessionStore
	confirms *confirmStore
}

func NewHandler(bot *tgbotapi.BotAPI, cfg *config.Config, a *agent.Agent) *Handler {
	return &Handler{
		bot:      bot,
		cfg:      cfg,
		agent:    a,
		sessions: newSessionStore(),
		confirms: newConfirmStore(),
	}
}

func (h *Handler) Run(ctx context.Context) error {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := h.bot.GetUpdatesChan(u)

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case update, ok := <-updates:
			if !ok {
				return nil
			}
			if update.Message == nil {
				continue
			}
			go h.handleMessage(ctx, update.Message)
		}
	}
}

// SendToChat delivers a message to a specific chat (called by the webhook handler).
func (h *Handler) SendToChat(chatID int64, text string) {
	h.send(chatID, text)
}

const summarizeThreshold = 28

func (h *Handler) handleMessage(ctx context.Context, msg *tgbotapi.Message) {
	chatID := msg.Chat.ID
	username := msg.From.UserName

	if !h.isAuthorized(chatID) {
		return
	}

	sess := h.sessions.get(chatID)
	sess.mu.Lock()
	defer sess.mu.Unlock()

	text := strings.TrimSpace(msg.Text)
	if text == "" {
		return
	}

	switch text {
	case "/start", "/help":
		h.send(chatID, welcomeMessage())
		return
	case "/reset":
		h.sessions.reset(chatID)
		h.confirms.clear(chatID)
		h.send(chatID, "Conversation reset.")
		return
	case "/cancel":
		if pending, ok := h.confirms.get(chatID); ok {
			h.confirms.clear(chatID)
			// Inject a synthetic tool_result so the message history stays valid.
			// Without this, the assistant's tool_use block has no matching result
			// and the next Claude API call returns 400.
			sess := h.sessions.get(chatID)
			userMsg := anthropic.NewUserMessage(
				anthropic.NewToolResultBlock(pending.Tool.ID, "Cancelled by user.", true),
			)
			raw, _ := json.Marshal(userMsg)
			sess.append(raw)
			h.send(chatID, "Pending confirmation cancelled.")
		} else {
			h.send(chatID, "No pending confirmation to cancel.")
		}
		return
	case "/status":
		h.send(chatID, "_Checking cluster health..._")
		report := h.agent.StatusReport(ctx)
		h.send(chatID, report)
		return
	}

	// Check if this is a nonce reply for a pending confirmation.
	if pending, ok := h.confirms.get(chatID); ok {
		if strings.ToUpper(strings.TrimSpace(text)) == pending.Nonce {
			h.executeConfirmed(ctx, chatID, username, pending)
			return
		}
		h.send(chatID, fmt.Sprintf(
			"⚠️ Pending confirmation `%s` is active. Send the nonce to confirm or /cancel to discard.\n\nNew messages are ignored while a confirmation is pending.",
			pending.Nonce,
		))
		return
	}

	// Regular conversation turn.
	userMsg := anthropic.NewUserMessage(anthropic.NewTextBlock(text))
	raw, _ := json.Marshal(userMsg)
	sess.append(raw)

	h.send(chatID, "_Thinking..._")

	result, err := h.agent.RunTurn(ctx, sess.history(), chatID, username, func(s string) {
		h.send(chatID, s)
	})
	if err != nil {
		h.send(chatID, fmt.Sprintf("❌ Error: %v", err))
		return
	}

	sess.messages = result.Messages

	if len(sess.messages) > summarizeThreshold {
		if condensed, err := h.agent.SummarizeHistory(ctx, sess.messages); err == nil {
			sess.messages = condensed
		} else {
			log.Printf("summarize history chat=%d: %v", chatID, err)
		}
	}

	if result.AssistantText != "" {
		h.send(chatID, result.AssistantText)
	}

	if result.PendingTool != nil {
		c := &pendingConfirmation{
			ChatID:    chatID,
			Tool:      result.PendingTool,
			Summary:   toolSummary(result.PendingTool),
			Nonce:     newNonce(),
			ExpiresAt: time.Now().Add(5 * time.Minute),
		}
		h.confirms.set(c)
		h.send(chatID, confirmPrompt(c))
	}
}

func (h *Handler) executeConfirmed(ctx context.Context, chatID int64, username string, pending *pendingConfirmation) {
	h.confirms.clear(chatID)
	h.send(chatID, fmt.Sprintf("✅ Executing `%s`...", pending.Tool.Name))

	sess := h.sessions.get(chatID)

	result, err := h.agent.ExecuteTool(ctx, pending.Tool, sess.history(), chatID, username, pending.Nonce, func(s string) {
		h.send(chatID, s)
	})
	if err != nil {
		h.send(chatID, fmt.Sprintf("❌ `%s` failed: %v", pending.Tool.Name, err))
		return
	}

	sess.messages = result.Messages

	if len(sess.messages) > summarizeThreshold {
		if condensed, err := h.agent.SummarizeHistory(ctx, sess.messages); err == nil {
			sess.messages = condensed
		} else {
			log.Printf("summarize history chat=%d: %v", chatID, err)
		}
	}

	if result.AssistantText != "" {
		h.send(chatID, result.AssistantText)
	}

	// Chain another confirmation if Claude requested another mutating tool.
	if result.PendingTool != nil {
		c := &pendingConfirmation{
			ChatID:    chatID,
			Tool:      result.PendingTool,
			Summary:   toolSummary(result.PendingTool),
			Nonce:     newNonce(),
			ExpiresAt: time.Now().Add(5 * time.Minute),
		}
		h.confirms.set(c)
		h.send(chatID, confirmPrompt(c))
	}
}

func (h *Handler) send(chatID int64, text string) {
	if text == "" {
		return
	}
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdown
	if _, err := h.bot.Send(msg); err != nil {
		log.Printf("telegram send (chat %d): %v", chatID, err)
		msg.ParseMode = ""
		_, _ = h.bot.Send(msg)
	}
}

func (h *Handler) isAuthorized(chatID int64) bool {
	for _, id := range h.cfg.AllowedChatIDs {
		if id == chatID {
			return true
		}
	}
	return false
}

func welcomeMessage() string {
	return `*AI Ops Agent* 🤖

I manage your homelab Kubernetes clusters, SSH nodes, Helm releases, and git repos.

*Commands*
• /status — quick health check across all clusters
• /reset — clear conversation history
• /cancel — cancel pending confirmation

*Examples*
• "List all deployments on simitli-k8s"
• "Show logs for home-assistant in namespace home-assistant"
• "Disk usage on node1?"
• "Restart the frigate deployment"
• "What Helm releases are deployed?"
• "Reconcile the flux kustomization infra"
`
}
