package handler

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	githubPreWebhookBodyLimit = 10 << 20
	githubPreWebhookTimeout   = 8 * time.Second
	githubWebhookForwarded    = "X-Multica-GitHub-Forwarded"
)

var githubPreWebhookHeaders = []string{
	"X-Hub-Signature-256",
	"X-GitHub-Event",
	"X-GitHub-Delivery",
	"X-GitHub-Hook-ID",
	"X-GitHub-Hook-Installation-Target-ID",
	"X-GitHub-Hook-Installation-Target-Type",
}

var defaultGitHubPreWebhookTransport http.RoundTripper = &http.Transport{
	Proxy:                 nil,
	DialContext:           (&net.Dialer{Timeout: 3 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
	TLSHandshakeTimeout:   3 * time.Second,
	ResponseHeaderTimeout: githubPreWebhookTimeout,
	IdleConnTimeout:       90 * time.Second,
	MaxIdleConns:          32,
	MaxIdleConnsPerHost:   8,
	DisableCompression:    true,
	ForceAttemptHTTP2:     true,
}

func githubPreWebhookTarget(cfg Config) (*url.URL, error) {
	u, err := url.Parse(cfg.GitHubPreWebhookURL)
	if err != nil || u.Scheme != "https" || u.Hostname() == "" || u.User != nil ||
		(u.Port() != "" && u.Port() != "443") || u.Path != "/api/webhooks/github" ||
		u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return nil, errors.New("invalid pre-release webhook destination")
	}
	for _, origin := range []string{cfg.FrontendOrigin, cfg.PublicURL, cfg.AppURL} {
		own, err := url.Parse(origin)
		if err == nil && own.Hostname() != "" && strings.EqualFold(own.Hostname(), u.Hostname()) {
			return nil, errors.New("pre-release webhook destination is this deployment")
		}
	}
	return u, nil
}

// ForwardGitHubPreWebhook authenticates a separate App and forwards its original
// delivery to pre-release. It never invokes this deployment's domain handlers.
func (h *Handler) ForwardGitHubPreWebhook(w http.ResponseWriter, r *http.Request) {
	cfg := h.currentConfig()
	if cfg.GitHubPreWebhookURL == "" || cfg.GitHubPreWebhookSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "pre-release GitHub webhook forwarding is not configured")
		return
	}
	target, err := githubPreWebhookTarget(cfg)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "pre-release GitHub webhook forwarding is misconfigured")
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, githubPreWebhookBodyLimit)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "webhook body is too large")
		} else {
			writeError(w, http.StatusBadRequest, "read webhook body failed")
		}
		return
	}
	if !verifyWebhookSignature(cfg.GitHubPreWebhookSecret, r.Header.Get("X-Hub-Signature-256"), body) {
		writeError(w, http.StatusUnauthorized, "invalid signature")
		return
	}
	if r.Header.Get(githubWebhookForwarded) != "" {
		writeError(w, http.StatusLoopDetected, "GitHub webhook forwarding loop detected")
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, target.String(), bytes.NewReader(body))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "pre-release GitHub webhook forwarding is misconfigured")
		return
	}
	for _, key := range githubPreWebhookHeaders {
		if value := r.Header.Get(key); value != "" {
			req.Header.Set(key, value)
		}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(githubWebhookForwarded, "pre")
	transport := h.githubPreWebhookTransport
	if transport == nil {
		transport = defaultGitHubPreWebhookTransport
	}
	client := &http.Client{
		Transport: transport,
		Timeout:   githubPreWebhookTimeout,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		slog.Warn("GitHub pre-release webhook forwarding failed", "event", "github_pre_webhook_failed", "error_class", "transport")
		writeError(w, http.StatusBadGateway, "pre-release GitHub webhook delivery failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Warn("GitHub pre-release webhook forwarding rejected", "event", "github_pre_webhook_failed", "upstream_status", resp.StatusCode)
		writeError(w, http.StatusBadGateway, "pre-release GitHub webhook delivery failed")
		return
	}
	slog.Info("GitHub webhook forwarded to pre-release", "event", "github_pre_webhook_forwarded",
		"delivery_id", r.Header.Get("X-GitHub-Delivery"), "github_event", r.Header.Get("X-GitHub-Event"), "upstream_status", resp.StatusCode)
	writeJSON(w, http.StatusOK, map[string]string{"ok": "forwarded"})
}
