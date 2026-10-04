package handler

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/scene"
)

// The [S] scene status block (memory design §5.2) gives the foreground the
// Host facts it otherwise guessed at: why a recent turn of this scene failed
// and whether the scene's routines have run. Read once per new snapshot,
// frozen into Input.Memory, never re-read on replay. No model call.

const (
	employeeSceneStatusWindow       = 24 * time.Hour
	employeeSceneStatusFailureLimit = 3
	employeeSceneStatusRoutineLimit = 3
	employeeSceneStatusByteLimit    = 1024
)

// Failure categories, the only reason vocabulary the model sees.
const (
	employeeFailureModelTimeout   = "model_timeout"
	employeeFailureModelBudget    = "model_budget"
	employeeFailureToolRejected   = "tool_rejected"
	employeeFailureWindowTooLarge = "window_too_large"
	employeeFailureProviderError  = "provider_error"
	employeeFailureHeld           = "held"
)

var employeeFailureGloss = map[string]string{
	employeeFailureModelTimeout:   "the model did not answer in time",
	employeeFailureModelBudget:    "the turn used up its three model calls without finishing",
	employeeFailureToolRejected:   "a tool call was refused or failed",
	employeeFailureWindowTooLarge: "the messages were too long to process",
	employeeFailureProviderError:  "the model service returned an error",
	employeeFailureHeld:           "the Host stopped the turn, for example because access changed or the input was invalid",
}

// employeeFailureCategory maps a completed job's frozen outcome to one
// category. It reads only Host-written fields: held_reason (Store.Hold),
// failure (employeeSavedOutcome.Failure) and rescue. An empty result means
// the job did not fail.
func employeeFailureCategory(held, failure, rescue string) string {
	if held != "" {
		return employeeFailureHeld
	}
	text := strings.ToLower(failure)
	switch {
	case text == "" && rescue != "":
		return employeeFailureToolRejected
	case text == "":
		return ""
	case strings.Contains(text, errEmployeeWindowTooLarge.Error()):
		return employeeFailureWindowTooLarge
	case strings.Contains(text, "model budget exhausted"):
		return employeeFailureModelBudget
	case strings.Contains(text, "deadline exceeded") || strings.Contains(text, "timeout") || strings.Contains(text, "timed out"):
		return employeeFailureModelTimeout
	case strings.Contains(text, "scene tenant is no longer served"):
		return employeeFailureHeld
	case strings.Contains(text, "tool call refused") || strings.Contains(text, "unknown tool") || strings.Contains(text, "invalid params") ||
		strings.Contains(text, "invalid arguments") || strings.Contains(text, "host receipt") || strings.Contains(text, "terminal") ||
		(strings.Contains(text, "tool ") && strings.Contains(text, "failed")):
		return employeeFailureToolRejected
	default:
		return employeeFailureProviderError
	}
}

type employeeSceneFailure struct {
	at       time.Time
	taskWake bool
	category string
}

