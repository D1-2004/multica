package service

import (
	"context"
	"strings"
	"testing"
)

func TestStaticGitAgentTemplateCatalog(t *testing.T) {
	catalog, err := NewStaticGitAgentTemplateCatalog(`[
  {"key":"z-template","display_name":"Z","description":"last","repository":"acme/z","ref":"main","enabled":false},
  {"key":"a-template","display_name":"A","description":"first","repository":"acme/a","ref":"v1","enabled":true}
]`)
	if err != nil {
		t.Fatal(err)
	}
	items := catalog.List(context.Background(), "workspace")
	if len(items) != 2 || items[0].Key != "a-template" || items[1].Key != "z-template" {
		t.Fatalf("unexpected sorted catalog: %#v", items)
	}
	got, err := catalog.Get(context.Background(), "workspace", "a-template")
	if err != nil || got.Repository != "acme/a" || !got.Enabled {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
}

func TestStaticGitAgentTemplateCatalogEmpty(t *testing.T) {
	catalog, err := NewStaticGitAgentTemplateCatalog("  ")
	if err != nil {
		t.Fatal(err)
	}
	if got := catalog.List(context.Background(), "workspace"); len(got) != 0 {
		t.Fatalf("List() = %#v, want empty", got)
	}
}

func TestStaticGitAgentTemplateCatalogRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want string
	}{
		{"duplicate", `[{"key":"same","display_name":"A","repository":"a/r","ref":"main","enabled":true},{"key":"same","display_name":"B","repository":"a/r2","ref":"main","enabled":true}]`, "duplicate key"},
		{"bad key", `[{"key":"Bad Key","display_name":"A","repository":"a/r","ref":"main","enabled":true}]`, "key must match"},
		{"bad repository", `[{"key":"a","display_name":"A","repository":"not-a-repo","ref":"main","enabled":true}]`, "repository must be"},
		{"missing ref", `[{"key":"a","display_name":"A","repository":"a/r","ref":"","enabled":true}]`, "ref is required"},
		{"unknown field", `[{"key":"a","display_name":"A","repository":"a/r","ref":"main","enabled":true,"secret":"x"}]`, "unknown field"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NewStaticGitAgentTemplateCatalog(tc.raw)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %v, want substring %q", err, tc.want)
			}
		})
	}
}
