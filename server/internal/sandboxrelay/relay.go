package sandboxrelay

import (
	"crypto/subtle"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	multicaUpstreamEnv       = "MULTICA_SANDBOX_RELAY_MULTICA_UPSTREAM_URL"
	agentIdentityUpstreamEnv = "MULTICA_SANDBOX_RELAY_AGENT_IDENTITY_UPSTREAM_URL"
	maxInflightEnv           = "MULTICA_SANDBOX_RELAY_MAX_INFLIGHT"
	defaultMaxInflight       = 128
	agentIdentityRedeemPath  = "/api/agent-identity/v1/credentials/redeem"
)

var strippedIdentityHeaders = []string{
	"X-Actor-Source",
	"X-Auth-Method",
	"X-User-ID",
	"X-User-Email",
	"X-Forwarded-For",
	"X-Forwarded-Host",
	"X-Forwarded-Proto",
	"X-Real-IP",
}

type Relay struct {
	verifier      *Verifier
	multica       *httputil.ReverseProxy
	agentIdentity *httputil.ReverseProxy
	inflight      chan struct{}
	requests      atomic.Uint64
	rejected      atomic.Uint64
	failures      atomic.Uint64
}

type RelayConfig struct {
	Verifier              *Verifier
	MulticaUpstream       *url.URL
	AgentIdentityUpstream *url.URL
	MaxInflight           int
}

func LoadRelayFromEnv() (*Relay, error) {
	rawVerifyKeys := strings.TrimSpace(os.Getenv(verifyKeysEnv))
	rawMulticaUpstream := strings.TrimSpace(os.Getenv(multicaUpstreamEnv))
	rawAgentIdentityUpstream := strings.TrimSpace(os.Getenv(agentIdentityUpstreamEnv))
	rawMaxInflight := strings.TrimSpace(os.Getenv(maxInflightEnv))
	if rawVerifyKeys == "" && rawMulticaUpstream == "" && rawAgentIdentityUpstream == "" && rawMaxInflight == "" {
		return nil, nil
	}
	if rawVerifyKeys == "" || rawMulticaUpstream == "" || rawAgentIdentityUpstream == "" {
		return nil, fmt.Errorf("%s, %s, and %s must be configured together", verifyKeysEnv, multicaUpstreamEnv, agentIdentityUpstreamEnv)
	}
	verifier, err := loadVerifierFromEnv()
	if err != nil {
		return nil, err
	}
	multicaUpstream, err := parseUpstreamURL(multicaUpstreamEnv, rawMulticaUpstream)
	if err != nil {
		return nil, err
	}
	agentIdentityUpstream, err := parseUpstreamURL(agentIdentityUpstreamEnv, rawAgentIdentityUpstream)
	if err != nil {
		return nil, err
	}
	maxInflight := defaultMaxInflight
	if rawMaxInflight != "" {
		value, err := strconv.Atoi(rawMaxInflight)
		if err != nil || value <= 0 {
			return nil, fmt.Errorf("%s must be a positive integer", maxInflightEnv)
		}
		maxInflight = value
	}
	return NewRelay(RelayConfig{
		Verifier:              verifier,
		MulticaUpstream:       multicaUpstream,
		AgentIdentityUpstream: agentIdentityUpstream,
		MaxInflight:           maxInflight,
	})
}

func NewRelay(cfg RelayConfig) (*Relay, error) {
	if cfg.Verifier == nil {
		return nil, errors.New("sandbox relay verifier is required")
	}
	if cfg.MulticaUpstream == nil || cfg.AgentIdentityUpstream == nil {
		return nil, errors.New("sandbox relay upstream URLs are required")
	}
	if cfg.MaxInflight <= 0 {
		cfg.MaxInflight = defaultMaxInflight
	}
	relay := &Relay{
		verifier: cfg.Verifier,
		inflight: make(chan struct{}, cfg.MaxInflight),
	}
	relay.multica = relay.newProxy(cfg.MulticaUpstream, TargetMultica)
	relay.agentIdentity = relay.newProxy(cfg.AgentIdentityUpstream, TargetAgentIdentity)
	return relay, nil
}

func (relay *Relay) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		values := req.Header.Values(protocol.SandboxRelayTokenHeader)
		if len(values) == 0 {
			next.ServeHTTP(w, req)
			return
		}
		relay.requests.Add(1)
		if len(values) != 1 || strings.TrimSpace(values[0]) == "" {
			relay.reject(w, req, http.StatusUnauthorized, "invalid_token_header")
			return
		}
		claims, err := relay.verifier.Verify(values[0])
		if err != nil {
			relay.reject(w, req, http.StatusUnauthorized, "invalid_token")
			return
		}
		proxy, target, err := relay.selectProxy(req, claims)
		if err != nil {
			relay.reject(w, req, http.StatusForbidden, "request_not_allowed")
			return
		}
		select {
		case relay.inflight <- struct{}{}:
			defer func() { <-relay.inflight }()
		default:
			relay.failures.Add(1)
			writeRelayError(w, http.StatusServiceUnavailable, "sandbox relay is at capacity")
			return
		}
		slog.Info("sandbox relay request accepted",
			"event", "sandbox_relay_request",
			"target", target,
			"task_id", claims.TaskID,
			"runtime_id", claims.RuntimeID,
			"sandbox_id", claims.SandboxID,
			"method", req.Method,
			"path", req.URL.Path,
		)
		proxy.ServeHTTP(w, req)
	})
}

