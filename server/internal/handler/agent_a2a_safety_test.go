package handler

import "testing"

func TestEvaluateAgentA2ARuntimeSafety(t *testing.T) {
	tests := []struct {
		name                        string
		appEnv                      string
		aoneEnvType                 string
		envType                     string
		goEnv                       string
		allowUnsafeLocal            string
		allowUnsafePrerelease       string
		prereleaseExpectedPublicURL string
		publicURL                   string
		wantAllowed                 bool
		wantMode                    agentA2ARuntimeSafetyMode
		wantPublicURL               string
	}{
		{
			name:             "explicit local development localhost",
			appEnv:           "development",
			allowUnsafeLocal: "true",
			publicURL:        "http://localhost:8080/",
			wantAllowed:      true,
			wantMode:         agentA2ARuntimeSafetyModeUnsafeLocal,
			wantPublicURL:    "http://localhost:8080",
		},
		{
			name:             "unset non-production environment",
			allowUnsafeLocal: "TRUE",
			publicURL:        "https://127.44.5.6/base/",
			wantAllowed:      true,
			wantMode:         agentA2ARuntimeSafetyModeUnsafeLocal,
			wantPublicURL:    "https://127.44.5.6/base",
		},
		{
			name:             "exact IPv6 loopback",
			appEnv:           "test",
			allowUnsafeLocal: " true ",
			publicURL:        "http://[::1]:8080",
			wantAllowed:      true,
			wantMode:         agentA2ARuntimeSafetyModeUnsafeLocal,
			wantPublicURL:    "http://[::1]:8080",
		},
		{
			name:          "local override absent",
			appEnv:        "development",
			publicURL:     "http://127.0.0.1:8080",
			wantMode:      agentA2ARuntimeSafetyModeDenied,
			wantPublicURL: "http://127.0.0.1:8080",
		},
		{
			name:             "local override false",
			appEnv:           "test",
			allowUnsafeLocal: "false",
			publicURL:        "http://localhost:8080",
			wantMode:         agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:    "http://localhost:8080",
		},
		{
			name:                        "production wins over every override",
			appEnv:                      " ProDucTion ",
			allowUnsafeLocal:            "true",
			allowUnsafePrerelease:       "true",
			prereleaseExpectedPublicURL: "http://127.0.0.1:8080",
			publicURL:                   "http://127.0.0.1:8080",
			wantMode:                    agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:               "http://127.0.0.1:8080",
		},
		{
			name:             "Aone production marker blocks local test mode",
			appEnv:           "test",
			aoneEnvType:      "prod",
			allowUnsafeLocal: "true",
			publicURL:        "http://127.0.0.1:8080",
			wantMode:         agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:    "http://127.0.0.1:8080",
		},
		{
			name:             "unspecified address is not local E2E loopback",
			appEnv:           "development",
			allowUnsafeLocal: "true",
			publicURL:        "https://0.0.0.0:8080",
			wantMode:         agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:    "https://0.0.0.0:8080",
		},
		{
			name:             "public domain is rejected by local override",
			appEnv:           "development",
			allowUnsafeLocal: "true",
			publicURL:        "https://agents.example.test",
			wantMode:         agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:    "https://agents.example.test",
		},
		{
			name:             "localhost suffix is rejected by local override",
			appEnv:           "development",
			allowUnsafeLocal: "true",
			publicURL:        "https://localhost.example.test",
			wantMode:         agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:    "https://localhost.example.test",
		},
		{
			name:             "localhost trailing dot is rejected by local override",
			appEnv:           "development",
			allowUnsafeLocal: "true",
			publicURL:        "https://localhost.",
			wantMode:         agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:    "https://localhost.",
		},
		{
			name:             "IPv4 mapped IPv6 is rejected by local override",
			appEnv:           "development",
			allowUnsafeLocal: "true",
			publicURL:        "https://[::ffff:127.0.0.1]",
			wantMode:         agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:    "https://[::ffff:127.0.0.1]",
		},
		{
			name:             "expanded IPv6 loopback is not exact local E2E",
			appEnv:           "development",
			allowUnsafeLocal: "true",
			publicURL:        "https://[0:0:0:0:0:0:0:1]",
			wantMode:         agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:    "https://[0:0:0:0:0:0:0:1]",
		},
		{
			name:             "invalid public URL",
			appEnv:           "development",
			allowUnsafeLocal: "true",
			publicURL:        "http://agents.example.test",
			wantMode:         agentA2ARuntimeSafetyModeDenied,
		},
		{
			name:                        "prerelease environment marker unset",
			allowUnsafePrerelease:       "true",
			prereleaseExpectedPublicURL: "https://agents.example.test/a2a",
			publicURL:                   "https://agents.example.test/a2a",
			wantMode:                    agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:               "https://agents.example.test/a2a",
		},
		{
			name:                        "prerelease flag off",
			appEnv:                      "staging",
			prereleaseExpectedPublicURL: "https://agents.example.test/a2a",
			publicURL:                   "https://agents.example.test/a2a",
			wantMode:                    agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:               "https://agents.example.test/a2a",
		},
		{
			name:                        "prerelease expected URL mismatch",
			appEnv:                      "staging",
			allowUnsafePrerelease:       "true",
			prereleaseExpectedPublicURL: "https://other.example.test/a2a",
			publicURL:                   "https://agents.example.test/a2a",
			wantMode:                    agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:               "https://agents.example.test/a2a",
		},
		{
			name:                        "prerelease public HTTP is rejected",
			appEnv:                      "staging",
			allowUnsafePrerelease:       "true",
			prereleaseExpectedPublicURL: "http://agents.example.test/a2a",
			publicURL:                   "http://agents.example.test/a2a",
			wantMode:                    agentA2ARuntimeSafetyModeDenied,
		},
		{
			name:                        "prerelease production is always denied",
			appEnv:                      "production",
			allowUnsafePrerelease:       "true",
			prereleaseExpectedPublicURL: "https://agents.example.test/a2a",
			publicURL:                   "https://agents.example.test/a2a",
			wantMode:                    agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:               "https://agents.example.test/a2a",
		},
		{
			name:                        "prerelease and production marker conflict is denied",
			appEnv:                      "production",
			aoneEnvType:                 "prepub",
			allowUnsafePrerelease:       "true",
			prereleaseExpectedPublicURL: "https://agents.example.test/a2a",
			publicURL:                   "https://agents.example.test/a2a",
			wantMode:                    agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:               "https://agents.example.test/a2a",
		},
		{
			name:                        "prerelease exact normalized HTTPS URL is allowed",
			appEnv:                      "staging",
			aoneEnvType:                 "prepub",
			allowUnsafePrerelease:       " TRUE ",
			prereleaseExpectedPublicURL: "https://agents.example.test/a2a////",
			publicURL:                   " https://agents.example.test/a2a/ ",
			wantAllowed:                 true,
			wantMode:                    agentA2ARuntimeSafetyModeUnsafePrerelease,
			wantPublicURL:               "https://agents.example.test/a2a",
		},
		{
			name:                        "prerelease loopback URL is rejected",
			appEnv:                      "staging",
			allowUnsafePrerelease:       "true",
			prereleaseExpectedPublicURL: "https://127.0.0.1:8443",
			publicURL:                   "https://127.0.0.1:8443",
			wantMode:                    agentA2ARuntimeSafetyModeDenied,
			wantPublicURL:               "https://127.0.0.1:8443",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("APP_ENV", test.appEnv)
			t.Setenv("AONE_ENV_TYPE", test.aoneEnvType)
			t.Setenv("ENV_TYPE", test.envType)
			t.Setenv("GO_ENV", test.goEnv)
			t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, test.allowUnsafeLocal)
			t.Setenv(agentA2AAllowUnsafePrereleaseRuntimeEnv, test.allowUnsafePrerelease)
			t.Setenv(agentA2AUnsafePrereleasePublicURLEnv, test.prereleaseExpectedPublicURL)

			decision := evaluateAgentA2ARuntimeSafety(test.publicURL)
			if decision.Allowed != test.wantAllowed {
				t.Fatalf("Allowed = %v, want %v", decision.Allowed, test.wantAllowed)
			}
			if decision.PublicBaseURL != test.wantPublicURL {
				t.Fatalf("PublicBaseURL = %q, want %q", decision.PublicBaseURL, test.wantPublicURL)
			}
			if decision.Mode != test.wantMode {
				t.Fatalf("Mode = %q, want %q", decision.Mode, test.wantMode)
			}
		})
	}
}

