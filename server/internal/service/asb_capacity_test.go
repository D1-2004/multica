package service

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestASBCapacityFullWithOnlyBusyOrForeignSandboxesDoesNotDelete(t *testing.T) {
	ctx := context.Background()
	pool := newTaskClaimRacePool(t)
	queries := db.New(pool)
	taskID, _, workspaceID := dispatchedCommentTaskFixture(t, ctx, pool)
	task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
	if err != nil {
		t.Fatalf("load active task: %v", err)
	}
	if !task.IssueID.Valid {
		t.Fatal("capacity fixture task has no issue scope")
	}

	box, err := secretbox.New(bytes.Repeat([]byte{0x61}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	encryptedAPIKey, err := box.Seal([]byte(testASBAPIKey))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := queries.UpsertASBRuntimeCredential(ctx, db.UpsertASBRuntimeCredentialParams{
		RuntimeID:       task.RuntimeID,
		ApiKeyEncrypted: encryptedAPIKey,
		ApiKeyHint:      "-key",
	}); err != nil {
		t.Fatalf("seed ASB Runtime credential: %v", err)
	}
	if _, err := queries.UpsertCloudSandboxSession(ctx, db.UpsertCloudSandboxSessionParams{
		WorkspaceID:         util.MustParseUUID(workspaceID),
		RuntimeID:           task.RuntimeID,
		ScopeType:           fcE2BScopeTypeIssue,
		ScopeID:             task.IssueID,
		SandboxID:           "busy-local",
		ArtifactRef:         "registry.example/runtime@sha256:" + strings.Repeat("a", 64),
		ExpiresAt:           pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
		SandboxBackend:      string(SandboxBackendASB),
		IdentityFingerprint: asbUnboundIdentityFingerprint,
	}); err != nil {
		t.Fatalf("seed busy ASB session: %v", err)
	}

	var deleteCalls atomic.Int32
	var createCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/quotas":
			_, _ = io.WriteString(response, `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":5,"usage":5}]`)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes":
			state := request.URL.Query().Get("state")
			items := "[]"
			totalItems := 0
			if state == "Running" {
				totalItems = 2
				items = fmt.Sprintf(`[
					{"id":"busy-local","status":{"state":"Running"},"createdAt":"2026-08-24T05:00:00Z","metadata":{"multica.backend":"asb","multica.runtime_id":"%s","multica.task_id":"%s"}},
					{"id":"unknown-foreign","status":{"state":"Running"},"createdAt":"2026-08-24T04:00:00Z","metadata":{"multica.backend":"asb","multica.runtime_id":"11111111-1111-1111-1111-111111111111","multica.task_id":"22222222-2222-2222-2222-222222222222"}}
				]`, util.UUIDToString(task.RuntimeID), taskID)
			}
			_, _ = fmt.Fprintf(response, `{"items":%s,"pagination":{"page":1,"pageSize":100,"totalItems":%d,"totalPages":1,"hasNextPage":false}}`, items, totalItems)
		case request.Method == http.MethodDelete:
			deleteCalls.Add(1)
			response.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodPost && request.URL.Path == "/v1/sandboxes":
			createCalls.Add(1)
			response.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(response, `{"id":"unexpected","status":{"state":"Pending"},"createdAt":"2026-08-24T06:00:00Z"}`)
		default:
			t.Fatalf("unexpected ASB request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestASBClient(t, server)
	credentials := &ASBRuntimeClientProvider{
		Store:   queries,
		Secrets: box,
		Config:  ASBConfig{APIURL: server.URL},
	}
	conn, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire capacity connection: %v", err)
	}
	defer conn.Release()
	_, err = createASBSandboxWithCapacityOnConnection(
		ctx,
		db.New(conn),
		credentials,
		client,
		task.RuntimeID,
		pgtype.UUID{},
		conn,
		ASBCreateSandboxInput{Metadata: map[string]string{"multica.backend": string(SandboxBackendASB)}},
	)
	if !errors.Is(err, ErrASBCapacityUnavailable) {
		t.Fatalf("capacity result = %v, want ErrASBCapacityUnavailable", err)
	}
	if deleteCalls.Load() != 0 || createCalls.Load() != 0 {
		t.Fatalf("busy capacity made delete=%d create=%d calls", deleteCalls.Load(), createCalls.Load())
	}
}

func TestWaitForASBCapacityReleaseObservesSandboxAndQuota(t *testing.T) {
	t.Parallel()

	var getCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/sandbox-reclaimed":
			call := getCalls.Add(1)
			if call == 1 {
				_, _ = io.WriteString(response, `{"id":"sandbox-reclaimed","status":{"state":"Running"},"createdAt":"2026-08-03T05:00:00Z"}`)
				return
			}
			response.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(response, `{"code":"NOT_FOUND","message":"sandbox not found"}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/quotas":
			usage := 5
			if getCalls.Load() > 1 {
				usage = 4
			}
			_, _ = io.WriteString(response, `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":5,"usage":`+strconv.Itoa(usage)+`}]`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client := newTestASBClient(t, server)

	if err := waitForASBCapacityRelease(
		context.Background(),
		client,
		"sandbox-reclaimed",
	); err != nil {
		t.Fatalf("waitForASBCapacityRelease: %v", err)
	}
	if getCalls.Load() < 2 {
		t.Fatalf("get calls = %d, want at least 2", getCalls.Load())
	}
}

func TestASBCapacityContentionRemainsQueueableWhileQuotaIsFull(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name         string
		usage        int
		wantCapacity bool
	}{
		{name: "slot still occupied", usage: 5, wantCapacity: true},
		{name: "slot released", usage: 4, wantCapacity: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.Method != http.MethodGet || request.URL.Path != "/v1/sandboxes/quotas" {
					http.NotFound(response, request)
					return
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = fmt.Fprintf(response, `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":5,"usage":%d}]`, test.usage)
			}))
			defer server.Close()
			cause := errors.New("capacity release raced with another creator")
			err := asbCapacityUnavailableIfStillExhausted(
				context.Background(),
				newTestASBClient(t, server),
				cause,
			)
			if errors.Is(err, ErrASBCapacityUnavailable) != test.wantCapacity {
				t.Fatalf("capacity classification = %v, want capacity=%v", err, test.wantCapacity)
			}
			if !errors.Is(err, cause) {
				t.Fatalf("capacity classification lost original cause: %v", err)
			}
		})
	}
}

func TestGetLiveASBSandboxTreatsNotFoundAsMissing(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/missing-sandbox" {
			response.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(response, `{"code":"NOT_FOUND","message":"sandbox not found"}`)
			return
		}
		http.NotFound(response, request)
	}))
	defer server.Close()
	client := newTestASBClient(t, server)

	sandbox, exists, err := getLiveASBSandbox(
		context.Background(),
		client,
		"missing-sandbox",
	)
	if err != nil {
		t.Fatalf("getLiveASBSandbox: %v", err)
	}
	if exists || sandbox != nil {
		t.Fatalf("sandbox = %#v, exists = %t", sandbox, exists)
	}
}

func TestIsTerminalASBTaskStatusFailsClosed(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"queued", "dispatched", "running", "waiting_local_directory", "deferred"} {
		if isTerminalASBTaskStatus(status) {
			t.Fatalf("status %q must fence its sandbox from reclaim", status)
		}
	}
	for _, status := range []string{"completed", "failed", "cancelled"} {
		if !isTerminalASBTaskStatus(status) {
			t.Fatalf("status %q should allow an untracked sandbox reclaim", status)
		}
	}
	if isTerminalASBTaskStatus("future_non_terminal_status") {
		t.Fatal("unknown task status must fail closed")
	}
}
