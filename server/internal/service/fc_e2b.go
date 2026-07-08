package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/multica-ai/multica/server/pkg/redact"
)

const (
	FCE2BMetadataKind  = "fc-e2b"
	FCE2BProvider      = "hermes"
	FCE2BRunnerCommand = "multica-fc-hermes-runner"

	defaultFCE2BCLIPath             = "e2b"
	defaultFCE2BTimeoutSeconds      = 3600
	defaultFCE2BSandboxReadyTimeout = 60 * time.Second
	fcE2BDaemonTokenTTL             = time.Hour
)

type FCE2BConfig struct {
	Enabled             bool
	Template            string
	ServerURL           string
	APIKey              string
	APIURL              string
	Domain              string
	LLMBaseURL          string
	LLMAPIKey           string
	LLMModel            string
	CLIPath             string
	TimeoutSeconds      int
	SandboxReadyTimeout time.Duration
	ParseError          error
}

func FCE2BConfigFromEnv() FCE2BConfig {
	cfg := FCE2BConfig{
		Enabled:             envBool("MULTICA_FC_E2B_ENABLED"),
		Template:            strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_TEMPLATE")),
		ServerURL:           strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_SERVER_URL")), "/"),
		APIKey:              strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_API_KEY")),
		APIURL:              strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_API_URL")), "/"),
		Domain:              strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_DOMAIN")),
		LLMBaseURL:          strings.TrimRight(strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_OPENAI_BASE_URL")), "/"),
		LLMAPIKey:           strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_OPENAI_API_KEY")),
		LLMModel:            strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_OPENAI_MODEL")),
		CLIPath:             strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_CLI_PATH")),
		TimeoutSeconds:      defaultFCE2BTimeoutSeconds,
		SandboxReadyTimeout: defaultFCE2BSandboxReadyTimeout,
	}
	if cfg.CLIPath == "" {
		cfg.CLIPath = defaultFCE2BCLIPath
	}
	if raw := strings.TrimSpace(os.Getenv("MULTICA_FC_E2B_TIMEOUT_SECONDS")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n <= 0 {
			cfg.ParseError = fmt.Errorf("invalid MULTICA_FC_E2B_TIMEOUT_SECONDS")
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
	if strings.TrimSpace(c.Template) == "" {
		missing = append(missing, "MULTICA_FC_E2B_TEMPLATE")
	}
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
	if strings.TrimSpace(c.LLMModel) == "" {
		missing = append(missing, "MULTICA_FC_E2B_OPENAI_MODEL")
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

type FCE2BLauncher struct {
	Queries *db.Queries
	Tasks   *TaskService
	Config  FCE2BConfig
	Runner  CommandRunner
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

func (l *FCE2BLauncher) LaunchTask(ctx context.Context, task db.AgentTaskQueue) error {
	if l == nil || l.Queries == nil || l.Tasks == nil || !task.RuntimeID.Valid {
		return nil
	}
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

	sandboxID, err := l.createSandbox(ctx)
	if err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	if err := l.waitSandboxReady(ctx, sandboxID); err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	if err := l.execRunOnce(ctx, sandboxID, rt, token); err != nil {
		return l.failLaunch(ctx, task, err.Error())
	}
	return nil
}

func (l *FCE2BLauncher) createSandbox(ctx context.Context) (string, error) {
	args := []string{
		"sandbox", "create",
		"--detach",
		"--timeout", strconv.Itoa(l.Config.TimeoutSeconds),
		"--lifecycle.ontimeout", "kill",
		l.Config.Template,
	}
	out, err := l.Runner.Run(ctx, l.Config.CLIPath, args, l.e2bEnv())
	if err != nil {
		return "", fmt.Errorf("FC/E2B sandbox create failed: %w", err)
	}
	id, err := parseE2BSandboxID(out)
	if err != nil {
		return "", fmt.Errorf("FC/E2B sandbox create returned no sandbox id")
	}
	return id, nil
}

func (l *FCE2BLauncher) waitSandboxReady(ctx context.Context, sandboxID string) error {
	deadline := time.Now().Add(l.Config.SandboxReadyTimeout)
	var lastErr error
	for {
		_, err := l.Runner.Run(ctx, l.Config.CLIPath, []string{"sandbox", "exec", sandboxID, "true"}, l.e2bEnv())
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

func (l *FCE2BLauncher) execRunOnce(ctx context.Context, sandboxID string, rt db.AgentRuntime, token string) error {
	runtimeID := util.UUIDToString(rt.ID)
	args := []string{
		"sandbox", "exec",
		"--background",
		"-e", "MULTICA_SERVER_URL=" + l.Config.ServerURL,
		"-e", "MULTICA_DAEMON_TOKEN=" + token,
		"-e", "MULTICA_RUNTIME_ID=" + runtimeID,
		"-e", "MULTICA_DAEMON_ID=" + rt.DaemonID.String,
		"-e", "MULTICA_AGENT_RUNTIME_NAME=" + rt.Name,
		"-e", "OPENAI_BASE_URL=" + l.Config.LLMBaseURL,
		"-e", "OPENAI_API_KEY=" + l.Config.LLMAPIKey,
		"-e", "OPENAI_MODEL=" + l.Config.LLMModel,
		sandboxID,
		"--",
		FCE2BRunnerCommand,
		"--runtime-id", runtimeID,
		"--provider", FCE2BProvider,
	}
	if _, err := l.Runner.Run(ctx, l.Config.CLIPath, args, l.e2bEnv()); err != nil {
		return fmt.Errorf("FC/E2B runner exec failed: %w", err)
	}
	return nil
}

func (l *FCE2BLauncher) e2bEnv() []string {
	return []string{
		"E2B_API_KEY=" + l.Config.APIKey,
		"E2B_API_URL=" + l.Config.APIURL,
		"E2B_DOMAIN=" + l.Config.Domain,
	}
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
		return err
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
	s.broadcastTaskEvent(ctx, protocol.EventTaskFailed, task)
	return &task, nil
}
