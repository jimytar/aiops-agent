package executor

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jimytar/aiops-agent/internal/config"
)

func hasGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not found in PATH")
	}
}

func makeTempRepo(t *testing.T) string {
	t.Helper()
	hasGit(t)
	dir := t.TempDir()
	runGitCmd(t, dir, "git", "init")
	runGitCmd(t, dir, "git", "config", "user.email", "test@example.com")
	runGitCmd(t, dir, "git", "config", "user.name", "Test User")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("hello"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCmd(t, dir, "git", "add", ".")
	runGitCmd(t, dir, "git", "commit", "-m", "init")
	return dir
}

func runGitCmd(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cmd %v: %v\n%s", args, err, out)
	}
}

func newGitExec(repoDirs []string) *GitExecutor {
	return &GitExecutor{repoDirs: repoDirs}
}

// --- No repos configured ---

func TestGitStatusNoRepos(t *testing.T) {
	e := newGitExec(nil)
	out, err := e.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(out, "No git repositories") {
		t.Errorf("Status with no repos = %q", out)
	}
}

func TestGitLogNoRepos(t *testing.T) {
	e := newGitExec(nil)
	out, err := e.Log("", 10)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if !strings.Contains(out, "No git repositories") {
		t.Errorf("Log with no repos = %q", out)
	}
}

func TestGitDiffNoRepos(t *testing.T) {
	e := newGitExec(nil)
	out, err := e.Diff("")
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(out, "No git repositories") {
		t.Errorf("Diff with no repos = %q", out)
	}
}

func TestGitPullNoRepos(t *testing.T) {
	e := newGitExec(nil)
	_, err := e.Pull("")
	if err == nil {
		t.Error("Pull with no repos should error")
	}
}

func TestGitPushNoRepos(t *testing.T) {
	e := newGitExec(nil)
	_, err := e.Push("")
	if err == nil {
		t.Error("Push with no repos should error")
	}
}

func TestGitCommitNoRepos(t *testing.T) {
	e := newGitExec(nil)
	_, err := e.Commit("", "test message")
	if err == nil {
		t.Error("Commit with no repos should error")
	}
}

// --- With a real temp repo ---

func TestGitStatus(t *testing.T) {
	hasGit(t)
	dir := makeTempRepo(t)
	e := newGitExec([]string{dir})

	out, err := e.Status()
	if err != nil {
		t.Fatalf("Status: %v", err)
	}
	if !strings.Contains(out, dir) {
		t.Errorf("Status should include repo dir in header, got:\n%s", out)
	}
}

func TestGitLog(t *testing.T) {
	hasGit(t)
	dir := makeTempRepo(t)
	e := newGitExec([]string{dir})

	out, err := e.Log(dir, 5)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	if !strings.Contains(out, "init") {
		t.Errorf("Log should contain initial commit message, got:\n%s", out)
	}
}

func TestGitLogDefaultsToFirstRepo(t *testing.T) {
	hasGit(t)
	dir := makeTempRepo(t)
	e := newGitExec([]string{dir})

	out, err := e.Log("", 0) // empty dir → first repo, limit 0 → default 10
	if err != nil {
		t.Fatalf("Log with empty dir: %v", err)
	}
	if out == "" {
		t.Error("Log should return non-empty output")
	}
}

func TestGitDiffNoChanges(t *testing.T) {
	hasGit(t)
	dir := makeTempRepo(t)
	e := newGitExec([]string{dir})

	out, err := e.Diff(dir)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(out, "No changes") {
		t.Errorf("Diff with no changes = %q", out)
	}
}

func TestGitDiffWithChanges(t *testing.T) {
	hasGit(t)
	dir := makeTempRepo(t)
	e := newGitExec([]string{dir})

	// Make an unstaged change.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := e.Diff(dir)
	if err != nil {
		t.Fatalf("Diff: %v", err)
	}
	if !strings.Contains(out, "changed") && !strings.Contains(out, "diff") {
		t.Errorf("Diff should show changes, got:\n%s", out)
	}
}

func TestGitCommitAndStatus(t *testing.T) {
	hasGit(t)
	dir := makeTempRepo(t)
	e := newGitExec([]string{dir})

	if err := os.WriteFile(filepath.Join(dir, "new.txt"), []byte("new file"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := e.Commit(dir, "add new file")
	if err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if out == "" {
		t.Error("Commit should return output")
	}

	// After commit, diff should show no changes.
	diff, err := e.Diff(dir)
	if err != nil {
		t.Fatalf("Diff after commit: %v", err)
	}
	if !strings.Contains(diff, "No changes") {
		t.Errorf("After commit, diff should be clean, got:\n%s", diff)
	}
}

func TestRepoDirs(t *testing.T) {
	dirs := []string{"/a", "/b"}
	e := newGitExec(dirs)
	got := e.RepoDirs()
	if len(got) != 2 || got[0] != "/a" || got[1] != "/b" {
		t.Errorf("RepoDirs = %v", got)
	}
}

func TestNewGitExecutor(t *testing.T) {
	cfg := &config.Config{GitRepoDirs: []string{"/repo1", "/repo2"}}
	e := NewGitExecutor(cfg)
	if len(e.repoDirs) != 2 {
		t.Errorf("NewGitExecutor repoDirs = %v", e.repoDirs)
	}
}
