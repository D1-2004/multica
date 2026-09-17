package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/agentsource"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Applied previews are the durable publication ledger. The repository snapshot
// stays unchanged; the portable post-import definition makes rollback complete
// even when an older manifest omitted fields introduced by a newer publication.
type agentPublicationSnapshot struct {
	agentsource.RepositorySnapshot
	PublishedDefinition *agentsource.Bundle `json:"published_definition,omitempty"`
	RollbackOf string `json:"rollback_of,omitempty"`
}

func marshalAgentPublicationSnapshot(snapshot agentPublicationSnapshot) ([]byte, error) {
	encoded, err := json.Marshal(snapshot)
	if err != nil { return nil, err }
	if len(encoded) > 64<<20 {
		return nil, sourceRequestError(http.StatusUnprocessableEntity, "Agent repository and publication configuration exceed the 64 MiB snapshot limit; reduce package file sizes and preview again")
	}
	return encoded, nil
}

func captureAgentPublication(ctx context.Context, q *db.Queries, preview db.AgentSourcePreview, source db.AgentSource) ([]byte, error) {
	var saved agentPublicationSnapshot
	if err := json.Unmarshal(preview.Snapshot,&saved); err != nil { return nil,err }
	agent, err := q.GetAgent(ctx, source.AgentID)
	if err != nil { return nil,err }
	exported, err := (agentPackageService{}).Export(ctx,q,agent,saved.Definition.Manifest.Spec.Instructions)
	if err != nil { return nil,err }
	// External identities are declarations, not historical credentials. Keep the
	// authored requirements instead of synthesizing new account bindings.
	declarations := saved.Definition.Definition
	if saved.RollbackOf != "" && saved.PublishedDefinition != nil { declarations = saved.PublishedDefinition.Definition }
	if bindings, present := declarations["bindings"]; present { exported.Manifest["bindings"] = bindings } else { delete(exported.Manifest,"bindings") }
	mappings, err := q.ListAgentSourceSkills(ctx,source.ID)
	if err != nil { return nil,err }
	owned := map[string]bool{}
	for _, mapping := range mappings { owned[uuidToString(mapping.SkillID)] = true }
	for index := range exported.Skills {
		// A source-owned skill may be deleted by a later version. Its path and
		// contents can recreate it; shared skills must retain scope/ID checks.
		if owned[exported.Skills[index].SkillID] { exported.Skills[index].Scope = nil; exported.Skills[index].SkillID = "" }
	}
	archive, err := agentsource.ExportAgentPackage(ctx,exported.Manifest,exported.Instructions,exported.Skills,nil)
	if err != nil { return nil,err }
	parsed, err := agentsource.ParseAgentPackage(ctx,archive)
	if err != nil { return nil,err }
	bundle, err := parsed.Bundle()
	if err != nil { return nil,err }
	saved.PublishedDefinition = &bundle
	return marshalAgentPublicationSnapshot(saved)
}

func (h *Handler) ListAgentPublications(w http.ResponseWriter, r *http.Request) {
	agent, _, ok := h.loadPackageSourceForManage(w,r)
	if !ok { return }
	var before pgtype.UUID
	if cursor := r.URL.Query().Get("before"); cursor != "" {
		var valid bool
		before, valid = parseUUIDOrBadRequest(w,cursor,"before")
		if !valid { return }
	}
	rows, err := h.Queries.ListAgentPublications(r.Context(),db.ListAgentPublicationsParams{AgentID:agent.ID,WorkspaceID:agent.WorkspaceID,BeforeID:before})
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	var next *string
	if len(rows) > 50 { value := uuidToString(rows[49].ID); next = &value; rows = rows[:50] }
	publications := make([]map[string]any,0,len(rows))
	for _, row := range rows {
		kind, repositoryURL := "local", ""
		if row.Repository != "" { kind, repositoryURL = "git", row.Repository }
		publications = append(publications,map[string]any{
			"id":uuidToString(row.ID), "source_type":kind, "repository_url":repositoryURL, "ref":row.Ref,
			"commit_sha":row.ResolvedSha, "published_at":timestampToString(row.AppliedAt),
			"published_by":uuidToString(row.CreatedBy), "author_name":row.AuthorName,
			"changed":row.AppliedChanged, "rollback_of":row.RollbackOf,
			"has_configuration_snapshot":row.HasConfigurationSnapshot, "initial_publication":row.InitialPublication,
		})
	}
	w.Header().Set("Cache-Control","no-store")
	writeJSON(w,http.StatusOK,map[string]any{"publications":publications,"next_cursor":next})
}

