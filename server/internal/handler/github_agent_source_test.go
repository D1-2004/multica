package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/coordinatorcontract"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestGitAgentInstanceProfileUsesManifestDefaultsAndRequestOverrides(t *testing.T) {
	bundle := agentsource.Bundle{Manifest: agentsource.Manifest{Metadata: agentsource.ManifestMetadata{
		Name: "Manifest name", Description: "Manifest description",
	}}}

	name, description, err := agentPackageInstanceProfile(CreateAgentPackageRequest{}, nil, bundle)
	if err != nil || name != "Manifest name" || description != "Manifest description" {
		t.Fatalf("defaults = (%q, %q, %v)", name, description, err)
	}

	name, description, err = agentPackageInstanceProfile(CreateAgentPackageRequest{
		CreateAgentRequest: CreateAgentRequest{Name: " Instance name ", Description: ""},
	}, map[string]json.RawMessage{"description": json.RawMessage(`""`)}, bundle)
	if err != nil || name != "Instance name" || description != "" {
		t.Fatalf("overrides = (%q, %q, %v)", name, description, err)
	}

	_, _, err = agentPackageInstanceProfile(CreateAgentPackageRequest{
		CreateAgentRequest: CreateAgentRequest{Description: strings.Repeat("界", maxAgentDescriptionLength+1)},
	}, map[string]json.RawMessage{"description": json.RawMessage(`"long"`)}, bundle)
	if err == nil {
		t.Fatal("expected overlong instance description to be rejected")
	}
}

func TestGitAgentSourceSnapshotUpdatePreservesLocalProfile(t *testing.T) {
	params := gitAgentSourceSnapshotUpdate(pgtype.UUID{Valid: true}, agentsource.Bundle{
		Instructions: "repository instructions v2",
		Manifest: agentsource.Manifest{
			Metadata: agentsource.ManifestMetadata{Name: "repository name v2", Description: "repository description v2"},
		},
	})

	if params.Name.Valid || params.Description.Valid {
		t.Fatalf("sync must not update local profile: name=%#v description=%#v", params.Name, params.Description)
	}
	if !params.Instructions.Valid || params.Instructions.String != "repository instructions v2" {
		t.Fatalf("sync instructions = %#v", params.Instructions)
	}
}

func TestSourceManagedSkillNameIsIsolatedPerAgentSource(t *testing.T) {
	first := sourceManagedSkillName("repository-audit", pgtype.UUID{Bytes: [16]byte{0x12, 0x34, 0x56, 0x78, 0x90}, Valid: true})
	second := sourceManagedSkillName("repository-audit", pgtype.UUID{Bytes: [16]byte{0xab, 0xcd, 0xef, 0x01, 0x23}, Valid: true})
	if first == second || !strings.HasPrefix(first, "repository-audit--") || !strings.HasPrefix(second, "repository-audit--") {
		t.Fatalf("source-scoped names = %q, %q", first, second)
	}
}

func TestAgentSourceResponseKeepsPlatformGitSourceReadyWithoutInstallation(t *testing.T) {
	response := agentSourceToResponse(db.AgentSource{
		SourceType:       "git",
		ManagedSourceKey: pgtype.Text{String: "fde-agent", Valid: true},
		RepoOwner:        "keeperqaq",
		RepoName:         "fde-agent",
		SyncStatus:       "ready",
	})
	if response.SyncStatus != "ready" {
		t.Fatalf("platform Git source status = %q, want ready", response.SyncStatus)
	}
	if response.Connected || response.ConnectionID != nil {
		t.Fatalf("platform Git source must not invent a GitHub installation: %#v", response)
	}

	disconnected := agentSourceToResponse(db.AgentSource{
		SourceType: "git", RepoOwner: "acme", RepoName: "agent", SyncStatus: "ready",
	})
	if disconnected.SyncStatus != "disconnected" {
		t.Fatalf("ordinary source without installation status = %q", disconnected.SyncStatus)
	}
}

func TestWriteGitHubSourceErrorTreatsDTAProjectFailuresAsUnprocessable(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeGitRepoError(recorder, errors.New(`compile dingtalk-agent.json: agent.definition contains an unsafe path`))

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusUnprocessableEntity, recorder.Body.String())
	}
}

func TestGitAgentSourceSnapshotUpdatesAndClearsCoordinatorContract(t *testing.T) {
	bound, err := coordinatorcontract.Bind(&coordinatorcontract.Contract{Version: 1, Scope: "Route requests"}, "source SOP")
	if err != nil {
		t.Fatal(err)
	}
	params := gitAgentSourceSnapshotUpdate(pgtype.UUID{Valid: true}, agentsource.Bundle{Instructions: "source SOP", CoordinatorContract: bound})
	if _, state := coordinatorcontract.Resolve(params.CoordinatorContract, params.Instructions.String); state != coordinatorcontract.StateLoaded {
		t.Fatalf("sync contract state = %s", state)
	}
	cleared := gitAgentSourceSnapshotUpdate(pgtype.UUID{Valid: true}, agentsource.Bundle{Instructions: "source SOP"})
	if string(cleared.CoordinatorContract) != "null" {
		t.Fatalf("missing source contract must explicitly clear stored contract, got %q", cleared.CoordinatorContract)
	}
	preview := GitAgentPreviewResponse{CoordinatorContract: bound}
	raw, err := json.Marshal(preview)
	if err != nil || !strings.Contains(string(raw), `"coordinator_contract":{"version":1`) {
		t.Fatalf("preview lost contract: %s, %v", raw, err)
	}
}

func TestWriteGitHubSourceErrorTreatsBoundContractBudgetAsUnprocessable(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeGitRepoError(recorder, errors.New("spec.coordinator_contract: coordinator_contract must be 1600 characters or fewer including JSON fields and source hash"))
	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
