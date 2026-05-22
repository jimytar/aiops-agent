package executor

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"github.com/jimytar/aiops-agent/internal/config"
)

type GitExecutor struct {
	repoDirs []string
}

func NewGitExecutor(cfg *config.Config) *GitExecutor {
	// Allow git to operate on directories owned by a different UID (e.g. cloned by init container).
	// HOME must be writable for git config --global; /tmp is always writable.
	cmd := exec.Command("git", "config", "--global", "--add", "safe.directory", "*")
	cmd.Env = append(cmd.Environ(), "HOME=/tmp")
	cmd.Run() //nolint:errcheck
	return &GitExecutor{repoDirs: cfg.GitRepoDirs}
}

func (e *GitExecutor) Status() (string, error) {
	if len(e.repoDirs) == 0 {
		return "No git repositories configured.", nil
	}

	var buf bytes.Buffer
	for _, dir := range e.repoDirs {
		fmt.Fprintf(&buf, "=== %s ===\n", dir)
		out, err := e.run(dir, "git", "status", "--short", "--branch")
		if err != nil {
			fmt.Fprintf(&buf, "error: %v\n", err)
		} else {
			buf.WriteString(out)
		}
		buf.WriteString("\n")
	}
	return buf.String(), nil
}

func (e *GitExecutor) Log(repoDir string, limit int) (string, error) {
	if limit <= 0 {
		limit = 10
	}
	if repoDir == "" && len(e.repoDirs) > 0 {
		repoDir = e.repoDirs[0]
	}
	if repoDir == "" {
		return "No git repositories configured.", nil
	}

	return e.run(repoDir, "git", "log", fmt.Sprintf("-%d", limit), "--oneline", "--decorate")
}

func (e *GitExecutor) Pull(repoDir string) (string, error) {
	if repoDir == "" && len(e.repoDirs) > 0 {
		repoDir = e.repoDirs[0]
	}
	if repoDir == "" {
		return "", fmt.Errorf("no git repositories configured")
	}
	return e.run(repoDir, "git", "pull", "--ff-only")
}

func (e *GitExecutor) Push(repoDir string) (string, error) {
	if repoDir == "" && len(e.repoDirs) > 0 {
		repoDir = e.repoDirs[0]
	}
	if repoDir == "" {
		return "", fmt.Errorf("no git repositories configured")
	}
	return e.run(repoDir, "git", "push", "origin", "HEAD")
}

func (e *GitExecutor) Diff(repoDir string) (string, error) {
	if repoDir == "" && len(e.repoDirs) > 0 {
		repoDir = e.repoDirs[0]
	}
	if repoDir == "" {
		return "No git repositories configured.", nil
	}
	// Show staged + unstaged diff relative to HEAD.
	out, err := e.run(repoDir, "git", "diff", "HEAD")
	if err != nil {
		return out, err
	}
	if out == "" {
		return "No changes relative to HEAD.", nil
	}
	return out, nil
}

func (e *GitExecutor) Commit(repoDir, message string) (string, error) {
	if repoDir == "" && len(e.repoDirs) > 0 {
		repoDir = e.repoDirs[0]
	}
	if repoDir == "" {
		return "", fmt.Errorf("no git repositories configured")
	}
	if _, err := e.run(repoDir, "git", "add", "-A"); err != nil {
		return "", err
	}
	return e.run(repoDir, "git", "commit", "-m", message)
}

// Tag creates an annotated git tag and pushes it to origin.
// This triggers CI release pipelines that depend on tag pushes.
func (e *GitExecutor) Tag(repoDir, tag, message string) (string, error) {
	if repoDir == "" && len(e.repoDirs) > 0 {
		repoDir = e.repoDirs[0]
	}
	if repoDir == "" {
		return "", fmt.Errorf("no git repositories configured")
	}
	if tag == "" {
		return "", fmt.Errorf("tag name is required")
	}
	if message == "" {
		message = tag
	}
	if _, err := e.run(repoDir, "git", "tag", "-a", tag, "-m", message); err != nil {
		return "", err
	}
	return e.run(repoDir, "git", "push", "origin", tag)
}

func (e *GitExecutor) RepoDirs() []string {
	return e.repoDirs
}

func (e *GitExecutor) run(dir string, args ...string) (string, error) {
	cmd := exec.Command(args[0], args[1:]...) //nolint:gosec // args are hardcoded, not user-supplied
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), "HOME=/tmp")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		errMsg := strings.TrimSpace(stderr.String())
		if errMsg == "" {
			errMsg = err.Error()
		}
		return stdout.String(), fmt.Errorf("%s: %s", strings.Join(args, " "), errMsg)
	}
	return stdout.String(), nil
}
