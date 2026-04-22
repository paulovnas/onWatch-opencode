package agent

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/onllm-dev/onwatch/v2/internal/api"
	"github.com/onllm-dev/onwatch/v2/internal/notify"
	"github.com/onllm-dev/onwatch/v2/internal/store"
	"github.com/onllm-dev/onwatch/v2/internal/tracker"
)

const maxOpenCodeAuthFailures = 3

// OpenCodeCookieRefreshFunc is called before each poll to get a fresh OpenCode cookie.
type OpenCodeCookieRefreshFunc func() string

// OpenCodeAgent manages the background polling loop for OpenCode quota tracking.
type OpenCodeAgent struct {
	client       *api.OpenCodeClient
	store        *store.Store
	tracker      *tracker.OpenCodeTracker
	interval     time.Duration
	logger       *slog.Logger
	sm           *SessionManager
	notifier     *notify.NotificationEngine
	pollingCheck func() bool
	cookieRefresh OpenCodeCookieRefreshFunc
	lastCookie   string

	// Auth failure rate limiting
	authFailCount   int
	authPaused      bool
	lastFailedCookie string
}

// NewOpenCodeAgent creates a new OpenCodeAgent with the given dependencies.
func NewOpenCodeAgent(client *api.OpenCodeClient, st *store.Store, tracker *tracker.OpenCodeTracker, interval time.Duration, logger *slog.Logger, sm *SessionManager) *OpenCodeAgent {
	if logger == nil {
		logger = slog.Default()
	}
	return &OpenCodeAgent{
		client:   client,
		store:    st,
		tracker:  tracker,
		interval: interval,
		logger:   logger,
		sm:       sm,
	}
}

// SetPollingCheck sets a function called before each poll.
func (a *OpenCodeAgent) SetPollingCheck(fn func() bool) {
	a.pollingCheck = fn
}

// SetNotifier sets notification engine for sending alerts.
func (a *OpenCodeAgent) SetNotifier(n *notify.NotificationEngine) {
	a.notifier = n
}

// SetCookieRefresh sets a function called before each poll to refresh OpenCode cookie.
func (a *OpenCodeAgent) SetCookieRefresh(fn OpenCodeCookieRefreshFunc) {
	a.cookieRefresh = fn
}

// Run starts the agent polling loop.
func (a *OpenCodeAgent) Run(ctx context.Context) error {
	a.logger.Info("OpenCode agent started", "interval", a.interval)

	defer func() {
		if a.sm != nil {
			a.sm.Close()
		}
		a.logger.Info("OpenCode agent stopped")
	}()

	a.poll(ctx)

	ticker := time.NewTicker(a.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			a.poll(ctx)
		case <-ctx.Done():
			return nil
		}
	}
}

func (a *OpenCodeAgent) poll(ctx context.Context) {
	if a.pollingCheck != nil && !a.pollingCheck() {
		return
	}

	// Refresh cookie before each poll (picks up rotated credentials)
	if a.cookieRefresh != nil {
		newCookie := a.cookieRefresh()
		if newCookie != "" && newCookie != a.lastCookie {
			a.client.SetCookieHeader(newCookie)
			a.lastCookie = newCookie
			a.logger.Info("OpenCode cookie refreshed")

			// If we were paused due to auth failures and cookie changed, resume.
			if a.authPaused && newCookie != a.lastFailedCookie {
				a.authPaused = false
				a.authFailCount = 0
				a.lastFailedCookie = ""
				a.logger.Info("OpenCode auth failure pause lifted - new cookie detected")
			}
		}
	}

	// If auth is paused, skip polling until cookie changes.
	if a.authPaused {
		return
	}

	snapshot, err := a.client.FetchQuotas(ctx, "")
	if err != nil {
		if ctx.Err() != nil {
			return
		}

		// On auth error, force cookie re-read and retry once.
		if errors.Is(err, api.ErrOpenCodeUnauthorized) && a.cookieRefresh != nil {
			a.logger.Warn("OpenCode auth error, forcing cookie re-read", "error", err)
			a.lastCookie = "" // force re-read even if unchanged
			if retryCookie := a.cookieRefresh(); retryCookie != "" {
				a.client.SetCookieHeader(retryCookie)
				a.lastCookie = retryCookie
				a.logger.Info("Retrying OpenCode poll with refreshed cookie")
				snapshot, err = a.client.FetchQuotas(ctx, "")
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					if errors.Is(err, api.ErrOpenCodeUnauthorized) {
						a.authFailCount++
						a.lastFailedCookie = retryCookie
						a.logger.Error("OpenCode auth retry failed",
							"error", err,
							"failure_count", a.authFailCount,
							"max_failures", maxOpenCodeAuthFailures)

						if a.authFailCount >= maxOpenCodeAuthFailures {
							a.authPaused = true
							a.logger.Error("OpenCode polling PAUSED due to repeated auth failures",
								"failure_count", a.authFailCount,
								"action", "Re-authenticate OpenCode to resume polling")
						}
					} else {
						a.logger.Error("OpenCode retry failed with non-auth error", "error", err)
					}
					return
				}
				// Retry succeeded, reset auth failure count.
				a.authFailCount = 0
			} else {
				a.logger.Error("No OpenCode cookie available after re-read")
				return
			}
		} else {
			a.logger.Error("Failed to fetch OpenCode quotas", "error", err)
			return
		}
	} else {
		// Success, reset auth failure count.
		a.authFailCount = 0
	}

	if _, err := a.store.InsertOpenCodeSnapshot(snapshot); err != nil {
		a.logger.Error("Failed to insert OpenCode snapshot", "error", err)
		return
	}

	if a.tracker != nil {
		if err := a.tracker.Process(snapshot); err != nil {
			a.logger.Error("OpenCode tracker processing failed", "error", err)
		}
	}

	if a.notifier != nil {
		quotas := []api.OpenCodeQuota{snapshot.RollingUsage, snapshot.WeeklyUsage}
		if snapshot.HasMonthlyUsage {
			quotas = append(quotas, snapshot.MonthlyUsage)
		}
		for _, q := range quotas {
			a.notifier.Check(notify.QuotaStatus{
				Provider:    "opencode",
				QuotaKey:    q.Name,
				Utilization: q.Utilization,
				Limit:       100, // OpenCode uses percentages
			})
		}
	}

	if a.sm != nil {
		values := []float64{
			snapshot.RollingUsage.Utilization,
			snapshot.WeeklyUsage.Utilization,
		}
		if snapshot.HasMonthlyUsage {
			values = append(values, snapshot.MonthlyUsage.Utilization)
		}
		a.sm.ReportPoll(values)
	}

	a.logger.Info("OpenCode poll complete",
		"workspace_id", snapshot.WorkspaceID,
		"rolling_percent", snapshot.RollingUsage.Utilization,
		"weekly_percent", snapshot.WeeklyUsage.Utilization,
		"has_monthly", snapshot.HasMonthlyUsage,
	)
}
