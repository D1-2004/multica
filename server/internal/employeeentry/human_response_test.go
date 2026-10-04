package employeeentry

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/google/uuid"
)

func TestDecodeHumanResponseStrictReferences(t *testing.T) {
	valid := HumanResponse{QuestionRef: uuid.NewString(), ResponseRef: uuid.NewString(), Version: 2}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	item := Item{Payload: raw}
	if got, err := DecodeHumanResponse(item); err != nil || got != valid {
		t.Fatalf("valid references: %+v, %v", got, err)
	}
	for _, tc := range []struct {
		name string
		item Item
	}{
		{"IM masquerade", Item{Payload: raw, MessageCount: 1}},
		{"unknown target", Item{Payload: append(append(json.RawMessage(nil), raw[:len(raw)-1]...), []byte(`,"task_id":"untrusted"}`)...)}},
		{"trailing object", Item{Payload: append(append(json.RawMessage(nil), raw...), []byte(` {}`)...)}},
		{"trailing primitive", Item{Payload: append(append(json.RawMessage(nil), raw...), []byte(` true`)...)}},
		{"missing references", Item{Payload: json.RawMessage(`{"version":1}`)}},
		{"null", Item{Payload: json.RawMessage(`null`)}},
		{"invalid version", Item{Payload: json.RawMessage(`{"question_ref":"` + valid.QuestionRef + `","response_ref":"` + valid.ResponseRef + `","version":0}`)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := DecodeHumanResponse(tc.item); !errors.Is(err, ErrInvalid) {
				t.Fatalf("accepted unsafe payload: %v", err)
			}
		})
	}
}

func humanResponseDatabase(t *testing.T) fixture {
	t.Helper()
	f := database(t)
	_, self, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(self), "..", "..", "migrations")
	for _, name := range []string{"9977_employee_scene_job_human_response.up.sql", "9978_employee_scene_job_human_response_validate.up.sql"} {
		apply(t, f.pool, filepath.Join(dir, name))
	}
	return f
}

func humanResponseAdmission(f fixture) HumanResponseAdmission {
	return HumanResponseAdmission{Scope: f.admission.Scope, ReceiptID: f.admission.Item.ReceiptID, PrincipalID: f.admission.Item.PrincipalID,
		Response: HumanResponse{QuestionRef: uuid.NewString(), ResponseRef: uuid.NewString(), Version: 1}}
}

func admitHumanResponse(t *testing.T, f fixture, a HumanResponseAdmission) (Job, Consumption, error) {
	t.Helper()
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		return Job{}, Consumption{}, err
	}
	defer tx.Rollback(ctx)
	j, c, err := f.store.AdmitHumanResponseTx(ctx, tx, a)
	if err != nil {
		return Job{}, Consumption{}, err
	}
	return j, c, tx.Commit(ctx)
}

func TestHumanResponseConcurrentReplayAndContentConflict(t *testing.T) {
	f := humanResponseDatabase(t)
	a := humanResponseAdmission(f)
	var wg sync.WaitGroup
	jobs := make(chan Job, 8)
	errs := make(chan error, 8)
	for range 8 {
		wg.Go(func() { j, _, err := admitHumanResponse(t, f, a); jobs <- j; errs <- err })
	}
	wg.Wait()
	close(jobs)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var id string
	for j := range jobs {
		if id == "" {
			id = j.ID
		}
		if j.ID != id || j.Kind != KindHumanResponse || len(j.Items) != 1 || j.Items[0].MessageCount != 0 {
			t.Fatalf("replay changed job or synthesized IM: %+v", j)
		}
	}
	if n := countRows(t, f, `SELECT count(*) FROM employee_scene_job`); n != 1 {
		t.Fatalf("created %d jobs", n)
	}
	if n := countRows(t, f, `SELECT count(*) FROM employee_event_consumption`); n != 1 {
		t.Fatalf("created %d consumptions", n)
	}
	a.Response.Version++
	if _, _, err := admitHumanResponse(t, f, a); !errors.Is(err, ErrConflict) {
		t.Fatalf("same receipt changed content: %v", err)
	}
}

