package service

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const (
	FCE2BMetadataKind  = "fc-e2b"
	FCE2BProvider      = "hermes"
	FCE2BRunnerCommand = "multica-fc-hermes-runner"

	fcE2BScopeTypeChat  = "chat"
	fcE2BScopeTypeIssue = "issue"

	defaultFCE2BCLIPath             = "e2b"
	defaultFCE2BTimeoutSeconds      = 3600
	defaultFCE2BSandboxReadyTimeout = 60 * time.Second
	defaultAgentIdentityTimeout     = 10 * time.Second
	fcE2BDaemonTokenTTL             = time.Hour
	fcE2BRunnerClaimTimeout         = 30 * time.Second
	fcE2BRunnerClaimPollInterval    = 500 * time.Millisecond
	fcE2BRunOnceHealthPortBase      = 20000
	fcE2BRunOnceHealthPortSpan      = 30000
)

type fcE2BRunnerClaimState string

const (
	fcE2BRunnerClaimObserved fcE2BRunnerClaimState = "observed"
	fcE2BRunnerClaimBlocked  fcE2BRunnerClaimState = "blocked"
	fcE2BRunnerClaimStalled  fcE2BRunnerClaimState = "stalled"
)

type FCE2BConfig struct {
	Enabled              bool
	Template             string
	ServerURL            string
	APIKey               string
	APIURL               string
	Domain               string
	LLMBaseURL           string
	LLMAPIKey            string
	LLMModels            []string
	DWSSecretKey         string
	AgentIdentityBaseURL string
	AgentIdentityTimeout time.Duration
	CLIPath              string
	TimeoutSeconds       int
	SandboxReadyTimeout  time.Duration
	ParseError           error
}

func FCE2BConfigFromEnv() FCE2BConfig {
	cfg := FCE2BConfig{
		Enabled:              envBool("MULTICA_FC_E2B_ENABLED"),
		Template:             strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_TEMPLATE")),
		ServerURL:            strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_SERVER_URL")), "/"),
		APIKey:               strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_API_KEY")),
		APIURL:               strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_API_URL")), "/"),
		Domain:               strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_DOMAIN")),
		LLMBaseURL:           strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_OPENAI_BASE_URL")), "/"),
		LLMAPIKey:            strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_OPENAI_API_KEY")),
		DWSSecretKey:         strings.TrimSpace(os.Getenv("MULTICA_DWS_SECRET_KEY")),
		AgentIdentityBaseURL: strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_AGENT_IDENTITY_BASE_URL")), "/"),
		AgentIdentityTimeout: defaultAgentIdentityTimeout,
		CLIPath:              strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_CLI_PATH")),
		TimeoutSeconds:       defaultFCE2BTimeoutSeconds,
		SandboxReadyTimeout:  defaultFCE2BSandboxReadyTimeout,
	}
	models, err := parseFCE2BModels(os.Getenv("MULTICA_FC_E2B_OPENAI_MODELS"))
	if err != nil {
		cfg.ParseError = errors.Join(cfg.ParseError, err)
	} else {
		cfg.LLMModels = models
	}
	if cfg.CLIPath == "" {
		cfg.CLIPath = defaultFCE2BCLIPath
	}
	if raw := strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_TIMEOUT_SECONDS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			cfg.ParseError = errors.Join(cfg.ParseError, fmt.Errorf("invalid MULTICA_FC_E2B_TIMEOUT_SECONDS"))
		} else {
			cfg.TimeoutSeconds = n
		}
	}
	if raw := strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_SANDBOX_READY_TIMEOUT")); raw != "" {
		d, err := time.ParseDuration(raw)
		if err != nil || d <= 0 {
			cfg.ParseError = errors.Join(cfg.ParseError, fmt.Errorf("invalid MULTICA_FC_E2B_SANDBOX_READY_TIMEOUT"))
		} else {
			cfg.SandboxReadyTimeout = d
		}
	}
	if raw := strings.TrimSpace(os.Getenv("MULTICA_AGENT_IDENTITY_TIMEOUT_SECONDS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			cfg.ParseError = errors.Join(cfg.ParseError, fmt.Errorf("invalid MULTICA_AGENT_IDENTITY_TIMEOUT_SECONDS"))
		} else {
			cfg.AgentIdentityTimeout = time.Duration(n) * time.Second
		}
	}
	return cfg
}

func envBool(name string) bool {
	v := strings.TrimSpace(os.Getenv(name))
	return strings.EqualFold(v, "true") || v == "1"
}

