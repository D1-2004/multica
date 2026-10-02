package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
)

const noticeFileReadError = "文件发送失败（工具返回原文）：`--file /workspace/missing.txt cannot read: no such file or directory`"

func setNoticeExecutionOutput(t *testing.T, f employeeNoticeFixture, output string) {
	t.Helper()
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE employee_task_run SET result=$2 WHERE id=$1::uuid`, f.runID, output); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET result=jsonb_build_object('output',$2::text) WHERE id=$1::uuid`, f.queueID, output); err != nil {
		t.Fatal(err)
	}
}
func TestEmployeeNoticeUnconfirmedAndFailedKeepAttributedTaskOutput(t *testing.T) {
	for _, state := range []string{"unknown", "failed", "client_failed"} {
		for _, output := range []string{noticeFileReadError, "文件已发送成功。"} {
			t.Run(state+"/"+output, func(t *testing.T) {
				f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
				setNoticeExecutionOutput(t, f, output)
				reported := "unknown"
				if state == "client_failed" {
					reported = "failed"
				}
				noticeReceipt(t, f, &fileNoticeProvider{}, reported, f.command.Event.Data.Conversation.OpenConversationID)
				if state == "failed" {
					if _, err := testPool.Exec(context.Background(), `UPDATE sandbox_send_receipt SET state='failed',error_code='provider_failed' WHERE task_id=$1::uuid`, f.queueID); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
					t.Fatal(err)
				}
				var body string
				if err := testPool.QueryRow(context.Background(), `SELECT body FROM employee_run_notice WHERE run_id=$1::uuid`, f.runID).Scan(&body); err != nil {
					t.Fatal(err)
				}
				want := "本次执行已结束，但文件是否送达尚未确认。"
				if state == "failed" {
					want = "本次执行已结束，但文件发送失败。"
				}
				if !strings.HasPrefix(body, want) || !strings.Contains(body, "任务返回内容（不作为送达确认）：\n> "+output) {
					t.Fatal("state boundary or original output was lost", body)
				}
				if state == "client_failed" {
					var stored string
					if err := testPool.QueryRow(context.Background(), `SELECT state FROM sandbox_send_receipt WHERE task_id=$1::uuid`, f.queueID).Scan(&stored); err != nil || stored != "unknown" {
						t.Fatal("client exit became provider-confirmed failure", stored, err)
					}
				}
			})
		}
	}
}

// A synchronized writer also captures incidental background service logs safely.
type noticeLogBuffer struct {
	mu sync.Mutex
	bytes.Buffer
}

func (b *noticeLogBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.Write(p)
}
func (b *noticeLogBuffer) snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.Buffer.String()
}
func captureNoticeLogs(t *testing.T) *noticeLogBuffer {
	t.Helper()
	b := &noticeLogBuffer{}
	prior := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(b, nil)))
	t.Cleanup(func() { slog.SetDefault(prior) })
	return b
}
func noticeLogEvents(t *testing.T, b *noticeLogBuffer, event string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(b.snapshot()), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		if row["event"] == event {
			out = append(out, row)
		}
	}
	return out
}

