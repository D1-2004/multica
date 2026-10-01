package handler

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Agent work scene of inbound events (docs/agent-scene.md). Every inbound
// dispatch is registered once in agent_scene by resolveDispatchScene; the
// resulting SceneRef rides on the command (DispatchCommand.AgentScene), the
// Coordinator job (inbound_coordinator_job.scene_id) and the task context
// (agent_scene), and scene memory, associations, scene configuration and the
// outbound target read it instead of the raw conversation id.

// agentTenantOrg is the tenant org an event of the agent happens in, the
// same org context capabilities resolve (docs/context-capabilities.md §2):
// the org the dispatch recorded for the agent's DWS identity
// (external_identity.dws.orgId), else the org of its DingTalk identity
// binding. The org must be one the agent serves now: its identity org, a
// tenant created for it (agent_tenant), or, for an agent without an
// identity (a robot channel), the dispatch org itself. Any other org comes
// from an earlier binding and is scene.ErrStaleTenant; no org is
// scene.ErrUnresolved.
func agentTenantOrg(ctx context.Context, q *db.Queries, owner scene.Owner, dispatchOrg string) (string, error) {
	dispatchOrg = strings.TrimSpace(dispatchOrg)
	identityOrg := ""
	if q != nil {
		identity, err := q.GetAgentDingTalkIdentity(ctx, db.GetAgentDingTalkIdentityParams{WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID})
		switch {
		case err == nil:
			identityOrg = strings.TrimSpace(identity.OrgID)
		case errors.Is(err, pgx.ErrNoRows):
		default:
			return "", err
		}
	}
	org := dispatchOrg
	if org == "" {
		org = identityOrg
	}
	switch {
	case org == "":
		return "", scene.ErrUnresolved
	case org == identityOrg || identityOrg == "":
		return org, nil
	case q == nil:
		return "", scene.ErrStaleTenant
	}
	served, err := q.AgentServesTenantOrg(ctx, db.AgentServesTenantOrgParams{WorkspaceID: owner.WorkspaceID, AgentID: owner.AgentID, OrgID: org})
	if err != nil {
		return "", err
	}
	if !served {
		return "", scene.ErrStaleTenant
	}
	return org, nil
}

func dispatchRecordedOrg(command DispatchCommand) string {
	if command.ExternalIdentity.DWS == nil {
		return ""
	}
	return strings.TrimSpace(command.ExternalIdentity.DWS.OrgID)
}

// dispatchSceneLocator is the locator of a dispatch under tenantOrg: a
// channel event is its conversation (kind from the conversation type, else
// the Router context's chat type), any other domain (calendar, approval) is
// the enterprise scene.
func dispatchSceneLocator(command DispatchCommand, tenantOrg string) (scene.Locator, scene.Observation, error) {
	obs := scene.Observation{ActiveAt: dispatchMessageOccurredAt(command)}
	if command.Event.Domain != "channel" {
		return scene.DingTalkEnterprise(tenantOrg), obs, nil
	}
	ids := dispatchAssocIDs(command)
	if ids.ConversationID == "" {
		return scene.Locator{}, obs, scene.ErrUnresolved
	}
	chatType := strings.TrimSpace(command.Event.Data.Conversation.Type)
	if chatType == "" {
		chatType = strings.TrimSpace(ids.Kind)
	}
	kind, ok := scene.KindFromConversationType(chatType)
	if !ok {
		return scene.Locator{}, obs, scene.ErrUnknownKind
	}
	// The conversation type comes from the trusted event (or the Router's
	// context of it), so it may settle a migrated kind.
	obs.KindStated = true
	obs.Title = strings.TrimSpace(command.Event.Data.Conversation.Title)
	if obs.Title == "" && kind == scene.KindDM {
		obs.Title = strings.TrimSpace(command.Event.Data.Sender.DisplayName)
	}
	return scene.DingTalkConversation(tenantOrg, kind, ids.ConversationID), obs, nil
}

// resolveDispatchScene registers (or finds) the Agent work scene of an
// inbound dispatch. Errors are scene.ErrUnresolved, ErrUnknownKind,
// ErrStaleTenant, ErrKindConflict or a storage error; callers continue
// without a scene and skip every scene-dependent effect.
func (h *Handler) resolveDispatchScene(ctx context.Context, q *db.Queries, command DispatchCommand, dispatchContext agentDispatchContext) (db.AgentScene, error) {
	if q == nil {
		return db.AgentScene{}, scene.ErrUnresolved
	}
	owner := scene.Owner{WorkspaceID: dispatchContext.WorkspaceID, AgentID: dispatchContext.AgentID}
	org, err := agentTenantOrg(ctx, q, owner, dispatchRecordedOrg(command))
	if err != nil {
		return db.AgentScene{}, err
	}
	loc, obs, err := dispatchSceneLocator(command, org)
	if err != nil {
		return db.AgentScene{}, err
	}
	return scene.Resolve(ctx, q, owner, loc, obs)
}

