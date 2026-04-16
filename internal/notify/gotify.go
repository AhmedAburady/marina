package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

// GotifyConfig holds Gotify connection settings.
type GotifyConfig struct {
	URL      string
	Token    string
	Priority int
}

// SendGotify posts a push notification to a Gotify server.
func SendGotify(ctx context.Context, cfg GotifyConfig, title, message string) error {
	if cfg.URL == "" || cfg.Token == "" {
		return fmt.Errorf("gotify not configured: url and token are required")
	}

	payload := map[string]any{
		"title":    title,
		"message":  message,
		"priority": cfg.Priority,
	}

	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("marshal gotify payload: %w", err)
	}

	url := cfg.URL + "/message"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create gotify request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gotify-Key", cfg.Token)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("send gotify notification: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("gotify responded with status %d", resp.StatusCode)
	}

	return nil
}