func TestEmployeeNoticeCommittedStateLogsExcludeContentAndReplay(t *testing.T) {
	for _, suppress := range []bool{false, true} {
		t.Run(fmt.Sprint(suppress), func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
			setNoticeExecutionOutput(t, f, "PRIVATE_OUTPUT_SENTINEL https://private.example.invalid/secret-link")
			if suppress {
				noticeReceipt(t, f, &fileNoticeProvider{file: true}, "delivered", f.command.Event.Data.Conversation.OpenConversationID)
			}
			logs := captureNoticeLogs(t)
			for range 3 {
				if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
					t.Fatal(err)
				}
			}
			rows := noticeLogEvents(t, logs, "employee_run_notice_recorded")
			if len(rows) != 1 {
				t.Fatal("new notice log missing or replay inflated", len(rows))
			}
			row := rows[0]
			wantState, wantReason := "enqueued", ""
			if suppress {
				wantState, wantReason = "suppressed", "native_file_delivered"
			}
			if row["state"] != wantState || row["reason"] != wantReason || row["run_id"] != f.runID || row["queue_task_id"] != f.queueID || row["job_id"] != f.jobID || row["agent_id"] != f.agentID || row["workspace_id"] != testWorkspaceID || row["task_id"] == "" || row["scene_id"] == "" {
				t.Fatal("committed notice correlation missing", row)
			}
			for _, key := range []string{"workspace_id", "agent_id", "scene_id", "job_id", "task_id", "run_id", "queue_task_id"} {
				if value, ok := row[key].(string); !ok || value == "" {
					t.Fatal("correlation ID missing", key)
				}
			}
			for _, key := range []string{"body", "quote", "instruction_quote", "text", "result", "source_ref", "requester_ref"} {
				if _, present := row[key]; present {
					t.Fatal("private field logged", key)
				}
			}
			for _, private := range []string{"PRIVATE_OUTPUT_SENTINEL", "private.example.invalid", "文件发出后不用再发总结"} {
				if strings.Contains(logs.snapshot(), private) {
					t.Fatal("notice event leaked content")
				}
			}
		})
	}
}
func TestEmployeeNoticeRolledBackInsertDoesNotLogCommit(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	ctx := context.Background()
	name := "notice_log_reject_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`CREATE FUNCTION %s() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN IF NEW.agent_id='%s'::uuid THEN RAISE EXCEPTION 'injected notice failure'; END IF; RETURN NEW; END $$`, name, f.agentID)); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, fmt.Sprintf(`CREATE TRIGGER %s BEFORE INSERT ON employee_run_notice FOR EACH ROW EXECUTE FUNCTION %s()`, name, name)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP TRIGGER IF EXISTS %s ON employee_run_notice`, name))
		_, _ = testPool.Exec(ctx, fmt.Sprintf(`DROP FUNCTION IF EXISTS %s()`, name))
	})
	logs := captureNoticeLogs(t)
	if _, err := f.h.ReconcileEmployeeRunNotices(ctx, 100); err == nil {
		t.Fatal("fixture did not reject notice")
	}
	if len(noticeLogEvents(t, logs, "employee_run_notice_recorded")) != 0 {
		t.Fatal("rolled back notice emitted committed event")
	}
}
func TestEmployeeNoticeLateSuppressionLogsOneTransition(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
	if _, err := f.h.ReconcileEmployeeRunNotices(context.Background(), 100); err != nil {
		t.Fatal(err)
	}
	var in dingtalkresponse.ActionInput
	if err := testPool.QueryRow(context.Background(), `SELECT a.input FROM response_action a JOIN employee_run_notice n ON n.action_id=a.id WHERE n.run_id=$1::uuid`, f.runID).Scan(&in); err != nil {
		t.Fatal(err)
	}
	noticeReceipt(t, f, &fileNoticeProvider{file: true}, "delivered", f.command.Event.Data.Conversation.OpenConversationID)
	logs := captureNoticeLogs(t)
	for range 3 {
		_ = f.h.BeforeEmployeeRunNoticeSend(context.Background(), in)
	}
	rows := noticeLogEvents(t, logs, "employee_run_notice_state_changed")
	if len(rows) != 1 || rows[0]["state"] != "suppressed" || rows[0]["reason"] != "native_file_delivered" {
		t.Fatal("late suppression transition missing or repeated", rows)
	}
}

func TestEmployeeNoticeDeliveryOutputIsRedactedAndAttributed(t *testing.T) {
	secret := "sk-" + strings.Repeat("a", 24)
	body := employeeNoticeDeliveryBody("unconfirmed", "cannot read file\ncredential "+secret)
	if strings.Contains(body, secret) || !strings.Contains(body, "\n> cannot read file\n> credential ") {
		t.Fatal("attribution/redaction changed")
	}
	if employeeNoticeDeliveryBody("notify", "ordinary result") != "" {
		t.Fatal("ordinary output formatter changed")
	}
	if employeeNoticeDeliveryBody("unconfirmed", "") != "本次执行已结束，但文件是否送达尚未确认。" {
		t.Fatal("empty output needs only the verified status boundary")
	}
}
