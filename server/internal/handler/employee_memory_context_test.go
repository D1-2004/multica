package handler

import (
	"context"
	"encoding/json"
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	openai "github.com/openai/openai-go/v3"
)

func TestEmployeeMemoryGroupSnapshotOmitsPrivateButLookupStillWorks(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	f.command.Event.Data.Conversation.Type = "group"
	host, _, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "group-query", Text: "What is the project code?", SenderOpenDingTalkID: "requester-open-id"}})
	ctx := context.Background()
	privateScope := employeeMemoryScope(host, source)
	for key, value := range map[string]string{"project-code": "PRIVATE_MATCH", "other-record": "PRIVATE_UNRELATED"} {
		if _, err := f.h.EmployeeMemory.Record(ctx, privateScope, employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: key, Insight: value, Source: employeememory.LearningSourceObserved, Confidence: 4}, employeememory.TrustedEvidence{SourceID: key, EvidenceID: key, ActorID: source.RequesterRef}); err != nil {
			t.Fatal(err)
		}
	}
	sharedScope := privateScope
	sharedScope.Kind = employeememory.ScopeScene
	sharedScope.PrincipalID = ""
	if _, err := f.h.EmployeeMemory.Record(ctx, sharedScope, employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: "shared", Insight: "SCENE_SHARED", Confidence: 4, Source: employeememory.LearningSourceObserved}, employeememory.TrustedEvidence{SourceID: "shared", EvidenceID: "shared", ActorID: source.RequesterRef}); err != nil {
		t.Fatal(err)
	}
	// Wire labels cannot turn an admitted group into a private conversation.
	host.envelopes[0].Command.Event.Data.Conversation.Type = "single"
	input, err := host.worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(input.Input.Memory, "PRIVATE_") || !strings.Contains(input.Input.Memory, "SCENE_SHARED") {
		t.Fatalf("group snapshot leaked private memory or lost shared memory: %s", input.Input.Memory)
	}
	calls := 0
	model := employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		raw, _ := json.Marshal(p.Messages)
		if calls == 1 {
			if strings.Contains(string(raw), "PRIVATE_") {
				t.Fatal("private brief reached first group model request")
			}
			return employeeReplyCompletion(t, employeeReplyCall(t, "group-lookup", "memory_lookup", map[string]any{"source_ref": source.SourceRef, "query": "project-code"})), nil
		}
		if !strings.Contains(string(raw), "PRIVATE_MATCH") || strings.Contains(string(raw), "PRIVATE_UNRELATED") {
			t.Fatalf("explicit lookup did not return only matching private data: %s", raw)
		}
		return employeeMemoryAnswer(t), nil
	})
	if _, err = employeeloop.New(input.Config, model, host).Run(ctx, input.Input); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("group lookup calls=%d", calls)
	}
}

func TestEmployeeMemoryDMStillAnswersFromFirstRequest(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	f.command.Event.Data.Conversation.Type = "single"
	host, _, source := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "dm-query", Text: "What is the project code?", SenderOpenDingTalkID: "requester-open-id"}})
	ctx := context.Background()
	if _, err := f.h.EmployeeMemory.Record(ctx, employeeMemoryScope(host, source), employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: "project-code", Insight: "DM_CURRENT_VALUE", Confidence: 4, Source: employeememory.LearningSourceObserved}, employeememory.TrustedEvidence{SourceID: "dm-seed", EvidenceID: "dm-seed", ActorID: source.RequesterRef}); err != nil {
		t.Fatal(err)
	}
	input, err := host.worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	model := employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		raw, _ := json.Marshal(p.Messages)
		if !strings.Contains(string(raw), "DM_CURRENT_VALUE") {
			t.Fatal("DM first request lost private brief")
		}
		return employeeReplyCompletion(t, employeeReplyCall(t, "dm-answer", "reply", map[string]any{"source_ref": source.SourceRef, "reply": "DM_CURRENT_VALUE"})), nil
	})
	out, err := employeeloop.New(input.Config, model, host).Run(ctx, input.Input)
	if err != nil || calls != 1 || out.Reply != "DM_CURRENT_VALUE" {
		t.Fatalf("DM direct answer changed: calls=%d out=%+v err=%v", calls, out.Decision, err)
	}
}

func TestEmployeeMemoryAutomaticPrivateRejectsUnknownKind(t *testing.T) {
	messages := []employeeSourceMessage{{RequesterRef: "requester"}}
	for _, kind := range []string{"", "single", "unknown", scene.KindEnterprise, scene.KindGroup} {
		if _, ok := employeeAutomaticPrivateRequester(db.AgentScene{SceneKind: kind}, messages); ok {
			t.Fatalf("kind %q guessed as a direct conversation", kind)
		}
	}
	if requester, ok := employeeAutomaticPrivateRequester(db.AgentScene{SceneKind: scene.KindDM}, messages); !ok || requester != "requester" {
		t.Fatal("registered DM rejected")
	}
	for _, window := range [][]employeeSourceMessage{{{RequesterRef: ""}}, {{RequesterRef: "one"}, {RequesterRef: "two"}}} {
		if _, ok := employeeAutomaticPrivateRequester(db.AgentScene{SceneKind: scene.KindDM}, window); ok {
			t.Fatal("DM auto-private admitted ambiguous requester")
		}
	}
}

func TestEmployeeMemoryGroupFrozenSnapshotKeepsJournalIdentity(t *testing.T) {
	f, dc := employeeMemoryFixture(t)
	f.command.Event.Data.Conversation.Type = "group"
	host, _, _ := employeeMemoryHost(t, f, dc, []DispatchMessage{{OpenMsgID: "old-group-snapshot", Text: "continue", SenderOpenDingTalkID: "requester-open-id"}})
	ctx := context.Background()
	worker := host.worker
	input, err := worker.buildInput(ctx, host.job, host.envelopes, host.envelopes)
	if err != nil {
		t.Fatal(err)
	}
	// Earlier deployments froze this block even for a group. Existing snapshots
	// must replay as recorded; the new audience rule applies only at buildInput.
	input.Input.Memory = "Requester-private background context for this source only.\nHISTORICAL_PRIVATE_SNAPSHOT"
	raw, _ := json.Marshal(input)
	if _, err = worker.store.SaveInput(ctx, host.job, raw); err != nil {
		t.Fatal(err)
	}
	calls := 0
	model := employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		raw, _ := json.Marshal(p.Messages)
		if !strings.Contains(string(raw), "HISTORICAL_PRIVATE_SNAPSHOT") {
			t.Fatal("historical snapshot changed")
		}
		return employeeMemoryAnswer(t), nil
	})
	if _, err = employeeloop.New(input.Config, &employeeJournalModel{store: worker.store, job: host.job, delegate: model}, host).Run(ctx, input.Input); err != nil {
		t.Fatal(err)
	}
	if err = worker.store.Retry(ctx, host.job, "restart"); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `UPDATE employee_scene_job SET available_at=now() WHERE id=$1`, host.job.ID); err != nil {
		t.Fatal(err)
	}
	worker.model = model
	if _, err = worker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var failure string
	if err = testPool.QueryRow(ctx, `SELECT COALESCE(outcome->>'failure','') FROM employee_scene_job WHERE id=$1`, host.job.ID).Scan(&failure); err != nil || failure != "" || calls != 1 {
		t.Fatalf("old group checkpoint conflicted: calls=%d failure=%s err=%v", calls, failure, err)
	}
}
