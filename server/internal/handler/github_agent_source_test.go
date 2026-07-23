package handler

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
)

func TestGitAgentInstanceProfileUsesManifestDefaultsAndRequestOverrides(t *testing.T) {
	bundle := agentsource.Bundle{Manifest: agentsource.Manifest{Metadata: agentsource.ManifestMetadata{
		Name: "Manifest name", Description: "Manifest description",
	}}}

	name, description, err := gitAgentInstanceProfile(CreateGitHubAgentRequest{}, nil, bundle)
	if err != nil || name != "Manifest name" || description != "Manifest description" {
		t.Fatalf("defaults = (%q, %q, %v)", name, description, err)
	}

	name, description, err = gitAgentInstanceProfile(CreateGitHubAgentRequest{
		CreateAgentRequest: CreateAgentRequest{Name: " Instance name ", Description: ""},
	}, map[string]json.RawMessage{"description": json.RawMessage(`""`)}, bundle)
	if err != nil || name != "Instance name" || description != "" {
		t.Fatalf("overrides = (%q, %q, %v)", name, description, err)
	}

	_, _, err = gitAgentInstanceProfile(CreateGitHubAgentRequest{
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

func TestWriteGitHubSourceErrorTreatsDTAProjectFailuresAsUnprocessable(t *testing.T) {
	recorder := httptest.NewRecorder()
	writeGitHubSourceError(recorder, errors.New(`compile dingtalk-agent.json: agent.definition contains an unsafe path`))

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusUnprocessableEntity, recorder.Body.String())
	}
}
