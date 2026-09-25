package service

import (
	"context"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

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
	fallback, err := ParseFCE2BSDKRollout(os.Getenv(FCE2BSDKRolloutEnv))
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
	attrs := []any{
		"operation", fcE2BOperationName(args),
		"rollout_source", source,
		"duration_ms", time.Since(started).Milliseconds(),
	}
	for _, field := range []struct {
		key string
		id  uuid.UUID
	}{{"workspace_id", scope.WorkspaceID}, {"agent_id", scope.AgentID}, {"runtime_id", scope.RuntimeID}} {
		if field.id != uuid.Nil {
			attrs = append(attrs, field.key, field.id)
		}
	}
	if sandboxID := fcE2BOperationSandboxID(args); sandboxID != "" {
		attrs = append(attrs, "sandbox_id", sandboxID)
	}
	if err != nil {
		attrs = append(attrs, "outcome", "error", "error", err)
	} else {
		attrs = append(attrs, "outcome", "ok")
	}
	slog.Info("FC/E2B SDK transport", attrs...)
	return out, err
}

func fcE2BOperationName(args []string) string {
	operation, err := parseFCE2BOperation(args)
	switch {
	case err != nil:
		return "unsupported"
	case operation.create != nil:
		return "sandbox_create"
	case operation.exec != nil && operation.exec.Background:
		return "sandbox_exec_background"
	case operation.exec != nil:
		return "sandbox_exec"
	default:
		return "template_list"
	}
}

func fcE2BOperationSandboxID(args []string) string {
	operation, err := parseFCE2BOperation(args)
	if err != nil || operation.exec == nil {
		return ""
	}
	return operation.exec.SandboxID
}

// defaultFCE2BCommandRunner applies only MULTICA_FC_E2B_SDK_ROLLOUT. Servers
// with Diamond runtime configuration replace it with a Diamond-aware runner.
func defaultFCE2BCommandRunner() CommandRunner {
	return NewFCE2BRolloutRunner(nil)
}
