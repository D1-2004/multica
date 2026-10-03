package employeeentry

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/scene"
)

type wakeHost struct {
	ready    bool
	readyErr error
	fenceErr error
}

// sceneMessageReader is the minimal origin reader of a scene dispatch Task:
// the frozen consumption supplies the principal; the scene is the anchor.
type sceneMessageReader struct{}

func (sceneMessageReader) ReadTaskOrigin(ctx context.Context, db DB, scope Scope, task employeetask.Task, request employeetask.Entry) (TaskOrigin, error) {
	admission, err := ReadSceneMessageAdmission(ctx, db, scope, request)
	if err != nil {
		return TaskOrigin{}, err
	}
	return TaskOrigin{PrincipalID: admission.PrincipalID, PrincipalKind: PrincipalMember, ReceiptID: admission.ReceiptID, JobID: admission.JobID,
		History: HistoryScenePrincipal, HistoryPrincipalID: admission.PrincipalID, Anchor: DeliveryAnchor{Conversation: true, SceneID: scope.SceneID, DWSUID: "dws-employee"}}, nil
}

var testOrigins = func() *TaskOriginRegistry {
	r := NewTaskOriginRegistry()
	if err := r.Register(TaskOriginNamespace, sceneMessageReader{}); err != nil {
		panic(err)
	}
	return r
}()

func (h wakeHost) TaskWakeProducerReady(context.Context) (bool, error) { return h.ready, h.readyErr }
func (h wakeHost) FenceTaskWakeScene(context.Context, pgx.Tx, Scope) error {
	return h.fenceErr
}
func (h wakeHost) TaskOrigin(ctx context.Context, db DB, scope Scope, taskID string) (TaskOrigin, error) {
	return testOrigins.Read(ctx, db, scope, taskID)
}

var readyHost = wakeHost{ready: true}