// attachDispatchScene resolves the scene of an inbound command and sets
// command.AgentScene; an unresolvable scene is logged and leaves it nil.
func (h *Handler) attachDispatchScene(ctx context.Context, command *DispatchCommand, dispatchContext agentDispatchContext) {
	if h == nil || h.Queries == nil || command == nil {
		return
	}
	resolved, err := h.resolveDispatchScene(ctx, h.Queries, *command, dispatchContext)
	if err != nil {
		level := slog.LevelInfo
		if !errors.Is(err, scene.ErrUnresolved) && !errors.Is(err, scene.ErrUnknownKind) {
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "agent scene unresolved",
			"event", "agent_scene_unresolved",
			"agent_id", util.UUIDToString(dispatchContext.AgentID),
			"domain", command.Event.Domain,
			"conversation_type", command.Event.Data.Conversation.Type,
			"error", err,
		)
		command.AgentScene = nil
		return
	}
	ref := scene.RefOf(resolved)
	command.AgentScene = &ref
}

// dispatchScene loads the command's scene from the directory through the
// use-time fence (fencedScene); nil AgentScene or a scene of another agent
// is ErrNotFound, a scene of an org the agent no longer serves
// ErrStaleTenant.
func dispatchScene(ctx context.Context, q *db.Queries, command DispatchCommand, dispatchContext agentDispatchContext) (db.AgentScene, error) {
	return fencedScene(ctx, q, command.AgentScene, scene.Owner{WorkspaceID: dispatchContext.WorkspaceID, AgentID: dispatchContext.AgentID}, dispatchRecordedOrg(command))
}

// fencedScene is the use-time fence of a persisted SceneRef
// (docs/agent-scene.md §6): the scene must still be the owner's and belong
// to the org the agent serves now (agentTenantOrg: its DingTalk identity
// org, else the recorded dispatch org). A job or task admitted before the
// agent was re-bound to another org therefore reads no scene state.
func fencedScene(ctx context.Context, q *db.Queries, ref *scene.Ref, owner scene.Owner, dispatchOrg string) (db.AgentScene, error) {
	if ref == nil || q == nil {
		return db.AgentScene{}, scene.ErrNotFound
	}
	id, err := scene.ParseID(ref.SceneID)
	if err != nil {
		return db.AgentScene{}, err
	}
	sc, err := scene.Get(ctx, q, owner, id)
	if err != nil {
		return db.AgentScene{}, err
	}
	org, err := agentTenantOrg(ctx, q, owner, dispatchOrg)
	if err != nil {
		return db.AgentScene{}, err
	}
	if err := scene.CheckTenant(sc, org); err != nil {
		return db.AgentScene{}, err
	}
	return sc, nil
}

// fenceSceneRef returns ref while it passes fencedScene, else nil (logged):
// callers then skip every scene-dependent effect.
func (h *Handler) fenceSceneRef(ctx context.Context, ref *scene.Ref, owner scene.Owner, dispatchOrg string) *scene.Ref {
	if ref == nil || h == nil || h.Queries == nil {
		return nil
	}
	sc, err := fencedScene(ctx, h.Queries, ref, owner, dispatchOrg)
	if err != nil {
		level := slog.LevelInfo
		if !errors.Is(err, scene.ErrNotFound) && !errors.Is(err, scene.ErrStaleTenant) && !errors.Is(err, scene.ErrUnresolved) {
			level = slog.LevelWarn
		}
		slog.Log(ctx, level, "agent scene fenced",
			"event", "agent_scene_fenced",
			"agent_id", util.UUIDToString(owner.AgentID),
			"scene_id", ref.SceneID,
			"error", err,
		)
		return nil
	}
	out := scene.RefOf(sc)
	return &out
}

// dispatchSceneID is the command's scene_id, "" without a resolved scene.
func dispatchSceneID(command DispatchCommand) string {
	if command.AgentScene == nil {
		return ""
	}
	return strings.TrimSpace(command.AgentScene.SceneID)
}

// dispatchSceneNode is the graph node of the command's scene, read back from
// the directory; the zero node when the dispatch has no scene.
func (h *Handler) dispatchSceneNode(ctx context.Context, command DispatchCommand, dispatchContext agentDispatchContext) assoc.SceneNode {
	if h == nil || h.Queries == nil {
		return assoc.SceneNode{}
	}
	sc, err := dispatchScene(ctx, h.Queries, command, dispatchContext)
	if err != nil {
		return assoc.SceneNode{}
	}
	return sceneNodeOf(sc)
}

