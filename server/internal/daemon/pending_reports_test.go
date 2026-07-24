package daemon

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func testPendingDaemon(t *testing.T, serverURL string, store *pendingReportStore) *Daemon {
	t.Helper()
	return &Daemon{
		client:         NewClient(serverURL),
		logger:         slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
		pendingReports: store,
	}
}

func TestPendingReportStoreRoundtrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending_reports.json")
	s := loadPendingReportStore(path, nil)
	s.Enqueue(pendingTerminalReport{Kind: pendingReportKindComplete, TaskID: "task-a", Output: "done"})
	s.Enqueue(pendingTerminalReport{Kind: pendingReportKindFail, TaskID: "task-b", Error: "boom"})
	// Re-enqueue for the same task replaces the earlier entry.
	s.Enqueue(pendingTerminalReport{Kind: pendingReportKindFail, TaskID: "task-a", Error: "later state wins"})

	reloaded := loadPendingReportStore(path, nil)
	if got := reloaded.Len(); got != 2 {
		t.Fatalf("reloaded queue length = %d, want 2", got)
	}
	for _, r := range reloaded.Snapshot() {
		if r.TaskID == "task-a" && (r.Kind != pendingReportKindFail || r.Error != "later state wins") {
			t.Fatalf("task-a entry not replaced by later enqueue: %+v", r)
		}
		if r.CreatedAt.IsZero() {
			t.Fatalf("CreatedAt not stamped on enqueue: %+v", r)
		}
	}
}

func TestPendingReportStoreRoundtripPreservesResultMessage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending_reports.json")
	store := loadPendingReportStore(path, nil)
	store.Enqueue(pendingTerminalReport{
		Kind:          pendingReportKindComplete,
		TaskID:        "task-result-message",
		Output:        "agent execution summary",
		ResultMessage: "最终回复正文",
	})

	reloaded := loadPendingReportStore(path, nil)
	snapshot := reloaded.Snapshot()
	if len(snapshot) != 1 {
		t.Fatalf("snapshot length = %d", len(snapshot))
	}
	if snapshot[0].ResultMessage != "最终回复正文" {
		t.Fatalf("result message = %q", snapshot[0].ResultMessage)
	}
}

