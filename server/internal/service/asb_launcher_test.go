package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeASBTaskIdentityResolver struct {
	identity ASBResolvedIdentity
	err      error
}

func (resolver fakeASBTaskIdentityResolver) ResolveASBTaskIdentity(
	context.Context,
	pgtype.UUID,
	pgtype.UUID,
) (ASBResolvedIdentity, error) {
	return resolver.identity, resolver.err
}

func TestASBLaunchIdentityAllowsExplicitUnboundMode(t *testing.T) {
	t.Parallel()

	launcher := &ASBLauncher{
		Identity: fakeASBTaskIdentityResolver{err: ErrEnterpriseIdentityNeedsReauth},
	}
	identity, err := launcher.resolveTaskIdentity(context.Background(), pgtype.UUID{}, pgtype.UUID{})
	if err != nil {
		t.Fatalf("resolveTaskIdentity: %v", err)
	}
	if identity.Mode != asbIdentityModeUnbound ||
		identity.Fingerprint != asbUnboundIdentityFingerprint {
		t.Fatalf("unbound identity = %#v", identity)
	}
	if extensions := identity.sandboxExtensions(); len(extensions) != 0 {
		t.Fatalf("unbound sandbox extensions = %#v", extensions)
	}
}

func TestASBLaunchIdentityDoesNotHideResolverFailure(t *testing.T) {
	t.Parallel()

	resolverErr := errors.New("identity store unavailable")
	launcher := &ASBLauncher{
		Identity: fakeASBTaskIdentityResolver{err: resolverErr},
	}
	if _, err := launcher.resolveTaskIdentity(
		context.Background(),
		pgtype.UUID{},
		pgtype.UUID{},
	); !errors.Is(err, resolverErr) {
		t.Fatalf("resolveTaskIdentity error = %v, want %v", err, resolverErr)
	}
}

func TestASBBoundIdentityKeepsIdentityExtensions(t *testing.T) {
	t.Parallel()

	identity := ASBResolvedIdentity{
		Mode:          asbIdentityModeBound,
		RawEmployeeID: "12345",
		BUCAgentID:    "agent-multica-asb",
		AgentSPIFFEID: "spiffe://multica.prod.ali/ns/default/agents/agent-1",
		AIPID:         "aip-1",
		BUCTokens: BUCIdentityTokens{
			AccessToken:  "buc-access",
			RefreshToken: "buc-refresh",
			IDToken:      "buc-id",
		},
		AgentIdentityToken: "ait",
		Fingerprint:        strings.Repeat("a", 64),
	}
	if err := identity.validate(); err != nil {
		t.Fatalf("validate bound identity: %v", err)
	}
	extensions := identity.sandboxExtensions()
	for key, expected := range map[string]string{
		"spiffe.lazyAuth":    "true",
		"wireguard.lazyAuth": "true",
	} {
		if extensions[key] != expected {
			t.Fatalf("bound sandbox extension %s = %q, want %q", key, extensions[key], expected)
		}
	}
	for _, forbidden := range []string{"wireguard.worker", "wireguard.uemCredentials"} {
		if _, ok := extensions[forbidden]; ok {
			t.Fatalf("bound sandbox extensions include create-time identity field %s", forbidden)
		}
	}
}

