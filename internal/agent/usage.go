package agent

import (
	"fmt"
	"strings"
	"sync/atomic"
	"time"
)

// modelPricing holds per-MTok prices in USD for a Claude model.
type modelPricing struct {
	input       float64
	output      float64
	cacheWrite  float64
	cacheRead   float64
}

// knownPricing maps model ID prefixes to pricing. Prices are per million tokens.
var knownPricing = map[string]modelPricing{
	"claude-opus-4":    {input: 15.00, output: 75.00, cacheWrite: 18.75, cacheRead: 1.50},
	"claude-sonnet-4":  {input: 3.00, output: 15.00, cacheWrite: 3.75, cacheRead: 0.30},
	"claude-haiku-4":   {input: 0.80, output: 4.00, cacheWrite: 1.00, cacheRead: 0.08},
	"claude-sonnet-3":  {input: 3.00, output: 15.00, cacheWrite: 3.75, cacheRead: 0.30},
	"claude-haiku-3":   {input: 0.25, output: 1.25, cacheWrite: 0.30, cacheRead: 0.03},
}

func pricingFor(model string) (modelPricing, bool) {
	for prefix, p := range knownPricing {
		if strings.HasPrefix(model, prefix) {
			return p, true
		}
	}
	return modelPricing{}, false
}

// UsageTracker accumulates token counts across the process lifetime using
// lock-free atomics. All fields are int64 token counts.
type UsageTracker struct {
	inputTokens       atomic.Int64
	outputTokens      atomic.Int64
	cacheWriteTokens  atomic.Int64
	cacheReadTokens   atomic.Int64
	apiCalls          atomic.Int64
	startTime         time.Time
}

func newUsageTracker() *UsageTracker {
	return &UsageTracker{startTime: time.Now()}
}

func (u *UsageTracker) add(input, output, cacheWrite, cacheRead int64) {
	u.inputTokens.Add(input)
	u.outputTokens.Add(output)
	u.cacheWriteTokens.Add(cacheWrite)
	u.cacheReadTokens.Add(cacheRead)
	u.apiCalls.Add(1)
}

func (u *UsageTracker) report(model string) string {
	input := u.inputTokens.Load()
	output := u.outputTokens.Load()
	cacheWrite := u.cacheWriteTokens.Load()
	cacheRead := u.cacheReadTokens.Load()
	calls := u.apiCalls.Load()
	uptime := time.Since(u.startTime).Round(time.Minute)

	var buf strings.Builder
	fmt.Fprintf(&buf, "*Token usage* (since startup, %s ago)\n\n", uptime)
	fmt.Fprintf(&buf, "API calls:         `%d`\n", calls)
	fmt.Fprintf(&buf, "Input tokens:      `%s`\n", fmtTokens(input))
	fmt.Fprintf(&buf, "Output tokens:     `%s`\n", fmtTokens(output))
	fmt.Fprintf(&buf, "Cache write:       `%s`\n", fmtTokens(cacheWrite))
	fmt.Fprintf(&buf, "Cache read:        `%s`\n", fmtTokens(cacheRead))

	p, known := pricingFor(model)
	if known {
		inputCost := float64(input) / 1_000_000 * p.input
		outputCost := float64(output) / 1_000_000 * p.output
		writeCost := float64(cacheWrite) / 1_000_000 * p.cacheWrite
		readCost := float64(cacheRead) / 1_000_000 * p.cacheRead
		total := inputCost + outputCost + writeCost + readCost

		// Estimated cost without caching for comparison.
		noCacheCost := float64(input+cacheWrite+cacheRead) / 1_000_000 * p.input
		noCacheCost += float64(output) / 1_000_000 * p.output
		saved := noCacheCost - total

		fmt.Fprintf(&buf, "\n*Estimated cost* (`%s`)\n\n", model)
		fmt.Fprintf(&buf, "Input:             `$%.4f`\n", inputCost)
		fmt.Fprintf(&buf, "Output:            `$%.4f`\n", outputCost)
		fmt.Fprintf(&buf, "Cache write:       `$%.4f`\n", writeCost)
		fmt.Fprintf(&buf, "Cache read:        `$%.4f`\n", readCost)
		fmt.Fprintf(&buf, "**Total:           `$%.4f`**\n", total)
		if saved > 0.0001 {
			fmt.Fprintf(&buf, "Cache saved:       `$%.4f`\n", saved)
		}
	} else {
		fmt.Fprintf(&buf, "\n_Pricing not available for model `%s`_\n", model)
	}

	return buf.String()
}

func fmtTokens(n int64) string {
	if n >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.1fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}
