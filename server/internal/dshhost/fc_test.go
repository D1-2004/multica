package dshhost

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func fakeFC(t *testing.T, handler http.HandlerFunc) *FCProvider {
	t.Helper()
	s := httptest.NewServer(handler)
	t.Cleanup(s.Close)
	p, err := NewFCProvider(FCConfig{s.URL, "test-secret", "vpc-test", "sg-test", []string{"vsw-test"}, 600})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFCDestroyConfirmsAbsenceIndependentOfCallerCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p := fakeFC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "DELETE" {
			w.WriteHeader(202)
			return
		}
		cancel()
		w.WriteHeader(404)
	})
	if err := p.DestroyAndConfirmAbsent(ctx, "sandbox-1"); err != nil {
		t.Fatal(err)
	}
}

func TestFCDeleteRequiresAuthoritativeAbsence(t *testing.T) {
	for _, status := range []int{200, 202, 403, 500, 404} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			p := fakeFC(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method == "DELETE" {
					w.WriteHeader(202)
					return
				}
				w.WriteHeader(status)
			})
			err := p.DestroyAndConfirmAbsent(context.Background(), "sandbox-1")
			if (err == nil) != (status == 404) {
				t.Fatalf("GET status=%d err=%v", status, err)
			}
		})
	}
}

func TestFCTransportRejectsRedirectAndDoesNotLeakBody(t *testing.T) {
	called := false
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	defer destination.Close()
	p := fakeFC(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", destination.URL)
		w.WriteHeader(307)
		_, _ = w.Write([]byte("test-secret"))
	})
	err := p.Healthy(context.Background(), "sandbox-1")
	if err == nil || called || strings.Contains(err.Error(), "test-secret") {
		t.Fatalf("called=%v err=%v", called, err)
	}
}

