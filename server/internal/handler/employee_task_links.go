package handler

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/employeeverification"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
)

// dispatchUpstream resolves dispatch_task's builds_on refs. Each ref must be a
// task candidate bound to this wake's selected source, so it is the same
// requester's Employee Direct task in this scene, and it must have a
// successful Run. Its latest report becomes a bounded UPSTREAM RESULTS
// material; the link and the report grant no access.
func (h *employeeSceneHost) dispatchUpstream(ctx context.Context, tx pgx.Tx, source employeeSourceMessage, env employeeDispatchEnvelope, call employeeloop.ToolCall) ([]string, []employeetask.PacketMaterial, error) {
	refs, err := optionalStringArray(call.Arguments, "builds_on")
	if err != nil || len(refs) == 0 {
		return nil, nil, err
	}
	if len(refs) > employeetask.MaxBuildsOn {
		return nil, nil, fmt.Errorf("builds_on accepts at most %d task refs", employeetask.MaxBuildsOn)
	}
	store := employeetask.NewStore(tx)
	seen := map[string]bool{}
	var ids []string
	var materials []employeetask.PacketMaterial
	for _, ref := range refs {
		binding, err := h.currentTaskBinding(ctx, tx, source, ref)
		if err != nil {
			return nil, nil, fmt.Errorf("builds_on %q is not a task candidate of this source", ref)
		}
		if seen[binding.TaskID] {
			return nil, nil, errors.New("builds_on lists the same task twice")
		}
		seen[binding.TaskID] = true
		task, err := store.Get(ctx, h.taskScope(), binding.TaskID)
		if err != nil {
			return nil, nil, err
		}
		if task.RequesterRef != source.RequesterRef || task.OwnerLoop != employeetask.LoopEmployee || task.DispatchMode != employeetask.DispatchDirect {
			return nil, nil, fmt.Errorf("builds_on %q is not this requester's task", ref)
		}
		report, err := store.UpstreamReport(ctx, h.taskScope(), task.ID)
		if err != nil {
			return nil, nil, err
		}
		if report.RunID == "" {
			return nil, nil, fmt.Errorf("builds_on %q has no successful result yet; wait for it or ask the requester before building on it", ref)
		}
		gate := ""
		if g, err := employeeverification.GateTx(ctx, tx, h.taskScope(), task.ID, report.RunID); err == nil && g.Status != employeeverification.GateNone {
			gate = string(g.Status)
		}
		ids = append(ids, task.ID)
		materials = append(materials, employeetask.PacketMaterial{Ref: "upstream:" + task.ID, Scope: h.taskScope(), PrincipalID: env.PrincipalID, Body: employeeUpstreamBody(ref, report, gate)})
	}
	return ids, materials, nil
}

// employeeUpstreamBody renders one upstream report within the Compiler's
// per-report bound. The report is redacted executor output, not delivery proof.
func employeeUpstreamBody(ref string, r employeetask.UpstreamReport, gate string) string {
	lines := []string{
		fmt.Sprintf("Task %s, goal: %s", ref, employeeTaskData(r.Goal, 400)),
		fmt.Sprintf("Current state: %s; latest successful run %s (goal revision %d)", r.State, r.RunID, r.GoalRevision),
	}
	if gate != "" {
		lines = append(lines, "Host verification of that run: "+gate)
	}
	lines = append(lines, "Executor report (not delivery proof):", employeeTaskData(r.Result, 3000))
	body := strings.Join(lines, "\n")
	if len(body) > employeetask.MaxUpstreamReportBytes {
		body = strings.ToValidUTF8(body[:employeetask.MaxUpstreamReportBytes], "")
	}
	return body
}
