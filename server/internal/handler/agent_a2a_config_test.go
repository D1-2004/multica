package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/auth"
	"github.com/multica-ai/multica/server/internal/featureflags"
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const agentA2ATestManagedOpenCodeRuntimeMetadata = `{
	"kind":"cloud-sandbox",
	"sandbox_backend":"aliyun_fc",
	"provider":"opencode",
	"artifact_kind":"e2b_template",
	"artifact_channel":"candidate",
	"artifact_ref":"template-a2a-m5",
	"manifest_version":5,
	"runner_protocol":"root-log-v1",
	"capabilities":["opencode","dws","mcp","a2a_inbound_opencode_v1"]
}`

const agentA2ATestManagedOpenCodeRuntimeMetadataWithoutA2A = `{
	"kind":"cloud-sandbox",
	"sandbox_backend":"aliyun_fc",
	"provider":"opencode",
	"artifact_kind":"e2b_template",
	"artifact_channel":"candidate",
	"artifact_ref":"template-pre-a2a",
	"manifest_version":4,
	"runner_protocol":"root-log-v1",
	"capabilities":["opencode","dws","mcp"]
}`

func TestNormalizeAgentA2APublicBaseURL(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		want    string
		wantErr bool
	}{
		{name: "https", input: " https://api.example.test/ ", want: "https://api.example.test"},
		{name: "loopback IPv4", input: "http://127.0.0.1:8080", want: "http://127.0.0.1:8080"},
		{name: "loopback IPv6", input: "http://[::1]:8080", want: "http://[::1]:8080"},
		{name: "localhost", input: "http://localhost:8080", want: "http://localhost:8080"},
		{name: "public HTTP", input: "http://api.example.test", wantErr: true},
		{name: "relative", input: "/api", wantErr: true},
		{name: "userinfo", input: "https://user@example.test", wantErr: true},
		{name: "query", input: "https://api.example.test?host=evil.test", wantErr: true},
		{name: "empty", input: "", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := normalizeAgentA2APublicBaseURL(test.input)
			if (err != nil) != test.wantErr {
				t.Fatalf("normalizeAgentA2APublicBaseURL(%q) error = %v, wantErr %v", test.input, err, test.wantErr)
			}
			if got != test.want {
				t.Fatalf("normalizeAgentA2APublicBaseURL(%q) = %q, want %q", test.input, got, test.want)
			}
		})
	}
}

func TestNormalizeAgentA2AScopes(t *testing.T) {
	got, err := normalizeAgentA2AScopes([]string{"read", "send", "read"})
	if err != nil {
		t.Fatalf("normalizeAgentA2AScopes: %v", err)
	}
	want := []string{"send", "read"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("normalized scopes = %v, want %v", got, want)
	}
	for _, unsupported := range []string{"list", "cancel", "admin"} {
		if _, err := normalizeAgentA2AScopes([]string{"send", unsupported}); err == nil {
			t.Fatalf("unsupported scope %q must be rejected", unsupported)
		}
	}
	if _, err := normalizeAgentA2AScopes(nil); err == nil {
		t.Fatal("empty scopes must be rejected")
	}
}

func TestNormalizeAgentA2ACardSkillsUsesOfficialWireFields(t *testing.T) {
	raw := json.RawMessage(`[{
  "id":"build",
  "name":"Build project",
  "description":"Builds and tests a local project.",
  "tags":["coding"],
  "inputModes":["text/plain"],
  "outputModes":["text/plain"],
  "securityRequirements":[{"untrusted":[]}]
}]`)
	skills, encoded, err := normalizeAgentA2ACardSkills(raw)
	if err != nil {
		t.Fatalf("normalizeAgentA2ACardSkills: %v", err)
	}
	if len(skills) != 1 || skills[0].ID != "build" {
		t.Fatalf("skills = %#v", skills)
	}
	if skills[0].SecurityRequirements != nil {
		t.Fatal("per-skill security requirements must not be persisted")
	}
	if strings.Contains(string(encoded), "securityRequirements") {
		t.Fatalf("encoded skills unexpectedly contain per-skill security: %s", encoded)
	}
	if _, _, err := normalizeAgentA2ACardSkills(json.RawMessage(`[{"id":"x","name":"x","description":"x","tags":[],"inputModes":["application/json"]}]`)); err == nil {
		t.Fatal("non-text skill mode must be rejected")
	}
}

func TestAgentA2AEndpointPresentationUsesConfiguredPublicURL(t *testing.T) {
	allowUnsafeLocalAgentA2ARuntimeForTest(t)
	ownerID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	agentID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	endpointID := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	now := pgtype.Timestamptz{Time: testNow(), Valid: true}
	h := &Handler{cfg: Config{PublicURL: "http://127.0.0.1:8080/base"}}
	agent := db.Agent{
		ID:        agentID,
		OwnerID:   ownerID,
		RuntimeID: util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
	}
	endpoint := db.AgentA2aEndpoint{
		ID:                endpointID,
		AgentID:           agentID,
		PublicAgentID:     "public-agent-opaque-id",
		Enabled:           true,
		DelegatedByUserID: ownerID,
		CardName:          "Coding Agent",
		CardDescription:   "Builds local projects.",
		CardVersion:       "1.0.0",
		CardSkills:        []byte(`[]`),
		CreatedAt:         now,
		UpdatedAt:         now,
	}

	response, card, err := h.agentA2AEndpointPresentation(agent, true, endpoint)
	if err != nil {
		t.Fatalf("agentA2AEndpointPresentation: %v", err)
	}
	if !response.Enabled {
		t.Fatal("valid endpoint should be effectively enabled")
	}
	if response.CardURL != "http://127.0.0.1:8080/base/api/a2a/agents/public-agent-opaque-id/.well-known/agent-card.json" {
		t.Fatalf("card URL = %q", response.CardURL)
	}
	if response.RPCURL != "http://127.0.0.1:8080/base/api/a2a/agents/public-agent-opaque-id/v1" {
		t.Fatalf("RPC URL = %q", response.RPCURL)
	}
	if card == nil || len(card.SupportedInterfaces) != 1 || card.SupportedInterfaces[0].URL != response.RPCURL {
		t.Fatalf("card does not use the canonical RPC URL: %#v", card)
	}

	response, _, err = h.agentA2AEndpointPresentation(agent, false, endpoint)
	if err != nil {
		t.Fatalf("runtime-ineligible presentation: %v", err)
	}
	if response.Enabled {
		t.Fatal("runtime ineligibility must fail closed without clearing stored enabled")
	}

	// Owner drift is an effective kill switch even if the stored enabled bit
	// remains true until the new owner explicitly saves the configuration.
	agent.OwnerID = util.MustParseUUID("55555555-5555-5555-5555-555555555555")
	response, _, err = h.agentA2AEndpointPresentation(agent, true, endpoint)
	if err != nil {
		t.Fatalf("owner-drift presentation: %v", err)
	}
	if response.Enabled {
		t.Fatal("owner drift must fail closed")
	}
}

