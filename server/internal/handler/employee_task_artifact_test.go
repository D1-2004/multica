package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/middleware"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type employeeArtifactFixture struct {
	access  directAccessFixture
	scope   employeetask.Scope
	task    employeetask.Task
	run     employeetask.Run
	token   string
	storage *mockStorage
}

func TestEmployeeArtifactUploadRequiresTaskField(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	var body bytes.Buffer
	form := multipart.NewWriter(&body)
	part, err := form.CreateFormFile("file", "report.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = io.WriteString(part, "artifact contract fixture"); err != nil {
		t.Fatal(err)
	}
	if err = form.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/upload-file", &body)
	r.Header.Set("Content-Type", form.FormDataContentType())
	r.Header.Set("X-Workspace-ID", f.scope.WorkspaceID)
	r.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	middleware.Auth(testHandler.Queries, nil, nil)(http.HandlerFunc(testHandler.UploadFile)).ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Fatalf("incomplete task upload: HTTP %d", w.Code)
	}
	var attachments int
	if err = testPool.QueryRow(context.Background(), `SELECT count(*) FROM attachment WHERE workspace_id=$1::uuid`, f.scope.WorkspaceID).Scan(&attachments); err != nil {
		t.Fatal(err)
	}
	f.storage.mu.Lock()
	objects := len(f.storage.files)
	f.storage.mu.Unlock()
	if attachments != 0 || objects != 0 {
		t.Fatalf("incomplete request published data: attachments=%d objects=%d", attachments, objects)
	}
}

