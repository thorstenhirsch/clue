package claude

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/thorstenhirsch/clue/provider"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func response(status int, headers http.Header) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     headers,
		Body:       io.NopCloser(strings.NewReader("")),
	}
}

func TestFetchUsage(t *testing.T) {
	client := NewClient("token")
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if got := r.Header.Get("Authorization"); got != "Bearer token" {
			t.Errorf("Authorization = %q", got)
		}
		headers := make(http.Header)
		headers.Set("anthropic-ratelimit-unified-5h-utilization", "0.485")
		headers.Set("anthropic-ratelimit-unified-7d-utilization", "1.1")
		headers.Set("anthropic-ratelimit-unified-5h-reset", "2026-07-27T12:00:00Z")
		headers.Set("anthropic-ratelimit-unified-7d-reset", "1700000000")
		return response(http.StatusOK, headers), nil
	})
	usage, err := client.FetchUsage()
	if err != nil {
		t.Fatal(err)
	}
	if usage.Primary.Used != 4850 || usage.Primary.DurationMins != 300 {
		t.Fatalf("primary = %+v", usage.Primary)
	}
	if usage.Secondary.Used != 10000 || usage.Secondary.DurationMins != 10080 {
		t.Fatalf("secondary = %+v", usage.Secondary)
	}
	if want := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC); !usage.Primary.Reset.Equal(want) {
		t.Fatalf("reset = %s, want %s", usage.Primary.Reset, want)
	}
}

func TestFetchUsageErrors(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			client := NewClient("token")
			client.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				return response(status, make(http.Header)), nil
			})
			_, err := client.FetchUsage()
			if err != provider.ErrAuth {
				t.Fatalf("error = %v", err)
			}
		})
	}
}
