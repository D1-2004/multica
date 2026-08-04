package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/chattrace"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeASBTaskIdentityResolver struct {
	identity ASBResolvedIdentity
	err      error
}

type fakeASBSandboxCapacity struct {
	t              *testing.T
	wantRuntimeID  pgtype.UUID
	createRequests int
}

func (capacity *fakeASBSandboxCapacity) Create(
	ctx context.Context,
	runtimeID pgtype.UUID,
	client *ASBClient,
	input ASBCreateSandboxInput,
) (*ASBSandbox, error) {
	capacity.t.Helper()
	if runtimeID != capacity.wantRuntimeID {
		capacity.t.Fatalf("capacity Runtime ID = %v, want %v", runtimeID, capacity.wantRuntimeID)
	}
	capacity.createRequests++
	return client.CreateSandbox(ctx, input)
}

func (resolver fakeASBTaskIdentityResolver) ResolveASBTaskIdentity(
	context.Context,
	pgtype.UUID,
	pgtype.UUID,
	pgtype.UUID,
) (ASBResolvedIdentity, error) {
	return resolver.identity, resolver.err
}

func (resolver fakeASBTaskIdentityResolver) AcquireASBTaskIdentitySource(
	context.Context,
	pgtype.UUID,
	pgtype.UUID,
	pgtype.UUID,
	string,
) (func(context.Context) error, error) {
	if resolver.err != nil {
		return nil, resolver.err
	}
	return func(context.Context) error { return nil }, nil
}

func TestASBLaunchIdentityAllowsExplicitUnboundMode(t *testing.T) {
	t.Parallel()

	launcher := &ASBLauncher{
		Identity: fakeASBTaskIdentityResolver{err: ErrEnterpriseIdentityNeedsReauth},
	}
	identity, err := launcher.resolveTaskIdentity(
		context.Background(),
		pgtype.UUID{},
		pgtype.UUID{},
		pgtype.UUID{},
	)
	if err != nil {
		t.Fatalf("resolveTaskIdentity: %v", err)
	}
	if identity.Mode != asbIdentityModeUnbound ||
		identity.Fingerprint != asbUnboundIdentityFingerprint {
		t.Fatalf("unbound identity = %#v", identity)
	}
	if extensions := identity.sandboxExtensions("wg-client"); len(extensions) != 0 {
		t.Fatalf("unbound sandbox extensions = %#v", extensions)
	}
}

func TestASBLaunchIdentityResolverFailureUsesUnboundMode(t *testing.T) {
	t.Parallel()

	resolverErr := errors.New("identity store unavailable")
	launcher := &ASBLauncher{
		Identity: fakeASBTaskIdentityResolver{err: resolverErr},
	}
	identity, err := launcher.resolveTaskIdentity(
		context.Background(),
		pgtype.UUID{},
		pgtype.UUID{},
		pgtype.UUID{},
	)
	if err != nil {
		t.Fatalf("resolveTaskIdentity: %v", err)
	}
	if identity != unboundASBResolvedIdentity() {
		t.Fatalf("identity = %#v, want unbound", identity)
	}
}

func TestASBLaunchWithoutIdentityServiceUsesUnboundMode(t *testing.T) {
	t.Parallel()

	launcher := &ASBLauncher{}
	identity, err := launcher.resolveTaskIdentity(
		context.Background(),
		pgtype.UUID{},
		pgtype.UUID{},
		pgtype.UUID{},
	)
	if err != nil {
		t.Fatalf("resolveTaskIdentity: %v", err)
	}
	if identity != unboundASBResolvedIdentity() {
		t.Fatalf("identity = %#v, want unbound", identity)
	}
}