func newEmployeeArtifactFixture(t *testing.T) employeeArtifactFixture {
	t.Helper()
	ensureEmployeeArtifactSchema(t)
	ctx := context.Background()
	wsID, runtimeID, sceneID := uuid.NewString(), uuid.NewString(), ""
	a := directAccessFixture{agent: uuid.NewString(), originator: uuid.NewString(), plain: uuid.NewString(), owned: uuid.NewString(), external: uuid.NewString(), legacy: uuid.NewString()}
	issueID := uuid.NewString()
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := testPool.Exec(ctx, sql, args...); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		for _, table := range []string{"employee_task_artifact", "attachment", "employee_task_entry", "employee_task_run", "employee_task", "agent_scene", "task_token"} {
			_, _ = testPool.Exec(ctx, `DELETE FROM `+table+` WHERE workspace_id=$1::uuid`, wsID)
		}
		_, _ = testPool.Exec(ctx, `DELETE FROM task_message WHERE task_id IN (SELECT id FROM agent_task_queue WHERE agent_id=$1::uuid)`, a.agent)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_task_queue WHERE agent_id=$1::uuid`, a.agent)
		_, _ = testPool.Exec(ctx, `DELETE FROM issue WHERE workspace_id=$1::uuid`, wsID)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_invocation_target WHERE agent_id=$1::uuid`, a.agent)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent WHERE id=$1::uuid`, a.agent)
		_, _ = testPool.Exec(ctx, `DELETE FROM agent_runtime WHERE id=$1::uuid`, runtimeID)
		_, _ = testPool.Exec(ctx, `DELETE FROM member WHERE workspace_id=$1::uuid`, wsID)
		_, _ = testPool.Exec(ctx, `DELETE FROM workspace WHERE id=$1::uuid`, wsID)
		_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE id=$1::uuid OR id=$2::uuid`, a.originator, a.plain)
	})
	exec(`INSERT INTO workspace(id,name,slug) VALUES($1::uuid,'Artifact fixture',$2)`, wsID, "artifact-"+wsID)
	for _, user := range []string{a.originator, a.plain} {
		exec(`INSERT INTO "user"(id,name,email) VALUES($1::uuid,'Artifact member',$2)`, user, user+"@artifact.test")
	}
	for _, user := range []string{testUserID, a.originator, a.plain} {
		role := "member"
		if user == testUserID {
			role = "owner"
		}
		exec(`INSERT INTO member(workspace_id,user_id,role) VALUES($1::uuid,$2::uuid,$3)`, wsID, user, role)
	}
	exec(`INSERT INTO agent_runtime(id,workspace_id,daemon_id,name,runtime_mode,provider,status,owner_id) VALUES($1::uuid,$2::uuid,$3,'Artifact runtime','local','codex','online',$4::uuid)`, runtimeID, wsID, "artifact-"+runtimeID, testUserID)
	exec(`INSERT INTO agent(id,workspace_id,name,runtime_mode,runtime_id,owner_id,permission_mode) VALUES($1::uuid,$2::uuid,'Artifact agent','local',$3::uuid,$4::uuid,'public_to')`, a.agent, wsID, runtimeID, testUserID)
	exec(`INSERT INTO agent_invocation_target(agent_id,target_type,target_id) VALUES($1::uuid,'workspace',$2::uuid)`, a.agent, wsID)
	sc, err := scene.Resolve(ctx, testHandler.Queries, scene.Owner{WorkspaceID: parseUUID(wsID), AgentID: parseUUID(a.agent)}, scene.DingTalkConversation("artifact-org", scene.KindDM, "cid-artifact-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	sceneID = uuidToString(sc.ID)
	scope := employeetask.Scope{WorkspaceID: wsID, AgentID: a.agent, TenantOrgID: "artifact-org", Kind: employeetask.ScopeScene, Scene: scene.Ref{SceneID: sceneID}}
	store := employeetask.NewStore(testPool)
	task, err := store.Create(ctx, employeetask.CreateParams{Scope: scope, OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: a.originator, Definition: employeetask.Definition{Goal: "Create a private report"}, Source: employeetask.Source{Namespace: "artifact_test", Key: uuid.NewString()}, Input: "Create report"})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(service.DirectTaskContext{Type: service.DirectTaskContextType, WorkspaceID: wsID, EmployeeTaskID: task.ID, Prompt: "Create private report", PrincipalID: a.originator, OriginatorUserID: a.originator})
	for _, queueID := range []string{a.owned, a.external} {
		exec(`INSERT INTO agent_task_queue(id,agent_id,runtime_id,status,context,originator_user_id,accountable_user_id,originator_source,trigger_evidence_kind,trigger_evidence_ref_id) VALUES($1::uuid,$2::uuid,$3::uuid,'running',$4,$5::uuid,$5::uuid,'direct_human','employee_task',$6::uuid)`, queueID, a.agent, runtimeID, raw, a.originator, task.ID)
	}
	exec(`INSERT INTO issue(id,workspace_id,title,creator_type,creator_id) VALUES($1::uuid,$2::uuid,'Legacy artifact target','member',$3::uuid)`, issueID, wsID, testUserID)
	exec(`INSERT INTO agent_task_queue(id,agent_id,runtime_id,issue_id,status) VALUES($1::uuid,$2::uuid,$3::uuid,$4::uuid,'queued')`, a.legacy, a.agent, runtimeID, issueID)
	run, err := store.StartRun(ctx, scope, task.ID, employeetask.StartRunParams{Source: employeetask.Source{Namespace: "artifact_run", Key: uuid.NewString()}, QueueTaskID: a.owned, ExpectedVersion: task.Version})
	if err != nil {
		t.Fatal(err)
	}
	token, err := auth.GenerateAgentTaskToken()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testHandler.Queries.CreateTaskToken(ctx, db.CreateTaskTokenParams{TokenHash: auth.HashToken(token), TaskID: parseUUID(a.owned), AgentID: parseUUID(a.agent), WorkspaceID: parseUUID(wsID), UserID: parseUUID(testUserID), ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true}}); err != nil {
		t.Fatal(err)
	}
	previousStorage, previousBox := testHandler.Storage, testHandler.InternalConnectorSecretBox
	memory := &mockStorage{}
	box, err := secretbox.New(bytes.Repeat([]byte{17}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	testHandler.Storage = memory
	testHandler.InternalConnectorSecretBox = box
	t.Cleanup(func() { testHandler.Storage = previousStorage; testHandler.InternalConnectorSecretBox = previousBox })
	return employeeArtifactFixture{access: a, scope: scope, task: task, run: run, token: token, storage: memory}
}
func (f employeeArtifactFixture) upload(t *testing.T, name string, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err = writer.WriteField("task_id", f.access.owned); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest(http.MethodPost, "/api/upload-file", &body)
	r.Header.Set("Content-Type", writer.FormDataContentType())
	r.Header.Set("Authorization", "Bearer "+f.token)
	w := httptest.NewRecorder()
	middleware.Auth(testHandler.Queries, nil, nil)(http.HandlerFunc(testHandler.UploadFile)).ServeHTTP(w, r)
	return w
}
func (f employeeArtifactFixture) download(t *testing.T, id, user string) *httptest.ResponseRecorder {
	t.Helper()
	r := withURLParam(newRequestAs(user, http.MethodGet, "/api/attachments/"+id+"/download", nil), "id", id)
	w := httptest.NewRecorder()
	testHandler.DownloadAttachment(w, r)
	return w
}
func TestEmployeeArtifactUploadReturnsOnlyPersistedPrivateReference(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	body := []byte("PRIVATE_REPORT_BYTES")
	w := f.upload(t, "日报.txt", body)
	if w.Code != http.StatusOK {
		t.Fatalf("Direct upload HTTP %d: %s", w.Code, w.Body.String())
	}
	var got struct {
		AttachmentResponse
		ArtifactRef string `json:"artifact_ref"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.ID == "" || got.ArtifactRef != "attachment:"+got.ID || got.SHA256 != sha256Hex(body) || strings.Contains(got.URL, "cdn.example.com") || strings.Contains(got.DownloadURL, "Signature") {
		t.Fatalf("upload returned non-durable or public reference: %+v", got)
	}
	f.storage.mu.Lock()
	for _, stored := range f.storage.files {
		if bytes.Contains(stored, body) {
			t.Error("public object store contains plaintext artifact")
		}
	}
	f.storage.mu.Unlock()
	download := f.download(t, got.ID, f.access.originator)
	data, err := io.ReadAll(download.Result().Body)
	if err != nil || download.Code != http.StatusOK || !bytes.Equal(data, body) {
		t.Fatalf("private artifact read failed HTTP %d: %q %v", download.Code, data, err)
	}
	denied := f.download(t, got.ID, f.access.plain)
	if denied.Code != http.StatusForbidden && denied.Code != http.StatusNotFound {
		t.Fatalf("workspace member read private artifact: %d %s", denied.Code, denied.Body.String())
	}
	replay := f.upload(t, "日报.txt", body)
	var again struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(replay.Body.Bytes(), &again); err != nil || replay.Code != http.StatusOK || again.ID != got.ID {
		t.Fatalf("upload replay changed artifact: %d %s", replay.Code, replay.Body.String())
	}
}

func ensureEmployeeArtifactSchema(t *testing.T) {
	t.Helper()
	if testPool == nil {
		t.Skip("DATABASE_URL required")
	}
	for _, name := range []string{"9660_employee_task_artifact", "9661_employee_task_artifact_id_idx", "9662_employee_task_artifact_identity_idx", "9663_employee_task_artifact_run_idx", "9664_employee_task_artifact_due_idx"} {
		raw, err := os.ReadFile(filepath.Join("..", "..", "migrations", name+".up.sql"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := testPool.Exec(context.Background(), string(raw)); err != nil {
			t.Fatal(err)
		}
	}
}

func employeeArtifactUploadedID(t *testing.T, f employeeArtifactFixture) string {
	t.Helper()
	w := f.upload(t, "report.txt", []byte("PRIVATE_REPORT_BYTES"))
	if w.Code != http.StatusOK {
		t.Fatalf("upload %d %s", w.Code, w.Body.String())
	}
	var result struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.ID
}
func TestEmployeeArtifactReadyListIgnoresDeletedAttachment(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	id := employeeArtifactUploadedID(t, f)
	refs, err := testHandler.ListEmployeeTaskArtifacts(context.Background(), f.scope, f.task.ID, f.run.ID)
	if err != nil || len(refs) != 1 || refs[0].ID != id {
		t.Fatalf("ready refs %+v %v", refs, err)
	}
	if _, err := testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id=$1::uuid`, id); err != nil {
		t.Fatal(err)
	}
	refs, err = testHandler.ListEmployeeTaskArtifacts(context.Background(), f.scope, f.task.ID, f.run.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("notice refers to deleted attachment: %+v %v", refs, err)
	}
}
func TestEmployeeArtifactSourceCannotBeReboundToIssue(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	id := employeeArtifactUploadedID(t, f)
	legacy, err := testHandler.Queries.GetAgentTask(context.Background(), parseUUID(f.access.legacy))
	if err != nil {
		t.Fatal(err)
	}
	if err := testHandler.Queries.LinkAttachmentsToIssue(context.Background(), db.LinkAttachmentsToIssueParams{IssueID: legacy.IssueID, WorkspaceID: parseUUID(f.scope.WorkspaceID), Column3: []pgtype.UUID{parseUUID(id)}}); err != nil {
		t.Fatal(err)
	}
	att, err := testHandler.Queries.GetAttachmentByIDOnly(context.Background(), parseUUID(id))
	if err != nil || att.IssueID.Valid {
		t.Fatalf("Direct artifact changed domain: %+v %v", att, err)
	}
}
func TestEmployeeArtifactRequesterCanDeleteWithoutLeavingReadableReference(t *testing.T) {
	f := newEmployeeArtifactFixture(t)
	id := employeeArtifactUploadedID(t, f)
	req := withURLParam(newRequestAs(f.access.originator, http.MethodDelete, "/api/attachments/"+id, nil), "id", id)
	req.Header.Set("X-Workspace-ID", f.scope.WorkspaceID)
	w := httptest.NewRecorder()
	testHandler.DeleteAttachment(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("artifact requester delete HTTP %d %s", w.Code, w.Body.String())
	}
	if got := f.download(t, id, f.access.originator); got.Code == http.StatusOK {
		t.Fatal("deleted artifact is readable")
	}
	refs, err := testHandler.ListEmployeeTaskArtifacts(context.Background(), f.scope, f.task.ID, f.run.ID)
	if err != nil || len(refs) != 0 {
		t.Fatalf("deleted artifact returned ready ref: %+v %v", refs, err)
	}
	var state string
	if err := testPool.QueryRow(context.Background(), `SELECT state FROM employee_task_artifact WHERE attachment_id=$1::uuid`, id).Scan(&state); err != nil || state != "deleting" {
		t.Fatalf("cleanup not durable: %s %v", state, err)
	}
}
