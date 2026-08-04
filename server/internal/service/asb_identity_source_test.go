package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func TestASBIdentitySourceRemainsRunningForInheritance(t *testing.T) {
	const (
		sourceSandboxID = "identity-source-123"
		runtimeImageRef = "registry.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
		employeeID      = "12345"
		bucAgentID      = "agent-multica-asb"
	)

	state := "Running"
	sourceImageRef := runtimeImageRef
	attachCalls := 0
	renewCalls := 0
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
			if payload.Image.URI != runtimeImageRef ||
				payload.Timeout != asbMaxCreateTimeout ||
				payload.Extensions["wireguard.lazyAuth"] != "true" ||
				len(payload.Extensions) != 1 {
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
				"image":     map[string]string{"uri": sourceImageRef},
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
			if input.Command != asbBUCOnlyIdentityProbeCommand() ||
				input.Envs["EXPECTED_EMP_ID"] != employeeID ||
				input.Envs["EXPECTED_BUC_AGENT_ID"] != bucAgentID ||
				strings.Contains(input.Command, "a1 ") ||
				strings.Contains(input.Command, "nw-aliwork-cli") {
				t.Fatalf("identity probe input = %#v", input)
			}
			response.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(response, "data: {\"type\":\"execution_complete\",\"execution_time\":1}\n")
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/sandboxes/"+sourceSandboxID+"/renew-expiration":
			renewCalls++
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
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/sandboxes/quotas":
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":5,"usage":0}]`)
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
		enterpriseIdentitySourceKey{
			WorkspaceID:   util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
			BoundBy:       util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
			RuntimeID:     runtimeID,
			RawEmployeeID: employeeID,
			BUCAgentID:    bucAgentID,
		},
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
		state != "Running" ||
		pauseCalls != 0 ||
		deleteCalls != 0 {
		t.Fatalf(
			"created identity source = %#v, state=%s pause_calls=%d delete_calls=%d",
			source,
			state,
			pauseCalls,
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
	if state != "Running" || resumeCalls != 0 || renewCalls != 1 {
		t.Fatalf(
			"prepared state=%s resume_calls=%d renew_calls=%d",
			state,
			resumeCalls,
			renewCalls,
		)
	}
	if err := manager.Park(context.Background(), runtimeID, sourceSandboxID); err != nil {
		t.Fatalf("Park identity source: %v", err)
	}
	if state != "Running" || pauseCalls != 0 {
		t.Fatalf("released state=%s pause_calls=%d", state, pauseCalls)
	}
	sourceImageRef = "registry.example/runtime@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	if err := manager.Prepare(
		context.Background(),
		runtimeID,
		sourceSandboxID,
		employeeID,
		bucAgentID,
	); !errors.Is(err, ErrEnterpriseIdentityNeedsReauth) {
		t.Fatalf("Prepare mismatched identity source error = %v", err)
	}
	if err := manager.Delete(context.Background(), runtimeID, sourceSandboxID); err != nil {
		t.Fatalf("Delete identity source: %v", err)
	}
	if attachCalls != 1 || deleteCalls != 1 {
		t.Fatalf("attach_calls=%d delete_calls=%d", attachCalls, deleteCalls)
	}
}

func TestASBIdentitySourceInitialExpirationLeavesOneMinuteSafetyMargin(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 30, 14, 0, 0, 0, time.UTC)
	expiresAt := asbIdentitySourceInitialExpiration(now)
	want := now.Add(asbMaxRenewalDuration - time.Minute)
	if !expiresAt.Equal(want) {
		t.Fatalf("expires_at = %s, want %s", expiresAt, want)
	}
	if got := expiresAt.Sub(now); got != 167*time.Hour+59*time.Minute {
		t.Fatalf("initial source lifetime = %s, want 167h59m", got)
	}
}

func TestAttachAndProbeASBIdentitySourceSubmitsAttachOnceThenProbes(t *testing.T) {
	const sandboxID = "identity-source-delayed-attach"
	attachCalls := 0
	endpointCalls := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/sandboxes/"+sandboxID+"/identity/wireguard":
			attachCalls++
			if request.URL.Query().Get("sync") != "true" {
				t.Fatalf("identity source attach sync = %q", request.URL.Query().Get("sync"))
			}
			if attachCalls > 1 {
				t.Fatalf("identity source attach calls = %d, want exactly one", attachCalls)
			}
			time.Sleep(10 * time.Millisecond)
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
		100*time.Millisecond,
	); err != nil {
		t.Fatalf("attachAndProbeASBIdentitySource: %v", err)
	}
	if endpointCalls != 2 {
		t.Fatalf("endpoint calls = %d, want 2", endpointCalls)
	}
	if attachCalls != 1 {
		t.Fatalf("attach calls = %d, want 1", attachCalls)
	}
}

