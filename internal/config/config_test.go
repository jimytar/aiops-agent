package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestSSHHostAddr(t *testing.T) {
	tests := []struct {
		host SSHHost
		want string
	}{
		{SSHHost{Host: "192.168.1.10", Port: 22, User: "root"}, "192.168.1.10:22"},
		{SSHHost{Host: "192.168.1.10", Port: 0, User: "root"}, "192.168.1.10:22"},
		{SSHHost{Host: "myhost.local", Port: 2222, User: "ubuntu"}, "myhost.local:2222"},
	}
	for _, tt := range tests {
		if got := tt.host.Addr(); got != tt.want {
			t.Errorf("Addr() = %q, want %q", got, tt.want)
		}
	}
}

func TestDefaults(t *testing.T) {
	cfg := defaults()
	if cfg.ClaudeModel != "claude-sonnet-4-6" {
		t.Errorf("ClaudeModel default = %q", cfg.ClaudeModel)
	}
	if cfg.WebhookPort != 8080 {
		t.Errorf("WebhookPort default = %d", cfg.WebhookPort)
	}
	if cfg.MaxToolOutputBytes != 8192 {
		t.Errorf("MaxToolOutputBytes default = %d", cfg.MaxToolOutputBytes)
	}
	if cfg.ConfirmationTimeoutSeconds != 300 {
		t.Errorf("ConfirmationTimeoutSeconds default = %d", cfg.ConfirmationTimeoutSeconds)
	}
	if len(cfg.SSHAllowedReadonly) == 0 {
		t.Error("SSHAllowedReadonly should not be empty")
	}
	if len(cfg.SSHAllowedMutating) == 0 {
		t.Error("SSHAllowedMutating should not be empty")
	}
	if len(cfg.KubectlExecAllowedCommands) == 0 {
		t.Error("KubectlExecAllowedCommands should not be empty")
	}
	if cfg.SystemPrompt == "" {
		t.Error("SystemPrompt should not be empty")
	}
}

func TestLoadNoFile(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "tg-token")
	t.Setenv("ANTHROPIC_API_KEY", "ak-key")
	t.Setenv("ALLOWED_CHAT_IDS", "123")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TelegramToken != "tg-token" {
		t.Errorf("TelegramToken = %q", cfg.TelegramToken)
	}
	if cfg.AnthropicAPIKey != "ak-key" {
		t.Errorf("AnthropicAPIKey = %q", cfg.AnthropicAPIKey)
	}
	if len(cfg.AllowedChatIDs) != 1 || cfg.AllowedChatIDs[0] != 123 {
		t.Errorf("AllowedChatIDs = %v", cfg.AllowedChatIDs)
	}
	// defaults should still apply
	if cfg.ClaudeModel != "claude-sonnet-4-6" {
		t.Errorf("ClaudeModel = %q", cfg.ClaudeModel)
	}
}

func TestLoadYAML(t *testing.T) {
	yaml := `
telegramToken: my-tg-token
anthropicAPIKey: my-ak-key
allowedChatIDs: [111, 222]
defaultChatID: 111
claudeModel: claude-opus-4-7
webhookPort: 9090
`
	f, err := os.CreateTemp(t.TempDir(), "config*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(yaml); err != nil {
		t.Fatal(err)
	}
	f.Close()

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TelegramToken != "my-tg-token" {
		t.Errorf("TelegramToken = %q", cfg.TelegramToken)
	}
	if cfg.ClaudeModel != "claude-opus-4-7" {
		t.Errorf("ClaudeModel = %q", cfg.ClaudeModel)
	}
	if cfg.WebhookPort != 9090 {
		t.Errorf("WebhookPort = %d", cfg.WebhookPort)
	}
	if len(cfg.AllowedChatIDs) != 2 {
		t.Errorf("AllowedChatIDs = %v", cfg.AllowedChatIDs)
	}
	if cfg.DefaultChatID != 111 {
		t.Errorf("DefaultChatID = %d", cfg.DefaultChatID)
	}
}

