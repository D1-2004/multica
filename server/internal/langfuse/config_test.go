package langfuse

import "testing"

func TestEndpointURL(t *testing.T) {
	tests := []struct {
		base string
		want string
		err  bool
	}{
		{base: "https://unify-aipilot.dingtalk.com", want: "https://unify-aipilot.dingtalk.com/api/public/otel/v1/traces"},
		{base: "https://unify-aipilot.dingtalk.com/", want: "https://unify-aipilot.dingtalk.com/api/public/otel/v1/traces"},
		{base: "https://cloud.langfuse.com/api/public/otel", want: "https://cloud.langfuse.com/api/public/otel/v1/traces"},
		{base: "https://cloud.langfuse.com/api/public/otel/v1/traces", want: "https://cloud.langfuse.com/api/public/otel/v1/traces"},
		{base: "http://localhost:3000/prefix", want: "http://localhost:3000/prefix/api/public/otel/v1/traces"},
		{base: "localhost:3000", err: true},
		{base: "https://user:pw@host", err: true},
		{base: "https://host/?x=1", err: true},
	}
	for _, tt := range tests {
		got, err := Config{BaseURL: tt.base}.endpointURL()
		if tt.err {
			if err == nil {
				t.Errorf("endpointURL(%q) = %q, want error", tt.base, got)
			}
			continue
		}
		if err != nil || got != tt.want {
			t.Errorf("endpointURL(%q) = %q, %v; want %q", tt.base, got, err, tt.want)
		}
	}
}

func TestNormalizeEnvironment(t *testing.T) {
	tests := map[string]string{
		"":                  "",
		"production":        "production",
		"Pre Release":       "pre-release",
		"PRE":               "pre",
		"langfuse-internal": "env-langfuse-internal",
		"预发/staging":        "staging",
	}
	for in, want := range tests {
		if got := NormalizeEnvironment(in); got != want {
			t.Errorf("NormalizeEnvironment(%q) = %q, want %q", in, got, want)
		}
	}
	if got := NormalizeEnvironment("a123456789b123456789c123456789d123456789e"); len(got) > maxEnvironmentLength {
		t.Errorf("environment not capped: %q", got)
	}
}

func TestConfigFromEnv(t *testing.T) {
	t.Setenv(EnvPublicKey, " pk ")
	t.Setenv(EnvSecretKey, "sk")
	t.Setenv(EnvBaseURL, "")
	t.Setenv(EnvHost, "https://host.example.test")
	t.Setenv(EnvEnvironment, "")
	t.Setenv("AONE_ENV_TYPE", "")
	t.Setenv("APP_ENV", "production")
	t.Setenv(EnvEnabled, "")
	cfg := ConfigFromEnv()
	if !cfg.Enabled() {
		t.Fatalf("config should be enabled: %+v", cfg)
	}
	if cfg.PublicKey != "pk" || cfg.BaseURL != "https://host.example.test" || cfg.Environment != "production" {
		t.Fatalf("config = %+v", cfg)
	}
	t.Setenv(EnvBaseURL, "https://base.example.test")
	t.Setenv("AONE_ENV_TYPE", "pre")
	if cfg = ConfigFromEnv(); cfg.BaseURL != "https://base.example.test" || cfg.Environment != "pre" {
		t.Fatalf("precedence config = %+v", cfg)
	}
	t.Setenv(EnvEnabled, "false")
	if ConfigFromEnv().Enabled() {
		t.Fatal("kill switch must disable tracing")
	}
	t.Setenv(EnvEnabled, "true")
	t.Setenv(EnvSecretKey, "")
	if ConfigFromEnv().Enabled() {
		t.Fatal("missing secret key must disable tracing")
	}
}
