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
	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type fakeASBTaskIdentityResolver struct {
	identity ASBResolvedIdentity
	err      error
	released *atomic.Bool
	calls    *atomic.Int32
}

func TestASBConfigCommandReadyTimeout(t *testing.T) {
	t.Setenv("MULTICA_ASB_READY_TIMEOUT", "")
	if got := ASBConfigFromEnv().ReadyTimeout; got != 6*time.Minute {
		t.Fatalf("default sandbox ready timeout = %s, want 6m", got)
	}

	t.Setenv("MULTICA_ASB_COMMAND_READY_TIMEOUT", "")
	if got := ASBConfigFromEnv().CommandReadyTimeout; got != 7*time.Minute {
		t.Fatalf("default command ready timeout = %s, want 7m", got)
	}

	t.Setenv("MULTICA_ASB_COMMAND_READY_TIMEOUT", "9m")
	if got := ASBConfigFromEnv().CommandReadyTimeout; got != 9*time.Minute {
		t.Fatalf("configured command ready timeout = %s, want 9m", got)
	}
}

func TestNeedsCandidateASBArtifactActivation(t *testing.T) {
	readyMetadata := map[string]any{
		"artifact_status":  "READY",
		"manifest_version": float64(7),
	}
	pendingMetadata := map[string]any{
		"artifact_status":  "PENDING_STABLE_VALIDATION",
		"manifest_version": float64(0),
	}
	candidateMetadata := map[string]any{"artifact_channel": CloudSandboxChannelCandidate}
	stableMetadata := map[string]any{"artifact_channel": CloudSandboxChannelStable}

	tests := []struct {
		name     string
		status   string
		metadata map[string]any
		managed  map[string]any
		want     bool
	}{
		{
			name:     "offline bootstrap",
			status:   "offline",
			metadata: pendingMetadata,
			managed:  candidateMetadata,
			want:     true,
		},
		{
			name:     "online bootstrap with pending manifest",
			status:   "online",
			metadata: pendingMetadata,
			managed:  candidateMetadata,
			want:     true,
		},
		{
			name:     "offline candidate with verified manifest",
			status:   "offline",
			metadata: readyMetadata,
			managed:  candidateMetadata,
			want:     true,
		},
		{
			name:     "online verified candidate",
			status:   "online",
			metadata: readyMetadata,
			managed:  candidateMetadata,
			want:     false,
		},
		{
			name:     "stable release update never activates",
			status:   "offline",
			metadata: pendingMetadata,
			managed:  stableMetadata,
			want:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := needsCandidateASBArtifactActivation(
				db.AgentRuntime{Status: tt.status},
				tt.metadata,
				tt.managed,
			)
			if got != tt.want {
				t.Fatalf("activation = %v, want %v", got, tt.want)
			}
		})
	}
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
	if resolver.calls != nil {
		resolver.calls.Add(1)
	}
	return resolver.identity, resolver.err
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

