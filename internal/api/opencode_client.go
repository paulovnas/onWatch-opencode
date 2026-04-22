package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"sync"
	"time"
)

var (
	ErrOpenCodeUnauthorized    = errors.New("opencode: unauthorized - invalid or expired cookie")
	ErrOpenCodeNetworkError    = errors.New("opencode: network error")
	ErrOpenCodeInvalidResponse = errors.New("opencode: invalid response")
	ErrOpenCodeParseFailed     = errors.New("opencode: parse failed")
)

const (
	OpenCodeBaseURL      = "https://opencode.ai"
	OpenCodeServerURL    = "https://opencode.ai/_server"
	OpenCodeWorkspacesID = "def39973159c7f0483d8793a822b8dbb10d067e12c65455fcb4608459ba0234f"
	OpenCodeUserAgent    = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/143.0.0.0 Safari/537.36"
)

type OpenCodeClient struct {
	httpClient   *http.Client
	cookieHeader string
	cookieMu     sync.RWMutex
	baseURL      string
	serverURL    string
	logger       *slog.Logger
}

type OpenCodeOption func(*OpenCodeClient)

func WithOpenCodeBaseURL(url string) OpenCodeOption {
	return func(c *OpenCodeClient) {
		c.baseURL = url
	}
}

func WithOpenCodeTimeout(timeout time.Duration) OpenCodeOption {
	return func(c *OpenCodeClient) {
		c.httpClient.Timeout = timeout
	}
}

// NormalizeOpenCodeCookie converts a raw auth cookie value into a valid
// Cookie header value expected by OpenCode requests.
func NormalizeOpenCodeCookie(raw string) string {
	cookie := strings.TrimSpace(raw)
	if cookie == "" {
		return ""
	}

	lower := strings.ToLower(cookie)
	if strings.HasPrefix(lower, "auth=") || strings.Contains(lower, ";auth=") || strings.Contains(lower, "; auth=") {
		return cookie
	}
	return "auth=" + cookie
}

func NewOpenCodeClient(cookieHeader string, logger *slog.Logger, opts ...OpenCodeOption) *OpenCodeClient {
	client := &OpenCodeClient{
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				MaxIdleConns:          1,
				MaxIdleConnsPerHost:   1,
				ResponseHeaderTimeout: 30 * time.Second,
				IdleConnTimeout:       30 * time.Second,
				TLSHandshakeTimeout:   10 * time.Second,
				ForceAttemptHTTP2:     true,
			},
		},
		cookieHeader: NormalizeOpenCodeCookie(cookieHeader),
		baseURL:      OpenCodeBaseURL,
		serverURL:    OpenCodeServerURL,
		logger:       logger,
	}

	for _, opt := range opts {
		opt(client)
	}

	return client
}

// DetectOpenCodeCookie attempts to auto-detect the OpenCode cookie.
// Returns empty string if not found.
// For now, this checks environment variable. Future versions may detect from browser cookies.
func DetectOpenCodeCookie(logger *slog.Logger) string {
	// Check environment variable first
	if cookie := os.Getenv("OPENCODE_COOKIE"); cookie != "" {
		return NormalizeOpenCodeCookie(cookie)
	}

	// TODO: Add browser cookie detection for future versions
	// This would involve reading browser cookie stores on different platforms

	return ""
}

func (c *OpenCodeClient) SetCookieHeader(cookieHeader string) {
	c.cookieMu.Lock()
	defer c.cookieMu.Unlock()
	c.cookieHeader = NormalizeOpenCodeCookie(cookieHeader)
}

func (c *OpenCodeClient) getCookieHeader() string {
	c.cookieMu.RLock()
	defer c.cookieMu.RUnlock()
	return c.cookieHeader
}

func (c *OpenCodeClient) GetCookieHeader() string {
	return c.getCookieHeader()
}