func (c FCE2BConfig) Validate() error {
	if c.ParseError != nil {
		return c.ParseError
	}
	var missing []string
	if strings.TrimSpace(c.ServerURL) == "" {
		missing = append(missing, "MULTICA_FC_E2B_SERVER_URL")
	}
	if strings.TrimSpace(c.APIKey) == "" {
		missing = append(missing, "MULTICA_FC_E2B_API_KEY")
	}
	if strings.TrimSpace(c.APIURL) == "" {
		missing = append(missing, "MULTICA_FC_E2B_API_URL")
	}
	if strings.TrimSpace(c.Domain) == "" {
		missing = append(missing, "MULTICA_FC_E2B_DOMAIN")
	}
	if strings.TrimSpace(c.LLMBaseURL) == "" {
		missing = append(missing, "MULTICA_FC_E2B_OPENAI_BASE_URL")
	}
	if strings.TrimSpace(c.LLMAPIKey) == "" {
		missing = append(missing, "MULTICA_FC_E2B_OPENAI_API_KEY")
	}
	if len(c.LLMModels) == 0 {
		missing = append(missing, "MULTICA_FC_E2B_OPENAI_MODELS")
	}
	if strings.TrimSpace(c.CLIPath) == "" {
		missing = append(missing, "MULTICA_FC_E2B_CLI_PATH")
	}
	if c.TimeoutSeconds <= 0 {
		missing = append(missing, "MULTICA_FC_E2B_TIMEOUT_SECONDS")
	}
	if c.SandboxReadyTimeout <= 0 {
		missing = append(missing, "MULTICA_FC_E2B_SANDBOX_READY_TIMEOUT")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing FC/E2B config: %s", strings.Join(missing, ", "))
	}
	return nil
}

func parseFCE2BModels(raw string) ([]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var models []string
	if err := json.Unmarshal([]byte(raw), &models); err != nil {
		return nil, errors.New("invalid MULTICA_FC_E2B_OPENAI_MODELS: expected a JSON string array")
	}
	if len(models) == 0 {
		return nil, errors.New("invalid MULTICA_FC_E2B_OPENAI_MODELS: array must not be empty")
	}
	seen := make(map[string]struct{}, len(models))
	for i, model := range models {
		model = strings.TrimSpace(model)
		if model == "" {
			return nil, fmt.Errorf("invalid MULTICA_FC_E2B_OPENAI_MODELS: item %d is empty", i)
		}
		if _, ok := seen[model]; ok {
			return nil, fmt.Errorf("invalid MULTICA_FC_E2B_OPENAI_MODELS: duplicate model %q", model)
		}
		seen[model] = struct{}{}
		models[i] = model
	}
	return models, nil
}

func (c FCE2BConfig) ModelForAgent(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		if len(c.LLMModels) == 0 {
			return "", errors.New("MULTICA_FC_E2B_OPENAI_MODELS is empty")
		}
		return c.LLMModels[0], nil
	}
	for _, allowed := range c.LLMModels {
		if model == allowed {
			return model, nil
		}
	}
	return "", fmt.Errorf("agent model %q is not configured in MULTICA_FC_E2B_OPENAI_MODELS", model)
}

func (c FCE2BConfig) ValidateTemplateAPI() error {
	if c.ParseError != nil {
		return c.ParseError
	}
	var missing []string
	if strings.TrimSpace(c.APIKey) == "" {
		missing = append(missing, "MULTICA_FC_E2B_API_KEY")
	}
	if strings.TrimSpace(c.APIURL) == "" {
		missing = append(missing, "MULTICA_FC_E2B_API_URL")
	}
	if strings.TrimSpace(c.Domain) == "" {
		missing = append(missing, "MULTICA_FC_E2B_DOMAIN")
	}
	if strings.TrimSpace(c.CLIPath) == "" {
		missing = append(missing, "MULTICA_FC_E2B_CLI_PATH")
	}
	if len(missing) > 0 {
		return fmt.Errorf("missing FC/E2B template config: %s", strings.Join(missing, ", "))
	}
	return nil
}

func IsFCE2BRuntime(rt db.AgentRuntime) bool {
	if rt.RuntimeMode != "cloud" {
		return false
	}
	var metadata struct {
		Kind string `json:"kind"`
	}
	if len(rt.Metadata) == 0 || json.Unmarshal(rt.Metadata, &metadata) != nil {
		return false
	}
	return metadata.Kind == FCE2BMetadataKind
}

type CommandRunner interface {
	Run(ctx context.Context, name string, args []string, env []string) (string, error)
}

type FCE2BTemplate struct {
	ID        string         `json:"id,omitempty"`
	Name      string         `json:"name,omitempty"`
	Template  string         `json:"template"`
	Status    string         `json:"status,omitempty"`
	CreatedAt string         `json:"created_at,omitempty"`
	UpdatedAt string         `json:"updated_at,omitempty"`
	Metadata  map[string]any `json:"metadata,omitempty"`
}

type OSCommandRunner struct{}

func (OSCommandRunner) Run(ctx context.Context, name string, args []string, env []string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = append(os.Environ(), env...)
	var output bytes.Buffer
	cmd.Stdout = &output
	cmd.Stderr = &output
	if err := cmd.Run(); err != nil {
		return output.String(), fmt.Errorf("command failed: %w: %s", err, redact.Text(output.String()))
	}
	return output.String(), nil
}

