package store

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
)

// OpenCodeResetCycle represents an OpenCode quota reset cycle.
type OpenCodeResetCycle struct {
	ID              int64
	QuotaName       string
	CycleStart      time.Time
	CycleEnd        *time.Time
	ResetsAt        *time.Time
	PeakUtilization float64
	TotalDelta      float64
}

func parseOpenCodeTime(value string, field string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("failed to parse %s %q: %w", field, value, err)
	}
	return parsed, nil
}

// InsertOpenCodeSnapshot inserts an OpenCode snapshot with its quota values.
func (s *Store) InsertOpenCodeSnapshot(snapshot *api.OpenCodeSnapshot) (int64, error) {
	tx, err := s.db.Begin()
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	result, err := tx.Exec(
		`INSERT INTO opencode_snapshots (captured_at, workspace_id, has_monthly_usage, raw_json, quota_count) VALUES (?, ?, ?, ?, ?)`,
		snapshot.CapturedAt.Format(time.RFC3339Nano),
		snapshot.WorkspaceID,
		snapshot.HasMonthlyUsage,
		snapshot.RawJSON,
		3, // rolling, weekly, and optionally monthly
	)
	if err != nil {
		return 0, fmt.Errorf("failed to insert opencode snapshot: %w", err)
	}

	snapshotID, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get snapshot ID: %w", err)
	}

	quotas := []api.OpenCodeQuota{snapshot.RollingUsage, snapshot.WeeklyUsage}
	if snapshot.HasMonthlyUsage {
		quotas = append(quotas, snapshot.MonthlyUsage)
	}

	for _, q := range quotas {
		var resetsAt interface{}
		if q.ResetsAt != nil {
			resetsAt = q.ResetsAt.Format(time.RFC3339Nano)
		}
		_, err := tx.Exec(
			`INSERT INTO opencode_quota_values (snapshot_id, quota_name, utilization, resets_at, reset_in_sec) VALUES (?, ?, ?, ?, ?)`,
			snapshotID,
			q.Name,
			q.Utilization,
			resetsAt,
			q.ResetInSec,
		)
		if err != nil {
			return 0, fmt.Errorf("failed to insert opencode quota value %s: %w", q.Name, err)
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, fmt.Errorf("failed to commit: %w", err)
	}

	return snapshotID, nil
}

