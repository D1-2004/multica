package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/service"
)

func TestEmployeeInheritedNoticeUsesCurrentRunDelivery(t *testing.T) {
	for _, tool := range []string{"steer_task", "continue_task"} {
		for _, outcome := range []string{"verified_file", "no_receipt", "prior_receipt_only", "failed"} {
			t.Run(tool+"/"+outcome, func(t *testing.T) {
				f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
				ctx := context.Background()
				var originSource string
				if err := testPool.QueryRow(ctx, `SELECT context->>'employee_source_ref' FROM agent_task_queue WHERE id=$1::uuid`, f.queueID).Scan(&originSource); err != nil {
					t.Fatal(err)
				}
				provider := &fileNoticeProvider{file: true}
				if outcome == "prior_receipt_only" {
					noticeReceipt(t, f, provider, "delivered", f.command.Event.Data.Conversation.OpenConversationID)
				}
				run := employeeCombinedWake(t, f, "继续完成这个文件，只保留签约客户", tool)
				var policy employeetask.CompletionNoticePolicy
				var raw []byte
				if err := testPool.QueryRow(ctx, `SELECT COALESCE(context->'employee_completion_notice_policy','{}'::jsonb) FROM agent_task_queue WHERE id=$1::uuid`, run.Queue).Scan(&raw); err != nil || json.Unmarshal(raw, &policy) != nil {
					t.Fatal(err, string(raw))
				}
				if policy.Mode != employeetask.CompletionNoticeIfNotDelivered || policy.SourceRef != originSource {
					t.Error("successor lost original file-only authorization", policy)
				}
				current := f
				current.runID, current.queueID, current.jobID = run.Run, run.Queue, run.Job
				if outcome == "verified_file" || outcome == "failed" {
					noticeReceipt(t, current, provider, "delivered", f.command.Event.Data.Conversation.OpenConversationID)
				}
				if outcome == "failed" {
					if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1::uuid`, run.Queue); err != nil {
						t.Fatal(err)
					}
					if _, err := f.h.TaskService.FailTask(ctx, parseUUID(run.Queue), "inherited-policy failure", "", "", "agent_error", false, ""); err != nil {
						t.Fatal(err)
					}
				} else {
					employeeCombinedComplete(t, f, run)
				}
				if _, err := f.h.enqueueEmployeeRunNotice(ctx, testWorkspaceID, run.Run); err != nil {
					t.Fatal(err)
				}
				var state, reason, body, source string
				if err := testPool.QueryRow(ctx, `SELECT state,reason,body,source_ref FROM employee_run_notice WHERE run_id=$1::uuid`, run.Run).Scan(&state, &reason, &body, &source); err != nil {
					t.Fatal(err)
				}
				if outcome == "verified_file" {
					if state != "suppressed" || reason != "native_file_delivered" || body != "" || provider.verifies == 0 {
						t.Fatal("verified current file should suppress inherited notice", state, reason, body, provider.verifies)
					}
				} else if state != "enqueued" || body == "" || source != run.DeliverySource || outcome == "failed" && !strings.Contains(body, "inherited-policy failure") {
					t.Fatal("inherited policy hid missing delivery or failure", state, reason, body, source)
				}
			})
		}
	}
}

func TestEmployeeInheritedNoticeRejectsErasedCommitment(t *testing.T) {
	for _, tool := range []string{"steer_task", "continue_task"} {
		t.Run(tool, func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
			run := employeeCombinedWake(t, f, "继续完成这个文件", tool)
			employeeCombinedComplete(t, f, run)
			ctx := context.Background()
			if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET context=jsonb_set(context - 'employee_completion_notice_policy' - 'employee_completion_notice_origin','{employee_direct_input}',(context->'employee_direct_input') - 'employee_completion_notice_policy' - 'employee_completion_notice_origin') WHERE id=$1::uuid`, run.Queue); err != nil {
				t.Fatal(err)
			}
			if _, err := f.h.enqueueEmployeeRunNotice(ctx, testWorkspaceID, run.Run); err != nil {
				t.Fatal(err)
			}
			var state, reason string
			if err := testPool.QueryRow(ctx, `SELECT state,reason FROM employee_run_notice WHERE run_id=$1::uuid`, run.Run).Scan(&state, &reason); err != nil || state != "suppressed" || reason == "native_file_delivered" {
				t.Fatal("erasing both queue policy copies revoked accepted quiet instruction", state, reason, err)
			}
		})
	}
}

func TestEmployeeInheritedNoticeAlwaysRequiresAcceptedDeliveryAnchor(t *testing.T) {
	f := employeeNoticeDatabase(t, "succeeded", false, false)
	run := employeeCombinedWake(t, f, "只保留签约客户", "steer_task")
	employeeCombinedComplete(t, f, run)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET context=context || jsonb_build_object('employee_job_id',$2::text,'employee_source_ref',$3::text,'employee_direct_input',(context->'employee_direct_input') || jsonb_build_object('employee_job_id',$2::text,'employee_source_ref',$3::text)) WHERE id=$1::uuid`, run.Queue, run.Job, run.Source); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.enqueueEmployeeRunNotice(ctx, testWorkspaceID, run.Run); err != nil {
		t.Fatal(err)
	}
	var state string
	if err := testPool.QueryRow(ctx, `SELECT state FROM employee_run_notice WHERE run_id=$1::uuid`, run.Run).Scan(&state); err != nil || state != "suppressed" {
		t.Fatal("correction source impersonated the accepted delivery anchor", state, err)
	}
}

func TestEmployeeInheritedNoticeMalformedQuietCannotHideFailure(t *testing.T) {
	for name, mutation := range map[string]string{
		"wrong_source_ref": `UPDATE agent_task_queue SET context=jsonb_set(context,'{employee_completion_notice_policy,source_ref}','"another/source"') WHERE id=$1::uuid`,
		"malformed_policy": `UPDATE agent_task_queue SET context=jsonb_set(context,'{employee_completion_notice_policy}','["invalid"]') WHERE id=$1::uuid`,
		"frozen_policy":    `UPDATE agent_task_queue SET context=jsonb_set(context,'{employee_direct_input,employee_completion_notice_policy}','["invalid"]') WHERE id=$1::uuid`,
		"frozen_origin":    `UPDATE agent_task_queue SET context=jsonb_set(context,'{employee_direct_input,employee_completion_notice_origin}','"invalid"') WHERE id=$1::uuid`,
		"erased_policy":    `UPDATE agent_task_queue SET context=jsonb_set(context - 'employee_completion_notice_policy' - 'employee_completion_notice_origin','{employee_direct_input}',(context->'employee_direct_input') - 'employee_completion_notice_policy' - 'employee_completion_notice_origin') WHERE id=$1::uuid`,
	} {
		t.Run(name, func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
			run := employeeCombinedWake(t, f, "继续完成文件", "continue_task")
			ctx := context.Background()
			if _, err := testPool.Exec(ctx, mutation, run.Queue); err != nil {
				t.Fatal(err)
			}
			if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='running',started_at=now() WHERE id=$1::uuid`, run.Queue); err != nil {
				t.Fatal(err)
			}
			if _, err := f.h.TaskService.FailTask(ctx, parseUUID(run.Queue), "failure must remain visible", "", "", "agent_error", false, ""); err != nil {
				t.Fatal(err)
			}
			if _, err := f.h.enqueueEmployeeRunNotice(ctx, testWorkspaceID, run.Run); err != nil {
				t.Fatal(err)
			}
			var state, body string
			if err := testPool.QueryRow(ctx, `SELECT state,body FROM employee_run_notice WHERE run_id=$1::uuid`, run.Run).Scan(&state, &body); err != nil || state != "enqueued" || !strings.Contains(body, "failure must remain visible") {
				t.Fatal("quiet metadata hid an authorized failure notice", state, body, err)
			}
		})
	}
}