func TestASBA2ATaskSkipsAgentEnterpriseIdentity(t *testing.T) {
	t.Parallel()

	bound := ASBResolvedIdentity{
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
	agentID := util.MustParseUUID("22222222-2222-2222-2222-222222222222")
	runtimeID := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	var calls atomic.Int32
	launcher := &ASBLauncher{Identity: fakeASBTaskIdentityResolver{identity: bound, calls: &calls}}
	identity, err := launcher.resolveTaskIdentityForTask(
		context.Background(),
		db.AgentTaskQueue{
			AgentID:   agentID,
			RuntimeID: runtimeID,
			Context:   newA2ATaskContext(),
		},
		pgtype.UUID{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if identity != unboundASBResolvedIdentity() {
		t.Fatalf("A2A identity = %#v, want unbound", identity)
	}
	if calls.Load() != 0 {
		t.Fatalf("A2A task resolved Agent enterprise identity %d times", calls.Load())
	}
}

func TestASBA2ADEAPDWSTaskResolvesAgentEnterpriseIdentity(t *testing.T) {
	t.Parallel()

	want := ASBResolvedIdentity{
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
	var calls atomic.Int32
	launcher := &ASBLauncher{Identity: fakeASBTaskIdentityResolver{identity: want, calls: &calls}}
	identity, err := launcher.resolveTaskIdentityForTask(
		context.Background(),
		db.AgentTaskQueue{
			AgentID:   util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
			RuntimeID: util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
			Context: newA2ATaskContext(a2aintegration.InvocationIdentity{
				DEAPDWSToken: "request-token",
			}),
		},
		pgtype.UUID{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if identity != want {
		t.Fatalf("DEAP DWS A2A identity = %#v, want %#v", identity, want)
	}
	if calls.Load() != 1 {
		t.Fatalf("DEAP DWS A2A identity resolver calls = %d, want 1", calls.Load())
	}
}

func TestASBOrdinaryTaskResolvesAgentEnterpriseIdentity(t *testing.T) {
	t.Parallel()

	want := ASBResolvedIdentity{
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
	var calls atomic.Int32
	launcher := &ASBLauncher{Identity: fakeASBTaskIdentityResolver{identity: want, calls: &calls}}
	identity, err := launcher.resolveTaskIdentityForTask(
		context.Background(),
		db.AgentTaskQueue{
			AgentID:   util.MustParseUUID("22222222-2222-2222-2222-222222222222"),
			RuntimeID: util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
		},
		pgtype.UUID{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if identity != want {
		t.Fatalf("ordinary task identity = %#v, want %#v", identity, want)
	}
	if calls.Load() != 1 {
		t.Fatalf("ordinary task identity resolver calls = %d, want 1", calls.Load())
	}
}

func TestASBBoundIdentityDeclaresLazyWireGuardAttach(t *testing.T) {
	t.Parallel()

	identity := ASBResolvedIdentity{
		Mode:               asbIdentityModeBound,
		RawEmployeeID:      "12345",
		BUCAgentID:         "agent-multica-asb",
		AgentSPIFFEID:      "spiffe://multica.prod.ali/ns/default/agents/agent-1",
		AIPID:              "aip-1",
		AgentIdentityToken: "ait",
		BUCTokens: BUCIdentityTokens{
			AccessToken:  "buc-access",
			RefreshToken: "buc-refresh",
			IDToken:      "buc-id",
			ExpiresIn:    3600,
		},
		Fingerprint: strings.Repeat("a", 64),
	}
	if err := identity.validate(); err != nil {
		t.Fatalf("validate bound identity: %v", err)
	}
	extensions := identity.sandboxExtensions("wireguard-credentials")
	for key, expected := range map[string]string{
		"spiffe.lazyAuth":    "true",
		"wireguard.lazyAuth": "true",
	} {
		if extensions[key] != expected {
			t.Fatalf("bound sandbox extension %s = %q, want %q", key, extensions[key], expected)
		}
	}
	for _, forbidden := range []string{"wireguard.worker", "buc.originalSandboxID"} {
		if _, ok := extensions[forbidden]; ok {
			t.Fatalf("bound sandbox extension %q still inherits a terminated seed", forbidden)
		}
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

func TestResolveASBSandboxAttachesFreshBUCTokensBeforeProbe(t *testing.T) {
	pool := newSandboxLockPool(t)
	_, agentID, runtimeID := seedFCE2BSandboxRuntime(
		t,
		pool,
		"Bound ASB Identity Source Lease",
	)
	queries := db.New(pool)
	runtime, err := queries.GetAgentRuntime(context.Background(), runtimeID)
	if err != nil {
		t.Fatalf("load runtime: %v", err)
	}

	attached := &atomic.Bool{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set("Content-Type", "application/json")
		switch {
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/quotas":
			_, _ = io.WriteString(response, `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":5,"usage":1}]`)
		case request.Method == http.MethodPost && request.URL.Path == "/v1/sandboxes":
			response.WriteHeader(http.StatusAccepted)
			_, _ = io.WriteString(response, `{"id":"bound-sandbox","status":{"state":"Pending"},"createdAt":"2026-08-04T08:00:00Z"}`)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/bound-sandbox":
			_, _ = io.WriteString(response, `{"id":"bound-sandbox","status":{"state":"Running"},"createdAt":"2026-08-04T08:00:00Z"}`)
		case request.Method == http.MethodPost && request.URL.Path == "/v1/sandboxes/bound-sandbox/identity/wireguard":
			var grant ASBBUCIdentityGrant
			if err := json.NewDecoder(request.Body).Decode(&grant); err != nil {
				t.Fatalf("decode BUC attach grant: %v", err)
			}
			if grant.OriginalSandboxID != "" ||
				grant.BUCAccessToken != "buc-access" ||
				grant.BUCRefreshToken != "buc-refresh" ||
				grant.BUCIDToken != "buc-id" ||
				grant.EmployeeID != "12345" {
				t.Fatalf("BUC attach grant = %#v", grant)
			}
			attached.Store(true)
			response.WriteHeader(http.StatusOK)
		case request.Method == http.MethodGet && request.URL.Path == "/v1/sandboxes/bound-sandbox/endpoints/44772":
			_ = json.NewEncoder(response).Encode(map[string]any{
				"endpoint": server.URL + "/execd",
				"headers":  map[string]string{"X-Sandbox-Token": testEndpointToken},
			})
		case request.Method == http.MethodPost && request.URL.Path == "/execd/command":
			if !attached.Load() {
				t.Error("BUC identity was probed before attachWireguardIdentity")
			}
			var payload asbExecRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode BUC identity probe: %v", err)
			}
			if payload.Envs["EXPECTED_EMP_ID"] != "12345" ||
				payload.Envs["EXPECTED_BUC_AGENT_ID"] != "agent-multica-asb" ||
				!strings.Contains(payload.Command, "get_zt_identity.json") {
				t.Fatalf("BUC identity probe = %#v", payload)
			}
			response.Header().Set("Content-Type", "text/event-stream")
			_, _ = io.WriteString(response, `data: {"type":"execution_complete","execution_time":1}`+"\n")
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	box, err := secretbox.New(bytes.Repeat([]byte{0x53}, secretbox.KeySize))
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
	identity := ASBResolvedIdentity{
		Mode:               asbIdentityModeBound,
		RawEmployeeID:      "12345",
		BUCAgentID:         "agent-multica-asb",
		AgentSPIFFEID:      "spiffe://multica.prod.ali/ns/default/agents/agent-1",
		AIPID:              "aip-1",
		AgentIdentityToken: "ait",
		BUCTokens: BUCIdentityTokens{
			AccessToken:  "buc-access",
			RefreshToken: "buc-refresh",
			IDToken:      "buc-id",
			ExpiresIn:    3600,
		},
		Fingerprint: strings.Repeat("a", 64),
	}
	launcher := &ASBLauncher{
		Queries:     queries,
		Config:      ASBConfig{TimeoutSeconds: 300, ReadyTimeout: time.Second, WireGuardReadyTimeout: time.Second, ResourceCPU: "2", ResourceMemory: "4Gi", WireGuardCredentials: "wireguard-credentials"},
		Client:      newTestASBClient(t, server),
		Credentials: credentials,
		Identity: fakeASBTaskIdentityResolver{
			identity: identity,
		},
	}
	conn, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("acquire runtime lock connection: %v", err)
	}
	defer conn.Release()
	launcher.Queries = db.New(conn)
	sandboxID, coldStart, resolvedIdentity, err := launcher.resolveSandbox(
		context.Background(),
		runtime,
		CloudSandboxRuntimeMetadata{ArtifactRef: "registry.example/runtime@sha256:" + strings.Repeat("a", 64)},
		fcE2BTaskScope{},
		false,
		agentID,
		pgtype.UUID{},
		identity,
		conn,
		chattrace.New("task"),
	)
	if err != nil {
		t.Fatalf("resolve sandbox: %v", err)
	}
	if sandboxID != "bound-sandbox" || !coldStart || resolvedIdentity != identity {
		t.Fatalf("resolution = (%q, %v, %#v)", sandboxID, coldStart, resolvedIdentity)
	}
	if !attached.Load() {
		t.Fatal("new sandbox did not attach a refreshed BUC token trio")
	}
}

func TestWaitSandboxInheritedBUCIdentityReadyRetriesUntilEmployeeMatches(t *testing.T) {
	t.Parallel()

	var execCalls atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch {
		case request.Method == http.MethodGet &&
			request.URL.Path == "/v1/sandboxes/probe-sandbox/endpoints/44772":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(map[string]any{
				"endpoint": server.URL + "/execd",
				"headers":  map[string]string{"X-Sandbox-Token": testEndpointToken},
			})
		case request.Method == http.MethodPost && request.URL.Path == "/execd/command":
			var payload asbExecRequest
			if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
				t.Fatalf("decode inherited identity probe: %v", err)
			}
			if payload.Envs["EXPECTED_EMP_ID"] != "12345" {
				t.Fatalf("expected employee = %q", payload.Envs["EXPECTED_EMP_ID"])
			}
			response.Header().Set("Content-Type", "text/event-stream")
			if execCalls.Add(1) == 1 {
				_, _ = io.WriteString(response, `data: {"type":"error","error":{"name":"ExitCode","value":"42"}}`+"\n")
			}
			_, _ = io.WriteString(response, `data: {"type":"execution_complete","execution_time":1}`+"\n")
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	launcher := &ASBLauncher{Client: newTestASBClient(t, server)}
	if err := launcher.waitSandboxInheritedBUCIdentityReady(
		context.Background(),
		"probe-sandbox",
		"12345",
		time.Second,
	); err != nil {
		t.Fatalf("wait for inherited BUC identity: %v", err)
	}
	if got := execCalls.Load(); got != 2 {
		t.Fatalf("inherited BUC probe calls = %d, want 2", got)
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

func TestValidateASBManifestVersionContract(t *testing.T) {
	t.Parallel()

	manifest := func(version int, capabilities ...string) map[string]any {
		providers := []string{"hermes", "opencode", "pi"}
		if version == 7 {
			providers = append(providers, "dsh", "opencode-v2")
		}
		return map[string]any{
			"schema_version":   version,
			"sandbox_backends": []string{"aliyun_fc", "asb"},
			"providers":        providers,
			"capabilities_by_backend": map[string][]string{
				"asb": append([]string{"dws", "mcp", "a1", "mw", "buc"}, capabilities...),
			},
			"identity_modes_by_backend": map[string][]string{
				"asb": {"agent_identity", "spiffe", "buc_wireguard"},
			},
			"runner_protocol": string(fcE2BRunnerLaunchRootLog),
		}
	}

	tests := []struct {
		name           string
		manifest       map[string]any
		wantRuntimeErr bool
		wantReleaseErr bool
	}{
		{
			name:           "existing schema v3 runtime",
			manifest:       manifest(3, RuntimeStartCapabilityEventsV1),
			wantReleaseErr: true,
		},
		{
			name:           "existing schema v4 runtime",
			manifest:       manifest(4, RuntimeStartCapabilityEventsV1, LLMTraceCapability),
			wantReleaseErr: true,
		},
		{
			name:           "existing schema v5 runtime",
			manifest:       manifest(5, RuntimeStartCapabilityEventsV1, LLMTraceCapability),
			wantReleaseErr: true,
		},
		{
			name:           "existing schema v6 runtime",
			manifest:       manifest(6, RuntimeStartCapabilityEventsV1, LLMTraceCapability, A2AInvocationV2Capability),
			wantReleaseErr: true,
		},
		{
			name:           "schema v7 release missing trace capability",
			manifest:       manifest(7, RuntimeStartCapabilityEventsV1, A2AInvocationV2Capability, DSHTrajectoryCapability),
			wantRuntimeErr: false,
			wantReleaseErr: true,
		},
		{
			name:           "schema v7 release missing A2A invocation capability",
			manifest:       manifest(7, RuntimeStartCapabilityEventsV1, LLMTraceCapability, DSHTrajectoryCapability),
			wantReleaseErr: true,
		},
		{
			name:           "schema v7 release missing DSH trajectory capability",
			manifest:       manifest(7, RuntimeStartCapabilityEventsV1, LLMTraceCapability, A2AInvocationV2Capability),
			wantRuntimeErr: true,
			wantReleaseErr: true,
		},
		{
			name:     "current schema v7 release",
			manifest: manifest(7, RuntimeStartCapabilityEventsV1, LLMTraceCapability, A2AInvocationV2Capability, DSHTrajectoryCapability),
		},
		{
			name:           "unsupported schema v2",
			manifest:       manifest(2, RuntimeStartCapabilityEventsV1),
			wantRuntimeErr: true,
			wantReleaseErr: true,
		},
		{
			name:           "unsupported schema v8",
			manifest:       manifest(8, RuntimeStartCapabilityEventsV1, LLMTraceCapability),
			wantRuntimeErr: true,
			wantReleaseErr: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if gotErr := validateASBRuntimeManifest(test.manifest) != nil; gotErr != test.wantRuntimeErr {
				t.Fatalf("validateASBRuntimeManifest() error = %v, want error %v", gotErr, test.wantRuntimeErr)
			}
			if gotErr := validateASBReleaseManifest(test.manifest) != nil; gotErr != test.wantReleaseErr {
				t.Fatalf("validateASBReleaseManifest() error = %v, want error %v", gotErr, test.wantReleaseErr)
			}
		})
	}
}

func TestASBVerifyStableArtifactUsesSandboxDefaultUser(t *testing.T) {
	t.Parallel()

	const runtimeAPIKey = "runtime-owned-validation-key"
	digest := "sha256:" + strings.Repeat("a", 64)
	manifest := map[string]any{
		"schema_version":   7,
		"sandbox_backends": []string{"aliyun_fc", "asb"},
		"providers":        []string{"hermes", "opencode", "pi", "dsh", "opencode-v2"},
		"capabilities_by_backend": map[string][]string{
			"asb": {"dws", "mcp", "a1", "mw", "buc", RuntimeStartCapabilityEventsV1, LLMTraceCapability, A2AInvocationV2Capability, DSHTrajectoryCapability},
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
	if intMetadataValue(got, "schema_version") != 7 {
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

func TestASBExecRunOnceUsesDefaultUserAndUnboundedBackgroundCommand(t *testing.T) {
	t.Parallel()

	var captured asbExecRequest
	var capturedBody []byte
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
			body, err := io.ReadAll(request.Body)
			if err != nil {
				t.Fatalf("read exec request: %v", err)
			}
			capturedBody = append(capturedBody[:0], body...)
			captured = asbExecRequest{}
			if err := json.Unmarshal(body, &captured); err != nil {
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
			ReadyTimeout:        5 * time.Minute,
			CommandReadyTimeout: 3 * time.Second,
			LLMBaseURL:          "https://models.example/v1",
			LLMAPIKey:           "test-model-key",
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
		map[string]string{
			llmTraceEnabledEnvKey: "true",
			llmTraceSinkURLEnvKey: "https://trace.example.test/ingest",
		},
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
	if captured.Timeout != 0 || bytes.Contains(capturedBody, []byte(`"timeout"`)) {
		t.Fatalf("ASB background runner must not have a command timeout: request=%s", capturedBody)
	}
	if captured.Envs["MULTICA_RUNNER_PROVIDER"] != "hermes" ||
		captured.Envs["HOME"] != asbRunnerHome ||
		captured.Envs["USER"] != "user" ||
		captured.Envs["LOGNAME"] != "user" ||
		captured.Envs[llmTraceEnabledEnvKey] != "true" ||
		captured.Envs[llmTraceSinkURLEnvKey] != "https://trace.example.test/ingest" {
		t.Fatalf("ASB runner environment = %#v", captured.Envs)
	}
}

func TestWaitSandboxCommandReadyUsesDedicatedTimeout(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/v1/sandboxes/sandbox-123/endpoints/44772":
			response.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(response).Encode(map[string]any{
				"endpoint": strings.TrimPrefix(serverURLFromRequest(request), "http://") + "/execd",
				"headers":  map[string]string{"X-Sandbox-Token": testEndpointToken},
			})
		case "/execd/command":
			http.Error(response, "execd is still starting", http.StatusBadGateway)
		default:
			http.NotFound(response, request)
		}
	}))
	defer server.Close()

	launcher := &ASBLauncher{
		Client: newTestASBClient(t, server),
		Config: ASBConfig{
			ReadyTimeout:        time.Millisecond,
			CommandReadyTimeout: 50 * time.Millisecond,
		},
	}
	started := time.Now()
	_, err := launcher.waitSandboxCommandReady(context.Background(), testSandboxID)
	if err == nil || !strings.Contains(err.Error(), "was not ready within 50ms") {
		t.Fatalf("wait command ready error = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("dedicated command timeout elapsed = %s, want less than 1s", elapsed)
	}
}