func TestASBBoundIdentityKeepsIdentityExtensions(t *testing.T) {
	t.Parallel()

	identity := ASBResolvedIdentity{
		Mode:               asbIdentityModeBound,
		RawEmployeeID:      "12345",
		BUCAgentID:         "agent-multica-asb",
		AgentSPIFFEID:      "spiffe://multica.prod.ali/ns/default/agents/agent-1",
		AIPID:              "aip-1",
		SourceSandboxID:    "identity-source-1",
		SourceRuntimeID:    util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		AgentIdentityToken: "ait",
		Fingerprint:        strings.Repeat("a", 64),
	}
	if err := identity.validate(); err != nil {
		t.Fatalf("validate bound identity: %v", err)
	}
	extensions := identity.sandboxExtensions("wireguard-credentials")
	for key, expected := range map[string]string{
		"spiffe.lazyAuth":          "true",
		"wireguard.worker":         identity.RawEmployeeID,
		"wireguard.uemCredentials": "wireguard-credentials",
		"buc.originalSandboxID":    identity.SourceSandboxID,
	} {
		if extensions[key] != expected {
			t.Fatalf("bound sandbox extension %s = %q, want %q", key, extensions[key], expected)
		}
	}
	if _, ok := extensions["wireguard.lazyAuth"]; ok {
		t.Fatal("bound sandbox extensions mix lazy WireGuard auth with create-time identity")
	}
}

func TestASBLauncherUsesCreateTimeBUCAndAttachesAgentIdentityBeforeProbe(t *testing.T) {
	t.Parallel()

	var spiffeGrant ASBAgentIdentityGrant
	var spiffeAttached atomic.Bool
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/sandboxes/sandbox-123/endpoints/44772":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"endpoint": server.URL + "/execd",
				"headers":  map[string]string{"X-Sandbox-Token": testEndpointToken},
			})
		case request.Method == http.MethodPost &&
			request.URL.Path == "/execd/command":
			if !spiffeAttached.Load() {
				http.Error(response, "SPIFFE identity must be attached before the CLI probe", http.StatusConflict)
				return
			}
			response.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(response, `data: {"type":"execution_complete","execution_time":1}`+"\n")
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/sandboxes/sandbox-123/identity/spiffe":
			if err := json.NewDecoder(request.Body).Decode(&spiffeGrant); err != nil {
				t.Fatalf("decode SPIFFE identity: %v", err)
			}
			spiffeAttached.Store(true)
			response.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	identity := ASBResolvedIdentity{
		Mode:               asbIdentityModeBound,
		RawEmployeeID:      "12345",
		BUCAgentID:         "agent-multica-asb",
		AgentSPIFFEID:      "spiffe://multica.prod.ali/ns/default/agents/agent-1",
		AIPID:              "aip-1",
		SourceSandboxID:    "identity-source-1",
		SourceRuntimeID:    util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		AgentIdentityToken: "ait",
		Fingerprint:        strings.Repeat("a", 64),
	}
	launcher := &ASBLauncher{
		Client: newTestASBClient(t, server),
		Config: ASBConfig{
			WireGuardCredentials:  "wireguard-credentials",
			WireGuardReadyTimeout: time.Second,
		},
	}
	if err := launcher.ensureSandboxIdentityReady(
		context.Background(),
		"sandbox-123",
		identity,
		launcher.Config.WireGuardReadyTimeout,
	); err != nil {
		t.Fatalf("ensureSandboxIdentityReady: %v", err)
	}
	if spiffeGrant.RawEmployeeID != identity.RawEmployeeID ||
		spiffeGrant.AgentToken != identity.AgentIdentityToken ||
		spiffeGrant.AgentID != identity.AgentSPIFFEID {
		t.Fatalf("SPIFFE identity grant = %#v", spiffeGrant)
	}
}