func TestEmployeeInheritedNoticeRejectsChangedAuthority(t *testing.T) {
	for name, mutation := range map[string]string{
		"frozen_policy":         `UPDATE agent_task_queue SET context=jsonb_set(context,'{employee_direct_input,employee_completion_notice_policy}','["invalid"]') WHERE id=$1::uuid`,
		"frozen_origin":         `UPDATE agent_task_queue SET context=jsonb_set(context,'{employee_direct_input,employee_completion_notice_origin}','"invalid"') WHERE id=$1::uuid`,
		"policy_quote":          `UPDATE agent_task_queue SET context=jsonb_set(jsonb_set(context,'{employee_completion_notice_policy,instruction_quote}','"fabricated exemption"'),'{employee_direct_input,employee_completion_notice_policy,instruction_quote}','"fabricated exemption"') WHERE id=$1::uuid`,
		"foreign_task":          `UPDATE agent_task_queue SET context=jsonb_set(jsonb_set(context,'{employee_completion_notice_origin,task_id}',to_jsonb($4::text)),'{employee_direct_input,employee_completion_notice_origin,task_id}',to_jsonb($4::text)) WHERE id=$1::uuid`,
		"foreign_queue":         `UPDATE agent_task_queue SET context=jsonb_set(jsonb_set(context,'{employee_completion_notice_origin,queue_task_id}',to_jsonb($4::text)),'{employee_direct_input,employee_completion_notice_origin,queue_task_id}',to_jsonb($4::text)) WHERE id=$1::uuid`,
		"foreign_scope":         `UPDATE agent_task_queue SET context=jsonb_set(context,'{employee_direct_input,agent_scene,scene_id}',to_jsonb($4::text)) WHERE id=$2::uuid`,
		"source_evidence":       `UPDATE employee_task_entry SET body='{}' WHERE task_id=$5::uuid AND kind='request'`,
		"source_requester":      `UPDATE employee_task_entry SET actor_ref='another-requester' WHERE task_id=$5::uuid AND kind='request'`,
		"source_journal":        `UPDATE employee_scene_job SET tool_journal=jsonb_set(tool_journal,'{call-first,input,arguments,completion_notice_policy,instruction_quote}','"invented quote"') WHERE id=$3::uuid`,
		"default_cannot_revoke": `UPDATE agent_task_queue SET context=context - 'employee_completion_notice_origin' - 'employee_completion_notice_policy' WHERE id=$1::uuid`,
	} {
		t.Run(name, func(t *testing.T) {
			f := employeeNoticeDatabase(t, "succeeded", false, false, fileOnlyNotice)
			run := employeeCombinedWake(t, f, "继续完成文件", "continue_task")
			employeeCombinedComplete(t, f, run)
			ctx := context.Background()
			if _, err := testPool.Exec(ctx, `WITH fixture AS (SELECT $1::uuid,$2::uuid,$3::uuid,$4::uuid,$5::uuid) `+mutation, run.Queue, f.queueID, f.jobID, f.taskID, run.Task); err != nil {
				t.Fatal(err)
			}
			if _, err := f.h.enqueueEmployeeRunNotice(ctx, testWorkspaceID, run.Run); err != nil {
				t.Fatal(err)
			}
			var state string
			var action bool
			if err := testPool.QueryRow(ctx, `SELECT state,action_id IS NOT NULL FROM employee_run_notice WHERE run_id=$1::uuid`, run.Run).Scan(&state, &action); err != nil || state != "suppressed" || action {
				t.Fatal("unproven inherited policy authorized delivery", state, action, err)
			}
		})
	}
}