func TestCurrentAgentA2AEnvironmentMarkers(t *testing.T) {
	environmentNames := []string{"AONE_ENV_TYPE", "ENV_TYPE", "GO_ENV", "APP_ENV"}
	clearEnvironment := func(t *testing.T) {
		t.Helper()
		for _, name := range environmentNames {
			t.Setenv(name, "")
		}
	}

	t.Run("unset", func(t *testing.T) {
		clearEnvironment(t)
		markers := currentAgentA2AEnvironmentMarkers()
		if markers.Production || markers.Prerelease {
			t.Fatalf("unset markers = %+v, want neither production nor prerelease", markers)
		}
	})

	for _, name := range environmentNames {
		for _, alias := range []string{"prod", "production", "online"} {
			t.Run("production_"+name+"_"+alias, func(t *testing.T) {
				clearEnvironment(t)
				t.Setenv(name, alias)
				markers := currentAgentA2AEnvironmentMarkers()
				if !markers.Production || markers.Prerelease {
					t.Fatalf("%s=%q markers = %+v, want production only", name, alias, markers)
				}
			})
		}
	}

	for _, name := range environmentNames {
		for _, alias := range []string{"pre", "prepub", "prepublish", "staging", "stage"} {
			t.Run("prerelease_"+name+"_"+alias, func(t *testing.T) {
				clearEnvironment(t)
				t.Setenv(name, alias)
				markers := currentAgentA2AEnvironmentMarkers()
				if markers.Production || !markers.Prerelease {
					t.Fatalf("%s=%q markers = %+v, want prerelease only", name, alias, markers)
				}
			})
		}
	}

	t.Run("production and prerelease conflict", func(t *testing.T) {
		clearEnvironment(t)
		t.Setenv("AONE_ENV_TYPE", "prepub")
		t.Setenv("APP_ENV", "online")
		markers := currentAgentA2AEnvironmentMarkers()
		if !markers.Production || !markers.Prerelease {
			t.Fatalf("conflicting markers = %+v, want both signals for fail-closed evaluation", markers)
		}
	})
}