// QueryLatestOpenCode returns the most recent OpenCode snapshot with quotas.
func (s *Store) QueryLatestOpenCode() (*api.OpenCodeSnapshot, error) {
	var snapshot api.OpenCodeSnapshot
	var capturedAt string

	err := s.db.QueryRow(
		`SELECT id, captured_at, workspace_id, has_monthly_usage, quota_count FROM opencode_snapshots ORDER BY captured_at DESC LIMIT 1`,
	).Scan(&snapshot.ID, &capturedAt, &snapshot.WorkspaceID, &snapshot.HasMonthlyUsage, new(int))

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query latest opencode: %w", err)
	}

	parsedCapturedAt, err := parseOpenCodeTime(capturedAt, "opencode snapshot captured_at")
	if err != nil {
		return nil, err
	}
	snapshot.CapturedAt = parsedCapturedAt

	rows, err := s.db.Query(
		`SELECT quota_name, utilization, resets_at, reset_in_sec FROM opencode_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
		snapshot.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query opencode quota values: %w", err)
	}
	defer rows.Close()

	for rows.Next() {
		var q api.OpenCodeQuota
		var resetsAt sql.NullString
		if err := rows.Scan(&q.Name, &q.Utilization, &resetsAt, &q.ResetInSec); err != nil {
			return nil, fmt.Errorf("failed to scan opencode quota value: %w", err)
		}
		if resetsAt.Valid && resetsAt.String != "" {
			parsedResetsAt, err := parseOpenCodeTime(resetsAt.String, "opencode quota resets_at")
			if err != nil {
				return nil, err
			}
			q.ResetsAt = &parsedResetsAt
		}

		switch q.Name {
		case "rolling":
			snapshot.RollingUsage = q
		case "weekly":
			snapshot.WeeklyUsage = q
		case "monthly":
			snapshot.MonthlyUsage = q
			snapshot.HasMonthlyUsage = true
		}
	}

	return &snapshot, rows.Err()
}

// QueryOpenCodeRange returns OpenCode snapshots within a time range.
func (s *Store) QueryOpenCodeRange(start, end time.Time, limit ...int) ([]*api.OpenCodeSnapshot, error) {
	query := `SELECT id, captured_at, workspace_id, has_monthly_usage, quota_count FROM opencode_snapshots
		WHERE captured_at BETWEEN ? AND ? ORDER BY captured_at ASC`
	args := []interface{}{start.Format(time.RFC3339Nano), end.Format(time.RFC3339Nano)}
	if len(limit) > 0 && limit[0] > 0 {
		query = `SELECT id, captured_at, workspace_id, has_monthly_usage, quota_count
			FROM (
				SELECT id, captured_at, workspace_id, has_monthly_usage, quota_count
				FROM opencode_snapshots
				WHERE captured_at BETWEEN ? AND ?
				ORDER BY captured_at DESC
				LIMIT ?
			) recent
			ORDER BY captured_at ASC`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query opencode range: %w", err)
	}
	defer rows.Close()

	var snapshots []*api.OpenCodeSnapshot
	for rows.Next() {
		var snap api.OpenCodeSnapshot
		var capturedAt string
		if err := rows.Scan(&snap.ID, &capturedAt, &snap.WorkspaceID, &snap.HasMonthlyUsage, new(int)); err != nil {
			return nil, fmt.Errorf("failed to scan opencode snapshot: %w", err)
		}
		parsedCapturedAt, err := parseOpenCodeTime(capturedAt, "opencode snapshot captured_at")
		if err != nil {
			return nil, err
		}
		snap.CapturedAt = parsedCapturedAt
		snapshots = append(snapshots, &snap)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	for _, snap := range snapshots {
		qRows, err := s.db.Query(
			`SELECT quota_name, utilization, resets_at, reset_in_sec FROM opencode_quota_values WHERE snapshot_id = ? ORDER BY quota_name`,
			snap.ID,
		)
		if err != nil {
			return nil, fmt.Errorf("failed to query opencode quota values for snapshot %d: %w", snap.ID, err)
		}
		for qRows.Next() {
			var q api.OpenCodeQuota
			var resetsAt sql.NullString
			if err := qRows.Scan(&q.Name, &q.Utilization, &resetsAt, &q.ResetInSec); err != nil {
				qRows.Close()
				return nil, fmt.Errorf("failed to scan opencode quota value: %w", err)
			}
			if resetsAt.Valid && resetsAt.String != "" {
				parsedResetsAt, err := parseOpenCodeTime(resetsAt.String, "opencode quota resets_at")
				if err != nil {
					qRows.Close()
					return nil, err
				}
				q.ResetsAt = &parsedResetsAt
			}

			switch q.Name {
			case "rolling":
				snap.RollingUsage = q
			case "weekly":
				snap.WeeklyUsage = q
			case "monthly":
				snap.MonthlyUsage = q
				snap.HasMonthlyUsage = true
			}
		}
		qRows.Close()
	}

	return snapshots, nil
}

// CreateOpenCodeCycle creates a new OpenCode reset cycle.
func (s *Store) CreateOpenCodeCycle(quotaName string, cycleStart time.Time, resetsAt *time.Time) (int64, error) {
	var resetsAtVal interface{}
	if resetsAt != nil {
		resetsAtVal = resetsAt.Format(time.RFC3339Nano)
	}

	result, err := s.db.Exec(
		`INSERT INTO opencode_reset_cycles (quota_name, cycle_start, resets_at) VALUES (?, ?, ?)`,
		quotaName,
		cycleStart.Format(time.RFC3339Nano),
		resetsAtVal,
	)
	if err != nil {
		return 0, fmt.Errorf("failed to create opencode cycle: %w", err)
	}

	id, err := result.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("failed to get cycle ID: %w", err)
	}
	return id, nil
}

// CloseOpenCodeCycle closes an OpenCode reset cycle with final stats.
func (s *Store) CloseOpenCodeCycle(quotaName string, cycleEnd time.Time, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE opencode_reset_cycles SET cycle_end = ?, peak_utilization = ?, total_delta = ?
		WHERE quota_name = ? AND cycle_end IS NULL`,
		cycleEnd.Format(time.RFC3339Nano),
		peak,
		delta,
		quotaName,
	)
	if err != nil {
		return fmt.Errorf("failed to close opencode cycle: %w", err)
	}
	return nil
}

