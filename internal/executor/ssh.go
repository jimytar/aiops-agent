package executor

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/jimytar/aiops-agent/internal/config"
	"golang.org/x/crypto/ssh"
)

type SSHExecutor struct {
	cfg     *config.Config
	signers []ssh.Signer
}

func NewSSHExecutor(cfg *config.Config) (*SSHExecutor, error) {
	e := &SSHExecutor{cfg: cfg}

	entries, err := os.ReadDir(cfg.SSHKeyDir)
	if err != nil {
		if os.IsNotExist(err) {
			return e, nil
		}
		return nil, fmt.Errorf("read ssh key dir: %w", err)
	}

	keyFiles := 0
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		keyFiles++
		path := filepath.Join(cfg.SSHKeyDir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		signer, err := ssh.ParsePrivateKey(data)
		if err != nil {
			log.Printf("ssh: skipping %s: %v", entry.Name(), err)
			continue
		}
		e.signers = append(e.signers, signer)
	}

	if keyFiles > 0 && len(e.signers) == 0 {
		log.Printf("ssh: WARNING — %d key file(s) found in %s but none parsed successfully; SSH operations will fail", keyFiles, cfg.SSHKeyDir)
	}

	return e, nil
}

func (e *SSHExecutor) host(name string) (config.SSHHost, error) {
	h, ok := e.cfg.SSHHosts[name]
	if !ok {
		var known []string
		for k := range e.cfg.SSHHosts {
			known = append(known, k)
		}
		return config.SSHHost{}, fmt.Errorf("unknown host %q (known: %v)", name, known)
	}
	return h, nil
}

func (e *SSHExecutor) ExecReadonly(ctx context.Context, hostName, command string) (string, error) {
	if !e.isAllowed(command, e.cfg.SSHAllowedReadonly) {
		return "", fmt.Errorf("command not in readonly allowlist: %q", command)
	}
	return e.exec(ctx, hostName, command)
}

func (e *SSHExecutor) Exec(ctx context.Context, hostName, command string) (string, error) {
	allowed := append(e.cfg.SSHAllowedReadonly, e.cfg.SSHAllowedMutating...)
	if !e.isAllowed(command, allowed) {
		return "", fmt.Errorf("command not in allowlist: %q", command)
	}
	return e.exec(ctx, hostName, command)
}

func (e *SSHExecutor) exec(ctx context.Context, hostName, command string) (string, error) {
	h, err := e.host(hostName)
	if err != nil {
		return "", err
	}

	if len(e.signers) == 0 {
		return "", fmt.Errorf("no SSH keys loaded from %s", e.cfg.SSHKeyDir)
	}

	authMethods := make([]ssh.AuthMethod, len(e.signers))
	for i, s := range e.signers {
		authMethods[i] = ssh.PublicKeys(s)
	}

	sshCfg := &ssh.ClientConfig{
		User:            h.User,
		Auth:            authMethods,
		HostKeyCallback: ssh.InsecureIgnoreHostKey(), //nolint:gosec // homelab — known hosts not managed here
		Timeout:         30 * time.Second,
	}

	client, err := ssh.Dial("tcp", h.Addr(), sshCfg)
	if err != nil {
		return "", fmt.Errorf("ssh dial %s: %w", h.Addr(), err)
	}
	defer client.Close()

	session, err := client.NewSession()
	if err != nil {
		return "", fmt.Errorf("ssh session: %w", err)
	}
	defer session.Close()

	var stdout, stderr bytes.Buffer
	session.Stdout = &stdout
	session.Stderr = &stderr

	type result struct {
		err error
	}
	done := make(chan result, 1)
	go func() { done <- result{session.Run(command)} }()

	select {
	case r := <-done:
		out := stdout.String()
		if stderr.Len() > 0 {
			out += "\nSTDERR:\n" + stderr.String()
		}
		if r.err != nil {
			return out, fmt.Errorf("ssh exec on %s: %w", hostName, r.err)
		}
		return out, nil
	case <-ctx.Done():
		_ = session.Signal(ssh.SIGTERM)
		return "", fmt.Errorf("ssh exec on %s: %w", hostName, ctx.Err())
	}
}

func (e *SSHExecutor) isAllowed(command string, allowlist []string) bool {
	cmd := strings.TrimSpace(command)
	for _, prefix := range allowlist {
		if strings.HasPrefix(cmd, prefix) {
			return true
		}
	}
	return false
}

func (e *SSHExecutor) KnownHosts() []string {
	names := make([]string, 0, len(e.cfg.SSHHosts))
	for name := range e.cfg.SSHHosts {
		names = append(names, name)
	}
	return names
}
