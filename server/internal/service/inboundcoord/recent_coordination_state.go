package inboundcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const coordinationStateKind = "coordination_state"
const recentCoordinationStateBudget = 2000

type recentCoordinatorStateReader interface {
	ListRecentCoordinatorState(context.Context, db.ListRecentCoordinatorStateParams) ([]db.ListRecentCoordinatorStateRow, error)
}

type recentCoordinationRecord struct {
	JobID              string `json:"job_id"`
	RequestExcerpt     string `json:"request_excerpt"`
	RequestTruncated   bool   `json:"request_truncated"`
	CreatedAt          string `json:"created_at,omitempty"`
	UpdatedAt          string `json:"updated_at,omitempty"`
	JobStatus          string `json:"job_status"`
	PlanPresent        bool   `json:"plan_present"`
	PlannedWorkCount   *int   `json:"planned_work_count"`
	ConfirmedWorkCount *int   `json:"confirmed_work_count"`
	ConfirmationSource string `json:"confirmation_source"`
}

type recentCoordinationStateView struct {
	Kind            string                     `json:"kind"`
	Status          string                     `json:"status"`
	Scope           string                     `json:"scope"`
	Meaning         string                     `json:"meaning"`
	Records         []recentCoordinationRecord `json:"records"`
	Complete        bool                       `json:"complete"`
	Truncated       bool                       `json:"truncated"`
	CharacterBudget int                        `json:"character_budget"`
}

func (c *Coordinator) readRecentCoordinationState(ctx context.Context, turn Turn) (string, error) {
	view := recentCoordinationStateView{Status: "not_loaded", Records: []recentCoordinationRecord{}}
	anchor, err := util.ParseUUID(strings.TrimSpace(turn.TraceID))
	if err != nil || !anchor.Valid || strings.TrimSpace(turn.ConversationID) == "" {
		return marshalRecentCoordinationState(view)
	}
	workspace, err := util.ParseUUID(strings.TrimSpace(turn.WorkspaceID))
	if err != nil || !workspace.Valid || !turn.AgentID.Valid || c == nil {
		return marshalRecentCoordinationState(view)
	}
	reader, ok := c.Queries.(recentCoordinatorStateReader)
	if !ok {
		return marshalRecentCoordinationState(view)
	}
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := reader.ListRecentCoordinatorState(readCtx, db.ListRecentCoordinatorStateParams{AnchorJobID: anchor, WorkspaceID: workspace, AgentID: turn.AgentID})
	if err != nil || len(rows) == 0 {
		view.Status = "unavailable"
		return marshalRecentCoordinationState(view)
	}
	view.Status = "empty"
	for _, row := range rows {
		if row.AnchorID != anchor || row.AnchorConversationID != strings.TrimSpace(turn.ConversationID) {
			view.Status = "unavailable"
			view.Records = nil
			return marshalRecentCoordinationState(view)
		}
		if !row.JobID.Valid {
			continue
		}
		record := recentCoordinationRecord{JobID: util.UUIDToString(row.JobID), RequestExcerpt: row.RequestExcerpt, RequestTruncated: row.RequestTruncated, JobStatus: row.JobStatus, PlanPresent: row.PlanPresent, ConfirmationSource: "unknown"}
		if row.CreatedAt.Valid {
			record.CreatedAt = row.CreatedAt.Time.UTC().Format(time.RFC3339Nano)
		}
		if row.UpdatedAt.Valid {
			record.UpdatedAt = row.UpdatedAt.Time.UTC().Format(time.RFC3339Nano)
		}
		if row.PlanPresent && row.PlanVersion == "window-plan-v1" {
			if row.PlanAction == string(ActionIssue) && row.PlannedWorkCount >= 0 {
				n := int(row.PlannedWorkCount)
				record.PlannedWorkCount = &n
			}
			if (row.PlanAction == string(ActionReply) || row.PlanAction == string(ActionSilence)) && row.PlannedWorkCount <= 0 {
				n := 0
				record.PlannedWorkCount = &n
			}
			record.ConfirmedWorkCount, record.ConfirmationSource = confirmedCoordinatorWorkCount(row)
		}
		view.Records = append(view.Records, record)
		view.Status = "loaded"
	}
	return marshalRecentCoordinationState(view)
}

