package main

import (
	"strings"
	"testing"

	"github.com/thorstenhirsch/clue/provider"
)

func TestEncodeUsageExtendedProtocol(t *testing.T) {
	usage := &provider.Usage{
		Primary:   provider.Window{Used: 25, Limit: 100, DurationMins: 300},
		Secondary: provider.Window{Used: 80, Limit: 100, DurationMins: 10080},
	}
	got := encodeUsage(usage, 1, 720, 5, 900)
	want := "U:25:100:80:100:720:5:900:1:300:10080"
	if got != want {
		t.Fatalf("encodeUsage() = %q, want %q", got, want)
	}
	if fields := strings.Count(got, ":"); fields != 10 {
		t.Fatalf("field separators = %d", fields)
	}
}

func TestUsagePercentHandlesMissingAndClamps(t *testing.T) {
	for _, tc := range []struct {
		window provider.Window
		want   int64
	}{
		{provider.Window{}, 0},
		{provider.Window{Used: -1, Limit: 100}, 0},
		{provider.Window{Used: 42, Limit: 100}, 42},
		{provider.Window{Used: 120, Limit: 100}, 100},
	} {
		if got := usagePercent(tc.window); got != tc.want {
			t.Errorf("usagePercent(%+v) = %d, want %d", tc.window, got, tc.want)
		}
	}
}
