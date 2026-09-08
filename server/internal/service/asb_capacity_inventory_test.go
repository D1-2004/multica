package service

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestASBCapacityReclaimSkipsHistoricalSandboxes(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	taskID, _, workspaceID := dispatchedCommentTaskFixture(t, ctx, pool)
	task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, task.ID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM fc_e2b_sandbox_session WHERE runtime_id = $1`, task.RuntimeID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM asb_runtime_credential WHERE runtime_id = $1`, task.RuntimeID)
	})
	const historyCount = 1680
	if _, err := pool.Exec(ctx, `
		INSERT INTO fc_e2b_sandbox_session (
			workspace_id, runtime_id, scope_type, scope_id, sandbox_id, template,
			status, expires_at, last_used_at, sandbox_backend, identity_fingerprint, artifact_ref
		)
		SELECT $1, $2, 'issue', gen_random_uuid(), 'history-' || n, 'test',
			CASE WHEN n % 2 = 0 THEN 'stale' ELSE 'running' END,
			now() - interval '1 day', now() - interval '1 day', 'asb', $4, 'test'
		FROM generate_series(1, $3::int) n`, workspaceID, task.RuntimeID, historyCount, asbUnboundIdentityFingerprint); err != nil {
		t.Fatal(err)
	}
	if _, err := queries.UpsertCloudSandboxSession(ctx, db.UpsertCloudSandboxSessionParams{
		WorkspaceID:         util.MustParseUUID(workspaceID),
		RuntimeID:           task.RuntimeID,
		ScopeType:           fcE2BScopeTypeIssue,
		ScopeID:             task.IssueID,
		SandboxID:           "live-idle",
		ArtifactRef:         "test",
		ExpiresAt:           pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		SandboxBackend:      string(SandboxBackendASB),
		IdentityFingerprint: asbUnboundIdentityFingerprint,
	}); err != nil {
		t.Fatal(err)
	}
	// A stale session can still consume real quota after Runtime rotation.
	if _, err := pool.Exec(ctx, `UPDATE fc_e2b_sandbox_session SET status = 'stale' WHERE runtime_id = $1`, task.RuntimeID); err != nil {
		t.Fatal(err)
	}
	box, err := secretbox.New(bytes.Repeat([]byte{0x62}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	encrypted, err := box.Seal([]byte(testASBAPIKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.UpsertASBRuntimeCredential(ctx, db.UpsertASBRuntimeCredentialParams{
		RuntimeID: task.RuntimeID, ApiKeyEncrypted: encrypted, ApiKeyHint: "-key",
	}); err != nil {
		t.Fatal(err)
	}

	var historicalGets, liveGets, deletes atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes":
			items, count := "[]", 0
			if r.URL.Query().Get("state") == "Running" && deletes.Load() == 0 {
				items, count = `[{"id":"live-idle","status":{"state":"Running"}}]`, 1
			}
			_, _ = fmt.Fprintf(w, `{"items":%s,"pagination":{"page":1,"pageSize":100,"totalItems":%d,"totalPages":1,"hasNextPage":false}}`, items, count)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/v1/sandboxes/history-"):
			historicalGets.Add(1)
			_, _ = io.WriteString(w, `{"id":"history","status":{"state":"Terminated"}}`)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/live-idle":
			liveGets.Add(1)
			if deletes.Load() > 0 {
				w.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(w, `{"code":"NOT_FOUND"}`)
			} else {
				_, _ = io.WriteString(w, `{"id":"live-idle","status":{"state":"Running"}}`)
			}
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/sandboxes/live-idle":
			deletes.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/quotas":
			_, _ = io.WriteString(w, `[{"quota":2,"usage":1}]`)
		default:
			t.Errorf("unexpected ASB request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	client := newTestASBClient(t, server)
	credentials := &ASBRuntimeClientProvider{Store: queries, Secrets: box, Config: ASBConfig{APIURL: server.URL}}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Release()

	for _, wantReclaimed := range []bool{true, false} {
		reclaimed, err := reclaimASBSandboxForCredential(ctx, db.New(conn), credentials, client, task.RuntimeID, pgtype.UUID{}, conn)
		if err != nil || reclaimed != wantReclaimed {
			t.Fatalf("reclaimed = %t, err = %v; want %t", reclaimed, err, wantReclaimed)
		}
	}
	if historicalGets.Load() != 0 || liveGets.Load() != 2 || deletes.Load() != 1 {
		t.Fatalf("history GET=%d, live GET=%d, DELETE=%d; want 0, 2, 1", historicalGets.Load(), liveGets.Load(), deletes.Load())
	}
	var retained int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM fc_e2b_sandbox_session WHERE runtime_id = $1`, task.RuntimeID).Scan(&retained); err != nil {
		t.Fatal(err)
	}
	if retained != historyCount+1 {
		t.Fatalf("retained %d session records, want %d", retained, historyCount+1)
	}
	t.Logf("%d historical records: 0 historical GETs, 2 live GETs, 1 safe reclaim; empty inventory does no additional GETs", historyCount)
}

func TestASBCapacityCandidateFilterPreservesTaskRecheck(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	taskID, _, workspaceID := dispatchedCommentTaskFixture(t, ctx, pool)
	task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM fc_e2b_sandbox_session WHERE runtime_id = $1`, task.RuntimeID)
	})
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, task.ID); err != nil {
		t.Fatal(err)
	}
	candidate, err := queries.UpsertCloudSandboxSession(ctx, db.UpsertCloudSandboxSessionParams{
		WorkspaceID:         util.MustParseUUID(workspaceID),
		RuntimeID:           task.RuntimeID,
		ScopeType:           fcE2BScopeTypeIssue,
		ScopeID:             task.IssueID,
		SandboxID:           "recheck-live",
		ArtifactRef:         "test",
		ExpiresAt:           pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		SandboxBackend:      string(SandboxBackendASB),
		IdentityFingerprint: asbUnboundIdentityFingerprint,
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeIDs := []pgtype.UUID{task.RuntimeID}
	for _, test := range []struct {
		name string
		ids  []string
		want int
	}{
		{"unfiltered rotation", nil, 1},
		{"empty live inventory", []string{}, 0},
		{"different live sandbox", []string{"other"}, 0},
		{"matching live sandbox", []string{candidate.SandboxID}, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			rows, err := queries.ListIdleASBSandboxSessionsByRuntimes(ctx, db.ListIdleASBSandboxSessionsByRuntimesParams{
				RuntimeIds: runtimeIDs, SandboxIds: test.ids,
			})
			if err != nil || len(rows) != test.want {
				t.Fatalf("candidates = %d, err = %v; want %d", len(rows), err, test.want)
			}
		})
	}
	// Another transaction admits work after the inventory was read. The
	// under-lock recheck must still fence the sandbox from deletion.
	if _, err := pool.Exec(ctx, `UPDATE agent_task_queue SET status = 'queued' WHERE id = $1`, task.ID); err != nil {
		t.Fatal(err)
	}
	idle, err := isIdleASBSandboxCandidate(ctx, queries, runtimeIDs, pgtype.UUID{}, candidate)
	if err != nil || idle {
		t.Fatalf("candidate became busy: idle = %t, err = %v", idle, err)
	}
}
