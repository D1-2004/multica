package handler

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"errors"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/agentsource"
	"strings"
	"sort"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Only declarations and opaque resource fingerprints live here. Credential
// values remain in the existing configuration/auth stores and never enter state.
type packageBindingState struct {
	Declarations map[string]json.RawMessage `json:"declarations"`
	Receipts map[string]packageBindingReceipt `json:"receipts"`
	SecretRefs map[string]string `json:"secret_refs"`
}
type packageBindingReceipt struct {
	Declaration string `json:"declaration"`
	Actual json.RawMessage `json:"actual"`
	Mappings map[string]string `json:"mappings,omitempty"`
}

func readPackageBindingState(ctx context.Context, q *db.Queries, agent db.Agent) (packageBindingState, error) {
	encoded, err := q.GetAgentPackageBindingState(ctx, db.GetAgentPackageBindingStateParams{AgentID:agent.ID, WorkspaceID:agent.WorkspaceID})
	if err != nil { return packageBindingState{}, err }
	var state packageBindingState
	if err := json.Unmarshal(encoded, &state); err != nil { return state, err }
	if state.Declarations == nil { state.Declarations = map[string]json.RawMessage{} }
	if state.Receipts == nil { state.Receipts = map[string]packageBindingReceipt{} }
	if state.SecretRefs == nil { state.SecretRefs = map[string]string{} }
	// Older package publications stored the original manifest in their applied
	// preview. Recover its unresolved requirements without inventing receipts.
	if string(encoded) == "{}" {
		preview, err := q.LatestAppliedAgentSourcePreview(ctx,db.LatestAppliedAgentSourcePreviewParams{AgentID:agent.ID,WorkspaceID:agent.WorkspaceID})
		if err != nil && !errors.Is(err,pgx.ErrNoRows) { return state,err }
		if err == nil {
			var snapshot agentsource.RepositorySnapshot
			if err := json.Unmarshal(preview.Snapshot,&snapshot); err != nil { return state,err }
			definition := snapshot.Definition.Definition
			var bindings portableBindings
			if raw := definition["bindings"]; raw != nil { if err := json.Unmarshal(raw,&bindings); err != nil { return state,err }; for kind, declaration := range bindings { state.declare("/bindings/"+kind,declaration) } }
			for _, path := range packageRequirements(snapshot.Definition).DeferredBindings {
				if strings.HasPrefix(path,"/bindings/") { continue }
				if declaration := definition[strings.TrimPrefix(path,"/")]; declaration != nil { state.declare(path,declaration) }
			}
			state.captureSecrets(definition["configuration"])
		}
	}
	return state, nil
}
func writePackageBindingState(ctx context.Context, q *db.Queries, agent db.Agent, state packageBindingState) error {
	encoded, err := json.Marshal(state)
	if err != nil { return err }
	count, err := q.UpdateAgentPackageBindingState(ctx, db.UpdateAgentPackageBindingStateParams{AgentID:agent.ID, WorkspaceID:agent.WorkspaceID, State:encoded})
	if err == nil && count != 1 { return fmt.Errorf("Agent package source no longer exists") }
	return err
}
func packageValueHash(value any) string {
	encoded, _ := json.Marshal(value)
	var normalized any
	_ = json.Unmarshal(encoded, &normalized)
	// Binding collections are sets: SQL row order and manifest ordering do
	// not invalidate an otherwise identical resource receipt.
	var canonicalize func(any) any
	canonicalize = func(v any) any { switch item := v.(type) {
	case map[string]any: for key, child := range item { item[key] = canonicalize(child) }
	case []any:
		for i, child := range item { item[i] = canonicalize(child) }
		sort.Slice(item,func(i,j int)bool { left,_ := json.Marshal(item[i]); right,_ := json.Marshal(item[j]); return string(left) < string(right) })
	}; return v }
	encoded, _ = json.Marshal(canonicalize(normalized))
	return fmt.Sprintf("%x", sha256.Sum256(encoded))
}
func (s *packageBindingState) declare(path string, value json.RawMessage) {
	if packageValueHash(s.Declarations[path]) != packageValueHash(value) { delete(s.Receipts, path) }
	s.Declarations[path] = value
}
func (s *packageBindingState) declareValue(path string, value any) {
	encoded, _ := json.Marshal(value)
	s.declare(path, encoded)
}
func (s packageBindingState) exportDeclaration(path string, declaration, actual json.RawMessage) json.RawMessage {
	receipt, confirmed := s.Receipts[path]
	if !confirmed || s.ready(path,declaration,actual) { return declaration }
	// After a manual change, export the current business state. Preserve aliases
	// only for resources whose verified identity remains in the saved mapping.
	var value any
	if json.Unmarshal(actual,&value) != nil { return actual }
	aliases := map[string]string{}
	for logical, target := range receipt.Mappings { aliases[target] = logical }
	var replace func(any)
	replace = func(v any) { switch item := v.(type) {
	case map[string]any:
		for key, child := range item { if key == "ref" || key == "runtime_ref" { if ref,ok := child.(string); ok && aliases[ref] != "" { item[key] = aliases[ref] } } else { replace(child) } }
	case []any: for _, child := range item { replace(child) }
	} }
	replace(value)
	encoded, err := json.Marshal(value); if err != nil { return actual }; return encoded
}
func (s packageBindingState) ready(path string, declaration, actual json.RawMessage) bool {
	// An explicitly empty plugin selection has no identities to authorize.
	// Keep unavailable/null state and all nonempty selections receipt-gated.
	if path == "/dsh_plugins" {
		var wanted, configured []json.RawMessage
		if json.Unmarshal(declaration,&wanted) == nil && wanted != nil && len(wanted) == 0 && json.Unmarshal(actual,&configured) == nil && configured != nil && len(configured) == 0 { return true }
	}
	receipt, exists := s.Receipts[path]
	return exists && receipt.Declaration == packageValueHash(declaration) && packageValueHash(receipt.Actual) == packageValueHash(actual)
}
func packageJSONPointer(path, key string) string { return path + "/" + strings.ReplaceAll(strings.ReplaceAll(key, "~", "~0"), "/", "~1") }
func walkPackageSecrets(value any, path string, visit func(string, string)) {
	switch item := value.(type) {
	case map[string]any:
		if ref, ok := item["secret_ref"].(string); ok { visit(path, ref); return }
		for key, child := range item { walkPackageSecrets(child, packageJSONPointer(path, key), visit) }
	case []any:
		for i, child := range item { walkPackageSecrets(child, fmt.Sprintf("%s/%d", path, i), visit) }
	}
}
func (s *packageBindingState) captureSecrets(configuration json.RawMessage) {
	var root map[string]any
	if json.Unmarshal(configuration, &root) != nil { return }
	// Omitted configuration fields keep their previous aliases. A supplied
	// field replaces that field's references, including explicit empty/null.
	for key := range root {
		prefix := packageJSONPointer("/configuration", key)
		for path := range s.SecretRefs { if path == prefix || strings.HasPrefix(path, prefix + "/") { delete(s.SecretRefs, path) } }
	}
	walkPackageSecrets(root, "/configuration", func(path, ref string) { s.SecretRefs[path] = ref })
}
