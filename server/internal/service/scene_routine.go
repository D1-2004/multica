package service

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/jackc/pgx/v5"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

// SceneRoutines binds the autopilots that are scene routines (例行任务 of an
// Agent work scene, context_scope_routine) to their scene. The handler
// implements it; a nil value leaves every autopilot ordinary.
type SceneRoutines interface {
	// RoutineRuntimeContext returns the task context a run of ap carries:
	// the routine's SceneRef plus its frozen binding (agent_scene,
	// scene_routine). It returns nil for an ordinary autopilot and an
	// ErrSceneRoutineUnusable error when the routine's scene can no longer
	// be used (the run is then recorded as skipped, never run without it).
	RoutineRuntimeContext(ctx context.Context, ap db.Autopilot) ([]byte, error)
	// RoutineTaskQueued posts the start notice of a routine run into its
	// scene. Best effort: the notice is idempotent on the run id.
	RoutineTaskQueued(ctx context.Context, ap db.Autopilot, run db.AutopilotRun, task db.AgentTaskQueue)
	// RoutineTaskFinished enqueues the end notice of a routine run inside the
	// task's terminal transaction (tx is nil without a transaction starter).
	// An error rolls the terminal transition back, so it is returned only
	// for database failures; an unusable scene is logged and skipped.
	RoutineTaskFinished(ctx context.Context, tx pgx.Tx, task db.AgentTaskQueue, status string, result []byte, errMessage string) error
	// RoutineTaskSettled posts the end notice of a routine run whose task
	// reached a terminal status without a completion transaction (cancel,
	// the stale-task sweeper, a runtime that failed to start). Best effort:
	// it does nothing when the end notice exists or another attempt of the
	// run is still active.
	RoutineTaskSettled(ctx context.Context, task db.AgentTaskQueue)
}

// ErrSceneRoutineUnusable marks a routine whose scene can no longer be used:
// it belongs to an org the agent no longer serves, or it is gone.
var ErrSceneRoutineUnusable = errors.New("scene routine scene is unusable")

// IsSceneRoutineContext reports whether a task or run context carries a scene
// routine binding.
func IsSceneRoutineContext(raw []byte) bool {
	if len(raw) == 0 {
		return false
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return false
	}
	routine, ok := envelope[protocol.SceneRoutineContextKey]
	return ok && len(routine) > 0 && string(routine) != "null"
}

// withSceneRoutine returns the service to dispatch ap with: a copy carrying
// the routine's runtime context when ap is a scene routine, s itself
// otherwise. skipReason is set when the routine's scene is unusable.
func (s *AutopilotService) withSceneRoutine(ctx context.Context, ap db.Autopilot) (svc *AutopilotService, skipReason string, err error) {
	if s.SceneRoutines == nil || len(s.RuntimeContext) > 0 {
		return s, "", nil
	}
	runtimeContext, err := s.SceneRoutines.RoutineRuntimeContext(ctx, ap)
	switch {
	case errors.Is(err, ErrSceneRoutineUnusable):
		return s, formatAdmissionReason(ap, err.Error()), nil
	case err != nil:
		return s, "", err
	case len(runtimeContext) == 0:
		return s, "", nil
	}
	copy := *s
	copy.RuntimeContext = runtimeContext
	return &copy, "", nil
}
