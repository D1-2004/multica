// Copyright (c) 2026 Nex.
// Copied and modified from gawkbot at 71e82a1809565281cbd0bf8185d3c125b715d934.
// See LICENSE and SOURCE_MAP.md for provenance and modification details.
package employeeloop

import "testing"

func makeTestTool(name string) Tool {
	return Tool{
		Name:        name,
		Description: "A test tool",
		Schema: map[string]any{
			"type":     "object",
			"required": []any{"query"},
			"properties": map[string]any{
				"query": map[string]any{"type": "string"},
				"limit": map[string]any{"type": "number"},
			},
		},
	}
}

func TestToolRegistry_RegisterAndGet(t *testing.T) {
	r := NewToolRegistry()
	tool := makeTestTool("search")
	r.Register(tool)

	got, ok := r.Get("search")
	if !ok {
		t.Fatal("expected tool to be found")
	}
	if got.Name != "search" {
		t.Errorf("expected name %q, got %q", "search", got.Name)
	}
}

func TestToolRegistry_Has(t *testing.T) {
	r := NewToolRegistry()
	r.Register(makeTestTool("search"))

	if !r.Has("search") {
		t.Error("expected Has to return true for registered tool")
	}
	if r.Has("missing") {
		t.Error("expected Has to return false for unregistered tool")
	}
}

func TestToolRegistry_Unregister(t *testing.T) {
	r := NewToolRegistry()
	r.Register(makeTestTool("search"))
	r.Unregister("search")

	if r.Has("search") {
		t.Error("expected tool to be removed after Unregister")
	}
}

func TestToolRegistry_List(t *testing.T) {
	r := NewToolRegistry()
	r.Register(makeTestTool("alpha"))
	r.Register(makeTestTool("beta"))

	list := r.List()
	if len(list) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(list))
	}
}

func TestToolRegistry_GetMissing(t *testing.T) {
	r := NewToolRegistry()
	_, ok := r.Get("nonexistent")
	if ok {
		t.Error("expected Get to return false for missing tool")
	}
}

func TestToolRegistry_Validate_UnknownTool(t *testing.T) {
	r := NewToolRegistry()
	ok, errs := r.Validate("nope", map[string]any{"query": "test"})
	if ok {
		t.Error("expected validation to fail for unknown tool")
	}
	if len(errs) == 0 {
		t.Error("expected error messages")
	}
}

func TestToolRegistry_Validate_MissingRequired(t *testing.T) {
	r := NewToolRegistry()
	r.Register(makeTestTool("search"))

	ok, errs := r.Validate("search", map[string]any{
		"limit": 10.0,
	})
	if ok {
		t.Error("expected validation to fail when required param missing")
	}
	found := false
	for _, e := range errs {
		if e == `missing required param: "query"` {
			found = true
		}
	}
	if !found {
		t.Errorf("expected missing-required error, got: %v", errs)
	}
}

func TestToolRegistry_Validate_UnknownParam(t *testing.T) {
	r := NewToolRegistry()
	r.Register(makeTestTool("search"))

	ok, errs := r.Validate("search", map[string]any{
		"query":   "hello",
		"unknown": "value",
	})
	if ok {
		t.Error("expected validation to fail with unknown param")
	}
	found := false
	for _, e := range errs {
		if e == `unknown param: "unknown"` {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unknown-param error, got: %v", errs)
	}
}

func TestToolRegistry_Validate_Valid(t *testing.T) {
	r := NewToolRegistry()
	r.Register(makeTestTool("search"))

	ok, errs := r.Validate("search", map[string]any{
		"query": "hello",
		"limit": 5.0,
	})
	if !ok {
		t.Errorf("expected validation to pass, got errors: %v", errs)
	}
}

func TestToolRegistry_Validate_RequiredOnly(t *testing.T) {
	r := NewToolRegistry()
	r.Register(makeTestTool("search"))

	ok, errs := r.Validate("search", map[string]any{
		"query": "test",
	})
	if !ok {
		t.Errorf("expected validation to pass with only required params, got: %v", errs)
	}
}

func TestToolRegistryValidateTypedRequiredAndStableList(t *testing.T) {
	r := NewToolRegistry()
	for _, name := range []string{"zebra", "alpha"} {
		tool := makeTestTool(name)
		tool.Schema["required"] = []string{"query"}
		r.Register(tool)
	}
	if ok, _ := r.Validate("alpha", map[string]any{}); ok {
		t.Fatal("typed required field was ignored")
	}
	list := r.List()
	if list[0].Name != "alpha" || list[1].Name != "zebra" {
		t.Fatalf("tool order unstable: %+v", list)
	}
}
