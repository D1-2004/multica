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

func TestASBCapacityReclaimsRecentlyIdleSandboxForEveryScope(t *testing.T) {
	for _, scopeType := range []string{fcE2BScopeTypeChat, fcE2BScopeTypeIssue} {
		t.Run(scopeType, func(t *testing.T) {
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
			// Upsert sets last_used_at to now: even a freshly idle chat must be
			// reclaimable as soon as a different task needs the occupied slot.
			if _, err := queries.UpsertCloudSandboxSession(ctx, db.UpsertCloudSandboxSessionParams{
				WorkspaceID: util.MustParseUUID(workspaceID), RuntimeID: task.RuntimeID,
				ScopeType: scopeType, ScopeID: task.IssueID, SandboxID: "recently-idle",
				ArtifactRef:    "registry.example/runtime@sha256:" + strings.Repeat("a", 64),
				ExpiresAt:      pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
				SandboxBackend: string(SandboxBackendASB), IdentityFingerprint: asbUnboundIdentityFingerprint,
			}); err != nil {
				t.Fatal(err)
			}

			var deleted atomic.Bool
			var created atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/quotas":
					usage := 1
					if deleted.Load() {
						usage = 0
					}
					fmt.Fprintf(w, `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":1,"usage":%d}]`, usage)
				case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes":
					items := "[]"
					count := 0
					if r.URL.Query().Get("state") == "Running" && !deleted.Load() {
						items = `[{"id":"recently-idle","status":{"state":"Running"}}]`
						count = 1
					}
					fmt.Fprintf(w, `{"items":%s,"pagination":{"page":1,"pageSize":100,"totalItems":%d,"totalPages":1,"hasNextPage":false}}`, items, count)
				case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/recently-idle":
					if deleted.Load() {
						w.WriteHeader(http.StatusNotFound)
						io.WriteString(w, `{"code":"NOT_FOUND"}`)
					} else {
						io.WriteString(w, `{"id":"recently-idle","status":{"state":"Running"}}`)
					}
				case r.Method == http.MethodDelete && r.URL.Path == "/v1/sandboxes/recently-idle":
					deleted.Store(true)
					w.WriteHeader(http.StatusNoContent)
				case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
					if !deleted.Load() {
						t.Error("created a sandbox before releasing the occupied slot")
					}
					created.Add(1)
					w.WriteHeader(http.StatusAccepted)
					io.WriteString(w, `{"id":"replacement","status":{"state":"Pending"}}`)
				default:
					t.Errorf("unexpected ASB request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusBadRequest)
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
			sandbox, err := createASBSandboxWithCapacityOnConnection(ctx, db.New(conn), credentials, client,
				task.RuntimeID, pgtype.UUID{}, conn, ASBCreateSandboxInput{
					ImageURI: "registry.example/runtime:reclaim-test", TimeoutSeconds: 60,
					ResourceCPU: "1", ResourceMemory: "1Gi", Entrypoint: []string{"sleep", "infinity"},
					Metadata: map[string]string{"multica.backend": "asb"},
				})
			if err != nil {
				t.Fatalf("reclaim freshly idle %s sandbox: %v", scopeType, err)
			}
			if !deleted.Load() || created.Load() != 1 || sandbox.ID != "replacement" {
				t.Fatalf("reclaim/create outcome: deleted=%v created=%d sandbox=%+v", deleted.Load(), created.Load(), sandbox)
			}
		})
	}
}
