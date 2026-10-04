package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type agentMCPIssueTestFixture struct {
	principal  a2aintegration.Principal
	agentID    string
	endpointID string
	clientID   string
}

func createAgentMCPIssueTestFixture(t *testing.T) agentMCPIssueTestFixture {
	t.Helper()
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	agentID := createHandlerTestAgent(t, "agent-mcp-issue-integration", nil)
	identity := strings.ReplaceAll(agentID, "-", "")
	publicAgentID := "mcp_agent_" + identity

	var endpointID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_a2a_endpoint (
			workspace_id, agent_id, public_agent_id, enabled,
			delegated_by_user_id, card_name, card_description, card_version,
			card_skills
		)
		VALUES ($1, $2, $3, FALSE, $4, 'Issue MCP Agent',
		        'Creates durable Issues', '1.0.0',
		        '[{"id":"code","name":"Code","description":"Implement code changes","tags":["coding"]}]'::jsonb)
		RETURNING id
	`, testWorkspaceID, agentID, publicAgentID, testUserID).Scan(&endpointID); err != nil {
		t.Fatalf("create MCP endpoint: %v", err)
	}

	var clientID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO a2a_client (endpoint_id, name, status, scopes, created_by, updated_by)
		VALUES ($1, 'Issue MCP test client', 'active', ARRAY['send', 'read']::text[], $2, $2)
		RETURNING id
	`, endpointID, testUserID).Scan(&clientID); err != nil {
		t.Fatalf("create MCP client: %v", err)
	}

	var credentialID string
	credentialHash := identity + identity
	if err := testPool.QueryRow(ctx, `
		INSERT INTO a2a_client_credential (
			client_id, key_id, token_hash, token_prefix, status, created_by
		)
		VALUES ($1, $2, $3, 'mcp_test', 'active', $4)
		RETURNING id
	`, clientID, "mcp_key_"+identity, credentialHash, testUserID).Scan(&credentialID); err != nil {
		t.Fatalf("create MCP credential: %v", err)
	}

	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_task_queue WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM issue WHERE origin_type = 'agent_mcp' AND assignee_id = $1`, agentID)
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_a2a_endpoint WHERE id = $1`, endpointID)
	})

	return agentMCPIssueTestFixture{
		agentID: agentID, endpointID: endpointID, clientID: clientID,
		principal: a2aintegration.Principal{
			WorkspaceID: testWorkspaceID, AgentID: agentID, EndpointID: endpointID,
			PublicAgentID: publicAgentID, ClientID: clientID, CredentialID: credentialID,
			OwnerID: testUserID, Scopes: []string{"send", "read"},
			EndpointEnabled: false, AllowDisabledEndpoint: true,
		},
	}
}

