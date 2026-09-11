package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/events"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/redis/go-redis/v9"
)

func newASBCapacityRedisTestClient(t *testing.T) *redis.Client {
	t.Helper()
	raw := os.Getenv("REDIS_TEST_URL")
	if raw == "" {
		t.Skip("REDIS_TEST_URL not set")
	}
	opts, err := redis.ParseURL(raw)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(opts)
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(context.Background()).Err(); err != nil {
		t.Fatal(err)
	}
	return client
}

func newASBCapacityGateTestTenant(t *testing.T, rdb *redis.Client) *ASBClient {
	t.Helper()
	client, err := NewASBClient(ASBClientConfig{BaseURL: "https://sandbox.example", APIKey: uuid.NewString()})
	if err != nil {
		t.Fatal(err)
	}
	client.CapacityGate = NewASBCapacityGate(rdb)
	t.Cleanup(func() { rdb.Del(context.Background(), client.capacityGateKeys()...) })
	return client
}

func TestASBCapacityGateDoesNotPaceHealthyReplicasAndSharesFullResult(t *testing.T) {
	ctx := context.Background()
	rdb := newASBCapacityRedisTestClient(t)
	nodeA := newASBCapacityGateTestTenant(t, rdb)
	nodeB := *nodeA
	nodeB.CapacityGate = NewASBCapacityGate(newASBCapacityRedisTestClient(t))
	var admitted atomic.Int32
	var wg sync.WaitGroup
	for i := range 32 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			client := nodeA
			if i%2 == 0 {
				client = &nodeB
			}
			delay, err := client.CapacityGate.admit(ctx, client)
			if err != nil {
				t.Error(err)
				return
			}
			if delay == 0 {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if admitted.Load() != 32 {
		t.Fatalf("healthy gate delayed %d callers; reservations are serialized by PostgreSQL", 32-admitted.Load())
	}
	if _, err := nodeA.CapacityGate.record(ctx, nodeA, ErrASBCapacityUnavailable); err != nil {
		t.Fatal(err)
	}
	delay, err := nodeB.CapacityGate.admit(ctx, &nodeB)
	if err != nil || delay < 29*time.Second {
		t.Fatalf("peer did not reuse full result: %s, %v", delay, err)
	}
	if delay, err := nodeB.CapacityGate.throttleDelay(ctx, &nodeB); err != nil || delay != 0 {
		t.Fatalf("full capacity incorrectly blocked warm reuse: %s %v", delay, err)
	}
	before := rdb.PTTL(ctx, nodeA.capacityGateKeys()[0]).Val()
	for range 10 {
		_, _ = nodeB.CapacityGate.admit(ctx, &nodeB)
	}
	if after := rdb.PTTL(ctx, nodeA.capacityGateKeys()[0]).Val(); after > before {
		t.Fatal("cache hits extended the cooldown; queued tasks could starve forever")
	}
	other := newASBCapacityGateTestTenant(t, rdb)
	if delay, err := other.CapacityGate.admit(ctx, other); err != nil || delay != 0 {
		t.Fatalf("another tenant was blocked: %s, %v", delay, err)
	}
}

func TestASBCapacityGateIgnoresOldPositiveMarkers(t *testing.T) {
	ctx := context.Background()
	rdb := newASBCapacityRedisTestClient(t)
	client := newASBCapacityGateTestTenant(t, rdb)
	for _, verdict := range []string{"checking", "available"} {
		t.Run(verdict, func(t *testing.T) {
			if err := rdb.Set(ctx, client.capacityGateKeys()[0], verdict, 5*time.Second).Err(); err != nil {
				t.Fatal(err)
			}
			if delay, err := client.CapacityGate.admit(ctx, client); err != nil || delay != 0 {
				t.Fatalf("old positive marker delayed a tenant lock holder: %s, %v", delay, err)
			}
			if delay, err := client.CapacityGate.record(ctx, client, nil); err != nil || delay != 0 {
				t.Fatalf("healthy result added a new interval: %s, %v", delay, err)
			}
			if rdb.Exists(ctx, client.capacityGateKeys()[0]).Val() != 0 {
				t.Fatal("healthy result retained a cooldown")
			}
		})
	}
}

func TestASBCapacityGateSuccessCannotClearConcurrentNegativeVerdict(t *testing.T) {
	ctx := context.Background()
	rdb := newASBCapacityRedisTestClient(t)
	client := newASBCapacityGateTestTenant(t, rdb)
	for _, verdict := range []string{"full", "throttled"} {
		t.Run(verdict, func(t *testing.T) {
			if err := rdb.Set(ctx, client.capacityGateKeys()[0], verdict, 30*time.Second).Err(); err != nil {
				t.Fatal(err)
			}
			if delay, err := client.CapacityGate.record(ctx, client, nil); err != nil || delay < 29*time.Second {
				t.Fatalf("success shortened a concurrent negative verdict: %s, %v", delay, err)
			}
			if delay, err := client.CapacityGate.admit(ctx, client); err != nil || delay < 29*time.Second {
				t.Fatalf("negative verdict was bypassed: %s, %v", delay, err)
			}
		})
	}
}

func TestASBCapacityGateBackoffAndRetryAfter(t *testing.T) {
	ctx := context.Background()
	rdb := newASBCapacityRedisTestClient(t)
	client := newASBCapacityGateTestTenant(t, rdb)
	throttle := &ASBHTTPError{StatusCode: 429}
	for _, want := range []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
		rdb.Del(ctx, client.capacityGateKeys()[0])
		got, err := client.CapacityGate.record(ctx, client, throttle)
		if err != nil || got != want {
			t.Fatalf("backoff = %s, %v; want %s", got, err, want)
		}
	}
	throttle.RetryAfter = 15 * time.Minute
	got, err := client.CapacityGate.record(ctx, client, throttle)
	if err != nil || got != 15*time.Minute {
		t.Fatalf("Retry-After ignored: %s %v", got, err)
	}
	if delay, err := client.CapacityGate.throttleDelay(ctx, client); err != nil || delay < 14*time.Minute {
		t.Fatalf("warm reuse bypassed rate-limit cooldown: %s %v", delay, err)
	}
	// A shorter later verdict must not undo an already advertised cooldown.
	got, err = client.CapacityGate.record(ctx, client, nil)
	if err != nil || got < 14*time.Minute {
		t.Fatalf("cooldown shortened: %s %v", got, err)
	}
	// Simulate expiry without waiting minutes; a successful probe resets the streak.
	rdb.Del(ctx, client.capacityGateKeys()[0])
	if _, err := client.CapacityGate.record(ctx, client, nil); err != nil {
		t.Fatal(err)
	}
	throttle.RetryAfter = 0
	got, err = client.CapacityGate.record(ctx, client, throttle)
	if err != nil || got != 30*time.Second {
		t.Fatalf("streak not reset: %s %v", got, err)
	}
}

