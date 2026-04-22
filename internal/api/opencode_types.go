package api

import (
	"encoding/json"
	"regexp"
	"strconv"
	"time"
)

// OpenCodeQuota represents a single quota metric for OpenCode
type OpenCodeQuota struct {
	Name        string
	Used        float64
	Limit       float64
	Utilization float64
	ResetsAt    *time.Time
	ResetInSec  int
}

// OpenCodeSnapshot represents the complete quota snapshot for OpenCode
type OpenCodeSnapshot struct {
	ID              int64
	CapturedAt      time.Time
	WorkspaceID     string
	RollingUsage    OpenCodeQuota
	WeeklyUsage     OpenCodeQuota
	MonthlyUsage    OpenCodeQuota
	HasMonthlyUsage bool
	RawJSON         string
}

// OpenCodeUsageSnapshot is the parsed usage data from OpenCode
type OpenCodeUsageSnapshot struct {
	HasMonthlyUsage     bool
	RollingUsagePercent float64
	WeeklyUsagePercent  float64
	MonthlyUsagePercent float64
	RollingResetInSec   int
	WeeklyResetInSec    int
	MonthlyResetInSec   int
	UpdatedAt           time.Time
}

// ParseOpenCodeUsageFromHTML extracts usage data from HTML page
func ParseOpenCodeUsageFromHTML(html string, now time.Time) (*OpenCodeUsageSnapshot, error) {
	snapshot := &OpenCodeUsageSnapshot{
		UpdatedAt: now,
	}

	// Extract rolling usage percent
	if rollingPercent := extractDoubleFromHTML(html, `rollingUsage[^}]*?usagePercent\s*:\s*([0-9]+(?:\.[0-9]+)?)`); rollingPercent != nil {
		snapshot.RollingUsagePercent = normalizeOpenCodeUtilizationFraction(*rollingPercent)
	}
	if rollingReset := extractIntFromHTML(html, `rollingUsage[^}]*?resetInSec\s*:\s*([0-9]+)`); rollingReset != nil {
		snapshot.RollingResetInSec = *rollingReset
	}

	// Extract weekly usage percent
	if weeklyPercent := extractDoubleFromHTML(html, `weeklyUsage[^}]*?usagePercent\s*:\s*([0-9]+(?:\.[0-9]+)?)`); weeklyPercent != nil {
		snapshot.WeeklyUsagePercent = normalizeOpenCodeUtilizationFraction(*weeklyPercent)
	}
	if weeklyReset := extractIntFromHTML(html, `weeklyUsage[^}]*?resetInSec\s*:\s*([0-9]+)`); weeklyReset != nil {
		snapshot.WeeklyResetInSec = *weeklyReset
	}

	// Extract monthly usage (optional)
	if monthlyPercent := extractDoubleFromHTML(html, `monthlyUsage[^}]*?usagePercent\s*:\s*([0-9]+(?:\.[0-9]+)?)`); monthlyPercent != nil {
		snapshot.MonthlyUsagePercent = normalizeOpenCodeUtilizationFraction(*monthlyPercent)
		snapshot.HasMonthlyUsage = true
	}
	if monthlyReset := extractIntFromHTML(html, `monthlyUsage[^}]*?resetInSec\s*:\s*([0-9]+)`); monthlyReset != nil {
		snapshot.MonthlyResetInSec = *monthlyReset
		snapshot.HasMonthlyUsage = true
	}

	return snapshot, nil
}

func normalizeOpenCodeUtilizationFraction(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value >= 1 {
		if value > 100 {
			return 1
		}
		return value / 100
	}
	return value
}

func extractDoubleFromHTML(html, pattern string) *float64 {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	matches := re.FindStringSubmatch(html)
	if len(matches) < 2 {
		return nil
	}
	val, err := strconv.ParseFloat(matches[1], 64)
	if err != nil {
		return nil
	}
	return &val
}

func extractIntFromHTML(html, pattern string) *int {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}
	matches := re.FindStringSubmatch(html)
	if len(matches) < 2 {
		return nil
	}
	val, err := strconv.Atoi(matches[1])
	if err != nil {
		return nil
	}
	return &val
}

// ToOpenCodeSnapshot converts usage data to a snapshot
func ToOpenCodeSnapshot(workspaceID string, usage *OpenCodeUsageSnapshot) *OpenCodeSnapshot {
	now := time.Now().UTC()

	snapshot := &OpenCodeSnapshot{
		CapturedAt:      now,
		WorkspaceID:     workspaceID,
		HasMonthlyUsage: usage.HasMonthlyUsage,
	}

	// Rolling quota
	snapshot.RollingUsage = OpenCodeQuota{
		Name:        "rolling",
		Utilization: usage.RollingUsagePercent,
		ResetInSec:  usage.RollingResetInSec,
	}
	if usage.RollingResetInSec > 0 {
		resetAt := now.Add(time.Duration(usage.RollingResetInSec) * time.Second)
		snapshot.RollingUsage.ResetsAt = &resetAt
	}

	// Weekly quota
	snapshot.WeeklyUsage = OpenCodeQuota{
		Name:        "weekly",
		Utilization: usage.WeeklyUsagePercent,
		ResetInSec:  usage.WeeklyResetInSec,
	}
	if usage.WeeklyResetInSec > 0 {
		resetAt := now.Add(time.Duration(usage.WeeklyResetInSec) * time.Second)
		snapshot.WeeklyUsage.ResetsAt = &resetAt
	}

	// Monthly quota (optional)
	if usage.HasMonthlyUsage {
		snapshot.MonthlyUsage = OpenCodeQuota{
			Name:        "monthly",
			Utilization: usage.MonthlyUsagePercent,
			ResetInSec:  usage.MonthlyResetInSec,
		}
		if usage.MonthlyResetInSec > 0 {
			resetAt := now.Add(time.Duration(usage.MonthlyResetInSec) * time.Second)
			snapshot.MonthlyUsage.ResetsAt = &resetAt
		}
	}

	if raw, err := json.Marshal(usage); err == nil {
		snapshot.RawJSON = string(raw)
	}

	return snapshot
}
