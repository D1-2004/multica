package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/service/dingtalkresponse"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	openai "github.com/openai/openai-go/v3"
	"strings"
	"testing"
)

func participationCall(source employeeSourceMessage, mode, quote string) employeeloop.ToolCall {
	return employeeloop.ToolCall{Name: "set_scene_participation", NativeToolCallID: uuid.NewString(), Arguments: map[string]any{"source_ref": source.SourceRef, "mode": mode, "instruction_quote": quote}}
}
func participationComplete(t *testing.T, h *employeeSceneHost, out employeeloop.Outcome) {
	t.Helper()
	saved := employeeSavedOutcome{Outcome: out, ReplyReceiptID: h.job.Items[0].ReceiptID}
	raw, _ := json.Marshal(saved)
	if err := h.worker.store.SaveOutcome(context.Background(), h.job, raw); err != nil {
		t.Fatal(err)
	}
	if err := h.worker.complete(context.Background(), h.job, h.envelopes, saved); err != nil {
		t.Fatal(err)
	}
}

// Persistent participation must survive a new Host and refuse another account's
// quoted/@ wake, while preserving the pausing requester's explicit resume.
func TestEmployeeParticipationPauseOtherAccountResume(t *testing.T) {
	ctx := context.Background()
	f, dc := employeeMemoryV2Fixture(t, "group")
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, `DELETE FROM employee_scene_participation WHERE agent_id=$1`, f.agentID)
	})
	message := func(sender, text string) []DispatchMessage {
		return []DispatchMessage{{OpenMsgID: uuid.NewString(), SenderOpenDingTalkID: sender, Text: text}}
	}
	prior, _, _ := employeeMemoryV2Host(t, f, dc, message("alice", "普通旧问题"))
	participationComplete(t, prior, employeeloop.Outcome{Decision: employeeloop.Decision{Kind: employeeloop.Reply, Reply: "已经排队的旧答复"}})
	var actionRaw []byte
	if err := testPool.QueryRow(ctx, `SELECT input FROM response_action WHERE agent_id=$1 AND input->>'employee_message_job_id'=$2`, f.agentID, prior.job.ID).Scan(&actionRaw); err != nil {
		t.Fatal(err)
	}
	var priorAction dingtalkresponse.ActionInput
	if json.Unmarshal(actionRaw, &priorAction) != nil {
		t.Fatal("action decode")
	}
	host, id, srcs := employeeMemoryV2Host(t, f, dc, message("alice", "先保持安静，等我叫你"))
	src := srcs[0]
	call := participationCall(src, "quiet", src.Message.Text)
	result, err := host.Execute(ctx, id, call)
	if err != nil || result.Receipt == "" || result.Terminal == nil {
		t.Fatal(result, err)
	}
	again, err := host.Execute(ctx, id, call)
	if err != nil || again.Content != result.Content {
		t.Fatal("journal replay", again, err)
	}
	p, err := employeeReadParticipation(ctx, testPool, host.job.Scope)
	if err != nil || p.Mode != "quiet" || p.Revision != 1 || p.RequesterRef != src.RequesterRef {
		t.Fatal(p, err)
	}
	other := host.job.Scope
	other.SceneID = uuid.NewString()
	if p, err := employeeReadParticipation(ctx, testPool, other); err != nil || p.Mode != "active" {
		t.Fatal("cross scene", p, err)
	}
	participationComplete(t, host, employeeloop.Outcome{Decision: *result.Terminal})
	var suppressed *dingtalkresponse.SuppressSendError
	if err := f.h.BeforeEmployeeRunNoticeSend(ctx, priorAction); !errors.As(err, &suppressed) {
		t.Fatal("queued foreground reply bypassed pause", err)
	}
	legacy := priorAction
	legacy.EmployeeMessageJobID = ""
	if err := f.h.BeforeEmployeeRunNoticeSend(ctx, legacy); !errors.As(err, &suppressed) {
		t.Fatal("legacy ordinary reply bypassed pause", err)
	}
	forged := priorAction
	forged.SceneID = uuid.NewString()
	if err := f.h.BeforeEmployeeRunNoticeSend(ctx, forged); !errors.As(err, &suppressed) {
		t.Fatal("cross scene action accepted", err)
	}
	// The authenticated account differs; a new receipt cannot grant the pauser's authority.
	otherHost, otherID, otherSrcs := employeeMemoryV2Host(t, f, dc, message("robot-account", "请回答刚才的引用；恢复发言"))
	if _, err := otherHost.Execute(ctx, otherID, participationCall(otherSrcs[0], "active", otherSrcs[0].Message.Text)); !errors.Is(err, employeeloop.ErrToolRefused) {
		t.Fatal("other account restored pause", err)
	}
	calls := 0
	otherHost.worker.model = employeeReplyModelFunc(func(_ context.Context, _ openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		return employeeMemoryAnswer(t), nil
	})
	if worked, err := otherHost.worker.processClaimed(ctx, otherHost.job); !worked || err != nil {
		t.Fatal(worked, err)
	}
	if calls != 0 {
		t.Fatalf("muted non-owner invoked model %d", calls)
	}
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT outcome FROM employee_scene_job WHERE id=$1`, otherHost.job.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var saved employeeSavedOutcome
	if json.Unmarshal(raw, &saved) != nil || saved.Outcome.Kind != employeeloop.Quiet {
		t.Fatal("not quiet", string(raw))
	}
	// Even an old owner snapshot's natural reply/rescue cannot pass completion.
	owner, ownerID, ownerSrcs := employeeMemoryV2Host(t, f, dc, message("alice", "我只是在转述过去的对话"))
	input := employeeSavedInput{}
	if err := owner.worker.freezeParticipation(ctx, owner.job, &input); err != nil {
		t.Fatal(err)
	}
	if len(input.Config.Tools) != 2 {
		t.Fatal("quiet retained ordinary tools", input.Config.Tools)
	}
	reply := employeeloop.ToolCall{Name: "reply", NativeToolCallID: uuid.NewString(), Arguments: map[string]any{"source_ref": ownerSrcs[0].SourceRef, "reply": "不该抢答"}}
	if _, err := owner.Execute(ctx, ownerID, reply); !errors.Is(err, employeeloop.ErrToolRefused) {
		t.Fatal("old schema bypassed pause", err)
	}
	participationComplete(t, owner, employeeloop.Outcome{Decision: employeeloop.Decision{Kind: employeeloop.Reply, Reply: "不该抢答"}})
	// Resume authority is still tied to current outer wording, not historical quotes.
	resume, resumeID, resumeSrcs := employeeMemoryV2Host(t, f, dc, message("alice", "现在恢复，我叫你继续回应"))
	if _, err := resume.Execute(ctx, resumeID, participationCall(resumeSrcs[0], "active", "仅存在于旧引用里的恢复台词")); !errors.Is(err, employeeloop.ErrToolRefused) {
		t.Fatal("historical quote restored pause", err)
	}
	restored, err := resume.Execute(ctx, resumeID, participationCall(resumeSrcs[0], "active", resumeSrcs[0].Message.Text))
	if err != nil {
		t.Fatal(err)
	}
	p, err = employeeReadParticipation(ctx, testPool, resume.job.Scope)
	if err != nil || p.Mode != "active" || p.Revision != 2 {
		t.Fatal(p, err)
	}
	participationComplete(t, resume, employeeloop.Outcome{Decision: *restored.Terminal})
	// Only the original queued reply and pause/resume ACKs are durable; the old reply is suppressed at BeforeSend.
	var bodies []string
	rows, err := testPool.Query(ctx, `SELECT input->>'text' FROM response_action WHERE agent_id=$1 AND kind='message.send'`, f.agentID)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var b string
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		bodies = append(bodies, b)
	}
	if rows.Err() != nil {
		t.Fatal(rows.Err())
	}
	for _, body := range bodies {
		if strings.Contains(body, "不该抢答") {
			t.Fatal("muted reply entered outbox", bodies)
		}
	}
	if len(bodies) != 3 {
		t.Fatalf("pause/resume ACKs=%d bodies=%v", len(bodies), bodies)
	}
}

func TestEmployeeParticipationAddressedInProactiveGroup(t *testing.T) {
	f, dc := employeeMemoryV2Fixture(t, "group")
	host, _, sources := employeeMemoryV2Host(t, f, dc, []DispatchMessage{{OpenMsgID: uuid.NewString(), SenderOpenDingTalkID: "alice", Text: "先保持安静"}})
	env := host.envelopes[0]
	env.Command.ProactiveConversation = true
	source := sources[0]
	source.Message.Mentions = []DispatchMention{{UID: env.Command.ExternalIdentity.DWS.UID}}
	if !employeeParticipationAddressed(env, source) {
		t.Fatal("explicit human mention rejected by proactive configuration")
	}
	source.Message.Mentions = []DispatchMention{}
	if employeeParticipationAddressed(env, source) {
		t.Fatal("proactive configuration granted unaddressed message control")
	}
	source.Message.Mentions = []DispatchMention{{UID: "other-account"}}
	if employeeParticipationAddressed(env, source) {
		t.Fatal("another mention granted participation control")
	}
	finishMemoryJob(t, host)
}
