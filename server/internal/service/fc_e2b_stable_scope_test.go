package service

import (
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestStableProviderScopeKeepsArtifactCapabilitiesAndTargetsSeparate(t *testing.T) {
	providers := []string{"hermes", "dsh", "codex"}
	scoped, err := stableScopedProviders(providers, "dsh", nil)
	if err != nil || !reflect.DeepEqual(scoped, []string{"dsh"}) || !reflect.DeepEqual(providers, []string{"hermes", "dsh", "codex"}) {
		t.Fatal("provider scope changed artifact capabilities", scoped, err)
	}
	shared, err := stableScopedProviders(providers, "", []string{"dsh"})
	if err != nil || !reflect.DeepEqual(shared, []string{"hermes", "codex"}) {
		t.Fatal("shared release includes an independent channel", shared, err)
	}
	if _, err := stableScopedProviders(providers, "pi", nil); err == nil {
		t.Fatal("unsupported provider admitted")
	}
	for _, item := range []struct {
		backend SandboxBackendKind
		scope   string
	}{
		{SandboxBackendASB, "dsh"}, {SandboxBackendAliyunFC, "DSH"}, {SandboxBackendAliyunFC, "dsh,hermes"}, {SandboxBackendAliyunFC, "unknown"},
	} {
		if ValidateStableProviderScope(item.backend, item.scope) == nil {
			t.Fatalf("accepted invalid scope %+v", item)
		}
	}
	if ValidateStableProviderScope(SandboxBackendAliyunFC, "dsh") != nil || ValidateStableProviderScope(SandboxBackendASB, "") != nil {
		t.Fatal("valid scope rejected")
	}
}

func TestStableProviderScopeFiltersRolloutAndLateJoinGates(t *testing.T) {
	manifest := map[string]any{"providers": []string{"hermes", "dsh", "codex"}, "target_providers": []string{"dsh"}}
	id := func() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
	targets := []stableRuntimeTarget{{RuntimeID: id(), WorkspaceID: id(), Provider: "hermes"}, {RuntimeID: id(), WorkspaceID: id(), Provider: "dsh"}, {RuntimeID: id(), WorkspaceID: id(), Provider: "codex"}}
	selected := stableTargetsForManifest(uuid.NewString(), targets, manifest)
	if len(selected) != 1 || selected[0].RuntimeID != targets[1].RuntimeID {
		t.Fatal("rollout scope widened", selected)
	}
	release := FCE2BStableRelease{SandboxBackend: string(SandboxBackendAliyunFC), ProviderScope: "dsh", Manifest: manifest}
	if !reflect.DeepEqual(releaseTemplate(release).Providers, []string{"hermes", "dsh", "codex"}) {
		t.Fatal("release template lost the artifact's real capabilities")
	}
	for _, provider := range []string{"hermes", "dsh", "codex"} {
		runtime := db.AgentRuntime{Provider: provider, RuntimeMode: "cloud", Metadata: []byte(`{"kind":"fc-e2b","template_channel":"stable","template_id":"old"}`)}
		missing := stableRuntimeMissingForRelease(runtime, "", release, SandboxBackendAliyunFC)
		if missing != (provider == "dsh") {
			t.Fatalf("late join gate provider=%s missing=%v", provider, missing)
		}
		if stableRuntimeMissingForRelease(runtime, "updated", release, SandboxBackendAliyunFC) {
			t.Fatal("updated runtime still missing")
		}
	}
	manifest["target_providers"] = []string{}
	if got := stableTargetsForManifest(uuid.NewString(), targets, manifest); len(got) != 0 {
		t.Fatal("empty explicit scope fell back to all capabilities")
	}
	delete(manifest, "target_providers")
	if got := stableTargetsForManifest(uuid.NewString(), targets, manifest); len(got) != 3 {
		t.Fatal("historical shared release changed scope")
	}
}

func TestStableProviderScopeHasDistinctRetryIdentity(t *testing.T) {
	input := CreateFCE2BStableReleaseInput{SandboxBackend: SandboxBackendAliyunFC, ArtifactRef: "template", TemplateID: "template"}
	shared := stableReleaseFingerprint(input)
	input.ProviderScope = "dsh"
	scoped := stableReleaseFingerprint(input)
	if scoped == shared || scoped != stableReleaseFingerprint(input) {
		t.Fatal("scoped release retry identity is not stable and distinct")
	}
	if stableProviderChannel("") != "stable" || stableProviderChannel("dsh") != "stable:dsh" {
		t.Fatal("incorrect channel names")
	}
}
