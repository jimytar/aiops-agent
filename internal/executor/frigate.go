package executor

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type FrigateExecutor struct {
	baseURL string
	client  *http.Client
}

func NewFrigateExecutor(baseURL string) *FrigateExecutor {
	return &FrigateExecutor{
		baseURL: strings.TrimRight(baseURL, "/"),
		client:  &http.Client{Timeout: 15 * time.Second},
	}
}

// Cameras returns a list of configured camera names.
func (e *FrigateExecutor) Cameras() (string, error) {
	var cfg struct {
		Cameras map[string]interface{} `json:"cameras"`
	}
	if err := e.getJSON("/api/config", &cfg); err != nil {
		return "", err
	}
	if len(cfg.Cameras) == 0 {
		return "No cameras configured.", nil
	}
	names := make([]string, 0, len(cfg.Cameras))
	for name := range cfg.Cameras {
		names = append(names, name)
	}
	return "Cameras: " + strings.Join(names, ", "), nil
}

// Snapshot fetches the latest JPEG frame for the given camera, resized to 720p
// height to keep token count manageable for Claude vision (~1280 tokens vs 200k+
// for a full-resolution frame embedded as text).
func (e *FrigateExecutor) Snapshot(camera string) ([]byte, string, error) {
	url := fmt.Sprintf("%s/api/%s/latest.jpg?h=720", e.baseURL, camera)
	resp, err := e.client.Get(url)
	if err != nil {
		return nil, "", fmt.Errorf("fetch snapshot: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("frigate returned %d for camera %q", resp.StatusCode, camera)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5 MB max
	if err != nil {
		return nil, "", fmt.Errorf("read snapshot: %w", err)
	}
	return data, "image/jpeg", nil
}

type frigateEvent struct {
	ID        string  `json:"id"`
	Camera    string  `json:"camera"`
	Label     string  `json:"label"`
	Score     float64 `json:"score"`
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
	HasClip   bool    `json:"has_clip"`
	HasSnap   bool    `json:"has_snapshot"`
}

// Events returns recent detection events, optionally filtered by camera and label.
func (e *FrigateExecutor) Events(camera, label string, limit int) (string, error) {
	if limit <= 0 {
		limit = 10
	}
	u := fmt.Sprintf("%s/api/events?limit=%d", e.baseURL, limit)
	if camera != "" {
		u += "&camera=" + url.QueryEscape(camera)
	}
	if label != "" {
		u += "&label=" + url.QueryEscape(label)
	}

	var events []frigateEvent
	if err := e.getJSON(u, &events); err != nil {
		return "", err
	}
	if len(events) == 0 {
		return "No events found.", nil
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("Recent events (%d):\n", len(events)))
	for _, ev := range events {
		t := time.Unix(int64(ev.StartTime), 0).UTC().Format("2006-01-02 15:04:05 UTC")
		clip := ""
		if ev.HasClip {
			clip = " [clip]"
		}
		sb.WriteString(fmt.Sprintf("- %s | %s | %s (%.0f%%)%s\n",
			t, ev.Camera, ev.Label, ev.Score*100, clip))
	}
	return sb.String(), nil
}

func (e *FrigateExecutor) getJSON(url string, dst interface{}) error {
	if !strings.HasPrefix(url, "http") {
		url = e.baseURL + url
	}
	resp, err := e.client.Get(url)
	if err != nil {
		return fmt.Errorf("frigate request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("frigate returned %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(dst)
}
