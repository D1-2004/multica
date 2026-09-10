package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type asbQuotaRecoveryTestLauncher func(context.Context, db.AgentTaskQueue) error

func (launch asbQuotaRecoveryTestLauncher) LaunchTask(ctx context.Context, task db.AgentTaskQueue) error {
	return launch(ctx, task)
}

func TestASBCapacityWaiterResumesWhenRegionalQuotaIncreases(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	rdb := newASBCapacityRedisTestClient(t)
	tasks := NewTaskService(queries, pool, nil, events.New())
	taskID, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
	task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='queued', dispatched_at=NULL WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(bytes.Repeat([]byte{0x43}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	client := newASBCapacityGateTestTenant(t, rdb)
	sealed, err := box.Seal([]byte(client.apiKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.UpsertASBRuntimeCredential(ctx, db.UpsertASBRuntimeCredentialParams{
		RuntimeID: task.RuntimeID, ApiKeyEncrypted: sealed, ApiKeyHint: "test",
	}); err != nil {
		t.Fatal(err)
	}
	credentials := &ASBRuntimeClientProvider{Store: queries, Secrets: box}
	var expanded atomic.Bool
	var creates atomic.Int32
	regional := newASBRegionalTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/quotas":
			quota := 30
			if expanded.Load() {
				quota = 50
			}
			fmt.Fprintf(w, `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":11,"usage":11},{"networkZone":"ALITest","region":"cn-hangzhou","quota":%d,"usage":30}]`, quota)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes":
			io.WriteString(w, `{"items":[],"pagination":{"page":1,"pageSize":100,"hasNextPage":false}}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			if !expanded.Load() || r.URL.Host != "sandbox-cn-hangzhou.aone.alibaba-inc.com" {
				t.Error("create did not select the newly available region")
			}
			creates.Add(1)
			w.WriteHeader(http.StatusAccepted)
			io.WriteString(w, `{"id":"quota-recovered","status":{"state":"Running"}}`)
		default:
			t.Errorf("unexpected capacity request: %s %s", r.Method, r.URL.Path)
		}
	})
	client.baseURL = regional.baseURL
	client.lifecycleClient = regional.lifecycleClient
	launch := func(ctx context.Context, task db.AgentTaskQueue) (*ASBSandbox, error) {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return nil, err
		}
		defer conn.Release()
		return createASBSandboxWithCapacityOnConnection(ctx, db.New(conn), credentials, client,
			task.RuntimeID, task.ID, conn, ASBCreateSandboxInput{
				ImageURI: "registry.example/runtime:quota-recovery", TimeoutSeconds: 60,
				ResourceCPU: "1", ResourceMemory: "1Gi", Entrypoint: []string{"sleep", "infinity"},
			})
	}
	attempt, err := tasks.BeginRuntimeStartAttempt(ctx, task, SandboxBackendASB, RuntimeStartProtocolHTTPJSONV1)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := launch(ctx, task); !errors.Is(err, ErrASBCapacityUnavailable) {
		t.Fatalf("expected initial full capacity: %v", err)
	}
	if waiting, err := tasks.MarkRuntimeStartCapacityWaiting(ctx, attempt); err != nil || !waiting {
		t.Fatalf("persist capacity wait: %v", err)
	}
	if creates.Load() != 0 {
		t.Fatal("created while quota full")
	}
	// Simulate quota expansion and expiration of the bounded full-capacity
	// cooldown. The queued task is recovered by the regular worker pass without
	// enqueueing a new task or depending on a terminal-task notification.
	expanded.Store(true)
	if _, err := pool.Exec(ctx, `UPDATE agent_task_runtime_start_attempt SET finished_at=now()-interval '2 minutes', updated_at=now()-interval '2 minutes' WHERE id=$1`, attempt.ID); err != nil {
		t.Fatal(err)
	}
	if err := rdb.Del(ctx, client.capacityGateKeys()[0]).Err(); err != nil {
		t.Fatal(err)
	}
	result := make(chan error, 1)
	tasks.RuntimeLauncher = asbQuotaRecoveryTestLauncher(func(ctx context.Context, queued db.AgentTaskQueue) error {
		sandbox, err := launch(ctx, queued)
		if err == nil && (queued.ID != task.ID || sandbox.ID != "quota-recovered") {
			err = fmt.Errorf("wrong task or sandbox resumed")
		}
		result <- err
		return err
	})
	tasks.runtimeLaunchLeases = newPostgresTaskRuntimeLaunchLeaseStore(queries)
	waiter := &ASBLauncher{Queries: queries, Tasks: tasks, Credentials: credentials}
	if scheduled, err := waiter.retryCapacityWaitingTasks(ctx); err != nil || scheduled != 1 {
		t.Fatalf("recovery pass scheduled=%d err=%v", scheduled, err)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("queued task did not create after quota expansion")
	}
	select {
	case <-waiter.CapacityWait.wakeups:
	case <-time.After(time.Second):
		t.Fatal("recovered launch did not finish")
	}
	if creates.Load() != 1 {
		t.Fatalf("creates=%d, want exactly one", creates.Load())
	}
}

func TestASBCapacityReclaimRetriesThenTriesNextIdleCandidate(t *testing.T) {
	for _, test := range []struct {
		name        string
		tracked     int
		waitFailure bool
		allFail     bool
	}{
		{name: "tracked delete failure", tracked: 2},
		{name: "tracked release observation failure", tracked: 2, waitFailure: true},
		{name: "untracked delete failure"},
		{name: "tracked then untracked", tracked: 1},
		{name: "every candidate fails", tracked: 1, allFail: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			pool := newTaskClaimRacePool(t)
			queries := db.New(pool)
			taskID, _, workspaceID := dispatchedCommentTaskFixture(t, ctx, pool)
			task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed', completed_at = now() WHERE id = $1`, task.ID); err != nil {
				t.Fatal(err)
			}
			box, err := secretbox.New(bytes.Repeat([]byte{0x61}, secretbox.KeySize))
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := box.Seal([]byte(testASBAPIKey))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := queries.UpsertASBRuntimeCredential(ctx, db.UpsertASBRuntimeCredentialParams{
				RuntimeID: task.RuntimeID, ApiKeyEncrypted: sealed, ApiKeyHint: "-key",
			}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < test.tracked; i++ {
				if _, err := queries.UpsertCloudSandboxSession(ctx, db.UpsertCloudSandboxSessionParams{
					WorkspaceID: util.MustParseUUID(workspaceID), RuntimeID: task.RuntimeID,
					ScopeType: fcE2BScopeTypeIssue, ScopeID: util.MustParseUUID(uuid.NewString()),
					SandboxID: fmt.Sprintf("idle-%d", i), ArtifactRef: "registry.example/runtime:retry",
					ExpiresAt:      pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
					SandboxBackend: string(SandboxBackendASB), IdentityFingerprint: asbUnboundIdentityFingerprint,
				}); err != nil {
					t.Fatal(err)
				}
			}
			var deletes []string
			deleted := false
			client := newASBRegionalTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/quotas":
					usage := 2
					if deleted {
						usage = 1
					}
					fmt.Fprintf(w, `[{"networkZone":"ALITest","region":"cn-hangzhou","quota":2,"usage":%d}]`, usage)
				case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes":
					items := "[]"
					if r.URL.Query().Get("state") == "Running" {
						items = fmt.Sprintf(`[
							{"id":"idle-0","status":{"state":"Running"},"createdAt":"2026-09-10T01:00:00Z","metadata":{"multica.runtime_id":"%s","multica.task_id":"%s"}},
							{"id":"idle-1","status":{"state":"Running"},"createdAt":"2026-09-10T02:00:00Z","metadata":{"multica.runtime_id":"%s","multica.task_id":"%s"}}
						]`, util.UUIDToString(task.RuntimeID), taskID, util.UUIDToString(task.RuntimeID), taskID)
					}
					fmt.Fprintf(w, `{"items":%s,"pagination":{"page":1,"pageSize":100,"totalItems":2,"totalPages":1,"hasNextPage":false}}`, items)
				case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/sandboxes/idle-"):
					id := strings.TrimPrefix(r.URL.Path, "/v1/sandboxes/")
					if id == "idle-0" && len(deletes) > 0 && test.waitFailure {
						w.WriteHeader(http.StatusServiceUnavailable)
					} else if id == "idle-1" && deleted {
						w.WriteHeader(http.StatusNotFound)
					} else {
						fmt.Fprintf(w, `{"id":%q,"status":{"state":"Running"}}`, id)
					}
				case r.Method == http.MethodDelete:
					id := strings.TrimPrefix(r.URL.Path, "/v1/sandboxes/")
					deletes = append(deletes, id)
					if test.allFail || (id == "idle-0" && !test.waitFailure) {
						w.WriteHeader(http.StatusInternalServerError)
					} else {
						deleted = id == "idle-1"
						w.WriteHeader(http.StatusNoContent)
					}
				case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
					if !deleted {
						t.Error("created before quota was released")
					}
					w.WriteHeader(http.StatusAccepted)
					io.WriteString(w, `{"id":"replacement","status":{"state":"Pending"}}`)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
				}
			})
			credentials := &ASBRuntimeClientProvider{Store: queries, Secrets: box}
			conn, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			sandbox, err := createASBSandboxWithCapacityOnConnection(ctx, db.New(conn), credentials, client,
				task.RuntimeID, pgtype.UUID{}, conn, ASBCreateSandboxInput{
					ImageURI: "registry.example/runtime:retry", TimeoutSeconds: 60,
					ResourceCPU: "1", ResourceMemory: "1Gi", Entrypoint: []string{"sleep", "infinity"},
				})
			want := []string{"idle-0", "idle-0", "idle-0", "idle-1"}
			if test.allFail {
				want = append(want, "idle-1", "idle-1")
				if !errors.Is(err, ErrASBCapacityUnavailable) {
					t.Fatalf("result %v must remain queueable", err)
				}
			} else if err != nil || sandbox == nil || sandbox.ID != "replacement" {
				t.Fatalf("next candidate did not unblock creation: sandbox=%+v err=%v", sandbox, err)
			}
			if !reflect.DeepEqual(deletes, want) {
				t.Fatalf("deletes=%v, want %v", deletes, want)
			}
		})
	}
}

