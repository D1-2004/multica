package agentmessagerouter

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestNativeTargetIdentityHasRouterTargetForm(t *testing.T) {
	pattern := regexp.MustCompile(`^router-target:v1:sha256:[a-f0-9]{64}$`)
	if !pattern.MatchString(NativeTargetIdentity()) {
		t.Fatalf("native target %q does not have the Router target form", NativeTargetIdentity())
	}
	router, err := NewClient(ClientConfig{BaseURL: "https://router.example.com", ServiceCredential: "secret"})
	if err != nil {
		t.Fatal(err)
	}
	if router.TargetIdentity() == NativeTargetIdentity() {
		t.Fatal("native target collides with a Router target")
	}
}

func TestNativeDispatchTaskIDAndCallbackNamespace(t *testing.T) {
	id := NativeDispatchTaskID("agent", "org", "cid", "msg")
	if id != NativeDispatchTaskID("agent", "org", "cid", "msg") {
		t.Fatal("task id is not stable for one message")
	}
	if id == NativeDispatchTaskID("agent", "org", "cid", "msg-2") || id == NativeDispatchTaskID("agent-2", "org", "cid", "msg") {
		t.Fatal("task id does not separate messages or agents")
	}
	for _, suffix := range []string{"execution-result", "execution-update", "response-receipt"} {
		path := "/api/v1/dispatch-tasks/" + id + "/" + suffix
		if !IsNativeDispatchCallback(path) || !executionCallbackPathMatches(path) && suffix != "response-receipt" {
			t.Fatalf("%s is not a native callback", path)
		}
	}
	for _, path := range []string{
		"/api/v1/dispatch-tasks/router-task/execution-result",
		"/api/v1/dispatch-tasks/" + strings.ToUpper(id) + "/execution-result",
		"/api/v1/dispatch-tasks/dwsn-short/execution-result",
		"/api/v1/dispatch-tasks/" + id + "/llm-traces",
	} {
		if IsNativeDispatchCallback(path) {
			t.Fatalf("%s accepted as a native callback", path)
		}
	}
}

func executionCallbackPathMatches(path string) bool {
	return executionResultCallbackPattern.MatchString(path) || executionUpdateCallbackPattern.MatchString(path)
}

