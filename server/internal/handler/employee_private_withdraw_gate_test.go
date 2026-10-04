package handler

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/deploymentfence"
)

func TestEmployeePrivateWithdrawalEpochRejectsMixedReaders(t *testing.T) {
	ctx := context.Background()
	current, old := uuid.NewString(), uuid.NewString()
	fence, err := deploymentfence.New(ctx, testPool, current, "candidate "+EmployeeLoopReplicaMarker)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM deployment_fence_replica_ack WHERE instance_id=ANY($1::text[])`, []string{current, old})
	})
	if EmployeeLoopReplicaMarker != "[employee-loop:18]" {
		t.Fatal("cross-origin privacy reader epoch is not advertised")
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO deployment_fence_replica_ack(instance_id,build_id,state,revision,last_seen_at) VALUES($1,'previous [employee-loop:17]','normal',1,now())`, old); err != nil {
		t.Fatal(err)
	}
	ready, err := fence.AllLiveReplicasSupport(ctx, EmployeeLoopReplicaMarker)
	if err != nil || ready {
		t.Fatalf("mixed readers opened privacy gate: %v %v", ready, err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE deployment_fence_replica_ack SET build_id=$2 WHERE instance_id=$1`, old, "upgraded "+EmployeeLoopReplicaMarker); err != nil {
		t.Fatal(err)
	}
	ready, err = fence.AllLiveReplicasSupport(ctx, EmployeeLoopReplicaMarker)
	if err != nil || !ready {
		t.Fatalf("all compatible readers did not recover: %v %v", ready, err)
	}
}
