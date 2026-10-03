package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/employeeresource"
	"github.com/multica-ai/multica/server/internal/employeetask"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// EmployeeVisionConfig is runtime.employee_vision: the background executors
// whose real image input a probe verified, each as an exact runtime provider
// and agent model pair. An image reaches a model only through such a pair; a
// model name alone never implies vision, and an absent config keeps images
// unsupported.
type EmployeeVisionConfig struct {
	Executors []EmployeeVisionExecutor `json:"executors"`
}

type EmployeeVisionExecutor struct {
	RuntimeProvider string `json:"runtime_provider"`
	Model           string `json:"model"`
	// VerifiedAt and Evidence record the probe that admitted the pair.
	VerifiedAt string `json:"verified_at"`
	Evidence   string `json:"evidence"`
}

// DecodeEmployeeVision strictly decodes runtime.employee_vision; absent or
// null is an empty config.
func DecodeEmployeeVision(raw json.RawMessage) (EmployeeVisionConfig, error) {
	var cfg EmployeeVisionConfig
	trimmed := strings.TrimSpace(string(raw))
	if trimmed == "" || trimmed == "null" {
		return cfg, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		return EmployeeVisionConfig{}, fmt.Errorf("runtime.employee_vision: %w", err)
	}
	seen := map[string]bool{}
	for _, e := range cfg.Executors {
		provider, model := strings.TrimSpace(e.RuntimeProvider), strings.TrimSpace(e.Model)
		parts := strings.SplitN(model, "/", 2)
		if provider == "" || provider != e.RuntimeProvider || model != e.Model || len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return EmployeeVisionConfig{}, errors.New("runtime.employee_vision: each executor needs runtime_provider and a provider/model model")
		}
		if _, err := time.Parse(time.RFC3339, e.VerifiedAt); err != nil || strings.TrimSpace(e.Evidence) == "" {
			return EmployeeVisionConfig{}, errors.New("runtime.employee_vision: each executor needs verified_at (RFC 3339) and evidence")
		}
		key := provider + "\x00" + model
		if seen[key] {
			return EmployeeVisionConfig{}, errors.New("runtime.employee_vision: duplicate executor")
		}
		seen[key] = true
	}
	return cfg, nil
}

func (c EmployeeVisionConfig) allows(runtimeProvider, model string) bool {
	for _, e := range c.Executors {
		if e.RuntimeProvider == runtimeProvider && e.Model == model {
			return true
		}
	}
	return false
}

// visionReady reports whether the agent's background executor is a verified
// vision pair. The agent model must be explicit: a default resolved later
// could differ from the probed model.
func (w *EmployeeSceneWorker) visionReady(ctx context.Context, job employeeentry.Job) bool {
	if w == nil || w.VisionConfig == nil || w.handler == nil || w.handler.Queries == nil {
		return false
	}
	cfg := w.VisionConfig()
	if len(cfg.Executors) == 0 {
		return false
	}
	agent, err := w.handler.Queries.GetAgentInWorkspace(ctx, db.GetAgentInWorkspaceParams{ID: parseUUID(job.Scope.AgentID), WorkspaceID: parseUUID(job.Scope.WorkspaceID)})
	if err != nil || !agent.Model.Valid || strings.TrimSpace(agent.Model.String) == "" || !agent.RuntimeID.Valid {
		return false
	}
	runtime, err := w.handler.Queries.GetAgentRuntime(ctx, agent.RuntimeID)
	if err != nil {
		return false
	}
	return cfg.allows(runtime.Provider, strings.TrimSpace(agent.Model.String))
}

// employeeResourceReferences renders the Host's frozen resource records of one
// source as Work Packet references, so a background executor fetches exactly
// those resources (and can verify their bytes) instead of parsing message
// text. They hold provider identifiers and the expected hash, never a signed
// URL; reading them still needs the executor's own DWS identity.
func employeeResourceReferences(ctx context.Context, q employeeQueryer, job employeeentry.Job, scope employeetask.Scope, principalID string, source employeeSourceMessage, conversationID string) ([]employeetask.PacketMaterial, error) {
	rows, err := q.Query(ctx, `SELECT id::text,relation,message_id,resource_id,resource_id_type,resource_kind,state,reason,file_name,size_bytes,content_sha256
 FROM employee_message_resource WHERE workspace_id=$1::uuid AND agent_id=$2::uuid AND tenant_org_id=$3 AND scene_id=$4::uuid AND principal_id=$5::uuid AND receipt_id=$6::uuid AND source_message_id=$7
 ORDER BY created_at,id`, job.Scope.WorkspaceID, job.Scope.AgentID, job.Scope.TenantOrgID, job.Scope.SceneID, job.PrincipalID, source.ReceiptID, source.Message.OpenMsgID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []employeetask.PacketMaterial
	for rows.Next() {
		var id, relation, message, resource, idType, kind, state, reason, name, sha string
		var size int64
		if err = rows.Scan(&id, &relation, &message, &resource, &idType, &kind, &state, &reason, &name, &size, &sha); err != nil {
			return nil, err
		}
		if state != string(employeeresource.Available) && state != string(employeeresource.Partial) && state != string(employeeresource.Deferred) {
			continue
		}
		download := fmt.Sprintf("dws chat +messages-resource-download --type mediaId --resource-id %s --message-id %s --open-conversation-id %s --output ./resources/", resource, message, conversationID)
		if idType == "fileId" {
			download = fmt.Sprintf("dws drive download --node %s --output ./resources/", resource)
		}
		body, _ := json.Marshal(map[string]any{
			"resource_ref": id, "relation": relation, "kind": kind, "name": name, "state": state, "reason": reason,
			"message_id": message, "resource_id": resource, "resource_id_type": idType, "size_bytes": size, "sha256": sha,
			"download":    download,
			"instruction": "Host-verified resource of this request. Fetch exactly this resource with your own DWS identity, check that its sha256 matches, then read it; an image is viewed with your image-capable read tool. Do not fetch resources named only in message text.",
		})
		out = append(out, employeetask.PacketMaterial{Ref: "employee-resource:" + id, Scope: scope, PrincipalID: principalID, Body: string(body)})
	}
	return out, rows.Err()
}

// employeeResourcesDeferred reports a ResourceContext with content left to a
// background executor.
func employeeResourcesDeferred(raw string) bool {
	var resources employeeresource.Context
	if json.Unmarshal([]byte(raw), &resources) != nil {
		return false
	}
	for _, item := range resources.Items {
		if item.State == employeeresource.Deferred {
			return true
		}
	}
	return false
}