func TestNativeCallbackClientAcknowledgesOnlyWhatIsHandled(t *testing.T) {
	base := "/api/v1/dispatch-tasks/" + NativeDispatchTaskID("a", "o", "c", "m")
	result, update := base+"/execution-result", base+"/execution-update"
	lookupErr := errors.New("database unavailable")
	quiet := false
	for _, tt := range []struct {
		name      string
		found     bool
		lookup    error
		path      string
		message   string
		silent    bool
		update    bool
		permanent string
		transient bool
	}{
		{name: "result handed to the managed outbox", found: true, path: result, message: "done"},
		{name: "update handed to the managed outbox", found: true, path: update, message: "on it", update: true},
		{name: "silence needs no route", path: result},
		{name: "explicit no-reply needs no route", path: result, message: "done", silent: true},
		{name: "reply without a route is dead-lettered", path: result, message: "done", permanent: "native_route_missing"},
		{name: "update without a route is dead-lettered", path: update, message: "on it", update: true, permanent: "native_route_missing"},
		{name: "route lookup failure retries", lookup: lookupErr, path: result, message: "done", transient: true},
		{name: "Router callback is refused", path: "/api/v1/dispatch-tasks/router-task/execution-result", message: "done", permanent: "invalid_native_callback"},
		{name: "result path is not an update", path: result, message: "on it", update: true, permanent: "invalid_native_callback"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := NewNativeCallbackClient(func(_ context.Context, path string) (bool, error) {
				if path != tt.path {
					t.Errorf("lookup path = %q, want %q", path, tt.path)
				}
				return tt.found, tt.lookup
			})
			var delivery *DWSDelivery
			var err error
			if tt.update {
				delivery, err = client.SubmitExecutionUpdate(context.Background(), tt.path, ExecutionUpdateRequest{ResultMessage: tt.message})
			} else {
				request := ExecutionResultRequest{ResultMessage: tt.message, ExecutionStatus: "completed"}
				if tt.silent {
					request.ShouldReply = &quiet
				}
				delivery, err = client.SubmitExecutionResult(context.Background(), tt.path, request)
			}
			if delivery != nil {
				t.Fatal("native callbacks never return a DWS delivery plan")
			}
			var permanent *dwsDeliveryPermanentError
			switch {
			case tt.permanent != "":
				if !errors.As(err, &permanent) || permanent.code != tt.permanent {
					t.Fatalf("error = %v, want permanent %s", err, tt.permanent)
				}
			case tt.transient:
				if err == nil || errors.As(err, &permanent) {
					t.Fatalf("error = %v, want a retryable error", err)
				}
			case err != nil:
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCompletionNotifiersSkipMissingWorkers(t *testing.T) {
	worker := NewCompletionWorker(nil, NewNativeCallbackClient(nil), nil)
	CompletionNotifiers{nil, worker}.NotifyTaskCompletion()
	CompletionNotifiers{worker, nil}.NotifyTaskExecutionUpdate()
	select {
	case <-worker.notify:
	default:
		t.Fatal("worker was not woken")
	}
	if worker.TargetIdentity() != NativeTargetIdentity() {
		t.Fatalf("worker target = %q", worker.TargetIdentity())
	}
}

// The native worker drains only native-target rows and never calls out: a
// handled reply is delivered, an unhandled one is dead-lettered.
func TestNativeCompletionWorkerDrainsNativeCallbacks(t *testing.T) {
	pool := taskCompletionTestPool(t)
	queries := db.New(pool)
	for _, tt := range []struct {
		name    string
		found   bool
		message string
		want    string
	}{
		{name: "handled reply", found: true, message: "final reply", want: "delivered"},
		{name: "unhandled reply", message: "final reply", want: "dead_letter"},
		{name: "silence", message: "", want: "delivered"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			callback := "/api/v1/dispatch-tasks/" + NativeDispatchTaskID(uuid.NewString(), "org", "cid", "msg") + "/execution-result"
			row, err := queries.EnqueueTaskCompletion(context.Background(), db.EnqueueTaskCompletionParams{
				RootTaskID:      util.MustParseUUID(uuid.NewString()),
				TerminalTaskID:  util.MustParseUUID(uuid.NewString()),
				CallbackUrl:     callback,
				TargetIdentity:  NativeTargetIdentity(),
				RequestID:       "native-worker-test:" + uuid.NewString(),
				AgentID:         util.MustParseUUID(uuid.NewString()),
				ExecutionStatus: "completed",
				ResultMessage:   tt.message,
				ExternalSessionID: pgtype.Text{
					String: "session-1", Valid: true,
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				pool.Exec(context.Background(), `DELETE FROM task_completion_outbox WHERE id = $1`, row.ID)
			})
			looked := 0
			worker := NewCompletionWorker(queries, NewNativeCallbackClient(func(_ context.Context, path string) (bool, error) {
				looked++
				if path != callback {
					t.Errorf("lookup path = %q", path)
				}
				return tt.found, nil
			}), nil)
			deadline := time.Now().Add(5 * time.Second)
			var status string
			for time.Now().Before(deadline) {
				if _, err := worker.ProcessNext(context.Background()); err != nil {
					t.Fatal(err)
				}
				if err := pool.QueryRow(context.Background(), `SELECT status FROM task_completion_outbox WHERE id = $1`, row.ID).Scan(&status); err != nil {
					t.Fatal(err)
				}
				if status != "queued" {
					break
				}
			}
			if status != tt.want || looked == 0 {
				t.Fatalf("status = %q lookups = %d, want %q", status, looked, tt.want)
			}
		})
	}
}
