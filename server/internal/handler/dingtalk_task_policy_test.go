package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type fakeDingTalkTaskPolicyReader struct {
	policy         db.GetAgentDingTalkResponsePolicyRow
	identity       db.AgentDingtalkIdentity
	policyErr      error
	identityErr    error
	policyReads    int
	identityReads  int
	identityParams db.GetAgentDingTalkIdentityParams
}

func (f *fakeDingTalkTaskPolicyReader) GetAgentDingTalkResponsePolicy(context.Context, pgtype.UUID) (db.GetAgentDingTalkResponsePolicyRow, error) {
	f.policyReads++
	return f.policy, f.policyErr
}

func (f *fakeDingTalkTaskPolicyReader) GetAgentDingTalkIdentity(_ context.Context, params db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error) {
	f.identityReads++
	f.identityParams = params
	return f.identity, f.identityErr
}

func dingTalkTaskPolicyContext(t *testing.T, managed bool) []byte {
	t.Helper()
	context := persistedDispatchContext{
		Source: DispatchSource{Type: "digital_employee"},
		Domain: "channel", Type: "message.created",
		Outbound:         DispatchOutbound{Mode: protocol.DispatchOutboundModeDWS},
		ExternalIdentity: &persistedDispatchExternalIdentity{DWS: &AgentDispatchDWSIdentity{UID: "123", OrgID: "456"}},
	}
	if managed {
		context.ResponsePolicy = &protocol.DingTalkResponsePolicy{
			Version: 1, Mode: protocol.DingTalkResponseModeCoordinator, Revision: 4, ShowAITag: true,
		}
	}
	raw, err := json.Marshal(context)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDingTalkTaskPolicyCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name     string
		header   string
		mode     string
		metadata string
		want     bool
	}{
		{"new local", protocol.DWSMessagePolicyCapability, "local", `{}`, true},
		{"old local", "", "local", `{"client_capabilities":["dws_message_policy_v1"]}`, false},
		{"new cloud", protocol.DWSMessagePolicyCapability, "cloud", `{"kind":"fc-e2b","capabilities":["dws","dws_message_policy_v1"]}`, true},
		{"old image", protocol.DWSMessagePolicyCapability, "cloud", `{"kind":"fc-e2b","capabilities":["dws"]}`, false},
		{"old cloud daemon", "", "cloud", `{"kind":"fc-e2b","capabilities":["dws_message_policy_v1"]}`, false},
		{"unknown cloud", protocol.DWSMessagePolicyCapability, "cloud", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/claim", nil)
			r.Header.Set("X-Client-Capabilities", tc.header)
			got := dingTalkTaskPolicyCapable(r, db.AgentRuntime{RuntimeMode: tc.mode, Metadata: []byte(tc.metadata)})
			if got != tc.want {
				t.Fatalf("capable = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestResolveDingTalkTaskPolicyFrozenSnapshot(t *testing.T) {
	reader := &fakeDingTalkTaskPolicyReader{policy: db.GetAgentDingTalkResponsePolicyRow{DingtalkShowAiTag: false}}
	policy, err := resolveDingTalkTaskPolicy(context.Background(), reader, db.AgentTaskQueue{Context: dingTalkTaskPolicyContext(t, true)}, db.AgentRuntime{}, true)
	if err != nil {
		t.Fatal(err)
	}
	if policy == nil || !policy.ShowAITag || !policy.PlatformManagedLifecycle {
		t.Fatalf("frozen policy was not preserved: %+v", policy)
	}
	if reader.policyReads != 0 || reader.identityReads != 0 {
		t.Fatal("frozen identity and policy must not drift with later agent settings")
	}
}

func TestResolveDingTalkTaskPolicyFreshBindingSettingsEachClaim(t *testing.T) {
	reader := &fakeDingTalkTaskPolicyReader{identity: db.AgentDingtalkIdentity{DwsUid: "123", OrgID: "456"}}
	task := db.AgentTaskQueue{AgentID: parseUUID("00000000-0000-4000-8000-000000000001")}
	runtime := db.AgentRuntime{WorkspaceID: parseUUID("00000000-0000-4000-8000-000000000002")}
	for _, show := range []bool{true, false, true} {
		reader.policy.DingtalkShowAiTag = show
		policy, err := resolveDingTalkTaskPolicy(context.Background(), reader, task, runtime, true)
		if err != nil {
			t.Fatal(err)
		}
		if policy == nil || policy.ShowAITag != show || policy.PlatformManagedLifecycle {
			t.Fatalf("new claim did not refresh policy: %+v", policy)
		}
	}
	if reader.policyReads != 3 || reader.identityParams.AgentID != task.AgentID || reader.identityParams.WorkspaceID != runtime.WorkspaceID {
		t.Fatalf("binding lookup was not scoped to trusted task/runtime: %+v", reader)
	}
}

func TestResolveDingTalkTaskPolicyScopeAndLegacy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		context   []byte
		capable   bool
		wantNil   bool
		wantError bool
	}{
		{"managed old daemon fails", dingTalkTaskPolicyContext(t, true), false, true, true},
		{"legacy old daemon unchanged", dingTalkTaskPolicyContext(t, false), false, true, false},
		{"unbound task unchanged", nil, true, true, false},
		{"A2A cannot inherit employee binding", []byte(`{"multica_origin":"a2a"}`), true, true, false},
		{"bound external legacy gets label policy", dingTalkTaskPolicyContext(t, false), true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reader := &fakeDingTalkTaskPolicyReader{identityErr: pgx.ErrNoRows}
			policy, err := resolveDingTalkTaskPolicy(context.Background(), reader, db.AgentTaskQueue{Context: tc.context}, db.AgentRuntime{}, tc.capable)
			if (err != nil) != tc.wantError || (policy == nil) != tc.wantNil {
				t.Fatalf("policy=%+v error=%v", policy, err)
			}
			if !tc.capable && reader.policyReads+reader.identityReads != 0 {
				t.Fatal("old runtime should not query new policy or bindings")
			}
			if policy != nil && policy.PlatformManagedLifecycle {
				t.Fatal("legacy task must retain legacy platform lifecycle")
			}
		})
	}
}

