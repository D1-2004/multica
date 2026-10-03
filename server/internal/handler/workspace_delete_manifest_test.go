package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/eventrouter"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	"github.com/multica-ai/multica/server/internal/taskinput"
)

type workspaceDeleteAction string

const (
	workspaceDelete       workspaceDeleteAction = "delete"
	workspaceDeleteDetach workspaceDeleteAction = "detach"
	workspaceDeleteKeep   workspaceDeleteAction = "keep"
	workspaceDeleteSettle workspaceDeleteAction = "settle"
)

// workspaceDeletionManifest is the schema coverage contract for workspace
// teardown. Adding a table requires an explicit ownership decision here; the
// handler deletion graph must then implement that decision before CI passes.
var workspaceDeletionManifest = map[string]workspaceDeleteAction{
	"activity_log":                    workspaceDelete,
	"agent":                           workspaceDelete,
	"agent_builder_draft":             workspaceDelete,
	"agent_invocation_target":         workspaceDelete,
	"agent_runtime":                   workspaceDelete,
	"agent_scene":                     workspaceDelete,
	"agent_scene_config":              workspaceDelete,
	"agent_scene_memory":              workspaceDelete,
	"agent_skill":                     workspaceDelete,
	"agent_task_queue":                workspaceDelete,
	"agent_tenant":                    workspaceDelete,
	"agent_to_label":                  workspaceDelete,
	"attachment":                      workspaceDelete,
	"autopilot":                       workspaceDelete,
	"autopilot_collaborator":          workspaceDelete,
	"autopilot_rule_version":          workspaceDelete,
	"autopilot_run":                   workspaceDelete,
	"autopilot_subscriber":            workspaceDelete,
	"autopilot_trigger":               workspaceDelete,
	"channel_binding_token":           workspaceDelete,
	"channel_chat_session_binding":    workspaceDelete,
	"channel_inbound_audit":           workspaceDelete,
	"channel_inbound_message_dedup":   workspaceDelete,
	"channel_installation":            workspaceDelete,
	"channel_media_pending_object":    workspaceDeleteSettle,
	"channel_outbound_card_message":   workspaceDelete,
	"channel_user_binding":            workspaceDelete,
	"chat_draft_restore":              workspaceDelete,
	"chat_message":                    workspaceDelete,
	"chat_pinned_agent":               workspaceDelete,
	"chat_session":                    workspaceDelete,
	"client_usage_daily":              workspaceDeleteDetach,
	"comment":                         workspaceDelete,
	"comment_reaction":                workspaceDelete,
	"connector_app":                   workspaceDelete,
	"connector_auth_binding":          workspaceDelete,
	"connector_auth_instance":         workspaceDelete,
	"connector_oauth_client":          workspaceDelete,
	"connector_oauth_state":           workspaceDelete,
	"contact_sales_inquiry":           workspaceDeleteKeep,
	"context_capability_binding":      workspaceDelete,
	"context_config_grant":            workspaceDelete,
	"context_config_link":             workspaceDelete,
	"context_connector_credential":    workspaceDelete,
	"context_connector_app":           workspaceDelete,
	"context_prompt_component":        workspaceDelete,
	"context_scope_mcp_config":        workspaceDelete,
	"context_scope_routine":           workspaceDelete,
	"daemon_connection":               workspaceDelete,
	"daemon_token":                    workspaceDelete,
	"scene_event_receipt":             workspaceDelete,
	"employee_event_consumption":      workspaceDelete,
	"employee_scene_job":              workspaceDelete,
	"employee_learning":               workspaceDelete,
	"employee_learning_consumption":   workspaceDelete,
	"employee_memory_state":           workspaceDelete,
	"employee_run_notice":             workspaceDelete,
	"employee_task_artifact":          workspaceDeleteSettle,
	"employee_task":                   workspaceDelete,
	"employee_task_collection":        workspaceDelete,
	"employee_task_entry":             workspaceDelete,
	"employee_task_input":             workspaceDelete,
	"employee_task_invitation":        workspaceDelete,
	"employee_task_ready_intent":      workspaceDelete,
	"employee_task_run":               workspaceDelete,
	"employee_task_wait":              workspaceDelete,
	"feedback":                        workspaceDeleteDetach,
	"git_connection":                  workspaceDelete,
	"github_pending_check_suite":      workspaceDelete,
	"github_pending_installation":     workspaceDeleteKeep,
	"github_pull_request":             workspaceDelete,
	"github_pull_request_check_run":   workspaceDelete,
	"github_pull_request_check_suite": workspaceDelete,
	"inbox_item":                      workspaceDelete,
	"issue":                           workspaceDelete,
	"issue_view":                      workspaceDelete,
	"issue_view_preference":           workspaceDelete,
	"issue_dependency":                workspaceDelete,
	"issue_label":                     workspaceDelete,
	"issue_property":                  workspaceDelete,
	"issue_pull_request":              workspaceDelete,
	"issue_reaction":                  workspaceDelete,
	"issue_subscriber":                workspaceDelete,
	"issue_to_label":                  workspaceDelete,
	"issue_vcs_pull_request":          workspaceDelete,
	"lark_binding_token":              workspaceDelete,
	"lark_chat_session_binding":       workspaceDelete,
	"lark_inbound_audit":              workspaceDelete,
	"lark_inbound_message_dedup":      workspaceDelete,
	"lark_installation":               workspaceDelete,
	"lark_outbound_card_message":      workspaceDelete,
	"lark_user_binding":               workspaceDelete,
	"member":                          workspaceDelete,
	"notification_preference":         workspaceDelete,
	"personal_access_token":           workspaceDeleteKeep,
	"pinned_item":                     workspaceDelete,
	"project":                         workspaceDelete,
	"project_resource":                workspaceDelete,
	"quick_action":                    workspaceDelete,
	"runtime_profile":                 workspaceDelete,
	"schema_migrations":               workspaceDeleteKeep,
	"skill":                           workspaceDelete,
	"skill_file":                      workspaceDelete,
	"skill_to_label":                  workspaceDelete,
	"squad":                           workspaceDelete,
	"squad_member":                    workspaceDelete,
	"tag_config_revision":             workspaceDelete,
	"tag_tenant":                      workspaceDelete,
	"sys_cron_executions":             workspaceDeleteKeep,
	"task_message":                    workspaceDelete,
	"task_token":                      workspaceDelete,
	"task_usage":                      workspaceDelete,
	"task_usage_hourly":               workspaceDelete,
	"task_usage_hourly_dirty":         workspaceDelete,
	"task_usage_hourly_rollup_state":  workspaceDeleteKeep,
	"user":                            workspaceDeleteKeep,
	"user_composio_connection":        workspaceDeleteKeep,
	"vcs_commit_status":               workspaceDelete,
	"vcs_connection":                  workspaceDelete,
	"vcs_pull_request":                workspaceDelete,
	"verification_code":               workspaceDeleteKeep,
	"webhook_delivery":                workspaceDelete,
	"workspace":                       workspaceDelete,
	"workspace_invitation":            workspaceDelete,
	"workspace_tag":                   workspaceDelete,
}

