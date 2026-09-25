package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

// FCE2BSDKRolloutEnv holds the JSON rollout that moves FC/E2B operations from
// the e2b CLI to the Go SDK. Unset, empty or invalid keeps every operation on
// the CLI. It is an environment variable rather than a Diamond runtime field
// because that document rejects unknown fields: publishing a new field would
// stop any replica without this code, including other changes in the shared
// pre-release pipeline, from starting.
const FCE2BSDKRolloutEnv = "MULTICA_FC_E2B_SDK_ROLLOUT"

// fcE2BSDKRolloutBucketKey namespaces percent bucketing so this rollout does
// not correlate with feature-flag buckets for the same identifiers.
const fcE2BSDKRolloutBucketKey = "fc_e2b_sdk_transport"

// FCE2BSDKRollout selects the operations that use the Go SDK. The zero value
// selects none. Remove it together with the CLI once production runs on the
// SDK.
type FCE2BSDKRollout struct {
	WorkspaceIDs []string `json:"workspace_ids,omitempty"`
	AgentIDs     []string `json:"agent_ids,omitempty"`
	RuntimeIDs   []string `json:"runtime_ids,omitempty"`
	// Percent buckets scoped operations by agent, then runtime, then
	// workspace. 100 also selects operations that carry no scope, such as
	// the stable-channel template scan.
	Percent int `json:"percent,omitempty"`
}

// ParseFCE2BSDKRollout validates the rollout JSON. Blank input is the empty
// rollout.
func ParseFCE2BSDKRollout(raw string) (FCE2BSDKRollout, error) {
	var rollout FCE2BSDKRollout
	if strings.TrimSpace(raw) == "" {
		return rollout, nil
	}
	decoder := json.NewDecoder(bytes.NewReader([]byte(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&rollout); err != nil {
		return FCE2BSDKRollout{}, fmt.Errorf("invalid %s: %w", FCE2BSDKRolloutEnv, err)
	}
	if decoder.More() {
		return FCE2BSDKRollout{}, fmt.Errorf("invalid %s: trailing data", FCE2BSDKRolloutEnv)
	}
	if rollout.Percent < 0 || rollout.Percent > 100 {
		return FCE2BSDKRollout{}, fmt.Errorf("invalid %s: percent must be between 0 and 100", FCE2BSDKRolloutEnv)
	}
	for _, list := range [][]string{rollout.WorkspaceIDs, rollout.AgentIDs, rollout.RuntimeIDs} {
		for i, id := range list {
			parsed, err := uuid.Parse(strings.TrimSpace(id))
			if err != nil || parsed == uuid.Nil {
				return FCE2BSDKRollout{}, fmt.Errorf("invalid %s: %q is not a UUID", FCE2BSDKRolloutEnv, id)
			}
			list[i] = parsed.String()
		}
	}
	return rollout, nil
}

// Enabled reports whether the rollout can select any operation.
func (r FCE2BSDKRollout) Enabled() bool {
	return r.Percent > 0 || len(r.WorkspaceIDs) > 0 || len(r.AgentIDs) > 0 || len(r.RuntimeIDs) > 0
}

// Selects reports whether an operation with scope uses the SDK.
func (r FCE2BSDKRollout) Selects(scope FCE2BScope) bool {
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
// selects its scope. The choice is made once per operation and never retried
// on the other transport, so an ambiguous create or exec is not repeated.
type FCE2BRolloutRunner struct {
	CLI     CommandRunner
	SDK     CommandRunner
	Rollout FCE2BSDKRollout
}

func (r FCE2BRolloutRunner) Run(ctx context.Context, name string, args []string, env []string) (string, error) {
	scope := fcE2BScopeFrom(ctx)
	if !r.Rollout.Selects(scope) {
		return r.CLI.Run(ctx, name, args, env)
	}
	started := time.Now()
	out, err := r.SDK.Run(ctx, name, args, env)
	// Only SDK operations log here, so CLI-only deployments keep their logs.
	attrs := []any{
		"operation", fcE2BOperationName(args),
		"workspace_id", scope.WorkspaceID,
		"agent_id", scope.AgentID,
		"runtime_id", scope.RuntimeID,
		"duration_ms", time.Since(started).Milliseconds(),
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

// defaultFCE2BCommandRunner reads MULTICA_FC_E2B_SDK_ROLLOUT. A missing or
// invalid rollout keeps every operation on the CLI.
func defaultFCE2BCommandRunner() CommandRunner {
	rollout, err := ParseFCE2BSDKRollout(os.Getenv(FCE2BSDKRolloutEnv))
	if err != nil {
		slog.Error("FC/E2B SDK rollout ignored; every operation uses the CLI", "error", err)
		rollout = FCE2BSDKRollout{}
	}
	return FCE2BRolloutRunner{CLI: OSCommandRunner{}, SDK: SDKCommandRunner{}, Rollout: rollout}
}