func TestPendingReportStoreCorruptFileDropped(t *testing.T) {
	path := filepath.Join(t.TempDir(), "pending_reports.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	s := loadPendingReportStore(path, nil)
	if got := s.Len(); got != 0 {
		t.Fatalf("corrupt file should load as empty queue, got %d entries", got)
	}
	// Store must remain usable after dropping the corrupt payload.
	s.Enqueue(pendingTerminalReport{Kind: pendingReportKindComplete, TaskID: "task-a"})
	if got := loadPendingReportStore(path, nil).Len(); got != 1 {
		t.Fatalf("queue not persisted after corrupt-file recovery, got %d entries", got)
	}
}

func TestDrainPendingReportsRedelivers(t *testing.T) {
	var completeCalls, failCalls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/daemon/tasks/task-a/complete":
			completeCalls.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["result_message"] != "最终回复正文" {
				t.Fatalf("result_message = %#v", body["result_message"])
			}
		case "/api/daemon/tasks/task-b/fail":
			failCalls.Add(1)
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if body["result_message"] != "已向用户说明失败" {
				t.Fatalf("failed result_message = %#v", body["result_message"])
			}
		default:
			t.Errorf("unexpected request path %s", r.URL.Path)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	store := loadPendingReportStore(filepath.Join(t.TempDir(), "q.json"), nil)
	store.Enqueue(pendingTerminalReport{
		Kind:          pendingReportKindComplete,
		TaskID:        "task-a",
		Output:        "done",
		ResultMessage: "最终回复正文",
	})
	store.Enqueue(pendingTerminalReport{
		Kind:          pendingReportKindFail,
		TaskID:        "task-b",
		Error:         "boom",
		ResultMessage: "已向用户说明失败",
		FailureReason: "cancelled",
	})

	d := testPendingDaemon(t, srv.URL, store)
	d.drainPendingReports(context.Background())

	if got := store.Len(); got != 0 {
		t.Fatalf("queue not drained, %d entries left", got)
	}
	if completeCalls.Load() != 1 || failCalls.Load() != 1 {
		t.Fatalf("complete=%d fail=%d, want 1/1", completeCalls.Load(), failCalls.Load())
	}
}

func TestDrainPendingReportsStopsOnTransient(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	store := loadPendingReportStore(filepath.Join(t.TempDir(), "q.json"), nil)
	store.Enqueue(pendingTerminalReport{Kind: pendingReportKindComplete, TaskID: "task-a", CreatedAt: time.Now().Add(-time.Minute)})
	store.Enqueue(pendingTerminalReport{Kind: pendingReportKindComplete, TaskID: "task-b"})

	d := testPendingDaemon(t, srv.URL, store)
	d.drainPendingReports(context.Background())

	// The pass aborts on the first transient failure: exactly one request,
	// both entries retained, the attempted one with a bumped counter.
	if calls.Load() != 1 {
		t.Fatalf("drain issued %d requests, want 1 (abort on transient)", calls.Load())
	}
	if got := store.Len(); got != 2 {
		t.Fatalf("queue length = %d, want 2 (transient keeps entries)", got)
	}
	for _, r := range store.Snapshot() {
		if r.TaskID == "task-a" && r.Attempts != 1 {
			t.Fatalf("task-a attempts = %d, want 1", r.Attempts)
		}
		if r.TaskID == "task-b" && r.Attempts != 0 {
			t.Fatalf("task-b attempts = %d, want 0 (never tried)", r.Attempts)
		}
	}
}

func TestDrainPendingReportsConvertsPermanentComplete(t *testing.T) {
	var failedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/daemon/tasks/task-a/complete":
			w.WriteHeader(http.StatusConflict) // permanent 4xx
		case "/api/daemon/tasks/task-a/fail":
			if err := json.NewDecoder(r.Body).Decode(&failedBody); err != nil {
				t.Fatal(err)
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{}`))
		default:
			t.Errorf("unexpected request path %s", r.URL.Path)
		}
	}))
	defer srv.Close()

	store := loadPendingReportStore(filepath.Join(t.TempDir(), "q.json"), nil)
	store.Enqueue(pendingTerminalReport{
		Kind:          pendingReportKindComplete,
		TaskID:        "task-a",
		Output:        "done",
		ResultMessage: "已完成并回复用户",
	})

	d := testPendingDaemon(t, srv.URL, store)

	// First pass: complete is permanently rejected → converted to fail in place.
	d.drainPendingReports(context.Background())
	snap := store.Snapshot()
	if len(snap) != 1 || snap[0].Kind != pendingReportKindFail {
		t.Fatalf("expected converted fail entry, got %+v", snap)
	}

	// Second pass: the fail replay succeeds → entry removed.
	d.drainPendingReports(context.Background())
	if got := store.Len(); got != 0 {
		t.Fatalf("queue not drained after fail replay, %d entries left", got)
	}
	if failedBody["result_message"] != "已完成并回复用户" {
		t.Fatalf("converted fail result_message = %#v", failedBody["result_message"])
	}
}

func TestDrainPendingReportsDropsExpired(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("expired entry must not be redelivered, got request to %s", r.URL.Path)
	}))
	defer srv.Close()

	store := loadPendingReportStore(filepath.Join(t.TempDir(), "q.json"), nil)
	store.Enqueue(pendingTerminalReport{
		Kind:      pendingReportKindComplete,
		TaskID:    "task-old",
		CreatedAt: time.Now().Add(-pendingReportTTL - time.Hour),
	})

	d := testPendingDaemon(t, srv.URL, store)
	d.drainPendingReports(context.Background())

	if got := store.Len(); got != 0 {
		t.Fatalf("expired entry not dropped, %d entries left", got)
	}
}