func (c *OpenCodeClient) FetchQuotas(ctx context.Context, workspaceIDOverride string) (*OpenCodeSnapshot, error) {
	cookieHeader := c.getCookieHeader()
	if cookieHeader == "" {
		return nil, ErrOpenCodeUnauthorized
	}

	workspaceID := workspaceIDOverride
	if workspaceID == "" {
		var err error
		workspaceID, err = c.fetchWorkspaceID(ctx, cookieHeader)
		if err != nil {
			return nil, fmt.Errorf("opencode: fetch workspace ID: %w", err)
		}
	}

	html, err := c.fetchUsagePage(ctx, workspaceID, cookieHeader)
	if err != nil {
		return nil, fmt.Errorf("opencode: fetch usage page: %w", err)
	}

	usage, err := ParseOpenCodeUsageFromHTML(html, time.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("opencode: parse usage: %w", err)
	}

	snapshot := ToOpenCodeSnapshot(workspaceID, usage)
	c.logger.Debug("opencode: quotas fetched",
		"workspace_id", workspaceID,
		"rolling_percent", usage.RollingUsagePercent,
		"weekly_percent", usage.WeeklyUsagePercent,
		"monthly_percent", usage.MonthlyUsagePercent,
		"has_monthly", usage.HasMonthlyUsage,
	)

	return snapshot, nil
}

func (c *OpenCodeClient) fetchWorkspaceID(ctx context.Context, cookieHeader string) (string, error) {
	// Try GET first
	text, err := c.fetchServerText(ctx, cookieHeader, OpenCodeWorkspacesID, "GET", "")
	if err != nil {
		return "", err
	}

	if c.looksSignedOut(text) {
		return "", ErrOpenCodeUnauthorized
	}

	ids := c.parseWorkspaceIDs(text)
	if len(ids) == 0 {
		// Try POST as fallback
		c.logger.Debug("opencode: workspace IDs missing after GET, retrying with POST")
		text, err = c.fetchServerText(ctx, cookieHeader, OpenCodeWorkspacesID, "POST", "[]")
		if err != nil {
			return "", err
		}
		if c.looksSignedOut(text) {
			return "", ErrOpenCodeUnauthorized
		}
		ids = c.parseWorkspaceIDs(text)
		if len(ids) == 0 {
			return "", ErrOpenCodeParseFailed
		}
	}

	return ids[0], nil
}

func (c *OpenCodeClient) fetchUsagePage(ctx context.Context, workspaceID, cookieHeader string) (string, error) {
	usageURL := fmt.Sprintf("%s/workspace/%s/go", c.baseURL, workspaceID)

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, usageURL, nil)
	if err != nil {
		return "", fmt.Errorf("opencode: create usage request: %w", err)
	}

	req.Header.Set("Cookie", cookieHeader)
	req.Header.Set("User-Agent", OpenCodeUserAgent)
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%w: %v", ErrOpenCodeNetworkError, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("%w: reading body: %v", ErrOpenCodeInvalidResponse, err)
	}

	switch {
	case resp.StatusCode == http.StatusOK:
		// Continue
	case resp.StatusCode == http.StatusUnauthorized:
		return "", ErrOpenCodeUnauthorized
	case resp.StatusCode == http.StatusForbidden:
		return "", ErrOpenCodeUnauthorized
	case resp.StatusCode >= 500:
		return "", fmt.Errorf("opencode: server error %d", resp.StatusCode)
	default:
		return "", fmt.Errorf("opencode: unexpected status %d", resp.StatusCode)
	}

	html := string(body)
	if c.looksSignedOut(html) {
		return "", ErrOpenCodeUnauthorized
	}

	// Validate that usage fields are present
	if !c.hasUsageFields(html) {
		c.logger.Error("opencode: usage page payload missing usage fields")
		return "", ErrOpenCodeParseFailed
	}

	return html, nil
}

