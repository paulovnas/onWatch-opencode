package web

import (
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/store"
)

func TestOpenCodeUsageStatus_NormalizesPercentInput(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		utilization float64
		expected    string
	}{
		{name: "healthy_percent_scale", utilization: 18, expected: "healthy"},
		{name: "warning_percent_scale", utilization: 82, expected: "warning"},
		{name: "critical_percent_scale", utilization: 95, expected: "critical"},
		{name: "healthy_fraction_scale", utilization: 0.18, expected: "healthy"},
		{name: "warning_fraction_scale", utilization: 0.82, expected: "warning"},
		{name: "critical_fraction_scale", utilization: 1.0, expected: "critical"},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := openCodeUsageStatus(tc.utilization); got != tc.expected {
				t.Fatalf("openCodeUsageStatus(%v) = %q, want %q", tc.utilization, got, tc.expected)
			}
		})
	}
}

func TestBuildOpenCodeCurrent_NormalizesLegacyPercentSnapshot(t *testing.T) {
	t.Parallel()

	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)
	_, err = s.InsertOpenCodeSnapshot(&api.OpenCodeSnapshot{
		CapturedAt:      now,
		WorkspaceID:     "wrk_test",
		HasMonthlyUsage: true,
		RollingUsage: api.OpenCodeQuota{
			Name:        "rolling",
			Utilization: 18,
			ResetsAt:    &resetAt,
			ResetInSec:  int((2 * time.Hour).Seconds()),
		},
		WeeklyUsage: api.OpenCodeQuota{
			Name:        "weekly",
			Utilization: 82,
			ResetsAt:    &resetAt,
			ResetInSec:  int((2 * time.Hour).Seconds()),
		},
		MonthlyUsage: api.OpenCodeQuota{
			Name:        "monthly",
			Utilization: 5,
			ResetsAt:    &resetAt,
			ResetInSec:  int((2 * time.Hour).Seconds()),
		},
	})
	if err != nil {
		t.Fatalf("InsertOpenCodeSnapshot: %v", err)
	}

	h := NewHandler(s, nil, nil, nil, createTestConfigWithSynthetic())
	resp := h.buildOpenCodeCurrent()

	quotaList, ok := resp["quotas"].([]map[string]interface{})
	if !ok {
		t.Fatalf("expected quotas []map[string]interface{}, got %T", resp["quotas"])
	}
	if len(quotaList) != 3 {
		t.Fatalf("expected 3 quotas, got %d", len(quotaList))
	}

	byName := map[string]map[string]interface{}{}
	for _, q := range quotaList {
		name, _ := q["quotaName"].(string)
		byName[name] = q
	}

	rolling := byName["rolling_usage"]
	if rolling == nil {
		t.Fatal("missing rolling_usage quota")
	}
	assertOpenCodeFieldClose(t, rolling["usagePercent"], 18)
	assertOpenCodeFieldClose(t, rolling["remainingPercent"], 82)
	if status, _ := rolling["status"].(string); status != "healthy" {
		t.Fatalf("expected rolling status healthy, got %q", status)
	}

	weekly := byName["weekly_usage"]
	if weekly == nil {
		t.Fatal("missing weekly_usage quota")
	}
	assertOpenCodeFieldClose(t, weekly["usagePercent"], 82)
	if status, _ := weekly["status"].(string); status != "warning" {
		t.Fatalf("expected weekly status warning, got %q", status)
	}
}

func TestApplyProviderSettingsFromDB_OpenCodeCookie(t *testing.T) {
	t.Parallel()

	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	if err := s.SetSetting("provider_settings", `{"opencode":{"cookie":"raw_cookie_value"}}`); err != nil {
		t.Fatalf("SetSetting: %v", err)
	}

	cfg := createTestConfigWithSynthetic()
	ApplyProviderSettingsFromDB(s, cfg, nil)

	if got, want := cfg.OpenCodeCookie, "auth=raw_cookie_value"; got != want {
		t.Fatalf("cfg.OpenCodeCookie = %q, want %q", got, want)
	}
	if cfg.OpenCodeAutoCookie {
		t.Fatal("expected OpenCodeAutoCookie to be false after applying provider_settings")
	}
}

func TestStripProviderSecrets_MasksCookie(t *testing.T) {
	t.Parallel()

	providers := map[string]interface{}{
		"opencode": map[string]interface{}{
			"cookie": "auth=secret_cookie",
		},
	}

	stripProviderSecrets(providers)

	opencode, ok := providers["opencode"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected opencode settings map, got %T", providers["opencode"])
	}

	if got, _ := opencode["cookie"].(string); got != "" {
		t.Fatalf("expected masked cookie value, got %q", got)
	}
	if set, _ := opencode["cookie_set"].(bool); !set {
		t.Fatalf("expected cookie_set=true, got %v", opencode["cookie_set"])
	}
}

