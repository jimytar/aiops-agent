package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/jimytar/aiops-agent/internal/config"
)

// HTTPIntegrationExecutor executes calls to configured REST API integrations.
type HTTPIntegrationExecutor struct {
	integrations map[string]*resolvedIntegration
	client       *http.Client
}

type resolvedIntegration struct {
	cfg      config.HTTPIntegrationConfig
	apiKey   string
	keyHeader string
	endpoints map[string]config.HTTPEndpointConfig
}

func NewHTTPIntegrationExecutor(cfgs []config.HTTPIntegrationConfig) *HTTPIntegrationExecutor {
	e := &HTTPIntegrationExecutor{
		integrations: make(map[string]*resolvedIntegration, len(cfgs)),
		client:       &http.Client{Timeout: 30 * time.Second},
	}
	for _, cfg := range cfgs {
		ri := &resolvedIntegration{
			cfg:       cfg,
			apiKey:    os.Getenv(cfg.APIKeyEnv),
			keyHeader: cfg.APIKeyHeader,
			endpoints: make(map[string]config.HTTPEndpointConfig, len(cfg.Endpoints)),
		}
		if ri.keyHeader == "" {
			ri.keyHeader = "X-Api-Key"
		}
		for _, ep := range cfg.Endpoints {
			ri.endpoints[ep.Name] = ep
		}
		e.integrations[cfg.Name] = ri
	}
	return e
}

// Call executes a named integration endpoint.
// queryParams is merged into the URL for GET requests (as a JSON object string).
// body is the JSON body for POST/PUT requests; integration defaults are merged in.
func (e *HTTPIntegrationExecutor) Call(ctx context.Context, integration, endpoint, queryParams, body string, maxBytes int) (string, error) {
	ri, ok := e.integrations[integration]
	if !ok {
		return "", fmt.Errorf("unknown integration %q", integration)
	}
	ep, ok := ri.endpoints[endpoint]
	if !ok {
		return "", fmt.Errorf("unknown endpoint %q for integration %q", endpoint, integration)
	}

	method := strings.ToUpper(ep.Method)
	if method == "" {
		method = http.MethodGet
	}

	baseURL := strings.TrimRight(ri.cfg.BaseURL, "/")
	urlStr := baseURL + ep.Path

	// Merge query params for GET requests.
	var reqBody io.Reader
	if method == http.MethodGet || method == http.MethodDelete {
		if queryParams != "" {
			var params map[string]interface{}
			if err := json.Unmarshal([]byte(queryParams), &params); err == nil {
				sep := "?"
				if strings.Contains(urlStr, "?") {
					sep = "&"
				}
				for k, v := range params {
					urlStr += fmt.Sprintf("%s%s=%v", sep, k, v)
					sep = "&"
				}
			}
		}
	} else {
		// Merge defaults with provided body for POST/PUT.
		merged := make(map[string]interface{})
		for k, v := range ri.cfg.Defaults {
			merged[k] = v
		}
		if body != "" {
			var userBody map[string]interface{}
			if err := json.Unmarshal([]byte(body), &userBody); err != nil {
				return "", fmt.Errorf("invalid JSON body: %w", err)
			}
			// User-supplied values override defaults.
			for k, v := range userBody {
				merged[k] = v
			}
		}
		encoded, err := json.Marshal(merged)
		if err != nil {
			return "", fmt.Errorf("encode body: %w", err)
		}
		reqBody = bytes.NewReader(encoded)
	}

	req, err := http.NewRequestWithContext(ctx, method, urlStr, reqBody)
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set(ri.keyHeader, ri.apiKey)
	if reqBody != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	req.Header.Set("Accept", "application/json")

	resp, err := e.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("%s %s: %w", method, urlStr, err)
	}
	defer resp.Body.Close()

	limit := maxBytes
	if limit <= 0 {
		limit = 32768
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)))
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 400 {
		return "", fmt.Errorf("%s %s returned HTTP %d: %s", method, ep.Path, resp.StatusCode, string(raw))
	}

	// Pretty-print JSON if possible.
	var pretty bytes.Buffer
	if json.Indent(&pretty, raw, "", "  ") == nil {
		return pretty.String(), nil
	}
	return string(raw), nil
}

// Integrations returns the list of configured integration names (for logging).
func (e *HTTPIntegrationExecutor) Integrations() []string {
	names := make([]string, 0, len(e.integrations))
	for n := range e.integrations {
		names = append(names, n)
	}
	return names
}
