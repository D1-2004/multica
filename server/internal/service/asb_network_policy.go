package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"net/url"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const asbNetworkAllowlistKey = "asb_network_allowlist"
const asbNetworkPolicyFingerprintKey = "multica.network_policy_sha256"

// Audited against DWS v1.0.62-beta.6 (e45f7ca9), including direct transfers
// after MCP returns a signed URL. See docs/security/asb-dws-network-audit.md.
// Built-in domain families include the Alibaba intranet and DingTalk domains
// requested for default access. Other shared tenant domains remain exact-only.
var asbBuiltinNetworkTargets = []string{
	"mcp.dingtalk.com", "pre-mcp.dingtalk.com", "mcp-gw.dingtalk.com", "pre-mcp-gw.dingtalk.com",
	"open-dev.dingtalk.com", "pre-open-dev.dingtalk.com", "api.dingtalk.com", "oapi.dingtalk.com", "pre-oapi.dingtalk.com",
	"login.dingtalk.com", "pre-login.dingtalk.com",
	"mcp.dingtalk.io", "pre-mcp.dingtalk.io", "mcp-gw.dingtalk.io", "pre-mcp-gw.dingtalk.io",
	"login.dingtalk.io", "pre-login.dingtalk.io", "api.dingtalk.io", "open-dev.dingtalk.io", "pre-open-dev.dingtalk.io",
	"trans.dingtalk.com", "sh-dualstack.trans.dingtalk.com", "*.trans.dingtalk.com",
	"down.dingtalk.com", "*.down.dingtalk.com", "download.dingtalk.com", "upload.dingtalk.com",
	"alidocs.oss-cn-zhangjiakou.aliyuncs.com", "alidocs2.oss-cn-zhangjiakou.aliyuncs.com",
	"alimail-cn.aliyuncs.com", "alimail-personal.aliyuncs.com",
	"wss-open-connection.dingtalk.com", "pre-wss-open-connection.dingtalk.com",
	"alidocs.dingtalk.com", "docs.dingtalk.com", "shanji.dingtalk.com", "aihub.dingtalk.com",
	"open.dingtalk.com", "s.dingtalk.com", "img.alicdn.com", "server.safeding.com",
	"github.com", "api.github.com", "raw.githubusercontent.com", "objects.githubusercontent.com", "release-assets.githubusercontent.com", "gosspublic.alicdn.com",
	"generativelanguage.googleapis.com",
	"agent-identity.dingtalk.com", "pre-agent-identity.dingtalk.com",
	"*.alibaba-inc.com", "*.dingtalk.com",
	"login.alibaba-inc.com", "authx.alibaba-inc.com", "id-api.alibaba-inc.com",
	"tp-alilang.alibaba-inc.com",
	// WireGuard's managed gateways use literal destinations (UDP 11940), so
	// allowing only the trust-device registration domain cannot establish BUC.
	// Verified against ASB Hangzhou with default-deny egress on 2026-09-15.
	"140.205.109.26", "140.205.109.30",
	"aone.alibaba-inc.com", "code.alibaba-inc.com", "sandbox.aone.alibaba-inc.com",
	"registry.npmjs.org", "registry.npmmirror.com", "pypi.org", "files.pythonhosted.org",
}

// Only these code-reviewed service families may use ASB wildcard matching.
// Custom Runtime/Agent/deployment configuration still accepts exact hosts only.
func isASBManagedNetworkFamily(target string) bool {
	switch target {
	case "*.trans.dingtalk.com", "*.down.dingtalk.com", "*.alibaba-inc.com", "*.dingtalk.com":
		return true
	default:
		return false
	}
}

type ASBNetworkRule struct {
	Action string `json:"action"`
	Target string `json:"target"`
}

type ASBNetworkPolicy struct {
	DefaultAction string           `json:"defaultAction"`
	Egress        []ASBNetworkRule `json:"egress"`
}

type ASBNetworkPolicySettings struct {
	DefaultAction    string   `json:"default_action"`
	DefaultTargets   []string `json:"default_targets"`
	CustomTargets    []string `json:"custom_targets"`
	EffectiveTargets []string `json:"effective_targets"`
}

