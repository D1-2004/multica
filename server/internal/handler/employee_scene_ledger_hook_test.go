package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service/employeememory/digest"
)

// A finished wake appends exactly one deterministic ledger entry inside its
// completion transaction; a replayed completion appends nothing.
func TestEmployeeWakeLedgerRecordedOncePerJob(t *testing.T) {
	f, model, dc := employeeFixture(t)
	cleanupEmployeeDigest(t, f.agentID)
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	if response := employeeHTTP(t, f, dc, uuid.NewString()); response.Code != http.StatusAccepted {
		t.Fatal(response.Body.String())
	}
	ctx := context.Background()
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("worker: %v %v", worked, err)
	}
	read := func() (int, digest.LedgerBody, string) {
		t.Helper()
		var count int
		var raw []byte
		var source string
		if err := testPool.QueryRow(ctx, `SELECT count(*),COALESCE(max(entry::text),'{}'),COALESCE(max(source_id),'') FROM employee_scene_ledger WHERE agent_id=$1 AND entry_kind='wake'`, f.agentID).Scan(&count, &raw, &source); err != nil {
			t.Fatal(err)
		}
		var body digest.LedgerBody
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatal(err)
		}
		return count, body, source
	}
	count, body, source := read()
	var jobID string
	if err := testPool.QueryRow(ctx, `SELECT id::text FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if count != 1 || source != jobID || body.JobID != jobID || body.Outcome != "reply" || body.Reply == "" || len(body.SendActionIDs) != 1 || len(body.Requests) != 1 || body.Requests[0].MessageID == "" {
		t.Fatalf("ledger count=%d source=%s body=%+v", count, source, body)
	}
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',available_at=now() WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if worked, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("recovery: %v %v", worked, err)
	}
	if count, _, _ = read(); count != 1 || model.calls != 1 {
		t.Fatalf("replayed completion: ledger=%d model=%d", count, model.calls)
	}
}