func sceneNodeOf(sc db.AgentScene) assoc.SceneNode {
	return assoc.SceneNode{SceneID: util.UUIDToString(sc.ID), ConversationID: sc.ExternalSceneID, Kind: sc.SceneKind}
}

// taskDispatchOrg is the org a task's dispatch recorded for the agent
// (external_identity.dws.orgId in its context), "" when the task is unknown
// or recorded none. A tool call inside a task names conversations of that
// org (agentTenantOrg).
func (h *Handler) taskDispatchOrg(ctx context.Context, workspaceID, taskID string) string {
	if h == nil || h.Queries == nil || strings.TrimSpace(taskID) == "" {
		return ""
	}
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		return ""
	}
	id, err := util.ParseUUID(strings.TrimSpace(taskID))
	if err != nil {
		return ""
	}
	task, err := h.Queries.GetAgentTaskInWorkspace(ctx, db.GetAgentTaskInWorkspaceParams{ID: id, WorkspaceID: ws})
	if err != nil {
		return ""
	}
	return contextDispatchOrg(task.Context)
}

// contextDispatchOrg reads external_identity.dws.orgId from a task context.
func contextDispatchOrg(raw []byte) string {
	var envelope struct {
		ExternalIdentity struct {
			DWS struct {
				OrgID string `json:"orgId"`
			} `json:"dws"`
		} `json:"external_identity"`
	}
	if len(raw) == 0 || json.Unmarshal(raw, &envelope) != nil {
		return ""
	}
	return strings.TrimSpace(envelope.ExternalIdentity.DWS.OrgID)
}

// conversationSceneNode finds the agent's work scene of a DingTalk
// conversation id an API or tool caller named, in the agent's tenant org.
// With register set, an unseen conversation is registered as kind (dm or
// group, as the caller states it); a registered conversation keeps its
// kind, unless kindStated says the kind comes from a trusted event (a
// channel callback, a send to a person) and the registered kind is one a
// migration assigned without evidence (scene.Observation.KindStated).
// Without register, an unseen conversation is (zero, false, nil).
func (h *Handler) conversationSceneNode(ctx context.Context, workspaceID, agentID, conversationID, kind, dispatchOrg string, register, kindStated bool) (assoc.SceneNode, bool, error) {
	cid := assoc.NormalizeConversationID(conversationID)
	if h == nil || h.Queries == nil || cid == "" {
		return assoc.SceneNode{}, false, nil
	}
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		return assoc.SceneNode{}, false, fmt.Errorf("%w: invalid workspace", assoc.ErrInvalidQuery)
	}
	agent, err := util.ParseUUID(agentID)
	if err != nil {
		return assoc.SceneNode{}, false, fmt.Errorf("%w: invalid agent", assoc.ErrInvalidQuery)
	}
	owner := scene.Owner{WorkspaceID: ws, AgentID: agent}
	org, err := agentTenantOrg(ctx, h.Queries, owner, dispatchOrg)
	if errors.Is(err, scene.ErrUnresolved) || errors.Is(err, scene.ErrStaleTenant) {
		if register {
			return assoc.SceneNode{}, false, fmt.Errorf("%w: the agent has no tenant org for conversation scenes", assoc.ErrInvalidQuery)
		}
		return assoc.SceneNode{}, false, nil
	}
	if err != nil {
		return assoc.SceneNode{}, false, err
	}
	found, err := scene.Lookup(ctx, h.Queries, owner, scene.DingTalkConversation(org, "", cid))
	switch {
	case err == nil && !(register && kindStated && found.KindSource == scene.KindSourceMigrated):
		return sceneNodeOf(found), true, nil
	case err == nil:
		// Fall through to Resolve, which settles the migrated kind.
	case errors.Is(err, scene.ErrInvalidLocator):
		return assoc.SceneNode{}, false, fmt.Errorf("%w: conversation_id is not a DingTalk conversation id", assoc.ErrInvalidQuery)
	case !errors.Is(err, scene.ErrNotFound):
		return assoc.SceneNode{}, false, err
	}
	if !register {
		return assoc.SceneNode{}, false, nil
	}
	sceneKind, ok := scene.KindFromConversationType(kind)
	if !ok {
		return assoc.SceneNode{}, false, fmt.Errorf("%w: kind must be dm or group for a new conversation", assoc.ErrInvalidQuery)
	}
	registered, err := scene.Resolve(ctx, h.Queries, owner, scene.DingTalkConversation(org, sceneKind, cid), scene.Observation{KindStated: kindStated})
	if err != nil {
		if errors.Is(err, scene.ErrInvalidLocator) || errors.Is(err, scene.ErrKindConflict) {
			return assoc.SceneNode{}, false, fmt.Errorf("%w: %v", assoc.ErrInvalidQuery, err)
		}
		return assoc.SceneNode{}, false, err
	}
	return sceneNodeOf(registered), true, nil
}