// NormalizeASBNetworkTargets accepts exact DNS names and individual IPs only.
// Reject URL syntax, wildcards and CIDRs so a UI edit cannot disable isolation.
func NormalizeASBNetworkTargets(values []string) ([]string, error) {
	if len(values) > 256 {
		return nil, errors.New("network allowlist supports at most 256 targets")
	}
	unique := map[string]bool{}
	for _, value := range values {
		target := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
		if target == "" {
			continue
		}
		if _, err := netip.ParseAddr(target); err != nil {
			if len(target) > 253 || !strings.Contains(target, ".") {
				return nil, fmt.Errorf("invalid network target %q: use an exact domain or IP", value)
			}
			for _, label := range strings.Split(target, ".") {
				if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
					return nil, fmt.Errorf("invalid network target %q", value)
				}
				for _, ch := range label {
					if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
						return nil, fmt.Errorf("invalid network target %q: URLs, wildcards and CIDRs are not allowed", value)
					}
				}
			}
		}
		unique[target] = true
	}
	result := make([]string, 0, len(unique))
	for target := range unique {
		result = append(result, target)
	}
	sort.Strings(result)
	return result, nil
}

func asbURLTarget(raw string) string {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https" && parsed.Scheme != "ws" && parsed.Scheme != "wss") {
		return ""
	}
	host := strings.ToLower(strings.TrimSuffix(parsed.Hostname(), "."))
	if host == "localhost" {
		return ""
	}
	if ip, err := netip.ParseAddr(host); err == nil && ip.IsLoopback() {
		return ""
	}
	if _, err := NormalizeASBNetworkTargets([]string{host}); err != nil {
		return ""
	}
	return host
}

func asbConfiguredURLTargets(raw []byte) []string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return nil
	}
	var result []string
	var visit func(any)
	visit = func(v any) {
		switch item := v.(type) {
		case map[string]any:
			for key, child := range item {
				// Credentials/headers are never interpreted as destinations.
				lower := strings.ToLower(key)
				if lower == "headers" || strings.Contains(lower, "token") || strings.Contains(lower, "secret") || strings.Contains(lower, "password") || strings.Contains(lower, "api_key") {
					continue
				}
				visit(child)
			}
		case []any:
			for _, child := range item {
				visit(child)
			}
		case string:
			if target := asbURLTarget(item); target != "" {
				result = append(result, target)
			}
		}
	}
	visit(value)
	return result
}

func asbRuntimeCustomTargets(runtime db.AgentRuntime) ([]string, error) {
	var metadata map[string]json.RawMessage
	if len(runtime.Metadata) == 0 {
		return []string{}, nil
	}
	if err := json.Unmarshal(runtime.Metadata, &metadata); err != nil {
		return nil, errors.New("invalid ASB runtime metadata")
	}
	var values []string
	if raw, exists := metadata[asbNetworkAllowlistKey]; exists {
		if err := json.Unmarshal(raw, &values); err != nil {
			return nil, errors.New("invalid ASB runtime network allowlist")
		}
	}
	return NormalizeASBNetworkTargets(values)
}

func asbNetworkSettings(cfg ASBConfig, runtime db.AgentRuntime, configuredTargets []string) (ASBNetworkPolicySettings, error) {
	custom, err := asbRuntimeCustomTargets(runtime)
	if err != nil {
		return ASBNetworkPolicySettings{}, err
	}
	defaults := append([]string{}, asbBuiltinNetworkTargets...)
	platform, err := NormalizeASBNetworkTargets(cfg.NetworkAllowlist)
	if err != nil {
		return ASBNetworkPolicySettings{}, err
	}
	defaults = append(defaults, platform...)
	for _, raw := range append([]string{cfg.ServerURL, cfg.LLMBaseURL}, cfg.NetworkServiceURLs...) {
		if target := asbURLTarget(raw); target != "" {
			defaults = append(defaults, target)
		}
	}
	defaults = append(defaults, configuredTargets...)
	// The total can exceed the per-user limit when many Agents have MCP servers.
	defaults = uniqueASBTargets(defaults)
	effective := uniqueASBTargets(append(append([]string{}, defaults...), custom...))
	return ASBNetworkPolicySettings{DefaultAction: "deny", DefaultTargets: defaults, CustomTargets: custom, EffectiveTargets: effective}, nil
}

