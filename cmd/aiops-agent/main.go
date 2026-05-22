package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
	"github.com/jimytar/aiops-agent/internal/agent"
	"github.com/jimytar/aiops-agent/internal/bot"
	"github.com/jimytar/aiops-agent/internal/config"
	"github.com/jimytar/aiops-agent/internal/executor"
	k8sclient "github.com/jimytar/aiops-agent/internal/k8s"
	"github.com/jimytar/aiops-agent/internal/webhook"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("fatal: %v", err)
	}
}

func run() error {
	cfgPath := os.Getenv("CONFIG_FILE")
	if cfgPath == "" {
		cfgPath = "/etc/aiops/config.yaml"
	}

	cfg, err := config.Load(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Kubernetes clients (one per configured cluster).
	k8sClients, err := k8sclient.NewClients(cfg)
	if err != nil {
		return fmt.Errorf("init k8s clients: %w", err)
	}
	log.Printf("loaded k8s clients for clusters: %v", k8sClients.ClusterNames())

	// SSH executor.
	sshExec, err := executor.NewSSHExecutor(cfg)
	if err != nil {
		return fmt.Errorf("init ssh executor: %w", err)
	}
	log.Printf("loaded %d SSH keys, known hosts: %v", 0, sshExec.KnownHosts())

	// Assemble executors.
	var frigateExec *executor.FrigateExecutor
	if cfg.FrigateURL != "" {
		frigateExec = executor.NewFrigateExecutor(cfg.FrigateURL)
		log.Printf("frigate integration enabled: %s", cfg.FrigateURL)
	}

	execs := &agent.Executors{
		Kubectl: executor.NewKubectlExecutor(k8sClients, cfg.KubectlExecAllowedCommands),
		SSH:     sshExec,
		Git:     executor.NewGitExecutor(cfg),
		Helm:    executor.NewHelmExecutor(cfg),
		Flux:    executor.NewFluxExecutor(k8sClients),
		File:    executor.NewFileExecutor(cfg.GitRepoDirs),
		Frigate: frigateExec,
	}

	// Agent.
	a := agent.New(cfg, execs, k8sClients.ClusterNames())

	// Telegram bot.
	tgBot, err := tgbotapi.NewBotAPI(cfg.TelegramToken)
	if err != nil {
		return fmt.Errorf("telegram bot init: %w", err)
	}
	log.Printf("telegram bot authorized as @%s", tgBot.Self.UserName)

	botHandler := bot.NewHandler(tgBot, cfg, a)

	// Webhook server.
	webhookServer := webhook.NewServer(cfg.WebhookToken, cfg.DefaultChatID, botHandler)

	// Root context with signal handling.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Start webhook server in background.
	webhookAddr := fmt.Sprintf(":%d", cfg.WebhookPort)
	go func() {
		if err := webhookServer.Run(ctx, webhookAddr); err != nil {
			log.Printf("webhook server exited: %v", err)
		}
	}()

	log.Printf("aiops-agent started (model: %s, clusters: %v)", cfg.ClaudeModel, k8sClients.ClusterNames())

	// Telegram long polling (blocks until context cancelled).
	return botHandler.Run(ctx)
}