func TestWorkspaceDeletionManifestCoversPublicSchema(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}

	rows, err := testPool.Query(context.Background(), `
SELECT tablename
FROM pg_tables
WHERE schemaname = 'public'
ORDER BY tablename
`)
	if err != nil {
		t.Fatalf("list public tables: %v", err)
	}
	defer rows.Close()

	actual := make(map[string]struct{}, len(workspaceDeletionManifest))
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatalf("scan public table: %v", err)
		}
		actual[table] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate public tables: %v", err)
	}

	var unclassified, missing []string
	for table := range actual {
		if _, ok := workspaceDeletionManifest[table]; !ok {
			unclassified = append(unclassified, table)
		}
	}
	for table := range workspaceDeletionManifest {
		if _, ok := actual[table]; !ok {
			missing = append(missing, table)
		}
	}
	sort.Strings(unclassified)
	sort.Strings(missing)
	if len(unclassified) > 0 || len(missing) > 0 {
		t.Fatalf("workspace deletion manifest drift: unclassified=%v missing=%v", unclassified, missing)
	}

	workspaceColumns, err := testPool.Query(context.Background(), `
SELECT table_name
FROM information_schema.columns
WHERE table_schema = 'public'
  AND column_name = 'workspace_id'
`)
	if err != nil {
		t.Fatalf("list workspace_id columns: %v", err)
	}
	defer workspaceColumns.Close()

	withWorkspaceID := make(map[string]struct{})
	for workspaceColumns.Next() {
		var table string
		if err := workspaceColumns.Scan(&table); err != nil {
			t.Fatalf("scan workspace_id table: %v", err)
		}
		withWorkspaceID[table] = struct{}{}
	}
	if err := workspaceColumns.Err(); err != nil {
		t.Fatalf("iterate workspace_id tables: %v", err)
	}

	for table, action := range workspaceDeletionManifest {
		_, hasWorkspaceID := withWorkspaceID[table]
		switch action {
		case workspaceDeleteKeep:
			if hasWorkspaceID {
				t.Errorf("KEEP table %s gained workspace_id; classify its teardown behavior", table)
			}
		case workspaceDeleteDetach, workspaceDeleteSettle:
			if !hasWorkspaceID {
				t.Errorf("%s table %s lost workspace_id; update its teardown selector", action, table)
			}
		}
	}
}