func assertOpenCodeFieldClose(t *testing.T, value interface{}, want float64) {
	t.Helper()
	got, ok := value.(float64)
	if !ok {
		t.Fatalf("expected float64 value, got %T", value)
	}
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("expected %.4f, got %.4f", want, got)
	}
}

func TestHistoryBoth_IncludesOpenCodeSeries(t *testing.T) {
	t.Parallel()

	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetAt := now.Add(2 * time.Hour)
	_, err = s.InsertOpenCodeSnapshot(&api.OpenCodeSnapshot{
		CapturedAt:      now.Add(-10 * time.Minute),
		WorkspaceID:     "wrk_test",
		HasMonthlyUsage: true,
		RollingUsage: api.OpenCodeQuota{
			Name:        "rolling",
			Utilization: 0.2,
			ResetsAt:    &resetAt,
			ResetInSec:  int((2 * time.Hour).Seconds()),
		},
		WeeklyUsage: api.OpenCodeQuota{
			Name:        "weekly",
			Utilization: 0.5,
			ResetsAt:    &resetAt,
			ResetInSec:  int((2 * time.Hour).Seconds()),
		},
		MonthlyUsage: api.OpenCodeQuota{
			Name:        "monthly",
			Utilization: 0.8,
			ResetsAt:    &resetAt,
			ResetInSec:  int((2 * time.Hour).Seconds()),
		},
	})
	if err != nil {
		t.Fatalf("InsertOpenCodeSnapshot: %v", err)
	}

	cfg := createTestConfigWithSynthetic()
	cfg.OpenCodeCookie = "test_cookie"
	h := NewHandler(s, nil, nil, nil, cfg)

	req := httptest.NewRequest(http.MethodGet, "/api/history?provider=both&range=1h", nil)
	rr := httptest.NewRecorder()
	h.History(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	opencode, ok := resp["opencode"].([]interface{})
	if !ok {
		t.Fatalf("expected opencode history array, got %T", resp["opencode"])
	}
	if len(opencode) == 0 {
		t.Fatal("expected at least one opencode history point")
	}

	first, ok := opencode[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected opencode history row object, got %T", opencode[0])
	}
	assertOpenCodeFieldClose(t, first["rolling_usage"], 20)
	assertOpenCodeFieldClose(t, first["weekly_usage"], 50)
	assertOpenCodeFieldClose(t, first["monthly_usage"], 80)
}

func TestLoggingHistoryOpenCode_NormalizesLegacyPercentSnapshot(t *testing.T) {
	t.Parallel()

	s, err := store.New(":memory:")
	if err != nil {
		t.Fatalf("store.New: %v", err)
	}
	defer s.Close()

	now := time.Now().UTC()
	resetAt := now.Add(90 * time.Minute)
	_, err = s.InsertOpenCodeSnapshot(&api.OpenCodeSnapshot{
		CapturedAt:      now.Add(-5 * time.Minute),
		WorkspaceID:     "wrk_legacy",
		HasMonthlyUsage: true,
		RollingUsage: api.OpenCodeQuota{
			Name:        "rolling",
			Utilization: 18,
			ResetsAt:    &resetAt,
		},
		WeeklyUsage: api.OpenCodeQuota{
			Name:        "weekly",
			Utilization: 82,
			ResetsAt:    &resetAt,
		},
		MonthlyUsage: api.OpenCodeQuota{
			Name:        "monthly",
			Utilization: 5,
			ResetsAt:    &resetAt,
		},
	})
	if err != nil {
		t.Fatalf("InsertOpenCodeSnapshot: %v", err)
	}

	h := NewHandler(s, nil, nil, nil, createTestConfigWithSynthetic())
	req := httptest.NewRequest(http.MethodGet, "/api/logging-history?provider=opencode&range=1", nil)
	rr := httptest.NewRecorder()
	h.LoggingHistory(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rr.Code, rr.Body.String())
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	logs, ok := resp["logs"].([]interface{})
	if !ok || len(logs) == 0 {
		t.Fatalf("expected non-empty logs, got %#v", resp["logs"])
	}

	firstLog, ok := logs[0].(map[string]interface{})
	if !ok {
		t.Fatalf("expected log object, got %T", logs[0])
	}
	quotaValues, ok := firstLog["quotaValues"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected quotaValues object, got %T", firstLog["quotaValues"])
	}

	rolling, ok := quotaValues["rolling_usage"].(map[string]interface{})
	if !ok {
		t.Fatalf("expected rolling_usage object, got %T", quotaValues["rolling_usage"])
	}
	assertOpenCodeFieldClose(t, rolling["usagePercent"], 18)
	assertOpenCodeFieldClose(t, rolling["utilization"], 0.18)
}
