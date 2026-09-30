package handler

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type githubPreWebhookRoundTrip func(*http.Request) (*http.Response, error)

func (f githubPreWebhookRoundTrip) RoundTrip(r *http.Request) (*http.Response, error) {
	return f(r)
}

func githubPreWebhookRequest(body, secret string) *http.Request {
	r := httptest.NewRequest(http.MethodPost, "/api/webhooks/github/pre", strings.NewReader(body))
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	r.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(mac.Sum(nil)))
	r.Header.Set("X-GitHub-Event", "installation")
	r.Header.Set("X-GitHub-Delivery", "919ebeda-55fb-4c2f-906b-0956498961ac")
	return r
}

func githubPreWebhookTestConfig() Config {
	return Config{
		GitHubPreWebhookURL:    "https://pre.example.com/api/webhooks/github",
		GitHubPreWebhookSecret: "pre-app-webhook-secret",
		FrontendOrigin:         "https://prod.example.com",
	}
}

func TestGitHubPreWebhookPreservesDeliveryAndIsolatesCredentials(t *testing.T) {
	body := "{ \"action\": \"created\", \"note\": \"中文\" }\n"
	cfg := githubPreWebhookTestConfig()
	r := githubPreWebhookRequest(body, cfg.GitHubPreWebhookSecret)
	r.URL.RawQuery = "target=https://attacker.example"
	r.Header.Set("Authorization", "Bearer production-user-token")
	r.Header.Set("Cookie", "production-session=private")
	r.Header.Set("X-Forwarded-Host", "attacker.example")
	r.Header.Set("X-GitHub-Hook-ID", "123")
	calls := 0
	h := &Handler{cfg: cfg, githubPreWebhookTransport: githubPreWebhookRoundTrip(func(out *http.Request) (*http.Response, error) {
		calls++
		got, err := io.ReadAll(out.Body)
		if err != nil || string(got) != body {
			t.Fatalf("raw delivery changed: %v", err)
		}
		if out.URL.String() != cfg.GitHubPreWebhookURL || out.Method != http.MethodPost {
			t.Fatal("request-controlled destination or wrong method")
		}
		for _, key := range []string{"X-Hub-Signature-256", "X-GitHub-Event", "X-GitHub-Delivery", "X-GitHub-Hook-ID"} {
			if out.Header.Get(key) != r.Header.Get(key) {
				t.Fatalf("lost delivery header %s", key)
			}
		}
		for _, key := range []string{"Authorization", "Cookie", "X-Forwarded-Host"} {
			if out.Header.Get(key) != "" {
				t.Fatalf("leaked caller credential/routing header %s", key)
			}
		}
		if !verifyWebhookSignature(cfg.GitHubPreWebhookSecret, out.Header.Get("X-Hub-Signature-256"), got) || out.Header.Get(githubWebhookForwarded) != "pre" {
			t.Fatal("pre-release cannot authenticate the forwarded delivery")
		}
		deadline, ok := out.Context().Deadline()
		if !ok || time.Until(deadline) > githubPreWebhookTimeout {
			t.Fatal("forwarding attempt has no bounded deadline")
		}
		return &http.Response{StatusCode: http.StatusAccepted, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(""))}, nil
	})}
	w := httptest.NewRecorder()
	h.ForwardGitHubPreWebhook(w, r)
	if w.Code != http.StatusOK || calls != 1 {
		t.Fatalf("delivery failed or retried: status=%d calls=%d", w.Code, calls)
	}
	// Handler has no DB: a successful installation delivery must not have run
	// the production installation handler, which requires a database.
}

func TestGitHubPreWebhookRejectsBeforeNetwork(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*Config, *http.Request)
		status int
	}{
		{"disabled", func(c *Config, _ *http.Request) { c.GitHubPreWebhookSecret = "" }, http.StatusServiceUnavailable},
		{"production-secret", func(_ *Config, r *http.Request) {
			r.Header.Set("X-Hub-Signature-256", githubPreWebhookRequest("{}", "production-app-secret").Header.Get("X-Hub-Signature-256"))
		}, http.StatusUnauthorized},
		{"tampered", func(_ *Config, r *http.Request) { r.Body = io.NopCloser(strings.NewReader("{\"different\":true}")) }, http.StatusUnauthorized},
		{"loop", func(_ *Config, r *http.Request) { r.Header.Set(githubWebhookForwarded, "pre") }, http.StatusLoopDetected},
		{"plain-http", func(c *Config, _ *http.Request) { c.GitHubPreWebhookURL = "http://pre.example.com/api/webhooks/github" }, http.StatusServiceUnavailable},
		{"relay-target", func(c *Config, _ *http.Request) { c.GitHubPreWebhookURL += "/pre" }, http.StatusServiceUnavailable},
		{"same-deployment", func(c *Config, _ *http.Request) { c.GitHubPreWebhookURL = c.FrontendOrigin + "/api/webhooks/github" }, http.StatusServiceUnavailable},
		{"query-target", func(c *Config, _ *http.Request) { c.GitHubPreWebhookURL += "?target=elsewhere" }, http.StatusServiceUnavailable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := githubPreWebhookTestConfig()
			r := githubPreWebhookRequest("{}", cfg.GitHubPreWebhookSecret)
			tc.change(&cfg, r)
			h := &Handler{cfg: cfg, githubPreWebhookTransport: githubPreWebhookRoundTrip(func(*http.Request) (*http.Response, error) {
				t.Fatal("rejected delivery reached the network")
				return nil, nil
			})}
			w := httptest.NewRecorder()
			h.ForwardGitHubPreWebhook(w, r)
			if w.Code != tc.status {
				t.Fatalf("status=%d want=%d", w.Code, tc.status)
			}
		})
	}
}

func TestGitHubPreWebhookRejectsOversizedBody(t *testing.T) {
	cfg := githubPreWebhookTestConfig()
	r := githubPreWebhookRequest(strings.Repeat("x", githubPreWebhookBodyLimit+1), cfg.GitHubPreWebhookSecret)
	h := &Handler{cfg: cfg, githubPreWebhookTransport: githubPreWebhookRoundTrip(func(*http.Request) (*http.Response, error) {
		t.Fatal("oversized delivery reached the network")
		return nil, nil
	})}
	w := httptest.NewRecorder()
	h.ForwardGitHubPreWebhook(w, r)
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized delivery status=%d", w.Code)
	}
}

func TestGitHubPreWebhookDoesNotAcknowledgeOrRetryFailedDelivery(t *testing.T) {
	for _, status := range []int{0, http.StatusFound, http.StatusUnauthorized, http.StatusInternalServerError} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			cfg := githubPreWebhookTestConfig()
			calls := 0
			h := &Handler{cfg: cfg, githubPreWebhookTransport: githubPreWebhookRoundTrip(func(*http.Request) (*http.Response, error) {
				calls++
				if status == 0 {
					return nil, errors.New("private upstream details must not be returned")
				}
				return &http.Response{StatusCode: status, Header: http.Header{"Location": {"https://attacker.example/"}}, Body: io.NopCloser(strings.NewReader("private upstream response"))}, nil
			})}
			w := httptest.NewRecorder()
			h.ForwardGitHubPreWebhook(w, githubPreWebhookRequest("{}", cfg.GitHubPreWebhookSecret))
			if w.Code != http.StatusBadGateway || calls != 1 || strings.Contains(w.Body.String(), "private") {
				t.Fatalf("failure acknowledged, retried, redirected, or exposed: status=%d calls=%d", w.Code, calls)
			}
		})
	}
}
