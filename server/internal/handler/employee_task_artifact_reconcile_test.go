package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type employeeArtifactFaultStorage struct {
	*mockStorage
	failUpload   atomic.Bool
	failDelete   atomic.Bool
	uploads      atomic.Int32
	deletes      atomic.Int32
	afterUpload  func()
	beforeUpload func()
}

func (s *employeeArtifactFaultStorage) Upload(ctx context.Context, key string, data []byte, contentType, name string) (string, error) {
	s.uploads.Add(1)
	if s.beforeUpload != nil {
		s.beforeUpload()
	}
	url, err := s.mockStorage.Upload(ctx, key, data, contentType, name)
	if err != nil {
		return "", err
	}
	if s.afterUpload != nil {
		s.afterUpload()
	}
	if s.failUpload.Load() {
		return "", errors.New("ambiguous PUT failure")
	}
	return url, nil
}
func (s *employeeArtifactFaultStorage) DeleteObject(ctx context.Context, key string) error {
	s.deletes.Add(1)
	if s.failDelete.Load() {
		return errors.New("temporary object delete failure")
	}
	return s.mockStorage.DeleteObject(ctx, key)
}
func employeeArtifactDue(t *testing.T, workspaceID string) {
	t.Helper()
	if _, err := testPool.Exec(context.Background(), `UPDATE employee_task_artifact SET next_attempt_at=now()-interval '1 minute',lease_expires_at=now()-interval '1 minute' WHERE workspace_id=$1::uuid`, workspaceID); err != nil {
		t.Fatal(err)
	}
}
func employeeArtifactObjectCount(s *mockStorage) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.files)
}

func TestEmployeeArtifactWorkspaceDeletionRetriesAndTombstonesLateObjects(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	faults := &employeeArtifactFaultStorage{mockStorage: f.storage}
	testHandler.Storage = faults
	id := employeeArtifactUploadedID(t, f)
	ctx := context.Background()
	var key string
	if err := testPool.QueryRow(ctx, `SELECT storage_key FROM employee_task_artifact WHERE attachment_id=$1::uuid`, id).Scan(&key); err != nil {
		t.Fatal(err)
	}
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM workspace WHERE id=$1::uuid FOR UPDATE`, f.scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err = testHandler.MarkWorkspaceEmployeeArtifactsDeleting(ctx, tx, parseUUID(f.scope.WorkspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM workspace WHERE id=$1::uuid`, f.scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var state string
	if err = testPool.QueryRow(ctx, `SELECT state FROM employee_task_artifact WHERE attachment_id=$1::uuid`, id).Scan(&state); err != nil || state != "deleting" {
		t.Fatalf("workspace deletion lost cleanup intent: %q %v", state, err)
	}
	faults.failDelete.Store(true)
	if _, err = testHandler.ReconcileEmployeeTaskArtifacts(ctx, 10); err == nil {
		t.Fatal("delete failure was not surfaced")
	}
	if employeeArtifactObjectCount(f.storage) != 1 {
		t.Fatal("failed delete lost fake object")
	}
	faults.failDelete.Store(false)
	employeeArtifactDue(t, f.scope.WorkspaceID)
	if n, err := testHandler.ReconcileEmployeeTaskArtifacts(ctx, 10); err != nil || n < 1 {
		t.Fatalf("retry: %d %v", n, err)
	}
	if employeeArtifactObjectCount(f.storage) != 0 {
		t.Fatal("object was not reclaimed")
	}
	f.storage.mu.Lock()
	f.storage.files[key] = []byte("late PUT")
	f.storage.mu.Unlock()
	employeeArtifactDue(t, f.scope.WorkspaceID)
	if n, err := testHandler.ReconcileEmployeeTaskArtifacts(ctx, 10); err != nil || n < 1 {
		t.Fatalf("tombstone revisit: %d %v", n, err)
	}
	if employeeArtifactObjectCount(f.storage) != 0 {
		t.Fatal("late materialization escaped tombstone")
	}
}

func TestEmployeeArtifactAmbiguousUploadNeverReturnsReadyAndIsReclaimed(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	faults := &employeeArtifactFaultStorage{mockStorage: f.storage}
	faults.failUpload.Store(true)
	testHandler.Storage = faults
	w := f.upload(t, "uncertain.txt", []byte("private uncertain bytes"))
	if w.Code < 400 || strings.Contains(w.Body.String(), "artifact_ref") {
		t.Fatalf("failed PUT advertised completed artifact: %d %s", w.Code, w.Body.String())
	}
	refs, err := testHandler.ListEmployeeTaskArtifacts(context.Background(), f.scope, f.task.ID, f.run.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("failed PUT returned ready refs: %+v %v", refs, err)
	}
	employeeArtifactDue(t, f.scope.WorkspaceID)
	if n, err := testHandler.ReconcileEmployeeTaskArtifacts(context.Background(), 10); err != nil || n < 1 {
		t.Fatalf("failed upload reclaim: %d %v", n, err)
	}
	if employeeArtifactObjectCount(f.storage) != 0 {
		t.Fatal("uncertain upload object leaked")
	}
}