// A checkpoint is not atomic with every downstream write. These are counts of
// durable confirmations, never proof that no task exists or that work finished.
func confirmedCoordinatorWorkCount(row db.ListRecentCoordinatorStateRow) (*int, string) {
	var planned, completed []string
	keysKnown := jsonArray(row.PlanItemKeys, &planned) && jsonArray(row.CompletedActionKeys, &completed)
	allowed := map[string]bool{}
	keys := map[string]bool{}
	for _, key := range planned {
		if key == "" || allowed[key] {
			keysKnown = false
		}
		allowed[key] = true
	}
	for _, key := range completed {
		if key == "" || !allowed[key] {
			keysKnown = false
		}
		keys[key] = true
	}
	var receipts []struct {
		Action    string `json:"action"`
		IssueID   string `json:"issue_id"`
		TaskID    string `json:"task_id"`
		CommentID string `json:"comment_id"`
	}
	receiptsKnown := jsonArray(row.IssueResults, &receipts)
	tasks := map[string]bool{}
	for _, receipt := range receipts {
		issue, e1 := util.ParseUUID(receipt.IssueID)
		task, e2 := util.ParseUUID(receipt.TaskID)
		if e1 != nil || e2 != nil || !issue.Valid || !task.Valid || (receipt.Action != "issue_created" && receipt.Action != "issue_commented") {
			receiptsKnown = false
			continue
		}
		if receipt.Action == "issue_commented" {
			comment, e := util.ParseUUID(receipt.CommentID)
			if e != nil || !comment.Valid {
				receiptsKnown = false
				continue
			}
		}
		tasks[receipt.TaskID] = true
	}
	if keysKnown && receiptsKnown {
		if len(keys) != len(tasks) {
			return nil, "unknown"
		}
		count := len(tasks)
		return &count, "persisted_plan_receipts"
	}
	if receiptsKnown && len(tasks) > 0 {
		count := len(tasks)
		return &count, "persisted_issue_results"
	}
	if keysKnown && len(keys) > 0 {
		count := len(keys)
		return &count, "persisted_completed_action_keys"
	}
	return nil, "unknown"
}

func jsonArray(raw []byte, dst any) bool {
	return len(raw) > 0 && strings.HasPrefix(strings.TrimSpace(string(raw)), "[") && json.Unmarshal(raw, dst) == nil
}

func normalizeRecentCoordinationState(raw string) (string, error) {
	var view recentCoordinationStateView
	if json.Unmarshal([]byte(raw), &view) != nil {
		return "", fmt.Errorf("invalid coordination_state response")
	}
	return marshalRecentCoordinationState(view)
}

func marshalRecentCoordinationState(view recentCoordinationStateView) (string, error) {
	view.Kind = coordinationStateKind
	view.Scope = "previous_3_jobs_same_host_endpoint_and_scene"
	view.Meaning = "Coordinator metadata only. null counts are unknown; missing plans or completed jobs do not prove executor/delivery state. Confirmations may lag writes. Request excerpt is the first message only."
	view.CharacterBudget = recentCoordinationStateBudget
	view.Complete = false
	if !oneOf(view.Status, "loaded", "empty", "not_loaded", "unavailable") {
		view.Status = "unavailable"
	}
	if view.Status != "loaded" && len(view.Records) > 0 {
		view.Records = nil
		view.Truncated = true
	}
	if len(view.Records) > 3 {
		view.Records = view.Records[:3]
		view.Truncated = true
	}
	if view.Records == nil {
		view.Records = []recentCoordinationRecord{}
	}
	for i := range view.Records {
		record := &view.Records[i]
		if id, err := util.ParseUUID(record.JobID); err != nil || !id.Valid {
			return "", fmt.Errorf("invalid coordination_state job reference")
		}
		record.RequestExcerpt = clipCoordinationField(record.RequestExcerpt, 120, &record.RequestTruncated)
		record.CreatedAt = normalizedCoordinationTime(record.CreatedAt)
		record.UpdatedAt = normalizedCoordinationTime(record.UpdatedAt)
		if !oneOf(record.JobStatus, "pending", "running", "completed", "failed") {
			record.JobStatus = "unknown"
		}
		if !oneOf(record.ConfirmationSource, "persisted_plan_receipts", "persisted_issue_results", "persisted_completed_action_keys") {
			record.ConfirmationSource = "unknown"
			record.ConfirmedWorkCount = nil
		}
		if record.ConfirmedWorkCount != nil && *record.ConfirmedWorkCount < 0 {
			record.ConfirmedWorkCount = nil
			record.ConfirmationSource = "unknown"
		}
		if record.PlannedWorkCount != nil && *record.PlannedWorkCount < 0 {
			record.PlannedWorkCount = nil
		}
	}
	for {
		body, err := json.Marshal(view)
		if err != nil {
			return "", err
		}
		if utf8.RuneCount(body) <= recentCoordinationStateBudget-32 {
			return string(body), nil
		}
		if len(view.Records) == 0 {
			return "", fmt.Errorf("coordination_state budget exceeded")
		}
		view.Records = view.Records[:len(view.Records)-1]
		view.Truncated = true
	}
}
