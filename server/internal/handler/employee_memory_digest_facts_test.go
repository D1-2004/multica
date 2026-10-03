package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/internal/service/employeememory/digest"
	"github.com/multica-ai/multica/server/internal/util"
	openai "github.com/openai/openai-go/v3"
)

type digestSliceTranscript []digest.TranscriptMessage

func (s digestSliceTranscript) After(_ context.Context, _ digest.Queryer, _ digest.SceneKey, at time.Time, id string, limit int) ([]digest.TranscriptMessage, error) {
	var out []digest.TranscriptMessage
	for _, m := range s {
		if at.IsZero() || m.SentAt.After(at) || m.SentAt.Equal(at) && m.ProviderMessageID > id {
			out = append(out, m)
		}
		if len(out) == limit {
			break
		}
	}
	return out, nil
}
func (digestSliceTranscript) Before(context.Context, digest.Queryer, digest.SceneKey, time.Time, string, int) ([]digest.TranscriptMessage, error) {
	return nil, nil
}

type digestScriptModel struct {
	calls  atomic.Int32
	script []string
}

func (m *digestScriptModel) Chat(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, string, error) {
	n := int(m.calls.Add(1)) - 1
	args, _ := json.Marshal(m.script[min(n, len(m.script)-1)])
	raw := fmt.Sprintf(`{"id":"c","object":"chat.completion","created":1,"model":"fake","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":null,"tool_calls":[{"id":"call_%d","type":"function","function":{"name":"propose_scene_digest","arguments":%s}}]}}]}`, n, args)
	var out openai.ChatCompletion
	return &out, "test/fake", json.Unmarshal([]byte(raw), &out)
}

// The writer lands decisions in the real scene-shared layer as flush output:
// synthesis, confidence 3, untrusted, attributed to the quoted human; it can
// retract only its own records and a grounded message is never reused.
func TestEmployeeDigestWritesThroughSceneFactStore(t *testing.T) {
	f, _, _ := employeeFixture(t)
	cleanupEmployeeDigest(t, f.agentID)
	t.Cleanup(func() {
		for _, table := range []string{"employee_learning", "employee_memory_state"} {
			_, _ = testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE agent_id=$1`, f.agentID)
		}
	})
	ctx := context.Background()
	row, err := scene.Resolve(ctx, f.h.Queries, scene.Owner{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(f.agentID)}, scene.DingTalkConversation("digest-org", scene.KindGroup, "cid-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	key := digest.SceneKey{WorkspaceID: testWorkspaceID, AgentID: f.agentID, TenantOrgID: "digest-org", SceneID: util.UUIDToString(row.ID)}
	base := time.Now().Add(-2 * time.Hour)
	lines := digestSliceTranscript{
		{ProviderMessageID: "msg-1", SentAt: base, SenderClass: "human", SenderRef: "dingtalk:digest-org:uid:director", SenderName: "Director", Body: "本场候选编号有 K6、X3、Z2。"},
		{ProviderMessageID: "msg-2", SentAt: base.Add(time.Minute), SenderClass: "human", SenderRef: "dingtalk:digest-org:uid:director", SenderName: "Director", Body: "定了：周四发版，Z2 先上。"},
	}
	store := employeememory.NewStore(testPool)
	model := &digestScriptModel{script: []string{`{"ops":[{"op":"upsert","kind":"decision","subject":"周四发版","quote":"定了：周四发版","evidence":"g2"}]}`}}
	writer := &digest.Writer{DB: testPool, Transcript: lines, Facts: NewEmployeeSceneFacts(store), Model: model, Fence: employeeDigestFence, Ready: func(context.Context) error { return nil }}
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = digest.MarkSceneDirtyTx(ctx, tx, key, 2, base.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if worked, err := writer.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("digest: %v %v", worked, err)
	}
	var rec employeememory.LearningRecord
	var raw []byte
	if err = testPool.QueryRow(ctx, `SELECT record FROM employee_learning WHERE agent_id=$1 AND scope_kind='scene' AND forgotten_at IS NULL`, f.agentID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Type != employeememory.LearningTypeDecision || rec.Source != employeememory.LearningSourceSynthesis || rec.Confidence != 3 || rec.Trusted ||
		rec.CaptureOrigin != employeememory.CaptureOriginFlush || rec.CreatedBy != digest.FlushActor(f.agentID) || rec.SpeakerRef != "dingtalk:digest-org:uid:director" || rec.Insight != "定了：周四发版" || rec.EvidenceID != "msg-2" {
		t.Fatalf("flush record = %+v", rec)
	}
	// A member's own capture of another subject cannot be retracted by the
	// writer; its own decision can.
	member, err := func() (employeememory.SceneEntry, error) {
		tx, err := testPool.Begin(ctx)
		if err != nil {
			return employeememory.SceneEntry{}, err
		}
		defer tx.Rollback(ctx)
		entry, err := store.UpsertSceneFactTx(ctx, tx, employeeDigestScope(key), employeememory.SceneFactInput{Type: employeememory.LearningTypeFact, Subject: "候选编号", Quote: "本场候选编号有 K6、X3、Z2", Origin: employeememory.CaptureOriginTranscript, ActorID: "dingtalk:digest-org:uid:lisi",
			Grounding: employeememory.SceneFactGrounding{MessageID: lines[0].ProviderMessageID, Text: lines[0].Body, SpeakerRef: lines[0].SenderRef, SpeakerName: "Director", SpeakerClass: "human", SaidAt: lines[0].SentAt}})
		if err == nil {
			err = tx.Commit(ctx)
		}
		return entry, err
	}()
	if err != nil {
		t.Fatal(err)
	}
	facts := NewEmployeeSceneFacts(store)
	tx, err = testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	var refused *digest.OpRejectedError
	if err = facts.Retract(ctx, tx, key, member.Record.ID, digest.FlushActor(f.agentID)); !errors.As(err, &refused) || refused.Reason != "retract_denied" {
		t.Fatalf("retracting a member record: %v", err)
	}
	if err = facts.Retract(ctx, tx, key, rec.ID, digest.FlushActor(f.agentID)); err != nil {
		t.Fatalf("own retract: %v", err)
	}
	// The tombstone keeps the message consumed: a later flush of the same
	// message replays instead of reviving the retracted decision.
	write, err := facts.Upsert(ctx, tx, key, digest.FlushActor(f.agentID), digest.FactUpsert{Kind: "decision", Subject: "周四发版", Quote: "定了：周四发版", Evidence: lines[1]})
	if err != nil || !write.Replayed || write.Changed {
		t.Fatalf("re-flush of a retracted message: %+v %v", write, err)
	}
}
