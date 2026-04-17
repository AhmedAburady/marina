package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/url"
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

	// Enforce HTTPS unless the target is a loopback address (local dev / test).
	u, err := url.Parse(cfg.URL)
	if err != nil {
		return fmt.Errorf("gotify url is invalid: %w", err)
	}
	if u.Scheme != "https" {
		host := u.Hostname()
		ip := net.ParseIP(host)
		isLoopback := host == "localhost" || (ip != nil && ip.IsLoopback())
		if !isLoopback {
			return fmt.Errorf("gotify url must use https (got %q); use https:// to protect your token in transit", cfg.URL)
		}
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

	endpoint := cfg.URL + "/message"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create gotify request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gotify-Key", cfg.Token)

	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) > 0 && via[0].URL.Scheme == "https" && req.URL.Scheme != "https" {
				return fmt.Errorf("gotify redirect from https to %s refused: possible token exfiltration", req.URL.Scheme)
			}
			return nil
		},
	}
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
