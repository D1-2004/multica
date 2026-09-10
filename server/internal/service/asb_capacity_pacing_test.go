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

func TestASBCapacityHealthyCreatesAcrossReplicasWithoutArtificialDelay(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	pool := newTaskClaimRacePool(t)
	rdb := newASBCapacityRedisTestClient(t)
	var mu sync.Mutex
	var created []time.Time
	var quotaCalls atomic.Int32
	var creating atomic.Int32
	var maxCreating atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/quotas":
			quotaCalls.Add(1)
			_, _ = io.WriteString(w, `[{"quota":30,"usage":3}]`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
			concurrent := creating.Add(1)
			defer creating.Add(-1)
			for previous := maxCreating.Load(); concurrent > previous; previous = maxCreating.Load() {
				if maxCreating.CompareAndSwap(previous, concurrent) {
					break
				}
			}
			time.Sleep(20 * time.Millisecond)
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
	// A healthy result left by an older replica must not add five seconds to
	// each subsequent launch. PostgreSQL still serializes quota reservations.
	if err := rdb.Set(ctx, client.capacityGateKeys()[0], "available", 5*time.Second).Err(); err != nil {
		t.Fatal(err)
	}
	results := make(chan error, 3)
	start := time.Now()
	for _, node := range []*ASBClient{client, &peer, client} {
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
	for range 3 {
		if err := <-results; err != nil {
			t.Errorf("available capacity did not continue in the current launch: %v", err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if len(created) != 3 || quotaCalls.Load() != 3 {
		t.Fatalf("creates=%d quota checks=%d, want three successful launches", len(created), quotaCalls.Load())
	}
	if maxCreating.Load() != 1 {
		t.Fatalf("tenant capacity lock allowed %d simultaneous reservations", maxCreating.Load())
	}
	if elapsed := time.Since(start); elapsed >= 3*time.Second {
		t.Fatalf("healthy launches retained an artificial delay: %s", elapsed)
	}
}

func TestASBCapacityTenantLockCancellationDoesNotProbeOrReportFull(t *testing.T) {
	pool := newTaskClaimRacePool(t)
	rdb := newASBCapacityRedisTestClient(t)
	client := newASBCapacityGateTestTenant(t, rdb)
	var calls atomic.Int32
	client.lifecycleClient = &http.Client{Transport: a2aRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected upstream request")
	})}
	holder, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer holder.Release()
	release, err := lockASBTenantCapacityOnConnection(context.Background(), holder, client)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
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
		t.Fatalf("cancelled lock wait must preserve cancellation instead of claiming quota full: %v", err)
	}
	if calls.Load() != 0 {
		t.Fatal("cancelled launch contacted ASB")
	}
}