func TestAgentA2AEndpointPresentationFailsClosedWithoutRuntimeSafety(t *testing.T) {
	t.Setenv("APP_ENV", "production")
	t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, "true")
	ownerID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	agentID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	h := &Handler{cfg: Config{PublicURL: "https://public.example.test/base"}}

	response, card, err := h.agentA2AEndpointPresentation(db.Agent{
		ID:        agentID,
		OwnerID:   ownerID,
		RuntimeID: util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
	}, true, db.AgentA2aEndpoint{
		ID:                util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
		AgentID:           agentID,
		PublicAgentID:     "public-agent-opaque-id",
		Enabled:           true,
		DelegatedByUserID: ownerID,
		CardName:          "Coding Agent",
		CardVersion:       "1.0.0",
		CardSkills:        []byte(`[]`),
	})
	if err != nil {
		t.Fatalf("agentA2AEndpointPresentation: %v", err)
	}
	if response.Enabled {
		t.Fatal("stored endpoint must be effectively disabled outside the runtime safety exemption")
	}
	if response.CardURL == "" || response.RPCURL == "" || card == nil {
		t.Fatalf("owner-only export metadata should remain available: %#v, card=%#v", response, card)
	}
}

func TestAgentA2AEndpointPresentationWithoutPublicURLStaysManageable(t *testing.T) {
	ownerID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	agentID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	h := &Handler{cfg: Config{}}
	response, card, err := h.agentA2AEndpointPresentation(db.Agent{
		ID:        agentID,
		OwnerID:   ownerID,
		RuntimeID: util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
	}, true, db.AgentA2aEndpoint{
		ID:                util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
		AgentID:           agentID,
		PublicAgentID:     "public-agent-opaque-id",
		Enabled:           true,
		DelegatedByUserID: ownerID,
		CardName:          "Coding Agent",
		CardVersion:       "1.0.0",
		CardSkills:        []byte(`[]`),
	})
	if err != nil {
		t.Fatalf("agentA2AEndpointPresentation: %v", err)
	}
	if response.Enabled || response.CardURL != "" || response.RPCURL != "" || card != nil {
		t.Fatalf("misconfigured endpoint must fail closed without inventing request-host URLs: %#v, card=%#v", response, card)
	}
}

func TestAgentA2AManagementRequiresExactHumanOwner(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	agentID, ownerID, _ := privateAgentTestFixture(t)
	withFeatureFlag(t, testHandler, featureflags.AgentA2AInbound, true)

	// A workspace owner/admin is not allowed to manage another user's Agent.
	response := httptest.NewRecorder()
	testHandler.GetAgentA2AConfig(response, withAgentA2AURLParams(
		newRequest(http.MethodGet, "/api/agents/"+agentID+"/a2a", nil),
		"id", agentID,
	))
	if response.Code != http.StatusNotFound {
		t.Fatalf("workspace owner management status = %d, want 404: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	testHandler.GetAgentA2AConfig(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodGet, "/api/agents/"+agentID+"/a2a", nil),
		"id", agentID,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("exact owner management status = %d, want 200: %s", response.Code, response.Body.String())
	}

	machineRequest := withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodGet, "/api/agents/"+agentID+"/a2a", nil),
		"id", agentID,
	)
	machineRequest.Header.Set("X-Actor-Source", "task_token")
	response = httptest.NewRecorder()
	testHandler.GetAgentA2AConfig(response, machineRequest)
	if response.Code != http.StatusForbidden {
		t.Fatalf("machine owner management status = %d, want 403: %s", response.Code, response.Body.String())
	}

	withFeatureFlag(t, testHandler, featureflags.AgentA2AInbound, false)
	response = httptest.NewRecorder()
	testHandler.GetAgentA2AConfig(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodGet, "/api/agents/"+agentID+"/a2a", nil),
		"id", agentID,
	))
	if response.Code != http.StatusNotFound {
		t.Fatalf("flag-off management status = %d, want 404: %s", response.Code, response.Body.String())
	}
}

func TestAgentA2AEnableRequiresRuntimeSafetyExemption(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	agentID, ownerID, _ := privateAgentTestFixture(t)
	assignAgentA2ATestRuntime(t, agentID, "local", "claude")
	withFeatureFlag(t, testHandler, featureflags.AgentA2AInbound, true)
	originalProvider := testHandler.configProvider
	testHandler.SetConfigProvider(func() Config {
		return Config{PublicURL: "http://127.0.0.1:8080"}
	})
	t.Cleanup(func() { testHandler.SetConfigProvider(originalProvider) })

	putEnabled := func() *httptest.ResponseRecorder {
		t.Helper()
		response := httptest.NewRecorder()
		testHandler.UpdateAgentA2AConfig(response, withAgentA2AURLParams(
			newRequestAs(ownerID, http.MethodPut, "/api/agents/"+agentID+"/a2a", map[string]any{
				"enabled":          true,
				"card_name":        "Coding Agent",
				"card_description": "Builds local projects.",
				"card_version":     "1.0.0",
				"card_skills":      []any{},
			}),
			"id", agentID,
		))
		return response
	}

	t.Setenv("APP_ENV", "development")
	t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, "")
	response := putEnabled()
	if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "runtime credential and tool-shell isolation") {
		t.Fatalf("default enable response = %d %s, want clear 403", response.Code, response.Body.String())
	}

	t.Setenv("APP_ENV", "production")
	t.Setenv(agentA2AAllowUnsafeLocalRuntimeEnv, "true")
	response = putEnabled()
	if response.Code != http.StatusForbidden {
		t.Fatalf("production override response = %d, want 403: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	testHandler.UpdateAgentA2AConfig(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodPut, "/api/agents/"+agentID+"/a2a", map[string]any{
			"enabled":          false,
			"card_name":        "Coding Agent",
			"card_description": "Builds local projects.",
			"card_version":     "1.0.0",
			"card_skills":      []any{},
		}),
		"id", agentID,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("production disable response = %d, want 200: %s", response.Code, response.Body.String())
	}
}