func seedWorkspaceEmployeeTask(t *testing.T) string {
	t.Helper()
	ctx := context.Background()
	var workspaceID, runtimeID, agentID string
	if err := testPool.QueryRow(ctx, `INSERT INTO workspace(name,slug) VALUES('EmployeeTask deletion test',$1) RETURNING id::text`, "employee-delete-"+uuid.NewString()).Scan(&workspaceID); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		for _, table := range []string{"employee_task_ready_intent", "employee_task_input", "employee_task_invitation", "employee_task_collection", "employee_run_notice", "employee_learning_consumption", "employee_event_consumption", "employee_scene_job", "employee_learning", "employee_memory_state", "scene_event_receipt", "employee_task_wait", "employee_task_run", "employee_task_entry", "employee_task", "agent_scene", "agent", "agent_runtime", "member"} {
			if _, err := testPool.Exec(context.Background(), `DELETE FROM `+table+` WHERE workspace_id=$1`, workspaceID); err != nil {
				t.Errorf("cleanup %s: %v", table, err)
			}
		}
		if _, err := testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id=$1`, workspaceID); err != nil {
			t.Error(err)
		}
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO member(workspace_id,user_id,role) VALUES($1,$2,'owner')`, workspaceID, testUserID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO agent_runtime(workspace_id,name,runtime_mode,provider,status,device_info,metadata,owner_id,last_seen_at)
 VALUES($1,'EmployeeTask test','cloud','employee-task-test','online','test','{}'::jsonb,$2,now()) RETURNING id::text`, workspaceID, testUserID).Scan(&runtimeID); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `INSERT INTO agent(workspace_id,name,description,runtime_mode,runtime_config,runtime_id,visibility,max_concurrent_tasks,owner_id)
 VALUES($1,'EmployeeTask test','','cloud','{}'::jsonb,$2,'workspace',1,$3) RETURNING id::text`, workspaceID, runtimeID, testUserID).Scan(&agentID); err != nil {
		t.Fatal(err)
	}
	registered, err := scene.Resolve(ctx, testHandler.Queries, scene.Owner{WorkspaceID: parseUUID(workspaceID), AgentID: parseUUID(agentID)}, scene.DingTalkConversation("employee-delete-org", scene.KindGroup, "cid-"+uuid.NewString()), scene.Observation{KindStated: true})
	if err != nil {
		t.Fatal(err)
	}
	store := employeetask.NewStore(testPool)
	task, err := store.Create(ctx, employeetask.CreateParams{
		Scope:     employeetask.Scope{WorkspaceID: workspaceID, AgentID: agentID, TenantOrgID: "employee-delete-org", Kind: employeetask.ScopeScene, Scene: scene.RefOf(registered)},
		OwnerLoop: employeetask.LoopEmployee, DispatchMode: employeetask.DispatchDirect, RequesterRef: testUserID,
		Definition: employeetask.Definition{Goal: "Preserve until workspace deletion"}, Source: employeetask.Source{Namespace: "test", Key: "request"}, Input: "Preserve until workspace deletion",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.StartRun(ctx, task.Scope, task.ID, employeetask.StartRunParams{Source: employeetask.Source{Namespace: "test", Key: "run"}, QueueTaskID: uuid.NewString(), ExpectedVersion: task.Version}); err != nil {
		t.Fatal(err)
	}
	// Teardown coverage only: a wait row keyed by this workspace.
	if _, err = testPool.Exec(ctx, `INSERT INTO employee_task_wait(workspace_id,agent_id,tenant_org_id,task_id,kind,ref_id,mandatory,goal_revision,opened_seq) VALUES($1::uuid,$2::uuid,$3,$4::uuid,'collection','delete-fixture',true,1,1)`, workspaceID, agentID, task.Scope.TenantOrgID, task.ID); err != nil {
		t.Fatal(err)
	}
	receipt, _, err := eventrouter.Admit(ctx, testPool, eventrouter.Event{Version: 1, ID: "delete-fixture", Source: "employee-test", Type: "channel.message.created", Category: eventrouter.UserMessage, OccurredAt: time.Now(), PayloadSchema: "test", Payload: json.RawMessage(`{"text":"work"}`)}, eventrouter.Host{Owner: scene.Owner{WorkspaceID: parseUUID(workspaceID), AgentID: parseUUID(agentID)}, PrincipalID: parseUUID(testUserID), TenantOrgID: task.Scope.TenantOrgID, Locator: scene.DingTalkConversation(task.Scope.TenantOrgID, scene.KindGroup, registered.ExternalSceneID), Observation: scene.Observation{KindStated: true}, Route: eventrouter.Unified, ConfigVersion: "test", Fingerprint: "delete-fixture"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = employeeentry.NewStore(testPool).Admit(ctx, employeeentry.Admission{Scope: employeeentry.Scope{WorkspaceID: workspaceID, AgentID: agentID, TenantOrgID: task.Scope.TenantOrgID, SceneID: task.Scope.Scene.SceneID}, Item: employeeentry.Item{ReceiptID: uuidToString(receipt.ID), PrincipalID: testUserID, Payload: json.RawMessage(`{"trusted":"delete fixture"}`), MessageCount: 1}, Owner: employeeentry.Employee, ConfigRevision: "test"}); err != nil {
		t.Fatal(err)
	}
	if _, err = employeememory.NewStore(testPool).Record(ctx, employeememory.Scope{WorkspaceID: parseUUID(workspaceID), AgentID: parseUUID(agentID), TenantOrgID: task.Scope.TenantOrgID, Scene: task.Scope.Scene, Kind: employeememory.ScopeScene}, employeememory.LearningRecord{Type: employeememory.LearningTypePreference, Key: "delete-fixture", Insight: "Keep updates concise", Confidence: 8}, employeememory.TrustedEvidence{SourceID: "delete-fixture", EvidenceID: workspaceID, ActorID: testUserID, HumanStated: true}); err != nil {
		t.Fatal(err)
	}
	if _, err = testPool.Exec(ctx, `INSERT INTO employee_learning_consumption(workspace_id,agent_id,tenant_org_id,scene_id,task_id,run_id,queue_task_id,requester_ref,state,reason) SELECT t.workspace_id,t.agent_id,t.tenant_org_id,t.scene_id,t.id,r.id,r.queue_task_id,t.requester_ref,'skipped','delete_fixture' FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id WHERE t.id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	seedWorkspaceTaskInput(t, task)
	if _, err = testPool.Exec(ctx, `INSERT INTO employee_run_notice(workspace_id,agent_id,tenant_org_id,scene_id,task_id,run_id,queue_task_id,requester_ref,source_ref,result_state,state,reason) SELECT t.workspace_id,t.agent_id,t.tenant_org_id,t.scene_id,t.id,r.id,r.queue_task_id,t.requester_ref,'delete-fixture','cancelled','suppressed','delete_fixture' FROM employee_task t JOIN employee_task_run r ON r.task_id=t.id WHERE t.id=$1`, task.ID); err != nil {
		t.Fatal(err)
	}
	return workspaceID
}

