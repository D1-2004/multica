package service

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dshhost"
	"github.com/multica-ai/multica/server/internal/dshprofile"
)

type deliveryRow func(...any) error

func (r deliveryRow) Scan(dest ...any) error { return r(dest...) }

type deliveryDB struct {
	workspace uuid.UUID
	build     dshprofile.Build
	intent    uuid.UUID
	artifact  dshprofile.BuildArtifact
	key       string
}

func (*deliveryDB) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (d *deliveryDB) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	return deliveryRow(func(dest ...any) error {
		if args[0] != d.workspace || args[1] != d.build.ID || args[2] != d.build.Key || args[3] != "template-1" {
			return pgx.ErrNoRows
		}
		*dest[0].(*string) = d.key
		*dest[1].(*string) = d.artifact.BuildDigest
		*dest[2].(*string) = d.artifact.ArchiveSHA256
		*dest[3].(*int64) = d.artifact.ArchiveSize
		*dest[4].(*string) = d.artifact.RuntimeLockSHA256
		*dest[5].(*uuid.UUID) = d.intent
		return nil
	})
}

type deliveryRunner struct {
	request       string
	corrupt, fail bool
	calls         int
}

func (r *deliveryRunner) Run(_ context.Context, _ string, args, _ []string) (string, error) {
	r.calls++
	for _, arg := range args {
		if strings.HasPrefix(arg, "MULTICA_DSH_ARTIFACT_REQUEST=") {
			r.request = strings.TrimPrefix(arg, "MULTICA_DSH_ARTIFACT_REQUEST=")
		}
	}
	if r.fail {
		return r.request, errors.New(r.request)
	}
	var req struct {
		Receipt dshprofile.BuildArtifact `json:"receipt"`
	}
	if err := json.Unmarshal([]byte(r.request), &req); err != nil {
		return "", err
	}
	if r.corrupt {
		req.Receipt.RuntimeLockSHA256 = strings.Repeat("f", 64)
	}
	result, _ := json.Marshal(req.Receipt)
	return string(result), nil
}

func TestDSHDeliveryScopesArtifactsAndRequiresInstallationReceipt(t *testing.T) {
	host := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, TemplateID: "template-1", SandboxID: "sandbox-1", Generation: 1}
	plugin := dshprofile.SourcePlugin{ID: uuid.New(), Enabled: true, PackageName: "fixture", Version: "1.0.0", Integrity: "sha256-" + strings.Repeat("a", 64), RowID: "fixture", Config: map[string]any{"credential": "employee-secret-canary"}}
	source := dshprofile.Source{TemplateID: host.TemplateID, Plugins: []dshprofile.SourcePlugin{plugin}}
	build := dshprofile.Build{ID: uuid.New(), Key: dshprofile.BuildKey(host.TemplateID, plugin), State: "ready", Digest: strings.Repeat("b", 64)}
	intent := uuid.New()
	build.ArtifactKey = "dsh-plugin-builds/" + host.WorkspaceID.String() + "/" + build.Key + "/" + intent.String() + "/tree.tgz"
	raw, digest, err := dshprofile.Resolve(host.Key, 1, source, map[string]dshprofile.Build{build.Key: build})
	if err != nil {
		t.Fatal(err)
	}
	revision := dshprofile.Revision{ID: 1, TemplateID: host.TemplateID, Descriptor: raw, Digest: digest, Builds: []dshprofile.Build{build}}
	database := &deliveryDB{workspace: host.WorkspaceID, build: build, intent: intent, key: build.ArtifactKey, artifact: dshprofile.BuildArtifact{BuildDigest: build.Digest, ArchiveSHA256: strings.Repeat("c", 64), ArchiveSize: 123, RuntimeLockSHA256: strings.Repeat("d", 64)}}
	store := dshprofile.Store{DB: database}
	runner, grants := &deliveryRunner{}, &buildGrantFixture{}
	launcher := &FCE2BLauncher{Runner: runner, DSHArtifactSigner: grants}
	if err := launcher.deliverDSHProfile(context.Background(), store, host, revision); err != nil {
		t.Fatal(err)
	}
	if grants.getKey != build.ArtifactKey || strings.Contains(runner.request, "employee-secret-canary") || !strings.Contains(runner.request, "/mnt/multica/plugin-builds") {
		t.Fatal("artifact delivery escaped package-only boundary")
	}
	runner.corrupt = true
	if launcher.deliverDSHProfile(context.Background(), store, host, revision) == nil {
		t.Fatal("wrong installed runtime lock accepted")
	}
	runner.fail = true
	if err := launcher.deliverDSHProfile(context.Background(), store, host, revision); err == nil || strings.Contains(err.Error(), "canary") {
		t.Fatal("unknown delivery receipt or private CLI output accepted")
	}
	before := runner.calls
	database.key = "dsh-plugin-builds/another-workspace/tree.tgz"
	if launcher.deliverDSHProfile(context.Background(), store, host, revision) == nil || runner.calls != before {
		t.Fatal("unbound object reached the employee sandbox")
	}
}

func TestDSHLargeProfileUsesBoundedChunksAndSmallHostEnvironment(t *testing.T) {
	host := dshhost.Host{Key: dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}, TemplateID: "template-1", SandboxID: "sandbox-1", Generation: 1}
	descriptor := dshprofile.Descriptor{Version: 1, WorkspaceID: host.WorkspaceID, AgentID: host.AgentID, Revision: "1",
		Plugins: []dshprofile.Plugin{{PackageName: "fixture", Version: "1.0.0", Integrity: "sha256-" + strings.Repeat("a", 64), BuildDigest: strings.Repeat("b", 64), RowID: "fixture", Config: map[string]any{"large": strings.Repeat("汉", 330000)}}}}
	raw, _ := json.Marshal(descriptor)
	revision := dshprofile.Revision{ID: 1, TemplateID: host.TemplateID, Descriptor: string(raw), Digest: fmt.Sprintf("%x", sha256.Sum256(raw))}
	runner := dshHomeRunner{profiles: &sync.Map{}}
	l := FCE2BLauncher{Runner: runner}
	if err := l.stageDSHProfile(context.Background(), host, revision); err != nil {
		t.Fatal(err)
	}
	for _, arg := range dshNativeHostEnsureArgs(host, "[]", "https://authority.test", "https://gateway.test", "key", revision) {
		if len(arg) > 1024 || strings.Contains(arg, "汉") {
			t.Fatal("private Profile payload reached the long-lived Host environment")
		}
	}
}