func TestASBLauncherProbesAfterSPIFFEAttachmentCSI502(t *testing.T) {
	t.Parallel()

	var probeObserved atomic.Bool
	var attachCalls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/sandboxes/sandbox-123/endpoints/44772":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"endpoint": server.URL + "/execd",
				"headers":  map[string]string{"X-Sandbox-Token": testEndpointToken},
			})
		case request.Method == http.MethodPost && request.URL.Path == "/execd/command":
			probeObserved.Store(true)
			response.Header().Set("Content-Type", "text/event-stream")
			if attachCalls.Load() < 2 {
				_, _ = io.WriteString(response, `data: {"type":"stderr","text":"probe_stage=a1\n"}`+"\n")
				_, _ = io.WriteString(response, `data: {"type":"error","error":{"ename":"CommandExecError","evalue":"1"}}`+"\n")
			}
			_, _ = io.WriteString(response, `data: {"type":"execution_complete","execution_time":1}`+"\n")
		case request.Method == http.MethodPost &&
			request.URL.Path == "/v1/sandboxes/sandbox-123/identity/spiffe":
			if attachCalls.Add(1) >= 2 {
				response.WriteHeader(http.StatusAccepted)
				return
			}
			response.Header().Set("Content-Type", "application/json")
			response.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(response, `{
				"code":"BAD_REQUEST",
				"message":"failed to attach sandbox spiffe identity to sandbox-123, got status code 502"
			}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	launcher := &ASBLauncher{
		Client: newTestASBClient(t, server),
		Config: ASBConfig{
			WireGuardCredentials:  "wireguard-credentials",
			WireGuardReadyTimeout: 500 * time.Millisecond,
		},
	}
	identity := ASBResolvedIdentity{
		Mode:               asbIdentityModeBound,
		RawEmployeeID:      "12345",
		BUCAgentID:         "agent-multica-asb",
		AgentSPIFFEID:      "spiffe://multica.prod.ali/ns/default/agents/agent-1",
		AgentIdentityToken: "ait",
		SourceSandboxID:    "identity-source-1",
	}
	if err := launcher.ensureSandboxIdentityReady(
		context.Background(),
		"sandbox-123",
		identity,
		launcher.Config.WireGuardReadyTimeout,
	); err != nil {
		t.Fatalf("ensureSandboxIdentityReady: %v", err)
	}
	if !probeObserved.Load() {
		t.Fatal("enterprise CLI probe was not executed after the converging attachment response")
	}
	if attachCalls.Load() != 2 {
		t.Fatalf("SPIFFE attachment calls = %d, want 2", attachCalls.Load())
	}
}

func TestASBOptionalIdentityAttachmentDoesNotBlockTaskStart(t *testing.T) {
	t.Parallel()

	requestStarted := make(chan struct{})
	releaseRequest := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost ||
			request.URL.Path != "/v1/sandboxes/sandbox-123/identity/spiffe" {
			http.NotFound(response, request)
			return
		}
		close(requestStarted)
		<-releaseRequest
		response.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	launcher := &ASBLauncher{Client: newTestASBClient(t, server)}
	startedAt := time.Now()
	launcher.attachSandboxIdentityAfterTaskStart(
		util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
		"sandbox-123",
		ASBResolvedIdentity{
			Mode:               asbIdentityModeBound,
			RawEmployeeID:      "12345",
			AgentSPIFFEID:      "spiffe://multica.prod.ali/ns/default/agents/agent-1",
			AgentIdentityToken: "ait",
		},
	)
	if elapsed := time.Since(startedAt); elapsed > 100*time.Millisecond {
		t.Fatalf("optional identity attachment blocked for %s", elapsed)
	}
	select {
	case <-requestStarted:
	case <-time.After(time.Second):
		t.Fatal("optional identity attachment request was not started")
	}
	close(releaseRequest)
}

func TestASBAgentIdentityAttachmentConvergingRejectsOtherBadRequest(t *testing.T) {
	t.Parallel()

	err := &ASBHTTPError{
		Operation:    "attach_agent_identity",
		StatusCode:   http.StatusBadRequest,
		ErrorMessage: "identity grant is invalid",
	}
	if isASBAgentIdentityAttachmentConverging(err) {
		t.Fatal("unrelated HTTP 400 must not be classified as a converging SPIFFE attachment")
	}
}

func TestASBLauncherReadsTenantQuotaBeforeCreatingSandbox(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		response  string
		exhausted bool
		wantErr   bool
	}{
		{
			name:      "slot available",
			response:  `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":5,"usage":4}]`,
			exhausted: false,
		},
		{
			name:      "quota exhausted",
			response:  `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":5,"usage":5}]`,
			exhausted: true,
		},
		{
			name:     "missing allocation",
			response: `[]`,
			wantErr:  true,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(
				response http.ResponseWriter,
				request *http.Request,
			) {
				if request.Method != http.MethodGet ||
					request.URL.Path != "/v1/sandboxes/quotas" {
					http.NotFound(response, request)
					return
				}
				response.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(response, test.response)
			}))
			defer server.Close()

			client := newTestASBClient(t, server)
			exhausted, err := asbTenantQuotaExhausted(context.Background(), client)
			if test.wantErr {
				if err == nil {
					t.Fatal("asbTenantQuotaExhausted unexpectedly succeeded")
				}
				return
			}
			if err != nil {
				t.Fatalf("asbTenantQuotaExhausted: %v", err)
			}
			if exhausted != test.exhausted {
				t.Fatalf("quota exhausted = %v, want %v", exhausted, test.exhausted)
			}
		})
	}
}