// UpdateOpenCodeCycle updates the peak and delta for an active OpenCode cycle.
func (s *Store) UpdateOpenCodeCycle(quotaName string, peak, delta float64) error {
	_, err := s.db.Exec(
		`UPDATE opencode_reset_cycles SET peak_utilization = ?, total_delta = ?
		WHERE quota_name = ? AND cycle_end IS NULL`,
		peak,
		delta,
		quotaName,
	)
	if err != nil {
		return fmt.Errorf("failed to update opencode cycle: %w", err)
	}
	return nil
}

// UpdateOpenCodeCycleResetsAt updates the reset timestamp for an active OpenCode cycle.
func (s *Store) UpdateOpenCodeCycleResetsAt(quotaName string, resetsAt *time.Time) error {
	var resetsAtValue interface{}
	if resetsAt != nil {
		resetsAtValue = resetsAt.Format(time.RFC3339Nano)
	}

	_, err := s.db.Exec(
		`UPDATE opencode_reset_cycles SET resets_at = ?
		WHERE quota_name = ? AND cycle_end IS NULL`,
		resetsAtValue,
		quotaName,
	)
	if err != nil {
		return fmt.Errorf("failed to update opencode cycle resets_at: %w", err)
	}
	return nil
}

// QueryActiveOpenCodeCycle returns the active cycle for an OpenCode quota.
func (s *Store) QueryActiveOpenCodeCycle(quotaName string) (*OpenCodeResetCycle, error) {
	var cycle OpenCodeResetCycle
	var cycleStart string
	var cycleEnd, resetsAt sql.NullString

	err := s.db.QueryRow(
		`SELECT id, quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM opencode_reset_cycles WHERE quota_name = ? AND cycle_end IS NULL`,
		quotaName,
	).Scan(
		&cycle.ID,
		&cycle.QuotaName,
		&cycleStart,
		&cycleEnd,
		&resetsAt,
		&cycle.PeakUtilization,
		&cycle.TotalDelta,
	)

	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query active opencode cycle: %w", err)
	}

	parsedCycleStart, err := parseOpenCodeTime(cycleStart, "opencode cycle_start")
	if err != nil {
		return nil, err
	}
	cycle.CycleStart = parsedCycleStart
	if cycleEnd.Valid {
		parsedCycleEnd, err := parseOpenCodeTime(cycleEnd.String, "opencode cycle_end")
		if err != nil {
			return nil, err
		}
		cycle.CycleEnd = &parsedCycleEnd
	}
	if resetsAt.Valid {
		parsedResetsAt, err := parseOpenCodeTime(resetsAt.String, "opencode cycle resets_at")
		if err != nil {
			return nil, err
		}
		cycle.ResetsAt = &parsedResetsAt
	}

	return &cycle, nil
}

