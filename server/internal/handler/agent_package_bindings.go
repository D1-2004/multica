package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentitygithub"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type packageBindingItem struct {
	Path string `json:"path"`
	Status string `json:"status"`
	Declaration json.RawMessage `json:"declaration"`
	Current json.RawMessage `json:"current"`
	CurrentFingerprint string `json:"current_fingerprint"`
	ConfigTab string `json:"config_tab"`
	Message string `json:"message"`
}
type packageBindingReport struct {
	Revision string `json:"revision"`
	Bindings []packageBindingItem `json:"bindings"`
	Resources []map[string]string `json:"resources"`
}

// Resource selection/authentication is performed by the existing configuration
// workflows. This endpoint only acknowledges a freshly verified, same-Agent
// resource; it cannot grant access, move an account, or copy credentials.
func (h *Handler) GetAgentPackageBindings(w http.ResponseWriter, r *http.Request) {
	agent, ok := h.loadAgentForUser(w, r, chi.URLParam(r,"id"))
	if !ok || !h.canManageAgent(w,r,agent) { return }
	state, err := readPackageBindingState(r.Context(), h.Queries, agent)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	report, err := h.packageBindingReport(r.Context(), h.Queries, agent, requestUserID(r), state)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	w.Header().Set("Cache-Control","no-store")
	writeJSON(w,http.StatusOK,report)
}

func (h *Handler) ConfirmAgentPackageBinding(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("X-Actor-Source") != "" { writeError(w,http.StatusForbidden,"package binding confirmation requires a human actor"); return }
	agent, ok := h.loadAgentForUser(w,r,chi.URLParam(r,"id"))
	if !ok || !h.canManageAgent(w,r,agent) { return }
	var request struct {
		Path string `json:"path"`
		Revision string `json:"revision"`
		CurrentFingerprint string `json:"current_fingerprint"`
		Mappings map[string]string `json:"mappings"`
	}
	if err := decodeLimitedJSON(w,r,32<<10,&request,"invalid package binding confirmation"); err != nil { return }
	tx, err := h.TxStarter.Begin(r.Context())
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	defer tx.Rollback(r.Context())
	q := h.Queries.WithTx(tx)
	agent, err = q.GetAgentForUpdate(r.Context(),agent.ID)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	if !h.canManageAgent(w,r,agent) { return }
	if _, err := q.LockAgentSourceByAgentID(r.Context(),agent.ID); err != nil { writeAgentSourceDatabaseError(w,err); return }
	state, err := readPackageBindingState(r.Context(),q,agent)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	report, err := h.packageBindingReport(r.Context(),q,agent,requestUserID(r),state)
	if err != nil { writeAgentSourceDatabaseError(w,err); return }
	if request.Revision == "" || request.Revision != report.Revision { writeError(w,http.StatusConflict,"package requirements changed; reload and confirm again"); return }
	var selected *packageBindingItem
	for i := range report.Bindings { if report.Bindings[i].Path == request.Path { selected = &report.Bindings[i]; break } }
	if selected == nil || selected.Status == "unavailable" || request.CurrentFingerprint != selected.CurrentFingerprint || request.CurrentFingerprint == "" { writeError(w,http.StatusConflict,"the selected Agent resource is unavailable or changed; configure it and reload"); return }
	if !packageBindingMatches(selected.Declaration, selected.Current, request.Mappings) { writeError(w,http.StatusUnprocessableEntity,"map every declared reference to the matching configured resource; resource kinds, enabled flags and access policy must agree"); return }
	state.Receipts[request.Path] = packageBindingReceipt{Declaration:packageValueHash(selected.Declaration), Actual:selected.Current, Mappings:request.Mappings}
	if err := writePackageBindingState(r.Context(),q,agent,state); err != nil { writeAgentSourceDatabaseError(w,err); return }
	if err := tx.Commit(r.Context()); err != nil { writeAgentSourceDatabaseError(w,err); return }
	selected.Status = "ready"
	report.Revision = packageValueHash(state)
	writeJSON(w,http.StatusOK,report)
}

func packageBindingMatches(declaration, current json.RawMessage, mappings map[string]string) bool {
	var desired, actual any
	if json.Unmarshal(declaration,&desired) != nil || json.Unmarshal(current,&actual) != nil { return false }
	used := map[string]bool{}
	var match func(any,any) bool
	match = func(d,a any) bool {
		switch value := d.(type) {
		case map[string]any:
			other, ok := a.(map[string]any); if !ok { return false }
			for key, child := range value {
				if key == "ref" || key == "runtime_ref" {
					ref, ok := child.(string); if !ok || mappings[ref] == "" || mappings[ref] != other[key] { return false }
					continue
				}
				if !match(child,other[key]) { return false }
			}
			return true
		case []any:
			other, ok := a.([]any); if !ok || len(value) != len(other) { return false }
			matched := map[int]bool{}
			for _, child := range value { found := false; for i, candidate := range other { if !matched[i] && match(child,candidate) { matched[i] = true; found = true; break } }; if !found { return false } }
			return true
		default: return d == a
		}
	}
	for _, target := range mappings { if used[target] { return false }; used[target] = true }
	return match(desired,actual)
}

