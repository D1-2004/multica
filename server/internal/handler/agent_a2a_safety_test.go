package handler

import "testing"

func TestEvaluateAgentA2ARuntimeSafety(t *testing.T) {
	tests := []struct {
		name          string
		appEnv        string
		allowUnsafe   string
		publicURL     string
		wantAllowed   bool
		wantPublicURL string
	}{
		{
			name:          "explicit local development localhost",
			appEnv:        "development",
			allowUnsafe:   "true",
			publicURL:     "http://localhost:8080/",
			wantAllowed:   true,
			wantPublicURL: "http://localhost:8080",
		},
		{
			name:          "unset non-production environment",
			allowUnsafe:   "TRUE",
			publicURL:     "https://127.44.5.6/base/",
			wantAllowed:   true,
			wantPublicURL: "https://127.44.5.6/base",
		},
		{
			name:          "exact IPv6 loopback",
			appEnv:        "test",
			allowUnsafe:   " true ",
			publicURL:     "http://[::1]:8080",
			wantAllowed:   true,
			wantPublicURL: "http://[::1]:8080",
		},
		{
			name:          "override absent",
			appEnv:        "development",
			publicURL:     "http://127.0.0.1:8080",
			wantPublicURL: "http://127.0.0.1:8080",
		},
		{
			name:          "override false",
			appEnv:        "test",
			allowUnsafe:   "false",
			publicURL:     "http://localhost:8080",
			wantPublicURL: "http://localhost:8080",
		},
		{
			name:          "production wins over override",
			appEnv:        " ProDucTion ",
			allowUnsafe:   "true",
			publicURL:     "http://127.0.0.1:8080",
			wantPublicURL: "http://127.0.0.1:8080",
		},
		{
			name:          "unspecified address is not loopback",
			appEnv:        "development",
			allowUnsafe:   "true",
			publicURL:     "https://0.0.0.0:8080",
			wantPublicURL: "https://0.0.0.0:8080",
		},
		{
			name:          "public domain is rejected",
			appEnv:        "development",
			allowUnsafe:   "true",
			publicURL:     "https://agents.example.test",
			wantPublicURL: "https://agents.example.test",
		},
		{
			name:          "localhost suffix is rejected",
			appEnv:        "development",
			allowUnsafe:   "true",
			publicURL:     "https://localhost.example.test",
			wantPublicURL: "https://localhost.example.test",
		},
		{
			name:          "localhost trailing dot is rejected",
			appEnv:        "development",
			allowUnsafe:   "true",
			publicURL:     "https://localhost.",
			wantPublicURL: "https://localhost.",
		},
		{
			name:          "IPv4 mapped IPv6 is rejected",
			appEnv:        "development",
			allowUnsafe:   "true",
			publicURL:     "https://[::ffff:127.0.0.1]",
			wantPublicURL: "https://[::ffff:127.0.0.1]",
		},
		{
			name:          "expanded IPv6 loopback is not exact",
			appEnv:        "development",
			allowUnsafe:   "true",
			publicURL:     "https://[0:0:0:0:0:0:0:1]",
			wantPublicURL: "https://[0:0:0:0:0:0:0:1]",
		},
		{
			name:        "invalid public URL",
			appEnv:      "development",
			allowUnsafe: "true",
			publicURL:   "http://agents.example.test",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("APP_ENV", test.appEnv)
			t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, test.allowUnsafe)

			decision := evaluateAgentA2ARuntimeSafety(test.publicURL)
			if decision.Allowed != test.wantAllowed {
				t.Fatalf("Allowed = %v, want %v", decision.Allowed, test.wantAllowed)
			}
			if decision.PublicBaseURL != test.wantPublicURL {
				t.Fatalf("PublicBaseURL = %q, want %q", decision.PublicBaseURL, test.wantPublicURL)
			}
		})
	}
}

func TestEvaluateAgentA2ALocalRequestSafetyRequiresLoopbackSocketPeer(t *testing.T) {
	allowUnsafeLocalAgentA2ARuntimeForTest(t)
	const publicURL = "http://127.0.0.1:8080"
	tests := []struct {
		name       string
		remoteAddr string
		want       bool
	}{
		{name: "IPv4 loopback", remoteAddr: "127.0.0.1:49152", want: true},
		{name: "other IPv4 loopback", remoteAddr: "127.99.4.3:49152", want: true},
		{name: "exact IPv6 loopback", remoteAddr: "[::1]:49152", want: true},
		{name: "LAN peer", remoteAddr: "192.168.1.20:49152"},
		{name: "unspecified peer", remoteAddr: "0.0.0.0:49152"},
		{name: "localhost name is not a socket IP", remoteAddr: "localhost:49152"},
		{name: "IPv4 mapped IPv6 peer", remoteAddr: "[::ffff:127.0.0.1]:49152"},
		{name: "expanded IPv6 loopback is not exact", remoteAddr: "[0:0:0:0:0:0:0:1]:49152"},
		{name: "missing port", remoteAddr: "127.0.0.1"},
		{name: "empty peer"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			decision := evaluateAgentA2ALocalRequestSafety(publicURL, test.remoteAddr)
			if decision.Allowed != test.want {
				t.Fatalf("Allowed = %v, want %v", decision.Allowed, test.want)
			}
		})
	}
}

func allowUnsafeLocalAgentA2ARuntimeForTest(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "test")
	t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, "true")
}
