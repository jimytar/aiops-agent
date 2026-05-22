package agent

import (
	"strings"
	"testing"
)

func TestFmtTokens(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0"},
		{999, "999"},
		{1000, "1.0K"},
		{1500, "1.5K"},
		{999999, "1000.0K"},
		{1000000, "1.00M"},
		{2500000, "2.50M"},
	}
	for _, c := range cases {
		got := fmtTokens(c.n)
		if got != c.want {
			t.Errorf("fmtTokens(%d) = %q, want %q", c.n, got, c.want)
		}
	}
}

func TestPricingFor(t *testing.T) {
	cases := []struct {
		model string
		found bool
	}{
		{"claude-sonnet-4-6", true},
		{"claude-opus-4-7", true},
		{"claude-haiku-4-5", true},
		{"claude-sonnet-3-5", true},
		{"claude-haiku-3-0", true},
		{"gpt-4", false},
		{"", false},
	}
	for _, c := range cases {
		p, ok := pricingFor(c.model)
		if ok != c.found {
			t.Errorf("pricingFor(%q): found=%v, want=%v", c.model, ok, c.found)
		}
		if ok && p.input == 0 {
			t.Errorf("pricingFor(%q): input price is zero", c.model)
		}
	}
}

func TestUsageTrackerAdd(t *testing.T) {
	u := newUsageTracker()

	u.add(100, 50, 200, 10)
	u.add(300, 150, 0, 90)

	if got := u.inputTokens.Load(); got != 400 {
		t.Errorf("inputTokens = %d, want 400", got)
	}
	if got := u.outputTokens.Load(); got != 200 {
		t.Errorf("outputTokens = %d, want 200", got)
	}
	if got := u.cacheWriteTokens.Load(); got != 200 {
		t.Errorf("cacheWriteTokens = %d, want 200", got)
	}
	if got := u.cacheReadTokens.Load(); got != 100 {
		t.Errorf("cacheReadTokens = %d, want 100", got)
	}
	if got := u.apiCalls.Load(); got != 2 {
		t.Errorf("apiCalls = %d, want 2", got)
	}
}

func TestUsageTrackerReportKnownModel(t *testing.T) {
	u := newUsageTracker()
	u.add(1_000_000, 500_000, 200_000, 100_000)

	out := u.report("claude-sonnet-4-6")

	for _, want := range []string{
		"Token usage",
		"API calls",
		"Estimated cost",
		"claude-sonnet-4-6",
		"Total",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("report missing %q, got:\n%s", want, out)
		}
	}
}

func TestUsageTrackerReportUnknownModel(t *testing.T) {
	u := newUsageTracker()
	u.add(100, 50, 0, 0)

	out := u.report("gpt-99-turbo")

	if !strings.Contains(out, "Pricing not available") {
		t.Errorf("expected 'Pricing not available', got:\n%s", out)
	}
	if strings.Contains(out, "Total") {
		t.Errorf("should not show cost breakdown for unknown model, got:\n%s", out)
	}
}

func TestUsageTrackerReportCacheSavings(t *testing.T) {
	u := newUsageTracker()
	// Large cache read triggers the "Cache saved" line (saved > 0.0001).
	u.add(0, 0, 0, 10_000_000)

	out := u.report("claude-sonnet-4-6")

	if !strings.Contains(out, "Cache saved") {
		t.Errorf("expected cache savings line, got:\n%s", out)
	}
}

func TestUsageTrackerReportZeroState(t *testing.T) {
	u := newUsageTracker()
	out := u.report("claude-sonnet-4-6")

	if !strings.Contains(out, "API calls") {
		t.Errorf("report should still render headers with zero state, got:\n%s", out)
	}
}