func TestAgentA2AEnableIsRuntimeVersionAgnostic(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	configureAgentA2ATestHandler(t)

	tests := []struct {
		name        string
		runtimeMode string
		provider    string
		metadata    string
		wantAllowed bool
	}{
		{
			name:        "local Claude",
			runtimeMode: "local",
			provider:    "claude",
			metadata:    `{}`,
			wantAllowed: true,
		},
		{
			name:        "managed OpenCode before A2A capability",
			runtimeMode: "cloud",
			provider:    "opencode",
			metadata:    agentA2ATestManagedOpenCodeRuntimeMetadataWithoutA2A,
			wantAllowed: true,
		},
		{
			name:        "managed OpenCode current capability",
			runtimeMode: "cloud",
			provider:    "opencode",
			metadata:    agentA2ATestManagedOpenCodeRuntimeMetadata,
			wantAllowed: true,
		},
		{
			name:        "legacy managed OpenCode metadata",
			runtimeMode: "cloud",
			provider:    "opencode",
			metadata:    `{"kind":"fc-e2b","template":"legacy-opencode","runner":"multica-fc-opencode-runner"}`,
			wantAllowed: true,
		},
		{
			name:        "cloud Claude",
			runtimeMode: "cloud",
			provider:    "claude",
			metadata:    `{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc","provider":"claude","artifact_kind":"e2b_template","artifact_ref":"template"}`,
		},
		{
			name:        "local OpenCode",
			runtimeMode: "local",
			provider:    "opencode",
			metadata:    `{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc"}`,
		},
		{
			name:        "cloud OpenCode missing managed metadata",
			runtimeMode: "cloud",
			provider:    "opencode",
			metadata:    `{}`,
		},
		{
			name:        "cloud OpenCode metadata provider drift",
			runtimeMode: "cloud",
			provider:    "opencode",
			metadata:    `{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc","provider":"hermes","artifact_kind":"e2b_template","artifact_ref":"template-a2a-m5","capabilities":["a2a_inbound_opencode_v1"]}`,
		},
		{
			name:        "cloud OpenCode blank artifact ref",
			runtimeMode: "cloud",
			provider:    "opencode",
			metadata:    `{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc","provider":"opencode","artifact_kind":"e2b_template","artifact_ref":"   ","capabilities":["a2a_inbound_opencode_v1"]}`,
		},
		{
			name:        "cloud OpenCode wrong sandbox backend",
			runtimeMode: "cloud",
			provider:    "opencode",
			metadata:    `{"kind":"cloud-sandbox","sandbox_backend":"asb"}`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agentID, ownerID, _ := privateAgentTestFixture(t)
			assignAgentA2ATestRuntime(t, agentID, test.runtimeMode, test.provider, test.metadata)

			response := putAgentA2ATestConfig(t, agentID, ownerID, true)
			wantStatus := http.StatusBadRequest
			if test.wantAllowed {
				wantStatus = http.StatusOK
			}
			if response.Code != wantStatus {
				t.Fatalf("enable response = %d %s, want %d", response.Code, response.Body.String(), wantStatus)
			}

			var enabledEndpointExists bool
			if err := testPool.QueryRow(context.Background(), `
				SELECT EXISTS (
					SELECT 1 FROM agent_a2a_endpoint
					WHERE agent_id = $1 AND enabled = TRUE
				)
			`, agentID).Scan(&enabledEndpointExists); err != nil {
				t.Fatalf("check rejected endpoint: %v", err)
			}
			if enabledEndpointExists != test.wantAllowed {
				t.Fatalf("enabled endpoint exists = %v, want %v", enabledEndpointExists, test.wantAllowed)
			}
		})
	}
}

func TestAgentA2AManagedHostedRuntimeIsPublishedAndAdmitted(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	configureAgentA2ATestHandler(t)
	agentID, ownerID, _ := privateAgentTestFixture(t)
	runtimeID := assignAgentA2ATestRuntime(
		t,
		agentID,
		"cloud",
		"opencode",
		agentA2ATestManagedOpenCodeRuntimeMetadata,
	)
	endpoint, client, secret := createAgentA2ATestCaller(t, agentID, ownerID)

	ownerResponse := httptest.NewRecorder()
	testHandler.GetAgentA2AConfig(ownerResponse, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodGet, "/api/agents/"+agentID+"/a2a", nil),
		"id", agentID,
	))
	if ownerResponse.Code != http.StatusOK {
		t.Fatalf("managed runtime owner config status = %d, want 200: %s", ownerResponse.Code, ownerResponse.Body.String())
	}
	var ownerConfig AgentA2AConfigResponse
	if err := json.Unmarshal(ownerResponse.Body.Bytes(), &ownerConfig); err != nil {
		t.Fatalf("decode managed runtime owner config: %v", err)
	}
	if ownerConfig.Endpoint == nil || !ownerConfig.Endpoint.Enabled {
		t.Fatalf("managed runtime owner config endpoint = %#v, want effectively enabled", ownerConfig.Endpoint)
	}

	published, err := testHandler.Queries.GetPublishedAgentA2AEndpointByPublicID(
		context.Background(),
		endpoint.PublicAgentID,
	)
	if err != nil {
		t.Fatalf("publish managed runtime endpoint: %v", err)
	}
	if uuidToString(published.AgentRuntimeID) != runtimeID {
		t.Fatalf("published runtime = %s, want %s", uuidToString(published.AgentRuntimeID), runtimeID)
	}

	admission, err := testHandler.Queries.LockAgentA2ASendAdmission(
		context.Background(),
		db.LockAgentA2ASendAdmissionParams{
			AgentID:       util.MustParseUUID(agentID),
			WorkspaceID:   util.MustParseUUID(testWorkspaceID),
			EndpointID:    util.MustParseUUID(endpoint.ID),
			PublicAgentID: endpoint.PublicAgentID,
			ClientID:      util.MustParseUUID(client.ID),
			CredentialID:  util.MustParseUUID(secret.Credential.ID),
		},
	)
	if err != nil {
		t.Fatalf("admit managed runtime caller: %v", err)
	}
	if uuidToString(admission.AgentRuntimeID) != runtimeID {
		t.Fatalf("admitted runtime = %s, want %s", uuidToString(admission.AgentRuntimeID), runtimeID)
	}
}

