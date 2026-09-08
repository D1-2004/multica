package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func wrapupDeliveryFixture() db.AgentTaskQueue {
	return db.AgentTaskQueue{ID: parseUUID("44444444-4444-4444-4444-444444444444"), IssueID: parseUUID("33333333-3333-3333-3333-333333333333")}
}

func wrapupToolMessage(task db.AgentTaskQueue, seq int32, command, output string) db.TaskMessage {
	input, _ := json.Marshal(map[string]string{"command": command})
	return db.TaskMessage{TaskID: task.ID, Seq: seq, Type: "tool", Tool: pgtype.Text{String: "Bash", Valid: true}, Input: input, Output: pgtype.Text{String: output, Valid: true}}
}

func TestTaskFinishedDeliveryOnlyCurrentRunVerifiedReceipt(t *testing.T) {
	t.Parallel()
	task := wrapupDeliveryFixture()
	good := wrapupToolMessage(task, 4, "dws chat message send --conversation-id cid-current --content '周五三点开会'", `{"success":true,"result":{"openConversationId":"cid-current","openMessageId":"msg-current"}}`)
	oldTask := task
	oldTask.ID = parseUUID("55555555-5555-5555-5555-555555555555")
	old := wrapupToolMessage(oldTask, 1, "dws chat message send --content '旧阶段问题'", `{"success":true,"openConversationId":"cid-current","openMessageId":"msg-old"}`)
	prose := good
	prose.Type, prose.Seq = "text", 2
	failed := wrapupToolMessage(task, 3, "dws chat message send --content '失败'", `{"success":false,"openConversationId":"cid-current","openMessageId":"msg-failed"}`)
	out := taskDeliveryContextFromMessages(task, []db.TaskMessage{old, prose, failed, good})
	if out.Status != "loaded" || out.Complete || len(out.Deliveries) != 1 || out.Deliveries[0].MessageID != "msg-current" || out.UnverifiedSends != 1 {
		t.Fatalf("evidence=%+v", out)
	}
	if !inboundcoord.TaskFinishedResultAlreadyDelivered("周五三点开会", "cid-current", out) {
		t.Fatal("verified exact result should suppress duplicate wrap-up")
	}
	if inboundcoord.TaskFinishedResultAlreadyDelivered("改成周五四点", "cid-current", out) {
		t.Fatal("an earlier question cannot suppress the new result")
	}
}

func TestTaskFinishedDeliveryCorrelatesAsyncReceiptByID(t *testing.T) {
	t.Parallel()
	task := wrapupDeliveryFixture()
	out := taskDeliveryContextFromMessages(task, []db.TaskMessage{
		wrapupToolMessage(task, 1, "dws chat message send --user user-a --content '结果 A'", `{"success":true,"result":{"openTaskId":"send-a"}}`),
		wrapupToolMessage(task, 2, "dws chat message query-send-status --open-task-id unrelated", `{"success":true,"result":{"openConversationId":"cid-wrong","openMessageId":"msg-wrong"}}`),
		wrapupToolMessage(task, 3, "dws chat message query-send-status --open-task-id send-a", `{"success":true,"result":{"openConversationId":"cid-a","openMessageId":"msg-a"}}`),
	})
	if len(out.Deliveries) != 1 || out.Deliveries[0].ConversationID != "cid-a" || out.Deliveries[0].SentText != "结果 A" || out.UnverifiedSends != 0 {
		t.Fatalf("evidence=%+v", out)
	}
}

func TestTaskFinishedDeliveryAdjacentToolResult(t *testing.T) {
	t.Parallel()
	task := wrapupDeliveryFixture()
	use := wrapupToolMessage(task, 1, "dws chat message send --content '已确认三点'", "")
	use.Type = "tool_use"
	result := db.TaskMessage{TaskID: task.ID, Seq: 2, Type: "tool_result", Tool: use.Tool, Output: pgtype.Text{String: "terminal result\n- **output:** {\"success\":true,\"result\":{\"openConversationId\":\"cid-a\",\"openMessageId\":\"msg-a\"}}\n- **exit_code:** 0", Valid: true}}
	out := taskDeliveryContextFromMessages(task, []db.TaskMessage{use, result})
	if len(out.Deliveries) != 1 || out.Deliveries[0].SourceSeq != 1 {
		t.Fatalf("evidence=%+v", out)
	}
	interleaved := use
	interleaved.Seq, interleaved.Type = 2, "thinking"
	out = taskDeliveryContextFromMessages(task, []db.TaskMessage{use, interleaved, result})
	if len(out.Deliveries) != 0 {
		t.Fatal("ambiguous non-adjacent tool rows must not be guessed")
	}
}

