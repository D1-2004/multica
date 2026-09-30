package service

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/pkg/featureflag"
	"github.com/multica-ai/multica/server/pkg/redact"
	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

// fcE2BSDKRolloutBucketKey namespaces percent bucketing so this rollout does
// not correlate with feature-flag buckets for the same identifiers.
const fcE2BSDKRolloutBucketKey = "fc_e2b_sdk_transport"

// Sources of the rollout that decided an operation: the snapshot a launch or
// stop froze, or the live runtime document for a standalone operation.
const (
	fcE2BRolloutSourceSnapshot = "snapshot"
	fcE2BRolloutSourceLive     = "live"
)

// FCE2BSDKRollout is runtime.fc_e2b_sdk_rollout. Remove it together with the
// CLI once production runs on the SDK.
type FCE2BSDKRollout = runtimeconfig.FCE2BSDKRollout

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
	for _, id := range []uuid.UUID{scope.AgentID, scope.RuntimeID, scope.WorkspaceID} {
		if id != uuid.Nil {
			return featureflag.InPercent(fcE2BSDKRolloutBucketKey, id.String(), r.Percent)
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

// fcE2BFrozenRolloutKey carries the rollout of the configuration snapshot a
// launch or stop froze, so all of its commands use one generation.
type fcE2BFrozenRolloutKey struct{}

// withFCE2BFrozenRollout attaches a frozen snapshot's rollout to operations
// started under ctx.
func withFCE2BFrozenRollout(ctx context.Context, rollout FCE2BSDKRollout) context.Context {
	return context.WithValue(ctx, fcE2BFrozenRolloutKey{}, rollout)
}

// FCE2BRolloutRunner sends each operation to the CLI unless
// runtime.fc_e2b_sdk_rollout selects its scope. The rollout comes from the
// snapshot the caller froze, else from Rollout, which reads the live runtime
// document, so a Diamond update applies to the next launch without a
// restart. The choice is made once per operation and never retried on the
// other transport, so an ambiguous create or exec is not repeated.
type FCE2BRolloutRunner struct {
	CLI CommandRunner
	SDK CommandRunner
	// Rollout returns the rollout of the current runtime document. Nil
	// selects nothing.
	Rollout func() FCE2BSDKRollout
}

// NewFCE2BRolloutRunner routes by the runtime document through rollout. A
// nil rollout means the deployment has no runtime document: only frozen
// snapshots, which then carry no rollout either, can select the SDK.
func NewFCE2BRolloutRunner(rollout func() FCE2BSDKRollout) FCE2BRolloutRunner {
	return FCE2BRolloutRunner{CLI: OSCommandRunner{}, SDK: SDKCommandRunner{}, Rollout: rollout}
}

func (r FCE2BRolloutRunner) Run(ctx context.Context, name string, args []string, env []string) (string, error) {
	scope := fcE2BScopeFrom(ctx)
	rollout, frozen := ctx.Value(fcE2BFrozenRolloutKey{}).(FCE2BSDKRollout)
	source := fcE2BRolloutSourceSnapshot
	if !frozen {
		source = fcE2BRolloutSourceLive
		if r.Rollout != nil {
			rollout = r.Rollout()
		}
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

// defaultFCE2BCommandRunner follows only frozen snapshots. Servers with the
// Diamond runtime document replace it with a runner that also reads the live
// document.
func defaultFCE2BCommandRunner() CommandRunner {
	return NewFCE2BRolloutRunner(nil)
}
