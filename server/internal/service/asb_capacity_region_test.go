package service

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	"github.com/multica-ai/multica/server/internal/util/secretbox"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func newASBRegionalTestClient(t *testing.T, handler http.HandlerFunc) *ASBClient {
	t.Helper()
	client, err := NewASBClient(ASBClientConfig{
		BaseURL: "https://sandbox.aone.alibaba-inc.com", APIKey: testASBAPIKey,
		HTTPClient: &http.Client{Transport: a2aRoundTripperFunc(func(r *http.Request) (*http.Response, error) {
			w := httptest.NewRecorder()
			handler(w, r)
			return w.Result(), nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestASBCapacityRegionalQuotaRefreshAndFailover(t *testing.T) {
	const zjk = "sandbox-cn-zhangjiakou.aone.alibaba-inc.com"
	const hz = "sandbox-cn-hangzhou.aone.alibaba-inc.com"
	const oldFree = `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":1,"usage":0}]`
	const newFree = `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":1,"usage":1},{"networkZone":"ALITest","region":"cn-hangzhou","quota":10,"usage":0}]`
	const bothFree = `[{"networkZone":"ALITest","region":"cn-zhangjiakou","quota":20,"usage":0},{"networkZone":"ALITest","region":"cn-hangzhou","quota":10,"usage":0}]`
	for _, test := range []struct {
		name           string
		before, after  string
		errorCode      string
		allReject      bool
		wantHosts      []string
		wantQuotaReads int
		wantCapacity   bool
	}{
		{name: "default full uses new regional allocation", before: newFree, after: newFree, wantHosts: []string{hz}, wantQuotaReads: 1},
		{name: "regional contention uses another allocation", before: bothFree, after: bothFree, errorCode: "QUOTA_EXCEEDED", wantHosts: []string{zjk, hz}, wantQuotaReads: 1},
		{name: "new region becomes visible during create", before: oldFree, after: newFree, errorCode: "QUOTA_EXCEEDED", wantHosts: []string{zjk, hz}, wantQuotaReads: 2},
		{name: "same region quota increase becomes visible", before: oldFree, after: `[{"region":"cn-zhangjiakou","quota":2,"usage":1}]`, errorCode: "QUOTA_EXCEEDED", wantHosts: []string{zjk, zjk}, wantQuotaReads: 2},
		{name: "stale free counts keep capacity errors queued", before: bothFree, after: bothFree, errorCode: "QUOTA_EXCEEDED", allReject: true, wantHosts: []string{zjk, hz, zjk, hz}, wantQuotaReads: 2, wantCapacity: true},
		{name: "real forbidden is not quota contention", before: bothFree, after: bothFree, errorCode: "FORBIDDEN", wantHosts: []string{zjk}, wantQuotaReads: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx := context.Background()
			pool := newTaskClaimRacePool(t)
			queries := db.New(pool)
			taskID, _, _ := dispatchedCommentTaskFixture(t, ctx, pool)
			task, err := queries.GetAgentTask(ctx, util.MustParseUUID(taskID))
			if err != nil {
				t.Fatal(err)
			}
			box, err := secretbox.New(bytes.Repeat([]byte{0x61}, secretbox.KeySize))
			if err != nil {
				t.Fatal(err)
			}
			sealed, err := box.Seal([]byte(testASBAPIKey))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := queries.UpsertASBRuntimeCredential(ctx, db.UpsertASBRuntimeCredentialParams{
				RuntimeID: task.RuntimeID, ApiKeyEncrypted: sealed, ApiKeyHint: "test",
			}); err != nil {
				t.Fatal(err)
			}
			var hosts []string
			quotaReads, inventoryReads := 0, 0
			client := newASBRegionalTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes/quotas":
					quotaReads++
					if quotaReads == 1 {
						io.WriteString(w, test.before)
					} else {
						io.WriteString(w, test.after)
					}
				case r.Method == http.MethodPost && r.URL.Path == "/v1/sandboxes":
					hosts = append(hosts, r.URL.Host)
					if test.errorCode != "" && (test.allReject || len(hosts) == 1) {
						w.WriteHeader(http.StatusForbidden)
						io.WriteString(w, `{"code":"`+test.errorCode+`","message":"rejected"}`)
					} else {
						w.WriteHeader(http.StatusCreated)
						io.WriteString(w, `{"id":"regional-sandbox","status":{"state":"Pending"}}`)
					}
				case r.Method == http.MethodGet && r.URL.Path == "/v1/sandboxes" && test.wantCapacity:
					inventoryReads++
					io.WriteString(w, `{"items":[]}`)
				default:
					t.Errorf("unexpected upstream request %s %s", r.Method, r.URL)
					w.WriteHeader(http.StatusBadRequest)
				}
			})
			credentials := &ASBRuntimeClientProvider{Store: queries, Secrets: box}
			conn, err := pool.Acquire(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer conn.Release()
			sandbox, err := createASBSandboxWithCapacityOnConnection(ctx, db.New(conn), credentials, client,
				task.RuntimeID, pgtype.UUID{}, conn, ASBCreateSandboxInput{
					ImageURI: "registry.example/runtime:region-test", TimeoutSeconds: 60,
					ResourceCPU: "1", ResourceMemory: "1Gi", Entrypoint: []string{"sleep", "infinity"},
				})
			if errors.Is(err, ErrASBCapacityUnavailable) != test.wantCapacity {
				t.Fatalf("capacity classification = %v, want capacity=%v", err, test.wantCapacity)
			}
			if test.errorCode == "FORBIDDEN" {
				var upstream *ASBHTTPError
				if !errors.As(err, &upstream) || upstream.ErrorCode != "FORBIDDEN" {
					t.Fatalf("forbidden error lost: %v", err)
				}
			} else if !test.wantCapacity && (err != nil || sandbox == nil || sandbox.ID != "regional-sandbox") {
				t.Fatalf("create result: sandbox=%v err=%v", sandbox, err)
			}
			if !reflect.DeepEqual(hosts, test.wantHosts) || quotaReads != test.wantQuotaReads {
				t.Fatalf("hosts=%v quota reads=%d, want hosts=%v reads=%d", hosts, quotaReads, test.wantHosts, test.wantQuotaReads)
			}
			if !test.wantCapacity && inventoryReads != 0 {
				t.Fatal("reclaimed despite a usable allocation or a non-capacity failure")
			}
			if client.baseURL.Host != "sandbox.aone.alibaba-inc.com" {
				t.Fatal("regional create mutated the shared client's routing")
			}
		})
	}
}

func TestASBCapacityRegionalEndpointPreservesConnection(t *testing.T) {
	for _, test := range []struct{ base, region, want string }{
		{"https://sandbox.aone.alibaba-inc.com", "cn-hangzhou", "https://sandbox-cn-hangzhou.aone.alibaba-inc.com/v1"},
		{"https://pre-sandbox.aone.alibaba-inc.com/proxy", "ap-southeast-1", "https://pre-sandbox-ap-southeast-1.aone.alibaba-inc.com/proxy/v1"},
		{"http://sandbox-cn-zhangjiakou.aone.alibaba-inc.com:8080", "cn-hangzhou", "http://sandbox-cn-hangzhou.aone.alibaba-inc.com:8080/v1"},
		{"https://custom-gateway.example/proxy", "cn-hangzhou", "https://custom-gateway.example/proxy/v1"},
	} {
		t.Run(test.base, func(t *testing.T) {
			client, err := NewASBClient(ASBClientConfig{BaseURL: test.base, APIKey: testASBAPIKey})
			if err != nil {
				t.Fatal(err)
			}
			regional, err := client.forCreateRegion(test.region)
			if err != nil || regional.baseURL.String() != test.want {
				t.Fatalf("regional client=%v err=%v, want %s", regional, err, test.want)
			}
			if regional.capacityLockKey != client.capacityLockKey || regional.lifecycleClient != client.lifecycleClient || regional.apiKey != client.apiKey {
				t.Fatal("regional create changed tenant identity or HTTP transport")
			}
		})
	}
	client := newASBRegionalTestClient(t, nil)
	if _, err := client.forCreateRegion("cn-hangzhou.evil.example"); err == nil {
		t.Fatal("accepted a quota region that can alter the service domain")
	}
}
