package employeeentry

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
)

// transcriptDatabase is the employee entry fixture plus the transcript table
// (988*) and the memory tables its read fence consults (9620).
func transcriptDatabase(t *testing.T) (fixture, SceneMessageKey) {
	t.Helper()
	f := database(t)
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(self), "..", "..", "migrations")
	apply(t, f.pool, filepath.Join(dir, "9620_employee_memory.up.sql"))
	owned, err := filepath.Glob(filepath.Join(dir, "988*.up.sql"))
	if err != nil || len(owned) == 0 {
		t.Fatalf("transcript migrations: %v %v", owned, err)
	}
	sort.Strings(owned)
	for _, path := range owned {
		apply(t, f.pool, path)
	}
	return f, f.admission.Scope
}

func transcriptRow(id string, at time.Time, sender, body string) SceneMessageRow {
	return SceneMessageRow{SceneMessageInput: SceneMessageInput{ProviderMessageID: id, SentAt: at, SenderClass: SceneSenderHuman,
		SenderRef: "dingtalk:org-a:open_id:" + sender, SenderName: sender, Body: body}}
}

func mustInsert(t *testing.T, q SceneMessageDB, key SceneMessageKey, source string, rows ...SceneMessageRow) SceneMessageInsertResult {
	t.Helper()
	result, err := InsertSceneMessages(context.Background(), q, key, source, rows)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func observationReceipt(t *testing.T, f fixture, key SceneMessageKey, eventID string) string {
	t.Helper()
	ws, _ := util.ParseUUID(key.WorkspaceID)
	agent, _ := util.ParseUUID(key.AgentID)
	principal, _ := util.ParseUUID(uuid.NewString())
	ev := eventrouter.Event{Version: 1, ID: eventID, Source: "dws-native-group/test", Type: "user_im_message_receive_group_all", Category: eventrouter.Observation,
		OccurredAt: time.Now(), PayloadSchema: "dws.native.event/1", Payload: json.RawMessage(`{"k":"` + eventID + `"}`)}
	receipt, _, err := eventrouter.Admit(context.Background(), f.pool, ev, eventrouter.Host{Owner: scene.Owner{WorkspaceID: ws, AgentID: agent}, PrincipalID: principal,
		TenantOrgID: key.TenantOrgID, Locator: scene.DingTalkConversation(key.TenantOrgID, scene.KindGroup, "cid-test"), Route: eventrouter.Unified, ConfigVersion: "t", Fingerprint: "fp-" + eventID})
	if err != nil {
		t.Fatal(err)
	}
	return util.UUIDToString(receipt.ID)
}

// One provider message is one row whichever path saw it first; a native
// observation that arrives after the wake-time read only attaches its receipt.
func TestObservationDedupAcrossWakeReadAndNative(t *testing.T) {
	f, key := transcriptDatabase(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Minute).UTC()
	first := mustInsert(t, f.pool, key, SceneMessageSourceWakeRead, transcriptRow("msg-1", at, "u1", "发版定在周四"))
	if first.Inserted != 1 || first.HumanInserted != 1 || !first.LastHumanAt.Equal(at.Truncate(time.Microsecond)) && !first.LastHumanAt.Equal(at) {
		t.Fatalf("first insert = %+v", first)
	}
	receipt := observationReceipt(t, f, key, "ev-1")
	row := transcriptRow("msg-1", at, "u1", "发版定在周四")
	row.ReceiptID = receipt
	again := mustInsert(t, f.pool, key, SceneMessageSourceNativeGroup, row)
	if again.Inserted != 0 || again.HumanInserted != 0 {
		t.Fatalf("duplicate counted as new: %+v", again)
	}
	var count int
	var gotReceipt, source string
	if err := f.pool.QueryRow(ctx, `SELECT count(*),max(receipt_id::text),max(source) FROM employee_scene_message`).Scan(&count, &gotReceipt, &source); err != nil {
		t.Fatal(err)
	}
	if count != 1 || gotReceipt != receipt || source != SceneMessageSourceWakeRead {
		t.Fatalf("rows=%d receipt=%q source=%q", count, gotReceipt, source)
	}
	// The same native event replayed changes nothing.
	if replay := mustInsert(t, f.pool, key, SceneMessageSourceNativeGroup, row); replay.Inserted != 0 {
		t.Fatalf("replay inserted %+v", replay)
	}
}

func TestSceneMessageInsertValidatesAndClips(t *testing.T) {
	f, key := transcriptDatabase(t)
	ctx := context.Background()
	long := strings.Repeat("长", 2000) // 6000 bytes
	result := mustInsert(t, f.pool, key, SceneMessageSourceNativeGroup, transcriptRow("msg-long", time.Now(), "u1", long))
	if result.Inserted != 1 {
		t.Fatalf("insert %+v", result)
	}
	var stored string
	var original int
	var truncated bool
	if err := f.pool.QueryRow(ctx, `SELECT body,original_bytes,truncated FROM employee_scene_message`).Scan(&stored, &original, &truncated); err != nil {
		t.Fatal(err)
	}
	if len(stored) > SceneMessageBodyLimit || !truncated || original != len(long) || !strings.HasPrefix(long, stored) {
		t.Fatalf("clip: %d bytes truncated=%v original=%d", len(stored), truncated, original)
	}
	for name, row := range map[string]SceneMessageRow{
		"empty body":   transcriptRow("m", time.Now(), "u", "  "),
		"no id":        transcriptRow("", time.Now(), "u", "x"),
		"no time":      transcriptRow("m", time.Time{}, "u", "x"),
		"bad class":    {SceneMessageInput: SceneMessageInput{ProviderMessageID: "m", SentAt: time.Now(), SenderClass: "admin", Body: "x"}},
		"bad receipt":  {SceneMessageInput: transcriptRow("m", time.Now(), "u", "x").SceneMessageInput, ReceiptID: "nope"},
		"newline id":   transcriptRow("m\n1", time.Now(), "u", "x"),
		"oversize ref": {SceneMessageInput: SceneMessageInput{ProviderMessageID: "m", SentAt: time.Now(), SenderClass: SceneSenderHuman, SenderRef: strings.Repeat("r", 400), Body: "x"}},
	} {
		if _, err := InsertSceneMessages(ctx, f.pool, key, SceneMessageSourceNativeGroup, []SceneMessageRow{row}); err != ErrInvalid {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	if _, err := InsertSceneMessages(ctx, f.pool, key, "router", []SceneMessageRow{transcriptRow("m", time.Now(), "u", "x")}); err != ErrInvalid {
		t.Errorf("unknown source accepted: %v", err)
	}
	bad := key
	bad.SceneID = ""
	if _, err := InsertSceneMessages(ctx, f.pool, bad, SceneMessageSourceNativeGroup, []SceneMessageRow{transcriptRow("m", time.Now(), "u", "x")}); err != ErrInvalid {
		t.Errorf("scene-less key accepted: %v", err)
	}
}

// Every read is fenced to the exact scene and tenant and hides withdrawn
// rows, lines before the scene memory reset and the evidence of forgotten or
// superseded memory records.
func TestSceneMessageReadFence(t *testing.T) {
	f, key := transcriptDatabase(t)
	ctx := context.Background()
	base := time.Now().Add(-2 * time.Hour).UTC().Truncate(time.Second)
	var rows []SceneMessageRow
	for i := 0; i < 6; i++ {
		rows = append(rows, transcriptRow(fmt.Sprintf("m%d", i), base.Add(time.Duration(i)*time.Minute), "u1", fmt.Sprintf("第%d条 发版计划", i)))
	}
	mustInsert(t, f.pool, key, SceneMessageSourceNativeGroup, rows...)
	if _, err := f.pool.Exec(ctx, `UPDATE employee_scene_message SET withdrawn_at=now(),withdrawn_reason='retention' WHERE provider_message_id='m1'`); err != nil {
		t.Fatal(err)
	}
	// Scene reset at m2's time hides m0..m2.
	if _, err := f.pool.Exec(ctx, `INSERT INTO employee_memory_state(workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id,reset_at) VALUES($1,$2,$3,$4,'scene','',$5)`,
		key.WorkspaceID, key.AgentID, key.TenantOrgID, key.SceneID, base.Add(2*time.Minute)); err != nil {
		t.Fatal(err)
	}
	// A private reset of one person does not bound the group transcript.
	if _, err := f.pool.Exec(ctx, `INSERT INTO employee_memory_state(workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id,reset_at) VALUES($1,$2,$3,$4,'private','dingtalk:org-a:open_id:u1',now())`,
		key.WorkspaceID, key.AgentID, key.TenantOrgID, key.SceneID); err != nil {
		t.Fatal(err)
	}
	// m4 is the evidence of a forgotten scene record.
	if _, err := f.pool.Exec(ctx, `INSERT INTO employee_learning(id,workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id,replay_key,record,forgotten_at)
 VALUES(gen_random_uuid(),$1,$2,$3,$4,'scene','',repeat('a',64),jsonb_build_object('evidence_id','m4'),now())`,
		key.WorkspaceID, key.AgentID, key.TenantOrgID, key.SceneID); err != nil {
		t.Fatal(err)
	}
	got, err := SceneMessagesAfter(ctx, f.pool, key, time.Time{}, "", 100)
	if err != nil {
		t.Fatal(err)
	}
	if ids := messageIDs(got); strings.Join(ids, ",") != "m3,m5" {
		t.Fatalf("visible = %v", ids)
	}
	corpus, err := SceneRecallCorpus(ctx, f.pool, key, time.Now(), []string{"m5"}, 100)
	if err != nil || strings.Join(messageIDs(corpus), ",") != "m3" {
		t.Fatalf("corpus = %v %v", messageIDs(corpus), err)
	}
	other := key
	other.TenantOrgID = "org-b"
	if got, err := SceneMessagesAfter(ctx, f.pool, other, time.Time{}, "", 100); err != nil || len(got) != 0 {
		t.Fatalf("another tenant read %v %v", messageIDs(got), err)
	}
	other = key
	other.SceneID = uuid.NewString()
	if got, err := SceneMessagesAfter(ctx, f.pool, other, time.Time{}, "", 100); err != nil || len(got) != 0 {
		t.Fatalf("another scene read %v %v", messageIDs(got), err)
	}
}

func messageIDs(rows []SceneMessage) []string {
	out := []string{}
	for _, r := range rows {
		out = append(out, r.ProviderMessageID)
	}
	return out
}

// The digest cursor reads strictly after (sent_at, id), ascending, with ties
// on sent_at broken by id; Before returns the newest rows oldest first.
func TestSceneMessagesCursorReads(t *testing.T) {
	f, key := transcriptDatabase(t)
	ctx := context.Background()
	at := time.Now().Add(-time.Hour).UTC().Truncate(time.Millisecond)
	mustInsert(t, f.pool, key, SceneMessageSourceNativeGroup,
		transcriptRow("b", at, "u1", "一"), transcriptRow("a", at, "u2", "二"), transcriptRow("c", at.Add(time.Second), "u1", "三"),
		transcriptRow("d", at.Add(2*time.Second), "u3", "四"))
	page, err := SceneMessagesAfter(ctx, f.pool, key, time.Time{}, "", 2)
	if err != nil || strings.Join(messageIDs(page), ",") != "a,b" {
		t.Fatalf("page1 = %v %v", messageIDs(page), err)
	}
	last := page[len(page)-1]
	page, err = SceneMessagesAfter(ctx, f.pool, key, last.SentAt, last.ProviderMessageID, 2)
	if err != nil || strings.Join(messageIDs(page), ",") != "c,d" {
		t.Fatalf("page2 = %v %v", messageIDs(page), err)
	}
	if page[0].SenderRef != "dingtalk:org-a:open_id:u1" || page[0].Body != "三" || page[0].Source != SceneMessageSourceNativeGroup {
		t.Fatalf("row = %+v", page[0])
	}
	before, err := SceneMessagesBefore(ctx, f.pool, key, at.Add(2*time.Second), "d", 2)
	if err != nil || strings.Join(messageIDs(before), ",") != "b,c" {
		t.Fatalf("before = %v %v", messageIDs(before), err)
	}
}

// The purge deletes in bounded batches, removes only the observation
// receipts its rows carry, and two replicas purging at once never delete a
// row twice or block each other.
func TestRetentionPurgeBatches(t *testing.T) {
	f, key := transcriptDatabase(t)
	ctx := context.Background()
	old := time.Now().Add(-15 * 24 * time.Hour).UTC()
	receipt := observationReceipt(t, f, key, "ev-old")
	// A user message receipt is never purged even if a row names it.
	userReceipt := f.admission.Item.ReceiptID
	const total = 1103
	for start := 0; start < total; start += SceneMessageInsertLimit {
		var rows []SceneMessageRow
		for i := start; i < start+SceneMessageInsertLimit && i < total; i++ {
			row := transcriptRow(fmt.Sprintf("old-%04d", i), old, "u1", "旧消息")
			if i == 0 {
				row.ReceiptID = receipt
			}
			if i == 1 {
				row.ReceiptID = userReceipt
			}
			rows = append(rows, row)
		}
		mustInsert(t, f.pool, key, SceneMessageSourceNativeGroup, rows...)
	}
	mustInsert(t, f.pool, key, SceneMessageSourceNativeGroup, transcriptRow("fresh", time.Now(), "u1", "新消息"))
	if _, err := f.pool.Exec(ctx, `UPDATE employee_scene_message SET first_seen_at=$1 WHERE provider_message_id LIKE 'old-%'`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE scene_event_receipt SET created_at=$1`, old); err != nil {
		t.Fatal(err)
	}
	cutoff := time.Now().Add(-SceneMessageRetention)
	var wg sync.WaitGroup
	var mu sync.Mutex
	deleted, receipts := 0, 0
	for replica := 0; replica < 2; replica++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				n, r, err := PurgeSceneMessages(ctx, f.pool, cutoff, 500)
				if err != nil {
					t.Error(err)
					return
				}
				if n > 500 {
					t.Errorf("batch of %d", n)
				}
				mu.Lock()
				deleted += n
				receipts += r
				mu.Unlock()
				if n == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()
	var left int
	var leftIDs []string
	rows, err := f.pool.Query(ctx, `SELECT provider_message_id FROM employee_scene_message`)
	if err != nil {
		t.Fatal(err)
	}
	leftIDs, err = pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	left = len(leftIDs)
	if deleted != total || left != 1 || leftIDs[0] != "fresh" || receipts != 1 {
		t.Fatalf("deleted=%d receipts=%d left=%v", deleted, receipts, leftIDs)
	}
	var userLeft, obsLeft int
	_ = f.pool.QueryRow(ctx, `SELECT count(*) FILTER (WHERE id=$1::uuid), count(*) FILTER (WHERE id=$2::uuid) FROM scene_event_receipt`, userReceipt, receipt).Scan(&userLeft, &obsLeft)
	if userLeft != 1 || obsLeft != 0 {
		t.Fatalf("user receipt left=%d observation receipt left=%d", userLeft, obsLeft)
	}
}

// Two replicas claiming due candidates never get the same row; a settled
// candidate is never reopened.
func TestProactiveClaimSkipLockedAndDecideOnce(t *testing.T) {
	f, key := transcriptDatabase(t)
	ctx := context.Background()
	now := time.Now().UTC()
	due := transcriptRow("q1", now.Add(-2*time.Minute), "u1", "请问发版是哪天？")
	due.ProactiveDueAt = now.Add(-time.Minute)
	later := transcriptRow("q2", now, "u1", "谁负责周报？")
	later.ProactiveDueAt = now.Add(time.Hour)
	wakeRead := transcriptRow("q3", now.Add(-3*time.Minute), "u1", "这是哪个版本？")
	wakeRead.ProactiveDueAt = now.Add(-time.Minute)
	mustInsert(t, f.pool, key, SceneMessageSourceNativeGroup, due, later)
	// A wake-time read never creates a proactive candidate.
	mustInsert(t, f.pool, key, SceneMessageSourceWakeRead, wakeRead)
	tx1, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx1.Rollback(ctx)
	got1, err := ClaimDueProactive(ctx, tx1, now, 10)
	if err != nil || len(got1) != 1 || got1[0].Message.ProviderMessageID != "q1" || got1[0].Key != key {
		t.Fatalf("claim1 = %+v %v", got1, err)
	}
	tx2, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	got2, err := ClaimDueProactive(ctx, tx2, now, 10)
	if err != nil || len(got2) != 0 {
		t.Fatalf("second replica claimed %+v %v", got2, err)
	}
	_ = tx2.Rollback(ctx)
	if err := DecideProactive(ctx, tx1, key, "q1", ProactiveAdmitted, "admitted", now); err != nil {
		t.Fatal(err)
	}
	if err := tx1.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if err := DecideProactive(ctx, f.pool, key, "q1", ProactiveSkipped, "late", now); err != nil {
		t.Fatal(err)
	}
	var state, reason string
	_ = f.pool.QueryRow(ctx, `SELECT proactive_state,proactive_reason FROM employee_scene_message WHERE provider_message_id='q1'`).Scan(&state, &reason)
	if state != ProactiveAdmitted || reason != "admitted" {
		t.Fatalf("reopened: %s %s", state, reason)
	}
	if n, err := ProactiveAdmittedSince(ctx, f.pool, key, now.Add(-time.Hour)); err != nil || n != 1 {
		t.Fatalf("admitted since = %d %v", n, err)
	}
}

// A colleague's later line counts as an answer; the asker's own follow-up
// and lines after the wait do not.
func TestHumanRepliesAfter(t *testing.T) {
	f, key := transcriptDatabase(t)
	ctx := context.Background()
	at := time.Now().Add(-10 * time.Minute).UTC().Truncate(time.Millisecond)
	mustInsert(t, f.pool, key, SceneMessageSourceNativeGroup,
		transcriptRow("q", at, "asker", "请问周报哪天交？"),
		transcriptRow("self-follow", at.Add(10*time.Second), "asker", "急"),
		transcriptRow("late", at.Add(5*time.Minute), "other", "周五"))
	question := SceneMessage{ProviderMessageID: "q", SentAt: at, SenderRef: "dingtalk:org-a:open_id:asker"}
	if n, err := HumanRepliesAfter(ctx, f.pool, key, question, at.Add(90*time.Second)); err != nil || n != 0 {
		t.Fatalf("within wait = %d %v", n, err)
	}
	if n, err := HumanRepliesAfter(ctx, f.pool, key, question, at.Add(6*time.Minute)); err != nil || n != 1 {
		t.Fatalf("after wait = %d %v", n, err)
	}
}

// Admitted user messages are found by provider message id; the addressed
// flag is true only for a line that @-mentions the receiving account.
func TestAdmittedTextsAndMessageAdmitted(t *testing.T) {
	f, key := transcriptDatabase(t)
	ctx := context.Background()
	payload := map[string]any{"command": map[string]any{
		"externalIdentity": map[string]any{"dws": map[string]any{"uid": "self-uid", "orgId": "org-a"}},
		"event": map[string]any{"data": map[string]any{"messages": []any{
			map[string]any{"openMsgId": "at-1", "text": "先别说话", "mentions": []any{map[string]any{"uid": "self-uid"}}},
			map[string]any{"openMsgId": "plain-1", "text": "大家好", "mentions": []any{}},
		}}}}}
	raw, _ := json.Marshal(payload)
	if _, err := f.pool.Exec(ctx, `INSERT INTO employee_event_consumption(receipt_id,workspace_id,agent_id,tenant_org_id,scene_id,principal_id,payload,state,owner_loop,config_revision)
 VALUES($1,$2,$3,$4,$5,$6,$7,'queued','employee','one')`, f.admission.Item.ReceiptID, key.WorkspaceID, key.AgentID, key.TenantOrgID, key.SceneID, f.admission.Item.PrincipalID, raw); err != nil {
		t.Fatal(err)
	}
	since := time.Now().Add(-time.Hour)
	texts, err := RecentAdmittedTexts(ctx, f.pool, key, since, 50)
	if err != nil || len(texts) != 2 {
		t.Fatalf("texts = %+v %v", texts, err)
	}
	addressed := map[string]bool{}
	for _, text := range texts {
		addressed[text.MessageID] = text.Addressed
	}
	if !addressed["at-1"] || addressed["plain-1"] {
		t.Fatalf("addressed = %v", addressed)
	}
	for id, want := range map[string]bool{"at-1": true, "plain-1": true, "never": false} {
		if got, err := MessageAdmitted(ctx, f.pool, key, id, since); err != nil || got != want {
			t.Fatalf("%s admitted = %v %v", id, got, err)
		}
	}
}
