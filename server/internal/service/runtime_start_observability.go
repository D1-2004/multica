package service

import (
	"context"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/startupobs"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"log/slog"
	"time"
)

func (s *TaskService) withStartupObservability(ctx context.Context, task db.AgentTaskQueue) context.Context {
	if !s.CurrentRuntimeStartRecoveryConfig().ForAgent(task.AgentID).StartupObservability {
		return ctx
	}
	opts := TaskLangfuseTraceOptions(task, nil, nil)
	return startupobs.WithRecorder(ctx, func(ctx context.Context, stage string, start time.Time, err error) {
		// Re-read for long launches: an emergency off stops subsequent emissions.
		if !s.CurrentRuntimeStartRecoveryConfig().ForAgent(task.AgentID).StartupObservability {
			return
		}
		status := "succeeded"
		if err != nil {
			status = "failed"
		}
		slog.Info("runtime startup substage", "event", "runtime_start_substage", "task_id", util.UUIDToString(task.ID), "runtime_id", util.UUIDToString(task.RuntimeID), "stage", stage, "status", status, "elapsed_ms", time.Since(start).Milliseconds())
		observation := s.Langfuse.StartObservationInTrace(context.WithoutCancel(ctx), opts, langfuse.ObservationOptions{Type: langfuse.TypeSpan, Name: "runtime_start." + stage, StartTime: start, ParentSpanID: TaskLangfuseRootSpanID(util.UUIDToString(task.ID)), Metadata: map[string]any{"task_id": util.UUIDToString(task.ID), "status": status}})
		observation.End(langfuse.EndOptions{EndTime: time.Now(), StatusMessage: status})
	})
}