func createSecondaryAgentMCPPrincipal(t *testing.T, fixture agentMCPIssueTestFixture) a2aintegration.Principal {
	t.Helper()
	ctx := context.Background()
	var clientID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO a2a_client (endpoint_id, name, status, scopes, created_by, updated_by)
		VALUES ($1, 'Isolated MCP client', 'active', ARRAY['send', 'read']::text[], $2, $2)
		RETURNING id
	`, fixture.endpointID, testUserID).Scan(&clientID); err != nil {
		t.Fatalf("create secondary MCP client: %v", err)
	}
	var credentialID string
	keySuffix := strings.ReplaceAll(clientID, "-", "")
	if err := testPool.QueryRow(ctx, `
		INSERT INTO a2a_client_credential (
			client_id, key_id, token_hash, token_prefix, status, created_by
		)
		VALUES ($1, $2, $3, 'mcp_other', 'active', $4)
		RETURNING id
	`, clientID, "mcp_other_"+keySuffix, strings.Repeat("b", 64), testUserID).Scan(&credentialID); err != nil {
		t.Fatalf("create secondary MCP credential: %v", err)
	}
	principal := fixture.principal
	principal.ClientID = clientID
	principal.CredentialID = credentialID
	return principal
}

func TestAgentMCPDescribeDoesNotRequireExecutionRuntime(t *testing.T) {
	f := createAgentMCPIssueTestFixture(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent SET runtime_id=NULL WHERE id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	ids, err := parseAgentMCPPrincipalIDs(f.principal)
	if err != nil {
		t.Fatal(err)
	}
	profile, err := testHandler.describeAgentMCP(ctx, f.principal, ids)
	if err != nil || profile["name"] != "Issue MCP Agent" {
		t.Fatalf("active MCP profile with A2A unpublished and no execution runtime: %v %v", profile, err)
	}
	if _, err = testHandler.Queries.GetPublishedAgentA2AEndpointByPublicID(ctx, db.GetPublishedAgentA2AEndpointByPublicIDParams{PublicAgentID: f.principal.PublicAgentID, AllowDisabledEndpoint: true}); err == nil {
		t.Fatal("execution admission was widened along with metadata read")
	}
	principal := f.principal
	principal.AllowDisabledEndpoint = false
	if _, err = testHandler.describeAgentMCP(ctx, principal, ids); err == nil {
		t.Fatal("caller without MCP's explicit unpublished-endpoint allowance read disabled endpoint")
	}
}

func TestAgentMCPDescribePreservesCurrentBindingGuards(t *testing.T) {
	f := createAgentMCPIssueTestFixture(t)
	ctx := context.Background()
	ids, err := parseAgentMCPPrincipalIDs(f.principal)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, mutation string }{
		{"archived_agent", `UPDATE agent SET archived_at=now() WHERE id=$1`},
		{"rebound_owner", `UPDATE agent_a2a_endpoint SET delegated_by_user_id=$2 WHERE agent_id=$1`},
		{"departed_owner", `DELETE FROM member WHERE workspace_id=$1 AND user_id=$2`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tx, err := testPool.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer tx.Rollback(ctx)
			args := []any{f.agentID}
			if tc.name == "departed_owner" {
				args = []any{testWorkspaceID, testUserID}
			}
			if tc.name == "rebound_owner" {
				ownerID := uuid.NewString()
				if _, err = tx.Exec(ctx, `INSERT INTO "user" (id,name,email) VALUES ($1,'Rebound MCP owner',$2)`, ownerID, ownerID+"@mcp-profile.test"); err != nil {
					t.Fatal(err)
				}
				args = []any{f.agentID, ownerID}
			}
			if _, err = tx.Exec(ctx, tc.mutation, args...); err != nil {
				t.Fatal(err)
			}
			h := *testHandler
			h.Queries = db.New(tx)
			if _, err = h.describeAgentMCP(ctx, f.principal, ids); err == nil {
				t.Fatal("invalid current binding read profile")
			}
		})
	}
	ids.EndpointID = parseUUID(uuid.NewString())
	if _, err = testHandler.describeAgentMCP(ctx, f.principal, ids); err == nil {
		t.Fatal("another endpoint read profile")
	}
}

func TestAgentMCPIssueDelegationCreatesOneNativeIssueAndContinuesIt(t *testing.T) {
	fixture := createAgentMCPIssueTestFixture(t)
	ctx := context.Background()
	objectKey := "mcp-artifacts/report.txt"
	storage := &mockStorage{files: map[string][]byte{objectKey: []byte("persistent artifact")}}
	previousStorage := testHandler.Storage
	testHandler.Storage = storage
	t.Cleanup(func() { testHandler.Storage = previousStorage })
	var attachmentID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO attachment (
			workspace_id, uploader_type, uploader_id, filename, url, content_type, size_bytes
		)
		VALUES ($1, 'member', $2, 'report.txt', $3, 'text/plain', 19)
		RETURNING id
	`, testWorkspaceID, testUserID, "https://cdn.example.com/"+objectKey).Scan(&attachmentID); err != nil {
		t.Fatalf("create input attachment: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM attachment WHERE id = $1`, attachmentID)
	})
	args := agentMCPIssueDelegateArguments{
		Instruction:   "Implement the durable MCP Issue flow\nInclude tests.",
		RequestID:     "integration-create-one-issue",
		Title:         "Durable MCP delegation",
		Priority:      "high",
		Stage:         int32Pointer(2),
		StartDate:     "2026-08-11",
		DueDate:       "2026-08-18",
		AttachmentIDs: []string{attachmentID},
	}

	const callers = 6
	results := make([]agentMCPIssueTaskProjection, callers)
	errorsByCaller := make([]error, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := range callers {
		go func() {
			defer wait.Done()
			results[index], errorsByCaller[index] = testHandler.delegateAgentMCPIssue(ctx, args, fixture.principal)
		}()
	}
	wait.Wait()
	for index, err := range errorsByCaller {
		if err != nil {
			t.Fatalf("delegate caller %d: %v", index, err)
		}
	}
	first := results[0]
	if first.Mode != "issue" || first.Issue.ID == "" || first.ID == "" {
		t.Fatalf("unexpected projection: %#v", first)
	}
	if len(first.Resources) != 1 || first.Resources[0]["id"] != attachmentID || first.Resources[0]["type"] != "resource" {
		t.Fatalf("initial task resources = %#v", first.Resources)
	}
	for index, result := range results[1:] {
		if result.ID != first.ID || result.Issue.ID != first.Issue.ID {
			t.Fatalf("caller %d got task/issue (%s, %s), want (%s, %s)", index+1, result.ID, result.Issue.ID, first.ID, first.Issue.ID)
		}
	}
	conflictingArgs := args
	conflictingArgs.Instruction = "A different request using the same idempotency key."
	if _, err := testHandler.delegateAgentMCPIssue(ctx, conflictingArgs, fixture.principal); err == nil || !strings.Contains(err.Error(), "request_id conflicts") {
		t.Fatalf("conflicting request_id error = %v", err)
	}

	var issueCount, taskCount, claimCount int
	var title, description, status, priority, assigneeType, assigneeID, creatorType, creatorID, originType string
	var stage int32
	var startDate, dueDate string
	if err := testPool.QueryRow(ctx, `
		SELECT title, description, status, priority, assignee_type,
		       assignee_id::text, creator_type, creator_id::text, origin_type,
		       stage, start_date::text, due_date::text
		FROM issue WHERE id = $1
	`, first.Issue.ID).Scan(
		&title, &description, &status, &priority, &assigneeType,
		&assigneeID, &creatorType, &creatorID, &originType,
		&stage, &startDate, &dueDate,
	); err != nil {
		t.Fatalf("load delegated Issue: %v", err)
	}
	if title != args.Title || description != args.Instruction || status != "todo" || priority != "high" ||
		assigneeType != "agent" || assigneeID != fixture.agentID || creatorType != "member" ||
		creatorID != testUserID || originType != "agent_mcp" || stage != 2 ||
		startDate != args.StartDate || dueDate != args.DueDate {
		t.Fatalf("native Issue fields were not preserved: title=%q description=%q status=%q priority=%q assignee=(%q,%q) creator=(%q,%q) origin=%q stage=%d dates=(%q,%q)",
			title, description, status, priority, assigneeType, assigneeID, creatorType, creatorID, originType, stage, startDate, dueDate)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM issue WHERE origin_type = 'agent_mcp' AND assignee_id = $1`, fixture.agentID).Scan(&issueCount); err != nil {
		t.Fatalf("count Issues: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, first.Issue.ID, fixture.agentID).Scan(&taskCount); err != nil {
		t.Fatalf("count tasks: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_mcp_delegation WHERE client_id = $1 AND request_id = $2`, fixture.clientID, args.RequestID).Scan(&claimCount); err != nil {
		t.Fatalf("count claims: %v", err)
	}
	if issueCount != 1 || taskCount != 1 || claimCount != 1 {
		t.Fatalf("idempotency counts issue=%d task=%d claim=%d, want 1/1/1", issueCount, taskCount, claimCount)
	}
	var localTaskID string
	if err := testPool.QueryRow(ctx, `
		SELECT id FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2
	`, first.Issue.ID, fixture.agentID).Scan(&localTaskID); err != nil {
		t.Fatalf("load local task: %v", err)
	}
	ids, err := parseAgentMCPPrincipalIDs(fixture.principal)
	if err != nil {
		t.Fatalf("parse MCP principal: %v", err)
	}
	artifacts, err := testHandler.listAgentMCPIssueArtifacts(ctx, parseUUID(first.Issue.ID), ids)
	if err != nil || len(artifacts) != 1 || artifacts[0]["id"] != attachmentID {
		t.Fatalf("list artifacts = %#v, err=%v", artifacts, err)
	}
	read, err := testHandler.readAgentMCPArtifact(ctx, parseUUID(attachmentID), ids)
	if err != nil || read.Resource.Text != "persistent artifact" || read.Resource.Blob != "" {
		t.Fatalf("read artifact = %#v, err=%v", read, err)
	}
	secondaryPrincipal := createSecondaryAgentMCPPrincipal(t, fixture)
	secondaryIDs, err := parseAgentMCPPrincipalIDs(secondaryPrincipal)
	if err != nil {
		t.Fatalf("parse secondary principal: %v", err)
	}
	if _, err := testHandler.getAgentMCPIssueTask(ctx, first.ID, secondaryIDs); err == nil {
		t.Fatal("secondary MCP client read another client's task")
	}
	if _, err := testHandler.getAgentMCPIssue(ctx, parseUUID(first.Issue.ID), secondaryIDs); err == nil {
		t.Fatal("secondary MCP client read another client's Issue")
	}
	if _, err := testHandler.readAgentMCPArtifact(ctx, parseUUID(attachmentID), secondaryIDs); err == nil {
		t.Fatal("secondary MCP client read another client's artifact")
	}

	var uploadBody bytes.Buffer
	uploadWriter := multipart.NewWriter(&uploadBody)
	uploadPart, err := uploadWriter.CreateFormFile("file", "generated.go")
	if err != nil {
		t.Fatalf("create upload part: %v", err)
	}
	if _, err := uploadPart.Write([]byte("package generated\n")); err != nil {
		t.Fatalf("write upload part: %v", err)
	}
	if err := uploadWriter.WriteField("task_id", localTaskID); err != nil {
		t.Fatalf("write task_id: %v", err)
	}
	if err := uploadWriter.Close(); err != nil {
		t.Fatalf("close upload: %v", err)
	}
	uploadRequest := httptest.NewRequest(http.MethodPost, "/api/upload-file", &uploadBody)
	uploadRequest.Header.Set("Content-Type", uploadWriter.FormDataContentType())
	uploadRequest.Header.Set("X-User-ID", testUserID)
	uploadRequest.Header.Set("X-Workspace-ID", testWorkspaceID)
	uploadRequest.Header.Set("X-Agent-ID", fixture.agentID)
	uploadRequest.Header.Set("X-Task-ID", localTaskID)
	uploadRequest.Header.Set("X-Actor-Source", "task_token")
	uploadResponse := httptest.NewRecorder()
	testHandler.UploadFile(uploadResponse, uploadRequest)
	if uploadResponse.Code != http.StatusOK {
		t.Fatalf("upload Issue-task artifact: status=%d body=%s", uploadResponse.Code, uploadResponse.Body.String())
	}
	var uploaded AttachmentResponse
	if err := json.Unmarshal(uploadResponse.Body.Bytes(), &uploaded); err != nil {
		t.Fatalf("decode uploaded artifact: %v", err)
	}
	if uploaded.IssueID == nil || *uploaded.IssueID != first.Issue.ID || uploaded.ChatSessionID != nil {
		t.Fatalf("uploaded artifact owner = %#v, want Issue %s", uploaded, first.Issue.ID)
	}
	artifacts, err = testHandler.listAgentMCPIssueArtifacts(ctx, parseUUID(first.Issue.ID), ids)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("list artifacts after task upload = %#v, err=%v", artifacts, err)
	}
	issueView, err := testHandler.getAgentMCPIssue(ctx, parseUUID(first.Issue.ID), ids)
	if err != nil {
		t.Fatalf("get MCP Issue: %v", err)
	}
	if tasks, ok := issueView["tasks"].([]agentMCPIssueTaskProjection); !ok || len(tasks) != 1 || tasks[0].ID != first.ID {
		t.Fatalf("Issue task history = %#v, want initial task %s", issueView["tasks"], first.ID)
	}

	followUp := agentMCPContinueIssueArguments{
		IssueID: first.Issue.ID, Instruction: "Now add the integration test.", RequestID: "integration-follow-up",
	}
	if _, err := testHandler.continueAgentMCPIssue(ctx, followUp, fixture.principal); err == nil || !strings.Contains(err.Error(), "active Agent task") {
		t.Fatalf("active-task continuation error = %v", err)
	}

	completedPayload, _ := json.Marshal(map[string]any{"output": "initial task completed"})
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_task_queue
		SET status = 'completed', result = $2, completed_at = now()
		WHERE issue_id = $1 AND agent_id = $3
	`, first.Issue.ID, completedPayload, fixture.agentID); err != nil {
		t.Fatalf("complete initial task: %v", err)
	}

	followResults := make([]agentMCPIssueTaskProjection, callers)
	followErrors := make([]error, callers)
	wait.Add(callers)
	for index := range callers {
		go func() {
			defer wait.Done()
			followResults[index], followErrors[index] = testHandler.continueAgentMCPIssue(ctx, followUp, fixture.principal)
		}()
	}
	wait.Wait()
	continued := followResults[0]
	for index, err := range followErrors {
		if err != nil {
			t.Fatalf("continue caller %d: %v", index, err)
		}
		if followResults[index].ID != continued.ID || followResults[index].Issue.ID != first.Issue.ID || followResults[index].Operation != "follow_up" {
			t.Fatalf("follow-up caller %d mismatch: %#v", index, followResults[index])
		}
	}
	var commentCount int
	if err := testPool.QueryRow(ctx, `
		SELECT count(*) FROM comment
		WHERE issue_id = $1 AND content = $2 AND agent_mcp_claim_id IS NOT NULL
	`, first.Issue.ID, followUp.Instruction).Scan(&commentCount); err != nil {
		t.Fatalf("count follow-up comments: %v", err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM agent_task_queue WHERE issue_id = $1 AND agent_id = $2`, first.Issue.ID, fixture.agentID).Scan(&taskCount); err != nil {
		t.Fatalf("count continued tasks: %v", err)
	}
	if commentCount != 1 || taskCount != 2 {
		t.Fatalf("follow-up counts comment=%d task=%d, want 1/2", commentCount, taskCount)
	}
	issueView, err = testHandler.getAgentMCPIssue(ctx, parseUUID(first.Issue.ID), ids)
	if err != nil {
		t.Fatalf("get continued MCP Issue: %v", err)
	}
	if comments, ok := issueView["comments"].([]map[string]any); !ok || len(comments) != 1 || comments[0]["content"] != followUp.Instruction {
		t.Fatalf("Issue comment timeline = %#v, want one follow-up comment", issueView["comments"])
	}
	if tasks, ok := issueView["tasks"].([]agentMCPIssueTaskProjection); !ok || len(tasks) != 2 {
		t.Fatalf("continued Issue task history = %#v, want two tasks", issueView["tasks"])
	}
}

func int32Pointer(value int32) *int32 { return &value }
