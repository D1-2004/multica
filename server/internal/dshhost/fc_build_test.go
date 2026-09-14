package dshhost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
)

func TestFCBuildIsolatedCreateReconciliationAndFencedCleanup(t *testing.T) {
	identity := FCBuildIdentity{WorkspaceID: uuid.New(), BuildID: uuid.New(), Intent: uuid.New(), BuildKey: strings.Repeat("a", 64), TemplateID: "immutable-template"}
	info := sandboxInfo{ID: "sbx-build", Template: "display-alias", State: "running", Metadata: identity.labels()}
	posts, deletes := 0, 0
	absent, duplicate := false, false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != "fixture-key" {
			t.Error("missing provider authentication")
		}
		switch {
		case r.Method == "POST":
			posts++
			var body map[string]json.RawMessage
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if len(body) != 4 || body["volumeMounts"] != nil || body["envs"] != nil {
				t.Error("build sandbox inherited a Home mount or environment")
			}
			var metadata map[string]string
			if err := json.Unmarshal(body["metadata"], &metadata); err != nil {
				t.Fatal(err)
			}
			if len(metadata) != 5 || metadata["fc.sandbox.auth.role"] != "" || metadata["fc.sandbox.network.vpc"] != "" {
				t.Error("build sandbox inherited employee network or role")
			}
			_ = json.NewEncoder(w).Encode(info)
		case strings.HasPrefix(r.URL.Path, "/v2/sandboxes"):
			items := []sandboxInfo{info}
			if duplicate {
				items = append(items, info)
			}
			_ = json.NewEncoder(w).Encode(items)
		case r.Method == "DELETE":
			deletes++
			w.WriteHeader(http.StatusAccepted)
		case absent:
			w.WriteHeader(http.StatusNotFound)
		default:
			_ = json.NewEncoder(w).Encode(info)
		}
	}))
	defer server.Close()
	provider, err := NewFCBuildProvider(FCConfig{APIURL: server.URL, APIKey: "fixture-key", TimeoutSeconds: 1800})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if id, err := provider.Create(ctx, identity); err != nil || id != info.ID || posts != 1 {
		t.Fatal("isolated create failed", err)
	}
	if id, err := provider.FindCreated(ctx, identity); err != nil || id != info.ID || posts != 1 {
		t.Fatal("reconciliation changed create count", err)
	}
	duplicate = true
	if _, err := provider.FindCreated(ctx, identity); err == nil {
		t.Fatal("ambiguous intent accepted")
	}
	duplicate = false
	info.Mounts = []volumeMount{{Name: "employee-home", Path: MountPath}}
	if err := provider.DestroyAndConfirmAbsent(ctx, identity, info.ID); err == nil || deletes != 0 {
		t.Fatal("cleanup accepted a mounted employee sandbox")
	}
	info.Mounts = nil
	info.Metadata["multica.dsh.build.workspace"] = uuid.NewString()
	if _, _, err := provider.Inspect(ctx, identity, info.ID); err == nil {
		t.Fatal("cross-workspace sandbox accepted")
	}
	info.Metadata = identity.labels()
	if err := provider.DestroyAndConfirmAbsent(ctx, identity, info.ID); err == nil || deletes != 1 {
		t.Fatal("DELETE accepted without confirmed absence")
	}
	absent = true
	if err := provider.DestroyAndConfirmAbsent(ctx, identity, info.ID); err != nil || deletes != 1 {
		t.Fatal("already absent sandbox was not idempotent", err)
	}
}
