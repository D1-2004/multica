package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

var fcE2BAPISandboxIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

func (l *FCE2BLauncher) employeeHostTimeoutSeconds() int {
	if l == nil {
		return dshhost.DefaultSandboxTaskTimeoutSeconds
	}
	return dshhost.SandboxTaskTimeoutSeconds(l.Config.TimeoutSeconds)
}

// sandboxTaskTimeout is the lifetime applied at generic task-sandbox create
// and renewal. Config may raise it; values below the default floor are raised.
func (l *FCE2BLauncher) sandboxTaskTimeout() time.Duration {
	seconds := defaultFCE2BTimeoutSeconds
	if l != nil && l.Config.TimeoutSeconds > seconds {
		seconds = l.Config.TimeoutSeconds
	}
	return time.Duration(seconds) * time.Second
}

func (l *FCE2BLauncher) sandboxTaskTimeoutSeconds() int {
	return int(l.sandboxTaskTimeout() / time.Second)
}

func (l *FCE2BLauncher) sandboxLifetimeRequest(ctx context.Context, method, id, suffix string, body []byte) ([]byte, error) {
	baseURL := strings.TrimRight(l.Config.APIURL, "/")
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.Fragment != "" ||
		(base.Scheme != "https" && base.Scheme != "http") || strings.TrimSpace(l.Config.APIKey) == "" || !fcE2BAPISandboxIDPattern.MatchString(id) {
		return nil, errors.New("invalid FC/E2B sandbox lifetime configuration or identifier")
	}
	req, err := http.NewRequestWithContext(ctx, method, baseURL+"/sandboxes/"+id+suffix, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("create FC/E2B sandbox lifetime request failed")
	}
	req.Header.Set("X-API-KEY", l.Config.APIKey)
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(req)
	if err != nil {
		return nil, errors.New("FC/E2B sandbox lifetime transport failed")
	}
	defer response.Body.Close()
	if method == http.MethodDelete && response.StatusCode == http.StatusNotFound {
		return nil, nil
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, fmt.Errorf("FC/E2B sandbox lifetime returned HTTP %d", response.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, (16<<10)+1))
	if err != nil || len(data) > 16<<10 {
		return nil, errors.New("invalid FC/E2B sandbox lifetime response")
	}
	return data, nil
}

func (l *FCE2BLauncher) renewSandboxForTask(ctx context.Context, sandboxID string, trace chattrace.Trace) (expiresAt time.Time, err error) {
	return l.renewSandboxTimeout(ctx, sandboxID, trace, l.sandboxTaskTimeoutSeconds())
}

func (l *FCE2BLauncher) renewEmployeeHostSandbox(ctx context.Context, sandboxID string, trace chattrace.Trace) (expiresAt time.Time, err error) {
	return l.renewSandboxTimeout(ctx, sandboxID, trace, l.employeeHostTimeoutSeconds())
}

func (l *FCE2BLauncher) renewSandboxTimeout(ctx context.Context, sandboxID string, trace chattrace.Trace, timeoutSeconds int) (expiresAt time.Time, err error) {
	started := time.Now()
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_renew", "started", "sandbox_id", sandboxID, "timeout_seconds", timeoutSeconds)
	defer func() {
		if err != nil {
			chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_renew", "failed", "sandbox_id", sandboxID, "stage_elapsed_ms", time.Since(started).Milliseconds(), "error", err)
		} else {
			chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_renew", "succeeded", "sandbox_id", sandboxID, "timeout_seconds", timeoutSeconds, "expires_at", expiresAt, "stage_elapsed_ms", time.Since(started).Milliseconds())
		}
	}()
	body, _ := json.Marshal(map[string]int64{"timeout": int64(timeoutSeconds)})
	if _, err = l.sandboxLifetimeRequest(ctx, http.MethodPost, sandboxID, "/timeout", body); err != nil {
		return time.Time{}, err
	}
	data, err := l.sandboxLifetimeRequest(ctx, http.MethodGet, sandboxID, "", nil)
	if err != nil {
		return time.Time{}, err
	}
	var info struct {
		ID    string    `json:"sandboxID"`
		State string    `json:"state"`
		EndAt time.Time `json:"endAt"`
	}
	// The provider may truncate timestamps to seconds; allow a small clock skew.
	if err := json.Unmarshal(data, &info); err != nil || info.ID != sandboxID || info.State != "running" ||
		info.EndAt.Before(started.Add(time.Duration(timeoutSeconds)*time.Second-5*time.Second)) {
		return time.Time{}, fmt.Errorf("FC/E2B sandbox renewal did not confirm %d seconds of lifetime", timeoutSeconds)
	}
	return info.EndAt, nil
}

func (l *FCE2BLauncher) releaseUnusedSandbox(ctx context.Context, sandboxID string, trace chattrace.Trace) {
	// Cleanup has a separate, bounded budget when task startup was cancelled.
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if _, err := l.sandboxLifetimeRequest(cleanupCtx, http.MethodDelete, sandboxID, "", nil); err != nil {
		chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_release", "failed", "sandbox_id", sandboxID, "error", err)
		return
	}
	chattrace.LogStage(slog.Default(), trace, "fc_e2b_sandbox_release", "succeeded", "sandbox_id", sandboxID)
}