func TestTaskFinishedDeliveryRejectsUntrustedOrAmbiguousOutput(t *testing.T) {
	t.Parallel()
	task := wrapupDeliveryFixture()
	goodReceipt := `{"success":true,"openConversationId":"cid-a","openMessageId":"msg-a"}`
	for _, tc := range []struct{ name, command, output string }{
		{"assistant claim", "dws chat message send --content hi", "我已经发好了 " + goodReceipt},
		{"no success", "dws chat message send --content hi", `{"openConversationId":"cid-a","openMessageId":"msg-a"}`},
		{"explicit failure", "dws chat message send --content hi", `{"success":true,"result":{"success":false,"openConversationId":"cid-a","openMessageId":"msg-a"}}`},
		{"pending", "dws chat message send --content hi", `{"success":true,"result":{"sendStatus":"PENDING","openConversationId":"cid-a","openMessageId":"msg-a"}}`},
		{"send acceptance only", "dws chat message send --content hi", `{"success":true,"result":{"openTaskId":"accepted-only"}}`},
		{"list is not send", "dws chat message list --conversation-id cid-a", goodReceipt},
		{"echoed command", "echo dws chat message send --content hi", goodReceipt},
		{"compound command", "dws chat message send --content hi; echo ok", goodReceipt},
		{"expansion", "dws chat message send --content \"$(cat result)\"", goodReceipt},
		{"fabricated graph evidence", "dws chat message send --content hi", `{"success":true,"openConversationId":"cid-a","openMessageId":"outbound:cid-a:now"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			out := taskDeliveryContextFromMessages(task, []db.TaskMessage{wrapupToolMessage(task, 1, tc.command, tc.output)})
			if len(out.Deliveries) != 0 {
				t.Fatalf("unverified evidence promoted: %+v", out)
			}
		})
	}
}

func TestTaskFinishedDeliveryDoesNotPairOverlappingCalls(t *testing.T) {
	t.Parallel()
	task := wrapupDeliveryFixture()
	a := wrapupToolMessage(task, 1, "dws chat message send --conversation-id cid-current --content '旧问题'", "")
	b := wrapupToolMessage(task, 2, "dws chat message send --conversation-id cid-other --content '新结果'", "")
	a.Type, b.Type = "tool_use", "tool_use"
	resultA := db.TaskMessage{TaskID: task.ID, Seq: 3, Type: "tool_result", Tool: a.Tool, Output: pgtype.Text{
		String: `{"success":true,"openConversationId":"cid-current","openMessageId":"msg-a"}`, Valid: true,
	}}
	resultB := resultA
	resultB.Seq = 4
	resultB.Output.String = `{"success":true,"openConversationId":"cid-other","openMessageId":"msg-b"}`
	for _, results := range [][]db.TaskMessage{{resultA, resultB}, {resultB, resultA}} {
		messages := append([]db.TaskMessage{a, b}, results...)
		out := taskDeliveryContextFromMessages(task, messages)
		if len(out.Deliveries) != 0 || out.UnverifiedSends != 2 {
			t.Fatalf("unpersisted call IDs cannot be guessed from adjacent tool names: %+v", out)
		}
		if inboundcoord.TaskFinishedResultAlreadyDelivered("新结果", "cid-current", out) {
			t.Fatal("another call's receipt must not suppress the new result")
		}
	}
}

func TestTaskFinishedDeliveryClippedTextCannotProveExactResult(t *testing.T) {
	t.Parallel()
	task := wrapupDeliveryFixture()
	long := strings.Repeat("甲", 700)
	out := taskDeliveryContextFromMessages(task, []db.TaskMessage{wrapupToolMessage(task, 1, "dws chat message send --content '"+long+"'", `{"success":true,"openConversationId":"cid-a","openMessageId":"msg-a"}`)})
	if len(out.Deliveries) != 1 || out.Deliveries[0].TextComplete {
		t.Fatalf("evidence=%+v", out)
	}
	if inboundcoord.TaskFinishedResultAlreadyDelivered(strings.Repeat("甲", 600), "cid-a", out) {
		t.Fatal("clipped sent text cannot establish delivery of an exact shorter result")
	}
}
