package service

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/pkg/redact"
	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

// FCE2BSDKRolloutEnv holds the fallback rollout that applies while Diamond has
// no valid rollout document (runtimeconfig.FCE2BSDKRolloutDiamondDataID), and
// in deployments without Diamond. Unset, empty or invalid keeps every
// operation on the CLI.
const FCE2BSDKRolloutEnv = "MULTICA_FC_E2B_SDK_ROLLOUT"

// fcE2BSDKRolloutBucketKey namespaces percent bucketing so this rollout does
// not correlate with feature-flag buckets for the same identifiers.
const fcE2BSDKRolloutBucketKey = "fc_e2b_sdk_transport"

// Sources of the rollout that decided an operation.
const (
	fcE2BRolloutSourceDiamond = "diamond"
	fcE2BRolloutSourceEnv     = "env"
)

// FCE2BSDKRollout selects the operations that use the Go SDK. Remove it
// together with the CLI once production runs on the SDK.
type FCE2BSDKRollout = runtimeconfig.FCE2BSDKRollout

// ParseFCE2BSDKRollout validates rollout JSON. Blank input is the empty
// rollout.
func ParseFCE2BSDKRollout(raw string) (FCE2BSDKRollout, error) {
	rollout, err := runtimeconfig.ParseFCE2BSDKRollout([]byte(raw))
	if err != nil {
		return FCE2BSDKRollout{}, fmt.Errorf("invalid %s: %w", FCE2BSDKRolloutEnv, err)
	}
	return rollout, nil
}

// fcE2BRolloutActive reports whether the rollout can select any operation.
func fcE2BRolloutActive(r FCE2BSDKRollout) bool {
	return r.Enabled && (r.Percent > 0 || len(r.WorkspaceIDs) > 0 || len(r.AgentIDs) > 0 || len(r.RuntimeIDs) > 0)
}

// fcE2BRolloutSelects reports whether an operation with scope uses the SDK.
func fcE2BRolloutSelects(r FCE2BSDKRollout, scope FCE2BScope) bool {
	if !r.Enabled {
		return false
	}
	if r.Percent >= 100 {
		return true
	}
	if containsFCE2BScopeID(r.AgentIDs, scope.AgentID) ||
		containsFCE2BScopeID(r.RuntimeIDs, scope.RuntimeID) ||
		containsFCE2BScopeID(r.WorkspaceIDs, scope.WorkspaceID) {
		return true
	}
	if r.Percent <= 0 {
		return false
	}
	for _, id := range []uuid.UUID{scope.AgentID, scope.RuntimeID, scope.WorkspaceID} {
		if id != uuid.Nil {
			return fcE2BRolloutBucket(id.String()) < r.Percent
		}
	}
	return false
}

func containsFCE2BScopeID(ids []string, id uuid.UUID) bool {
	if id == uuid.Nil {
		return false
	}
	for _, candidate := range ids {
		if candidate == id.String() {
			return true
		}
	}
	return false
}

// fcE2BRolloutBucket is the FNV-1a bucket scheme used by pkg/featureflag.
func fcE2BRolloutBucket(identifier string) int {
	h := fnv.New32a()
	_, _ = h.Write([]byte(fcE2BSDKRolloutBucketKey))
	_, _ = h.Write([]byte{0})
	_, _ = h.Write([]byte(identifier))
	return int(h.Sum32() % 100)
}

// FCE2BScope identifies whose work an FC/E2B operation performs. It only
// routes the transport; it never grants access.
type FCE2BScope struct {
	WorkspaceID uuid.UUID
	AgentID     uuid.UUID
	RuntimeID   uuid.UUID
}

type fcE2BScopeKey struct{}

// WithFCE2BScope attaches scope to FC/E2B operations started under ctx.
func WithFCE2BScope(ctx context.Context, scope FCE2BScope) context.Context {
	return context.WithValue(ctx, fcE2BScopeKey{}, scope)
}

func fcE2BScopeFrom(ctx context.Context) FCE2BScope {
	scope, _ := ctx.Value(fcE2BScopeKey{}).(FCE2BScope)
	return scope
}

func pgFCE2BScopeID(id pgtype.UUID) uuid.UUID {
	if !id.Valid {
		return uuid.Nil
	}
	return uuid.UUID(id.Bytes)
}

// FCE2BRolloutRunner sends each operation to the CLI unless the rollout
// selects its scope. The rollout is read for every operation, so a Diamond
// update applies to the next operation without a restart. The choice is made
// once per operation and never retried on the other transport, so an
// ambiguous create or exec is not repeated.
type FCE2BRolloutRunner struct {
	CLI CommandRunner
	SDK CommandRunner
	// Rollout returns the rollout in force and where it came from.
	Rollout func() (FCE2BSDKRollout, string)
}

// NewFCE2BRolloutRunner uses the Diamond rollout while diamond reports a
// present snapshot and MULTICA_FC_E2B_SDK_ROLLOUT otherwise. A nil diamond
// means the deployment has no Diamond runtime configuration.
func NewFCE2BRolloutRunner(diamond func() runtimeconfig.FCE2BSDKRolloutSnapshot) FCE2BRolloutRunner {
	fallback := fcE2BRolloutFallback()
	return FCE2BRolloutRunner{
		CLI: OSCommandRunner{},
		SDK: SDKCommandRunner{},
		Rollout: func() (FCE2BSDKRollout, string) {
			if diamond != nil {
				if snapshot := diamond(); snapshot.Present {
					return snapshot.Rollout, fcE2BRolloutSourceDiamond
				}
			}
			return fallback, fcE2BRolloutSourceEnv
		},
	}
}

