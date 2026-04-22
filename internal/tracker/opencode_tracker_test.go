package tracker

import (
	"math"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestNormalizeOpenCodeTrackerUtilization(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input float64
		want  float64
	}{
		{name: "negative", input: -5, want: 0},
		{name: "fraction", input: 0.82, want: 0.82},
		{name: "one_is_full", input: 1, want: 1},
		{name: "percent_scale", input: 82, want: 0.82},
		{name: "over_100_clamped", input: 150, want: 1},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := normalizeOpenCodeTrackerUtilization(tc.input)
			if math.Abs(got-tc.want) > 1e-9 {
				t.Fatalf("normalizeOpenCodeTrackerUtilization(%v) = %v, want %v", tc.input, got, tc.want)
			}
		})
	}
}

func TestOpenCodeTrackerUsageSummary_ClampsProjectedUtilAndHandlesQuotaAlias(t *testing.T) {
	t.Parallel()

	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetAt := now.Add(1 * time.Hour)
	cycleStart := now.Add(-2 * time.Hour)

	if _, err := s.CreateOpenCodeCycle("rolling", cycleStart, &resetAt); err != nil {
		t.Fatalf("CreateOpenCodeCycle: %v", err)
	}
	// 0.95 current + (0.5 delta / 2h * 1h left) = 1.20 -> must clamp to 1.0
	if err := s.UpdateOpenCodeCycle("rolling", 0.95, 0.5); err != nil {
		t.Fatalf("UpdateOpenCodeCycle: %v", err)
	}

	_, err = s.InsertOpenCodeSnapshot(&api.OpenCodeSnapshot{
		CapturedAt:  now,
		WorkspaceID: "wrk_test",
		RollingUsage: api.OpenCodeQuota{
			Name:        "rolling",
			Utilization: 0.95,
			ResetsAt:    &resetAt,
		},
		WeeklyUsage: api.OpenCodeQuota{
			Name:        "weekly",
			Utilization: 0.15,
		},
	})
	if err != nil {
		t.Fatalf("InsertOpenCodeSnapshot: %v", err)
	}

	tr := NewOpenCodeTracker(s, nil)
	summary, err := tr.UsageSummary("rolling_usage")
	if err != nil {
		t.Fatalf("UsageSummary: %v", err)
	}
	if summary == nil {
		t.Fatal("expected non-nil summary")
	}
	if summary.QuotaName != "rolling" {
		t.Fatalf("QuotaName = %q, want rolling", summary.QuotaName)
	}
	if summary.ProjectedUtil != 1 {
		t.Fatalf("ProjectedUtil = %v, want 1", summary.ProjectedUtil)
	}
}
