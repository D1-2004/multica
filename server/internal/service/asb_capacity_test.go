package service

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
)

func TestPeerASBTaskSandboxCandidatesSelectOldestForeignOrdinaryTasks(t *testing.T) {
	t.Parallel()

	localRuntimeID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	peerRuntimeID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	oldTaskID := "33333333-3333-3333-3333-333333333333"
	newTaskID := "44444444-4444-4444-4444-444444444444"
	oldCreatedAt := time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)
	newCreatedAt := oldCreatedAt.Add(time.Minute)

	sandboxes := []ASBSandbox{
		{
			ID:        "peer-new",
			Status:    ASBSandboxStatus{State: "Running"},
			CreatedAt: newCreatedAt,
			Metadata: map[string]string{
				"multica.backend":    string(SandboxBackendASB),
				"multica.runtime_id": util.UUIDToString(peerRuntimeID),
				"multica.task_id":    newTaskID,
			},
		},
		{
			ID:        "peer-old-pending",
			Status:    ASBSandboxStatus{State: "Pending"},
			CreatedAt: oldCreatedAt,
			Metadata: map[string]string{
				"multica.backend":    string(SandboxBackendASB),
				"multica.runtime_id": util.UUIDToString(peerRuntimeID),
				"multica.task_id":    oldTaskID,
			},
		},
		{
			ID:        "local-task",
			Status:    ASBSandboxStatus{State: "Running"},
			CreatedAt: oldCreatedAt.Add(-time.Hour),
			Metadata: map[string]string{
				"multica.backend":    string(SandboxBackendASB),
				"multica.runtime_id": util.UUIDToString(localRuntimeID),
				"multica.task_id":    oldTaskID,
			},
		},
		{
			ID:        "peer-anchor",
			Status:    ASBSandboxStatus{State: "Running"},
			CreatedAt: oldCreatedAt.Add(-2 * time.Hour),
			Metadata: map[string]string{
				"multica.backend":         string(SandboxBackendASB),
				"multica.runtime_id":      util.UUIDToString(peerRuntimeID),
				"multica.task_id":         oldTaskID,
				"multica.identity_source": "true",
			},
		},
		{
			ID:        "peer-release-validation",
			Status:    ASBSandboxStatus{State: "Running"},
			CreatedAt: oldCreatedAt.Add(-3 * time.Hour),
			Metadata: map[string]string{
				"multica.backend":            string(SandboxBackendASB),
				"multica.runtime_id":         util.UUIDToString(peerRuntimeID),
				"multica.task_id":            oldTaskID,
				"multica.release_validation": "true",
			},
		},
		{
			ID:        "peer-terminal",
			Status:    ASBSandboxStatus{State: "Terminated"},
			CreatedAt: oldCreatedAt.Add(-4 * time.Hour),
			Metadata: map[string]string{
				"multica.backend":    string(SandboxBackendASB),
				"multica.runtime_id": util.UUIDToString(peerRuntimeID),
				"multica.task_id":    oldTaskID,
			},
		},
		{
			ID:        "peer-without-task-fence",
			Status:    ASBSandboxStatus{State: "Running"},
			CreatedAt: oldCreatedAt.Add(-5 * time.Hour),
			Metadata: map[string]string{
				"multica.backend":    string(SandboxBackendASB),
				"multica.runtime_id": util.UUIDToString(peerRuntimeID),
			},
		},
	}

	candidates := peerASBTaskSandboxCandidates([]pgtype.UUID{localRuntimeID}, sandboxes)
	if len(candidates) != 2 {
		t.Fatalf("candidate count = %d, want 2", len(candidates))
	}
	if candidates[0].sandbox.ID != "peer-old-pending" || candidates[1].sandbox.ID != "peer-new" {
		t.Fatalf("candidate order = [%s, %s]", candidates[0].sandbox.ID, candidates[1].sandbox.ID)
	}
}

func TestOrdinaryASBTaskMetadataExcludesAnchorAndReleaseValidation(t *testing.T) {
	t.Parallel()

	runtimeID := "11111111-1111-1111-1111-111111111111"
	taskID := "22222222-2222-2222-2222-222222222222"
	ordinary := map[string]string{
		"multica.backend":    string(SandboxBackendASB),
		"multica.runtime_id": runtimeID,
		"multica.task_id":    taskID,
	}
	if !isOrdinaryASBTaskSandboxMetadata(ordinary) {
		t.Fatal("ordinary task metadata was rejected")
	}
	for name, metadata := range map[string]map[string]string{
		"anchor": {
			"multica.backend":         string(SandboxBackendASB),
			"multica.runtime_id":      runtimeID,
			"multica.task_id":         taskID,
			"multica.identity_source": "true",
		},
		"release validation": {
			"multica.backend":            string(SandboxBackendASB),
			"multica.runtime_id":         runtimeID,
			"multica.task_id":            taskID,
			"multica.release_validation": "true",
		},
		"missing task fence": {
			"multica.backend":    string(SandboxBackendASB),
			"multica.runtime_id": runtimeID,
		},
	} {
		if isOrdinaryASBTaskSandboxMetadata(metadata) {
			t.Fatalf("%s metadata was accepted as an ordinary task", name)
		}
	}
}