func TestHumanResponseAdmissionRollbackAndReceiptFence(t *testing.T) {
	f := humanResponseDatabase(t)
	ctx := context.Background()
	a := humanResponseAdmission(f)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = f.store.AdmitHumanResponseTx(ctx, tx, a); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if n := countRows(t, f, `SELECT count(*) FROM employee_scene_job`); n != 0 {
		t.Fatalf("rollback leaked %d jobs", n)
	}
	if n := countRows(t, f, `SELECT count(*) FROM employee_event_consumption`); n != 0 {
		t.Fatalf("rollback leaked %d consumptions", n)
	}
	for _, mutate := range []func(*HumanResponseAdmission){
		func(a *HumanResponseAdmission) { a.Scope.TenantOrgID = "other" },
		func(a *HumanResponseAdmission) { a.PrincipalID = uuid.NewString() },
		func(a *HumanResponseAdmission) { a.Scope.SceneID = uuid.NewString() },
	} {
		bad := a
		mutate(&bad)
		if _, _, err := admitHumanResponse(t, f, bad); !errors.Is(err, ErrNotFound) {
			t.Fatalf("cross-boundary receipt accepted: %v", err)
		}
	}
	for _, tc := range []struct {
		change string
		want   error
	}{{`state='unmapped',scene_id=NULL`, ErrNotFound}, {`reason='held'`, ErrInvalid}, {`route='legacy',state='legacy'`, ErrInvalid}} {
		if _, err := f.pool.Exec(ctx, `UPDATE scene_event_receipt SET state='ready',scene_id=$2::uuid,reason='',route='unified' WHERE id=$1`, a.ReceiptID, a.Scope.SceneID); err != nil {
			t.Fatal(err)
		}
		if _, err := f.pool.Exec(ctx, `UPDATE scene_event_receipt SET `+tc.change+` WHERE id=$1`, a.ReceiptID); err != nil {
			t.Fatal(err)
		}
		if _, _, err := admitHumanResponse(t, f, a); !errors.Is(err, tc.want) {
			t.Fatalf("inadmissible receipt %s accepted: %v", tc.change, err)
		}
	}
}

func TestHumanResponsePrioritizesWakeAndOldSupportDoesNotClaim(t *testing.T) {
	f := humanResponseDatabase(t)
	ctx := context.Background()
	task, _ := originTask(t, f)
	completeJob(t, f, claim(t, f))
	wake, err := f.store.AdmitTaskWake(ctx, readyHost, wakeAdmission(f, task, "human-response-priority"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.pool.Exec(ctx, `UPDATE employee_scene_job SET created_at=now()-interval '1 minute',available_at=now()-interval '1 minute' WHERE id=$1`, wake.JobID); err != nil {
		t.Fatal(err)
	}
	newReceipt := secondReceipt(t, f)
	a := humanResponseAdmission(f)
	a.ReceiptID = newReceipt.Item.ReceiptID
	human, _, err := admitHumanResponse(t, f, a)
	if err != nil {
		t.Fatal(err)
	}
	old := Support{Kinds: []string{KindMessage, KindTaskWake}, WakeKinds: TaskWakeKinds(), WakeSchemas: []string{"1"}}
	if _, err := f.store.ClaimSupported(ctx, old); !errors.Is(err, ErrNoJob) {
		t.Fatalf("old support claimed human response or bypassed it: %v", err)
	}
	first := claim(t, f)
	if first.ID != human.ID || first.Kind != KindHumanResponse {
		t.Fatalf("Task wake preceded human answer: %+v", first)
	}
	if err := f.store.Retry(ctx, first, "temporary"); err != nil {
		t.Fatal(err)
	}
	if _, err := f.store.Claim(ctx); !errors.Is(err, ErrNoJob) {
		t.Fatalf("wake bypassed human answer backoff: %v", err)
	}
	if _, err := f.pool.Exec(ctx, `UPDATE employee_scene_job SET available_at=now() WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	completeJob(t, f, claim(t, f))
	if next := claim(t, f); next.ID != wake.JobID {
		t.Fatalf("wake not released after answer: %+v", next)
	}
}

func TestHumanResponseDoesNotReinterpretOrJoinMessageWindow(t *testing.T) {
	f := humanResponseDatabase(t)
	ctx := context.Background()
	message := admit(t, f)
	a := humanResponseAdmission(f)
	if _, _, err := admitHumanResponse(t, f, a); !errors.Is(err, ErrConflict) {
		t.Fatalf("message receipt reinterpreted as an answer: %v", err)
	}
	next := secondReceipt(t, f)
	a.ReceiptID = next.Item.ReceiptID
	human, _, err := admitHumanResponse(t, f, a)
	if err != nil {
		t.Fatal(err)
	}
	if human.ID == message.JobID {
		t.Fatal("typed answer joined a message window")
	}
	if n := countRows(t, f, `SELECT count(*) FROM employee_scene_job WHERE kind='message' AND jsonb_array_length(items)=1 AND message_count=1`); n != 1 {
		t.Fatalf("answer changed existing message window: %d", n)
	}
	if n := countRows(t, f, `SELECT count(*) FROM employee_task`); n != 0 {
		t.Fatalf("foreground answer unexpectedly created %d Tasks", n)
	}
	if n := countRows(t, f, `SELECT count(*) FROM employee_scene_job WHERE kind='human_response' AND jsonb_array_length(items)=1 AND message_count=0`); n != 1 {
		t.Fatalf("answer is not a separate reference job: %d", n)
	}
	if _, err = f.store.Admit(ctx, next); !errors.Is(err, ErrConflict) {
		t.Fatalf("answer receipt reinterpreted as message: %v", err)
	}
}