// seedWorkspaceTaskInput adds a cross-scene collection with one answered
// invitation and its ready intent, through the real taskinput store.
func seedWorkspaceTaskInput(t *testing.T, task employeetask.Task) {
	t.Helper()
	ctx := context.Background()
	scope := taskinput.Scope{WorkspaceID: task.Scope.WorkspaceID, AgentID: task.Scope.AgentID, TenantOrgID: task.Scope.TenantOrgID}
	sceneID := task.Scope.Scene.SceneID
	authority := taskinput.Authority{ActorRef: testUserID, SceneID: sceneID, ReceiptRef: "delete-fixture", VerifiedAt: time.Now()}
	store := taskinput.NewStore(testPool)
	col, invitations, err := store.CreateCollectionTx(ctx, scope, taskinput.CreateCollectionParams{TaskID: task.ID, OriginSceneID: sceneID,
		AuthorityRef: "delete-fixture", RequesterRef: testUserID, DeliveryAnchorRef: "delete-fixture", GoalRevision: task.GoalRevision,
		Invitations: []taskinput.InvitationSpec{{TargetSceneID: sceneID, ParticipantRef: "participant", Question: "Number?"}},
		Source:      taskinput.Source{Namespace: "test", Key: "collection"}, Authority: authority})
	if err != nil {
		t.Fatal(err)
	}
	inv, err := store.RecordInviteDeliveryTx(ctx, scope, taskinput.RecordDeliveryParams{InvitationID: invitations[0].ID, ActionID: invitations[0].DeliveryActionID,
		Outcome: taskinput.DeliverySent, ProviderMessageID: "delete-fixture-msg", RenderedHash: taskinput.CheckEgress(taskinput.EgressInput{Rendered: "Number?"}).RenderedHash})
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.AcceptInputTx(ctx, scope, taskinput.AcceptInputParams{CollectionID: col.ID, InvitationID: inv.ID, Source: taskinput.Source{Namespace: "test", Key: "answer"},
		Authority:  taskinput.Authority{ActorRef: "participant", SceneID: sceneID, ReceiptRef: "delete-fixture-answer", VerifiedAt: time.Now()},
		SenderKind: taskinput.SenderPerson, MessageKind: taskinput.MessageText, Binding: taskinput.BindReplyChain, OccurredAt: inv.CreatedAt.Add(time.Second), Body: "7", ExpectedRevision: col.Revision})
	if err != nil || result.Ready == nil {
		t.Fatalf("seed collection input: %+v %v", result, err)
	}
}

