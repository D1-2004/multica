package dshhost

import (
	"context"
	"fmt"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/startupobs"
	"testing"
	"time"
)

func TestProvisionReportsEachDurableSubstage(t *testing.T) {
	stages := map[string]int{}
	ctx := startupobs.WithRecorder(context.Background(), func(_ context.Context, name string, _ time.Time, err error) {
		if err != nil {
			t.Error(err)
		}
		stages[name]++
	})
	_, err := (Provisioner{Store: &provisionMemory{}, Provider: &provisionCloud{ambiguousStep: -1}}).Ensure(ctx, Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, provisionSpec())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 6; i++ {
		if stages[fmt.Sprintf("nas_step_%d", i)] != 1 {
			t.Fatalf("missing step %d: %v", i, stages)
		}
	}
	if stages["nas_provisioning"] != 1 {
		t.Fatal("total span missing")
	}
}
