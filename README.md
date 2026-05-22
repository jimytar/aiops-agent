# aiops-agent

A Telegram bot backed by Claude AI that manages a homelab via natural language. It speaks Kubernetes, Helm, Flux, SSH, Git, and file operations — and forwards Grafana/Alertmanager/Flux webhook payloads to your chat.

---

## Table of Contents

- [What it does](#what-it-does)
- [Architecture](#architecture)
- [Tool tiers and confirmation system](#tool-tiers-and-confirmation-system)
- [Configuration reference](#configuration-reference)
- [Secrets](#secrets)
- [Deployment (GitOps via Flux)](#deployment-gitops-via-flux)
- [HelmRelease values example](#helmrelease-values-example)
- [Secret file stubs](#secret-file-stubs)
- [GitOps workflow example](#gitops-workflow-example)
- [Webhook integration](#webhook-integration)
- [Building locally](#building-locally)
- [Helm chart](#helm-chart)

---

## What it does

| Capability | Operations |
|---|---|
| **Kubernetes** | get, describe, logs, events, restart, scale, rollout, delete, exec — across multiple clusters via client-go (no kubectl binary) |
| **Helm** | list, status, rollback releases |
| **Flux** | trigger reconciliation of Kustomizations and HelmReleases |
| **SSH** | read-only and mutating commands on homelab nodes (allowlisted prefixes) |
| **Git** | status, log, pull, push, commit, diff across cloned repos |
| **File ops** | read, list, write files in git repos (for GitOps workflows) |
| **MCP servers** | connect to external MCP servers, e.g. Home Assistant via Claude's Beta MCP API |
| **Webhook endpoint** | receive Grafana/Alertmanager/Flux payloads and forward to Telegram |
| **/status command** | one-shot health check: unready deployments, warning events, high-restart pods across all clusters |
| **Conversation summarisation** | auto-condenses history when it grows large |

---

## Architecture

```
cmd/aiops-agent/          Binary entrypoint
internal/
  config/                 YAML config loading + env var overrides
  bot/                    Telegram long-polling handler, confirmation UX, session store
  webhook/                HTTP server for external payloads (Grafana, Alertmanager, Flux)
  agent/                  Claude conversation loop, tool dispatch, audit log
  executor/               Tool implementations: kubectl (client-go), ssh, helm, git, flux, file
  k8s/                    Multi-cluster client factory
charts/aiops-agent/       Helm chart (published to oci://ghcr.io/jimytar/charts)
```

The agent runs a long-polling Telegram loop. Each message from an allowed chat ID enters a conversation session. Claude decides which tools to call; the agent dispatcher routes each tool call to the appropriate executor, enforces the tier confirmation system, and streams results back to Claude before replying to the user.

---

## Tool tiers and confirmation system

Every tool is assigned a tier. Before a mutating or destructive tool executes, the agent sends a confirmation challenge to Telegram. Only the original chat can satisfy it.

| Tier | Confirmation required | How to confirm |
|---|---|---|
| **readonly** | None — executes immediately | — |
| **mutating** | 6-character nonce sent to chat | Reply with the nonce within 5 minutes |
| **destructive** | Nonce + resource name re-entry | Reply with nonce and the resource name |

**Default tier assignments:**

| Tool | Default tier |
|---|---|
| `kubectl_get`, `kubectl_describe`, `kubectl_logs`, `kubectl_get_events` | readonly |
| `helm_list`, `helm_status` | readonly |
| `git_status`, `git_log`, `git_diff` | readonly |
| `ssh_exec_readonly` | readonly |
| `list_files`, `read_file` | readonly |
| `kubectl_restart`, `kubectl_scale`, `kubectl_rollout`, `kubectl_exec` | mutating |
| `helm_rollback` | mutating |
| `git_pull`, `git_push`, `git_commit` | mutating |
| `write_file` | mutating |
| `flux_reconcile` | mutating |
| `ssh_exec` | mutating |
| `kubectl_delete` | destructive |

Tiers can be overridden per-tool in `values.yaml`:

```yaml
agent:
  tools:
    tiers:
      kubectl_restart: readonly   # skip confirmation for restarts
```

Individual tools can be disabled entirely:

```yaml
agent:
  tools:
    disabled:
      - kubectl_delete
      - ssh_exec
```

---

## Configuration reference

Configuration is loaded from a YAML file (rendered from `values.yaml` into a ConfigMap) and can be partially overridden by environment variables.

### Go config struct

```go
TelegramToken              string
AllowedChatIDs             []int64
DefaultChatID              int64
AnthropicAPIKey            string
ClaudeModel                string
WebhookPort                int
WebhookToken               string
KubeconfigDir              string
Clusters                   []ClusterConfig{Name string; InCluster bool; KubeconfigFile string}
SSHKeyDir                  string
SSHHosts                   map[string]SSHHost{User, Host string; Port int}
SSHAllowedReadonly         []string
SSHAllowedMutating         []string
MaxToolOutputBytes         int
ConfirmationTimeoutSeconds int
GitRepoDirs                []string   // auto-populated from gitRepos.repos[*].mountPath
MCPServers                 []MCPServerConfig{Name, URL, TokenEnv string; AllowedTools, DeniedTools []string}
Tools                      ToolsConfig{Disabled []string; Tiers map[string]string}
KubectlExecAllowedCommands []string
FrigateURL                 string     // base URL of Frigate NVR; leave empty to disable
RunbookDir                 string     // directory of *.md files appended to system prompt (default /etc/aiops/runbooks)
```

### Environment variable overrides

| Variable | Config field |
|---|---|
| `TELEGRAM_TOKEN` | `TelegramToken` |
| `ANTHROPIC_API_KEY` | `AnthropicAPIKey` |
| `WEBHOOK_TOKEN` | `WebhookToken` |
| `CLAUDE_MODEL` | `ClaudeModel` |
| `DEFAULT_CHAT_ID` | `DefaultChatID` |
| `ALLOWED_CHAT_IDS` | `AllowedChatIDs` (comma-separated) |

---

## Secrets

All secrets are SOPS+AGE encrypted Kubernetes Secret objects.

| Secret name | Key | Purpose |
|---|---|---|
| `aiops-telegram-token` | `token` | Telegram bot token from BotFather |
| `aiops-claude-api-key` | `api-key` | Anthropic API key |
| `aiops-webhook-token` | `token` | Arbitrary token for webhook authentication |
| `aiops-ssh-keys` | `<filename>` → private key content | SSH private keys, mounted at `/etc/aiops/ssh/` |
| `aiops-ha-token` | `token` | Home Assistant long-lived access token (MCP) |
| `aiops-github-token` | `token` | GitHub PAT (repo or Contents:Write scope) for git repo cloning |
| `aiops-kubeconfig-<cluster>` | `kubeconfig` | One per remote cluster |

---

## Deployment (GitOps via Flux)

The agent is deployed on a bastion Kubernetes cluster and managed by Flux. Secrets are SOPS+AGE encrypted in the GitOps repository. The image is built by GitHub Actions on every push to `main` and pushed to `ghcr.io/jimytar/aiops-agent:main`. The Helm chart is published to `oci://ghcr.io/jimytar/charts` on git tags.

---

## HelmRelease values example

```yaml
image:
  repository: ghcr.io/jimytar/aiops-agent
  tag: "main"

agent:
  claudeModel: claude-sonnet-4-6
  allowedChatIDs: [123456789]
  defaultChatID: 123456789

  clusters:
    - name: bastion
      inCluster: true
    - name: simitli-k8s
      kubeconfigFile: simitli-k8s

  sshHosts:
    node1:
      user: root
      host: 192.168.1.10
      port: 22

  sshAllowedReadonly:
    - "systemctl status"
    - "journalctl"
    - "df -h"
    - "free -h"
    - "uptime"

  sshAllowedMutating:
    - "systemctl restart"
    - "systemctl stop"
    - "systemctl start"

  kubectlExecAllowedCommands:
    - "env"
    - "ls"
    - "cat "
    - "psql "
    - "redis-cli "

  tools:
    disabled: []
    tiers:
      kubectl_restart: readonly   # skip confirmation for restarts

  mcpServers:
    - name: home-assistant
      url: https://ha.example.com/api/mcp
      tokenEnv: HA_MCP_TOKEN
      tokenSecretName: aiops-ha-token
      tokenSecretKey: token

gitRepos:
  enabled: true
  tokenSecretName: aiops-github-token
  tokenSecretKey: token
  repos:
    - name: deployments
      url: https://github.com/youruser/deployments
      mountPath: /repos/deployments

runbooks:
  inline:
    oom-runbook.md: |
      ## OOMKilled Runbook
      When a pod is OOMKilled bump resources in the manifest and git_push.
    deploy-runbook.md: |
      ## Deployment Runbook
      Always run kubectl rollout status after a deploy and confirm readiness probes pass.

existingSecrets:
  telegramToken:
    secretName: aiops-telegram-token
    key: token
  anthropicAPIKey:
    secretName: aiops-claude-api-key
    key: api-key
  webhookToken:
    secretName: aiops-webhook-token
    key: token
  sshKeys:
    secretName: aiops-ssh-keys
  kubeconfigs:
    - secretName: aiops-kubeconfig-simitli
      key: kubeconfig
      filename: simitli-k8s

ingress:
  enabled: true
  className: traefik
  host: aiops.example.com
```

---

## Runbooks — injecting operational knowledge

Any `*.md` file mounted at `/etc/aiops/runbooks/` is appended to Claude's system prompt at startup under a `RUNBOOKS AND OPERATIONAL KNOWLEDGE` section. Use this to teach the agent team-specific procedures, naming conventions, escalation paths, or domain context — without rebuilding the image.

The prompt cache covers the runbooks (1 h TTL), so adding runbooks does not meaningfully increase per-turn cost.

### Option A — inline in values.yaml (chart generates the ConfigMap)

```yaml
runbooks:
  inline:
    oom-runbook.md: |
      ## OOMKilled Runbook
      When a pod is OOMKilled:
      1. Check current memory requests: kubectl describe pod <name>
      2. Check usage trend in Grafana (namespace/pod memory panel)
      3. If usage consistently exceeds requests, bump resources.requests.memory
         and resources.limits.memory in the deployment manifest and git_push.
      4. If the spike is abnormal (memory leak), restart and open an investigation.

    deploy-runbook.md: |
      ## Deployment Runbook
      Standard deploy process:
      1. Update the image tag in charts/<app>/values.yaml
      2. git_commit + git_push to trigger Flux reconciliation
      3. Watch rollout: kubectl rollout status deployment/<name>
      4. Verify readiness probes pass before declaring success.
```

The chart creates a ConfigMap named `<release>-runbooks` and mounts it at `/etc/aiops/runbooks/`. Changing any entry triggers an automatic pod rollout via the `checksum/runbooks` annotation.

### Option B — bring your own ConfigMap

Manage the ConfigMap outside Helm (e.g. in a separate GitOps path, SOPS-encrypted, or via `kubectl apply`) and reference it by name:

```yaml
runbooks:
  existingConfigMap: aiops-runbooks
```

The referenced ConfigMap must have `*.md` keys:

```yaml
apiVersion: v1
kind: ConfigMap
metadata:
  name: aiops-runbooks
  namespace: aiops
data:
  oom-runbook.md: |
    ## OOMKilled Runbook
    When a pod is OOMKilled:
    1. Check current memory requests: kubectl describe pod <name>
    2. Check usage trend in Grafana (namespace/pod memory panel)
    3. If usage consistently exceeds requests, bump resources in the manifest and git_push.

  on-call-runbook.md: |
    ## On-call Contacts
    - Database issues: ping #db-oncall in Slack
    - Network/DNS: ping @netops
    - Payment service: escalate directly to payment-team lead
```

---

## Secret file stubs

Create these files and encrypt them with `sops --encrypt --in-place <file>` before committing.

**`aiops-telegram-token.yaml`**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: aiops-telegram-token
  namespace: aiops-agent
stringData:
  token: "1234567890:ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghi"
```

**`aiops-claude-api-key.yaml`**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: aiops-claude-api-key
  namespace: aiops-agent
stringData:
  api-key: "sk-ant-api03-..."
```

**`aiops-webhook-token.yaml`**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: aiops-webhook-token
  namespace: aiops-agent
stringData:
  token: "your-random-webhook-token"
```

**`aiops-ssh-keys.yaml`**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: aiops-ssh-keys
  namespace: aiops-agent
stringData:
  id_ed25519: |
    -----BEGIN OPENSSH PRIVATE KEY-----
    ...
    -----END OPENSSH PRIVATE KEY-----
```

**`aiops-github-token.yaml`**
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: aiops-github-token
  namespace: aiops-agent
stringData:
  token: "ghp_..."
```

**`aiops-ha-token.yaml`** (if using Home Assistant MCP)
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: aiops-ha-token
  namespace: aiops-agent
stringData:
  token: "eyJ..."
```

**`aiops-kubeconfig-simitli.yaml`** (one per remote cluster)
```yaml
apiVersion: v1
kind: Secret
metadata:
  name: aiops-kubeconfig-simitli
  namespace: aiops-agent
stringData:
  kubeconfig: |
    apiVersion: v1
    kind: Config
    clusters:
    - cluster:
        server: https://192.168.1.100:6443
        certificate-authority-data: ...
      name: simitli-k8s
    contexts:
    - context:
        cluster: simitli-k8s
        user: admin
      name: simitli-k8s
    current-context: simitli-k8s
    users:
    - name: admin
      user:
        client-certificate-data: ...
        client-key-data: ...
```

---

## GitOps workflow example

A typical conversation when the agent edits a Kubernetes manifest and pushes it:

```
User:    Bump the zigbee2mqtt image tag to 2.11.0 in the deployments repo and push.

Agent:   I'll read the current manifest, update the tag, commit, and push.
         [calls read_file: /repos/deployments/apps/zigbee2mqtt/helmrelease.yaml]
         [calls write_file — nonce: xK7mP2]

User:    xK7mP2

Agent:   File written. Committing...
         [calls git_commit — nonce: 9Qr4nT]

User:    9Qr4nT

Agent:   Committed as a3f8c12: "chore(z2m): update to 2.11.0"
         Pushing...
         [calls git_push — nonce: Lw2vX8]

User:    Lw2vX8

Agent:   Pushed to origin/main. Flux will reconcile shortly.
         Run /status or ask me to watch the HelmRelease if you want to confirm rollout.
```

---

## Webhook integration

The agent exposes an HTTP endpoint for external alert sources. Payloads are forwarded to the configured Telegram chat.

**Authentication:** include the `X-Webhook-Token` header matching your `aiops-webhook-token` secret.

**Endpoint:** `POST /webhook`

### Grafana alert

```bash
curl -X POST https://aiops.example.com/webhook \
  -H "X-Webhook-Token: your-random-webhook-token" \
  -H "Content-Type: application/json" \
  -d '{
    "title": "High CPU on node1",
    "message": "CPU usage at 94% for 10 minutes",
    "state": "alerting"
  }'
```

### Alertmanager webhook receiver

```yaml
# alertmanager.yaml
receivers:
  - name: aiops
    webhook_configs:
      - url: https://aiops.example.com/webhook
        http_config:
          headers:
            X-Webhook-Token: <token>
        send_resolved: true
```

### Flux notification provider

```yaml
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Provider
metadata:
  name: aiops-agent
  namespace: flux-system
spec:
  type: generic
  address: https://aiops.example.com/webhook
  secretRef:
    name: aiops-webhook-token   # must contain field "token" used as X-Webhook-Token
---
apiVersion: notification.toolkit.fluxcd.io/v1beta3
kind: Alert
metadata:
  name: aiops-alerts
  namespace: flux-system
spec:
  providerRef:
    name: aiops-agent
  eventSeverity: error
  eventSources:
    - kind: HelmRelease
      name: "*"
    - kind: Kustomization
      name: "*"
```

---

## Building locally

```bash
# Build the binary
go build ./cmd/aiops-agent

# Build the container image
docker build -t aiops-agent .
```

---

## Helm chart

```bash
helm install aiops-agent oci://ghcr.io/jimytar/charts/aiops-agent \
  --namespace aiops-agent --create-namespace \
  --version 0.1.0 -f my-values.yaml
```

To upgrade:

```bash
helm upgrade aiops-agent oci://ghcr.io/jimytar/charts/aiops-agent \
  --namespace aiops-agent \
  --version 0.2.0 -f my-values.yaml
```

To inspect available versions:

```bash
helm show chart oci://ghcr.io/jimytar/charts/aiops-agent
```