func uniqueASBTargets(values []string) []string {
	unique := map[string]bool{}
	for _, value := range values {
		if value != "" {
			unique[value] = true
		}
	}
	result := make([]string, 0, len(unique))
	for value := range unique {
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func (settings ASBNetworkPolicySettings) Policy() ASBNetworkPolicy {
	policy := ASBNetworkPolicy{DefaultAction: "deny", Egress: make([]ASBNetworkRule, 0, len(settings.EffectiveTargets))}
	for _, target := range settings.EffectiveTargets {
		policy.Egress = append(policy.Egress, ASBNetworkRule{Action: "allow", Target: target})
	}
	return policy
}

func (policy ASBNetworkPolicy) Fingerprint() string {
	encoded, _ := json.Marshal(policy)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:])
}

func (l *ASBLauncher) RuntimeNetworkPolicy(ctx context.Context, runtime db.AgentRuntime) (ASBNetworkPolicySettings, error) {
	if configured := l.withCurrentConfig(); configured != l {
		return configured.RuntimeNetworkPolicy(ctx, runtime)
	}
	var targets []string
	agents, err := l.Queries.ListActiveAgentsByRuntime(ctx, runtime.ID)
	if err != nil {
		return ASBNetworkPolicySettings{}, fmt.Errorf("load ASB Agent network dependencies: %w", err)
	}
	for _, agent := range agents {
		targets = append(targets, asbConfiguredURLTargets(agent.McpConfig)...)
		targets = append(targets, asbConfiguredURLTargets(agent.CustomEnv)...)
		targets = append(targets, asbConfiguredURLTargets(agent.RuntimeConfig)...)
	}
	return asbNetworkSettings(l.Config, runtime, targets)
}

// UpdateRuntimeNetworkAllowlist serializes metadata writes with task launches.
// Existing active tasks finish; a subsequent launch replaces a sandbox whose
// policy fingerprint differs, including legacy sandboxes with no fingerprint.
func (l *ASBLauncher) UpdateRuntimeNetworkAllowlist(ctx context.Context, runtimeID pgtype.UUID, values []string) (db.AgentRuntime, error) {
	targets, err := NormalizeASBNetworkTargets(values)
	if err != nil {
		return db.AgentRuntime{}, err
	}
	if l.Pool == nil {
		return db.AgentRuntime{}, errors.New("ASB network update requires database coordination")
	}
	tx, err := l.Pool.Begin(ctx)
	if err != nil {
		return db.AgentRuntime{}, err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err = tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1, $2)", fcE2BRuntimeLockClass, fcE2BRuntimeLockKey(runtimeID)); err != nil {
		return db.AgentRuntime{}, err
	}
	qtx := l.Queries.WithTx(tx)
	runtime, err := qtx.LockAgentRuntime(ctx, runtimeID)
	if err != nil {
		return db.AgentRuntime{}, err
	}
	if !IsASBRuntime(runtime) {
		return db.AgentRuntime{}, ErrCloudSandboxRuntimeRequired
	}
	var metadata map[string]any
	if err = json.Unmarshal(runtime.Metadata, &metadata); err != nil || metadata == nil {
		return db.AgentRuntime{}, errors.New("invalid ASB runtime metadata")
	}
	metadata[asbNetworkAllowlistKey] = targets
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return db.AgentRuntime{}, err
	}
	runtime, err = qtx.UpdateFCE2BRuntimeMetadata(ctx, db.UpdateFCE2BRuntimeMetadataParams{ID: runtimeID, Metadata: encoded})
	if err != nil {
		return db.AgentRuntime{}, err
	}
	return runtime, tx.Commit(ctx)
}
