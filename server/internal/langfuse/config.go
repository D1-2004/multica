// Package langfuse exports server-side LLM traces to a Langfuse project over
// the OpenTelemetry (OTLP/HTTP) ingestion endpoint.
//
// Langfuse marks its legacy /api/public/ingestion batch API as deprecated and
// documents OTLP as the supported path for languages without a native SDK, so
// the package is a thin layer over the official OpenTelemetry Go SDK: it owns
// exporter construction, batching, authentication, and the langfuse.* span
// attribute vocabulary, and hands callers nil-safe Trace / Observation helpers
// so instrumented loops never have to touch OpenTelemetry types directly.
//
// Delivery is best-effort and fail-open. A deployment without credentials gets
// a nil *Client whose methods are all no-ops, export errors are logged and
// dropped, and a full queue drops spans instead of blocking the caller.
package langfuse

import (
	"encoding/base64"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
)

// Environment variable names. The key names follow the Langfuse SDK
// convention so the same values work for langfuse-cli and other tooling.
const (
	EnvPublicKey   = "LANGFUSE_PUBLIC_KEY"
	EnvSecretKey   = "LANGFUSE_SECRET_KEY"
	EnvBaseURL     = "LANGFUSE_BASE_URL"
	EnvHost        = "LANGFUSE_HOST"
	EnvEnvironment = "LANGFUSE_ENVIRONMENT"
	EnvEnabled     = "LANGFUSE_TRACING_ENABLED"
)

const (
	otelTracesPath        = "/api/public/otel/v1/traces"
	defaultExportTimeout  = 15 * time.Second
	defaultBatchTimeout   = 2 * time.Second
	defaultMaxQueueSize   = 4096
	defaultMaxBatchSize   = 256
	maxEnvironmentLength  = 40
	ingestionVersionValue = "4"
)

// Config holds the tunables for the exporter. An empty Config yields a
// disabled client (see Config.Enabled and New).
type Config struct {
	// PublicKey and SecretKey are the project API key pair. Both are required.
	PublicKey string
	SecretKey string
	// BaseURL is the Langfuse origin, e.g. https://cloud.langfuse.com. The
	// OTLP traces path is appended automatically; a value that already ends
	// with /api/public/otel or /api/public/otel/v1/traces is accepted as-is.
	BaseURL string
	// Environment tags every trace with the Langfuse environment (production,
	// pre, staging, ...). Empty leaves the Langfuse default.
	Environment string
	// Release is stamped as langfuse.release on every span, typically the
	// server version.
	Release string
	// Disabled turns tracing off even when credentials are present.
	Disabled bool

	ExportTimeout time.Duration
	BatchTimeout  time.Duration
	MaxQueueSize  int
	MaxBatchSize  int
}

// ConfigFromEnv reads the LANGFUSE_* variables. LANGFUSE_BASE_URL wins over the
// older LANGFUSE_HOST spelling. The environment falls back to the Aone
// environment type and then APP_ENV so pre-release and production traces stay
// separable without extra configuration.
func ConfigFromEnv() Config {
	base := strings.TrimSpace(os.Getenv(EnvBaseURL))
	if base == "" {
		base = strings.TrimSpace(os.Getenv(EnvHost))
	}
	environment := strings.TrimSpace(os.Getenv(EnvEnvironment))
	if environment == "" {
		environment = strings.TrimSpace(os.Getenv("AONE_ENV_TYPE"))
	}
	if environment == "" {
		environment = strings.TrimSpace(os.Getenv("APP_ENV"))
	}
	disabled := false
	if raw := strings.TrimSpace(os.Getenv(EnvEnabled)); raw != "" {
		switch strings.ToLower(raw) {
		case "0", "false", "no", "off":
			disabled = true
		}
	}
	return Config{
		PublicKey:   strings.TrimSpace(os.Getenv(EnvPublicKey)),
		SecretKey:   strings.TrimSpace(os.Getenv(EnvSecretKey)),
		BaseURL:     base,
		Environment: environment,
		Disabled:    disabled,
	}
}

// Enabled reports whether the configuration carries everything needed to
// export: both keys, a base URL, and no explicit kill switch.
func (c Config) Enabled() bool {
	return !c.Disabled &&
		strings.TrimSpace(c.PublicKey) != "" &&
		strings.TrimSpace(c.SecretKey) != "" &&
		strings.TrimSpace(c.BaseURL) != ""
}

func (c Config) withDefaults() Config {
	if c.ExportTimeout <= 0 {
		c.ExportTimeout = defaultExportTimeout
	}
	if c.BatchTimeout <= 0 {
		c.BatchTimeout = defaultBatchTimeout
	}
	if c.MaxQueueSize <= 0 {
		c.MaxQueueSize = defaultMaxQueueSize
	}
	if c.MaxBatchSize <= 0 {
		c.MaxBatchSize = defaultMaxBatchSize
	}
	c.Environment = NormalizeEnvironment(c.Environment)
	return c
}

// endpointURL resolves the absolute OTLP traces URL from BaseURL.
func (c Config) endpointURL() (string, error) {
	raw := strings.TrimSpace(c.BaseURL)
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("langfuse: parse base URL: %w", err)
	}
	if (parsed.Scheme != "https" && parsed.Scheme != "http") || parsed.Host == "" {
		return "", errors.New("langfuse: base URL must be an absolute http(s) URL")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("langfuse: base URL must not carry credentials, query, or fragment")
	}
	path := strings.TrimRight(parsed.Path, "/")
	switch {
	case strings.HasSuffix(path, otelTracesPath):
	case strings.HasSuffix(path, "/api/public/otel"):
		path += "/v1/traces"
	default:
		path += otelTracesPath
	}
	parsed.Path = path
	return parsed.String(), nil
}

func (c Config) authorization() string {
	pair := strings.TrimSpace(c.PublicKey) + ":" + strings.TrimSpace(c.SecretKey)
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(pair))
}

var environmentInvalid = regexp.MustCompile(`[^a-z0-9_-]+`)

// NormalizeEnvironment maps an arbitrary deployment label onto the Langfuse
// environment grammar: lowercase [a-z0-9_-], at most 40 characters, and not
// starting with the reserved "langfuse" prefix.
func NormalizeEnvironment(raw string) string {
	env := strings.ToLower(strings.TrimSpace(raw))
	env = environmentInvalid.ReplaceAllString(env, "-")
	env = strings.Trim(env, "-_")
	if strings.HasPrefix(env, "langfuse") {
		env = "env-" + env
	}
	if len(env) > maxEnvironmentLength {
		env = strings.Trim(env[:maxEnvironmentLength], "-_")
	}
	return env
}