func (relay *Relay) selectProxy(req *http.Request, claims *Claims) (*httputil.ReverseProxy, string, error) {
	if req == nil || claims == nil {
		return nil, "", errors.New("missing request or claims")
	}
	bearer, err := singleBearerToken(req.Header)
	if err != nil {
		return nil, "", err
	}
	if req.Method == http.MethodPost && req.URL.Path == agentIdentityRedeemPath && req.URL.RawQuery == "" {
		if !claims.Allows(TargetAgentIdentity) ||
			!stringsEqualConstantTime(auth.HashToken(bearer), claims.AgentIdentityTokenSHA256) {
			return nil, "", errors.New("Agent Identity relay authorization does not match")
		}
		return relay.agentIdentity, TargetAgentIdentity, nil
	}
	if !claims.Allows(TargetMultica) {
		return nil, "", errors.New("Multica relay target is not allowed")
	}
	if isDaemonAPIPath(req.URL.Path) {
		if !strings.HasPrefix(bearer, "mdt_") ||
			!stringsEqualConstantTime(auth.HashToken(bearer), claims.DaemonTokenSHA256) {
			return nil, "", errors.New("daemon relay authorization does not match")
		}
		return relay.multica, TargetMultica, nil
	}
	if strings.HasPrefix(req.URL.Path, "/api/") {
		if !strings.HasPrefix(bearer, "mat_") {
			return nil, "", errors.New("agent relay requires a task-scoped token")
		}
		if taskID := strings.TrimSpace(req.Header.Get("X-Task-ID")); taskID != "" && taskID != claims.TaskID {
			return nil, "", errors.New("agent relay task ID does not match")
		}
		if agentID := strings.TrimSpace(req.Header.Get("X-Agent-ID")); agentID != "" && agentID != claims.AgentID {
			return nil, "", errors.New("agent relay agent ID does not match")
		}
		return relay.multica, TargetMultica, nil
	}
	return nil, "", errors.New("sandbox relay path is not allowed")
}

func (relay *Relay) newProxy(upstream *url.URL, target string) *httputil.ReverseProxy {
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		for _, header := range strippedIdentityHeaders {
			req.Header.Del(header)
		}
		// A nil slice is the net/http ReverseProxy sentinel that prevents it
		// from appending a new X-Forwarded-For value after Director returns.
		req.Header["X-Forwarded-For"] = nil
		req.Header.Del(protocol.SandboxRelayTokenHeader)
		req.Host = upstream.Host
	}
	proxy.Transport = relayTransport()
	proxy.FlushInterval = -1
	proxy.ErrorHandler = func(w http.ResponseWriter, req *http.Request, err error) {
		relay.failures.Add(1)
		slog.Error("sandbox relay upstream failed",
			"event", "sandbox_relay_upstream_failed",
			"target", target,
			"method", req.Method,
			"path", req.URL.Path,
			"error", err,
		)
		writeRelayError(w, http.StatusBadGateway, "sandbox relay upstream failed")
	}
	return proxy
}

func (relay *Relay) reject(w http.ResponseWriter, req *http.Request, status int, reason string) {
	relay.rejected.Add(1)
	slog.Warn("sandbox relay request rejected",
		"event", "sandbox_relay_rejected",
		"reason", reason,
		"method", req.Method,
		"path", req.URL.Path,
	)
	writeRelayError(w, status, "sandbox relay request rejected")
}

func relayTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   128,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		TLSClientConfig:       &tls.Config{MinVersion: tls.VersionTLS12},
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: time.Second,
	}
}

func parseUpstreamURL(name, raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("%s is invalid: %w", name, err)
	}
	isLoopbackHTTP := parsed.Scheme == "http" && isLoopbackHost(parsed.Hostname())
	if parsed.Scheme != "https" && !isLoopbackHTTP {
		return nil, fmt.Errorf("%s must use HTTPS", name)
	}
	if parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%s must be an origin URL without credentials, query, or fragment", name)
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return nil, fmt.Errorf("%s must not contain a path", name)
	}
	parsed.Path = ""
	parsed.RawPath = ""
	return parsed, nil
}

func singleBearerToken(header http.Header) (string, error) {
	values := header.Values("Authorization")
	if len(values) != 1 {
		return "", errors.New("sandbox relay requires one Authorization header")
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(values[0], prefix) {
		return "", errors.New("sandbox relay requires bearer authorization")
	}
	token := strings.TrimSpace(strings.TrimPrefix(values[0], prefix))
	if token == "" || strings.ContainsAny(token, " \t\r\n,") {
		return "", errors.New("sandbox relay bearer token is invalid")
	}
	return token, nil
}

func isDaemonAPIPath(path string) bool {
	return path == "/api/daemon" || strings.HasPrefix(path, "/api/daemon/")
}

func stringsEqualConstantTime(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func writeRelayError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": message})
}