func TestResolveDingTalkTaskPolicyRejectsInvalidPolicyAndIdentity(t *testing.T) {
	for _, raw := range []string{
		`{"dispatch_response_policy":{"version":1,"mode":"multica_coordinator","revision":0}}`,
		`{"external_identity":{"dws":{"uid":"not-a-uid","orgId":"456"}}}`,
	} {
		policy, err := resolveDingTalkTaskPolicy(context.Background(), &fakeDingTalkTaskPolicyReader{}, db.AgentTaskQueue{Context: []byte(raw)}, db.AgentRuntime{}, true)
		if policy != nil || err == nil {
			t.Fatalf("invalid context accepted: %s", raw)
		}
	}
	reader := &fakeDingTalkTaskPolicyReader{identityErr: errors.New("database unavailable")}
	if _, err := resolveDingTalkTaskPolicy(context.Background(), reader, db.AgentTaskQueue{}, db.AgentRuntime{}, true); err == nil {
		t.Fatal("binding lookup failure must not silently skip policy enforcement")
	}
}

func TestResolveDingTalkTaskPolicyLifecycleRequiresExactSourceAndEvent(t *testing.T) {
	for _, update := range []func(*persistedDispatchContext){
		func(c *persistedDispatchContext) { c.Source.Type = "robot" },
		func(c *persistedDispatchContext) { c.Domain = "calendar" },
		func(c *persistedDispatchContext) { c.Type = "reaction.added" },
		func(c *persistedDispatchContext) { c.Outbound.Mode = "robot_sdk" },
		func(c *persistedDispatchContext) { c.ResponsePolicy.Mode = protocol.DingTalkResponseModeLegacy },
	} {
		var stored persistedDispatchContext
		if err := json.Unmarshal(dingTalkTaskPolicyContext(t, true), &stored); err != nil {
			t.Fatal(err)
		}
		update(&stored)
		raw, err := json.Marshal(stored)
		if err != nil {
			t.Fatal(err)
		}
		policy, err := resolveDingTalkTaskPolicy(context.Background(), &fakeDingTalkTaskPolicyReader{}, db.AgentTaskQueue{Context: raw}, db.AgentRuntime{}, true)
		if err != nil {
			t.Fatal(err)
		}
		if policy == nil || policy.PlatformManagedLifecycle {
			t.Fatalf("wrong lifecycle: %+v", policy)
		}
	}
}

func TestLocalDingTalkClientCapabilitiesWhitelist(t *testing.T) {
	for _, tc := range []struct {
		header string
		want   []string
	}{
		{"", []string{}},
		{"future-capability", []string{}},
		{"x-dws_message_policy_v1", []string{}},
		{" dws_message_policy_v1, dws_message_policy_v1, unknown ", []string{protocol.DWSMessagePolicyCapability}},
	} {
		if got := localDingTalkClientCapabilities(tc.header); !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("header %q: got %v, want %v", tc.header, got, tc.want)
		}
	}
}

