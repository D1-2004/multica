package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
)

func seedRunnerAccountUser(t *testing.T, prefix string) string {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")
	var userID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO "user" (name, email)
		VALUES ($1, $2)
		RETURNING id
	`, prefix, fmt.Sprintf("%s-%s@multica.test", prefix, suffix)).Scan(&userID); err != nil {
		t.Fatalf("seed Runner account user: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM "user" WHERE id = $1`, userID)
	})
	return userID
}

func seedRunnerAccountWorkspaceAgent(t *testing.T, ownerID, prefix string) (string, string, string, string) {
	t.Helper()
	suffix := strings.ReplaceAll(uuid.NewString(), "-", "")[:12]
	workspaceName := prefix + " Workspace"
	workspaceSlug := strings.ToLower(prefix) + "-" + suffix
	var workspaceID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO workspace (name, slug, description, issue_prefix)
		VALUES ($1, $2, '', 'RAT')
		RETURNING id
	`, workspaceName, workspaceSlug).Scan(&workspaceID); err != nil {
		t.Fatalf("seed Runner account workspace: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM workspace WHERE id = $1`, workspaceID)
	})
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO member (workspace_id, user_id, role)
		VALUES ($1, $2, 'owner')
	`, workspaceID, ownerID); err != nil {
		t.Fatalf("seed Runner account workspace member: %v", err)
	}

	var runtimeID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, last_seen_at
		)
		VALUES ($1, NULL, $2, 'cloud', 'runner_account_test', 'online', '', '{}'::jsonb, $3, now())
		RETURNING id
	`, workspaceID, prefix+" Runtime", ownerID).Scan(&runtimeID); err != nil {
		t.Fatalf("seed Runner account runtime: %v", err)
	}

	agentName := prefix + " Agent"
	var agentID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent (
			workspace_id, name, description, runtime_mode, runtime_config,
			runtime_id, visibility, permission_mode, max_concurrent_tasks,
			owner_id, instructions, custom_env, custom_args
		)
		VALUES ($1, $2, '', 'cloud', '{}'::jsonb, $3, 'private', 'private', 1, $4, '', '{}'::jsonb, '[]'::jsonb)
		RETURNING id
	`, workspaceID, agentName, runtimeID, ownerID).Scan(&agentID); err != nil {
		t.Fatalf("seed Runner account agent: %v", err)
	}
	return workspaceID, workspaceName, workspaceSlug, agentID
}

func seedRunnerAccountMachine(t *testing.T, ownerID, name string, keyByte byte, online bool) string {
	t.Helper()
	publicKey := bytes.Repeat([]byte{keyByte}, 32)
	var machineID string
	if online {
		if err := testPool.QueryRow(context.Background(), `
			INSERT INTO runner_machine (
				owner_id, name, os, arch, public_key, client_version,
				last_seen_at, connection_id, connected_at
			)
			VALUES ($1, $2, 'darwin', 'arm64', $3, 'test-version', now(), $4, now())
			RETURNING id
		`, ownerID, name, publicKey, uuid.NewString()).Scan(&machineID); err != nil {
			t.Fatalf("seed online Runner machine: %v", err)
		}
	} else if err := testPool.QueryRow(context.Background(), `
		INSERT INTO runner_machine (owner_id, name, os, arch, public_key, client_version)
		VALUES ($1, $2, 'darwin', 'arm64', $3, 'test-version')
		RETURNING id
	`, ownerID, name, publicKey).Scan(&machineID); err != nil {
		t.Fatalf("seed offline Runner machine: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM runner_machine WHERE id = $1`, machineID)
	})
	return machineID
}