func (h *Handler) packageBindingReport(ctx context.Context, q *db.Queries, agent db.Agent, actor string, state packageBindingState) (packageBindingReport, error) {
	notes := newAgentExportNotes()
	actual, unavailable, err := h.packageActualBindings(ctx,q,agent,actor,state,notes)
	if err != nil { return packageBindingReport{},err }
	report := packageBindingReport{Revision:packageValueHash(state),Bindings:[]packageBindingItem{},Resources:notes.Resources}
	paths := make([]string,0,len(state.Declarations))
	for path := range state.Declarations { if path != "/bindings/runtime" { paths = append(paths,path) } }
	sort.Strings(paths)
	for _, path := range paths {
		value := actual[path]
		if value == nil { value = json.RawMessage(`null`) }
		item := packageBindingItem{Path:path,Status:"pending",Declaration:state.Declarations[path],Current:value,ConfigTab:packageBindingConfigTab(path)}
		if unavailable[path] != "" { item.Status = "unavailable"; item.Message = unavailable[path] } else {
			item.CurrentFingerprint = packageValueHash(value)
			if state.ready(path,item.Declaration,value) { item.Status = "ready" }
		}
		report.Bindings = append(report.Bindings,item)
	}
	return report,nil
}

func packageBindingConfigTab(path string) string {
	switch path {
	case "/bindings/runner": return "runner"
	case "/bindings/github_identity": return "general"
	case "/bindings/enterprise_identity", "/bindings/dingtalk_account": return "digital_employee"
	case "/dsh_plugins": return "dsh_plugins"
	case "/access": return "access"
	case "/disabled_runtime_skills": return "skills"
	default: return "integrations"
	}
}

func (h *Handler) packageActualBindings(ctx context.Context, q *db.Queries, agent db.Agent, actor string, state packageBindingState, notes *agentExportNotes) (map[string]json.RawMessage,map[string]string,error) {
	result := map[string]json.RawMessage{}
	unavailable := map[string]string{}
	bindings, err := exportPackageBindings(ctx,q,agent,notes)
	if err != nil { return nil,nil,err }
	for kind, value := range bindings { result["/bindings/"+kind], _ = json.Marshal(value) }
	for path := range state.Declarations {
		switch path {
		case "/dsh_plugins": value, err := exportPackagePlugins(ctx,q,agent,notes); if err != nil { return nil,nil,err }; result[path],_ = json.Marshal(value)
		case "/access": value, err := exportPackageAccess(ctx,q,agent,notes); if err != nil { return nil,nil,err }; result[path],_ = json.Marshal(value)
		case "/disabled_runtime_skills": value, err := exportPackageDisabledRuntimeSkills(ctx,q,agent,notes); if err != nil { return nil,nil,err }; result[path],_ = json.Marshal(value)
		case "/bindings/github_identity":
			if h == nil || !h.AgentIdentityGitHub.Enabled() { unavailable[path] = "GitHub identity service is unavailable"; continue }
			connection, err := h.AgentIdentityGitHub.GetStatus(ctx,uuidToString(agent.WorkspaceID),uuidToString(agent.ID),actor)
			if err != nil {
				var serviceError *agentidentitygithub.ServiceError
				if errors.As(err,&serviceError) && serviceError.StatusCode == http.StatusNotFound { result[path] = json.RawMessage(`null`); continue }
				unavailable[path] = "GitHub identity could not be verified"; continue
			}
			if connection.ConnectionID == "" { result[path] = json.RawMessage(`null`); continue }
			if !connection.OK || !strings.EqualFold(connection.Status,"active") || (connection.RefreshExpiresAt != nil && packageEpochExpired(*connection.RefreshExpiresAt)) { unavailable[path] = "GitHub identity needs authorization"; continue }
			result[path],_ = json.Marshal(notes.resource("github-identity",connection.ConnectionID+":"+connection.AccountID,connection.AccountLogin))
		case "/bindings/enterprise_identity":
			identity, err := q.GetAgentEnterpriseIdentity(ctx,db.GetAgentEnterpriseIdentityParams{WorkspaceID:agent.WorkspaceID,AgentID:agent.ID})
			if errors.Is(err,pgx.ErrNoRows) { continue }; if err != nil { return nil,nil,err }
			if identity.Status == "revoked" { result[path] = json.RawMessage(`null`); continue }
			if identity.Status != "active" || (identity.AuthxRefreshExpiresAt.Valid && !identity.AuthxRefreshExpiresAt.Time.After(time.Now())) { unavailable[path] = "Enterprise identity needs authorization" }
		case "/bindings/dingtalk_account":
			if string(result[path]) == "null" { continue }
			if h == nil || h.DingTalkAccountBindings == nil { unavailable[path] = "DingTalk account service is unavailable"; continue }
			status, err := h.DingTalkAccountBindings.GetMessageBindingStatus(ctx,agent.WorkspaceID,agent.ID)
			if err != nil || !status.Configured || status.Verification.Status != "verified" { unavailable[path] = "DingTalk account binding could not be verified" }
		}
	}
	// Stable collection ordering keeps resource receipts independent of SQL row order.
	for path, raw := range result { if strings.HasPrefix(string(raw),"[") { var values []json.RawMessage; if json.Unmarshal(raw,&values) == nil { sort.Slice(values,func(i,j int)bool{return packageValueHash(values[i]) < packageValueHash(values[j])}); result[path],_ = json.Marshal(values) } } }
	return result,unavailable,nil
}

func packageEpochExpired(value int64) bool { if value > 1_000_000_000_000 { return value <= time.Now().UnixMilli() }; return value <= time.Now().Unix() }