func TestASBCapacityReclaimRetryBudgetAndCancellation(t *testing.T) {
	for _, test := range []struct {
		name        string
		succeedsOn  int
		cancelAfter time.Duration
		status      int
		wantDeletes int
		wantElapsed time.Duration
	}{
		{name: "stuck sandbox times out three times", wantDeletes: 3, wantElapsed: 90 * time.Second},
		{name: "third attempt releases quota", succeedsOn: 3, wantDeletes: 3, wantElapsed: 60 * time.Second},
		{name: "parent cancellation stops retries", cancelAfter: 5 * time.Second, wantDeletes: 1, wantElapsed: 5 * time.Second},
		{name: "tenant rate limit stops retries", status: http.StatusTooManyRequests, wantDeletes: 1},
		{name: "forbidden stops retries", status: http.StatusForbidden, wantDeletes: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if test.cancelAfter > 0 {
					time.AfterFunc(test.cancelAfter, cancel)
				}
				deletes := 0
				client := newASBRegionalTestClient(t, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/json")
					released := test.succeedsOn > 0 && deletes >= test.succeedsOn
					switch {
					case r.Method == http.MethodDelete:
						deletes++
						status := test.status
						if status == 0 {
							status = http.StatusNoContent
						}
						w.WriteHeader(status)
					case r.URL.Path == "/v1/sandboxes/quotas":
						usage := 1
						if released {
							usage = 0
						}
						fmt.Fprintf(w, `[{"networkZone":"ALITest","region":"cn-hangzhou","quota":1,"usage":%d}]`, usage)
					case r.URL.Path == "/v1/sandboxes/stuck":
						if released {
							w.WriteHeader(http.StatusNotFound)
						} else {
							io.WriteString(w, `{"id":"stuck","status":{"state":"Running"}}`)
						}
					default:
						t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					}
				})
				started := time.Now()
				err := reclaimASBSandboxWithRetries(ctx, client, "stuck")
				if (err == nil) != (test.succeedsOn > 0) {
					t.Fatalf("unexpected reclaim result: %v", err)
				}
				if test.cancelAfter > 0 && !errors.Is(err, context.Canceled) {
					t.Fatalf("cancellation lost: %v", err)
				}
				if deletes != test.wantDeletes || time.Since(started) != test.wantElapsed {
					t.Fatalf("deletes=%d elapsed=%s, want %d %s", deletes, time.Since(started), test.wantDeletes, test.wantElapsed)
				}
			})
		})
	}
}
