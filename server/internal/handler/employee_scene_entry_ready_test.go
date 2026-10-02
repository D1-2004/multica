package handler

import (
	"context"
	"errors"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/employeememory"
)

func configureEmployeeReadyDependencies(f *dingTalkResponseFixture) {
	f.h.EmployeeMemory = employeememory.NewStore(testPool)
	f.h.DingTalkResponses.BeforeSend = f.h.BeforeEmployeeRunNoticeSend
	f.h.EmployeeRunNoticeArtifacts = f.h.ListEmployeeTaskArtifacts
}

func TestEmployeeSceneReadinessRequiresEveryLiveReplica(t *testing.T) {
	f, model, dc := employeeFixture(t)
	configureEmployeeReadyDependencies(f)
	worker := f.h.EmployeeSceneWorker
	ctx := context.Background()
	checks := 0
	worker.ReplicaReady = func(context.Context) error { checks++; return nil }
	if err := worker.Ready(ctx, dc.WorkspaceID, dc.AgentID); err != nil {
		t.Fatalf("configured and verified Employee runtime is not ready: %v", err)
	}
	if checks != 1 || model.calls != 0 {
		t.Fatalf("ready must verify replicas without a model request: checks=%d model=%d", checks, model.calls)
	}
	oldReplica := errors.New("live replica lacks employee-loop:2")
	worker.ReplicaReady = func(context.Context) error { return oldReplica }
	if err := worker.Ready(ctx, dc.WorkspaceID, dc.AgentID); !errors.Is(err, oldReplica) {
		t.Fatalf("old replica accepted: %v", err)
	}
	worker.ReplicaReady = nil
	if err := worker.Ready(ctx, dc.WorkspaceID, dc.AgentID); err == nil {
		t.Fatal("missing replica verification accepted")
	}
}

func TestEmployeeSceneReadinessRequiresDeliveryAndMemoryDependencies(t *testing.T) {
	for _, tc := range []struct {
		name   string
		remove func(*dingTalkResponseFixture)
	}{
		{"database", func(f *dingTalkResponseFixture) { f.h.Queries = nil }},
		{"transaction", func(f *dingTalkResponseFixture) { f.h.TxStarter = nil }},
		{"send authority fence", func(f *dingTalkResponseFixture) { f.h.DingTalkResponses.BeforeSend = nil }},
		{"artifact reader", func(f *dingTalkResponseFixture) { f.h.EmployeeRunNoticeArtifacts = nil }},
		{"memory", func(f *dingTalkResponseFixture) { f.h.EmployeeMemory = nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, _, dc := employeeFixture(t)
			configureEmployeeReadyDependencies(f)
			tc.remove(f)
			if err := f.h.EmployeeSceneWorker.Ready(context.Background(), dc.WorkspaceID, dc.AgentID); err == nil {
				t.Fatal("incomplete Employee dependency wiring reported ready")
			}
		})
	}
}