func seedRunnerAccountBinding(t *testing.T, workspaceID, agentID, machineID, ownerID string, disconnected, revoked bool) string {
	t.Helper()
	roots, err := json.Marshal([]string{"/Users/test/project"})
	if err != nil {
		t.Fatalf("encode Runner roots: %v", err)
	}
	var bindingID string
	if err := testPool.QueryRow(context.Background(), `
		INSERT INTO agent_runner_binding (
			workspace_id, agent_id, machine_id, bound_by, roots,
			disconnected_at, disconnected_by, revoked_at, revoked_by
		)
		VALUES (
			$1, $2, $3, $4, $5::jsonb,
			CASE WHEN $6::boolean THEN now() END,
			CASE WHEN $6::boolean THEN $4::uuid END,
			CASE WHEN $7::boolean THEN now() END,
			CASE WHEN $7::boolean THEN $4::uuid END
		)
		RETURNING id
	`, workspaceID, agentID, machineID, ownerID, roots, disconnected, revoked).Scan(&bindingID); err != nil {
		t.Fatalf("seed Runner binding: %v", err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM agent_runner_binding WHERE id = $1`, bindingID)
	})
	return bindingID
}

func runnerAccountRequest(method, path, userID, bindingID string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-User-ID", userID)
	if bindingID != "" {
		req = withURLParam(req, "bindingId", bindingID)
	}
	return req
}

func TestListMyRunnerBindingsGroupsAcrossWorkspacesAndIsolatesOwners(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ownerID := seedRunnerAccountUser(t, "runner-list-owner")
	otherOwnerID := seedRunnerAccountUser(t, "runner-list-other")
	workspaceOneID, workspaceOneName, workspaceOneSlug, agentOneID := seedRunnerAccountWorkspaceAgent(t, ownerID, "RunnerOne")
	workspaceTwoID, workspaceTwoName, workspaceTwoSlug, agentTwoID := seedRunnerAccountWorkspaceAgent(t, ownerID, "RunnerTwo")
	otherWorkspaceID, _, _, otherAgentID := seedRunnerAccountWorkspaceAgent(t, otherOwnerID, "RunnerOther")

	sharedName := "Shared Hostname"
	onlineMachineID := seedRunnerAccountMachine(t, ownerID, sharedName, 1, true)
	offlineMachineID := seedRunnerAccountMachine(t, ownerID, sharedName, 2, false)
	otherMachineID := seedRunnerAccountMachine(t, otherOwnerID, sharedName, 3, true)
	connectedBindingID := seedRunnerAccountBinding(t, workspaceOneID, agentOneID, onlineMachineID, ownerID, false, false)
	disconnectedBindingID := seedRunnerAccountBinding(t, workspaceTwoID, agentTwoID, onlineMachineID, ownerID, true, false)
	visibleOfflineBindingID := seedRunnerAccountBinding(t, workspaceOneID, agentOneID, offlineMachineID, ownerID, false, false)
	_ = seedRunnerAccountBinding(t, workspaceTwoID, agentTwoID, offlineMachineID, ownerID, false, true)
	_ = seedRunnerAccountBinding(t, otherWorkspaceID, otherAgentID, otherMachineID, otherOwnerID, false, false)

	w := httptest.NewRecorder()
	testHandler.ListMyRunnerBindings(w, runnerAccountRequest(http.MethodGet, "/api/me/runner-bindings", ownerID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("ListMyRunnerBindings status = %d, body = %s", w.Code, w.Body.String())
	}
	var response struct {
		Machines []accountRunnerMachineResponse `json:"machines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode Runner account list: %v", err)
	}
	if len(response.Machines) != 2 {
		t.Fatalf("machine count = %d, want 2: %s", len(response.Machines), w.Body.String())
	}
	machines := make(map[string]accountRunnerMachineResponse, len(response.Machines))
	for _, machine := range response.Machines {
		machines[machine.MachineID] = machine
		if machine.MachineID == otherMachineID {
			t.Fatal("another owner's Runner machine was returned")
		}
	}

	onlineMachine := machines[onlineMachineID]
	if !onlineMachine.Online || onlineMachine.LastSeenAt == nil || len(onlineMachine.Bindings) != 2 {
		t.Fatalf("online machine = %#v, want online with two bindings", onlineMachine)
	}
	bindings := make(map[string]accountRunnerBindingResponse, len(onlineMachine.Bindings))
	for _, binding := range onlineMachine.Bindings {
		bindings[binding.BindingID] = binding
	}
	connected := bindings[connectedBindingID]
	if connected.Disconnected || connected.WorkspaceID != workspaceOneID || connected.WorkspaceName != workspaceOneName || connected.WorkspaceSlug != workspaceOneSlug || connected.AgentID != agentOneID || connected.AgentName != "RunnerOne Agent" {
		t.Fatalf("connected binding = %#v", connected)
	}
	disconnected := bindings[disconnectedBindingID]
	if !disconnected.Disconnected || disconnected.WorkspaceID != workspaceTwoID || disconnected.WorkspaceName != workspaceTwoName || disconnected.WorkspaceSlug != workspaceTwoSlug || disconnected.AgentID != agentTwoID || disconnected.AgentName != "RunnerTwo Agent" {
		t.Fatalf("disconnected binding = %#v", disconnected)
	}

	offlineMachine := machines[offlineMachineID]
	if offlineMachine.Online || offlineMachine.LastSeenAt != nil || len(offlineMachine.Bindings) != 1 || offlineMachine.Bindings[0].BindingID != visibleOfflineBindingID {
		t.Fatalf("offline machine = %#v, want one active binding and revoked binding excluded", offlineMachine)
	}
}

