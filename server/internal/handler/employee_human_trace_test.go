package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// This proves the observability boundary against a real local journal. It does
// not send DingTalk messages or execute a real Pi process.
func TestEmployeeHumanTraceExportsCommittedEffectAndDoesNotInventReplay(t *testing.T) {
	if os.Getenv("DATABASE_URL") == "" {
		t.Skip("requires explicit isolated DATABASE_URL")
	}
	ctx := context.Background()
	c := newCollectionHarness(t)
	client, exporter := employeeTraceClient(t)
	c.f.h.EmployeeSceneWorker.Langfuse = client
	if response := employeeHTTP(t, c.f, c.dc, uuid.NewString()); response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	c.process()
	var sourceJob string
	if err := testPool.QueryRow(ctx, `SELECT id::text FROM employee_scene_job WHERE agent_id=$1::uuid`, c.f.agentID).Scan(&sourceJob); err != nil {
		t.Fatal(err)
	}
	r := humanResponseWorkerFixture(t, c.f, sourceJob, "", "")
	c.model.set(func(string) (string, map[string]any) {
		return wakeToolCall("trace-human-work", "continue_question_work", map[string]any{"prompt": "整理简短版，Authorization: Bearer HUMAN_TRACE_SECRET", "reply": "我来整理。"})
	})
	c.process()
	check := func() {
		var outputs []string
		for _, span := range employeeTraceKind(exporter, "tool") {
			if span.Name == "continue_question_work" {
				outputs = append(outputs, employeeTraceAttr(span, "langfuse.observation.output"))
			}
		}
		if len(outputs) != 1 {
			t.Fatal("journal replay manufactured a tool effect observation", len(outputs))
		}
		var result struct{ Content, Receipt string }
		if err := json.Unmarshal([]byte(outputs[0]), &result); err != nil {
			t.Fatal(err, outputs[0])
		}
		var ids struct {
			TaskID  string `json:"task_id"`
			RunID   string `json:"run_id"`
			QueueID string `json:"queue_task_id"`
		}
		if json.Unmarshal([]byte(result.Content), &ids) != nil || ids.TaskID == "" || ids.QueueID == "" || ids.RunID != result.Receipt {
			t.Fatal("typed Host result lacks actual effect mapping", outputs[0])
		}
		var count int
		if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_task_run WHERE id=$1::uuid AND task_id=$2::uuid AND queue_task_id=$3::uuid`, ids.RunID, ids.TaskID, ids.QueueID).Scan(&count); err != nil || count != 1 {
			t.Fatal("trace receipt does not identify committed execution", count, err)
		}
		for _, span := range exporter.GetSpans() {
			raw, _ := json.Marshal(span)
			if strings.Contains(string(raw), "HUMAN_TRACE_SECRET") {
				t.Fatal("secret escaped existing trace redaction")
			}
		}
		raw, _ := json.Marshal(exporter.GetSpans())
		for _, id := range []string{r.QuestionID, r.ID, sourceJob, ids.RunID, ids.QueueID} {
			if !strings.Contains(string(raw), id) {
				t.Fatal("trace is not joinable by its real business ids", id)
			}
		}
	}
	check()
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',outcome=NULL,available_at=now() WHERE id=$1::uuid`, r.JobID); err != nil {
		t.Fatal(err)
	}
	c.process()
	check()
	assertHumanResponseRun(t, c.f, r, "", 1, 1)
}