// originTask creates an Employee Task whose request entry points at the
// fixture's admitted message receipt, as the scene Host's dispatch_task does.
func originTask(t *testing.T, f fixture) (employeetask.Task, Consumption) {
	t.Helper()
	origin := admit(t, f)
	task, err := employeetask.NewStore(f.pool).Create(context.Background(), employeetask.CreateParams{
		Scope:     employeetask.Scope{WorkspaceID: f.admission.Scope.WorkspaceID, AgentID: f.admission.Scope.AgentID, TenantOrgID: f.admission.Scope.TenantOrgID, Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: f.admission.Scope.SceneID}},
		OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: "dingtalk:org-a:uid:alice",
		Definition: employeetask.Definition{Goal: "Collect three answers"},
		Source:     employeetask.Source{Namespace: TaskOriginNamespace, Key: f.admission.Item.ReceiptID + "/call-origin/definition"},
		Input:      `{"source_ref":"origin"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	return task, origin
}

func wakeAdmission(f fixture, task employeetask.Task, id string) TaskWakeAdmission {
	return TaskWakeAdmission{Scope: f.admission.Scope, Source: "collection", EventID: id, OccurredAt: time.Now().UTC(), Wake: TaskWake{
		SchemaVersion: TaskWakeSchemaVersion, Kind: TaskWakeCollectionReady, TaskID: task.ID, GoalRevision: task.GoalRevision,
		InputSeq: task.LastEntrySeq, AuthorityRef: "task:" + task.ID, EvidenceRef: "collection:" + id,
	}}
}

func completeJob(t *testing.T, f fixture, job Job) {
	t.Helper()
	ctx := context.Background()
	if err := f.store.SaveOutcome(ctx, job, json.RawMessage(`{"kind":"quiet"}`)); err != nil {
		t.Fatal(err)
	}
	if err := f.store.Complete(ctx, job, nil); err != nil {
		t.Fatal(err)
	}
}

func countRows(t *testing.T, f fixture, query string, args ...any) int {
	t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestTaskWakeDoesNotCreateSyntheticChatOrMergeHumanWindow(t *testing.T) {
	f, _ := recentHistoryDatabase(t)
	ctx := context.Background()
	// A message window still refuses zero messages; a wake has its own path.
	zero := f.admission
	zero.Item.MessageCount = 0
	if _, err := f.store.Admit(ctx, zero); !errors.Is(err, ErrInvalid) {
		t.Fatalf("zero-message admission accepted: %v", err)
	}
	task, _ := originTask(t, f)
	completeJob(t, f, claim(t, f))
	wake, err := f.store.AdmitTaskWake(ctx, readyHost, wakeAdmission(f, task, "collection-1/ready"))
	if err != nil {
		t.Fatal(err)
	}
	if wake.State != "queued" || wake.Owner != Employee || !validID(wake.JobID) {
		t.Fatalf("wake consumption: %+v", wake)
	}
	// The same requester speaks inside the merge window: a new human window.
	human := secondReceipt(t, f)
	message, err := f.store.Admit(ctx, human)
	if err != nil {
		t.Fatal(err)
	}
	if message.JobID == wake.JobID {
		t.Fatal("human message merged into a task wake")
	}
	var kind string
	var count int
	var items []Item
	if err = f.pool.QueryRow(ctx, `SELECT kind,message_count,items FROM employee_scene_job WHERE id=$1`, wake.JobID).Scan(&kind, &count, &items); err != nil {
		t.Fatal(err)
	}
	if kind != KindTaskWake || count != 0 || len(items) != 1 || items[0].ReceiptID != wake.ReceiptID || items[0].MessageCount != 0 {
		t.Fatalf("wake job shape: kind=%s count=%d items=%+v", kind, count, items)
	}
	decoded, err := DecodeTaskWake(items[0])
	if err != nil || decoded.TaskID != task.ID || decoded.Kind != TaskWakeCollectionReady {
		t.Fatalf("wake payload: %+v %v", decoded, err)
	}
	for _, forbidden := range []string{"command", "messages", "text", "principal_id\":\"dingtalk"} {
		if strings.Contains(string(items[0].Payload), forbidden) {
			t.Fatalf("wake carries a synthetic message field %q: %s", forbidden, items[0].Payload)
		}
	}
	if err = f.pool.QueryRow(ctx, `SELECT kind,message_count FROM employee_scene_job WHERE id=$1`, message.JobID).Scan(&kind, &count); err != nil || kind != KindMessage || count != 1 {
		t.Fatalf("human window: kind=%s count=%d %v", kind, count, err)
	}
	var category, principal string
	if err = f.pool.QueryRow(ctx, `SELECT envelope->>'category',principal_id::text FROM scene_event_receipt WHERE id=$1`, wake.ReceiptID).Scan(&category, &principal); err != nil || category != "wake" || principal != f.admission.Item.PrincipalID {
		t.Fatalf("wake receipt: category=%s principal=%s %v", category, principal, err)
	}
	// Recent dialogue reads admitted human text only; the wake is not a turn.
	history, err := f.store.RecentConversation(ctx, RecentConversationRequest{Scope: f.admission.Scope, PrincipalID: f.admission.Item.PrincipalID, Before: time.Now().Add(time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	for _, turn := range history.Messages {
		if turn.ReceiptID == wake.ReceiptID {
			t.Fatalf("wake appeared as conversation: %+v", turn)
		}
	}
	// Storage rejects a message window without messages and a wake with any.
	for _, shape := range []struct {
		kind  string
		count int
	}{{KindMessage, 0}, {KindTaskWake, 1}, {"other", 1}} {
		_, err = f.pool.Exec(ctx, `INSERT INTO employee_scene_job(workspace_id,agent_id,tenant_org_id,scene_id,principal_id,items,message_count,kind) SELECT workspace_id,agent_id,tenant_org_id,scene_id,principal_id,items,$2,$3 FROM employee_scene_job WHERE id=$1`, wake.JobID, shape.count, shape.kind)
		if err == nil || !strings.Contains(err.Error(), "check constraint") {
			t.Fatalf("storage accepted kind=%s count=%d: %v", shape.kind, shape.count, err)
		}
	}
}

func TestTaskWakeReceiptReplayCreatesOneJob(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task, _ := originTask(t, f)
	first := wakeAdmission(f, task, "collection-2/ready")
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan Consumption, 8)
	errs := make(chan error, 8)
	for i := range 8 {
		wg.Go(func() {
			<-start
			a := first
			// Retries carry their own clock; identity must not depend on it.
			a.OccurredAt = first.OccurredAt.Add(time.Duration(i) * time.Second)
			c, err := NewStore(f.pool).AdmitTaskWake(ctx, readyHost, a)
			results <- c
			errs <- err
		})
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	job := ""
	for c := range results {
		if job != "" && c.JobID != job {
			t.Fatalf("replay created another job: %s vs %s", c.JobID, job)
		}
		job = c.JobID
	}
	if n := countRows(t, f, `SELECT count(*) FROM employee_scene_job WHERE kind='task_wake'`); n != 1 {
		t.Fatalf("task wake jobs=%d", n)
	}
	if n := countRows(t, f, `SELECT count(*) FROM scene_event_receipt WHERE source=$1 AND source_event_id=$2`, TaskWakeSourcePrefix+"collection", first.EventID); n != 1 {
		t.Fatalf("wake receipts=%d", n)
	}
	if n := countRows(t, f, `SELECT count(*) FROM employee_event_consumption WHERE job_id=$1`, job); n != 1 {
		t.Fatalf("wake consumptions=%d", n)
	}
	var occurred time.Time
	if err := f.pool.QueryRow(ctx, `SELECT (envelope->>'occurred_at')::timestamptz FROM scene_event_receipt WHERE source=$1 AND source_event_id=$2`, TaskWakeSourcePrefix+"collection", first.EventID).Scan(&occurred); err != nil {
		t.Fatal(err)
	}
	if occurred.Before(first.OccurredAt.Add(-time.Millisecond)) || occurred.After(first.OccurredAt.Add(8*time.Second)) {
		t.Fatalf("occurred_at not frozen from one admission: %s", occurred)
	}
	later := first
	later.OccurredAt = time.Now().Add(time.Hour)
	again, err := f.store.AdmitTaskWake(ctx, readyHost, later)
	if err != nil || again.JobID != job {
		t.Fatalf("later replay: %+v %v", again, err)
	}
	var stillOccurred time.Time
	if err = f.pool.QueryRow(ctx, `SELECT (envelope->>'occurred_at')::timestamptz FROM scene_event_receipt WHERE source=$1 AND source_event_id=$2`, TaskWakeSourcePrefix+"collection", first.EventID).Scan(&stillOccurred); err != nil || !stillOccurred.Equal(occurred) {
		t.Fatalf("replay rewrote occurred_at: %s -> %s %v", occurred, stillOccurred, err)
	}
}

func TestTaskWakeSourceScopeAuthorityRevokedAndFingerprintConflict(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task, _ := originTask(t, f)
	refused := func(name string, host TaskWakeHost, a TaskWakeAdmission, want error) {
		t.Helper()
		if _, err := f.store.AdmitTaskWake(ctx, host, a); !errors.Is(err, want) {
			t.Fatalf("%s: got %v, want %v", name, err, want)
		}
		if n := countRows(t, f, `SELECT count(*) FROM employee_scene_job WHERE kind='task_wake'`); n != 0 {
			t.Fatalf("%s created a wake job", name)
		}
	}
	a := wakeAdmission(f, task, "collection-3/ready")
	refused("replica not ready", wakeHost{}, a, ErrTaskWakeNotReady)
	refused("readiness error", wakeHost{readyErr: errors.New("fence unavailable")}, a, ErrTaskWakeNotReady)
	refused("nil host", nil, a, ErrTaskWakeNotReady)
	refused("tenant fence", wakeHost{ready: true, fenceErr: ErrNotFound}, a, ErrNotFound)
	bad := a
	bad.Scope.TenantOrgID = "org-other"
	refused("other tenant", readyHost, bad, ErrNotFound)
	other, err := scene.ParseID(uuid.NewString())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `INSERT INTO agent_scene(id,workspace_id,agent_id,provider,tenant_org_id,source_namespace,scene_kind,external_scene_id) SELECT $1,workspace_id,agent_id,provider,tenant_org_id,source_namespace,scene_kind,'cid-other' FROM agent_scene WHERE id=$2`, other, f.admission.Scope.SceneID); err != nil {
		t.Fatal(err)
	}
	bad = a
	bad.Scope.SceneID = uuid.UUID(other.Bytes).String()
	refused("other scene", readyHost, bad, ErrNotFound)
	bad = a
	bad.Wake.TaskID = uuid.NewString()
	refused("unknown task", readyHost, bad, ErrNotFound)
	bad = a
	bad.Wake.Kind = "approval.decision"
	refused("unknown kind", readyHost, bad, ErrInvalid)
	bad = a
	bad.Source = "Collection"
	refused("unreserved source", readyHost, bad, ErrInvalid)
	bad = a
	bad.Wake.GoalRevision = task.GoalRevision + 1
	refused("future goal revision", readyHost, bad, ErrTaskWakeStale)
	bad = a
	bad.Wake.InputSeq = task.LastEntrySeq + 1
	refused("input boundary ahead", readyHost, bad, ErrTaskWakeStale)
	// A correction advanced the goal: the old revision cannot wake the Task.
	if _, err = f.pool.Exec(ctx, `UPDATE employee_task SET goal_revision=goal_revision+1 WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	refused("stale goal revision", readyHost, a, ErrTaskWakeStale)
	if _, err = f.pool.Exec(ctx, `UPDATE employee_task SET goal_revision=goal_revision-1,state='cancelled' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	refused("stopped task", readyHost, a, ErrTaskWakeStopped)
	if _, err = f.pool.Exec(ctx, `UPDATE employee_task SET state='ready' WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	// The origin admission is the authority: without its Employee consumption
	// (or with a Task from another source) no wake can address the Task.
	if _, err = f.pool.Exec(ctx, `UPDATE employee_event_consumption SET owner_loop='coordinator' WHERE receipt_id=$1`, f.admission.Item.ReceiptID); err != nil {
		t.Fatal(err)
	}
	refused("origin owned by coordinator", readyHost, a, ErrTaskWakeOrigin)
	if _, err = f.pool.Exec(ctx, `UPDATE employee_event_consumption SET owner_loop='employee' WHERE receipt_id=$1`, f.admission.Item.ReceiptID); err != nil {
		t.Fatal(err)
	}
	foreign, err := employeetask.NewStore(f.pool).Create(ctx, employeetask.CreateParams{Scope: taskScope(f.admission.Scope), OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: "routine:1", Definition: employeetask.Definition{Goal: "Routine"}, Source: employeetask.Source{Namespace: "scene_routine", Key: "occurrence-1"}})
	if err != nil {
		t.Fatal(err)
	}
	refused("unsupported origin", readyHost, wakeAdmission(f, foreign, "routine-1"), ErrTaskWakeOrigin)

	accepted, err := f.store.AdmitTaskWake(ctx, readyHost, a)
	if err != nil {
		t.Fatal(err)
	}
	changed := a
	changed.Wake.EvidenceRef = "collection:other-answer"
	if _, err = f.store.AdmitTaskWake(ctx, readyHost, changed); !errors.Is(err, ErrConflict) {
		t.Fatalf("same identity with another fingerprint: %v", err)
	}
	moved := a
	moved.Scope.SceneID = uuid.UUID(other.Bytes).String()
	if _, err = f.store.AdmitTaskWake(ctx, readyHost, moved); err == nil {
		t.Fatal("same identity accepted in another scene")
	}
	if n := countRows(t, f, `SELECT count(*) FROM employee_scene_job WHERE kind='task_wake'`); n != 1 {
		t.Fatalf("conflicts created jobs: %d", n)
	}
	var job string
	if err = f.pool.QueryRow(ctx, `SELECT id::text FROM employee_scene_job WHERE kind='task_wake'`).Scan(&job); err != nil || job != accepted.JobID {
		t.Fatalf("accepted wake: %s %v", job, err)
	}
}

func TestTaskWakeCrashAfterIntentCommitRecoversOriginalJob(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task, _ := originTask(t, f)
	completeJob(t, f, claim(t, f))
	a := wakeAdmission(f, task, "execution-1/follow-up")
	a.Source, a.Wake.Kind = "execution", TaskWakeExecutionFollowUp
	committed, err := f.store.AdmitTaskWake(ctx, readyHost, a)
	if err != nil {
		t.Fatal(err)
	}
	// The producer crashed after commit and before notifying anyone. Meanwhile
	// the Task moved on; the replay still returns the original intent.
	if _, err = f.pool.Exec(ctx, `UPDATE employee_task SET goal_revision=goal_revision+1 WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	restarted := NewStore(f.pool)
	retry := a
	retry.OccurredAt = time.Now().Add(time.Minute)
	replayed, err := restarted.AdmitTaskWake(ctx, readyHost, retry)
	if err != nil || replayed.JobID != committed.JobID || replayed.ReceiptID != committed.ReceiptID {
		t.Fatalf("restart replay: %+v vs %+v %v", replayed, committed, err)
	}
	// The worker finds it by polling; a lost lease is reclaimed as the same job.
	job, err := restarted.Claim(ctx)
	if err != nil || job.ID != committed.JobID || job.Kind != KindTaskWake {
		t.Fatalf("poll recovery: %+v %v", job, err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE employee_scene_job SET lease_until=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	again, err := restarted.Claim(ctx)
	if err != nil || again.ID != job.ID || again.Generation <= job.Generation || again.Attempts != 2 {
		t.Fatalf("reclaim: %+v %v", again, err)
	}
	if n := countRows(t, f, `SELECT count(*) FROM employee_scene_job WHERE kind='task_wake'`); n != 1 {
		t.Fatalf("crash recovery jobs=%d", n)
	}
}

func TestTaskWakeHumanInputIsClaimedFirst(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task, _ := originTask(t, f)
	completeJob(t, f, claim(t, f))
	wake, err := f.store.AdmitTaskWake(ctx, readyHost, wakeAdmission(f, task, "collection-4/ready"))
	if err != nil {
		t.Fatal(err)
	}
	// Make the wake strictly older than the newer human message.
	if _, err = f.pool.Exec(ctx, `UPDATE employee_scene_job SET created_at=now()-interval '1 minute',available_at=now()-interval '1 minute' WHERE id=$1`, wake.JobID); err != nil {
		t.Fatal(err)
	}
	human, err := f.store.Admit(ctx, secondReceipt(t, f))
	if err != nil {
		t.Fatal(err)
	}
	first := claim(t, f)
	if first.ID != human.JobID || first.Kind != KindMessage {
		t.Fatalf("wake jumped ahead of newer human input: claimed %s (%s), human %s", first.ID, first.Kind, human.JobID)
	}
	if _, err = f.store.Claim(ctx); !errors.Is(err, ErrNoJob) {
		t.Fatalf("wake ran beside the human window: %v", err)
	}
	// A human window waiting for its retry still precedes the wake.
	if err = f.store.Retry(ctx, first, "temporary"); err != nil {
		t.Fatal(err)
	}
	if _, err = f.store.Claim(ctx); !errors.Is(err, ErrNoJob) {
		t.Fatalf("wake bypassed human input in retry backoff: %v", err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE employee_scene_job SET available_at=now() WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	completeJob(t, f, claim(t, f))
	next := claim(t, f)
	if next.ID != wake.JobID || next.Kind != KindTaskWake {
		t.Fatalf("wake not claimed after human input: %+v", next)
	}
}

func TestTaskWakeOldBinaryAndUnknownWakeKindStayPending(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task, _ := originTask(t, f)
	completeJob(t, f, claim(t, f))
	wake, err := f.store.AdmitTaskWake(ctx, readyHost, wakeAdmission(f, task, "collection-5/ready"))
	if err != nil {
		t.Fatal(err)
	}
	// Even knowing every wake kind, a binary without the task_wake job kind
	// never claims one.
	old := Support{Kinds: []string{KindMessage}, WakeKinds: TaskWakeKinds(), WakeSchemas: []string{"1"}}
	if _, err = f.store.ClaimSupported(ctx, old); !errors.Is(err, ErrNoJob) {
		t.Fatalf("message-only binary claimed a task wake: %v", err)
	}
	// A message-only binary still serves human input in the same scene.
	human, err := f.store.Admit(ctx, secondReceipt(t, f))
	if err != nil {
		t.Fatal(err)
	}
	job, err := f.store.ClaimSupported(ctx, old)
	if err != nil || job.ID != human.JobID {
		t.Fatalf("message-only binary: %+v %v", job, err)
	}
	completeJob(t, f, job)
	// A wake kind or schema from a newer binary is retained, not decoded.
	for _, patch := range []string{`{"kind":"approval.decision"}`, `{"schema_version":2}`} {
		if _, err = f.pool.Exec(ctx, `UPDATE employee_scene_job SET items=jsonb_set(items,'{0,payload}',items->0->'payload' || $2::jsonb) WHERE id=$1`, wake.JobID, patch); err != nil {
			t.Fatal(err)
		}
		if _, err = f.store.Claim(ctx); !errors.Is(err, ErrNoJob) {
			t.Fatalf("claimed unsupported wake %s: %v", patch, err)
		}
		if _, err = f.pool.Exec(ctx, `UPDATE employee_scene_job SET items=jsonb_set(items,'{0,payload}',items->0->'payload' || '{"kind":"collection.ready","schema_version":1}'::jsonb) WHERE id=$1`, wake.JobID); err != nil {
			t.Fatal(err)
		}
	}
	var state string
	if err = f.pool.QueryRow(ctx, `SELECT state FROM employee_scene_job WHERE id=$1`, wake.JobID).Scan(&state); err != nil || state != "pending" {
		t.Fatalf("unsupported wake was not retained: %s %v", state, err)
	}
	if job = claim(t, f); job.ID != wake.JobID {
		t.Fatalf("supporting binary did not recover wake: %+v", job)
	}
	if _, err = f.store.ClaimSupported(ctx, Support{}); !errors.Is(err, ErrInvalid) {
		t.Fatalf("empty support set: %v", err)
	}
}

type fixedOriginReader struct{ origin TaskOrigin }

func (r fixedOriginReader) ReadTaskOrigin(context.Context, DB, Scope, employeetask.Task, employeetask.Entry) (TaskOrigin, error) {
	return r.origin, nil
}

func TestTaskOriginRegistryDispatchesByNamespaceAndRejectsInconsistentOrigins(t *testing.T) {
	f := database(t)
	ctx := context.Background()
	task, _ := originTask(t, f)
	registry := NewTaskOriginRegistry()
	if _, err := registry.Read(ctx, f.pool, f.admission.Scope, task.ID); !errors.Is(err, ErrTaskWakeOrigin) {
		t.Fatalf("unregistered namespace: %v", err)
	}
	if err := registry.Register(TaskOriginNamespace, sceneMessageReader{}); err != nil {
		t.Fatal(err)
	}
	if err := registry.Register(TaskOriginNamespace, sceneMessageReader{}); !errors.Is(err, ErrConflict) {
		t.Fatalf("second reader for a namespace: %v", err)
	}
	origin, err := registry.Read(ctx, f.pool, f.admission.Scope, task.ID)
	if err != nil || origin.Task.ID != task.ID || origin.Request.Seq != 1 || origin.PrincipalID != f.admission.Item.PrincipalID || origin.ReceiptID != f.admission.Item.ReceiptID {
		t.Fatalf("scene origin: %+v %v", origin, err)
	}
	// A routine Task resolves only through its own reader, which must return a
	// self-consistent origin; an enterprise origin has no conversation.
	routine, err := employeetask.NewStore(f.pool).Create(ctx, employeetask.CreateParams{Scope: taskScope(f.admission.Scope), OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: "routine:1", Definition: employeetask.Definition{Goal: "Routine"}, Source: employeetask.Source{Namespace: "test_routine", Key: "occurrence-1"}})
	if err != nil {
		t.Fatal(err)
	}
	principal := uuid.NewString()
	good := TaskOrigin{PrincipalID: principal, PrincipalKind: "agent_creator", History: HistoryNotApplicable, Anchor: DeliveryAnchor{SceneID: f.admission.Scope.SceneID}}
	reader := &fixedOriginReader{origin: good}
	if err = registry.Register("test_routine", reader); err != nil {
		t.Fatal(err)
	}
	if origin, err = registry.Read(ctx, f.pool, f.admission.Scope, routine.ID); err != nil || origin.Task.ID != routine.ID || origin.History != HistoryNotApplicable {
		t.Fatalf("routine origin: %+v %v", origin, err)
	}
	for name, bad := range map[string]func(*TaskOrigin){
		"policy":               func(o *TaskOrigin) { o.History = "anyone" },
		"principal":            func(o *TaskOrigin) { o.PrincipalID = "agent" },
		"kind":                 func(o *TaskOrigin) { o.PrincipalKind = "" },
		"anchor_scene":         func(o *TaskOrigin) { o.Anchor.SceneID = uuid.NewString() },
		"not_applicable_read":  func(o *TaskOrigin) { o.HistoryPrincipalID = principal },
		"conversation_history": func(o *TaskOrigin) { o.Anchor.Conversation = true; o.Anchor.DWSUID = "dws" },
		"history_principal": func(o *TaskOrigin) {
			o.History = HistorySceneEndpointPrincipal
			o.Anchor.Conversation = true
			o.Anchor.DWSUID = "dws"
		},
		"identity": func(o *TaskOrigin) {
			o.History, o.HistoryPrincipalID, o.Anchor.Conversation = HistoryScenePrincipal, principal, true
		},
	} {
		reader.origin = good
		bad(&reader.origin)
		var hold *TaskOriginHold
		if _, err = registry.Read(ctx, f.pool, f.admission.Scope, routine.ID); !errors.As(err, &hold) || !errors.Is(err, ErrTaskWakeRevoked) {
			t.Fatalf("%s: inconsistent origin accepted: %v", name, err)
		}
	}
}

func TestHostNoticeBecomesHistoryForItsSceneAndPrincipal(t *testing.T) {
	f, now := recentHistoryDatabase(t)
	ctx := context.Background()
	scope, principal := f.admission.Scope, f.admission.Item.PrincipalID
	var conversation string
	if err := f.pool.QueryRow(ctx, `SELECT external_scene_id FROM agent_scene WHERE id=$1`, scope.SceneID).Scan(&conversation); err != nil {
		t.Fatal(err)
	}
	action := func(id, text string, at time.Time) {
		t.Helper()
		input, _ := json.Marshal(map[string]any{"workspace_id": scope.WorkspaceID, "agent_id": scope.AgentID, "scene_id": scope.SceneID, "dws_org_id": scope.TenantOrgID, "conversation_id": conversation, "dws_uid": "unrelated-to-window", "scene_notice_id": uuid.NewString(), "text": text})
		if _, err := f.pool.Exec(ctx, `INSERT INTO response_action(id,workspace_id,agent_id,request_id,kind,input,state,provider_message_id,provider_conversation_id,created_at,updated_at) VALUES($1,$2::uuid,$3::uuid,$1,'message.send',$4,'delivered',$5,$6,$7,$7)`, id, scope.WorkspaceID, scope.AgentID, input, "provider-"+id, conversation, at); err != nil {
			t.Fatal(err)
		}
	}
	history := func(principal string) []RecentConversationMessage {
		t.Helper()
		out, err := f.store.RecentConversation(ctx, RecentConversationRequest{Scope: scope, PrincipalID: principal, Before: now.Add(time.Minute)})
		if err != nil {
			t.Fatal(err)
		}
		return out.Messages
	}
	action("wake-reply", "李四：周二；王五：周三。", now.Add(-time.Minute))
	action("unlinked", "not a host notice", now.Add(-time.Minute))
	if len(history(principal)) != 0 {
		t.Fatal("an unlinked send became history")
	}
	notice := HostNotice{ActionID: "wake-reply", Scope: scope, PrincipalID: principal, SourceKind: HostNoticeTaskWake, SourceID: uuid.NewString(), OriginReceiptID: f.admission.Item.ReceiptID}
	if err := RecordHostNotice(ctx, f.pool, notice); err != nil {
		t.Fatal(err)
	}
	if err := RecordHostNotice(ctx, f.pool, notice); err != nil {
		t.Fatalf("idempotent record: %v", err)
	}
	other := notice
	other.SourceID = uuid.NewString()
	if err := RecordHostNotice(ctx, f.pool, other); !errors.Is(err, ErrConflict) {
		t.Fatalf("same action with other facts: %v", err)
	}
	other = notice
	other.SourceKind = "reminder"
	if err := RecordHostNotice(ctx, f.pool, other); !errors.Is(err, ErrInvalid) {
		t.Fatalf("unknown source kind: %v", err)
	}
	turns := history(principal)
	if len(turns) != 1 || turns[0].Role != "assistant" || turns[0].Text != "李四：周二；王五：周三。" || turns[0].ActionID != "wake-reply" {
		t.Fatalf("host notice history: %+v", turns)
	}
	if len(history(uuid.NewString())) != 0 {
		t.Fatal("another principal read the host notice")
	}
	// Forgetting evidence from the Task's origin message hides derived notices.
	recentHistoryMemoryEvidence(t, f, scope, "dingtalk:org-a:uid:alice", f.admission.Item.ReceiptID, "origin-message", "forgotten")
	if turns = history(principal); len(turns) != 0 {
		t.Fatalf("withdrawn origin still exposed its host notice: %+v", turns)
	}
}
