package agent

import (
	"errors"
	"strings"
	"testing"
)

// --- buildToolResultBlocks ---

func TestBuildToolResultBlocksTextOnly(t *testing.T) {
	out := toolOutput{Text: "hello world"}
	blocks := buildToolResultBlocks("id1", out, nil, 1024)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
}

func TestBuildToolResultBlocksWithError(t *testing.T) {
	out := toolOutput{Text: "partial"}
	blocks := buildToolResultBlocks("id1", out, errors.New("boom"), 1024)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block for error, got %d", len(blocks))
	}
	// The single block should be a tool_result with is_error=true.
	// We can't easily inspect the internal structure, but we can verify
	// it's non-nil and only one block was returned.
}

func TestBuildToolResultBlocksWithErrorNoText(t *testing.T) {
	out := toolOutput{}
	blocks := buildToolResultBlocks("id1", out, errors.New("something failed"), 1024)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
}

func TestBuildToolResultBlocksWithImage(t *testing.T) {
	out := toolOutput{
		Text:      "snapshot from cam1",
		ImageData: []byte{0xFF, 0xD8, 0xFF}, // fake JPEG bytes
		MediaType: "image/jpeg",
	}
	blocks := buildToolResultBlocks("id1", out, nil, 1024)
	// Expect 2 blocks: text tool_result + image block
	if len(blocks) != 2 {
		t.Fatalf("expected 2 blocks (tool_result + image), got %d", len(blocks))
	}
}

func TestBuildToolResultBlocksTruncatesLongText(t *testing.T) {
	long := strings.Repeat("x", 200)
	out := toolOutput{Text: long}
	blocks := buildToolResultBlocks("id1", out, nil, 100)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
	// truncation should have happened (no panic)
}

// --- buildBetaToolResultBlocks ---

func TestBuildBetaToolResultBlocksTextOnly(t *testing.T) {
	out := toolOutput{Text: "hello"}
	blocks := buildBetaToolResultBlocks("id1", out, nil, 1024)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block, got %d", len(blocks))
	}
}

func TestBuildBetaToolResultBlocksWithError(t *testing.T) {
	out := toolOutput{}
	blocks := buildBetaToolResultBlocks("id1", out, errors.New("err"), 1024)
	if len(blocks) != 1 {
		t.Fatalf("expected 1 block for error, got %d", len(blocks))
	}
}

func TestBuildBetaToolResultBlocksWithImage(t *testing.T) {
	out := toolOutput{
		Text:      "snapshot",
		ImageData: []byte{0xFF, 0xD8, 0xFF},
		MediaType: "image/jpeg",
	}
	blocks := buildBetaToolResultBlocks("id1", out, nil, 1024)
	// Beta path embeds image as data-URI in a single text block
	if len(blocks) != 1 {
		t.Fatalf("expected 1 beta block for image, got %d", len(blocks))
	}
}

// --- truncate ---

func TestTruncateShortString(t *testing.T) {
	s := truncate("hello", 100)
	if s != "hello" {
		t.Errorf("truncate short = %q, want %q", s, "hello")
	}
}

func TestTruncateLongString(t *testing.T) {
	s := truncate(strings.Repeat("a", 200), 100)
	if len(s) <= 100 {
		// truncated message adds overhead, so total may be slightly over 100 chars
		t.Errorf("truncated string len=%d, expected > 100 (with truncation notice)", len(s))
	}
	if !strings.Contains(s, "truncated") {
		t.Error("truncated string should contain 'truncated' notice")
	}
	if !strings.HasPrefix(s, strings.Repeat("a", 100)) {
		t.Error("truncated string should start with first 100 chars")
	}
}

func TestTruncateZeroMax(t *testing.T) {
	s := truncate("hello", 0)
	if s != "hello" {
		t.Errorf("truncate with max=0 should return original, got %q", s)
	}
}

func TestTruncateExactLength(t *testing.T) {
	s := truncate("hello", 5)
	if s != "hello" {
		t.Errorf("truncate exact length = %q, want %q", s, "hello")
	}
}
