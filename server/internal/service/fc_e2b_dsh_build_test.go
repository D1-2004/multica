package service

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshprofile"
)

type buildGrantFixture struct {
	getKey, putKey string
}

func (g *buildGrantFixture) PresignGet(_ context.Context, key string, ttl time.Duration) (string, error) {
	g.getKey = key
	if ttl > time.Hour {
		return "", errors.New("unbounded grant")
	}
	return "https://objects.example/source?signature=read-canary", nil
}
func (g *buildGrantFixture) PresignPut(_ context.Context, key, contentType string, ttl time.Duration) (string, error) {
	g.putKey = key
	if contentType != "application/gzip" || ttl > time.Hour {
		return "", errors.New("invalid grant")
	}
	return "https://objects.example/target?signature=write-canary", nil
}

type buildControlRunner struct {
	t       *testing.T
	corrupt bool
	err     bool
	last    map[string]any
}

func (r *buildControlRunner) Run(_ context.Context, name string, args, env []string) (string, error) {
	r.t.Helper()
	requireFCE2BSDKAccepts(args)
	if name != "/fixture/e2b" || len(env) != 3 {
		r.t.Fatal("unexpected CLI or ambient worker credentials")
	}
	var raw string
	for _, arg := range args {
		if strings.HasPrefix(arg, "MULTICA_DSH_BUILD_REQUEST=") {
			raw = strings.TrimPrefix(arg, "MULTICA_DSH_BUILD_REQUEST=")
		}
	}
	r.last = nil // Each CLI response is a new request, not a JSON merge.
	if err := json.Unmarshal([]byte(raw), &r.last); err != nil {
		r.t.Fatal(err)
	}
	if args[len(args)-4] != "/opt/task-python/bin/python3" || args[len(args)-1] != "/opt/multica-dsh/multica_dsh_build_worker.py" {
		r.t.Fatal("arbitrary worker command selected")
	}
	if r.err {
		return raw, errors.New(raw)
	}
	frozen := map[string]any{"identity": r.last["identity"], "plugin": r.last["plugin"]}
	encoded, _ := json.Marshal(frozen)
	digest := sha256.Sum256(encoded)
	value := hex.EncodeToString(digest[:])
	if r.corrupt {
		value = strings.Repeat("0", 64)
	}
	result, _ := json.Marshal(map[string]string{"state": "running", "identity_digest": value})
	return string(result), nil
}

func TestDSHBuildControlBindsIdentityAndScopesTransientGrants(t *testing.T) {
	grants := &buildGrantFixture{}
	runner := &buildControlRunner{t: t}
	driver := &dshBuildDriver{launcher: &FCE2BLauncher{Config: FCE2BConfig{CLIPath: "/fixture/e2b"}, Runner: runner}, get: grants, put: grants}
	job := dshprofile.BuildJob{WorkspaceID: uuid.New(), BuildID: uuid.New(), Intent: uuid.New(), BuildKey: strings.Repeat("b", 64),
		TemplateID: "immutable-template", SandboxID: "sbx-build",
		Plugin: dshprofile.SourcePlugin{PackageName: "fixture-plugin", Version: "1.0.0", Integrity: "sha256-" + strings.Repeat("a", 64), ArtifactKey: "workspace/source.tgz", Config: map[string]any{"credential": "employee-secret-canary"}}}
	job.Plugin.ArtifactKey = "dsh-plugins/" + job.WorkspaceID.String() + "/fixture/source.tgz"
	job.ArtifactKey = "dsh-plugin-builds/" + job.WorkspaceID.String() + "/" + job.BuildKey + "/" + job.Intent.String() + "/tree.tgz"
	if _, err := driver.control(context.Background(), job, "start", true); err != nil {
		t.Fatal(err)
	}
	if grants.getKey != job.Plugin.ArtifactKey || grants.putKey != job.ArtifactKey {
		t.Fatal("object grant escaped its saved source/target")
	}
	raw, _ := json.Marshal(runner.last)
	if strings.Contains(string(raw), "employee-secret-canary") || !strings.Contains(string(raw), "write-canary") {
		t.Fatal("worker credential boundary changed")
	}
	if _, err := driver.control(context.Background(), job, "status", false); err != nil {
		t.Fatal(err)
	}
	if runner.last["source_url"] != nil || runner.last["upload_url"] != nil {
		t.Fatal("status call renewed upload capability")
	}
	runner.corrupt = true
	if _, err := driver.control(context.Background(), job, "start", true); err == nil {
		t.Fatal("unrelated worker identity accepted")
	}
	runner.err = true
	if _, err := driver.control(context.Background(), job, "start", true); err == nil || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), "signature") {
		t.Fatal("CLI error leaked transfer credentials")
	}
}

func TestDSHBuildCapabilityDoesNotChangeOtherProviders(t *testing.T) {
	template := FCE2BTemplate{Capabilities: []string{DSHPluginBuildCapability}}
	for _, provider := range FCE2BSupportedProviders {
		got := FCE2BTemplateCapabilities(provider, template)
		found := false
		for _, value := range got {
			found = found || value == DSHPluginBuildCapability
		}
		if found != (provider == "dsh") {
			t.Fatal("DSH build capability exposed to another provider")
		}
	}
}

func TestDSHBuildPreflightRequiresImmutableTemplateID(t *testing.T) {
	template := FCE2BTemplate{ID: "immutable-template-id", Template: "display-alias", Status: "ready",
		Providers: []string{"dsh"}}
	if !dshBuildTemplateReady([]FCE2BTemplate{template}, template.ID) {
		t.Fatal("runtime's immutable template ID was not admitted")
	}
	if dshBuildTemplateReady([]FCE2BTemplate{template}, template.Template) {
		t.Fatal("mutable display alias admitted as build identity")
	}
	for _, change := range []func(*FCE2BTemplate){
		func(v *FCE2BTemplate) { v.Status = "building" },
		func(v *FCE2BTemplate) { v.Providers = []string{"pi"} },
	} {
		changed := template
		change(&changed)
		if dshBuildTemplateReady([]FCE2BTemplate{changed}, template.ID) {
			t.Fatal("unready or unsupported build template admitted")
		}
	}
}