func TestInspectReusableASBSandboxRejectsUnavailableStates(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch request.URL.Path {
		case "/v1/sandboxes/running":
			_, _ = io.WriteString(response, `{"id":"running","status":{"state":"Running"},"createdAt":"2026-08-04T08:00:00Z"}`)
		case "/v1/sandboxes/terminated":
			_, _ = io.WriteString(response, `{"id":"terminated","status":{"state":"Terminated"},"createdAt":"2026-08-04T08:00:00Z"}`)
		case "/v1/sandboxes/failed":
			_, _ = io.WriteString(response, `{"id":"failed","status":{"state":"Failed"},"createdAt":"2026-08-04T08:00:00Z"}`)
		case "/v1/sandboxes/missing":
			response.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(response, `{"code":"NOT_FOUND","message":"sandbox not found"}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()
	client := newTestASBClient(t, server)

	tests := []struct {
		name         string
		sandboxID    string
		wantReusable bool
		wantState    string
	}{
		{name: "running", sandboxID: "running", wantReusable: true, wantState: "Running"},
		{name: "terminated", sandboxID: "terminated", wantState: "Terminated"},
		{name: "failed", sandboxID: "failed", wantState: "Failed"},
		{name: "missing", sandboxID: "missing", wantState: "not_found"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			reusable, state, err := inspectReusableASBSandbox(
				context.Background(),
				client,
				test.sandboxID,
			)
			if err != nil {
				t.Fatalf("inspect reusable sandbox: %v", err)
			}
			if reusable != test.wantReusable || state != test.wantState {
				t.Fatalf(
					"inspection = (reusable=%v, state=%q), want (reusable=%v, state=%q)",
					reusable,
					state,
					test.wantReusable,
					test.wantState,
				)
			}
		})
	}
}

func TestResolveASBSandboxReplacesUnavailableWarmSession(t *testing.T) {
	pool := newSandboxLockPool(t)
	workspaceID, userID, runtimeID := seedFCE2BSandboxRuntime(
		t,
		pool,
		"Unavailable ASB Warm Session",
	)
	baseQueries := db.New(pool)
	runtime, err := baseQueries.GetAgentRuntime(context.Background(), runtimeID)
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}

	var createCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/quotas":
			_, _ = io.WriteString(response, `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":5,"usage":1}]`)
		case request.Method == http.MethodPost && request.URL.Path == "/v1/sandboxes":
			id := "replacement-" + strconv.Itoa(int(createCalls.Add(1)))
			response.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(response, `{"id":"`+id+`","status":{"state":"Pending"},"createdAt":"2026-08-04T08:00:00Z"}`)
		case request.Method == http.MethodGet && strings.HasPrefix(request.URL.Path, "/v1/sandboxes/replacement-"):
			id := strings.TrimPrefix(request.URL.Path, "/v1/sandboxes/")
			_, _ = io.WriteString(response, `{"id":"`+id+`","status":{"state":"Running"},"createdAt":"2026-08-04T08:00:00Z"}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/terminated-warm":
			_, _ = io.WriteString(response, `{"id":"terminated-warm","status":{"state":"Terminated"},"createdAt":"2026-08-04T07:00:00Z"}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/missing-warm":
			response.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(response, `{"code":"NOT_FOUND","message":"sandbox not found"}`)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	box, err := secretbox.New(bytes.Repeat([]byte{0x52}, secretbox.KeySize))
	if err != nil {
		t.Fatal(err)
	}
	encryptedAPIKey, err := box.Seal([]byte(testASBAPIKey))
	if err != nil {
		t.Fatal(err)
	}
	credential := db.AsbRuntimeCredential{
		RuntimeID:       runtimeID,
		ApiKeyEncrypted: encryptedAPIKey,
		ApiKeyHint:      "-key",
	}
	credentials := &ASBRuntimeClientProvider{
		Store: &fakeASBRuntimeCredentialStore{
			credential:  credential,
			credentials: []db.AsbRuntimeCredential{credential},
		},
		Secrets: box,
		Config:  ASBConfig{APIURL: server.URL},
	}
	common := NewFCE2BLauncher(baseQueries, nil, FCE2BConfig{}, nil)
	common.SetPool(pool)
	launcher := &ASBLauncher{
		Common:      common,
		Config:      ASBConfig{TimeoutSeconds: 300, ReadyTimeout: time.Second, ResourceCPU: "2", ResourceMemory: "4Gi"},
		Client:      newTestASBClient(t, server),
		Credentials: credentials,
		Pool:        pool,
	}
	metadata := CloudSandboxRuntimeMetadata{
		ArtifactRef: "registry.example/runtime@sha256:" + strings.Repeat("a", 64),
	}

	tests := []struct {
		name         string
		scopeID      pgtype.UUID
		oldSandboxID string
	}{
		{name: "terminated", scopeID: workspaceID, oldSandboxID: "terminated-warm"},
		{name: "not found", scopeID: userID, oldSandboxID: "missing-warm"},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			scope := fcE2BTaskScope{typ: fcE2BScopeTypeChat, id: test.scopeID}
			if _, err := baseQueries.UpsertCloudSandboxSession(
				context.Background(),
				db.UpsertCloudSandboxSessionParams{
					WorkspaceID:         workspaceID,
					RuntimeID:           runtimeID,
					ScopeType:           scope.typ,
					ScopeID:             scope.id,
					SandboxID:           test.oldSandboxID,
					ArtifactRef:         metadata.ArtifactRef,
					ExpiresAt:           pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
					SandboxBackend:      string(SandboxBackendASB),
					IdentityFingerprint: asbUnboundIdentityFingerprint,
				},
			); err != nil {
				t.Fatalf("seed warm sandbox session: %v", err)
			}

			conn, err := pool.Acquire(context.Background())
			if err != nil {
				t.Fatalf("acquire runtime lock connection: %v", err)
			}
			launcher.Queries = db.New(conn)
			sandboxID, coldStart, _, err := launcher.resolveSandbox(
				context.Background(),
				runtime,
				metadata,
				scope,
				true,
				userID,
				pgtype.UUID{},
				unboundASBResolvedIdentity(),
				conn,
				chattrace.New("task"),
			)
			conn.Release()
			if err != nil {
				t.Fatalf("resolve sandbox: %v", err)
			}
			wantSandboxID := "replacement-" + strconv.Itoa(index+1)
			if sandboxID != wantSandboxID || !coldStart {
				t.Fatalf("resolved sandbox = (%q, cold=%v), want (%q, true)", sandboxID, coldStart, wantSandboxID)
			}

			session, err := baseQueries.GetActiveCloudSandboxSession(
				context.Background(),
				db.GetActiveCloudSandboxSessionParams{
					RuntimeID:           runtimeID,
					ScopeType:           scope.typ,
					ScopeID:             scope.id,
					SandboxBackend:      string(SandboxBackendASB),
					IdentityFingerprint: asbUnboundIdentityFingerprint,
					ArtifactRef:         metadata.ArtifactRef,
				},
			)
			if err != nil {
				t.Fatalf("load replacement session: %v", err)
			}
			if session.SandboxID != wantSandboxID {
				t.Fatalf("persisted sandbox = %q, want %q", session.SandboxID, wantSandboxID)
			}
		})
	}
	if createCalls.Load() != int32(len(tests)) {
		t.Fatalf("ASB create calls = %d, want %d", createCalls.Load(), len(tests))
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
	capacity := &fakeASBSandboxCapacity{
		t:             t,
		wantRuntimeID: runtimeID,
	}
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
		Capacity: capacity,
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
	if capacity.createRequests != 1 {
		t.Fatalf("capacity create requests = %d, want 1", capacity.createRequests)
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
	commandRequests := 0
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
			commandRequests++
			if commandRequests == 1 {
				http.Error(response, "execd is still starting", http.StatusBadGateway)
				return
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
			ReadyTimeout: 3 * time.Second,
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
	if commandRequests != 3 {
		t.Fatalf("ASB command requests = %d, want readiness retry plus one runner submission", commandRequests)
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