func (c *OpenCodeClient) fetchServerText(ctx context.Context, cookieHeader, serverID, method string, args string) (string, error) {
	reqURL := c.serverURL
	if method == "GET" {
		baseURL, err := url.Parse(c.serverURL)
		if err != nil {
			return "", err
		}
		query := baseURL.Query()
		query.Set("id", serverID)
		if args != "" {
			query.Set("args", args)
		}
		baseURL.RawQuery = query.Encode()
		reqURL = baseURL.String()
	}

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, method, reqURL, nil)
	if err != nil {
		return "", fmt.Errorf("opencode: create server request: %w", err)
	}

	req.Header.Set("Cookie", cookieHeader)
	req.Header.Set("X-Server-Id", serverID)
	req.Header.Set("X-Server-Instance", "server-fn:"+generateUUID())
	req.Header.Set("User-Agent", OpenCodeUserAgent)
	req.Header.Set("Origin", c.baseURL)
	req.Header.Set("Referer", c.baseURL)
	req.Header.Set("Accept", "text/javascript, application/json;q=0.9, */*;q=0.8")

	if method != "GET" && args != "" {
		req.Header.Set("Content-Type", "application/json")
		req.Body = io.NopCloser(strings.NewReader(args))
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", fmt.Errorf("%w: %v", ErrOpenCodeNetworkError, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return "", fmt.Errorf("%w: reading body: %v", ErrOpenCodeInvalidResponse, err)
	}

	if resp.StatusCode != http.StatusOK {
		bodyText := string(body)
		if c.looksSignedOut(bodyText) {
			return "", ErrOpenCodeUnauthorized
		}
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			return "", ErrOpenCodeUnauthorized
		}
		return "", fmt.Errorf("opencode: server returned %d", resp.StatusCode)
	}

	return string(body), nil
}

func (c *OpenCodeClient) parseWorkspaceIDs(text string) []string {
	pattern := `id\s*:\s*"(wrk_[^"]+)"`
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil
	}

	matches := re.FindAllStringSubmatch(text, -1)
	var ids []string
	for _, match := range matches {
		if len(match) > 1 {
			ids = append(ids, match[1])
		}
	}
	return ids
}

func (c *OpenCodeClient) looksSignedOut(text string) bool {
	lower := strings.ToLower(text)
	return strings.Contains(lower, "login") ||
		strings.Contains(lower, "sign in") ||
		strings.Contains(lower, "auth/authorize") ||
		strings.Contains(lower, "not associated with an account") ||
		strings.Contains(lower, "actor of type \"public\"")
}

func (c *OpenCodeClient) hasUsageFields(html string) bool {
	// Check if either JSON parsing or regex extraction would work
	rollingPattern := `rollingUsage[^}]*?usagePercent\s*:\s*([0-9]+(?:\.[0-9]+)?)`
	re, err := regexp.Compile(rollingPattern)
	if err == nil && re.MatchString(html) {
		return true
	}
	return false
}

func generateUUID() string {
	// Simple UUID v4 generator
	return fmt.Sprintf("%x-%x-%x-%x-%x",
		randUint32(), randUint16(), randUint16(),
		randUint16(), randUint48())
}

func randUint32() uint32 {
	return uint32(time.Now().UnixNano())
}

func randUint16() uint16 {
	return uint16(time.Now().UnixNano())
}

func randUint48() uint64 {
	return uint64(time.Now().UnixNano())
}

func NormalizeWorkspaceID(raw string) string {
	if raw == "" {
		return ""
	}
	trimmed := strings.TrimSpace(raw)

	// Already in correct format
	if strings.HasPrefix(trimmed, "wrk_") && len(trimmed) > 4 {
		return trimmed
	}

	// Extract from URL
	if u, err := url.Parse(trimmed); err == nil {
		parts := strings.Split(u.Path, "/")
		for i, part := range parts {
			if part == "workspace" && i+1 < len(parts) {
				candidate := parts[i+1]
				if strings.HasPrefix(candidate, "wrk_") && len(candidate) > 4 {
					return candidate
				}
			}
		}
	}

	// Extract using regex
	re, err := regexp.Compile(`wrk_[A-Za-z0-9]+`)
	if err == nil {
		if match := re.FindString(trimmed); match != "" {
			return match
		}
	}

	return ""
}