func TestMyRunnerBindingActionsRequireMachineOwnerAfterAgentTransfer(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	machineOwnerID := seedRunnerAccountUser(t, "runner-action-owner")
	newAgentOwnerID := seedRunnerAccountUser(t, "runner-action-new-owner")
	workspaceID, _, _, agentID := seedRunnerAccountWorkspaceAgent(t, machineOwnerID, "RunnerAction")
	machineID := seedRunnerAccountMachine(t, machineOwnerID, "Action Machine", 4, true)
	bindingID := seedRunnerAccountBinding(t, workspaceID, agentID, machineID, machineOwnerID, false, false)
	if _, err := testPool.Exec(context.Background(), `
		INSERT INTO member (workspace_id, user_id, role) VALUES ($1, $2, 'member')
	`, workspaceID, newAgentOwnerID); err != nil {
		t.Fatalf("add the new Agent owner to the workspace: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE agent SET owner_id = $1 WHERE id = $2`, newAgentOwnerID, agentID); err != nil {
		t.Fatalf("transfer Agent ownership: %v", err)
	}
	if _, err := testPool.Exec(context.Background(), `DELETE FROM member WHERE workspace_id = $1 AND user_id = $2`, workspaceID, machineOwnerID); err != nil {
		t.Fatalf("remove machine owner from workspace: %v", err)
	}

	h := *testHandler
	h.configProvider = nil
	h.cfg.PublicURL = "https://multica.example"

	unauthorizedDisconnect := httptest.NewRecorder()
	h.DisconnectMyRunnerBinding(unauthorizedDisconnect, runnerAccountRequest(http.MethodPost, "/api/me/runner-bindings/"+bindingID+"/disconnect", newAgentOwnerID, bindingID))
	if unauthorizedDisconnect.Code != http.StatusNotFound {
		t.Fatalf("new Agent owner disconnect status = %d, want 404", unauthorizedDisconnect.Code)
	}
	var disconnectedAt pgtype.Timestamptz
	if err := testPool.QueryRow(context.Background(), `SELECT disconnected_at FROM agent_runner_binding WHERE id = $1`, bindingID).Scan(&disconnectedAt); err != nil {
		t.Fatalf("read binding after unauthorized disconnect: %v", err)
	}
	if disconnectedAt.Valid {
		t.Fatal("unauthorized disconnect changed the binding")
	}

	disconnect := httptest.NewRecorder()
	h.DisconnectMyRunnerBinding(disconnect, runnerAccountRequest(http.MethodPost, "/api/me/runner-bindings/"+bindingID+"/disconnect", machineOwnerID, bindingID))
	if disconnect.Code != http.StatusOK {
		t.Fatalf("machine owner disconnect status = %d, body = %s", disconnect.Code, disconnect.Body.String())
	}
	if err := testPool.QueryRow(context.Background(), `SELECT disconnected_at FROM agent_runner_binding WHERE id = $1`, bindingID).Scan(&disconnectedAt); err != nil || !disconnectedAt.Valid {
		t.Fatalf("binding was not disconnected: valid=%v err=%v", disconnectedAt.Valid, err)
	}

	unauthorizedReconnect := httptest.NewRecorder()
	h.CreateMyRunnerReconnectCommand(unauthorizedReconnect, runnerAccountRequest(http.MethodPost, "/api/me/runner-bindings/"+bindingID+"/reconnect-command", newAgentOwnerID, bindingID))
	if unauthorizedReconnect.Code != http.StatusNotFound {
		t.Fatalf("new Agent owner reconnect status = %d, want 404", unauthorizedReconnect.Code)
	}
	var reconnectCount int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM runner_reconnect_session WHERE binding_id = $1`, bindingID).Scan(&reconnectCount); err != nil || reconnectCount != 0 {
		t.Fatalf("unauthorized reconnect sessions = %d, err=%v", reconnectCount, err)
	}

	reconnect := httptest.NewRecorder()
	h.CreateMyRunnerReconnectCommand(reconnect, runnerAccountRequest(http.MethodPost, "/api/me/runner-bindings/"+bindingID+"/reconnect-command", machineOwnerID, bindingID))
	if reconnect.Code != http.StatusCreated {
		t.Fatalf("machine owner reconnect status = %d, want 201", reconnect.Code)
	}
	var reconnectResponse struct {
		ReconnectCommand string `json:"reconnect_command"`
		ExpiresAt        string `json:"expires_at"`
	}
	decodeErr := json.Unmarshal(reconnect.Body.Bytes(), &reconnectResponse)
	hasCommand := reconnectResponse.ReconnectCommand != ""
	hasTokenFlag := strings.Contains(reconnectResponse.ReconnectCommand, "--reconnect-token")
	hasExpiry := reconnectResponse.ExpiresAt != ""
	if decodeErr != nil || !hasCommand || !hasTokenFlag || !hasExpiry {
		t.Fatalf("reconnect response invalid: has_command=%v has_token_flag=%v has_expiry=%v err=%v", hasCommand, hasTokenFlag, hasExpiry, decodeErr)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM runner_reconnect_session WHERE binding_id = $1 AND consumed_at IS NULL`, bindingID).Scan(&reconnectCount); err != nil || reconnectCount != 1 {
		t.Fatalf("active reconnect sessions = %d, err=%v", reconnectCount, err)
	}

	unauthorizedRevoke := httptest.NewRecorder()
	h.RevokeMyRunnerBinding(unauthorizedRevoke, runnerAccountRequest(http.MethodDelete, "/api/me/runner-bindings/"+bindingID, newAgentOwnerID, bindingID))
	if unauthorizedRevoke.Code != http.StatusNotFound {
		t.Fatalf("new Agent owner revoke status = %d, want 404", unauthorizedRevoke.Code)
	}
	var revokedAt pgtype.Timestamptz
	if err := testPool.QueryRow(context.Background(), `SELECT revoked_at FROM agent_runner_binding WHERE id = $1`, bindingID).Scan(&revokedAt); err != nil {
		t.Fatalf("read binding after unauthorized revoke: %v", err)
	}
	if revokedAt.Valid {
		t.Fatal("unauthorized revoke changed the binding")
	}

	revoke := httptest.NewRecorder()
	h.RevokeMyRunnerBinding(revoke, runnerAccountRequest(http.MethodDelete, "/api/me/runner-bindings/"+bindingID, machineOwnerID, bindingID))
	if revoke.Code != http.StatusNoContent {
		t.Fatalf("machine owner revoke status = %d, body = %s", revoke.Code, revoke.Body.String())
	}
	var revokedBy pgtype.UUID
	if err := testPool.QueryRow(context.Background(), `SELECT revoked_at, revoked_by FROM agent_runner_binding WHERE id = $1`, bindingID).Scan(&revokedAt, &revokedBy); err != nil || !revokedAt.Valid || uuidToString(revokedBy) != machineOwnerID {
		t.Fatalf("binding revoke state: revoked=%v revoked_by=%s err=%v", revokedAt.Valid, uuidToString(revokedBy), err)
	}
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM runner_reconnect_session WHERE binding_id = $1`, bindingID).Scan(&reconnectCount); err != nil || reconnectCount != 0 {
		t.Fatalf("reconnect sessions after revoke = %d, err=%v", reconnectCount, err)
	}

	var auditCount int
	if err := testPool.QueryRow(context.Background(), `
		SELECT count(*) FROM activity_log
		WHERE workspace_id = $1
		  AND actor_id = $2
		  AND action IN ('runner_binding_disconnected', 'runner_binding_revoked')
	`, workspaceID, machineOwnerID).Scan(&auditCount); err != nil || auditCount != 2 {
		t.Fatalf("Runner account action audit count = %d, err=%v", auditCount, err)
	}
}

func TestListMyRunnerBindingsReturnsEmptyArray(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ownerID := seedRunnerAccountUser(t, "runner-empty-owner")
	w := httptest.NewRecorder()
	testHandler.ListMyRunnerBindings(w, runnerAccountRequest(http.MethodGet, "/api/me/runner-bindings", ownerID, ""))
	if w.Code != http.StatusOK || w.Body.String() != "{\"machines\":[]}\n" {
		t.Fatalf("empty Runner list status = %d, body = %s", w.Code, w.Body.String())
	}
}

func TestListMyRunnerBindingsIncludesMachineWithoutAgentMount(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ownerID := seedRunnerAccountUser(t, "runner-unmounted-owner")
	machineID := seedRunnerAccountMachine(t, ownerID, "Unmounted Machine", 9, true)

	w := httptest.NewRecorder()
	testHandler.ListMyRunnerBindings(w, runnerAccountRequest(http.MethodGet, "/api/me/runner-bindings", ownerID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("ListMyRunnerBindings status = %d, body = %s", w.Code, w.Body.String())
	}
	var response struct {
		Machines []accountRunnerMachineResponse `json:"machines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode Runner account list: %v", err)
	}
	if len(response.Machines) != 1 {
		t.Fatalf("machine count = %d, want 1: %s", len(response.Machines), w.Body.String())
	}
	machine := response.Machines[0]
	if machine.MachineID != machineID || machine.Name != "Unmounted Machine" || !machine.Online {
		t.Fatalf("machine = %#v", machine)
	}
	if len(machine.Bindings) != 0 {
		t.Fatalf("mount count = %d, want 0", len(machine.Bindings))
	}
}

