package web

import (
	"net/http"
	"strconv"
	"time"
)

// currentOpenCode returns current OpenCode usage quotas.
func (h *Handler) currentOpenCode(w http.ResponseWriter, r *http.Request) {
	respondJSON(w, http.StatusOK, h.buildOpenCodeCurrent())
}

// buildOpenCodeCurrent builds current OpenCode response.
func (h *Handler) buildOpenCodeCurrent() map[string]interface{} {
	now := time.Now().UTC()
	response := map[string]interface{}{
		"capturedAt": now.Format(time.RFC3339),
		"quotas":     []interface{}{},
	}
	if h.store == nil {
		return response
	}

	latest, err := h.store.QueryLatestOpenCode()
	if err != nil {
		h.logger.Error("failed to query latest OpenCode snapshot", "error", err)
		return response
	}
	if latest == nil {
		return response
	}

	response["capturedAt"] = latest.CapturedAt.Format(time.RFC3339)
	if latest.WorkspaceID != "" {
		response["workspaceId"] = latest.WorkspaceID
	}

	var quotas []map[string]interface{}

	// Rolling Usage
	rollingUtil := normalizeOpenCodeUtilization(latest.RollingUsage.Utilization)
	rollingUsagePercent := openCodeUsagePercent(latest.RollingUsage.Utilization)
	quota := map[string]interface{}{
		"quotaName":        "rolling_usage",
		"utilization":      rollingUtil,
		"usagePercent":     rollingUsagePercent,
		"remainingPercent": max(0, 100-rollingUsagePercent),
		"status":           openCodeUsageStatus(rollingUtil),
	}
	if latest.RollingUsage.ResetsAt != nil {
		timeUntilReset := time.Until(*latest.RollingUsage.ResetsAt)
		quota["resetTime"] = latest.RollingUsage.ResetsAt.Format(time.RFC3339)
		quota["timeUntilReset"] = formatDuration(timeUntilReset)
		quota["timeUntilResetSeconds"] = int64(timeUntilReset.Seconds())
	}
	if latest.RollingUsage.ResetInSec > 0 {
		quota["timeUntilResetSeconds"] = int64(latest.RollingUsage.ResetInSec)
	}
	if h.opencodeTracker != nil {
		if summary, err := h.opencodeTracker.UsageSummary("rolling"); err == nil && summary != nil {
			quota["currentRate"] = summary.CurrentRate
			quota["projectedUsage"] = normalizeOpenCodeUtilization(summary.ProjectedUtil)
		}
	}
	quotas = append(quotas, quota)

	// Weekly Usage
	weeklyUtil := normalizeOpenCodeUtilization(latest.WeeklyUsage.Utilization)
	weeklyUsagePercent := openCodeUsagePercent(latest.WeeklyUsage.Utilization)
	quota = map[string]interface{}{
		"quotaName":        "weekly_usage",
		"utilization":      weeklyUtil,
		"usagePercent":     weeklyUsagePercent,
		"remainingPercent": max(0, 100-weeklyUsagePercent),
		"status":           openCodeUsageStatus(weeklyUtil),
	}
	if latest.WeeklyUsage.ResetsAt != nil {
		timeUntilReset := time.Until(*latest.WeeklyUsage.ResetsAt)
		quota["resetTime"] = latest.WeeklyUsage.ResetsAt.Format(time.RFC3339)
		quota["timeUntilReset"] = formatDuration(timeUntilReset)
		quota["timeUntilResetSeconds"] = int64(timeUntilReset.Seconds())
	}
	if latest.WeeklyUsage.ResetInSec > 0 {
		quota["timeUntilResetSeconds"] = int64(latest.WeeklyUsage.ResetInSec)
	}
	if h.opencodeTracker != nil {
		if summary, err := h.opencodeTracker.UsageSummary("weekly"); err == nil && summary != nil {
			quota["currentRate"] = summary.CurrentRate
			quota["projectedUsage"] = normalizeOpenCodeUtilization(summary.ProjectedUtil)
		}
	}
	quotas = append(quotas, quota)

	// Monthly Usage (if available)
	if latest.HasMonthlyUsage {
		monthlyUtil := normalizeOpenCodeUtilization(latest.MonthlyUsage.Utilization)
		monthlyUsagePercent := openCodeUsagePercent(latest.MonthlyUsage.Utilization)
		quota = map[string]interface{}{
			"quotaName":        "monthly_usage",
			"utilization":      monthlyUtil,
			"usagePercent":     monthlyUsagePercent,
			"remainingPercent": max(0, 100-monthlyUsagePercent),
			"status":           openCodeUsageStatus(monthlyUtil),
		}
		if latest.MonthlyUsage.ResetsAt != nil {
			timeUntilReset := time.Until(*latest.MonthlyUsage.ResetsAt)
			quota["resetTime"] = latest.MonthlyUsage.ResetsAt.Format(time.RFC3339)
			quota["timeUntilReset"] = formatDuration(timeUntilReset)
			quota["timeUntilResetSeconds"] = int64(timeUntilReset.Seconds())
		}
		if latest.MonthlyUsage.ResetInSec > 0 {
			quota["timeUntilResetSeconds"] = int64(latest.MonthlyUsage.ResetInSec)
		}
		if h.opencodeTracker != nil {
			if summary, err := h.opencodeTracker.UsageSummary("monthly"); err == nil && summary != nil {
				quota["currentRate"] = summary.CurrentRate
				quota["projectedUsage"] = normalizeOpenCodeUtilization(summary.ProjectedUtil)
			}
		}
		quotas = append(quotas, quota)
	}

	response["quotas"] = quotas
	return response
}

