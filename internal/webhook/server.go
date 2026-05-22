package webhook

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
)

// Sender is implemented by bot.Handler to forward webhook payloads to Telegram.
type Sender interface {
	SendToChat(chatID int64, text string)
}

// Server listens for inbound webhook POSTs and forwards them to the Telegram chat.
type Server struct {
	token         string
	defaultChatID int64
	sender        Sender
}

func NewServer(token string, defaultChatID int64, sender Sender) *Server {
	return &Server{token: token, defaultChatID: defaultChatID, sender: sender}
}

func (s *Server) Run(ctx context.Context, addr string) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/webhook", s.handleWebhook)

	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}()

	log.Printf("webhook server listening on %s", addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		return fmt.Errorf("webhook server: %w", err)
	}
	return nil
}

func (s *Server) handleHealth(w http.ResponseWriter, _ *http.Request) {
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write([]byte("ok"))
}

func (s *Server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Validate webhook token if configured.
	if s.token != "" {
		got := r.Header.Get("X-Webhook-Token")
		if got == "" {
			got = r.URL.Query().Get("token")
		}
		if got != s.token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
	}

	chatID := s.defaultChatID
	if chatID == 0 {
		log.Printf("webhook: no defaultChatID configured, dropping message")
		w.WriteHeader(http.StatusAccepted)
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20) // 1 MB limit
	var payload map[string]interface{}
	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		// Accept non-JSON bodies too (e.g. plain text alerts).
		payload = map[string]interface{}{"raw": r.Body}
	}

	text := formatPayload(payload, r.Header.Get("X-Source"))
	go s.sender.SendToChat(chatID, text)

	w.WriteHeader(http.StatusAccepted)
}

func formatPayload(payload map[string]interface{}, source string) string {
	// Try common alert schemas (Grafana, Alertmanager, generic).
	if title, ok := payload["title"].(string); ok {
		msg := fmt.Sprintf("📣 *Webhook alert*")
		if source != "" {
			msg += fmt.Sprintf(" from `%s`", source)
		}
		msg += fmt.Sprintf("\n\n*%s*", title)
		if body, ok := payload["message"].(string); ok {
			msg += "\n" + body
		} else if body, ok := payload["body"].(string); ok {
			msg += "\n" + body
		}
		// Grafana state
		if state, ok := payload["state"].(string); ok {
			msg += fmt.Sprintf("\nState: `%s`", state)
		}
		return msg
	}

	// Alertmanager format
	if alerts, ok := payload["alerts"].([]interface{}); ok && len(alerts) > 0 {
		msg := fmt.Sprintf("🔔 *%d alert(s) received*", len(alerts))
		for i, a := range alerts {
			if i >= 3 {
				msg += fmt.Sprintf("\n...and %d more", len(alerts)-3)
				break
			}
			if am, ok := a.(map[string]interface{}); ok {
				if labels, ok := am["labels"].(map[string]interface{}); ok {
					if name, ok := labels["alertname"].(string); ok {
						status, _ := am["status"].(string)
						msg += fmt.Sprintf("\n• `%s` (%s)", name, status)
					}
				}
			}
		}
		return msg
	}

	// Fallback: dump JSON.
	data, _ := json.MarshalIndent(payload, "", "  ")
	prefix := "📨 *Webhook received*"
	if source != "" {
		prefix += fmt.Sprintf(" from `%s`", source)
	}
	return fmt.Sprintf("%s\n```json\n%s\n```", prefix, string(data))
}
