package api

import (
	"math"
	"testing"
	"time"
)

func TestParseOpenCodeUsageFromHTML_NormalizesPercentValues(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	html := `rollingUsage:{usagePercent:18,resetInSec:3600},weeklyUsage:{usagePercent:82,resetInSec:7200},monthlyUsage:{usagePercent:5,resetInSec:10800}`

	usage, err := ParseOpenCodeUsageFromHTML(html, now)
	if err != nil {
		t.Fatalf("ParseOpenCodeUsageFromHTML returned error: %v", err)
	}

	if !almostEqual(usage.RollingUsagePercent, 0.18) {
		t.Fatalf("expected rolling usage 0.18, got %f", usage.RollingUsagePercent)
	}
	if !almostEqual(usage.WeeklyUsagePercent, 0.82) {
		t.Fatalf("expected weekly usage 0.82, got %f", usage.WeeklyUsagePercent)
	}
	if !almostEqual(usage.MonthlyUsagePercent, 0.05) {
		t.Fatalf("expected monthly usage 0.05, got %f", usage.MonthlyUsagePercent)
	}
}

func TestParseOpenCodeUsageFromHTML_KeepsFractionValues(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	html := `rollingUsage:{usagePercent:0.18,resetInSec:3600},weeklyUsage:{usagePercent:0.82,resetInSec:7200}`

	usage, err := ParseOpenCodeUsageFromHTML(html, now)
	if err != nil {
		t.Fatalf("ParseOpenCodeUsageFromHTML returned error: %v", err)
	}

	if !almostEqual(usage.RollingUsagePercent, 0.18) {
		t.Fatalf("expected rolling usage 0.18, got %f", usage.RollingUsagePercent)
	}
	if !almostEqual(usage.WeeklyUsagePercent, 0.82) {
		t.Fatalf("expected weekly usage 0.82, got %f", usage.WeeklyUsagePercent)
	}
}

func TestParseOpenCodeUsageFromHTML_HandlesOnePercentValues(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	html := `rollingUsage:{usagePercent:1,resetInSec:3600},weeklyUsage:{usagePercent:1,resetInSec:7200}`

	usage, err := ParseOpenCodeUsageFromHTML(html, now)
	if err != nil {
		t.Fatalf("ParseOpenCodeUsageFromHTML returned error: %v", err)
	}

	if !almostEqual(usage.RollingUsagePercent, 0.01) {
		t.Fatalf("expected rolling usage 0.01, got %f", usage.RollingUsagePercent)
	}
	if !almostEqual(usage.WeeklyUsagePercent, 0.01) {
		t.Fatalf("expected weekly usage 0.01, got %f", usage.WeeklyUsagePercent)
	}
}

func almostEqual(a, b float64) bool {
	return math.Abs(a-b) < 1e-9
}
