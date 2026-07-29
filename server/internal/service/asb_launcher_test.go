package service

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestASBRunnerCommandUsesExecdUserCore(t *testing.T) {
	t.Parallel()

	command, err := asbRunnerCommand(
		"hermes",
		util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
	)
	if err != nil {
		t.Fatalf("asbRunnerCommand: %v", err)
	}
	if !strings.HasPrefix(command, "/usr/local/libexec/multica-fc-runner ") {
		t.Fatalf("ASB runner command = %q", command)
	}
	if strings.Contains(command, "container-log-entry") {
		t.Fatalf("ASB runner command uses the root-only FC wrapper: %q", command)
	}
}

func TestASBIdentityProbeUsesReadOnlyCLIsAndChecksEmployee(t *testing.T) {
	t.Parallel()

	command, err := asbIdentityProbeCommand("12345")
	if err != nil {
		t.Fatalf("asbIdentityProbeCommand: %v", err)
	}
	for _, expected := range []string{
		"a1 --no-update-check -f json auth whoami",
		"mw --no-update-check auth whoami --format json",
		"(.emp_id | tostring) == $expected",
		"(.employee_id | tostring) == $expected",
	} {
		if !strings.Contains(command, expected) {
			t.Fatalf("ASB identity probe is missing %q: %s", expected, command)
		}
	}
	if _, err := asbIdentityProbeCommand("12345; touch /tmp/unsafe"); err == nil {
		t.Fatal("ASB identity probe accepted an unsafe employee ID")
	}
}