func assertWorkspaceEmployeeRecords(t *testing.T, workspaceID string, tasks int) {
	t.Helper()
	for table, multiplier := range map[string]int{"employee_task_collection": 1, "employee_task_invitation": 1, "employee_task_input": 1, "employee_task_ready_intent": 1, "employee_run_notice": 1, "employee_learning_consumption": 1, "employee_task": 1, "employee_task_entry": 2, "employee_task_run": 1, "employee_task_wait": 1, "employee_event_consumption": 1, "employee_scene_job": 1, "scene_event_receipt": 1, "employee_learning": 1, "employee_memory_state": 1} {
		var count int
		if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM `+table+` WHERE workspace_id=$1`, workspaceID).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != tasks*multiplier {
			t.Errorf("%s rows for workspace %s = %d, want %d", table, workspaceID, count, tasks*multiplier)
		}
	}
}

func TestDeleteWorkspace_CleansEmployeeTasksAndPreservesNeighbor(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	target, neighbor := seedWorkspaceEmployeeTask(t), seedWorkspaceEmployeeTask(t)
	assertWorkspaceEmployeeRecords(t, target, 1)
	assertWorkspaceEmployeeRecords(t, neighbor, 1)
	response := httptest.NewRecorder()
	testHandler.DeleteWorkspace(response, withURLParam(newRequest(http.MethodDelete, "/api/workspaces/"+target, nil), "id", target))
	if response.Code != http.StatusNoContent {
		t.Fatalf("DeleteWorkspace: %d %s", response.Code, response.Body.String())
	}
	assertWorkspaceEmployeeRecords(t, target, 0)
	assertWorkspaceEmployeeRecords(t, neighbor, 1)
}

func TestDeleteWorkspace_RollsBackEmployeeTaskCleanup(t *testing.T) {
	if testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	workspaceID := seedWorkspaceEmployeeTask(t)
	setWorkspaceDeleteLockTimeoutForTest(t, 100*time.Millisecond)
	blocker, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = blocker.Rollback(context.Background()) })
	if _, err = blocker.Exec(ctx, `SELECT id FROM member WHERE workspace_id=$1 FOR UPDATE`, workspaceID); err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	testHandler.DeleteWorkspace(response, withURLParam(newRequest(http.MethodDelete, "/api/workspaces/"+workspaceID, nil), "id", workspaceID))
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("DeleteWorkspace: %d %s", response.Code, response.Body.String())
	}
	if err = blocker.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	assertWorkspaceEmployeeRecords(t, workspaceID, 1)
}