func TestAgentA2AClientRejectsUnimplementedFirstSliceControls(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	configureAgentA2ATestHandler(t)
	agentID, ownerID, _ := privateAgentTestFixture(t)
	assignAgentA2ATestRuntime(t, agentID, "local", "claude")

	if response := putAgentA2ATestConfig(t, agentID, ownerID, true); response.Code != http.StatusOK {
		t.Fatalf("enable A2A status = %d, want 200: %s", response.Code, response.Body.String())
	}

	createCases := []struct {
		name      string
		body      map[string]any
		wantError string
	}{
		{name: "list scope", body: map[string]any{"name": "list", "scopes": []string{"send", "list"}}, wantError: "send and read"},
		{name: "cancel scope", body: map[string]any{"name": "cancel", "scopes": []string{"cancel"}}, wantError: "send and read"},
		{name: "rate limit", body: map[string]any{"name": "rate", "rate_limit_per_minute": 10}, wantError: "rate_limit_per_minute is not supported"},
		{name: "concurrency limit", body: map[string]any{"name": "concurrency", "max_concurrent_tasks": 2}, wantError: "max_concurrent_tasks is not supported"},
	}
	for _, test := range createCases {
		t.Run("create "+test.name, func(t *testing.T) {
			response := createAgentA2ATestClient(t, agentID, ownerID, test.body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.wantError) {
				t.Fatalf("create response = %d %s, want clear 400 containing %q", response.Code, response.Body.String(), test.wantError)
			}
		})
	}

	response := createAgentA2ATestClient(t, agentID, ownerID, map[string]any{
		"name":                  "supported-client",
		"scopes":                []string{"read", "send"},
		"rate_limit_per_minute": nil,
		"max_concurrent_tasks":  nil,
	})
	if response.Code != http.StatusCreated {
		t.Fatalf("supported create status = %d, want 201: %s", response.Code, response.Body.String())
	}
	var client AgentA2AClientResponse
	if err := json.Unmarshal(response.Body.Bytes(), &client); err != nil {
		t.Fatalf("decode supported client: %v", err)
	}

	if _, err := testPool.Exec(context.Background(), `
		UPDATE a2a_client
		SET rate_limit_per_minute = 10,
		    max_concurrent_tasks = 2
		WHERE id = $1
	`, client.ID); err != nil {
		t.Fatalf("seed future client controls: %v", err)
	}

	updateCases := []struct {
		name      string
		body      map[string]any
		wantError string
	}{
		{name: "list scope", body: map[string]any{"scopes": []string{"list"}}, wantError: "send and read"},
		{name: "cancel scope", body: map[string]any{"scopes": []string{"send", "cancel"}}, wantError: "send and read"},
		{name: "rate limit", body: map[string]any{"rate_limit_per_minute": 20}, wantError: "rate_limit_per_minute is not supported"},
		{name: "concurrency limit", body: map[string]any{"max_concurrent_tasks": 3}, wantError: "max_concurrent_tasks is not supported"},
	}
	for _, test := range updateCases {
		t.Run("update "+test.name, func(t *testing.T) {
			response := updateAgentA2ATestClient(t, agentID, ownerID, client.ID, test.body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), test.wantError) {
				t.Fatalf("update response = %d %s, want clear 400 containing %q", response.Code, response.Body.String(), test.wantError)
			}
		})
	}

	response = updateAgentA2ATestClient(t, agentID, ownerID, client.ID, map[string]any{
		"name":                  "supported-client-updated",
		"rate_limit_per_minute": nil,
		"max_concurrent_tasks":  nil,
	})
	if response.Code != http.StatusOK {
		t.Fatalf("null-control update status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var controlsCleared bool
	if err := testPool.QueryRow(context.Background(), `
		SELECT rate_limit_per_minute IS NULL AND max_concurrent_tasks IS NULL
		FROM a2a_client
		WHERE id = $1
	`, client.ID).Scan(&controlsCleared); err != nil {
		t.Fatalf("load cleared client controls: %v", err)
	}
	if !controlsCleared {
		t.Fatal("explicit null must clear legacy unimplemented client controls")
	}
}

func TestAgentA2ASendRemainsAvailableAcrossRuntimeVersionChanges(t *testing.T) {
	if testHandler == nil || testHandler.A2AService == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	configureAgentA2ATestHandler(t)

	tests := []struct {
		name          string
		runtimeMode   string
		provider      string
		metadata      string
		wantAvailable bool
	}{
		{name: "runtime switched to cloud Claude", runtimeMode: "cloud", provider: "claude", metadata: `{}`},
		{name: "runtime switched to unsupported local provider", runtimeMode: "local", provider: "codebuddy", metadata: `{}`},
		{
			name:        "runtime switched to unmanaged cloud OpenCode",
			runtimeMode: "cloud",
			provider:    "opencode",
			metadata:    `{"kind":"cloud-sandbox","sandbox_backend":"asb"}`,
		},
		{
			name:          "runtime switched to pre-A2A managed OpenCode template",
			runtimeMode:   "cloud",
			provider:      "opencode",
			metadata:      agentA2ATestManagedOpenCodeRuntimeMetadataWithoutA2A,
			wantAvailable: true,
		},
		{
			name:          "runtime switched to current managed OpenCode template",
			runtimeMode:   "cloud",
			provider:      "opencode",
			metadata:      agentA2ATestManagedOpenCodeRuntimeMetadata,
			wantAvailable: true,
		},
		{
			name:          "runtime switched to legacy managed OpenCode metadata",
			runtimeMode:   "cloud",
			provider:      "opencode",
			metadata:      `{"kind":"fc-e2b","template":"legacy-opencode","runner":"multica-fc-opencode-runner"}`,
			wantAvailable: true,
		},
		{
			name:        "runtime metadata provider drifted away from OpenCode",
			runtimeMode: "cloud",
			provider:    "opencode",
			metadata:    `{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc","provider":"hermes","artifact_kind":"e2b_template","artifact_ref":"template-a2a-m5","capabilities":["a2a_inbound_opencode_v1"]}`,
		},
		{
			name:        "runtime metadata artifact ref became blank",
			runtimeMode: "cloud",
			provider:    "opencode",
			metadata:    `{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc","provider":"opencode","artifact_kind":"e2b_template","artifact_ref":"   ","capabilities":["a2a_inbound_opencode_v1"]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			agentID, ownerID, _ := privateAgentTestFixture(t)
			runtimeID := assignAgentA2ATestRuntime(t, agentID, "local", "claude")
			endpoint, client, secret := createAgentA2ATestCaller(t, agentID, ownerID)
			localCardRequest := httptest.NewRequest(
				http.MethodGet,
				"/api/a2a/agents/"+endpoint.PublicAgentID+"/.well-known/agent-card.json",
				nil,
			)
			localCardRequest.RemoteAddr = "127.0.0.1:49152"
			localCardRequest = withAgentA2AURLParams(localCardRequest, "publicAgentId", endpoint.PublicAgentID)
			localCardResponse := httptest.NewRecorder()
			testHandler.GetAgentA2ACard(localCardResponse, localCardRequest)
			if localCardResponse.Code != http.StatusOK {
				t.Fatalf("local Claude anonymous Card status = %d, want 200: %s", localCardResponse.Code, localCardResponse.Body.String())
			}
			admissionTx, err := testPool.Begin(context.Background())
			if err != nil {
				t.Fatalf("begin local admission transaction: %v", err)
			}
			admission, admissionErr := testHandler.Queries.WithTx(admissionTx).LockAgentA2ASendAdmission(
				context.Background(),
				db.LockAgentA2ASendAdmissionParams{
					AgentID:       util.MustParseUUID(agentID),
					WorkspaceID:   util.MustParseUUID(testWorkspaceID),
					EndpointID:    util.MustParseUUID(endpoint.ID),
					PublicAgentID: endpoint.PublicAgentID,
					ClientID:      util.MustParseUUID(client.ID),
					CredentialID:  util.MustParseUUID(secret.Credential.ID),
				},
			)
			if admissionErr != nil {
				_ = admissionTx.Rollback(context.Background())
				t.Fatalf("local Claude admission: %v", admissionErr)
			}
			if uuidToString(admission.AgentRuntimeID) != runtimeID {
				_ = admissionTx.Rollback(context.Background())
				t.Fatalf("admitted runtime = %s, want %s", uuidToString(admission.AgentRuntimeID), runtimeID)
			}
			if err := admissionTx.Rollback(context.Background()); err != nil {
				t.Fatalf("release local admission transaction: %v", err)
			}

			if _, err := testPool.Exec(context.Background(), `
				UPDATE agent_runtime
				SET runtime_mode = $2, provider = $3, metadata = $4::jsonb, updated_at = now()
				WHERE id = $1
			`, runtimeID, test.runtimeMode, test.provider, test.metadata); err != nil {
				t.Fatalf("switch runtime eligibility: %v", err)
			}

			ownerResponse := httptest.NewRecorder()
			testHandler.GetAgentA2AConfig(ownerResponse, withAgentA2AURLParams(
				newRequestAs(ownerID, http.MethodGet, "/api/agents/"+agentID+"/a2a", nil),
				"id", agentID,
			))
			if ownerResponse.Code != http.StatusOK {
				t.Fatalf("owner config after runtime switch status = %d, want 200: %s", ownerResponse.Code, ownerResponse.Body.String())
			}
			var ownerConfig AgentA2AConfigResponse
			if err := json.Unmarshal(ownerResponse.Body.Bytes(), &ownerConfig); err != nil {
				t.Fatalf("decode owner config after runtime switch: %v", err)
			}
			if ownerConfig.Endpoint == nil || ownerConfig.Endpoint.Enabled != test.wantAvailable {
				t.Fatalf("owner config endpoint = %#v, want available %v", ownerConfig.Endpoint, test.wantAvailable)
			}

			var storedEnabled bool
			if err := testPool.QueryRow(context.Background(), `
				SELECT enabled FROM agent_a2a_endpoint WHERE id = $1
			`, endpoint.ID).Scan(&storedEnabled); err != nil {
				t.Fatalf("load stored endpoint enabled state: %v", err)
			}
			if !storedEnabled {
				t.Fatal("runtime switch must preserve stored enabled for automatic recovery")
			}

			cardRequest := httptest.NewRequest(
				http.MethodGet,
				"/api/a2a/agents/"+endpoint.PublicAgentID+"/.well-known/agent-card.json",
				nil,
			)
			cardRequest.RemoteAddr = "127.0.0.1:49152"
			cardRequest = withAgentA2AURLParams(cardRequest, "publicAgentId", endpoint.PublicAgentID)
			cardResponse := httptest.NewRecorder()
			testHandler.GetAgentA2ACard(cardResponse, cardRequest)
			wantCardStatus := http.StatusNotFound
			if test.wantAvailable {
				wantCardStatus = http.StatusOK
			}
			if cardResponse.Code != wantCardStatus {
				t.Fatalf("anonymous Card after runtime switch status = %d, want %d: %s", cardResponse.Code, wantCardStatus, cardResponse.Body.String())
			}

			var taskCountBefore int
			if err := testPool.QueryRow(context.Background(), `
				SELECT count(*) FROM agent_task_queue WHERE agent_id = $1
			`, agentID).Scan(&taskCountBefore); err != nil {
				t.Fatalf("count tasks before SendMessage: %v", err)
			}

			ctx := a2aintegration.WithPrincipal(context.Background(), a2aintegration.Principal{
				WorkspaceID:     testWorkspaceID,
				AgentID:         agentID,
				EndpointID:      endpoint.ID,
				PublicAgentID:   endpoint.PublicAgentID,
				ClientID:        client.ID,
				CredentialID:    secret.Credential.ID,
				Scopes:          []string{"send", "read"},
				EndpointEnabled: true,
			})
			_, sendErr := testHandler.A2AService.SendMessage(ctx, &a2a.SendMessageRequest{
				Config: &a2a.SendMessageConfig{ReturnImmediately: true},
				Message: &a2a.Message{
					ID:    "message-runtime-version-independent",
					Role:  a2a.MessageRoleUser,
					Parts: a2a.ContentParts{a2a.NewTextPart("runtime version metadata must not block this task")},
				},
			})
			if test.wantAvailable && sendErr != nil {
				t.Fatalf("SendMessage after supported runtime version change: %v", sendErr)
			}
			if !test.wantAvailable && sendErr == nil {
				t.Fatal("SendMessage unexpectedly admitted an unsupported runtime family")
			}

			var taskCountAfter int
			if err := testPool.QueryRow(context.Background(), `
				SELECT count(*) FROM agent_task_queue WHERE agent_id = $1
			`, agentID).Scan(&taskCountAfter); err != nil {
				t.Fatalf("count tasks after SendMessage: %v", err)
			}
			wantTaskCount := taskCountBefore
			if test.wantAvailable {
				wantTaskCount++
			}
			if taskCountAfter != wantTaskCount {
				t.Fatalf("task count changed from %d to %d, want %d", taskCountBefore, taskCountAfter, wantTaskCount)
			}
		})
	}
}

func TestLockAgentA2ASendAdmissionLocksRuntimeBinding(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	configureAgentA2ATestHandler(t)
	agentID, ownerID, _ := privateAgentTestFixture(t)
	runtimeID := assignAgentA2ATestRuntime(t, agentID, "local", "claude")
	endpoint, client, secret := createAgentA2ATestCaller(t, agentID, ownerID)
	ctx := context.Background()

	admissionTx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin admission transaction: %v", err)
	}
	defer admissionTx.Rollback(context.Background())
	if _, err := testHandler.Queries.WithTx(admissionTx).LockAgentA2ASendAdmission(ctx, db.LockAgentA2ASendAdmissionParams{
		AgentID:       util.MustParseUUID(agentID),
		WorkspaceID:   util.MustParseUUID(testWorkspaceID),
		EndpointID:    util.MustParseUUID(endpoint.ID),
		PublicAgentID: endpoint.PublicAgentID,
		ClientID:      util.MustParseUUID(client.ID),
		CredentialID:  util.MustParseUUID(secret.Credential.ID),
	}); err != nil {
		t.Fatalf("lock local Claude admission: %v", err)
	}

	switchTx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin runtime switch transaction: %v", err)
	}
	defer switchTx.Rollback(context.Background())
	if _, err := switchTx.Exec(ctx, `SET LOCAL lock_timeout = '100ms'`); err != nil {
		t.Fatalf("set runtime switch lock timeout: %v", err)
	}
	_, switchErr := switchTx.Exec(ctx, `
		UPDATE agent_runtime
		SET runtime_mode = 'cloud', updated_at = now()
		WHERE id = $1
	`, runtimeID)
	var pgErr *pgconn.PgError
	if !errors.As(switchErr, &pgErr) || pgErr.Code != "55P03" {
		t.Fatalf("concurrent runtime switch error = %v, want lock timeout 55P03", switchErr)
	}
	if err := switchTx.Rollback(ctx); err != nil {
		t.Fatalf("rollback blocked runtime switch: %v", err)
	}
	if err := admissionTx.Rollback(ctx); err != nil {
		t.Fatalf("release admission locks: %v", err)
	}

	result, err := testPool.Exec(ctx, `
		UPDATE agent_runtime
		SET runtime_mode = 'cloud', updated_at = now()
		WHERE id = $1
	`, runtimeID)
	if err != nil {
		t.Fatalf("switch runtime after admission transaction: %v", err)
	}
	if result.RowsAffected() != 1 {
		t.Fatalf("runtime switch rows = %d, want 1", result.RowsAffected())
	}
}

func TestLockAgentA2ASendAdmissionKeepsVersionIndependentAdmissionAfterSwitch(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	configureAgentA2ATestHandler(t)
	agentID, ownerID, _ := privateAgentTestFixture(t)
	runtimeID := assignAgentA2ATestRuntime(t, agentID, "local", "claude")
	endpoint, client, secret := createAgentA2ATestCaller(t, agentID, ownerID)
	ctx := context.Background()

	admissionTx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin admission transaction: %v", err)
	}
	defer admissionTx.Rollback(context.Background())
	var admissionBackendPID int32
	if err := admissionTx.QueryRow(ctx, `SELECT pg_backend_pid()`).Scan(&admissionBackendPID); err != nil {
		t.Fatalf("load admission backend pid: %v", err)
	}

	switchTx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin runtime switch transaction: %v", err)
	}
	defer switchTx.Rollback(context.Background())
	if _, err := switchTx.Exec(ctx, `
		UPDATE agent_runtime
		SET runtime_mode = 'cloud',
		    provider = 'opencode',
		    metadata = $2::jsonb,
		    updated_at = now()
		WHERE id = $1
	`, runtimeID, agentA2ATestManagedOpenCodeRuntimeMetadataWithoutA2A); err != nil {
		t.Fatalf("stage runtime switch: %v", err)
	}

	type admissionOutcome struct {
		err error
	}
	admissionDone := make(chan admissionOutcome, 1)
	go func() {
		_, lockErr := testHandler.Queries.WithTx(admissionTx).LockAgentA2ASendAdmission(ctx, db.LockAgentA2ASendAdmissionParams{
			AgentID:       util.MustParseUUID(agentID),
			WorkspaceID:   util.MustParseUUID(testWorkspaceID),
			EndpointID:    util.MustParseUUID(endpoint.ID),
			PublicAgentID: endpoint.PublicAgentID,
			ClientID:      util.MustParseUUID(client.ID),
			CredentialID:  util.MustParseUUID(secret.Credential.ID),
		})
		admissionDone <- admissionOutcome{err: lockErr}
	}()

	waitDeadline := time.Now().Add(3 * time.Second)
	observedLockWait := false
	for time.Now().Before(waitDeadline) {
		select {
		case outcome := <-admissionDone:
			t.Fatalf("admission returned before runtime switch committed: %v", outcome.err)
		default:
		}
		if err := testPool.QueryRow(ctx, `
			SELECT COALESCE(wait_event_type = 'Lock', FALSE)
			FROM pg_stat_activity
			WHERE pid = $1
		`, admissionBackendPID).Scan(&observedLockWait); err != nil {
			t.Fatalf("observe admission lock wait: %v", err)
		}
		if observedLockWait {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if !observedLockWait {
		t.Fatal("admission did not wait on the in-flight runtime switch")
	}

	if err := switchTx.Commit(ctx); err != nil {
		t.Fatalf("commit runtime switch: %v", err)
	}
	select {
	case outcome := <-admissionDone:
		if outcome.err != nil {
			t.Fatalf("admission after runtime metadata switch: %v", outcome.err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("admission did not resume after runtime switch committed")
	}
}

func TestAgentA2ACredentialSecretAppearsOnlyInCreateResponse(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	agentID, ownerID, _ := privateAgentTestFixture(t)
	assignAgentA2ATestRuntime(t, agentID, "local", "claude")
	withFeatureFlag(t, testHandler, featureflags.AgentA2AInbound, true)
	allowUnsafeLocalAgentA2ARuntimeForTest(t)
	originalProvider := testHandler.configProvider
	testHandler.SetConfigProvider(func() Config {
		return Config{PublicURL: "http://127.0.0.1:8080"}
	})
	t.Cleanup(func() { testHandler.SetConfigProvider(originalProvider) })

	response := httptest.NewRecorder()
	testHandler.UpdateAgentA2AConfig(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodPut, "/api/agents/"+agentID+"/a2a", map[string]any{
			"enabled":          true,
			"card_name":        "Coding Agent",
			"card_description": "Builds and tests local projects.",
			"card_version":     "1.0.0",
			"card_skills":      []any{},
		}),
		"id", agentID,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("update A2A config status = %d, want 200: %s", response.Code, response.Body.String())
	}

	response = httptest.NewRecorder()
	testHandler.CreateAgentA2AClient(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodPost, "/api/agents/"+agentID+"/a2a/clients", map[string]any{"name": "local-e2e"}),
		"id", agentID,
	))
	if response.Code != http.StatusCreated {
		t.Fatalf("create A2A client status = %d, want 201: %s", response.Code, response.Body.String())
	}
	var client AgentA2AClientResponse
	if err := json.Unmarshal(response.Body.Bytes(), &client); err != nil {
		t.Fatalf("decode A2A client: %v", err)
	}

	response = httptest.NewRecorder()
	testHandler.CreateAgentA2ACredential(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodPost, "/api/agents/"+agentID+"/a2a/clients/"+client.ID+"/credentials", map[string]any{}),
		"id", agentID,
		"clientId", client.ID,
	))
	if response.Code != http.StatusCreated {
		t.Fatalf("create A2A credential status = %d, want 201: %s", response.Code, response.Body.String())
	}
	if got := response.Header().Get("Cache-Control"); got != "no-store, private" {
		t.Fatalf("credential Cache-Control = %q, want no-store, private", got)
	}
	if got := response.Header().Get("Pragma"); got != "no-cache" {
		t.Fatalf("credential Pragma = %q, want no-cache", got)
	}
	var secret AgentA2ACredentialSecretResponse
	if err := json.Unmarshal(response.Body.Bytes(), &secret); err != nil {
		t.Fatalf("decode A2A credential: %v", err)
	}
	if !strings.HasPrefix(secret.Token, "mca2a_") {
		t.Fatalf("credential token has wrong prefix: %q", secret.Token)
	}
	if strings.Contains(response.Body.String(), "token_hash") {
		t.Fatalf("credential response leaked token hash: %s", response.Body.String())
	}
	var storedHash string
	if err := testPool.QueryRow(context.Background(), `
		SELECT token_hash FROM a2a_client_credential WHERE id = $1
	`, secret.Credential.ID).Scan(&storedHash); err != nil {
		t.Fatalf("load stored credential hash: %v", err)
	}
	if storedHash != auth.HashToken(secret.Token) || storedHash == secret.Token {
		t.Fatal("database must store only the A2A token hash")
	}

	response = httptest.NewRecorder()
	testHandler.GetAgentA2AConfig(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodGet, "/api/agents/"+agentID+"/a2a", nil),
		"id", agentID,
	))
	if response.Code != http.StatusOK {
		t.Fatalf("get A2A config status = %d, want 200: %s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), secret.Token) || strings.Contains(response.Body.String(), `"token":`) || strings.Contains(response.Body.String(), "token_hash") {
		t.Fatalf("ordinary config response leaked credential secret: %s", response.Body.String())
	}

	response = httptest.NewRecorder()
	testHandler.DeleteAgentA2ACredential(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodDelete, "/api/agents/"+agentID+"/a2a/clients/"+client.ID+"/credentials/"+secret.Credential.ID, nil),
		"id", agentID,
		"clientId", client.ID,
		"credentialId", secret.Credential.ID,
	))
	if response.Code != http.StatusNoContent {
		t.Fatalf("delete A2A credential status = %d, want 204: %s", response.Code, response.Body.String())
	}
}

func TestAgentA2AOwnerTransferRevokesExistingCallersBeforeRebind(t *testing.T) {
	if testHandler == nil {
		t.Skip("database not available")
	}
	requireAgentA2ATestSchema(t)
	agentID, oldOwnerID, newOwnerID := privateAgentTestFixture(t)
	assignAgentA2ATestRuntime(t, agentID, "local", "claude")
	withFeatureFlag(t, testHandler, featureflags.AgentA2AInbound, true)
	allowUnsafeLocalAgentA2ARuntimeForTest(t)
	originalProvider := testHandler.configProvider
	testHandler.SetConfigProvider(func() Config {
		return Config{PublicURL: "http://127.0.0.1:8080"}
	})
	t.Cleanup(func() { testHandler.SetConfigProvider(originalProvider) })

	putConfig := func(ownerID string) AgentA2AConfigResponse {
		t.Helper()
		response := httptest.NewRecorder()
		testHandler.UpdateAgentA2AConfig(response, withAgentA2AURLParams(
			newRequestAs(ownerID, http.MethodPut, "/api/agents/"+agentID+"/a2a", map[string]any{
				"enabled":          true,
				"card_name":        "Transferred Coding Agent",
				"card_description": "Builds local projects.",
				"card_version":     "1.0.0",
				"card_skills":      []any{},
			}),
			"id", agentID,
		))
		if response.Code != http.StatusOK {
			t.Fatalf("update A2A config as %s status = %d, want 200: %s", ownerID, response.Code, response.Body.String())
		}
		var config AgentA2AConfigResponse
		if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
			t.Fatalf("decode A2A config: %v", err)
		}
		return config
	}
	putConfig(oldOwnerID)

	response := httptest.NewRecorder()
	testHandler.CreateAgentA2AClient(response, withAgentA2AURLParams(
		newRequestAs(oldOwnerID, http.MethodPost, "/api/agents/"+agentID+"/a2a/clients", map[string]any{"name": "old-owner-caller"}),
		"id", agentID,
	))
	if response.Code != http.StatusCreated {
		t.Fatalf("create old-owner client status = %d, want 201: %s", response.Code, response.Body.String())
	}
	var client AgentA2AClientResponse
	if err := json.Unmarshal(response.Body.Bytes(), &client); err != nil {
		t.Fatalf("decode old-owner client: %v", err)
	}

	response = httptest.NewRecorder()
	testHandler.CreateAgentA2ACredential(response, withAgentA2AURLParams(
		newRequestAs(oldOwnerID, http.MethodPost, "/api/agents/"+agentID+"/a2a/clients/"+client.ID+"/credentials", map[string]any{}),
		"id", agentID,
		"clientId", client.ID,
	))
	if response.Code != http.StatusCreated {
		t.Fatalf("create old-owner credential status = %d, want 201: %s", response.Code, response.Body.String())
	}
	var secret AgentA2ACredentialSecretResponse
	if err := json.Unmarshal(response.Body.Bytes(), &secret); err != nil {
		t.Fatalf("decode old-owner credential: %v", err)
	}

	if _, err := testPool.Exec(context.Background(), `
		UPDATE agent SET owner_id = $1, updated_at = now() WHERE id = $2
	`, newOwnerID, agentID); err != nil {
		t.Fatalf("transfer agent owner: %v", err)
	}
	config := putConfig(newOwnerID)
	if config.Endpoint == nil || !config.Endpoint.Enabled {
		t.Fatalf("new owner endpoint was not enabled after secure rebind: %#v", config.Endpoint)
	}
	if len(config.Clients) != 1 || config.Clients[0].Status != "revoked" {
		t.Fatalf("old caller was not surfaced as revoked after rebind: %#v", config.Clients)
	}

	var clientStatus, credentialStatus string
	if err := testPool.QueryRow(context.Background(), `
		SELECT client.status, credential.status
		FROM a2a_client client
		JOIN a2a_client_credential credential ON credential.client_id = client.id
		WHERE client.id = $1 AND credential.id = $2
	`, client.ID, secret.Credential.ID).Scan(&clientStatus, &credentialStatus); err != nil {
		t.Fatalf("load transferred caller state: %v", err)
	}
	if clientStatus != "revoked" || credentialStatus != "revoked" {
		t.Fatalf("owner transfer caller state = client:%s credential:%s, want revoked/revoked", clientStatus, credentialStatus)
	}
	if _, err := testHandler.Queries.GetAgentA2ACredentialByTokenHash(context.Background(), auth.HashToken(secret.Token)); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("old owner's raw token lookup error = %v, want pgx.ErrNoRows", err)
	}
}

func requireAgentA2ATestSchema(t *testing.T) {
	t.Helper()
	var available bool
	if err := testPool.QueryRow(context.Background(), `SELECT to_regclass('agent_a2a_endpoint') IS NOT NULL`).Scan(&available); err != nil || !available {
		t.Skip("A2A schema is not migrated in the handler test database")
	}
}

func configureAgentA2ATestHandler(t *testing.T) {
	t.Helper()
	withFeatureFlag(t, testHandler, featureflags.AgentA2AInbound, true)
	allowUnsafeLocalAgentA2ARuntimeForTest(t)
	originalProvider := testHandler.configProvider
	testHandler.SetConfigProvider(func() Config {
		return Config{PublicURL: "http://127.0.0.1:8080"}
	})
	t.Cleanup(func() { testHandler.SetConfigProvider(originalProvider) })
}

func assignAgentA2ATestRuntime(t *testing.T, agentID, runtimeMode, provider string, metadata ...string) string {
	t.Helper()
	if len(metadata) > 1 {
		t.Fatal("assignAgentA2ATestRuntime accepts at most one metadata document")
	}
	metadataJSON := `{}`
	if len(metadata) == 1 {
		metadataJSON = metadata[0]
	}
	ctx := context.Background()
	var originalRuntimeID string
	if err := testPool.QueryRow(ctx, `
		SELECT runtime_id
		FROM agent
		WHERE id = $1
	`, agentID).Scan(&originalRuntimeID); err != nil {
		t.Fatalf("load original agent runtime: %v", err)
	}

	var runtimeID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_runtime (
			workspace_id, daemon_id, name, runtime_mode, provider, status,
			device_info, metadata, owner_id, last_seen_at
		)
		SELECT
			a.workspace_id, NULL, 'A2A test runtime ' || a.id::text,
			$2, $3, 'online', 'A2A handler test', $4::jsonb,
			a.owner_id, now()
		FROM agent a
		WHERE a.id = $1
		RETURNING id
	`, agentID, runtimeMode, provider, metadataJSON).Scan(&runtimeID); err != nil {
		t.Fatalf("create A2A test runtime: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		UPDATE agent
		SET runtime_id = $1, updated_at = now()
		WHERE id = $2
	`, runtimeID, agentID); err != nil {
		t.Fatalf("assign A2A test runtime: %v", err)
	}

	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if _, err := testPool.Exec(cleanupCtx, `
			UPDATE agent
			SET runtime_id = $1, updated_at = now()
			WHERE id = $2
		`, originalRuntimeID, agentID); err != nil {
			t.Errorf("restore original agent runtime: %v", err)
			return
		}
		if _, err := testPool.Exec(cleanupCtx, `DELETE FROM agent_runtime WHERE id = $1`, runtimeID); err != nil {
			t.Errorf("delete A2A test runtime: %v", err)
		}
	})
	return runtimeID
}

func putAgentA2ATestConfig(t *testing.T, agentID, ownerID string, enabled bool) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	testHandler.UpdateAgentA2AConfig(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodPut, "/api/agents/"+agentID+"/a2a", map[string]any{
			"enabled":          enabled,
			"card_name":        "A2A Test Agent",
			"card_description": "Runs first-slice local A2A tests.",
			"card_version":     "1.0.0",
			"card_skills":      []any{},
		}),
		"id", agentID,
	))
	return response
}

func createAgentA2ATestClient(t *testing.T, agentID, ownerID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	testHandler.CreateAgentA2AClient(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodPost, "/api/agents/"+agentID+"/a2a/clients", body),
		"id", agentID,
	))
	return response
}

func updateAgentA2ATestClient(t *testing.T, agentID, ownerID, clientID string, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	response := httptest.NewRecorder()
	testHandler.UpdateAgentA2AClient(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodPut, "/api/agents/"+agentID+"/a2a/clients/"+clientID, body),
		"id", agentID,
		"clientId", clientID,
	))
	return response
}

func createAgentA2ATestCaller(
	t *testing.T,
	agentID string,
	ownerID string,
) (AgentA2AEndpointResponse, AgentA2AClientResponse, AgentA2ACredentialSecretResponse) {
	t.Helper()
	response := putAgentA2ATestConfig(t, agentID, ownerID, true)
	if response.Code != http.StatusOK {
		t.Fatalf("enable A2A status = %d, want 200: %s", response.Code, response.Body.String())
	}
	var config AgentA2AConfigResponse
	if err := json.Unmarshal(response.Body.Bytes(), &config); err != nil {
		t.Fatalf("decode A2A config: %v", err)
	}
	if config.Endpoint == nil {
		t.Fatal("enabled A2A config is missing endpoint")
	}
	if !config.Endpoint.Enabled {
		t.Fatal("local Claude A2A config must be effectively enabled")
	}

	response = createAgentA2ATestClient(t, agentID, ownerID, map[string]any{"name": "runtime-switch-caller"})
	if response.Code != http.StatusCreated {
		t.Fatalf("create A2A client status = %d, want 201: %s", response.Code, response.Body.String())
	}
	var client AgentA2AClientResponse
	if err := json.Unmarshal(response.Body.Bytes(), &client); err != nil {
		t.Fatalf("decode A2A client: %v", err)
	}

	response = httptest.NewRecorder()
	testHandler.CreateAgentA2ACredential(response, withAgentA2AURLParams(
		newRequestAs(ownerID, http.MethodPost, "/api/agents/"+agentID+"/a2a/clients/"+client.ID+"/credentials", map[string]any{}),
		"id", agentID,
		"clientId", client.ID,
	))
	if response.Code != http.StatusCreated {
		t.Fatalf("create A2A credential status = %d, want 201: %s", response.Code, response.Body.String())
	}
	var secret AgentA2ACredentialSecretResponse
	if err := json.Unmarshal(response.Body.Bytes(), &secret); err != nil {
		t.Fatalf("decode A2A credential: %v", err)
	}
	return *config.Endpoint, client, secret
}

func withAgentA2AURLParams(request *http.Request, pairs ...string) *http.Request {
	if len(pairs)%2 != 0 {
		panic("withAgentA2AURLParams requires key/value pairs")
	}
	routeContext := chi.NewRouteContext()
	for index := 0; index < len(pairs); index += 2 {
		routeContext.URLParams.Add(pairs[index], pairs[index+1])
	}
	return request.WithContext(context.WithValue(request.Context(), chi.RouteCtxKey, routeContext))
}

func testNow() (result time.Time) {
	return time.Date(2026, time.August, 9, 12, 0, 0, 0, time.UTC)
}
