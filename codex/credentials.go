package codex

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Credentials struct {
	AccessToken string
	AccountID   string
	ExpiresAt   time.Time
}

type credentialsFile struct {
	AuthMode string     `json:"auth_mode"`
	Tokens   *tokenData `json:"tokens"`
}

type tokenData struct {
	AccessToken string `json:"access_token"`
	IDToken     string `json:"id_token"`
	AccountID   string `json:"account_id"`
}

type jwtClaims struct {
	ExpiresAt int64  `json:"exp"`
	AccountID string `json:"chatgpt_account_id"`
}

func credentialsPath() (string, error) {
	if home := os.Getenv("CODEX_HOME"); home != "" {
		return filepath.Join(home, "auth.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("home directory: %w", err)
	}
	return filepath.Join(home, ".codex", "auth.json"), nil
}

func LoadCredentials() (*Credentials, error) {
	path, err := credentialsPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w (Codex must use cli_auth_credentials_store = \"file\")", path, err)
	}
	var f credentialsFile
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	if f.AuthMode != "chatgpt" {
		return nil, fmt.Errorf("Codex auth mode is %q; sign in with ChatGPT subscription access", f.AuthMode)
	}
	if f.Tokens == nil || f.Tokens.AccessToken == "" {
		return nil, fmt.Errorf("no ChatGPT access token in %s", path)
	}

	accessClaims, _ := parseJWTClaims(f.Tokens.AccessToken)
	idClaims, _ := parseJWTClaims(f.Tokens.IDToken)
	accountID := f.Tokens.AccountID
	if accountID == "" {
		accountID = accessClaims.AccountID
	}
	if accountID == "" {
		accountID = idClaims.AccountID
	}
	expiresAt := time.Time{}
	if accessClaims.ExpiresAt > 0 {
		expiresAt = time.Unix(accessClaims.ExpiresAt, 0)
	}
	return &Credentials{AccessToken: f.Tokens.AccessToken, AccountID: accountID, ExpiresAt: expiresAt}, nil
}

func parseJWTClaims(token string) (jwtClaims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return jwtClaims{}, fmt.Errorf("invalid JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return jwtClaims{}, err
	}
	var claims jwtClaims
	if err := json.Unmarshal(payload, &claims); err != nil {
		return jwtClaims{}, err
	}
	if claims.AccountID == "" {
		var raw struct {
			OpenAIAuth struct {
				AccountID string `json:"chatgpt_account_id"`
			} `json:"https://api.openai.com/auth"`
		}
		if json.Unmarshal(payload, &raw) == nil {
			claims.AccountID = raw.OpenAIAuth.AccountID
		}
	}
	return claims, nil
}
