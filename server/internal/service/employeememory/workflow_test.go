package employeememory

import (
	"context"
	"testing"

	"github.com/google/uuid"
)

func TestPostgresWorkflowCaptureLookupPromotePersistsWithoutCompletionGate(t *testing.T) {
	pool := memoryPool(t)
	s := NewStore(pool)
	scope := memoryScope(t, pool)
	ctx := context.Background()
	run := VerifiedRun{TaskID: uuid.NewString(), ExecutionID: uuid.NewString(), Title: "日报格式", ProofKind: "checks", Proof: "passed", ActorID: "host", EvidenceID: uuid.NewString(), Passed: true}
	rec, err := s.Distill(ctx, scope, run)
	if err != nil {
		t.Fatal(err)
	}
	if rec.Workflow == nil || rec.Workflow.Required || rec.Workflow.Status != MemoryWorkflowStatusNotRequired || rec.Workflow.Capture.Count != 1 {
		t.Fatalf("capture progress absent or made into gate: %+v", rec.Workflow)
	}
	lookup := WorkflowUpdate{Step: MemoryWorkflowStepLookup, Query: "report", Citations: []ContextCitation{{Backend: "postgres", Source: "learning", SourceID: "one", Title: "Report"}, {Backend: "postgres", Source: "learning", SourceID: "one", Snippet: "retained evidence"}}}
	if _, err := s.RecordWorkflow(ctx, scope, rec.ID, lookup, evidence("lookup")); err != nil {
		t.Fatal(err)
	}
	promotion := WorkflowUpdate{Step: MemoryWorkflowStepPromote, Artifact: MemoryWorkflowArtifact{Backend: "knowledge", Source: "host-promotion", Path: "runbooks/report.md", Title: "Report procedure"}}
	first, err := s.RecordWorkflow(ctx, scope, rec.ID, promotion, evidence("promote"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := s.RecordWorkflow(ctx, scope, rec.ID, promotion, evidence("promote"))
	if err != nil || replay.Promote.Count != first.Promote.Count {
		t.Fatalf("promotion replay: %+v %v", replay, err)
	}
	got, err := NewStore(pool).Workflow(ctx, scope, rec.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Required || got.Status != MemoryWorkflowStatusNotRequired || got.Lookup.Count != 1 || got.Capture.Count != 1 || got.Promote.Count != 1 || len(got.Citations) != 1 || got.Citations[0].Snippet != "retained evidence" || got.Promote.EvidenceID != "evidence-promote" || got.Capture.EvidenceID != run.EvidenceID {
		t.Fatalf("workflow lost step/citation evidence: %+v", got)
	}
	other := scope
	other.Kind = ScopePrivate
	other.PrincipalID = "someone"
	if _, err := s.Workflow(ctx, other, rec.ID); err == nil {
		t.Fatal("workflow crossed private scope")
	}
	if err := s.Reset(ctx, scope); err != nil {
		t.Fatal(err)
	}
	if _, err := s.RecordWorkflow(ctx, scope, rec.ID, promotion, evidence("late-promotion")); err == nil {
		t.Fatal("late workflow write resurrected reset learning")
	}
	if _, err := s.Workflow(ctx, scope, rec.ID); err == nil {
		t.Fatal("forgotten workflow remains readable")
	}
}

func TestPostgresRecordIgnoresModelWorkflow(t *testing.T) {
	pool := memoryPool(t)
	s := NewStore(pool)
	scope := memoryScope(t, pool)
	model := note("model-workflow", "report format")
	model.Workflow = &MemoryWorkflow{Required: true, Status: MemoryWorkflowStatusSatisfied, Promotions: []MemoryWorkflowArtifact{{Path: "model-claimed-promotion"}}}
	rec := record(t, s, scope, model, evidence("capture"))
	if rec.Workflow == nil || rec.Workflow.Required || len(rec.Workflow.Promotions) != 0 || rec.Workflow.Capture.Count != 1 {
		t.Fatalf("model fabricated workflow: %+v", rec.Workflow)
	}
}