func TestASBVerifyStableArtifactUsesSandboxDefaultUser(t *testing.T) {
	t.Parallel()

	digest := "sha256:" + strings.Repeat("a", 64)
	manifest := map[string]any{
		"schema_version":   3,
		"sandbox_backends": []string{"aliyun_fc", "asb"},
		"providers":        []string{"hermes", "opencode", "pi"},
		"capabilities_by_backend": map[string][]string{
			"asb": {"dws", "mcp", "a1", "mw", "buc"},
		},
		"identity_modes_by_backend": map[string][]string{
			"asb": {"agent_identity", "spiffe", "buc_wireguard"},
		},
		"runner_protocol": string(fcE2BRunnerLaunchRootLog),
	}
	manifestJSON, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}

	var server *httptest.Server
	execCalls := 0
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost && request.URL.Path == "/v1/sandboxes":
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(response, `{"id":"sandbox-123","status":{"state":"Pending"},"createdAt":"2026-07-29T05:00:00Z","entrypoint":["sleep infinity"]}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/sandbox-123":
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{"id":"sandbox-123","status":{"state":"Running"},"createdAt":"2026-07-29T05:00:00Z","entrypoint":["sleep infinity"]}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/sandbox-123/endpoints/44772":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(map[string]any{
				"endpoint": server.URL + "/execd",
				"headers":  map[string]string{"X-Sandbox-Token": testEndpointToken},
			})
		case request.Method == http.MethodPost && request.URL.Path == "/execd/command":
			execCalls++
			var payload asbExecRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode exec request: %v", err)
			}
			if payload.UID != nil || payload.GID != nil {
				t.Errorf("ASB exec requested an explicit user: uid=%v gid=%v", payload.UID, payload.GID)
			}
			if payload.Envs["HOME"] != asbRunnerHome ||
				payload.Envs["USER"] != "user" ||
				payload.Envs["LOGNAME"] != "user" {
				t.Errorf("ASB exec user environment = %#v", payload.Envs)
			}
			response.Header().Set("Content-Type", "text/event-stream")
			if payload.Command == "/bin/cat /usr/local/share/multica/runtime-manifest.json" {
				event, marshalErr := json.Marshal(map[string]any{
					"type": "stdout",
					"text": string(manifestJSON),
				})
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				_, _ = response.Write([]byte("data: " + string(event) + "\n"))
			}
			_, _ = io.WriteString(response, `data: {"type":"execution_complete","execution_time":1}`+"\n")
		case request.Method == http.MethodDelete && request.URL.Path == "/v1/sandboxes/sandbox-123":
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	launcher := &ASBLauncher{
		Client: newTestASBClient(t, server),
		Config: ASBConfig{
			TimeoutSeconds: 60,
			ReadyTimeout:   time.Second,
			ResourceCPU:    "2",
			ResourceMemory: "4Gi",
		},
	}
	got, err := launcher.VerifyStableArtifact(context.Background(), ASBArtifact{
		Ref:     "registry.example/runtime@" + digest,
		BuildID: "aone-run-1",
		Digest:  digest,
	})
	if err != nil {
		t.Fatalf("VerifyStableArtifact: %v", err)
	}
	if execCalls != 2 {
		t.Fatalf("exec calls = %d, want 2", execCalls)
	}
	if intMetadataValue(got, "schema_version") != 3 {
		t.Fatalf("manifest = %#v", got)
	}
}

func TestASBExecRunOnceUsesDefaultUserAndDirectCore(t *testing.T) {
	t.Parallel()

	var captured asbExecRequest
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/sandboxes/sandbox-123/endpoints/44772":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(map[string]any{
				"endpoint": strings.TrimPrefix(serverURLFromRequest(request), "http://") + "/execd",
				"headers":  map[string]string{"X-Sandbox-Token": testEndpointToken},
			})
		case "/execd/command":
			if err := json.NewDecoder(request.Body).Decode(&captured); err != nil {
				t.Fatalf("decode exec request: %v", err)
			}
			response.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(response, `data: {"type":"init","text":"exec-1"}`+"\n")
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	runtimeID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	taskID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	metadata, err := json.Marshal(map[string]any{
		"kind":              CloudSandboxMetadataKind,
		"sandbox_backend":   string(SandboxBackendASB),
		"provider":          "hermes",
		"artifact_kind":     CloudSandboxArtifactOCIImage,
		"artifact_channel":  CloudSandboxChannelCandidate,
		"artifact_ref":      "registry.example/runtime@sha256:" + strings.Repeat("a", 64),
		"artifact_build_id": "aone-run-1",
		"artifact_digest":   "sha256:" + strings.Repeat("a", 64),
	})
	if err != nil {
		t.Fatal(err)
	}
	launcher := &ASBLauncher{
		Client: newTestASBClient(t, server),
		Config: ASBConfig{
			ReadyTimeout: time.Second,
			LLMBaseURL:   "https://models.example/v1",
			LLMAPIKey:    "test-model-key",
		},
	}
	err = launcher.execRunOnce(
		context.Background(),
		testSandboxID,
		db.AgentRuntime{
			ID:          runtimeID,
			DaemonID:    pgtype.Text{String: "daemon-1", Valid: true},
			Name:        "ASB Runtime",
			RuntimeMode: "cloud",
			Provider:    "hermes",
			Metadata:    metadata,
		},
		taskID,
		"test-daemon-token",
		true,
		nil,
	)
	if err != nil {
		t.Fatalf("execRunOnce: %v", err)
	}
	if captured.UID != nil || captured.GID != nil {
		t.Fatalf("ASB runner requested an explicit user: uid=%v gid=%v", captured.UID, captured.GID)
	}
	if !captured.Background ||
		!strings.HasPrefix(captured.Command, "/usr/local/libexec/multica-fc-runner ") ||
		strings.Contains(captured.Command, "container-log-entry") {
		t.Fatalf("ASB runner exec request = %#v", captured)
	}
	if captured.Envs["MULTICA_RUNNER_PROVIDER"] != "hermes" ||
		captured.Envs["HOME"] != asbRunnerHome ||
		captured.Envs["USER"] != "user" ||
		captured.Envs["LOGNAME"] != "user" {
		t.Fatalf("ASB runner environment = %#v", captured.Envs)
	}
}