// QueryOpenCodeCycleHistory returns completed cycles for an OpenCode quota with optional limit.
func (s *Store) QueryOpenCodeCycleHistory(quotaName string, limit ...int) ([]*OpenCodeResetCycle, error) {
	query := `SELECT id, quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM opencode_reset_cycles WHERE quota_name = ? AND cycle_end IS NOT NULL ORDER BY cycle_start DESC`
	args := []interface{}{quotaName}
	if len(limit) > 0 && limit[0] > 0 {
		query += ` LIMIT ?`
		args = append(args, limit[0])
	}

	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query opencode cycles: %w", err)
	}
	defer rows.Close()

	var cycles []*OpenCodeResetCycle
	for rows.Next() {
		var cycle OpenCodeResetCycle
		var cycleStart, cycleEnd string
		var resetsAt sql.NullString

		if err := rows.Scan(
			&cycle.ID,
			&cycle.QuotaName,
			&cycleStart,
			&cycleEnd,
			&resetsAt,
			&cycle.PeakUtilization,
			&cycle.TotalDelta,
		); err != nil {
			return nil, fmt.Errorf("failed to scan opencode cycle: %w", err)
		}

		parsedCycleStart, err := parseOpenCodeTime(cycleStart, "opencode cycle_start")
		if err != nil {
			return nil, err
		}
		cycle.CycleStart = parsedCycleStart

		parsedCycleEnd, err := parseOpenCodeTime(cycleEnd, "opencode cycle_end")
		if err != nil {
			return nil, err
		}
		cycle.CycleEnd = &parsedCycleEnd
		if resetsAt.Valid {
			parsedResetsAt, err := parseOpenCodeTime(resetsAt.String, "opencode cycle resets_at")
			if err != nil {
				return nil, err
			}
			cycle.ResetsAt = &parsedResetsAt
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

// QueryOpenCodeCyclesSince returns completed cycles for a quota since a given time.
func (s *Store) QueryOpenCodeCyclesSince(quotaName string, since time.Time) ([]*OpenCodeResetCycle, error) {
	rows, err := s.db.Query(
		`SELECT id, quota_name, cycle_start, cycle_end, resets_at, peak_utilization, total_delta
		FROM opencode_reset_cycles WHERE quota_name = ? AND cycle_end IS NOT NULL AND cycle_start >= ?
		ORDER BY cycle_start DESC`,
		quotaName,
		since.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query opencode cycles since: %w", err)
	}
	defer rows.Close()

	var cycles []*OpenCodeResetCycle
	for rows.Next() {
		var cycle OpenCodeResetCycle
		var cycleStart, cycleEnd string
		var resetsAt sql.NullString

		if err := rows.Scan(
			&cycle.ID,
			&cycle.QuotaName,
			&cycleStart,
			&cycleEnd,
			&resetsAt,
			&cycle.PeakUtilization,
			&cycle.TotalDelta,
		); err != nil {
			return nil, fmt.Errorf("failed to scan opencode cycle: %w", err)
		}

		parsedCycleStart, err := parseOpenCodeTime(cycleStart, "opencode cycle_start")
		if err != nil {
			return nil, err
		}
		cycle.CycleStart = parsedCycleStart

		parsedCycleEnd, err := parseOpenCodeTime(cycleEnd, "opencode cycle_end")
		if err != nil {
			return nil, err
		}
		cycle.CycleEnd = &parsedCycleEnd
		if resetsAt.Valid {
			parsedResetsAt, err := parseOpenCodeTime(resetsAt.String, "opencode cycle resets_at")
			if err != nil {
				return nil, err
			}
			cycle.ResetsAt = &parsedResetsAt
		}

		cycles = append(cycles, &cycle)
	}

	return cycles, rows.Err()
}

// QueryOpenCodeUtilizationSeries returns per-quota utilization points since a given time.
func (s *Store) QueryOpenCodeUtilizationSeries(quotaName string, since time.Time) ([]UtilizationPoint, error) {
	rows, err := s.db.Query(
		`SELECT s.captured_at, qv.utilization
		FROM opencode_quota_values qv
		JOIN opencode_snapshots s ON s.id = qv.snapshot_id
		WHERE qv.quota_name = ? AND s.captured_at >= ?
		ORDER BY s.captured_at ASC`,
		quotaName,
		since.UTC().Format(time.RFC3339Nano),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query opencode utilization series: %w", err)
	}
	defer rows.Close()

	var points []UtilizationPoint
	for rows.Next() {
		var capturedAt string
		var util float64
		if err := rows.Scan(&capturedAt, &util); err != nil {
			return nil, fmt.Errorf("failed to scan opencode utilization point: %w", err)
		}
		parsedCapturedAt, err := parseOpenCodeTime(capturedAt, "opencode utilization captured_at")
		if err != nil {
			return nil, err
		}
		points = append(points, UtilizationPoint{CapturedAt: parsedCapturedAt, Utilization: util})
	}

	return points, rows.Err()
}

// QueryAllOpenCodeQuotaNames returns all distinct quota names from OpenCode quota values.
func (s *Store) QueryAllOpenCodeQuotaNames() ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT quota_name FROM opencode_quota_values ORDER BY quota_name`,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to query opencode quota names: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan opencode quota name: %w", err)
		}
		names = append(names, name)
	}

	return names, rows.Err()
}
