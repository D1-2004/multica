package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/multica-ai/multica/server/internal/contextcap"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/runtimeconfig"
)

// fcE2BSceneScopeNamespace is the UUIDv5 namespace for a scene+actor sandbox
// bucket. The stored scope_id is opaque; logs carry the scene id and actor.
var fcE2BSceneScopeNamespace = uuid.MustParse("6f1c0a4e-9b3d-5e27-8c41-0a7e5d2b91f4")

// fcE2BConnectionAdmit is set on the launch context when a task joins a
// scene sandbox. Reservation writes the sandbox id onto the start attempt
// before the scene lock is released, so the next launch counts this task.
type fcE2BConnectionAdmit struct {
	AttemptID pgtype.UUID
	TaskID    pgtype.UUID
	RuntimeID pgtype.UUID
	Limit     int
	// Recorded reports whether the scene session row points at the sandbox
	// that was returned. An overflow sandbox leaves it false.
	Recorded *bool
}

type fcE2BConnectionAdmitKey struct{}

func withFCE2BConnectionAdmit(ctx context.Context, admit fcE2BConnectionAdmit) context.Context {
	return context.WithValue(ctx, fcE2BConnectionAdmitKey{}, admit)
}

func fcE2BConnectionAdmitFrom(ctx context.Context) (fcE2BConnectionAdmit, bool) {
	admit, ok := ctx.Value(fcE2BConnectionAdmitKey{}).(fcE2BConnectionAdmit)
	return admit, ok
}

// fcE2BSceneScopeID hashes a scene and its trigger into the UUID column the
// session table already keys. An empty actorKey is the public scene bucket
// and does not collide with a staffId.
func fcE2BSceneScopeID(sceneID, actorKey string) pgtype.UUID {
	id := uuid.NewSHA1(fcE2BSceneScopeNamespace, []byte(sceneID+"\n"+actorKey))
	return pgtype.UUID{Bytes: id, Valid: true}
}

// connectionReuseScope chooses the scene bucket for this task.
// enabled is agent.sandbox_connection_reuse. Off returns skipReason
// disabled. A task that cannot reuse returns a2a, capability, or no_scene.
func connectionReuseScope(task db.AgentTaskQueue, runtime db.AgentRuntime, enabled bool) (fcE2BTaskScope, bool, string) {
	if !enabled {
		return fcE2BTaskScope{}, false, "disabled"
	}
	if IsA2ATaskOrigin(task.Context) {
		return fcE2BTaskScope{}, false, "a2a"
	}
	if !CloudSandboxRuntimeHasCapability(runtime, SandboxConnectionReuseCapability) {
		return fcE2BTaskScope{}, false, "capability"
	}
	scope := contextcap.ScopeFromTaskContext(task.Context)
	if !contextcap.ValidSceneID(scope.SceneID) {
		return fcE2BTaskScope{}, false, "no_scene"
	}
	return fcE2BTaskScope{
		typ:      fcE2BScopeTypeScene,
		id:       fcE2BSceneScopeID(scope.SceneID, scope.PersonKey),
		sceneID:  scope.SceneID,
		actorKey: scope.PersonKey,
	}, true, ""
}

func sceneReuseLimit(ctx context.Context) int {
	limit := runtimeconfig.DefaultFCE2BConnectionReuseTasks
	if admit, ok := fcE2BConnectionAdmitFrom(ctx); ok && admit.Limit > 0 {
		limit = admit.Limit
	}
	return limit
}

func sceneReuseExcludeTask(ctx context.Context) pgtype.UUID {
	if admit, ok := fcE2BConnectionAdmitFrom(ctx); ok && admit.TaskID.Valid {
		return admit.TaskID
	}
	return pgtype.UUID{Valid: true}
}

// fcE2BSceneSandboxOverflow reports that a live scene sandbox is already at
// its task cap. The next task gets a private sandbox and does not move the
// shared session pointer.
func fcE2BSceneSandboxOverflow(active, limit int) bool {
	if limit <= 0 {
		limit = runtimeconfig.DefaultFCE2BConnectionReuseTasks
	}
	return active >= limit
}

// countActiveSandboxTasks counts other tasks whose current start attempt is
// on sandboxID. It runs on the connection that holds the scene lock so it
// does not take a second pool connection. The reservation of this launch is
// committed on that same connection before the lock is released.
func (l *FCE2BLauncher) countActiveSandboxTasks(ctx context.Context, conn *pgxpool.Conn, runtimeID, excludeTask pgtype.UUID, sandboxID string) (int, error) {
	if conn == nil {
		return 0, errors.New("FC/E2B connection reuse occupancy requires the scope-lock connection")
	}
	var n int
	err := conn.QueryRow(ctx, `
SELECT count(DISTINCT t.id)::int
FROM agent_task_queue t
JOIN agent_task_runtime_start_attempt s
  ON s.task_id = t.id AND s.runtime_id = t.runtime_id
WHERE t.runtime_id = $1
  AND t.id <> $2
  AND t.status IN ('queued', 'dispatched', 'running', 'waiting_local_directory')
  AND s.sandbox_id = $3
  AND s.status IN ('starting', 'claimed', 'blocked')`,
		runtimeID, excludeTask, sandboxID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count FC/E2B sandbox tasks: %w", err)
	}
	return n, nil
}

// reserveSceneSandbox writes the sandbox id onto the start attempt while the
// scene lock is still held. Without an admit (chat and issue reuse) it does
// nothing. A failed reservation after the session row was written leaves that
// row in place so the next task can reuse the sandbox.
func (l *FCE2BLauncher) reserveSceneSandbox(ctx context.Context, sandboxID string, coldStart bool) error {
	admit, ok := fcE2BConnectionAdmitFrom(ctx)
	if !ok {
		return nil
	}
	if l == nil || l.Tasks == nil {
		return errors.New("FC/E2B connection reuse reservation requires a task service")
	}
	_, err := l.Tasks.UpdateRuntimeStartSandbox(ctx, db.AgentTaskRuntimeStartAttempt{
		ID:        admit.AttemptID,
		TaskID:    admit.TaskID,
		RuntimeID: admit.RuntimeID,
	}, sandboxID, coldStart, "sandbox_ready")
	if err != nil {
		return fmt.Errorf("reserve FC/E2B scene sandbox: %w", err)
	}
	return nil
}

func (l *FCE2BLauncher) logSceneReuseSkip(taskID, runtimeID, reason string) {
	if reason == "" {
		return
	}
	slog.Info("FC/E2B connection reuse skipped",
		"task_id", taskID,
		"runtime_id", runtimeID,
		"reason", reason,
	)
}
