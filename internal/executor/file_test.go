package executor

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newFileExec(t *testing.T) (*FileExecutor, string) {
	t.Helper()
	dir := t.TempDir()
	return NewFileExecutor([]string{dir}), dir
}

// --- checkPath ---

func TestCheckPathAllowed(t *testing.T) {
	e, dir := newFileExec(t)
	if err := e.checkPath(filepath.Join(dir, "subdir", "file.txt")); err != nil {
		t.Errorf("checkPath allowed path: %v", err)
	}
}

func TestCheckPathAllowedRoot(t *testing.T) {
	e, dir := newFileExec(t)
	if err := e.checkPath(dir); err != nil {
		t.Errorf("checkPath root of allowed dir: %v", err)
	}
}

func TestCheckPathTraversal(t *testing.T) {
	e, dir := newFileExec(t)
	outside := filepath.Join(dir, "..", "outside.txt")
	if err := e.checkPath(outside); err == nil {
		t.Error("checkPath should reject path traversal outside allowed dir")
	}
}

func TestCheckPathUnrelated(t *testing.T) {
	e, _ := newFileExec(t)
	if err := e.checkPath("/etc/passwd"); err == nil {
		t.Error("checkPath should reject /etc/passwd")
	}
}

func TestCheckPathMultipleDirs(t *testing.T) {
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	e := NewFileExecutor([]string{dir1, dir2})

	if err := e.checkPath(filepath.Join(dir2, "file.txt")); err != nil {
		t.Errorf("checkPath with multiple allowed dirs: %v", err)
	}
}

// --- ListFiles ---

func TestListFilesBasic(t *testing.T) {
	e, dir := newFileExec(t)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "b.txt"), []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := e.ListFiles(dir, 3)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "b.txt") {
		t.Errorf("ListFiles output missing files:\n%s", out)
	}
}

func TestListFilesExcludesGit(t *testing.T) {
	e, dir := newFileExec(t)
	if err := os.Mkdir(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".git", "HEAD"), []byte("ref: refs/heads/main"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package main"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := e.ListFiles(dir, 3)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if strings.Contains(out, ".git") {
		t.Errorf("ListFiles should not include .git directory:\n%s", out)
	}
	if !strings.Contains(out, "app.go") {
		t.Errorf("ListFiles missing app.go:\n%s", out)
	}
}

func TestListFilesDepthLimit(t *testing.T) {
	e, dir := newFileExec(t)
	deep := filepath.Join(dir, "a", "b", "c", "d")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(deep, "deep.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	out, err := e.ListFiles(dir, 2)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if strings.Contains(out, "deep.txt") {
		t.Errorf("ListFiles should not show file beyond maxDepth=2:\n%s", out)
	}
}

func TestListFilesEmptyDir(t *testing.T) {
	e, dir := newFileExec(t)
	out, err := e.ListFiles(dir, 3)
	if err != nil {
		t.Fatalf("ListFiles empty dir: %v", err)
	}
	if out != "Empty directory." {
		t.Errorf("ListFiles empty = %q", out)
	}
}

func TestListFilesOutsideDir(t *testing.T) {
	e, _ := newFileExec(t)
	_, err := e.ListFiles("/tmp", 3)
	if err == nil {
		t.Error("ListFiles outside allowed dir should error")
	}
}

func TestListFilesDefaultDepth(t *testing.T) {
	e, dir := newFileExec(t)
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// maxDepth=0 should use default of 3
	out, err := e.ListFiles(dir, 0)
	if err != nil {
		t.Fatalf("ListFiles: %v", err)
	}
	if !strings.Contains(out, "f.txt") {
		t.Errorf("ListFiles with default depth missing f.txt:\n%s", out)
	}
}

// --- ReadFile ---

func TestReadFile(t *testing.T) {
	e, dir := newFileExec(t)
	path := filepath.Join(dir, "hello.txt")
	if err := os.WriteFile(path, []byte("hello world"), 0o644); err != nil {
		t.Fatal(err)
	}

	content, err := e.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if content != "hello world" {
		t.Errorf("ReadFile content = %q", content)
	}
}

func TestReadFileMissing(t *testing.T) {
	e, dir := newFileExec(t)
	_, err := e.ReadFile(filepath.Join(dir, "nonexistent.txt"))
	if err == nil {
		t.Error("ReadFile missing file should error")
	}
}

func TestReadFileOutsideDir(t *testing.T) {
	e, _ := newFileExec(t)
	_, err := e.ReadFile("/etc/passwd")
	if err == nil {
		t.Error("ReadFile outside allowed dir should error")
	}
}

// --- WriteFile ---

func TestWriteFile(t *testing.T) {
	e, dir := newFileExec(t)
	path := filepath.Join(dir, "out.txt")

	result, err := e.WriteFile(path, "hello")
	if err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if !strings.Contains(result, "5") {
		t.Errorf("WriteFile result should mention byte count, got %q", result)
	}

	data, _ := os.ReadFile(path)
	if string(data) != "hello" {
		t.Errorf("Written content = %q", string(data))
	}
}

func TestWriteFileCreatesSubdirs(t *testing.T) {
	e, dir := newFileExec(t)
	path := filepath.Join(dir, "subdir", "nested", "file.txt")

	if _, err := e.WriteFile(path, "content"); err != nil {
		t.Fatalf("WriteFile with subdirs: %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("file not created: %v", err)
	}
}

func TestWriteFileOutsideDir(t *testing.T) {
	e, _ := newFileExec(t)
	_, err := e.WriteFile("/tmp/evil.txt", "bad")
	if err == nil {
		t.Error("WriteFile outside allowed dir should error")
	}
}

func TestWriteFileOverwrite(t *testing.T) {
	e, dir := newFileExec(t)
	path := filepath.Join(dir, "file.txt")
	if _, err := e.WriteFile(path, "first"); err != nil {
		t.Fatal(err)
	}
	if _, err := e.WriteFile(path, "second"); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if string(data) != "second" {
		t.Errorf("file content after overwrite = %q", string(data))
	}
}