func (h *Handler) previewAgentPublicationRollback(w http.ResponseWriter, r *http.Request, agent db.Agent, source db.AgentSource, publicationID string) {
	id, ok := parseUUIDOrBadRequest(w,publicationID,"publication_id")
	if !ok { return }
	publication, err := h.Queries.GetAgentPublication(r.Context(),db.GetAgentPublicationParams{ID:id,AgentID:agent.ID,WorkspaceID:agent.WorkspaceID})
	if errors.Is(err,pgx.ErrNoRows) { writeError(w,http.StatusNotFound,"Agent publication not found"); return }
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	var saved agentPublicationSnapshot
	if err := json.Unmarshal(publication.Snapshot,&saved); err != nil { writeAgentSourceDatabaseError(w,err); return }
	if publication.Repository == "" || publication.Repository != source.RepositoryUrl {
		writeError(w,http.StatusConflict,"select a publication from this Agent's Git repository"); return
	}
	// Recheck the current installation's repository permission. Never resolve
	// the old ref again: it can move or disappear after publication.
	resolved, err := h.resolveGitAgentRepository(r.Context(),agent.WorkspaceID,GitAgentSourceInput{ConnectionID:uuidToString(source.GitConnectionID),Repository:publication.Repository,Ref:publication.Ref})
	if err != nil { writeGitRepoError(w,err); return }
	resolved.sha, resolved.snapshot, resolved.bundle = publication.ResolvedSha, saved.RepositorySnapshot, saved.Definition
	if saved.PublishedDefinition != nil { resolved.bundle = *saved.PublishedDefinition }
	if err := agentsource.ValidateBundle(resolved.bundle); err != nil { writeAgentPackageValidationError(w,err); return }
	// Older replicas read Definition directly when confirming a preview. Keep
	// that execution field complete too; Files/Tree remain the original Git diff.
	resolved.snapshot.Definition = resolved.bundle
	resolved.rollbackOf, resolved.publicationDefinition = publicationID, saved.PublishedDefinition
	base, err := h.publishedSourceSnapshot(r.Context(),agent,source,resolved.remote)
	if err != nil { writeGitRepoError(w,err); return }
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	agent, err = q.GetAgentForUpdate(r.Context(),agent.ID)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	if !h.canManageAgent(w,r,agent) { return }
	locked, err := q.LockAgentSourceByAgentID(r.Context(),agent.ID)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	if locked.ID != source.ID || locked.SyncedCommitSha != source.SyncedCommitSha || locked.Ref != source.Ref || locked.GitConnectionID != source.GitConnectionID || locked.ManagedSourceKey.Valid || agent.ArchivedAt.Valid {
		writeError(w,http.StatusConflict,"Agent source changed; preview again"); return
	}
	current, stateHash, err := sourceStateFiles(r.Context(),q,agent,locked,resolved.bundle)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	if err := tx.Commit(r.Context()); err != nil { writeAgentSourceDatabaseError(w,err); return }
	preview, err := h.saveAgentSourcePreview(r,agent.WorkspaceID,agent,source,resolved,stateHash)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	requirements, err := h.packageRequirementsForAgent(r.Context(),h.Queries,agent,requestUserID(r),resolved.bundle)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	warnings := append([]string{},resolved.bundle.Warnings...)
	if saved.PublishedDefinition == nil { warnings = append(warnings,"This older publication only saved its package declaration; configuration omitted from that package will retain its current value.") }
	changes := diffPackageState(current,sourceDefinitionFiles(resolved.bundle))
	w.Header().Set("Cache-Control","no-store")
	writeJSON(w,http.StatusOK,AgentSourceSyncPreviewResponse{
		RollbackOf:publicationID,
		Requirements:requirements,PreviewID:uuidToString(preview.ID),ExpiresAt:timestampToString(preview.ExpiresAt),
		RepositoryURL:"https://github.com/"+publication.Repository,Ref:resolved.ref,BaseSHA:source.SyncedCommitSha,ResolvedSHA:resolved.sha,
		GitChanges:agentsource.DiffRepository(packageDiffSnapshot(base),packageDiffSnapshot(resolved.snapshot)),ConfigurationChanges:changes,Warnings:warnings,Changed:true,
	})
}