// historyOpenCode returns OpenCode usage history as a flat array.
// Each entry has capturedAt and quota-keyed usage values.
func (h *Handler) historyOpenCode(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, []interface{}{})
		return
	}

	rangeStr := r.URL.Query().Get("range")
	rangeDur, err := parseTimeRange(rangeStr)
	if err != nil {
		respondError(w, http.StatusBadRequest, err.Error())
		return
	}

	now := time.Now().UTC()
	start := now.Add(-rangeDur)

	snapshots, err := h.store.QueryOpenCodeRange(start, now)
	if err != nil {
		h.logger.Error("failed to query OpenCode history", "error", err)
		respondError(w, http.StatusInternalServerError, "failed to query history")
		return
	}

	var history []map[string]interface{}
	for _, s := range snapshots {
		entry := map[string]interface{}{
			"capturedAt":    s.CapturedAt.Format(time.RFC3339),
			"rolling_usage": openCodeUsagePercent(s.RollingUsage.Utilization),
			"weekly_usage":  openCodeUsagePercent(s.WeeklyUsage.Utilization),
		}
		if s.HasMonthlyUsage {
			entry["monthly_usage"] = openCodeUsagePercent(s.MonthlyUsage.Utilization)
		}
		history = append(history, entry)
	}

	respondJSON(w, http.StatusOK, history)
}

// cyclesOpenCode returns OpenCode reset cycle history.
// Each cycle shows quota_name, cycle_start, cycle_end, peak_utilization, and total_delta.
func (h *Handler) cyclesOpenCode(w http.ResponseWriter, r *http.Request) {
	if h.store == nil {
		respondJSON(w, http.StatusOK, map[string]interface{}{"cycles": []interface{}{}})
		return
	}

	limitParam := r.URL.Query().Get("limit")
	limit := 200
	if limitParam != "" {
		if v, err := strconv.Atoi(limitParam); err == nil && v > 0 && v < 200 {
			limit = v
		}
	}

	now := time.Now().UTC()
	start := now.Add(-30 * 24 * time.Hour)
	snapshots, err := h.store.QueryOpenCodeRange(start, now, limit)
	if err != nil || len(snapshots) == 0 {
		respondJSON(w, http.StatusOK, map[string]interface{}{"cycles": []interface{}{}})
		return
	}

	// Build cycle data from snapshots for rolling_usage, weekly_usage, and monthly_usage
	var results []map[string]interface{}
	for i := len(snapshots) - 1; i >= 0; i-- {
		snap := snapshots[i]
		entry := map[string]interface{}{
			"capturedAt":    snap.CapturedAt.Format(time.RFC3339),
			"rolling_usage": openCodeUsagePercent(snap.RollingUsage.Utilization),
			"weekly_usage":  openCodeUsagePercent(snap.WeeklyUsage.Utilization),
		}
		if snap.HasMonthlyUsage {
			entry["monthly_usage"] = openCodeUsagePercent(snap.MonthlyUsage.Utilization)
		} else {
			entry["monthly_usage"] = 0.0
		}
		results = append(results, entry)
	}

	respondJSON(w, http.StatusOK, map[string]interface{}{"cycles": results})
}

// openCodeUsageStatus returns status string based on utilization.
// Values must match the statusConfig keys used in the dashboard JS/CSS.
func openCodeUsageStatus(utilization float64) string {
	normalized := normalizeOpenCodeUtilization(utilization)
	if normalized >= 0.9 {
		return "critical"
	}
	if normalized >= 0.75 {
		return "warning"
	}
	return "healthy"
}

func openCodeUsagePercent(utilization float64) float64 {
	return normalizeOpenCodeUtilization(utilization) * 100
}

func normalizeOpenCodeUtilization(utilization float64) float64 {
	switch {
	case utilization < 0:
		return 0
	case utilization > 100:
		return 1
	case utilization > 1:
		return utilization / 100
	default:
		return utilization
	}
}
