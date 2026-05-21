package executor

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// FileExecutor handles file read/write operations restricted to allowed directories.
type FileExecutor struct {
	allowedDirs []string
}

func NewFileExecutor(allowedDirs []string) *FileExecutor {
	return &FileExecutor{allowedDirs: allowedDirs}
}

// checkPath returns an error if path is outside all allowedDirs (path traversal guard).
func (e *FileExecutor) checkPath(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}
	for _, dir := range e.allowedDirs {
		allowed, err := filepath.Abs(dir)
		if err != nil {
			continue
		}
		if abs == allowed || strings.HasPrefix(abs, allowed+string(filepath.Separator)) {
			return nil
		}
	}
	return fmt.Errorf("path %q is outside allowed git repo directories", path)
}

// ListFiles returns a directory listing up to maxDepth levels deep, excluding .git.
func (e *FileExecutor) ListFiles(dir string, maxDepth int) (string, error) {
	if err := e.checkPath(dir); err != nil {
		return "", err
	}
	if maxDepth <= 0 {
		maxDepth = 3
	}

	var lines []string
	baseDepth := strings.Count(filepath.Clean(dir), string(filepath.Separator))

	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		rel, _ := filepath.Rel(dir, path)
		if rel == "." {
			return nil
		}
		// Skip .git directory entirely.
		if info.IsDir() && info.Name() == ".git" {
			return filepath.SkipDir
		}
		depth := strings.Count(filepath.Clean(path), string(filepath.Separator)) - baseDepth
		if depth > maxDepth {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		indent := strings.Repeat("  ", depth-1)
		if info.IsDir() {
			lines = append(lines, indent+rel+"/")
		} else {
			lines = append(lines, indent+rel)
		}
		return nil
	})
	if err != nil {
		return "", fmt.Errorf("walk %s: %w", dir, err)
	}
	if len(lines) == 0 {
		return "Empty directory.", nil
	}
	return strings.Join(lines, "\n"), nil
}

// ReadFile returns the contents of a file.
func (e *FileExecutor) ReadFile(path string) (string, error) {
	if err := e.checkPath(path); err != nil {
		return "", err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	return string(data), nil
}

// WriteFile writes content to path, creating parent directories as needed.
func (e *FileExecutor) WriteFile(path, content string) (string, error) {
	if err := e.checkPath(path); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("mkdir: %w", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return fmt.Sprintf("Wrote %d bytes to %s.", len(content), path), nil
}
