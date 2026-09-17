package dshprofile

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

func fixtureSource() Source {
	return Source{TemplateID: "immutable-template", Plugins: []SourcePlugin{{ID: uuid.MustParse("99d95361-1895-4b05-b9ed-d4d221b57c09"), Enabled: true, ConfigRevision: 1, PackageName: "@test/employee", Version: "1.0.0-rc.1", Integrity: "sha256-" + strings.Repeat("a", 64), SourceKind: "npm", SourceSpec: "@test/employee@1.0.0-rc.1", RowID: "employee", Config: map[string]any{"credential": "synthetic-private-value", "limit": float64(7)}}}}
}

func TestProfileIdentityAndImmutableBuilds(t *testing.T) {
	source := fixtureSource()
	plugin := source.Plugins[0]
	buildKey := BuildKey(source.TemplateID, plugin)
	builds := map[string]Build{buildKey: {State: "ready", Digest: strings.Repeat("b", 64), ArtifactKey: "fixture/build.tgz"}}
	key := dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}
	raw, digest, err := Resolve(key, math.MaxInt64, source, builds)
	if err != nil {
		t.Fatal(err)
	}
	if hash([]byte(raw)) != digest {
		t.Fatal("digest must cover exact wire bytes")
	}
	var decoded Descriptor
	if err = json.Unmarshal([]byte(raw), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Revision != "9223372036854775807" || decoded.WorkspaceID != key.WorkspaceID || decoded.AgentID != key.AgentID || len(decoded.Plugins) != 1 || decoded.Plugins[0].Config["credential"] != "synthetic-private-value" {
		t.Fatal("descriptor lost exact identity or configuration")
	}
	for _, other := range []dshhost.Key{{WorkspaceID: uuid.New(), AgentID: key.AgentID}, {WorkspaceID: key.WorkspaceID, AgentID: uuid.New()}} {
		_, otherDigest, err := Resolve(other, math.MaxInt64, source, builds)
		if err != nil || otherDigest == digest {
			t.Fatal("different employee reused descriptor")
		}
	}
	for _, build := range []Build{{}, {State: "queued"}, {State: "failed"}, {State: "ready", Digest: strings.Repeat("b", 64)}, {State: "ready", Digest: "not-immutable", ArtifactKey: "object"}} {
		_, _, err := Resolve(key, 1, source, map[string]Build{buildKey: build})
		if !errors.Is(err, ErrPending) {
			t.Fatal("unverified build admitted")
		}
	}
	source.TemplateID = "other-template"
	if _, _, err = Resolve(key, 1, source, builds); !errors.Is(err, ErrPending) {
		t.Fatal("build from another runtime admitted")
	}
	source.Plugins[0].Enabled = false
	builds[BuildKey(source.TemplateID, source.Plugins[0])] = Build{State: "ready", Digest: strings.Repeat("b", 64), ArtifactKey: "object"}
	raw, _, err = Resolve(key, 1, source, builds)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(raw, `"enabled":false`) {
		t.Fatal("disabled plugin must remain installed but explicitly disabled")
	}
}

func TestProfileSourceCanonicalAndConfigSensitive(t *testing.T) {
	source := fixtureSource()
	other := source.Plugins[0]
	other.ID = uuid.New()
	other.PackageName = "z-plugin"
	source.Plugins = append(source.Plugins, other)
	raw, digest, err := EncodeSource(source)
	if err != nil {
		t.Fatal(err)
	}
	source.Plugins[0], source.Plugins[1] = source.Plugins[1], source.Plugins[0]
	reordered, reorderedDigest, err := EncodeSource(source)
	if err != nil || raw != reordered || digest != reorderedDigest {
		t.Fatal("input order changed revision")
	}
	beforeBuild := BuildKey(source.TemplateID, source.Plugins[0])
	source.Plugins[0].Config = map[string]any{"credential": "replacement"}
	source.Plugins[0].ConfigRevision++
	_, changed, err := EncodeSource(source)
	if err != nil || changed == digest {
		t.Fatal("private configuration change reused revision")
	}
	if BuildKey(source.TemplateID, source.Plugins[0]) != beforeBuild {
		t.Fatal("credential rotation forced a package build")
	}
	source.Plugins[0].Integrity = "sha256-" + strings.Repeat("c", 64)
	if BuildKey(source.TemplateID, source.Plugins[0]) == beforeBuild {
		t.Fatal("different package bytes reused build")
	}
	status := Status{State: "applied", DesiredRevision: "3", Current: true, SourceDigest: digest}
	status, err = status.CompareConfiguration(source)
	if err != nil || status.Current || status.State != "configuration_changed" {
		t.Fatal("old receipt shown as current")
	}
	encoded, _ := json.Marshal(status)
	if strings.Contains(string(encoded), digest) || strings.Contains(string(encoded), "replacement") {
		t.Fatal("status disclosed private source")
	}
}

func TestProfileRejectsMalformedSources(t *testing.T) {
	cases := map[string]func(*Source){
		"template":  func(s *Source) { s.TemplateID = "" },
		"id":        func(s *Source) { s.Plugins[0].ID = uuid.Nil },
		"name":      func(s *Source) { s.Plugins[0].PackageName = "../escape" },
		"version":   func(s *Source) { s.Plugins[0].Version = "latest" },
		"integrity": func(s *Source) { s.Plugins[0].Integrity = "short" },
		"config":    func(s *Source) { s.Plugins[0].Config = nil },
		"row":       func(s *Source) { s.Plugins[0].RowID = "" },
		"revision":  func(s *Source) { s.Plugins[0].ConfigRevision = -1 },
		"duplicate": func(s *Source) { s.Plugins = append(s.Plugins, s.Plugins[0]) },
		"oversize":  func(s *Source) { s.Plugins[0].Config["large"] = strings.Repeat("s", 1024*1024) },
		"non-json":  func(s *Source) { s.Plugins[0].Config["nan"] = math.NaN() },
	}
	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			source := fixtureSource()
			change(&source)
			if _, _, err := EncodeSource(source); err == nil {
				t.Fatal("invalid source accepted")
			} else if strings.Contains(err.Error(), "synthetic-private-value") {
				t.Fatal("error disclosed configuration")
			}
		})
	}
}