func TestEvaluateAgentA2ARequestSafetyLocalModeRequiresLoopbackSocketPeer(t *testing.T) {
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
			decision := evaluateAgentA2ARequestSafety(publicURL, test.remoteAddr)
			if decision.Allowed != test.want {
				t.Fatalf("Allowed = %v, want %v", decision.Allowed, test.want)
			}
			if decision.Mode != agentA2ARuntimeSafetyModeUnsafeLocal {
				t.Fatalf("Mode = %q, want %q", decision.Mode, agentA2ARuntimeSafetyModeUnsafeLocal)
			}
		})
	}
}

func TestEvaluateAgentA2ARequestSafetyPrereleaseModeAllowsRemoteSocketPeer(t *testing.T) {
	const publicURL = "https://agents.example.test/a2a"
	allowUnsafePrereleaseAgentA2ARuntimeForTest(t, publicURL)

	for _, remoteAddr := range []string{"10.20.30.40:49152", "192.168.1.20:49152", "203.0.113.8:49152", ""} {
		decision := evaluateAgentA2ARequestSafety(publicURL, remoteAddr)
		if !decision.Allowed {
			t.Fatalf("remote peer %q was rejected in prerelease mode", remoteAddr)
		}
		if decision.Mode != agentA2ARuntimeSafetyModeUnsafePrerelease {
			t.Fatalf("Mode = %q, want %q", decision.Mode, agentA2ARuntimeSafetyModeUnsafePrerelease)
		}
	}
}

func allowUnsafeLocalAgentA2ARuntimeForTest(t *testing.T) {
	t.Helper()
	t.Setenv("APP_ENV", "test")
	t.Setenv("AONE_ENV_TYPE", "")
	t.Setenv("ENV_TYPE", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, "true")
	t.Setenv(agentA2AAllowUnsafePrereleaseRuntimeEnv, "")
	t.Setenv(agentA2AUnsafePrereleasePublicURLEnv, "")
}

func allowUnsafePrereleaseAgentA2ARuntimeForTest(t *testing.T, publicURL string) {
	t.Helper()
	t.Setenv("APP_ENV", "staging")
	t.Setenv("AONE_ENV_TYPE", "prepub")
	t.Setenv("ENV_TYPE", "")
	t.Setenv("GO_ENV", "")
	t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, "")
	t.Setenv(agentA2AAllowUnsafePrereleaseRuntimeEnv, "true")
	t.Setenv(agentA2AUnsafePrereleasePublicURLEnv, publicURL)
}
