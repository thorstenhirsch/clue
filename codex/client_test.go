package codex

import (
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/thorstenhirsch/clue/provider"
)

func testJWT(t *testing.T, payload string) string {
	t.Helper()
	return "e30." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + ".signature"
}

func writeCredentials(t *testing.T, accessToken, accountID string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("CODEX_HOME", dir)
	data := fmt.Sprintf(`{"auth_mode":"chatgpt","tokens":{"access_token":%q,"id_token":"","account_id":%q}}`, accessToken, accountID)
	if err := os.WriteFile(filepath.Join(dir, "auth.json"), []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func TestFetchUsage(t *testing.T) {
	accessToken := testJWT(t, fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Hour).Unix()))
	writeCredentials(t, accessToken, "account-123")

	client := NewClient()
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+accessToken {
			t.Errorf("Authorization = %q", got)
		}
		if got := r.Header.Get("ChatGPT-Account-Id"); got != "account-123" {
			t.Errorf("ChatGPT-Account-Id = %q", got)
		}
		return response(http.StatusOK, `{"rate_limit":{"primary_window":{"used_percent":24.6,"limit_window_seconds":18000,"reset_at":1700000000},"secondary_window":{"used_percent":101,"limit_window_seconds":604800,"reset_at":1700100000}}}`), nil
	})
	usage, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	if usage.Primary.Used != 25 || usage.Primary.Limit != 100 || usage.Primary.DurationMins != 300 {
		t.Fatalf("primary = %+v", usage.Primary)
	}
	if usage.Secondary.Used != 100 || usage.Secondary.DurationMins != 10080 {
		t.Fatalf("secondary = %+v", usage.Secondary)
	}
}

func TestFetchUsageMissingSecondary(t *testing.T) {
	writeCredentials(t, "opaque-token", "account-123")
	client := NewClient()
	client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, `{"rate_limit":{"primary_window":{"used_percent":10}}}`), nil
	})
	usage, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	if usage.Secondary.Limit != 0 {
		t.Fatalf("secondary = %+v", usage.Secondary)
	}
}

func TestFetchUsageAuthFailures(t *testing.T) {
	t.Run("expired JWT", func(t *testing.T) {
		writeCredentials(t, testJWT(t, `{"exp":1}`), "account-123")
		_, err := NewClient().FetchUsage()
		if err != provider.ErrAuth {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("unauthorized", func(t *testing.T) {
		writeCredentials(t, "opaque-token", "account-123")
		client := NewClient()
		client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
			return response(http.StatusUnauthorized, ""), nil
		})
		_, err := client.FetchUsage()
		if err != provider.ErrAuth {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestLoadCredentialsAccountIDFallback(t *testing.T) {
	accessToken := testJWT(t, `{"https://api.openai.com/auth":{"chatgpt_account_id":"nested-account"}}`)
	writeCredentials(t, accessToken, "")
	creds, err := LoadCredentials()
	if err != nil {
		t.Fatal(err)
	}
	if creds.AccountID != "nested-account" {
		t.Fatalf("AccountID = %q", creds.AccountID)
	}
}
