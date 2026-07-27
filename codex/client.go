package codex

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/thorstenhirsch/clue/provider"
)

const defaultAPIURL = "https://chatgpt.com/backend-api/wham/usage"

type Client struct {
	apiURL string
	http   *http.Client
}

func NewClient() *Client {
	return &Client{apiURL: defaultAPIURL, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *Client) CheckCredentials() error {
	creds, err := LoadCredentials()
	if err != nil {
		return err
	}
	if !creds.ExpiresAt.IsZero() && time.Now().After(creds.ExpiresAt) {
		return provider.ErrAuth
	}
	return nil
}

func (c *Client) FetchUsage() (*provider.Usage, error) {
	creds, err := LoadCredentials()
	if err != nil {
		return nil, err
	}
	if !creds.ExpiresAt.IsZero() && time.Now().After(creds.ExpiresAt) {
		return nil, provider.ErrAuth
	}
	req, err := http.NewRequest(http.MethodGet, c.apiURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+creds.AccessToken)
	if creds.AccountID != "" {
		req.Header.Set("ChatGPT-Account-Id", creds.AccountID)
	}
	req.Header.Set("User-Agent", "clue/1.0")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		io.Copy(io.Discard, resp.Body)
		return nil, provider.ErrAuth
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		io.Copy(io.Discard, resp.Body)
		return nil, fmt.Errorf("Codex usage API returned HTTP %d", resp.StatusCode)
	}

	var payload usagePayload
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return nil, fmt.Errorf("decoding Codex usage: %w", err)
	}
	if payload.RateLimit == nil {
		return nil, fmt.Errorf("Codex usage response has no rate_limit")
	}
	return &provider.Usage{
		Primary:   mapWindow(payload.RateLimit.PrimaryWindow),
		Secondary: mapWindow(payload.RateLimit.SecondaryWindow),
	}, nil
}

type usagePayload struct {
	RateLimit *rateLimitDetails `json:"rate_limit"`
}

type rateLimitDetails struct {
	PrimaryWindow   *windowSnapshot `json:"primary_window"`
	SecondaryWindow *windowSnapshot `json:"secondary_window"`
}

type windowSnapshot struct {
	UsedPercent     float64 `json:"used_percent"`
	LimitWindowSecs int64   `json:"limit_window_seconds"`
	ResetAt         int64   `json:"reset_at"`
}

func mapWindow(w *windowSnapshot) provider.Window {
	if w == nil {
		return provider.Window{}
	}
	used := int64(w.UsedPercent + 0.5)
	if used < 0 {
		used = 0
	}
	if used > 100 {
		used = 100
	}
	window := provider.Window{Used: used, Limit: 100}
	if w.LimitWindowSecs > 0 {
		window.DurationMins = (w.LimitWindowSecs + 59) / 60
	}
	if w.ResetAt > 0 {
		window.Reset = time.Unix(w.ResetAt, 0)
	}
	return window
}