func ListFCE2BTemplates(ctx context.Context, cfg FCE2BConfig, runner CommandRunner) ([]FCE2BTemplate, error) {
	if runner == nil {
		runner = OSCommandRunner{}
	}
	if err := cfg.ValidateTemplateAPI(); err != nil {
		return nil, err
	}
	out, err := runner.Run(ctx, cfg.CLIPath, []string{"template", "list", "--format", "json"}, fcE2BEnv(cfg))
	if err != nil {
		return nil, fmt.Errorf("FC/E2B template list failed: %w", err)
	}
	templates, err := parseFCE2BTemplates(out)
	if err != nil {
		return nil, err
	}
	return templates, nil
}

func parseFCE2BTemplates(output string) ([]FCE2BTemplate, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return nil, errors.New("FC/E2B template list returned empty output")
	}

	var raw any
	if err := json.Unmarshal([]byte(trimmed), &raw); err != nil {
		return nil, fmt.Errorf("FC/E2B template list returned invalid JSON: %w", err)
	}

	var items []any
	switch v := raw.(type) {
	case []any:
		items = v
	case map[string]any:
		for _, key := range []string{"templates", "items", "data"} {
			if arr, ok := v[key].([]any); ok {
				items = arr
				break
			}
		}
	default:
		return nil, errors.New("FC/E2B template list returned unexpected JSON")
	}
	if items == nil {
		return nil, errors.New("FC/E2B template list returned no templates")
	}

	templates := make([]FCE2BTemplate, 0, len(items))
	for _, item := range items {
		obj, ok := item.(map[string]any)
		if !ok {
			continue
		}
		t := FCE2BTemplate{
			ID:        firstString(obj, "id", "template_id", "templateID"),
			Name:      firstString(obj, "name", "template_name", "templateName", "alias", "aliases", "names"),
			Status:    firstString(obj, "status", "state", "buildStatus", "build_status"),
			CreatedAt: firstString(obj, "created_at", "createdAt", "create_time", "createTime"),
			UpdatedAt: firstString(obj, "updated_at", "updatedAt", "update_time", "updateTime"),
			Metadata:  obj,
		}
		t.Template = firstString(obj, "template", "templateName", "name", "alias", "aliases", "names", "id", "template_id", "templateID")
		if strings.TrimSpace(t.Template) == "" {
			continue
		}
		templates = append(templates, t)
	}
	return templates, nil
}

func firstString(obj map[string]any, keys ...string) string {
	for _, key := range keys {
		switch v := obj[key].(type) {
		case string:
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		case fmt.Stringer:
			if s := strings.TrimSpace(v.String()); s != "" {
				return s
			}
		case []any:
			for _, item := range v {
				if s := strings.TrimSpace(fmt.Sprint(item)); s != "" {
					return s
				}
			}
		case []string:
			for _, item := range v {
				if s := strings.TrimSpace(item); s != "" {
					return s
				}
			}
		case float64:
			return strconv.FormatInt(int64(v), 10)
		}
	}
	return ""
}

type FCE2BLauncher struct {
	Queries *db.Queries
	Tasks   *TaskService
	Config  FCE2BConfig
	Runner  CommandRunner

	// Pool backs the cross-replica sandbox lock. Optional: without it the
	// launcher serializes nothing, which is only safe in a single-process
	// deployment (see resolveSandbox).
	Pool *pgxpool.Pool
}

// fcE2BSandboxLockClass namespaces the advisory lock so it cannot collide with
// the migration loop's lock or the usage-rollup lock. "FCE2" as an int32.
const fcE2BSandboxLockClass int32 = 0x46434532

// lockSandboxScope serializes the read-then-create in resolveSandbox across
// replicas. The launch is driven by whichever replica served the enqueue (or
// the pending-chat poll, which the load balancer spreads freely), and the
// in-process launch guard only dedups within one replica — so without this two
// replicas both miss the session row, both boot a sandbox, and the second
// Upsert clobbers the first, leaving an orphaned microVM billed until timeout.
//
// The lock is held across the sandbox boot (seconds), so it holds one pooled
// connection for that long. The loser blocks, then re-reads and reuses the
// winner's sandbox.
func (l *FCE2BLauncher) lockSandboxScope(ctx context.Context, rt db.AgentRuntime, scope fcE2BTaskScope) (func(), error) {
	if l.Pool == nil {
		return func() {}, nil
	}
	key := fcE2BScopeLockKey(rt.ID, scope)
	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		return nil, fmt.Errorf("acquire connection for sandbox lock: %w", err)
	}
	if _, err := conn.Exec(ctx, "SELECT pg_advisory_lock($1, $2)", fcE2BSandboxLockClass, key); err != nil {
		conn.Release()
		return nil, fmt.Errorf("acquire sandbox lock: %w", err)
	}
	return func() {
		// Unlock on a background context: the caller's ctx may already be done
		// (a cancelled launch), and a lock left held would wedge every later
		// launch for this scope until the connection is recycled.
		unlockCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := conn.Exec(unlockCtx, "SELECT pg_advisory_unlock($1, $2)", fcE2BSandboxLockClass, key); err != nil {
			slog.Warn("fc/e2b: release sandbox lock failed", "err", err)
		}
		conn.Release()
	}, nil
}