func TestASBRetryAfterParsing(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, tt := range []struct {
		input string
		want  time.Duration
	}{
		{"120", 2 * time.Minute}, {now.Add(time.Minute).Format(http.TimeFormat), time.Minute},
		{"-1", 0}, {"nonsense", 0}, {"", 0}, {now.Add(-time.Minute).Format(http.TimeFormat), 0},
		{"9223372036854775807", 24 * time.Hour},
	} {
		if got := parseASBRetryAfter(tt.input, now); got != tt.want {
			t.Fatalf("parse %q = %s, want %s", tt.input, got, tt.want)
		}
	}
}

func TestASBCapacityGate429WaitsAcrossReplicasThenResumes(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	rdb := newASBCapacityRedisTestClient(t)
	var calls atomic.Int32
	var healthy atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if !healthy.Load() {
			if r.URL.Path == "/v1/sandboxes/quotas" {
				_, _ = io.WriteString(w, `[{"quota":10,"usage":10}]`)
				return
			}
			if r.URL.Query().Get("state") == "Pending" {
				_, _ = io.WriteString(w, `{"items":[],"pagination":{"page":1,"pageSize":100,"hasNextPage":false}}`)
				return
			}
			w.Header().Set("Retry-After", "90")
			w.WriteHeader(429)
			_, _ = io.WriteString(w, `{"code":"TOO_MANY_REQUESTS","message":"nexa"}`)
			return
		}
		switch r.URL.Path {
		case "/v1/sandboxes/quotas":
			_, _ = io.WriteString(w, `[{"quota":10,"usage":0}]`)
		case "/v1/sandboxes":
			w.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(w, `{"id":"resumed","status":{"state":"Running"},"createdAt":"2026-09-07T00:00:00Z"}`)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()
	client := newASBCapacityGateTestTenant(t, rdb)
	client.baseURL, _ = normalizeASBBaseURL(server.URL)
	nodeB := *client
	nodeB.CapacityGate = NewASBCapacityGate(newASBCapacityRedisTestClient(t))
	taskID, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
	task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(bytes.Repeat([]byte{0x52}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := box.Seal([]byte(client.apiKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.UpsertASBRuntimeCredential(ctx, db.UpsertASBRuntimeCredentialParams{
		RuntimeID: task.RuntimeID, ApiKeyEncrypted: encrypted, ApiKeyHint: "test",
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Exec(context.Background(), `DELETE FROM asb_runtime_credential WHERE runtime_id=$1`, task.RuntimeID)
		pool.Exec(context.Background(), `DELETE FROM agent_task_runtime_start_attempt WHERE task_id=$1`, task.ID)
	})
	credentials := &ASBRuntimeClientProvider{Store: queries, Secrets: box, CapacityGate: client.CapacityGate}
	input := ASBCreateSandboxInput{ImageURI: "registry.example/runtime:gate-test", TimeoutSeconds: 60, ResourceCPU: "1", ResourceMemory: "1Gi", Entrypoint: []string{"sleep infinity"}}
	launch := func(c *ASBClient) error {
		conn, err := pool.Acquire(ctx)
		if err != nil {
			return err
		}
		defer conn.Release()
		_, err = createASBSandboxWithCapacityOnConnection(ctx, db.New(conn), credentials, c, task.RuntimeID, pgtype.UUID{}, conn, input)
		return err
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_runtime SET provider='hermes', metadata=$2 WHERE id=$1`, task.RuntimeID,
		[]byte(`{"kind":"cloud-sandbox","sandbox_backend":"asb","provider":"hermes","artifact_kind":"oci_image","artifact_channel":"stable","artifact_ref":"registry.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status='queued', dispatched_at=NULL WHERE id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	task, err = queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	cfg := ASBConfig{Enabled: true, APIURL: server.URL, ServerURL: server.URL, LLMBaseURL: server.URL, LLMAPIKey: "test", LLMModels: []string{"test"}, TimeoutSeconds: 60, ReadyTimeout: time.Second, CommandReadyTimeout: time.Second, WireGuardReadyTimeout: time.Second, ResourceCPU: "1", ResourceMemory: "1Gi", WireGuardCredentials: "test"}
	credentials.Config = cfg
	tasks := NewTaskService(queries, pool, nil, events.New())
	common := &FCE2BLauncher{Pool: pool, Queries: queries, Tasks: tasks}
	launcher := NewASBLauncher(queries, tasks, common, cfg, nil, credentials)
	launcher.SetPool(pool)
	if err := launcher.LaunchTask(ctx, task); err != nil {
		t.Fatalf("429 ended launch with a terminal error: %v", err)
	}
	current, err := queries.GetAgentTask(ctx, task.ID)
	if err != nil || current.Status != "queued" || current.Error.Valid {
		t.Fatalf("429 did not preserve queued task: status=%s error_valid=%v err=%v", current.Status, current.Error.Valid, err)
	}
	var status, code string
	if err := pool.QueryRow(ctx, `SELECT status,error_code FROM agent_task_runtime_start_attempt WHERE task_id=$1 ORDER BY created_at DESC LIMIT 1`, task.ID).Scan(&status, &code); err != nil {
		t.Fatal(err)
	}
	if status != "blocked" || code != asbCapacityWaitingErrorCode {
		t.Fatalf("429 attempt = %s %s", status, code)
	}
	if rdb.Exists(ctx, client.capacityGateKeys()[0]).Val() != 1 {
		t.Fatal("launcher did not share its capacity cooldown")
	}
	if err := launch(&nodeB); !errors.Is(err, ErrASBCapacityUnavailable) {
		t.Fatalf("peer not deferred: %v", err)
	}
	if got := calls.Load(); got != 3 {
		t.Fatalf("peer repeated upstream calls during cooldown: %d", got)
	}
	if ttl := rdb.PTTL(ctx, client.capacityGateKeys()[0]).Val(); ttl < 89*time.Second {
		t.Fatalf("Retry-After not shared: %s", ttl)
	}
	healthy.Store(true)
	rdb.Del(ctx, client.capacityGateKeys()[0])
	if err := launch(&nodeB); err != nil {
		t.Fatalf("could not resume: %v", err)
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("resumed requests = %d, want quota + create after quota and two list calls", got)
	}
	if rdb.Exists(ctx, client.capacityGateKeys()[1]).Val() != 0 {
		t.Fatal("success did not clear throttle streak")
	}
	// Losing Redis must not turn every arriving task into an upstream probe.
	nodeB.CapacityGate = NewASBCapacityGate(nil)
	if err := launch(&nodeB); !errors.Is(err, ErrASBCapacityUnavailable) {
		t.Fatalf("missing Redis did not defer: %v", err)
	}
	if got := calls.Load(); got != 5 {
		t.Fatalf("upstream contacted without coordination: %d", got)
	}
}

func TestASBCapacityGateKeysDoNotExposeCredentials(t *testing.T) {
	client, err := NewASBClient(ASBClientConfig{BaseURL: "https://sandbox.example", APIKey: "secret-example-credential"})
	if err != nil {
		t.Fatal(err)
	}
	keys := client.capacityGateKeys()
	for _, key := range keys {
		if strings.Contains(key, client.apiKey) {
			t.Fatal("credential exposed in Redis key")
		}
	}
	if strings.Split(keys[0], "}")[0] != strings.Split(keys[1], "}")[0] {
		t.Fatal("Redis Cluster hash slots differ")
	}
}
