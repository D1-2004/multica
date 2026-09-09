package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestASBCapacityPacingCreatesAcrossReplicasWithoutCapacityQueue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	pool := newTaskClaimRacePool(t)
	rdb := newASBCapacityRedisTestClient(t)
	var mu sync.Mutex
	var created []time.Time
	var quotaCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/quotas":
			quotaCalls.Add(1)
			_, _ = io.WriteString(w, `[{"quota":30,"usage":3}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			mu.Lock()
			created = append(created, time.Now())
			id := len(created)
			mu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"id":"paced-%d","status":{"state":"Pending"}}`, id)
		default:
			t.Errorf("free capacity must not inspect or reclaim sandboxes: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	defer server.Close()
	client := newASBCapacityGateTestTenant(t, rdb)
	client.baseURL, _ = normalizeASBBaseURL(server.URL)
	peer := *client
	peer.CapacityGate = NewASBCapacityGate(newASBCapacityRedisTestClient(t))
	// Both requests arrive before the remaining interval expires. Neither may
	// be returned to the durable full-capacity queue, and their actual creates
	// must still obey the shared interval across replicas.
	if err := rdb.Set(ctx, client.capacityGateKeys()[0], "available", 100*time.Millisecond).Err(); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 2)
	start := time.Now()
	for _, node := range []*ASBClient{client, &peer} {
		go func() {
			conn, err := pool.Acquire(ctx)
			if err != nil {
				results <- err
				return
			}
			defer conn.Release()
			sandbox, err := createASBSandboxWithCapacityOnConnection(ctx, db.New(conn), &ASBRuntimeClientProvider{}, node,
				util.MustParseUUID(uuid.NewString()), pgtype.UUID{}, conn, ASBCreateSandboxInput{
					ImageURI: "registry.example/runtime:pacing", TimeoutSeconds: 60,
					ResourceCPU: "1", ResourceMemory: "1Gi", Entrypoint: []string{"sleep", "infinity"},
				})
			if err == nil && sandbox == nil {
				err = errors.New("create returned no sandbox")
			}
			results <- err
		}()
	}
	for range 2 {
		if err := <-results; err != nil {
			t.Errorf("available capacity did not continue in the current launch: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(created) != 2 || quotaCalls.Load() != 2 {
		t.Fatalf("creates=%d quota checks=%d, want two successful launches", len(created), quotaCalls.Load())
	}
	if gap := created[1].Sub(created[0]); gap < asbCapacityCheckInterval-100*time.Millisecond {
		t.Fatalf("shared create interval was bypassed: %s", gap)
	}
	if elapsed := time.Since(start); elapsed >= 10*time.Second {
		t.Fatalf("short pacing became a long capacity wait: %s", elapsed)
	}
}

func TestASBCapacityPacingCancellationDoesNotProbeOrReportFull(t *testing.T) {
	pool := newTaskClaimRacePool(t)
	rdb := newASBCapacityRedisTestClient(t)
	client := newASBCapacityGateTestTenant(t, rdb)
	var calls atomic.Int32
	client.lifecycleClient = &http.Client{Transport: a2aRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected upstream request")
	})}
	if err := rdb.Set(context.Background(), client.capacityGateKeys()[0], "checking", 5*time.Second).Err(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()
	_, err = createASBSandboxWithCapacityOnConnection(ctx, db.New(conn), &ASBRuntimeClientProvider{}, client,
		util.MustParseUUID(uuid.NewString()), pgtype.UUID{}, conn, ASBCreateSandboxInput{})
	if !errors.Is(err, context.DeadlineExceeded) || errors.Is(err, ErrASBCapacityUnavailable) {
		t.Fatalf("cancelled pacing must preserve cancellation instead of claiming quota full: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled launch contacted ASB")
	}
}