func fcE2BScopeLockKey(runtimeID pgtype.UUID, scope fcE2BTaskScope) int32 {
	h := fnv.New32a()
	_, _ = h.Write(runtimeID.Bytes[:])
	_, _ = io.WriteString(h, scope.typ)
	_, _ = h.Write(scope.id.Bytes[:])
	return int32(h.Sum32())
}

type fcE2BTaskScope struct {
	typ string
	id  pgtype.UUID
}

func NewFCE2BLauncher(q *db.Queries, tasks *TaskService, cfg FCE2BConfig, runner CommandRunner) *FCE2BLauncher {
	if runner == nil {
		runner = OSCommandRunner{}
	}
	return &FCE2BLauncher{
		Queries: q,
		Tasks:   tasks,
		Config:  cfg,
		Runner:  runner,
	}
}

// SetPool wires the connection the cross-replica sandbox lock needs. Left nil
// (tests, single-process runs) the launcher serializes nothing — which is the
// pre-existing behavior, and the reason two replicas could each boot a sandbox
// for the same scope.
func (l *FCE2BLauncher) SetPool(pool *pgxpool.Pool) {
	if l != nil {
		l.Pool = pool
	}
}

func (l *FCE2BLauncher) LaunchTask(ctx context.Context, task db.AgentTaskQueue) error {
	if l == nil || l.Queries == nil || l.Tasks == nil || !task.RuntimeID.Valid {
		return nil
	}
	taskID := util.UUIDToString(task.ID)
	runtimeID := util.UUIDToString(task.RuntimeID)
	agentID := util.UUIDToString(task.AgentID)
	started := time.Now()
	slog.Info("FC/E2B launch started",
		"task_id", taskID,
		"runtime_id", runtimeID,
		"agent_id", agentID,
	)
	rt, err := l.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		return fmt.Errorf("load runtime for FC/E2B launch: %w", err)
	}
	if !IsFCE2BRuntime(rt) {
		return nil
	}
	if !l.Config.Enabled {
		return l.failLaunch(ctx, task, "FC/E2B runtime is disabled")
	}
	if err := l.Config.Validate(); err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	if !rt.DaemonID.Valid || strings.TrimSpace(rt.DaemonID.String) == "" {
		return l.failLaunch(ctx, task, "FC/E2B runtime has no daemon_id")
	}
	if !rt.OwnerID.Valid {
		return l.failLaunch(ctx, task, "FC/E2B runtime has no owner_id")
	}
	template, err := fcE2BTemplateForRuntime(rt, l.Config.Template)
	if err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	slog.Info("FC/E2B launch template resolved",
		"task_id", taskID,
		"runtime_id", runtimeID,
		"template", template,
	)

	scope, scoped := fcE2BScopeForTask(task)
	sandboxID, coldStart, err := l.resolveSandbox(ctx, rt, scope, scoped, template)
	if err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	scopeType := ""
	scopeID := ""
	if scoped {
		scopeType = scope.typ
		scopeID = util.UUIDToString(scope.id)
	}
	slog.Info("FC/E2B sandbox resolved",
		"task_id", taskID,
		"runtime_id", runtimeID,
		"sandbox_id", sandboxID,
		"cold_start", coldStart,
		"scope_type", scopeType,
		"scope_id", scopeID,
	)
	extraEnv, err := l.extraEnvForTask(ctx, task)
	if err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	token, err := auth.GenerateDaemonToken()
	if err != nil {
		return l.failLaunch(ctx, task, "failed to mint FC/E2B daemon token")
	}
	if _, err := l.Queries.CreateDaemonToken(ctx, db.CreateDaemonTokenParams{
		TokenHash:   auth.HashToken(token),
		WorkspaceID: rt.WorkspaceID,
		DaemonID:    rt.DaemonID.String,
		ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(fcE2BDaemonTokenTTL), Valid: true},
	}); err != nil {
		return l.failLaunch(ctx, task, "failed to persist FC/E2B daemon token")
	}
	if err := l.execRunOnce(ctx, sandboxID, rt, task.ID, token, coldStart, extraEnv); err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	slog.Info("FC/E2B run-once submitted",
		"task_id", taskID,
		"runtime_id", runtimeID,
		"sandbox_id", sandboxID,
		"cold_start", coldStart,
		"duration", time.Since(started).String(),
	)
	claimState, err := l.waitForRunOnceClaim(ctx, task)
	if err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	switch claimState {
	case fcE2BRunnerClaimObserved:
		slog.Info("FC/E2B run-once claim observed",
			"task_id", taskID,
			"runtime_id", runtimeID,
			"sandbox_id", sandboxID,
		)
	case fcE2BRunnerClaimBlocked:
		slog.Info("FC/E2B run-once claim blocked by active task",
			"task_id", taskID,
			"runtime_id", runtimeID,
			"sandbox_id", sandboxID,
		)
	case fcE2BRunnerClaimStalled:
		return l.failLaunch(ctx, task, fmt.Sprintf("FC/E2B runner did not claim task within %s after sandbox exec", fcE2BRunnerClaimTimeout))
	}
	if scoped {
		_ = l.Queries.TouchFCE2BSandboxSession(ctx, db.TouchFCE2BSandboxSessionParams{
			RuntimeID: rt.ID,
			ScopeType: scope.typ,
			ScopeID:   scope.id,
			SandboxID: sandboxID,
		})
	}
	return nil
}