func (r FCE2BRolloutRunner) Run(ctx context.Context, name string, args []string, env []string) (string, error) {
	scope := fcE2BScopeFrom(ctx)
	var rollout FCE2BSDKRollout
	source := fcE2BRolloutSourceEnv
	if r.Rollout != nil {
		rollout, source = r.Rollout()
	}
	if !fcE2BRolloutSelects(rollout, scope) {
		return r.CLI.Run(ctx, name, args, env)
	}
	started := time.Now()
	out, err := r.SDK.Run(ctx, name, args, env)
	// Only SDK operations log here, so CLI-only deployments keep their logs.
	slog.Info("FC/E2B SDK transport", fcE2BSDKTransportLogAttrs(args, source, scope, time.Since(started), err)...)
	return out, err
}

// fcE2BSDKTransportLogAttrs describes one SDK operation without its command,
// environment or output: command output can carry signed URLs that callers
// deliberately keep out of logs, so a failure is logged by kind, exit code
// and the transport cause only.
func fcE2BSDKTransportLogAttrs(args []string, source string, scope FCE2BScope, elapsed time.Duration, err error) []any {
	operationName, sandboxID := "unsupported", ""
	if operation, parseErr := parseFCE2BOperation(args); parseErr == nil {
		switch {
		case operation.create != nil:
			operationName = "sandbox_create"
		case operation.exec != nil && operation.exec.Background:
			operationName, sandboxID = "sandbox_exec_background", operation.exec.SandboxID
		case operation.exec != nil:
			operationName, sandboxID = "sandbox_exec", operation.exec.SandboxID
		default:
			operationName = "template_list"
		}
	}
	attrs := []any{
		"operation", operationName,
		"rollout_source", source,
		"duration_ms", elapsed.Milliseconds(),
	}
	for _, field := range []struct {
		key string
		id  uuid.UUID
	}{{"workspace_id", scope.WorkspaceID}, {"agent_id", scope.AgentID}, {"runtime_id", scope.RuntimeID}} {
		if field.id != uuid.Nil {
			attrs = append(attrs, field.key, field.id)
		}
	}
	if sandboxID != "" {
		attrs = append(attrs, "sandbox_id", sandboxID)
	}
	if err == nil {
		return append(attrs, "outcome", "ok")
	}
	attrs = append(attrs, "outcome", "error")
	var exitErr *fcE2BCommandExitError
	var limitErr *fcE2BOutputLimitError
	var sdkErr *fcE2BSDKError
	switch {
	case errors.As(err, &exitErr):
		return append(attrs, "error_kind", "exit", "exit_code", exitErr.ExitCode)
	case errors.As(err, &limitErr):
		return append(attrs, "error_kind", "output_limit", "stream", limitErr.stream)
	case errors.Is(err, context.DeadlineExceeded):
		attrs = append(attrs, "error_kind", "deadline")
	case errors.Is(err, context.Canceled):
		attrs = append(attrs, "error_kind", "canceled")
	default:
		attrs = append(attrs, "error_kind", "failed")
	}
	// The cause of an SDK failure is the transport or API error, never the
	// command's output; other errors are fixed texts.
	cause := err.Error()
	if errors.As(err, &sdkErr) && sdkErr.cause != nil {
		cause = sdkErr.cause.Error()
	}
	if len(cause) > fcE2BSDKLogCauseBytes {
		cause = cause[:fcE2BSDKLogCauseBytes]
	}
	return append(attrs, "error_cause", redact.Text(cause))
}

// fcE2BSDKLogCauseBytes bounds the transport cause in the SDK log line.
const fcE2BSDKLogCauseBytes = 256

// fcE2BRolloutFallbackState caches the parsed MULTICA_FC_E2B_SDK_ROLLOUT so
// every runner built from the same value shares one parse and one log line.
var fcE2BRolloutFallbackState struct {
	sync.Mutex
	loaded  bool
	raw     string
	rollout FCE2BSDKRollout
}

// fcE2BRolloutFallback returns the environment fallback rollout. Unset,
// empty or invalid keeps every operation on the CLI.
func fcE2BRolloutFallback() FCE2BSDKRollout {
	raw := os.Getenv(FCE2BSDKRolloutEnv)
	state := &fcE2BRolloutFallbackState
	state.Lock()
	defer state.Unlock()
	if state.loaded && state.raw == raw {
		return state.rollout
	}
	fallback, err := ParseFCE2BSDKRollout(raw)
	if err != nil {
		slog.Error("FC/E2B SDK rollout fallback ignored; it keeps every operation on the CLI", "error", err)
		fallback = FCE2BSDKRollout{}
	}
	if fcE2BRolloutActive(fallback) {
		slog.Info("FC/E2B SDK rollout fallback active",
			"workspaces", len(fallback.WorkspaceIDs),
			"agents", len(fallback.AgentIDs),
			"runtimes", len(fallback.RuntimeIDs),
			"percent", fallback.Percent,
		)
	}
	state.loaded, state.raw, state.rollout = true, raw, fallback
	return fallback
}

// defaultFCE2BCommandRunner applies only MULTICA_FC_E2B_SDK_ROLLOUT. Servers
// with Diamond runtime configuration replace it with a Diamond-aware runner.
func defaultFCE2BCommandRunner() CommandRunner {
	return NewFCE2BRolloutRunner(nil)
}
