package employeememory

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// WorkflowUpdate records Host-observed progress. It is not a model tool input.
// The caller supplies lookup results or a committed capture/promotion artifact;
// merely recording this progress grants no trust and performs no promotion.
type WorkflowUpdate struct {
	Step      MemoryWorkflowStep
	Query     string
	Citations []ContextCitation
	Artifact  MemoryWorkflowArtifact
}

// RecordWorkflow persists bounded informational progress on an active learning.
// It does not implement a task gate or call an external memory/model service.
func (s *Store) RecordWorkflow(ctx context.Context, scope Scope, id string, update WorkflowUpdate, e TrustedEvidence) (MemoryWorkflow, error) {
	if _, err := uuid.Parse(id); err != nil {
		return MemoryWorkflow{}, ErrInvalidLearning
	}
	if !validText(e.SourceID, 256) || !validText(e.EvidenceID, 256) || !validText(e.ActorID, 128) || len(update.Query) > 512 || len(update.Citations) > 16 || e.HumanStated && e.VerifiedExecution {
		return MemoryWorkflow{}, ErrInvalidLearning
	}
	raw, err := json.Marshal(update)
	if err != nil || len(raw) > 6000 {
		return MemoryWorkflow{}, ErrInvalidLearning
	}
	switch update.Step {
	case MemoryWorkflowStepLookup:
		if len(update.Citations) == 0 {
			return MemoryWorkflow{}, ErrInvalidLearning
		}
		for _, citation := range update.Citations {
			if !contextCitationHasEvidence(citation) {
				return MemoryWorkflow{}, ErrInvalidLearning
			}
		}
	case MemoryWorkflowStepCapture, MemoryWorkflowStepPromote:
		if memoryWorkflowArtifactKey(update.Artifact) == "" {
			return MemoryWorkflow{}, ErrInvalidLearning
		}
	default:
		return MemoryWorkflow{}, ErrInvalidLearning
	}
	tx, err := s.beginWrite(ctx, scope)
	if err != nil {
		return MemoryWorkflow{}, err
	}
	defer tx.Rollback(ctx)
	if err := tx.QueryRow(ctx, `SELECT record FROM employee_learning WHERE `+scopePredicate+` AND id=$7 AND forgotten_at IS NULL AND superseded_by IS NULL`, append(scope.args(), id)...).Scan(&raw); err != nil {
		return MemoryWorkflow{}, err
	}
	var rec LearningRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return MemoryWorkflow{}, err
	}
	if e.VerifiedExecution && (e.TaskID == "" || e.ExecutionID == "" || e.TaskID != rec.TaskID || e.ExecutionID != rec.ExecutionID) {
		return MemoryWorkflow{}, ErrInvalidLearning
	}
	timestamp := time.Now().UTC().Format(time.RFC3339Nano)
	if rec.Workflow == nil {
		rec.Workflow = &MemoryWorkflow{Status: MemoryWorkflowStatusNotRequired, CreatedAt: timestamp}
	}
	var changed bool
	switch update.Step {
	case MemoryWorkflowStepLookup:
		changed = recordMemoryWorkflowLookup(rec.Workflow, e.ActorID, update.Query, update.Citations, timestamp)
	case MemoryWorkflowStepCapture:
		changed = recordMemoryWorkflowCapture(rec.Workflow, e.ActorID, update.Artifact, timestamp)
	case MemoryWorkflowStepPromote:
		changed = recordMemoryWorkflowPromotion(rec.Workflow, e.ActorID, update.Artifact, timestamp)
	}
	var state *MemoryWorkflowStepState
	switch update.Step {
	case MemoryWorkflowStepLookup:
		state = &rec.Workflow.Lookup
	case MemoryWorkflowStepCapture:
		state = &rec.Workflow.Capture
	case MemoryWorkflowStepPromote:
		state = &rec.Workflow.Promote
	}
	if state.SourceID != e.SourceID || state.EvidenceID != e.EvidenceID {
		state.SourceID = e.SourceID
		state.EvidenceID = e.EvidenceID
		state.UpdatedAt = timestamp
		rec.Workflow.UpdatedAt = timestamp
		changed = true
	}
	if len(rec.Workflow.Citations) > 16 || len(rec.Workflow.Captures) > 16 || len(rec.Workflow.Promotions) > 16 {
		return MemoryWorkflow{}, fmt.Errorf("%w: workflow evidence exceeds limit", ErrInvalidLearning)
	}
	if changed {
		raw, err = json.Marshal(rec)
		if err != nil || len(raw) > 14000 {
			return MemoryWorkflow{}, fmt.Errorf("%w: workflow record exceeds limit", ErrInvalidLearning)
		}
		if _, err := tx.Exec(ctx, `UPDATE employee_learning SET record=$8 WHERE `+scopePredicate+` AND id=$7`, append(scope.args(), id, raw)...); err != nil {
			return MemoryWorkflow{}, err
		}
		if _, err := tx.Exec(ctx, `UPDATE employee_memory_state SET revision=revision+1,updated_at=now() WHERE `+scopePredicate, scope.args()...); err != nil {
			return MemoryWorkflow{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return MemoryWorkflow{}, err
	}
	return *rec.Workflow, nil
}

// Workflow reads only active progress from one exact, directory-checked scope.
func (s *Store) Workflow(ctx context.Context, scope Scope, id string) (MemoryWorkflow, error) {
	if s == nil || s.pool == nil {
		return MemoryWorkflow{}, ErrInvalidScope
	}
	if _, err := uuid.Parse(id); err != nil {
		return MemoryWorkflow{}, ErrInvalidLearning
	}
	if err := authorize(ctx, db.New(s.pool), scope); err != nil {
		return MemoryWorkflow{}, err
	}
	var raw []byte
	if err := s.pool.QueryRow(ctx, `SELECT record FROM employee_learning WHERE `+scopePredicate+` AND id=$7 AND forgotten_at IS NULL AND superseded_by IS NULL`, append(scope.args(), id)...).Scan(&raw); err != nil {
		return MemoryWorkflow{}, err
	}
	var rec LearningRecord
	if err := json.Unmarshal(raw, &rec); err != nil {
		return MemoryWorkflow{}, err
	}
	if rec.Workflow == nil {
		return MemoryWorkflow{Status: MemoryWorkflowStatusNotRequired}, nil
	}
	return *rec.Workflow, nil
}