func TestAttachAndProbeASBIdentitySourceProbesAfterWireGuardConvergingResponse(t *testing.T) {
	const sandboxID = "identity-source-wireguard-converging"
	attachCalls := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/sandboxes/"+sandboxID+"/identity/wireguard":
			attachCalls++
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(response, `{
				"code":"BAD_REQUEST",
				"message":"wireguard tunnel not ready yet, sandboxId=identity-source-wireguard-converging"
			}`)
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/sandboxes/"+sandboxID:
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{
				"id":"identity-source-wireguard-converging",
				"status":{"state":"Running"},
				"createdAt":"2026-07-31T05:00:00Z"
			}`)
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/sandboxes/"+sandboxID+"/endpoints/44772":
			response.Header().Set("Content-Type", "application/json")
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
		250*time.Millisecond,
	); err != nil {
		t.Fatalf("attachAndProbeASBIdentitySource: %v", err)
	}
	if attachCalls != 1 {
		t.Fatalf("attach calls = %d, want exactly one", attachCalls)
	}
}

func TestAttachAndProbeASBIdentitySourceDoesNotRetryOtherBadRequest(t *testing.T) {
	const sandboxID = "identity-source-invalid-grant"
	attachCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		attachCalls++
		response.Header().Set("Content-Type", "application/json")
		response.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(response, `{
			"code":"BAD_REQUEST",
			"message":"identity grant is invalid"
		}`)
	}))
	defer server.Close()

	client, err := NewASBClient(ASBClientConfig{
		BaseURL: server.URL,
		APIKey:  "runtime-api-key",
	})
	if err != nil {
		t.Fatalf("NewASBClient: %v", err)
	}
	err = attachAndProbeASBIdentitySource(
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
		250*time.Millisecond,
	)
	if err == nil {
		t.Fatal("attachAndProbeASBIdentitySource() error = nil, want bad request")
	}
	if attachCalls != 1 {
		t.Fatalf("attach calls = %d, want 1", attachCalls)
	}
}

func TestASBIdentityProbeStageReturnsLastSafeMarker(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		stderr string
		want   string
	}{
		{
			name:   "newline separated",
			stderr: "probe_stage=buc\nprobe_stage=a1\n",
			want:   "a1",
		},
		{
			name:   "curl error concatenated by SSE decoder",
			stderr: "probe_stage=buccurl: (22) The requested URL returned error: 502",
			want:   "buc",
		},
		{
			name:   "a1 error concatenated after adjacent markers",
			stderr: "probe_stage=bucprobe_stage=a1a1-server rejected authentication",
			want:   "a1",
		},
		{
			name:   "completed probe",
			stderr: "probe_stage=bucprobe_stage=a1probe_stage=complete",
			want:   "complete",
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := asbIdentityProbeStage(test.stderr); got != test.want {
				t.Fatalf("asbIdentityProbeStage() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestASBIdentityProbeCommandHasValidShellSyntax(t *testing.T) {
	t.Parallel()
	for name, command := range map[string]string{
		"buc_only": asbBUCOnlyIdentityProbeCommand(),
		"buc_a1":   asbBUCIdentityProbeCommand(),
	} {
		command := command
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if strings.Contains(command, "nw-aliwork") {
				t.Fatalf("identity probe still depends on removed nw-aliwork CLI: %q", command)
			}
			for _, required := range []string{
				"sandbox=true",
				"EXPECTED_EMP_ID",
				"EXPECTED_BUC_AGENT_ID",
				"p.get(\"success\") is True",
			} {
				if !strings.Contains(command, required) {
					t.Fatalf("identity probe is missing %q: %q", required, command)
				}
			}
			if output, err := exec.Command(
				"bash",
				"-n",
				"-c",
				command,
			).CombinedOutput(); err != nil {
				t.Fatalf("identity probe shell syntax: %v\n%s", err, output)
			}
		})
	}
}