func TestLoadEnvOverrides(t *testing.T) {
	yaml := `
telegramToken: yaml-tg-token
anthropicAPIKey: yaml-ak-key
allowedChatIDs: [111]
claudeModel: claude-haiku-4-5
`
	f, err := os.CreateTemp(t.TempDir(), "config*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(yaml); err != nil {
		t.Fatal(err)
	}
	f.Close()

	t.Setenv("TELEGRAM_TOKEN", "env-tg-token")
	t.Setenv("ANTHROPIC_API_KEY", "env-ak-key")
	t.Setenv("CLAUDE_MODEL", "claude-opus-4-7")
	t.Setenv("DEFAULT_CHAT_ID", "999")
	t.Setenv("ALLOWED_CHAT_IDS", "777,888")
	t.Setenv("WEBHOOK_TOKEN", "my-webhook-token")

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.TelegramToken != "env-tg-token" {
		t.Errorf("TelegramToken = %q (env should override yaml)", cfg.TelegramToken)
	}
	if cfg.AnthropicAPIKey != "env-ak-key" {
		t.Errorf("AnthropicAPIKey = %q", cfg.AnthropicAPIKey)
	}
	if cfg.ClaudeModel != "claude-opus-4-7" {
		t.Errorf("ClaudeModel = %q", cfg.ClaudeModel)
	}
	if cfg.DefaultChatID != 999 {
		t.Errorf("DefaultChatID = %d", cfg.DefaultChatID)
	}
	if len(cfg.AllowedChatIDs) != 2 || cfg.AllowedChatIDs[0] != 777 || cfg.AllowedChatIDs[1] != 888 {
		t.Errorf("AllowedChatIDs = %v", cfg.AllowedChatIDs)
	}
	if cfg.WebhookToken != "my-webhook-token" {
		t.Errorf("WebhookToken = %q", cfg.WebhookToken)
	}
}

func TestLoadAllowedChatIDsMultiple(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "tok")
	t.Setenv("ANTHROPIC_API_KEY", "key")
	t.Setenv("ALLOWED_CHAT_IDS", " 1 , 2 , 3 ")

	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(cfg.AllowedChatIDs) != 3 {
		t.Errorf("AllowedChatIDs = %v", cfg.AllowedChatIDs)
	}
}

func TestLoadAllowedChatIDsInvalidEnv(t *testing.T) {
	// Invalid IDs in ALLOWED_CHAT_IDS are skipped; if none are valid, yaml value is kept.
	yaml := `
telegramToken: tok
anthropicAPIKey: key
allowedChatIDs: [42]
`
	f, err := os.CreateTemp(t.TempDir(), "config*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(yaml); err != nil {
		t.Fatal(err)
	}
	f.Close()

	t.Setenv("ALLOWED_CHAT_IDS", "notanumber")

	cfg, err := Load(f.Name())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// No valid IDs parsed from env, so yaml value is kept.
	if len(cfg.AllowedChatIDs) != 1 || cfg.AllowedChatIDs[0] != 42 {
		t.Errorf("AllowedChatIDs = %v", cfg.AllowedChatIDs)
	}
}

func TestLoadMissingFileSilent(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "tok")
	t.Setenv("ANTHROPIC_API_KEY", "key")
	t.Setenv("ALLOWED_CHAT_IDS", "1")

	nonexistent := filepath.Join(t.TempDir(), "does_not_exist.yaml")
	_, err := Load(nonexistent)
	if err != nil {
		t.Fatalf("Load with missing file should not error: %v", err)
	}
}

func TestValidateMissingTelegramToken(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "key")
	t.Setenv("ALLOWED_CHAT_IDS", "1")

	_, err := Load("")
	if err == nil {
		t.Fatal("expected error for missing TELEGRAM_TOKEN")
	}
}

func TestValidateMissingAnthropicKey(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "tok")
	t.Setenv("ALLOWED_CHAT_IDS", "1")

	_, err := Load("")
	if err == nil {
		t.Fatal("expected error for missing ANTHROPIC_API_KEY")
	}
}

func TestValidateMissingAllowedChatIDs(t *testing.T) {
	t.Setenv("TELEGRAM_TOKEN", "tok")
	t.Setenv("ANTHROPIC_API_KEY", "key")

	_, err := Load("")
	if err == nil {
		t.Fatal("expected error for missing allowedChatIDs")
	}
}

func TestLoadBadYAML(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "config*.yaml")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString("not: valid: yaml: {{{"); err != nil {
		t.Fatal(err)
	}
	f.Close()

	_, err = Load(f.Name())
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
}