func TestPersistLocalDingTalkClientCapabilitiesClearsOldDaemonAndPreservesMetadata(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "DingTalk capabilities runtime")
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET runtime_mode = 'local', metadata = '{"version":"kept","client_capabilities":["dws_message_policy_v1","untrusted"]}'::jsonb WHERE id = $1`, runtimeID); err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"", "future-capability,dws_message_policy_v1", ""} {
		runtime, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(runtimeID))
		if err != nil {
			t.Fatal(err)
		}
		if err := testHandler.persistLocalDingTalkClientCapabilities(ctx, runtime, header); err != nil {
			t.Fatal(err)
		}
		updated, err := testHandler.Queries.GetAgentRuntime(ctx, runtime.ID)
		if err != nil {
			t.Fatal(err)
		}
		var metadata struct {
			Version      string   `json:"version"`
			Capabilities []string `json:"client_capabilities"`
		}
		if err := json.Unmarshal(updated.Metadata, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata.Version != "kept" || !reflect.DeepEqual(metadata.Capabilities, localDingTalkClientCapabilities(header)) {
			t.Fatalf("wrong persisted metadata: %s", updated.Metadata)
		}
	}
}

func TestPersistLocalDingTalkClientCapabilitiesDoesNotTouchCloud(t *testing.T) {
	handler := &Handler{}
	if err := handler.persistLocalDingTalkClientCapabilities(context.Background(), db.AgentRuntime{RuntimeMode: "cloud"}, protocol.DWSMessagePolicyCapability); err != nil {
		t.Fatal(err)
	}
}

func TestBuildClaimedTaskResponseDingTalkPolicyAndSkillBundle(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}
	ctx := context.Background()
	runtimeID := createClaimReclaimRuntime(t, ctx, "DingTalk policy claim runtime")
	if _, err := testPool.Exec(ctx, `UPDATE agent_runtime SET provider = 'hermes', metadata = '{"kind":"fc-e2b","capabilities":["hermes","dws","dws_message_policy_v1"]}'::jsonb WHERE id = $1`, runtimeID); err != nil {
		t.Fatal(err)
	}
	agentID, issueID := createClaimReclaimAgentAndIssue(t, ctx, runtimeID, "DingTalk policy claim agent")
	taskID := createDispatchedClaimFixtureTask(t, ctx, agentID, runtimeID, issueID, "0 seconds", false)
	if _, err := testPool.Exec(ctx, `UPDATE agent_task_queue SET context = $2::jsonb WHERE id = $1`, taskID, dingTalkTaskPolicyContext(t, true)); err != nil {
		t.Fatal(err)
	}
	runtime, err := testHandler.Queries.GetAgentRuntime(ctx, parseUUID(runtimeID))
	if err != nil {
		t.Fatal(err)
	}
	task, err := testHandler.Queries.GetAgentTask(ctx, parseUUID(taskID))
	if err != nil {
		t.Fatal(err)
	}
	req := newDaemonTokenRequest(http.MethodPost, "/claim", nil, testWorkspaceID, "dingtalk-policy-claim")
	req.Header.Set("X-Client-Capabilities", protocol.DWSMessagePolicyCapability+","+protocol.DaemonCapabilitySkillBundlesV1)
	response, _, _, _, failure := testHandler.buildClaimedTaskResponse(req, &task, runtime, service.SandboxBackendAliyunFC, runtimeID, testWorkspaceID)
	if failure != nil {
		t.Fatalf("claim failed: %+v", failure)
	}
	if response.DingTalkMessagePolicy == nil || !response.DingTalkMessagePolicy.ShowAITag || !response.DingTalkMessagePolicy.PlatformManagedLifecycle {
		t.Fatalf("missing claim snapshot: %+v", response.DingTalkMessagePolicy)
	}
	if response.Agent == nil {
		t.Fatal("missing agent")
	}
	var selected service.AgentSkillRefData
	for _, ref := range response.Agent.SkillRefs {
		if ref.ID == "builtin:multica-dws" {
			selected = ref
		}
	}
	if selected.Hash == "" {
		t.Fatal("missing policy-aware DWS bundle ref")
	}
	body := resolveSkillBundlesRequest{Skills: []resolveSkillBundleRef{{ID: selected.ID, Source: selected.Source, Hash: selected.Hash}}}
	w := httptest.NewRecorder()
	req = newDaemonTokenRequest(http.MethodPost, "/resolve", body, testWorkspaceID, "dingtalk-policy-claim")
	req.Header.Set("X-Client-Capabilities", protocol.DWSMessagePolicyCapability)
	req = withURLParams(req, "runtimeId", runtimeID, "taskId", taskID)
	testHandler.ResolveTaskSkillBundles(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve: %d: %s", w.Code, w.Body.String())
	}
	var result struct {
		Bundles []service.AgentSkillData `json:"bundles"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if len(result.Bundles) != 1 || result.Bundles[0].Hash != selected.Hash || strings.Contains(result.Bundles[0].Content, "--ai-tag=false") {
		t.Fatalf("wrong resolved policy bundle: %+v", result.Bundles)
	}

	// An old daemon cannot launch the already-managed task or report policy enforcement.
	req.Header.Del("X-Client-Capabilities")
	_, _, _, _, failure = testHandler.buildClaimedTaskResponse(req, &task, runtime, service.SandboxBackendAliyunFC, runtimeID, testWorkspaceID)
	if failure == nil || failure.status != http.StatusConflict {
		t.Fatalf("old daemon claim should fail: %+v", failure)
	}
	failedTask, err := testHandler.Queries.GetAgentTask(ctx, task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if failedTask.Status != "failed" || failedTask.FailureReason.String != "agent_error.runtime_version_unsupported" {
		t.Fatalf("capability failure was not terminal: status=%s reason=%s", failedTask.Status, failedTask.FailureReason.String)
	}
}
