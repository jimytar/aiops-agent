package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRunbooksEmpty(t *testing.T) {
	dir := t.TempDir()
	got := loadRunbooks(dir)
	if got != "" {
		t.Errorf("expected empty string for empty dir, got: %q", got)
	}
}

func TestLoadRunbooksNonexistentDir(t *testing.T) {
	got := loadRunbooks("/nonexistent/path/xyz")
	if got != "" {
		t.Errorf("expected empty string for nonexistent dir, got: %q", got)
	}
}

func TestLoadRunbooksSkipsNonMarkdown(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("plain text"), 0644)
	os.WriteFile(filepath.Join(dir, "script.sh"), []byte("#!/bin/bash"), 0644)

	got := loadRunbooks(dir)
	if got != "" {
		t.Errorf("expected empty string (no .md files), got: %q", got)
	}
}

func TestLoadRunbooksSkipsSubdirectories(t *testing.T) {
	dir := t.TempDir()
	subdir := filepath.Join(dir, "subdir")
	os.Mkdir(subdir, 0755)

	got := loadRunbooks(dir)
	if got != "" {
		t.Errorf("expected empty string (only subdirectory present), got: %q", got)
	}
}

func TestLoadRunbooksSingleFile(t *testing.T) {
	dir := t.TempDir()
	content := "## OOMKilled Runbook\nWhen a pod is OOMKilled, check memory limits."
	os.WriteFile(filepath.Join(dir, "oom.md"), []byte(content), 0644)

	got := loadRunbooks(dir)
	if !strings.Contains(got, "RUNBOOKS AND OPERATIONAL KNOWLEDGE") {
		t.Errorf("expected header section, got:\n%s", got)
	}
	if !strings.Contains(got, "OOMKilled") {
		t.Errorf("expected runbook content, got:\n%s", got)
	}
}

func TestLoadRunbooksMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "oom.md"), []byte("## OOM Runbook\nCheck memory."), 0644)
	os.WriteFile(filepath.Join(dir, "deploy.md"), []byte("## Deploy Runbook\nRun helm upgrade."), 0644)

	got := loadRunbooks(dir)
	if !strings.Contains(got, "OOM Runbook") {
		t.Errorf("missing first runbook, got:\n%s", got)
	}
	if !strings.Contains(got, "Deploy Runbook") {
		t.Errorf("missing second runbook, got:\n%s", got)
	}
	// Header should appear exactly once.
	count := strings.Count(got, "RUNBOOKS AND OPERATIONAL KNOWLEDGE")
	if count != 1 {
		t.Errorf("expected header to appear once, got %d occurrences", count)
	}
}

func TestLoadRunbooksMixedFiles(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "runbook.md"), []byte("## My Runbook"), 0644)
	os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("ignore me"), 0644)
	os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("key: value"), 0644)

	got := loadRunbooks(dir)
	if !strings.Contains(got, "My Runbook") {
		t.Errorf("expected markdown content, got:\n%s", got)
	}
	if strings.Contains(got, "ignore me") {
		t.Errorf("should not include .txt file content, got:\n%s", got)
	}
}