func TestFCCreatePayloadAndAmbiguousFailureAreNotRetried(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	calls := 0
	p := fakeFC(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		var body struct {
			Metadata   map[string]string `json:"metadata"`
			Mounts     []volumeMount     `json:"volumeMounts"`
			AutoPause  bool              `json:"autoPause"`
			AutoResume json.RawMessage   `json:"autoResume"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if len(body.Mounts) != 1 || body.Mounts[0] != (volumeMount{h.VolumeName, MountPath}) || body.AutoPause {
			t.Errorf("invalid mount/lifecycle: %+v", body)
		}
		if len(body.AutoResume) != 0 {
			t.Error("kill-on-timeout create must omit autoResume")
		}
		if body.Metadata["multica.dsh.intent"] == "" || body.Metadata["fc.sandbox.auth.role"] != h.RoleARN || body.Metadata["fc.sandbox.network.vpc"] == "" {
			t.Error("missing persisted identity or mount authority")
		}
		if r.Header.Get("X-API-KEY") != "test-secret" {
			t.Error("missing auth")
		}
		w.WriteHeader(503)
		_, _ = w.Write([]byte("test-secret"))
	})
	m := Manager{a, p}
	for range 2 {
		if _, err := m.Ensure(context.Background(), h.Key, "template-1"); !errors.Is(err, ErrPending) {
			t.Fatal(err)
		}
	}
	if calls != 1 {
		t.Fatalf("create retried %d times", calls)
	}
}

func TestFCReconcileRejectsDuplicateIntentAcrossPages(t *testing.T) {
	a, _ := stores(t)
	h := bind(t, a)
	// Seed a durable intent through the same manager path, then lose its reply.
	m := Manager{a, &cloud{createErr: errors.New("lost")}}
	_, _ = m.Ensure(context.Background(), h.Key, "template-1")
	h, err := a.Get(context.Background(), h.Key)
	if err != nil {
		t.Fatal(err)
	}
	info := sandboxInfo{ID: "sandbox-1", Template: "fc-display-alias", State: "running", Metadata: identity(h), Mounts: []volumeMount{{h.VolumeName, MountPath}}}
	if !matches(info, h) {
		t.Fatal("FC alias or consumed reserved metadata prevented identity verification")
	}
	valid := fakeFC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v2/sandboxes" {
			_ = json.NewEncoder(w).Encode([]sandboxInfo{info})
		} else {
			_ = json.NewEncoder(w).Encode(info)
		}
	})
	if id, err := valid.FindCreated(context.Background(), h); err != nil || id != info.ID {
		t.Fatalf("FC alias reconciliation id=%s err=%v", id, err)
	}
	p := fakeFC(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("nextToken") == "" {
			w.Header().Set("X-Next-Token", "page-2")
		}
		_ = json.NewEncoder(w).Encode([]sandboxInfo{info})
	})
	if _, err := p.FindCreated(context.Background(), h); err == nil {
		t.Fatal("adopted ambiguous duplicate intent")
	}
	info.Metadata["multica.dsh.agent"] = "different-employee"
	if matches(info, h) {
		t.Fatal("adopted another employee's sandbox")
	}
}

type loseCreateResponse struct {
	Provider
	CreatedID   string
	CreateError error
}

func (p *loseCreateResponse) Create(ctx context.Context, h Host) (string, error) {
	id, err := p.Provider.Create(ctx, h)
	p.CreatedID, p.CreateError = id, err
	if err != nil {
		return "", err
	}
	return "", errors.New("test intentionally discards successful create response")
}

// Explicit opt-in only. Credentials are read from process memory and never
// included in logs. Storage must be an authorized isolated probe volume.
func TestFCLiveCreateReconcileRetire(t *testing.T) {
	if os.Getenv("DSH_HOST_LIVE_TEST") != "1" {
		t.Skip("explicit DSH_HOST_LIVE_TEST=1 required")
	}
	a, b := stores(t)
	p, err := NewFCProvider(FCConfig{
		APIURL: os.Getenv("E2B_API_URL"), APIKey: os.Getenv("E2B_API_KEY"),
		VPCID: os.Getenv("DSH_TEST_VPC_ID"), SecurityGroupID: os.Getenv("DSH_TEST_SECURITY_GROUP_ID"),
		VSwitchIDs: []string{os.Getenv("DSH_TEST_VSWITCH_ID")}, TimeoutSeconds: 600,
	})
	if err != nil {
		t.Fatal(err)
	}
	seed, err := a.BindStorage(context.Background(), Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, Storage{
		FileSystemID: os.Getenv("DSH_TEST_FILE_SYSTEM_ID"), SpaceID: os.Getenv("DSH_TEST_SPACE_ID"),
		VolumeName: os.Getenv("DSH_TEST_VOLUME"), AccessPointARN: os.Getenv("DSH_TEST_ACCESS_POINT_ARN"), RoleARN: os.Getenv("DSH_TEST_ROLE_ARN"),
		VPCID: os.Getenv("DSH_TEST_VPC_ID"), SecurityGroupID: os.Getenv("DSH_TEST_SECURITY_GROUP_ID"), VSwitchIDs: []string{os.Getenv("DSH_TEST_VSWITCH_ID")},
	})
	if err != nil {
		t.Fatal(err)
	}
	m := Manager{a, p}
	ctx := context.Background()
	lost := &loseCreateResponse{Provider: p}
	t.Cleanup(func() {
		h, err := a.Get(ctx, seed.Key)
		if err != nil {
			t.Error(err)
			return
		}
		if h.State == "creating" {
			h, err = m.ReconcileCreate(ctx, seed.Key)
			if err != nil {
				if lost.CreatedID != "" {
					if cleanupErr := p.DestroyAndConfirmAbsent(ctx, lost.CreatedID); cleanupErr != nil {
						t.Error("direct cleanup unconfirmed", lost.CreatedID, cleanupErr)
					} else {
						t.Log("direct cleanup confirmed absent", lost.CreatedID)
					}
				}
				t.Error("cleanup requires create reconciliation", err)
				return
			}
		}
		if h.State == "running" || h.State == "retiring" {
			if err := m.Retire(ctx, seed.Key, h.Generation); err != nil {
				t.Error("cleanup unconfirmed", h.SandboxID, err)
			} else {
				t.Log("cleanup confirmed absent", h.SandboxID)
			}
		}
	})
	if _, err := (Manager{a, lost}).Ensure(ctx, seed.Key, os.Getenv("DSH_TEST_TEMPLATE")); !errors.Is(err, ErrPending) {
		t.Fatal(err)
	}
	t.Logf("create observation sandbox=%s error=%v", lost.CreatedID, lost.CreateError)
	var h Host
	// FC listing can lag the direct GET. Keep the same durable intent and
	// retry only reads; an empty listing must never start another sandbox.
	for attempt := 0; attempt < 30; attempt++ {
		h, err = (Manager{b, p}).ReconcileCreate(ctx, seed.Key)
		if err == nil {
			break
		}
		time.Sleep(time.Second)
	}
	if err != nil {
		pending, _ := a.Get(ctx, seed.Key)
		_, findErr := p.FindCreated(ctx, pending)
		t.Logf("provider reconciliation error=%v", findErr)
		all, _, _, listErr := p.request(ctx, http.MethodGet, "/v2/sandboxes?limit=100", nil)
		var listed []sandboxInfo
		if json.Unmarshal(all, &listed) == nil {
			for _, v := range listed {
				if v.ID == lost.CreatedID {
					t.Logf("unfiltered listing match=%t metadata_keys=%d mounts=%+v", matches(v, pending), len(v.Metadata), v.Mounts)
				}
			}
		} else {
			t.Log("unfiltered listing failed", listErr)
		}
		data, _, status, inspectErr := p.request(ctx, http.MethodGet, "/sandboxes/"+lost.CreatedID, nil)
		var info sandboxInfo
		if json.Unmarshal(data, &info) == nil {
			t.Logf("direct inspection status=%d sandbox=%s template=%s state=%s mounts=%+v", status, info.ID, info.Template, info.State, info.Mounts)
			t.Logf("direct identity match=%t", matches(info, pending))
		} else {
			t.Logf("direct inspection status=%d error=%v", status, inspectErr)
		}
		t.Fatal(err)
	}
	t.Logf("reconciled sandbox=%s generation=%d intent=%s", h.SandboxID, h.Generation, h.CreateIntent)
	warm, err := m.Ensure(ctx, seed.Key, h.TemplateID)
	if err != nil || warm.SandboxID != h.SandboxID {
		t.Fatal("warm reuse failed", err)
	}
	if err := m.Retire(ctx, seed.Key, h.Generation); err != nil {
		t.Fatal(err)
	}
	t.Log("old sandbox confirmed absent", h.SandboxID)
	next, err := m.Ensure(ctx, seed.Key, h.TemplateID)
	if err != nil {
		t.Fatal(err)
	}
	if next.Generation != h.Generation+1 || next.SandboxID == h.SandboxID {
		t.Fatal("generation did not advance")
	}
	t.Logf("replacement sandbox=%s generation=%d", next.SandboxID, next.Generation)
}