func TestEmployeeInheritedNoticeKeepsFlatOriginAcrossSteerAndContinue(t *testing.T) {
	for _, flow := range []string{"steer_continue", "continue_steer_merged", "queued_dispatch_steer_merged"} {
		t.Run(flow, func(t *testing.T) {
			initialState := "succeeded"
			if flow == "queued_dispatch_steer_merged" {
				initialState = "running"
			}
			f := employeeNoticeDatabase(t, initialState, false, false, fileOnlyNotice)
			ctx := context.Background()
			var run employeeCombinedRun
			switch flow {
			case "steer_continue":
				run = employeeCombinedWake(t, f, "文件只保留签约客户", "steer_task")
				employeeCombinedComplete(t, f, run)
				run = employeeCombinedWake(t, f, "继续完成这个文件", "continue_task")
			case "continue_steer_merged":
				run = employeeCombinedWake(t, f, "继续完成文件", "continue_task")
				previous := run
				run = employeeCombinedWake(t, f, "文件只保留签约客户", "steer_task")
				if run.Queue != previous.Queue {
					t.Fatal("queued steer did not merge", previous, run)
				}
			case "queued_dispatch_steer_merged":
				if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET status='queued',started_at=NULL WHERE id=$1::uuid`, f.queueID); err != nil {
					t.Fatal(err)
				}
				run = employeeCombinedWake(t, f, "文件只保留签约客户", "steer_task")
				if run.Queue != f.queueID {
					t.Fatal("first queued dispatch did not merge", run)
				}
			}
			var raw []byte
			if err := testPool.QueryRow(ctx, `SELECT context->'employee_completion_notice_origin' FROM agent_task_queue WHERE id=$1::uuid`, run.Queue).Scan(&raw); err != nil {
				t.Fatal(err)
			}
			var origin service.DirectTaskNoticeOrigin
			var fields map[string]any
			if json.Unmarshal(raw, &origin) != nil || json.Unmarshal(raw, &fields) != nil || origin.Version != 1 || origin.QueueTaskID != f.queueID || origin.TaskID != run.Task || len(fields) != 4 {
				t.Fatal("origin was chained or included evidence bodies", string(raw))
			}
			current := f
			current.runID, current.queueID, current.jobID = run.Run, run.Queue, run.Job
			provider := &fileNoticeProvider{file: true}
			noticeReceipt(t, current, provider, "delivered", f.command.Event.Data.Conversation.OpenConversationID)
			employeeCombinedComplete(t, f, run)
			if _, err := f.h.enqueueEmployeeRunNotice(ctx, testWorkspaceID, run.Run); err != nil {
				t.Fatal(err)
			}
			var state, reason string
			if err := testPool.QueryRow(ctx, `SELECT state,reason FROM employee_run_notice WHERE run_id=$1::uuid`, run.Run).Scan(&state, &reason); err != nil || state != "suppressed" || reason != "native_file_delivered" {
				t.Fatal("flat authorization could not verify current file delivery", state, reason, err)
			}
		})
	}
}