func TestASBLauncherInjectsPlatformCredentialsWithoutAnchorOrProbe(t *testing.T) {
	t.Parallel()

	var wireguardGrant ASBBUCIdentityGrant
	var spiffeGrant ASBAgentIdentityGrant
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/sandboxes/sandbox-123/identity/wireguard":
			if err := json.NewDecoder(request.Body).Decode(&wireguardGrant); err != nil {
				t.Fatalf("decode WireGuard identity: %v", err)
			}
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/sandboxes/sandbox-123/identity/spiffe":
			if err := json.NewDecoder(request.Body).Decode(&spiffeGrant); err != nil {
				t.Fatalf("decode SPIFFE identity: %v", err)
			}
			response.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	identity := ASBResolvedIdentity{
		Mode:          asbIdentityModeBound,
		RawEmployeeID: "12345",
		BUCAgentID:    "agent-multica-asb",
		AgentSPIFFEID: "spiffe://multica.prod.ali/ns/default/agents/agent-1",
		AIPID:         "aip-1",
		BUCTokens: BUCIdentityTokens{
			AccessToken:  "buc-access",
			RefreshToken: "buc-refresh",
			IDToken:      "buc-id",
		},
		AgentIdentityToken: "ait",
		Fingerprint:        strings.Repeat("a", 64),
	}
	launcher := &ASBLauncher{
		Client: newTestASBClient(t, server),
		Config: ASBConfig{
			WireGuardCredentials: "wireguard-credentials",
		},
	}
	if err := launcher.ensureSandboxIdentityReady(
		context.Background(),
		"sandbox-123",
		identity,
	); err != nil {
		t.Fatalf("ensureSandboxIdentityReady: %v", err)
	}
	if wireguardGrant.OriginalSandboxID != "" {
		t.Fatalf("platform credential injection reused anchor sandbox %q", wireguardGrant.OriginalSandboxID)
	}
	if wireguardGrant.EmployeeID != identity.RawEmployeeID ||
		wireguardGrant.BUCAccessToken != identity.BUCTokens.AccessToken ||
		wireguardGrant.BUCRefreshToken != identity.BUCTokens.RefreshToken ||
		wireguardGrant.BUCIDToken != identity.BUCTokens.IDToken {
		t.Fatalf("WireGuard identity grant = %#v", wireguardGrant)
	}
	if spiffeGrant.RawEmployeeID != identity.RawEmployeeID ||
		spiffeGrant.AgentToken != identity.AgentIdentityToken ||
		spiffeGrant.AgentID != identity.AgentSPIFFEID {
		t.Fatalf("SPIFFE identity grant = %#v", spiffeGrant)
	}
}

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

func TestASBVerifyStableArtifactUsesSandboxDefaultUser(t *testing.T) {
	t.Parallel()

	const runtimeAPIKey = "runtime-owned-validation-key"
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
			if got := request.Header.Get(asbAPIKeyHeader); got != runtimeAPIKey {
				t.Errorf("ASB validation credential = %q, want the Runtime-owned credential", got)
			}
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

	secrets, err := secretbox.New(bytes.Repeat([]byte{0x42}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	encryptedAPIKey, err := secrets.Seal([]byte(runtimeAPIKey))
	if err != nil {
		t.Fatal(err)
	}
	runtimeID := util.MustParseUUID("11111111-1111-1111-1111-111111111111")
	launcher := &ASBLauncher{
		Config: ASBConfig{
			APIURL:         server.URL,
			TimeoutSeconds: 60,
			ReadyTimeout:   time.Second,
			ResourceCPU:    "2",
			ResourceMemory: "4Gi",
		},
		Credentials: &ASBRuntimeClientProvider{
			Store: &fakeASBRuntimeCredentialStore{
				credential: db.AsbRuntimeCredential{
					RuntimeID:       runtimeID,
					ApiKeyEncrypted: encryptedAPIKey,
					ApiKeyHint:      "n-key",
				},
			},
			Secrets: secrets,
			Config:  ASBConfig{APIURL: server.URL},
		},
	}
	got, err := launcher.VerifyStableArtifact(context.Background(), runtimeID, ASBArtifact{
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

func TestASBExecFailureDetailRedactsAndBoundsOutput(t *testing.T) {
	t.Parallel()

	exitCode := 141
	detail := asbExecFailureDetail(&ASBExecResult{
		ExitCode:  &exitCode,
		ErrorName: "CommandExecError",
		Stderr:    strings.Repeat("x", 5000) + "\nAPI_KEY=sk-abcdefghijklmnopqrstuvwxyz123456",
	})
	if !strings.Contains(detail, "exit_code=141") ||
		!strings.Contains(detail, "error_name=CommandExecError") {
		t.Fatalf("failure detail = %q", detail)
	}
	if strings.Contains(detail, "sk-abcdefghijklmnopqrstuvwxyz123456") ||
		!strings.Contains(detail, "[REDACTED CREDENTIAL]") {
		t.Fatalf("failure detail did not redact the credential: %q", detail)
	}
	if len([]rune(detail)) > 1300 {
		t.Fatalf("failure detail has %d runes, want a bounded diagnostic", len([]rune(detail)))
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
