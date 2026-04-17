package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"
)

// GotifyConfig holds Gotify connection settings.
type GotifyConfig struct {
	URL string
	// Token is the Gotify app token in plaintext.
	// Prefer TokenEnv to keep the secret out of config files.
	Token string
	// TokenEnv names an environment variable holding the Gotify app token.
	// When set and Token is empty, the token is read from os.Getenv(TokenEnv).
	TokenEnv string
	Priority int
}

// SendGotify posts a push notification to a Gotify server.
func SendGotify(ctx context.Context, cfg GotifyConfig, title, message string) error {
	// Resolve token: plaintext wins; fall back to env var.
	token := cfg.Token
	if token == "" && cfg.TokenEnv != "" {
		token = os.Getenv(cfg.TokenEnv)
		if token == "" {
			return fmt.Errorf("gotify not configured: token_env %q is set but the environment variable is empty", cfg.TokenEnv)
		}
	}

	if cfg.URL == "" || token == "" {
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

	endpoint := cfg.URL + "/message"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create gotify request: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Gotify-Key", token)

	// POST /message should never redirect in practice; following one would
	// forward the X-Gotify-Key header (a custom header Go does not strip on
	// cross-origin redirects) to whatever target Location points at. Refuse
	// all redirects unconditionally so the token cannot be exfiltrated via
	// a compromised TLS proxy, vanity-domain shim, or DNS hijack.
	client := &http.Client{
		Timeout: 10 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
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