func fcE2BScopeForTask(task db.AgentTaskQueue) (fcE2BTaskScope, bool) {
	if task.ChatSessionID.Valid {
		return fcE2BTaskScope{typ: fcE2BScopeTypeChat, id: task.ChatSessionID}, true
	}
	if task.IssueID.Valid {
		return fcE2BTaskScope{typ: fcE2BScopeTypeIssue, id: task.IssueID}, true
	}
	return fcE2BTaskScope{}, false
}

func (l *FCE2BLauncher) waitForRunOnceClaim(ctx context.Context, task db.AgentTaskQueue) (fcE2BRunnerClaimState, error) {
	deadline := time.NewTimer(fcE2BRunnerClaimTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(fcE2BRunnerClaimPollInterval)
	defer ticker.Stop()
	checkedInitialBlocker := false

	for {
		current, err := l.Queries.GetAgentTask(ctx, task.ID)
		if err != nil {
			return "", fmt.Errorf("verify FC/E2B runner claim: %w", err)
		}
		if current.Status != "queued" {
			return fcE2BRunnerClaimObserved, nil
		}
		if !checkedInitialBlocker {
			checkedInitialBlocker = true
			tasks, err := l.Queries.ListAgentTasks(ctx, current.AgentID)
			if err != nil {
				return "", fmt.Errorf("check FC/E2B runner claim blockers: %w", err)
			}
			if fcE2BTaskHasActiveBlocker(current, tasks) {
				return fcE2BRunnerClaimBlocked, nil
			}
		}

		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline.C:
			fresh, err := l.Queries.GetAgentTask(ctx, task.ID)
			if err != nil {
				return "", fmt.Errorf("verify FC/E2B runner claim: %w", err)
			}
			if fresh.Status != "queued" {
				return fcE2BRunnerClaimObserved, nil
			}
			tasks, err := l.Queries.ListAgentTasks(ctx, fresh.AgentID)
			if err != nil {
				return "", fmt.Errorf("check FC/E2B runner claim blockers: %w", err)
			}
			if fcE2BTaskHasActiveBlocker(fresh, tasks) {
				return fcE2BRunnerClaimBlocked, nil
			}
			return fcE2BRunnerClaimStalled, nil
		case <-ticker.C:
		}
	}
}

func fcE2BTaskHasActiveBlocker(target db.AgentTaskQueue, tasks []db.AgentTaskQueue) bool {
	for _, active := range tasks {
		if active.ID == target.ID || active.AgentID != target.AgentID {
			continue
		}
		if !fcE2BTaskStatusBlocksClaim(active.Status) {
			continue
		}
		if target.IssueID.Valid && active.IssueID == target.IssueID {
			return true
		}
		if target.ChatSessionID.Valid && active.ChatSessionID == target.ChatSessionID {
			return true
		}
		if !target.IssueID.Valid && !target.ChatSessionID.Valid && !target.AutopilotRunID.Valid &&
			!active.IssueID.Valid && !active.ChatSessionID.Valid && !active.AutopilotRunID.Valid {
			return true
		}
	}
	return false
}

func fcE2BTaskStatusBlocksClaim(status string) bool {
	switch status {
	case "dispatched", "running", "waiting_local_directory":
		return true
	default:
		return false
	}
}

func (l *FCE2BLauncher) extraEnvForTask(ctx context.Context, task db.AgentTaskQueue) (map[string]string, error) {
	agentIdentityEnv, err := fcE2BAgentIdentityExtraEnv(task, l.Config)
	if err != nil {
		return nil, err
	}
	agentRow, err := l.Queries.GetAgent(ctx, task.AgentID)
	if err != nil {
		return nil, fmt.Errorf("load agent for FC/E2B launch: %w", err)
	}
	model, err := l.Config.ModelForAgent(agentRow.Model.String)
	if err != nil {
		return nil, err
	}
	env := map[string]string{"OPENAI_MODEL": model}
	for key, value := range agentIdentityEnv {
		env[key] = value
	}
	if len(agentIdentityEnv) > 0 {
		// ContextToken authorization is task-scoped and must not reuse a
		// previously bound DWS profile when sandbox initialization fails.
		return env, nil
	}

	profileID, hasProfile, err := DWSProfileIDFromRuntimeConfig(agentRow.RuntimeConfig)
	if err != nil {
		return nil, err
	}
	if !hasProfile {
		return env, nil
	}
	profileUUID, err := util.ParseUUID(profileID)
	if err != nil {
		return nil, errors.New("runtime_config.fc_e2b.dws_profile_id is not a valid UUID")
	}
	profile, err := l.Queries.GetDWSAuthProfileForOwner(ctx, db.GetDWSAuthProfileForOwnerParams{
		ID:          profileUUID,
		WorkspaceID: agentRow.WorkspaceID,
		OwnerID:     agentRow.OwnerID,
	})
	if err != nil {
		return nil, fmt.Errorf("load DWS profile for FC/E2B launch: %w", err)
	}
	archive, err := l.decryptDWSAuthArchive(profile.AuthArchiveEncrypted)
	if err != nil {
		return nil, err
	}
	env["DWS_AUTH_ARCHIVE_B64"] = archive
	return env, nil
}

