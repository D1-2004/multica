package handler

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
)

// Candidate source hints reuse the filtered dialogue the requester may already
// see. They are neither goal matching nor a second raw-history/authority path.
func employeeTaskSourceHints(task employeetask.Task, entries []employeetask.Entry, visible map[string]employeeentry.RecentConversationMessage) []map[string]any {
	hints := []map[string]any{}
	seen := map[string]bool{}
	appendEntry := func(entry employeetask.Entry) {
		if entry.ActorRef != task.RequesterRef || (entry.Kind != "request" && entry.Kind != "input") || !strings.HasPrefix(entry.Source.Namespace, "employee_scene") {
			return
		}
		var source employeeSourceMessage
		if json.Unmarshal([]byte(entry.Body), &source) != nil || source.RequesterRef != task.RequesterRef || source.SourceRef != source.ReceiptID+"/"+source.Message.OpenMsgID || strings.Split(entry.Source.Key, "/")[0] != source.ReceiptID {
			return
		}
		line, ok := visible[source.SourceRef]
		if !ok || line.Role != "user" || line.SpeakerRef != strings.TrimPrefix(task.RequesterRef, "dingtalk:"+task.Scope.TenantOrgID+":") || seen[source.SourceRef] {
			return
		}
		seen[source.SourceRef] = true
		hints = append(hints, map[string]any{"kind": entry.Kind, "source_ref": source.SourceRef, "observed_at": line.At, "text": employeeTaskData(line.Text, 1200)})
	}
	if len(entries) > 0 {
		appendEntry(entries[0])
	}
	for i := len(entries) - 1; i > 0; i-- {
		before := len(hints)
		appendEntry(entries[i])
		if len(hints) > before {
			break
		}
	}
	return hints
}

func employeeTaskCandidateContext(ctx context.Context, store *employeetask.Store, task employeetask.Task, ref string, visible map[string]employeeentry.RecentConversationMessage) (map[string]any, error) {
	out := map[string]any{"task_ref": ref, "goal": employeeTaskData(task.Definition.Goal, 2000), "state_at_snapshot": task.State, "created_at": task.CreatedAt, "updated_at": task.UpdatedAt, "has_active_run_at_snapshot": task.ActiveRunID != ""}
	// The first request and the recent ledger tail have distinct roles. Read only
	// the bounded tail; no body is shown unless its source remains visible.
	first, err := store.ReadEntries(ctx, task.Scope, task.ID, 0, 1)
	if err != nil {
		return nil, err
	}
	entries := first
	if task.LastEntrySeq > 1 {
		tail, err := store.ReadEntries(ctx, task.Scope, task.ID, max(int64(1), task.LastEntrySeq-20), 20)
		if err != nil {
			return nil, err
		}
		entries = append(entries, tail...)
	}
	out["human_source_links"] = employeeTaskSourceHints(task, entries, visible)
	run, err := store.LatestRun(ctx, task.Scope, task.ID)
	if err != nil && !errors.Is(err, employeetask.ErrNotFound) {
		return nil, err
	}
	if err == nil {
		out["latest_run_at_snapshot"] = map[string]any{"state": run.State, "created_at": run.CreatedAt, "finished_at": run.FinishedAt, "goal_revision": run.GoalRevision}
	}
	return out, nil
}
