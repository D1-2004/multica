// Copyright (c) 2026 Nex.
// Copied and modified from gawkbot at 71e82a1809565281cbd0bf8185d3c125b715d934.
// See LICENSE and SOURCE_MAP.md for provenance and modification details.
package employeeloop

import (
	"fmt"
	"sort"
)

// ToolRegistry manages a set of named Tools.
type ToolRegistry struct {
	tools map[string]Tool
}

// NewToolRegistry creates an empty registry.
func NewToolRegistry() *ToolRegistry {
	return &ToolRegistry{tools: make(map[string]Tool)}
}

// Register adds or replaces a tool in the registry.
func (r *ToolRegistry) Register(tool Tool) {
	r.tools[tool.Name] = tool
}

// Unregister removes a tool from the registry.
func (r *ToolRegistry) Unregister(name string) {
	delete(r.tools, name)
}

// Get looks up a tool by name.
func (r *ToolRegistry) Get(name string) (Tool, bool) {
	t, ok := r.tools[name]
	return t, ok
}

// List returns all registered tools.
func (r *ToolRegistry) List() []Tool {
	tools := make([]Tool, 0, len(r.tools))
	for _, t := range r.tools {
		tools = append(tools, t)
	}
	sort.Slice(tools, func(i, j int) bool { return tools[i].Name < tools[j].Name })
	return tools
}

// Has reports whether a tool with the given name is registered.
func (r *ToolRegistry) Has(name string) bool {
	_, ok := r.tools[name]
	return ok
}

// Validate checks whether params are valid for the named tool.
// Checks: tool exists, required params present, no unknown params.
// Returns (true, nil) on success; (false, []errors) on failure.
func (r *ToolRegistry) Validate(toolName string, params map[string]any) (bool, []string) {
	tool, ok := r.tools[toolName]
	if !ok {
		return false, []string{fmt.Sprintf("unknown tool: %q", toolName)}
	}

	props := map[string]any{}
	if p, ok := tool.Schema["properties"].(map[string]any); ok {
		props = p
	}

	var errs []string

	var required []string
	switch req := tool.Schema["required"].(type) {
	case []string:
		required = req
	case []any:
		for _, v := range req {
			if name, ok := v.(string); ok {
				required = append(required, name)
			}
		}
	}
	for _, name := range required {
		if _, present := params[name]; !present {
			errs = append(errs, fmt.Sprintf("missing required param: %q", name))
		}
	}

	for k := range params {
		if _, known := props[k]; !known {
			errs = append(errs, fmt.Sprintf("unknown param: %q", k))
		}
	}

	sort.Strings(errs)
	if len(errs) > 0 {
		return false, errs
	}
	return true, nil
}

// ValidateBatch rejects known terminal conflicts before executing any Host tool.
// Reads may accompany terminals, and multiple effect calls retain their existing
// per-call commit semantics. A no-effect terminal cannot share an effect batch.
func (r *ToolRegistry) ValidateBatch(calls []ToolCall) error {
	var terminal Disposition
	hasEffect := false
	for _, call := range calls {
		tool, ok := r.Get(call.Name)
		if !ok {
			continue
		}
		if tool.Exclusive && len(calls) != 1 {
			return ErrTerminalConflict
		}
		hasEffect = hasEffect || tool.Effect
		if !tool.Effect && tool.Terminal != "" {
			if terminal != "" && terminal != tool.Terminal {
				return ErrTerminalConflict
			}
			terminal = tool.Terminal
		}
	}
	if hasEffect && terminal != "" {
		return ErrTerminalConflict
	}
	return nil
}
