package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func (f employeeArtifactFixture) request(user, method, id string) *http.Request {
	r := withURLParam(httptest.NewRequest(method, "/", nil), "id", id)
	r.Header.Set("X-User-ID", user)
	r.Header.Set("X-Workspace-ID", f.scope.WorkspaceID)
	return r
}
func TestEmployeeArtifactPrivateReadSurfacesAndScope(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	id := employeeArtifactUploadedID(t, f)
	for _, surface := range []string{"metadata", "content", "download", "list"} {
		for _, user := range []string{testUserID, f.access.originator, f.access.plain} {
			t.Run(surface+"/"+user, func(t *testing.T) {
				r := f.request(user, http.MethodGet, id)
				w := httptest.NewRecorder()
				switch surface {
				case "metadata":
					testHandler.GetAttachmentByID(w, r)
				case "content":
					testHandler.GetAttachmentContent(w, r)
				case "download":
					testHandler.DownloadAttachment(w, r)
				case "list":
					r = withURLParam(r, "taskId", f.access.owned)
					middleware.RequireWorkspaceMember(testHandler.Queries)(http.HandlerFunc(testHandler.ListEmployeeTaskArtifactsByUser)).ServeHTTP(w, r)
				}
				allowed := user != f.access.plain
				if (w.Code == http.StatusOK) != allowed {
					t.Fatalf("HTTP %d allowed=%v %s", w.Code, allowed, w.Body.String())
				}
				if strings.Contains(w.Body.String(), "cdn.example.com") {
					t.Fatal("raw encrypted-object URL escaped metadata")
				}
			})
		}
	}
	wrong := f.scope
	wrong.TenantOrgID = "other-tenant"
	if refs, err := testHandler.ListEmployeeTaskArtifacts(context.Background(), wrong, f.task.ID, f.run.ID); err == nil || len(refs) != 0 {
		t.Fatalf("foreign tenant refs: %+v %v", refs, err)
	}
	wrong = f.scope
	wrong.WorkspaceID = uuid.NewString()
	if refs, err := testHandler.ListEmployeeTaskArtifacts(context.Background(), wrong, f.task.ID, f.run.ID); err == nil || len(refs) != 0 {
		t.Fatalf("foreign workspace refs: %+v %v", refs, err)
	}
	if refs, err := testHandler.ListEmployeeTaskArtifacts(context.Background(), f.scope, f.task.ID, uuid.NewString()); err == nil || len(refs) != 0 {
		t.Fatalf("foreign run refs: %+v %v", refs, err)
	}
	r := f.request(testUserID, http.MethodGet, id)
	r.Header.Set("X-Actor-Source", "workspace_access_token")
	w := httptest.NewRecorder()
	testHandler.GetAttachmentByID(w, r)
	if w.Code == http.StatusOK {
		t.Fatal("machine credential inherited manager artifact rights")
	}
}
func TestEmployeeArtifactWrongTaskTokenCannotReadOrUpload(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	id := employeeArtifactUploadedID(t, f)
	token, err := auth.GenerateAgentTaskToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.Queries.CreateTaskToken(context.Background(), db.CreateTaskTokenParams{TokenHash: auth.HashToken(token), TaskID: parseUUID(f.access.external), AgentID: parseUUID(f.access.agent), WorkspaceID: parseUUID(f.scope.WorkspaceID), UserID: parseUUID(testUserID), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	r := f.request(testUserID, http.MethodGet, id)
	r.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	middleware.Auth(testHandler.Queries, nil, nil)(http.HandlerFunc(testHandler.DownloadAttachment)).ServeHTTP(w, r)
	if w.Code == http.StatusOK {
		t.Fatal("another queue token read private artifact")
	}
	f.token = token
	w = f.upload(t, "stolen.txt", []byte("must not upload"))
	if w.Code < 400 {
		t.Fatalf("another queue token uploaded: %d %s", w.Code, w.Body.String())
	}
}
func TestEmployeeArtifactCiphertextAndSourceTamperingFailClosed(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	id := employeeArtifactUploadedID(t, f)
	f.storage.mu.Lock()
	for key, data := range f.storage.files {
		corrupt := append([]byte(nil), data...)
		corrupt[len(corrupt)-1] ^= 1
		f.storage.files[key] = corrupt
	}
	f.storage.mu.Unlock()
	w := f.download(t, id, f.access.originator)
	if w.Code == http.StatusOK || bytes.Contains(w.Body.Bytes(), []byte("PRIVATE_REPORT_BYTES")) {
		t.Fatal("tampered ciphertext was served")
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE employee_task_artifact SET tenant_org_id='other-tenant' WHERE attachment_id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	r := f.request(f.access.originator, http.MethodGet, id)
	w = httptest.NewRecorder()
	testHandler.GetAttachmentByID(w, r)
	if w.Code == http.StatusOK {
		t.Fatal("metadata ignored immutable tenant binding")
	}
}
func TestEmployeeArtifactConcurrentReplayHasOneReadyObject(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	faults := &employeeArtifactFaultStorage{mockStorage: f.storage}
	testHandler.Storage = faults
	var wg sync.WaitGroup
	responses := make(chan *httptest.ResponseRecorder, 8)
	for range 8 {
		wg.Go(func() { responses <- f.upload(t, "same.txt", []byte("same immutable data")) })
	}
	wg.Wait()
	close(responses)
	ids := map[string]bool{}
	for response := range responses {
		if response.Code == http.StatusConflict {
			continue
		}
		if response.Code != http.StatusOK {
			t.Fatalf("concurrent response %d %s", response.Code, response.Body.String())
		}
		var got struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		ids[got.ID] = true
	}
	if len(ids) != 1 || faults.uploads.Load() != 1 {
		t.Fatalf("duplicate artifact IDs=%v PUTs=%d", ids, faults.uploads.Load())
	}
}
func TestEmployeeArtifactLegacyIssueFileSurvivesQueueCleanup(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	ctx := context.Background()
	legacy, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(f.access.legacy))
	if err != nil {
		t.Fatal(err)
	}
	id := uuid.NewString()
	url, err := f.storage.Upload(ctx, "legacy-"+id, []byte("LEGACY_FILE"), "text/plain", "legacy.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.Queries.CreateAttachment(ctx, db.CreateAttachmentParams{ID: parseUUID(id), WorkspaceID: parseUUID(f.scope.WorkspaceID), IssueID: legacy.IssueID, TaskID: legacy.ID, UploaderType: "agent", UploaderID: parseUUID(f.access.agent), Filename: "legacy.txt", Url: url, ContentType: "text/plain", SizeBytes: 11, Sha256: sha256Hex([]byte("LEGACY_FILE"))}); err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE id=$1`, legacy.ID); err != nil {
		t.Fatal(err)
	}
	r := f.request(f.access.plain, http.MethodGet, id)
	w := httptest.NewRecorder()
	testHandler.GetAttachmentByID(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("Direct guard changed old Issue attachment semantics: %d %s", w.Code, w.Body.String())
	}
}

func TestEmployeeArtifactCancelledOrSupersededTaskCannotPublish(t *testing.T) {
	for _, change := range []string{"cancelled", "other_active_run"} {
		t.Run(change, func(t *testing.T) {
			f := newEmployeeArtifactFixture(t)
			sql := `UPDATE employee_task SET state='cancelled' WHERE id=$1::uuid`
			if change == "other_active_run" {
				sql = `UPDATE employee_task SET active_run_id=gen_random_uuid() WHERE id=$1::uuid`
			}
			if _, err := testPool.Exec(context.Background(), sql, f.task.ID); err != nil {
				t.Fatal(err)
			}
			w := f.upload(t, "stale.txt", []byte("stale output"))
			if w.Code < 400 || strings.Contains(w.Body.String(), "artifact_ref") {
				t.Fatalf("inactive task published artifact: %d %s", w.Code, w.Body.String())
			}
			if employeeArtifactObjectCount(f.storage) != 0 {
				t.Fatal("inactive task reached object storage")
			}
		})
	}
}
