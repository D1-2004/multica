package handler

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/modelregistry"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/internal/service/employeememory/digest"
	"github.com/multica-ai/multica/server/internal/util"
	openai "github.com/openai/openai-go/v3"
)

type digestScriptAttempt struct {
	ref      modelregistry.Ref
	calls    atomic.Int32
	args     string
	lastUser string
}

func (a *digestScriptAttempt) Ref() modelregistry.Ref       { return a.ref }
func (a *digestScriptAttempt) ConfigurationRevision() int64 { return 1 }
func (a *digestScriptAttempt) Chat(_ context.Context, request openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
	a.calls.Add(1)
	raw, _ := json.Marshal(request)
	a.lastUser = string(raw)
	args, _ := json.Marshal(a.args)
	var out openai.ChatCompletion
	err := json.Unmarshal([]byte(fmt.Sprintf(`{"id":"c","object":"chat.completion","created":1,"model":%q,"choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_1","type":"function","function":{"name":"propose_scene_digest","arguments":%s}}]}}]}`, a.ref.Model, args)), &out)
	return &out, err
}

type digestScriptRoutes struct{ attempt *digestScriptAttempt }

func (r digestScriptRoutes) CoordinatorPlan(context.Context) (modelregistry.CoordinatorPlan, error) {
	return modelregistry.CoordinatorPlan{Version: 1, Revision: 1, Candidates: []modelregistry.Ref{r.attempt.ref}, RequestProfile: modelregistry.EmployeeFastRequestProfile}, nil
}
func (r digestScriptRoutes) PrepareCoordinatorAttempt(context.Context, modelregistry.CoordinatorPlan, int) (modelregistry.CoordinatorAttempt, error) {
	return r.attempt, nil
}

// Realistic group flow, local: 30 unaddressed group lines (one of them the
// decision) are persisted by M8's insert path, which marks the scene dirty
// in the same transaction; the threshold schedules a flush; the writer reads
// the persisted transcript, calls the shared chain once and lands the
// decision as a flush candidate attributed to the human who said it, which
// a later question in a new segment retrieves.
func TestEmployeeGroupDecisionWithoutMentionIsFlushedAndRecalled(t *testing.T) {
	f, _, _ := employeeFixture(t)
	cleanupEmployeeDigest(t, f.agentID)
	t.Cleanup(func() {
		for _, table := range []string{"employee_learning", "employee_memory_state", "employee_scene_message"} {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE agent_id=$1`, f.agentID)
		}
	})
	ctx := context.Background()
	h := f.h
	h.EmployeeSceneMessagesObserved = func(ctx context.Context, tx pgx.Tx, key employeeentry.Scope, humanRows int, lastHumanAt time.Time) error {
		return digest.MarkSceneDirtyTx(ctx, tx, digest.SceneKey(key), humanRows, lastHumanAt)
	}
	row, err := scene.Resolve(ctx, h.Queries, scene.Owner{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}, scene.DingTalkConversation("digest-org", scene.KindGroup, "cid-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	key := employeeentry.Scope{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TenantOrgID: "digest-org", SceneID: util.UUIDToString(row.ID)}
	base := time.Now().Add(-2 * time.Hour)
	var rows []employeeentry.SceneMessageRow
	for i := 0; i < 30; i++ {
		body := fmt.Sprintf("第 %d 条：讨论一下版本里的改动点", i+1)
		speaker := "lisi"
		if i == 17 {
			body, speaker = "定了：周四发版，Z2 先上。", "director"
		}
		rows = append(rows, employeeentry.SceneMessageRow{SceneMessageInput: employeeentry.SceneMessageInput{ProviderMessageID: fmt.Sprintf("msg-%02d", i), SentAt: base.Add(time.Duration(i) * time.Minute), SenderClass: "human", SenderRef: "dingtalk:digest-org:uid:" + speaker, SenderName: speaker, Body: body}})
	}
	if err = h.storeEmployeeSceneMessages(ctx, testPool, key, employeeentry.SceneMessageSourceWakeRead, rows); err != nil {
		t.Fatal(err)
	}
	var pending int
	var due time.Time
	if err = testPool.QueryRow(ctx, `SELECT pending_human,due_at FROM employee_scene_digest_state WHERE scene_id=$1`, key.SceneID).Scan(&pending, &due); err != nil {
		t.Fatal(err)
	}
	if pending != 30 || time.Until(due) > 61*time.Second {
		t.Fatalf("threshold schedule: pending=%d due in %v", pending, time.Until(due))
	}
	if _, err = testPool.Exec(ctx, `UPDATE employee_scene_digest_state SET due_at=now() WHERE scene_id=$1`, key.SceneID); err != nil {
		t.Fatal(err)
	}
	attempt := &digestScriptAttempt{ref: modelregistry.Ref{Provider: "dashscope", Model: "qwen3.7-plus"}, args: `{"ops":[{"op":"upsert","kind":"decision","subject":"发版时间","quote":"定了：周四发版，Z2 先上。","evidence":"g18"}]}`}
	store := employeememory.NewStore(testPool)
	writer := NewEmployeeMemoryDigest(h, digestScriptRoutes{attempt: attempt}, nil, EmployeeSceneTranscript{}, NewEmployeeSceneFacts(store), func(context.Context) error { return nil })
	if worked, err := writer.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("digest: %v %v", worked, err)
	}
	if attempt.calls.Load() != 1 || !strings.Contains(attempt.lastUser, "g18 ") || !strings.Contains(attempt.lastUser, "qwen3.7-plus") {
		t.Fatalf("one shared-chain call over the persisted page: calls=%d", attempt.calls.Load())
	}
	scope := employeeDigestScope(digest.SceneKey(key))
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	facts, err := store.ActiveSceneFactsTx(ctx, tx, scope, 10)
	if err != nil || len(facts) != 1 {
		t.Fatalf("facts = %+v err=%v", facts, err)
	}
	got := facts[0]
	if got.Type != employeememory.LearningTypeDecision || got.Source != employeememory.LearningSourceSynthesis || got.Trusted || got.SpeakerRef != "dingtalk:digest-org:uid:director" || got.Subject != "发版时间" || got.EvidenceID != "msg-17" {
		t.Fatalf("decision = %+v", got)
	}
	// A new segment two hours later: the question retrieves the candidate.
	hits, err := store.RetrieveTx(ctx, tx, scope, "周四发版那个安排还算数吗？", 5)
	if err != nil || len(hits) != 1 || hits[0].ID != got.ID {
		t.Fatalf("recall hits = %+v err=%v", hits, err)
	}
	var outcome string
	var calls int
	if err = testPool.QueryRow(ctx, `SELECT outcome,calls FROM employee_scene_digest_run WHERE scene_id=$1`, key.SceneID).Scan(&outcome, &calls); err != nil || outcome != digest.OutcomeCommitted || calls != 1 {
		t.Fatalf("run outcome=%s calls=%d err=%v", outcome, calls, err)
	}
}