func fcE2BAgentIdentityExtraEnv(task db.AgentTaskQueue, cfg FCE2BConfig) (map[string]string, error) {
	if len(bytes.TrimSpace(task.Context)) == 0 {
		return nil, nil
	}
	var payload map[string]any
	if err := json.Unmarshal(task.Context, &payload); err != nil {
		return nil, fmt.Errorf("parse task context for Agent Identity: %w", err)
	}
	token, _ := payload[protocol.AgentIdentityContextTokenJSONKey].(string)
	token = strings.TrimSpace(token)
	if token == "" {
		return nil, nil
	}
	if cfg.AgentIdentityBaseURL == "" {
		return nil, errors.New("MULTICA_AGENT_IDENTITY_BASE_URL is required for ContextToken tasks")
	}
	timeout := cfg.AgentIdentityTimeout
	if timeout <= 0 {
		timeout = defaultAgentIdentityTimeout
	}
	seconds := int(timeout / time.Second)
	if seconds <= 0 {
		seconds = int(defaultAgentIdentityTimeout / time.Second)
	}
	return map[string]string{
		protocol.AgentIdentityContextTokenEnvKey: token,
		"MULTICA_AGENT_IDENTITY_BASE_URL":        cfg.AgentIdentityBaseURL,
		"MULTICA_AGENT_IDENTITY_TIMEOUT_SECONDS": strconv.Itoa(seconds),
	}, nil
}