// coordinatorSceneLookup resolves conversation ids the Coordinator model
// names to the agent's registered scenes.
type coordinatorSceneLookup struct{ h *Handler }

// CoordinatorSceneLookup is the Coordinator's resolver for conversation ids
// the model names (inboundcoord.Coordinator.SetSceneLookup).
func CoordinatorSceneLookup(h *Handler) inboundcoord.SceneLookup {
	return coordinatorSceneLookup{h: h}
}

func (l coordinatorSceneLookup) LookupConversationScene(ctx context.Context, turn inboundcoord.Turn, conversationID string) (assoc.SceneNode, bool, error) {
	return l.h.conversationSceneNode(ctx, strings.TrimSpace(turn.WorkspaceID), util.UUIDToString(turn.AgentID), conversationID, "", turn.DWSOrgID, false, false)
}

// AssociateChannelConversation links an Issue created by the channel engine
// (robot channels) to the agent's work scene of the conversation. The scene
// is registered like any inbound scene; without a tenant org or a known
// chat type the Issue is linked without a scene.
func (h *Handler) AssociateChannelConversation(ctx context.Context, in assoc.AssociateInput, conversationID, chatType string) error {
	if h == nil || h.Assoc == nil {
		return nil
	}
	if kind, ok := scene.KindFromConversationType(chatType); ok {
		node, found, err := h.conversationSceneNode(ctx, in.WorkspaceID, in.AgentID, conversationID, kind, "", true, true)
		switch {
		case err == nil && found:
			in.Scene = node
		case err != nil && !errors.Is(err, assoc.ErrInvalidQuery):
			return err
		}
	}
	return h.Assoc.AssociateIssueConversation(ctx, in)
}

// fencedSceneNode loads an agent's scene by scene_id through the use-time
// fence for dispatchOrg (fencedScene) as a graph node.
func (h *Handler) fencedSceneNode(ctx context.Context, workspaceID, agentID, sceneID, dispatchOrg string) (assoc.SceneNode, bool) {
	if h == nil || h.Queries == nil {
		return assoc.SceneNode{}, false
	}
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		return assoc.SceneNode{}, false
	}
	agent, err := util.ParseUUID(agentID)
	if err != nil {
		return assoc.SceneNode{}, false
	}
	sc, err := fencedScene(ctx, h.Queries, &scene.Ref{SceneID: sceneID}, scene.Owner{WorkspaceID: ws, AgentID: agent}, dispatchOrg)
	if err != nil {
		return assoc.SceneNode{}, false
	}
	return sceneNodeOf(sc), true
}

// sceneNodeByID loads an agent's scene by scene_id as a graph node.
func (h *Handler) sceneNodeByID(ctx context.Context, workspaceID, agentID, sceneID string) (assoc.SceneNode, bool) {
	if h == nil || h.Queries == nil {
		return assoc.SceneNode{}, false
	}
	ws, err := util.ParseUUID(workspaceID)
	if err != nil {
		return assoc.SceneNode{}, false
	}
	agent, err := util.ParseUUID(agentID)
	if err != nil {
		return assoc.SceneNode{}, false
	}
	id, err := scene.ParseID(sceneID)
	if err != nil {
		return assoc.SceneNode{}, false
	}
	sc, err := scene.Get(ctx, h.Queries, scene.Owner{WorkspaceID: ws, AgentID: agent}, id)
	if err != nil {
		return assoc.SceneNode{}, false
	}
	return sceneNodeOf(sc), true
}

// ReconcileSceneCredentials re-keys scene connector credentials stored under
// a conversation id onto their scene_id (contextcap.ReconcileSceneCredentials).
// It runs once at startup; failures are logged and retried next start.
func (h *Handler) ReconcileSceneCredentials(ctx context.Context) {
	if h == nil || h.DB == nil {
		return
	}
	box := h.contextCredentialBox()
	if box == nil {
		return
	}
	moved, err := contextcap.ReconcileSceneCredentials(ctx, h.DB, box)
	if err != nil {
		slog.WarnContext(ctx, "scene credential re-key failed", "event", "scene_credential_rekey", "moved", moved, "error", err)
		return
	}
	if moved > 0 {
		slog.InfoContext(ctx, "scene credentials re-keyed to scene ids", "event", "scene_credential_rekey", "moved", moved)
	}
}