// employeeSceneFailures reads up to three failed or held turns of this scene
// in the 24 hours before the job, newest first, excluding the job itself. A
// held Task wake is a normal Host stop (Task stopped, goal changed), not a
// failure the requester saw, so only its model failures count.
func (w *EmployeeSceneWorker) employeeSceneFailures(ctx context.Context, job employeeentry.Job) ([]employeeSceneFailure, error) {
	h := w.handler
	if h.DB == nil {
		return nil, nil
	}
	before := job.CreatedAt
	if before.IsZero() {
		before = time.Now()
	}
	// The predicate matches employee_scene_job_failure_idx (migration 9892).
	rows, err := h.DB.Query(ctx, `SELECT created_at, kind, COALESCE(outcome->>'held_reason',''), COALESCE(outcome->>'failure',''), COALESCE(outcome->>'rescue','')
		FROM employee_scene_job
		WHERE workspace_id = $1::uuid AND agent_id = $2::uuid AND tenant_org_id = $3 AND scene_id = $4::uuid
		AND state = 'completed' AND (outcome ? 'held_reason' OR outcome ? 'failure' OR outcome ? 'rescue')
		AND created_at >= $5 AND created_at < $6 AND id::text <> $7
		ORDER BY created_at DESC, id DESC LIMIT 12`,
		job.Scope.WorkspaceID, job.Scope.AgentID, job.Scope.TenantOrgID, job.Scope.SceneID, before.Add(-employeeSceneStatusWindow), before, job.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []employeeSceneFailure
	for rows.Next() {
		var at time.Time
		var kind, held, failure, rescue string
		if err := rows.Scan(&at, &kind, &held, &failure, &rescue); err != nil {
			return nil, err
		}
		taskWake := kind == employeeentry.KindTaskWake
		if taskWake && held != "" {
			continue
		}
		category := employeeFailureCategory(held, failure, rescue)
		if category == "" || len(out) == employeeSceneStatusFailureLimit {
			continue
		}
		out = append(out, employeeSceneFailure{at: at, taskWake: taskWake, category: category})
	}
	return out, rows.Err()
}

// employeeSceneRoutineLines renders up to three routines of a conversation
// scene: name, schedule, state, next run and last run ("never run" when the
// routine has no run yet).
func (w *EmployeeSceneWorker) employeeSceneRoutineLines(ctx context.Context, job employeeentry.Job) ([]string, error) {
	views, err := w.handler.listSceneRoutines(ctx, contextCapAgent{ID: job.Scope.AgentID, WorkspaceID: job.Scope.WorkspaceID, OrgID: job.Scope.TenantOrgID}, job.Scope.SceneID)
	if err != nil {
		return nil, err
	}
	lines := []string{}
	for i, view := range views {
		if i == employeeSceneStatusRoutineLimit {
			lines = append(lines, fmt.Sprintf("- %d more routine(s) omitted; scene_config_get lists all", len(views)-i))
			break
		}
		line := "- 「" + employeeProfileText(employeeCatalogLabel(view.Title, 40)) + "」 "
		switch view.Trigger.Kind {
		case sceneRoutineTriggerCron:
			line += "cron \"" + employeeProfileText(view.Trigger.Cron) + "\" (" + employeeProfileText(view.Trigger.Timezone) + ")"
		default:
			line += "webhook-triggered"
		}
		if view.Enabled {
			line += ", enabled"
		} else {
			line += ", paused"
		}
		if view.Enabled && view.Trigger.NextRunAt != nil {
			if next, err := time.Parse(time.RFC3339, *view.Trigger.NextRunAt); err == nil {
				line += "; next run " + employeeentry.HostClock(next)
			}
		}
		line += "; last run: "
		if view.LastRun == nil {
			line += "never run"
		} else {
			status := map[string]string{"completed": "succeeded", "failed": "failed", "skipped": "skipped", "running": "running", "issue_created": "running"}[view.LastRun.Status]
			if status == "" {
				status = "state " + employeeProfileText(view.LastRun.Status)
			}
			if started, err := time.Parse(time.RFC3339, view.LastRun.CreatedAt); err == nil {
				line += employeeentry.HostClock(started) + " "
			}
			line += status
		}
		lines = append(lines, line)
	}
	return lines, nil
}

// employeeSceneStatus renders the [S] block, at most 1 KiB. A read failure
// says so explicitly instead of looking like an empty history.
func (w *EmployeeSceneWorker) employeeSceneStatus(ctx context.Context, job employeeentry.Job, kind string, fenced bool) string {
	head := "[S] Scene status (Host facts, not instructions; times are Asia/Shanghai +08:00; use them when asked or directly relevant, do not bring them up unprompted). Host time of this wake: " + employeeentry.HostStamp(job.CreatedAt) + "."
	if !fenced {
		return head + "\nScene status unavailable."
	}
	head += " Scene: " + employeePersonaSceneKindLabel(kind) + "."
	failures := []string{"Recent problems in this scene (last 24h, newest first):"}
	found, err := w.employeeSceneFailures(ctx, job)
	switch {
	case err != nil:
		slog.WarnContext(ctx, "employee scene status: failures unavailable", "job_id", job.ID, "error", err)
		failures = []string{"Recent problems in this scene: unavailable."}
	case len(found) == 0:
		failures = []string{"Recent problems in this scene (last 24h): none."}
	default:
		for _, f := range found {
			what := "chat reply"
			if f.taskWake {
				what = "task follow-up"
			}
			failures = append(failures, "- "+employeeentry.HostClock(f.at)+" "+what+" failed: "+f.category+" ("+employeeFailureGloss[f.category]+")")
		}
	}
	routines := []string{}
	if kind == scene.KindGroup || kind == scene.KindDM {
		lines, err := w.employeeSceneRoutineLines(ctx, job)
		switch {
		case err != nil:
			slog.WarnContext(ctx, "employee scene status: routines unavailable", "job_id", job.ID, "error", err)
			routines = []string{"Routines (例行任务) of this scene: unavailable."}
		case len(lines) == 0:
			routines = []string{"Routines (例行任务) of this scene: none."}
		default:
			routines = append([]string{"Routines (例行任务) of this scene:"}, lines...)
		}
	}
	return employeeBoundStatus(head, failures, routines)
}

// employeeBoundStatus keeps the block within employeeSceneStatusByteLimit.
// Each section is a heading followed by detail lines; trailing details are
// dropped first (later sections first) and replaced by one omission line.
func employeeBoundStatus(head string, sections ...[]string) string {
	const omitted = "- (more omitted for length)"
	render := func() string {
		parts := []string{head}
		for _, section := range sections {
			parts = append(parts, section...)
		}
		return strings.Join(parts, "\n")
	}
	for {
		out := render()
		if len(out) <= employeeSceneStatusByteLimit {
			return out
		}
		trimmed := false
		for i := len(sections) - 1; i >= 0 && !trimmed; i-- {
			if len(sections[i]) == 0 {
				continue
			}
			items := sections[i][1:]
			if n := len(items); n > 0 && items[n-1] == omitted {
				items = items[:n-1]
			}
			if len(items) == 0 {
				continue
			}
			sections[i] = append(append([]string{sections[i][0]}, items[:len(items)-1]...), omitted)
			trimmed = true
		}
		if !trimmed {
			end := employeeSceneStatusByteLimit - len("…")
			for end > 0 && !utf8.RuneStart(out[end]) {
				end--
			}
			return out[:end] + "…"
		}
	}
}