func TestReclaimPeerASBTaskSandboxDeletesWithoutTaskStatusLookup(t *testing.T) {
	t.Parallel()

	localRuntimeID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	peerRuntimeID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	taskID := "33333333-3333-3333-3333-333333333333"
	createdAt := time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC)
	candidate := ASBSandbox{
		ID:        "peer-task",
		Status:    ASBSandboxStatus{State: "Running"},
		CreatedAt: createdAt,
		Metadata: map[string]string{
			"multica.backend":    string(SandboxBackendASB),
			"multica.runtime_id": util.UUIDToString(peerRuntimeID),
			"multica.task_id":    taskID,
		},
	}

	var deleted atomic.Bool
	var deleteCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/peer-task":
			if deleted.Load() {
				response.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(response, `{"code":"NOT_FOUND","message":"sandbox not found"}`)
				return
			}
			_, _ = io.WriteString(response, `{
				"id":"peer-task",
				"status":{"state":"Running"},
				"createdAt":"2026-08-06T08:00:00Z",
				"metadata":{
					"multica.backend":"asb",
					"multica.runtime_id":"22222222-2222-2222-2222-222222222222",
					"multica.task_id":"33333333-3333-3333-3333-333333333333"
				}
			}`)
		case request.Method == http.MethodDelete && request.URL.Path == "/v1/sandboxes/peer-task":
			deleteCalls.Add(1)
			deleted.Store(true)
			response.WriteHeader(http.StatusNoContent)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/quotas":
			_, _ = io.WriteString(response, `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":5,"usage":4}]`)
		default:
			t.Fatalf("unexpected ASB request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestASBClient(t, server)

	reclaimed, err := reclaimPeerASBTaskSandbox(
		context.Background(),
		client,
		localRuntimeID,
		[]pgtype.UUID{localRuntimeID},
		[]ASBSandbox{candidate},
	)
	if err != nil {
		t.Fatalf("reclaimPeerASBTaskSandbox: %v", err)
	}
	if !reclaimed || deleteCalls.Load() != 1 {
		t.Fatalf("reclaimed = %t, delete calls = %d", reclaimed, deleteCalls.Load())
	}
}

func TestReclaimPeerASBTaskSandboxRechecksAnchorMetadataBeforeDelete(t *testing.T) {
	t.Parallel()

	localRuntimeID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	candidate := ASBSandbox{
		ID:        "changed-to-anchor",
		Status:    ASBSandboxStatus{State: "Running"},
		CreatedAt: time.Date(2026, 8, 6, 8, 0, 0, 0, time.UTC),
		Metadata: map[string]string{
			"multica.backend":    string(SandboxBackendASB),
			"multica.runtime_id": "22222222-2222-2222-2222-222222222222",
			"multica.task_id":    "33333333-3333-3333-3333-333333333333",
		},
	}

	var deleteCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/changed-to-anchor":
			_, _ = io.WriteString(response, `{
				"id":"changed-to-anchor",
				"status":{"state":"Running"},
				"createdAt":"2026-08-06T08:00:00Z",
				"metadata":{
					"multica.backend":"asb",
					"multica.runtime_id":"22222222-2222-2222-2222-222222222222",
					"multica.task_id":"33333333-3333-3333-3333-333333333333",
					"multica.identity_source":"true"
				}
			}`)
		case request.Method == http.MethodDelete:
			deleteCalls.Add(1)
			response.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected ASB request: %s %s", request.Method, request.URL.Path)
		}
	}))
	defer server.Close()
	client := newTestASBClient(t, server)

	reclaimed, err := reclaimPeerASBTaskSandbox(
		context.Background(),
		client,
		localRuntimeID,
		[]pgtype.UUID{localRuntimeID},
		[]ASBSandbox{candidate},
	)
	if err != nil {
		t.Fatalf("reclaimPeerASBTaskSandbox: %v", err)
	}
	if reclaimed || deleteCalls.Load() != 0 {
		t.Fatalf("reclaimed = %t, delete calls = %d", reclaimed, deleteCalls.Load())
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

func TestIsActiveASBTaskStatus(t *testing.T) {
	t.Parallel()

	for _, status := range []string{"queued", "dispatched", "running", "waiting_local_directory", "deferred"} {
		if !isActiveASBTaskStatus(status) {
			t.Fatalf("status %q should fence its sandbox from reclaim", status)
		}
	}
	for _, status := range []string{"completed", "failed", "cancelled"} {
		if isActiveASBTaskStatus(status) {
			t.Fatalf("status %q should allow an untracked sandbox reclaim", status)
		}
	}
}
