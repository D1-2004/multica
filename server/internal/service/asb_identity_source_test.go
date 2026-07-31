package service

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeASBIdentitySourceStore struct {
	runtime    db.AgentRuntime
	credential db.AsbRuntimeCredential
}

type fakeASBIdentitySourceCapacity struct{}

func (fakeASBIdentitySourceCapacity) Create(
	ctx context.Context,
	_ pgtype.UUID,
	client *ASBClient,
	input ASBCreateSandboxInput,
) (*ASBSandbox, error) {
	return client.CreateSandbox(ctx, input)
}

func (store *fakeASBIdentitySourceStore) GetAgentRuntime(
	context.Context,
	pgtype.UUID,
) (db.AgentRuntime, error) {
	return store.runtime, nil
}

func (store *fakeASBIdentitySourceStore) GetASBRuntimeCredential(
	context.Context,
	pgtype.UUID,
) (db.AsbRuntimeCredential, error) {
	return store.credential, nil
}

func (store *fakeASBIdentitySourceStore) ListASBRuntimeCredentials(
	context.Context,
) ([]db.AsbRuntimeCredential, error) {
	return []db.AsbRuntimeCredential{store.credential}, nil
}

func TestASBIdentitySourcePausesWithoutDeletingAndResumesForInheritance(t *testing.T) {
	const (
		sourceSandboxID = "identity-source-123"
		employeeID      = "12345"
		bucAgentID      = "agent-multica-asb"
	)

	state := "Running"
	attachCalls := 0
	pauseCalls := 0
	resumeCalls := 0
	deleteCalls := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/sandboxes":
			var payload asbCreateSandboxRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			if payload.Timeout != asbMaxCreateTimeout ||
				payload.Extensions["wireguard.lazyAuth"] != "true" ||
				payload.Extensions["persistence"] != "snapshot" {
				t.Fatalf("identity source create request = %#v", payload)
			}
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(response, `{"id":"identity-source-123","status":{"state":"Pending"},"createdAt":"2026-07-31T05:00:00Z"}`)
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/sandboxes/"+sourceSandboxID:
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(map[string]any{
				"id":        sourceSandboxID,
				"status":    map[string]string{"state": state},
				"createdAt": "2026-07-31T05:00:00Z",
			})
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/sandboxes/"+sourceSandboxID+"/identity/wireguard":
			if request.URL.Query().Get("sync") != "true" {
				t.Fatalf("identity source attach sync = %q", request.URL.Query().Get("sync"))
			}
			var grant ASBBUCIdentityGrant
			if err := json.NewDecoder(request.Body).Decode(&grant); err != nil {
				t.Fatalf("decode BUC identity grant: %v", err)
			}
			if grant.EmployeeID != employeeID ||
				grant.BUCRefreshToken != "buc-refresh" ||
				grant.WireGuardCredentials != "wg-client" {
				t.Fatalf("BUC identity grant = %#v", grant)
			}
			attachCalls++
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/sandboxes/"+sourceSandboxID+"/endpoints/44772":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(map[string]any{
				"endpoint": server.URL + "/exec",
				"headers":  map[string]string{"X-Sandbox-Token": "endpoint-token"},
			})
		case request.Method == http.MethodPost && request.URL.Path == "/exec/command":
			var input asbExecRequest
			if err := json.NewDecoder(request.Body).Decode(&input); err != nil {
				t.Fatalf("decode identity probe: %v", err)
			}
			if input.Envs["EXPECTED_EMP_ID"] != employeeID ||
				input.Envs["EXPECTED_BUC_AGENT_ID"] != bucAgentID ||
				!strings.Contains(input.Command, "a1 --no-update-check") ||
				!strings.Contains(input.Command, "nw-aliwork-cli login --no-update-check") ||
				!strings.Contains(input.Command, "nw-aliwork-cli whoami --no-update-check") {
				t.Fatalf("identity probe input = %#v", input)
			}
			response.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(response, "data: {\"type\":\"execution_complete\",\"execution_time\":1}\n")
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/sandboxes/"+sourceSandboxID+"/renew-expiration":
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/sandboxes/"+sourceSandboxID+"/pause":
			pauseCalls++
			state = "Paused"
			response.WriteHeader(http.StatusAccepted)
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/sandboxes/"+sourceSandboxID+"/resume":
			resumeCalls++
			state = "Running"
			response.WriteHeader(http.StatusAccepted)
		case request.Method == http.MethodDelete &&
			request.URL.Path == "/v1/sandboxes/"+sourceSandboxID:
			deleteCalls++
			state = "Terminated"
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	box, err := secretbox.New(bytes.Repeat([]byte{0x42}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	sealedAPIKey, err := box.Seal([]byte("runtime-api-key"))
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	store := &fakeASBIdentitySourceStore{
		runtime: db.AgentRuntime{
			ID:          runtimeID,
			RuntimeMode: "cloud",
			Provider:    "hermes",
			Metadata: []byte(`{
				"kind":"cloud-sandbox",
				"sandbox_backend":"asb",
				"provider":"hermes",
				"artifact_kind":"oci_image",
				"artifact_channel":"stable",
				"artifact_ref":"registry.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			}`),
		},
		credential: db.AsbRuntimeCredential{
			RuntimeID:       runtimeID,
			ApiKeyEncrypted: sealedAPIKey,
		},
	}
	manager := &ASBIdentitySourceManager{
		Store: store,
		Credentials: &ASBRuntimeClientProvider{
			Store:   store,
			Secrets: box,
			Config:  ASBConfig{APIURL: server.URL},
		},
		Capacity: fakeASBIdentitySourceCapacity{},
		Config: ASBConfig{
			ResourceCPU:           "2",
			ResourceMemory:        "4Gi",
			ReadyTimeout:          time.Second,
			WireGuardReadyTimeout: time.Second,
			WireGuardCredentials:  "wg-client",
		},
	}

	source, err := manager.Create(
		context.Background(),
		runtimeID,
		util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
		employeeID,
		bucAgentID,
		BUCIdentityTokens{
			AccessToken:  "buc-access",
			RefreshToken: "buc-refresh",
			IDToken:      "buc-id",
		},
	)
	if err != nil {
		t.Fatalf("Create identity source: %v", err)
	}
	if source.SandboxID != sourceSandboxID ||
		source.RuntimeID != runtimeID ||
		state != "Paused" ||
		deleteCalls != 0 {
		t.Fatalf(
			"created identity source = %#v, state=%s delete_calls=%d",
			source,
			state,
			deleteCalls,
		)
	}

	if err := manager.Prepare(
		context.Background(),
		runtimeID,
		sourceSandboxID,
		employeeID,
		bucAgentID,
	); err != nil {
		t.Fatalf("Prepare identity source: %v", err)
	}
	if state != "Running" || resumeCalls != 1 {
		t.Fatalf("prepared state=%s resume_calls=%d", state, resumeCalls)
	}
	if err := manager.Park(context.Background(), runtimeID, sourceSandboxID); err != nil {
		t.Fatalf("Park identity source: %v", err)
	}
	if state != "Paused" || pauseCalls != 2 {
		t.Fatalf("parked state=%s pause_calls=%d", state, pauseCalls)
	}
	if err := manager.Delete(context.Background(), runtimeID, sourceSandboxID); err != nil {
		t.Fatalf("Delete identity source: %v", err)
	}
	if attachCalls != 1 || deleteCalls != 1 {
		t.Fatalf("attach_calls=%d delete_calls=%d", attachCalls, deleteCalls)
	}
}

func TestAttachAndProbeASBIdentitySourceStartsFreshProbeWindowAfterSyncAttach(t *testing.T) {
	const sandboxID = "identity-source-delayed-attach"
	endpointCalls := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/sandboxes/"+sandboxID+"/identity/wireguard":
			time.Sleep(30 * time.Millisecond)
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/sandboxes/"+sandboxID:
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{
				"id":"identity-source-delayed-attach",
				"status":{"state":"Running"},
				"createdAt":"2026-07-31T05:00:00Z"
			}`)
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/sandboxes/"+sandboxID+"/endpoints/44772":
			endpointCalls++
			response.Header().Set("Content-Type", "application/json")
			if endpointCalls == 1 {
				response.WriteHeader(http.StatusConflict)
				_, _ = io.WriteString(response, `{
					"code":"SANDBOX_STATE_CONFLICT",
					"message":"sandbox endpoint is converging"
				}`)
				return
			}
			_ = json.NewEncoder(response).Encode(map[string]any{
				"endpoint": server.URL + "/exec",
				"headers":  map[string]string{"X-Sandbox-Token": "endpoint-token"},
			})
		case request.Method == http.MethodPost && request.URL.Path == "/exec/command":
			response.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(
				response,
				"data: {\"type\":\"execution_complete\",\"execution_time\":1}\n",
			)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	client, err := NewASBClient(ASBClientConfig{
		BaseURL: server.URL,
		APIKey:  "runtime-api-key",
	})
	if err != nil {
		t.Fatalf("NewASBClient: %v", err)
	}
	if err := attachAndProbeASBIdentitySource(
		context.Background(),
		client,
		sandboxID,
		"12345",
		"agent-multica-asb",
		BUCIdentityTokens{
			AccessToken:  "buc-access",
			RefreshToken: "buc-refresh",
			IDToken:      "buc-id",
		},
		"wg-client",
		20*time.Millisecond,
	); err != nil {
		t.Fatalf("attachAndProbeASBIdentitySource: %v", err)
	}
	if endpointCalls != 2 {
		t.Fatalf("endpoint calls = %d, want 2", endpointCalls)
	}
}

func TestASBIdentityProbeStageReturnsLastSafeMarker(t *testing.T) {
	t.Parallel()

	stderr := "probe_stage=buc\nprobe_stage=a1\nprobe_stage=nw_aliwork_login\n"
	if got := asbIdentityProbeStage(stderr); got != "nw_aliwork_login" {
		t.Fatalf("asbIdentityProbeStage() = %q", got)
	}
}

func TestASBIdentityProbeCommandHasValidShellSyntax(t *testing.T) {
	t.Parallel()

	if output, err := exec.Command(
		"bash",
		"-n",
		"-c",
		asbBUCIdentityProbeCommand(),
	).CombinedOutput(); err != nil {
		t.Fatalf("identity probe shell syntax: %v\n%s", err, output)
	}
}