func TestListMyRunnerBindingsSkipsOrphanedAgentMount(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ownerID := seedRunnerAccountUser(t, "runner-orphan-owner")
	workspaceID, _, _, agentID := seedRunnerAccountWorkspaceAgent(t, ownerID, "RunnerOrphan")
	machineID := seedRunnerAccountMachine(t, ownerID, "Orphan Machine", 10, false)
	_ = seedRunnerAccountBinding(t, workspaceID, agentID, machineID, ownerID, false, false)
	if _, err := testPool.Exec(context.Background(), `DELETE FROM agent WHERE id = $1`, agentID); err != nil {
		t.Fatalf("delete mounted Agent: %v", err)
	}

	w := httptest.NewRecorder()
	testHandler.ListMyRunnerBindings(w, runnerAccountRequest(http.MethodGet, "/api/me/runner-bindings", ownerID, ""))
	if w.Code != http.StatusOK {
		t.Fatalf("ListMyRunnerBindings status = %d, body = %s", w.Code, w.Body.String())
	}
	var response struct {
		Machines []accountRunnerMachineResponse `json:"machines"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode Runner account list: %v", err)
	}
	if len(response.Machines) != 1 || response.Machines[0].MachineID != machineID {
		t.Fatalf("machines = %#v, want the paired machine", response.Machines)
	}
	if len(response.Machines[0].Bindings) != 0 {
		t.Fatalf("orphaned mounts = %#v, want none", response.Machines[0].Bindings)
	}
}