func TestEmployeeArtifactDeletedTaskCannotRetainAnOrphanObject(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	id := employeeArtifactUploadedID(t, f)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `DELETE FROM employee_task_run WHERE id=$1::uuid`, f.run.ID); err != nil {
		t.Fatal(err)
	}
	if n, err := testHandler.ReconcileEmployeeTaskArtifacts(ctx, 10); err != nil || n < 1 {
		t.Fatalf("orphan cleanup: %d %v", n, err)
	}
	if employeeArtifactObjectCount(f.storage) != 0 {
		t.Fatal("orphan task artifact object leaked")
	}
	req := withURLParam(newRequestAs(testUserID, http.MethodGet, "/api/attachments/"+id, nil), "id", id)
	req.Header.Set("X-Workspace-ID", f.scope.WorkspaceID)
	w := httptest.NewRecorder()
	testHandler.GetAttachmentByID(w, req)
	if w.Code == http.StatusOK {
		t.Fatal("deleted task still exposes artifact metadata")
	}
}

func TestEmployeeArtifactWorkspaceDeletionDuringPUTCannotPublishLateFile(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	entered, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	faults := &employeeArtifactFaultStorage{mockStorage: f.storage, beforeUpload: func() { close(entered); <-release }}
	testHandler.Storage = faults
	response := make(chan *httptest.ResponseRecorder, 1)
	go func() { response <- f.upload(t, "racing.txt", []byte("late private file")) }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("PUT did not begin")
	}
	ctx := context.Background()
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err = tx.Exec(ctx, `SELECT id FROM workspace WHERE id=$1::uuid FOR UPDATE`, f.scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err = testHandler.MarkWorkspaceEmployeeArtifactsDeleting(ctx, tx, parseUUID(f.scope.WorkspaceID)); err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(ctx, `DELETE FROM workspace WHERE id=$1::uuid`, f.scope.WorkspaceID); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err = testHandler.ReconcileEmployeeTaskArtifacts(ctx, 10); err != nil {
		t.Fatal(err)
	}
	releaseOnce.Do(func() { close(release) })
	var w *httptest.ResponseRecorder
	select {
	case w = <-response:
	case <-time.After(5 * time.Second):
		t.Fatal("late PUT did not return")
	}
	if w.Code < 400 || strings.Contains(w.Body.String(), "artifact_ref") {
		t.Fatalf("deleted workspace published artifact: %d %s", w.Code, w.Body.String())
	}
	if employeeArtifactObjectCount(f.storage) != 1 {
		t.Fatal("test did not materialize a late object")
	}
	employeeArtifactDue(t, f.scope.WorkspaceID)
	if _, err = testHandler.ReconcileEmployeeTaskArtifacts(ctx, 10); err != nil {
		t.Fatal(err)
	}
	if employeeArtifactObjectCount(f.storage) != 0 {
		t.Fatal("late object survived deletion tombstone")
	}
}

func TestEmployeeArtifactTerminalRunBetweenPUTAndCommitNeverBecomesReady(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	faults := &employeeArtifactFaultStorage{mockStorage: f.storage, afterUpload: func() {
		if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status='completed' WHERE id=$1::uuid`, f.access.owned); err != nil {
			t.Error(err)
		}
	}}
	testHandler.Storage = faults
	w := f.upload(t, "finished.txt", []byte("uncommitted file"))
	if w.Code < 400 || strings.Contains(w.Body.String(), "artifact_ref") {
		t.Fatalf("terminal run published incomplete reference: %d %s", w.Code, w.Body.String())
	}
	var count int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM attachment WHERE task_id=$1::uuid`, f.access.owned).Scan(&count); err != nil || count != 0 {
		t.Fatalf("late attachment count=%d %v", count, err)
	}
	employeeArtifactDue(t, f.scope.WorkspaceID)
	if _, err := testHandler.ReconcileEmployeeTaskArtifacts(context.Background(), 10); err != nil {
		t.Fatal(err)
	}
	if employeeArtifactObjectCount(f.storage) != 0 {
		t.Fatal("failed commit object was not reclaimed")
	}
}
