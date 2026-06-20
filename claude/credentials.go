package claude

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

type Credentials struct {
	AccessToken      string
	RefreshToken     string
	ExpiresAt        time.Time
	SubscriptionType string
}

type credentialsFile struct {
	ClaudeAiOauth *oauthEntry `json:"claudeAiOauth"`
}

type oauthEntry struct {
	AccessToken      string `json:"accessToken"`
	RefreshToken     string `json:"refreshToken"`
	ExpiresAt        int64  `json:"expiresAt"`
	SubscriptionType string `json:"subscriptionType"`
}

func LoadCredentials() (*Credentials, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("home directory: %w", err)
	}
	path := filepath.Join(home, ".claude", ".credentials.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var f credentialsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing credentials: %w", err)
	}
	if f.ClaudeAiOauth == nil {
		return nil, fmt.Errorf("no claudeAiOauth entry in %s", path)
	}
	o := f.ClaudeAiOauth
	if o.AccessToken == "" {
		return nil, fmt.Errorf("empty accessToken in %s", path)
	}
	return &Credentials{
		AccessToken:      o.AccessToken,
		RefreshToken:     o.RefreshToken,
		ExpiresAt:        time.UnixMilli(o.ExpiresAt),
		SubscriptionType: o.SubscriptionType,
	}, nil
}

func (c *Credentials) Expired() bool {
	return time.Now().After(c.ExpiresAt)
}