// DWSProfileIDFromRuntimeConfig extracts the optional DWS profile binding from
// the agent runtime_config JSON. The server accepts both the current nested
// shape and the legacy top-level key so old agents keep their binding.
func DWSProfileIDFromRuntimeConfig(raw []byte) (string, bool, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return "", false, nil
	}
	var cfg struct {
		FCE2B struct {
			DWSProfileID string `json:"dws_profile_id"`
		} `json:"fc_e2b"`
		DWSProfileID string `json:"dws_profile_id"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", false, fmt.Errorf("parse agent runtime_config for DWS profile: %w", err)
	}
	profileID := strings.TrimSpace(cfg.FCE2B.DWSProfileID)
	if profileID == "" {
		profileID = strings.TrimSpace(cfg.DWSProfileID)
	}
	return profileID, profileID != "", nil
}

func (l *FCE2BLauncher) decryptDWSAuthArchive(ciphertext []byte) (string, error) {
	if len(ciphertext) == 0 {
		return "", errors.New("DWS profile archive is empty")
	}
	rawKey := strings.TrimSpace(l.Config.DWSSecretKey)
	if rawKey == "" {
		return "", errors.New("MULTICA_DWS_SECRET_KEY is required for DWS profiles")
	}
	key, err := base64.StdEncoding.DecodeString(rawKey)
	if err != nil {
		return "", errors.New("MULTICA_DWS_SECRET_KEY is not valid base64")
	}
	box, err := secretbox.New(key)
	if err != nil {
		return "", fmt.Errorf("MULTICA_DWS_SECRET_KEY is invalid: %w", err)
	}
	plain, err := box.Open(ciphertext)
	if err != nil {
		return "", errors.New("failed to decrypt DWS profile archive")
	}
	archive := strings.TrimSpace(string(plain))
	if archive == "" {
		return "", errors.New("DWS profile archive is empty")
	}
	return archive, nil
}

func (l *FCE2BLauncher) resolveSandbox(ctx context.Context, rt db.AgentRuntime, scope fcE2BTaskScope, scoped bool, template string) (string, bool, error) {
	if scoped {
		// Serialize the lookup-or-create with the other replicas before reading:
		// a check outside the lock is exactly the race that orphans sandboxes.
		release, err := l.lockSandboxScope(ctx, rt, scope)
		if err != nil {
			return "", false, err
		}
		defer release()

		session, err := l.Queries.GetActiveFCE2BSandboxSession(ctx, db.GetActiveFCE2BSandboxSessionParams{
			RuntimeID: rt.ID,
			ScopeType: scope.typ,
			ScopeID:   scope.id,
		})
		if err == nil {
			if err := l.checkSandboxReady(ctx, session.SandboxID); err != nil {
				_ = l.Queries.MarkFCE2BSandboxSessionStale(ctx, db.MarkFCE2BSandboxSessionStaleParams{
					RuntimeID: rt.ID,
					ScopeType: scope.typ,
					ScopeID:   scope.id,
					SandboxID: session.SandboxID,
				})
				return "", false, fmt.Errorf("FC/E2B warm sandbox %s is unavailable: %w", session.SandboxID, err)
			}
			return session.SandboxID, false, nil
		}
		if err != pgx.ErrNoRows {
			return "", false, fmt.Errorf("load FC/E2B sandbox session: %w", err)
		}
	}

	sandboxID, err := l.createSandbox(ctx, template)
	if err != nil {
		return "", true, err
	}
	if err := l.waitSandboxReady(ctx, sandboxID); err != nil {
		return "", true, err
	}
	if scoped {
		_, err = l.Queries.UpsertFCE2BSandboxSession(ctx, db.UpsertFCE2BSandboxSessionParams{
			WorkspaceID: rt.WorkspaceID,
			RuntimeID:   rt.ID,
			ScopeType:   scope.typ,
			ScopeID:     scope.id,
			SandboxID:   sandboxID,
			Template:    template,
			ExpiresAt:   pgtype.Timestamptz{Time: time.Now().Add(time.Duration(l.Config.TimeoutSeconds) * time.Second), Valid: true},
		})
		if err != nil {
			return "", true, fmt.Errorf("record FC/E2B sandbox session: %w", err)
		}
	}
	return sandboxID, true, nil
}

func (l *FCE2BLauncher) createSandbox(ctx context.Context, template string) (string, error) {
	template = strings.TrimSpace(template)
	if template == "" {
		return "", errors.New("FC/E2B runtime has no template")
	}
	args := []string{
		"sandbox", "create",
		"--detach",
		"--timeout", strconv.Itoa(l.Config.TimeoutSeconds),
		"--lifecycle.ontimeout", "kill",
		template,
	}
	out, err := l.runE2BCommand(ctx, args)
	if err != nil {
		return "", fmt.Errorf("FC/E2B sandbox create failed: %w", err)
	}
	id, err := parseE2BSandboxID(out)
	if err != nil {
		return "", fmt.Errorf("FC/E2B sandbox create returned no sandbox id")
	}
	return id, nil
}

func (l *FCE2BLauncher) checkSandboxReady(ctx context.Context, sandboxID string) error {
	_, err := l.runE2BCommand(ctx, []string{"sandbox", "exec", sandboxID, "true"})
	return err
}

func (l *FCE2BLauncher) waitSandboxReady(ctx context.Context, sandboxID string) error {
	deadline := time.Now().Add(l.Config.SandboxReadyTimeout)
	var lastErr error
	for {
		_, err := l.runE2BCommand(ctx, []string{"sandbox", "exec", sandboxID, "true"})
		if err == nil {
			return nil
		}
		lastErr = err
		if time.Now().After(deadline) {
			return fmt.Errorf("FC/E2B sandbox was not ready within %s: %w", l.Config.SandboxReadyTimeout, lastErr)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
}

func (l *FCE2BLauncher) execRunOnce(ctx context.Context, sandboxID string, rt db.AgentRuntime, taskID pgtype.UUID, token string, coldStart bool, extraEnv map[string]string) error {
	runtimeID := util.UUIDToString(rt.ID)
	healthPort := fcE2BHealthPortForTask(taskID)
	args := []string{
		"sandbox", "exec",
		"--background",
		"-e", "MULTICA_SERVER_URL=" + l.Config.ServerURL,
		"-e", "MULTICA_DAEMON_TOKEN=" + token,
		"-e", "MULTICA_RUNTIME_ID=" + runtimeID,
		"-e", "MULTICA_TASK_ID=" + util.UUIDToString(taskID),
		"-e", "MULTICA_DAEMON_ID=" + rt.DaemonID.String,
		"-e", "MULTICA_AGENT_RUNTIME_NAME=" + rt.Name,
		"-e", "HOME=/home/user",
		"-e", "DWS_CONFIG_DIR=/home/user/.dws",
		"-e", "OPENAI_BASE_URL=" + l.Config.LLMBaseURL,
		"-e", "OPENAI_API_KEY=" + l.Config.LLMAPIKey,
	}
	if coldStart {
		args = append(args, "-e", "MULTICA_FC_E2B_COLD_START=true")
	}
	for _, key := range sortedEnvKeys(extraEnv) {
		args = append(args, "-e", key+"="+extraEnv[key])
	}
	args = append(args,
		sandboxID,
		"--",
		FCE2BRunnerCommand,
		"--runtime-id", runtimeID,
		"--provider", FCE2BProvider,
		"--health-port", strconv.Itoa(healthPort),
	)
	if _, err := l.runE2BCommand(ctx, args); err != nil {
		return fmt.Errorf("FC/E2B runner exec failed: %w", err)
	}
	return nil
}

func fcE2BHealthPortForTask(taskID pgtype.UUID) int {
	sum := 0
	for _, b := range taskID.Bytes {
		sum = (sum*31 + int(b)) % fcE2BRunOnceHealthPortSpan
	}
	return fcE2BRunOnceHealthPortBase + sum
}

func (l *FCE2BLauncher) runE2BCommand(ctx context.Context, args []string) (string, error) {
	timeout := l.Config.SandboxReadyTimeout
	if timeout <= 0 {
		timeout = defaultFCE2BSandboxReadyTimeout
	}
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return l.Runner.Run(cmdCtx, l.Config.CLIPath, args, l.e2bEnv())
}

func (l *FCE2BLauncher) e2bEnv() []string {
	return fcE2BEnv(l.Config)
}

func fcE2BEnv(cfg FCE2BConfig) []string {
	return []string{
		"E2B_API_KEY=" + cfg.APIKey,
		"E2B_API_URL=" + cfg.APIURL,
		"E2B_DOMAIN=" + cfg.Domain,
	}
}

func sortedEnvKeys(env map[string]string) []string {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func fcE2BTemplateForRuntime(rt db.AgentRuntime, configuredTemplate string) (string, error) {
	var metadata struct {
		Template string `json:"template"`
	}
	if len(rt.Metadata) > 0 {
		_ = json.Unmarshal(rt.Metadata, &metadata)
	}
	template := strings.TrimSpace(metadata.Template)
	if template == "" {
		template = strings.TrimSpace(configuredTemplate)
	}
	if template == "" {
		return "", errors.New("FC/E2B runtime has no template")
	}
	return template, nil
}

func (l *FCE2BLauncher) failLaunch(ctx context.Context, task db.AgentTaskQueue, msg string) error {
	_, err := l.Tasks.FailTaskRuntimeStart(ctx, task.ID, task.RuntimeID, msg)
	if err != nil {
		return err
	}
	return fmt.Errorf("FC/E2B launch failed: %s", redact.Text(msg))
}

var e2bSandboxIDPattern = regexp.MustCompile(`Sandbox created with ID ([A-Za-z0-9_-]+) using template`)

func parseE2BSandboxID(output string) (string, error) {
	trimmed := strings.TrimSpace(output)
	if trimmed == "" {
		return "", errors.New("empty sandbox output")
	}
	if match := e2bSandboxIDPattern.FindStringSubmatch(trimmed); len(match) == 2 {
		return match[1], nil
	}
	return "", errors.New("unrecognized sandbox output")
}

func (s *TaskService) FailTaskRuntimeStart(ctx context.Context, taskID, runtimeID pgtype.UUID, errMsg string) (*db.AgentTaskQueue, error) {
	errMsg = redact.Text(errMsg)
	var task db.AgentTaskQueue
	var assistantMsg *db.ChatMessage
	if err := s.runInTx(ctx, func(qtx *db.Queries) error {
		t, err := qtx.FailAgentTaskRuntimeStart(ctx, db.FailAgentTaskRuntimeStartParams{
			ID:        taskID,
			RuntimeID: runtimeID,
			Error:     pgtype.Text{String: errMsg, Valid: errMsg != ""},
		})
		if err != nil {
			return err
		}
		task = t

		seq := int32(1)
		messages, err := qtx.ListTaskMessages(ctx, taskID)
		if err != nil {
			return err
		}
		for _, msg := range messages {
			if msg.Seq >= seq {
				seq = msg.Seq + 1
			}
		}
		_, err = qtx.CreateTaskMessage(ctx, db.CreateTaskMessageParams{
			TaskID:  taskID,
			Seq:     seq,
			Type:    "error",
			Content: pgtype.Text{String: "Runtime start failed: " + errMsg, Valid: true},
		})
		if err != nil {
			return err
		}
		if task.ChatSessionID.Valid {
			row, err := qtx.CreateChatMessage(ctx, db.CreateChatMessageParams{
				ChatSessionID: task.ChatSessionID,
				Role:          "assistant",
				Content:       "Runtime start failed: " + errMsg,
				TaskID:        task.ID,
				FailureReason: pgtype.Text{String: "runtime_start_failed", Valid: true},
				ElapsedMs:     computeChatElapsedMs(task),
			})
			if err != nil {
				return fmt.Errorf("create runtime-start-failed chat message: %w", err)
			}
			assistantMsg = &row
			// Unread tracking moved to the last_read_at read cursor (migration
			// 151): a fresh assistant message is unread by construction, so the
			// old SetUnreadSinceIfNull stamp is no longer needed here.
		}
		return nil
	}); err != nil {
		if existing, lookupErr := s.Queries.GetAgentTask(ctx, taskID); lookupErr == nil && errors.Is(err, pgx.ErrNoRows) {
			return &existing, nil
		}
		return nil, fmt.Errorf("fail task runtime start: %w", err)
	}

	slog.Warn("task failed before runtime start",
		"task_id", util.UUIDToString(task.ID),
		"runtime_id", util.UUIDToString(task.RuntimeID),
		"agent_id", util.UUIDToString(task.AgentID),
		"error", errMsg,
		"failure_reason", "runtime_start_failed",
	)
	s.captureTaskFailed(ctx, task)
	s.ReconcileAgentStatus(ctx, task.AgentID)
	if task.ChatSessionID.Valid {
		s.broadcastChatDone(ctx, task, assistantMsg)
	}
	s.broadcastTaskEvent(ctx, protocol.EventTaskFailed, task)
	return &task, nil
}
