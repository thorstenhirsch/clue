package claude

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/thorstenhirsch/clue/provider"
)

const defaultAPIURL = "https://api.anthropic.com"

type Client struct {
	accessToken string
	apiURL      string
	http        *http.Client
}

func NewClient(accessToken string) *Client {
	return &Client{
		accessToken: accessToken,
		apiURL:      defaultAPIURL,
		http:        &http.Client{Timeout: 30 * time.Second},
	}
}

const probeBody = `{"model":"claude-haiku-4-5-20251001","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`

func (c *Client) FetchUsage() (*provider.Usage, error) {
	req, err := http.NewRequest("POST", c.apiURL+"/v1/messages", strings.NewReader(probeBody))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.accessToken)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "clue/1.0")

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, resp.Body)

	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return nil, provider.ErrAuth
	}

	h5util := parseHeaderFloat(resp.Header.Get("anthropic-ratelimit-unified-5h-utilization"))
	w1util := parseHeaderFloat(resp.Header.Get("anthropic-ratelimit-unified-7d-utilization"))
	h5reset := parseResetTime(resp.Header.Get("anthropic-ratelimit-unified-5h-reset"))
	w1reset := parseResetTime(resp.Header.Get("anthropic-ratelimit-unified-7d-reset"))

	if h5util < 0 && w1util < 0 {
		return nil, fmt.Errorf("no unified rate limit headers in response (HTTP %d)", resp.StatusCode)
	}

	const scale = 10000
	return &provider.Usage{
		Primary: provider.Window{
			Used: int64(math.Round(clamp01(h5util) * scale)), Limit: scale,
			DurationMins: 5 * 60, Reset: h5reset,
		},
		Secondary: provider.Window{
			Used: int64(math.Round(clamp01(w1util) * scale)), Limit: scale,
			DurationMins: 7 * 24 * 60, Reset: w1reset,
		},
	}, nil
}

func parseHeaderFloat(s string) float64 {
	if s == "" {
		return -1
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return -1
	}
	return v
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// parseResetTime parses a reset header value as RFC3339 or Unix epoch seconds.
// Returns the zero time if the header is empty or unparseable.
func parseResetTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	// Try RFC3339 first (e.g. "2026-06-20T14:30:00Z")
	t, err := time.Parse(time.RFC3339, s)
	if err == nil {
		return t
	}
	// Try Unix epoch seconds
	epoch, err := strconv.ParseInt(s, 10, 64)
	if err == nil && epoch > 0 {
		return time.Unix(epoch, 0)
	}
	return time.Time{}
}
