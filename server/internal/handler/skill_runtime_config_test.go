package handler

import (
	"reflect"
	"testing"
)

func TestMergeImportedSkillConfigPreservesExecutionPolicy(t *testing.T) {
	t.Parallel()

	imported := map[string]any{
		"origin": map[string]any{"type": "github", "url": "new"},
	}
	got := mergeImportedSkillConfig([]byte(`{
		"origin":{"type":"github","url":"old"},
		"execution":{"required_runtime_capabilities":["a1"]}
	}`), imported)
	want := map[string]any{
		"origin": map[string]any{"type": "github", "url": "new"},
		"execution": map[string]any{
			"required_runtime_capabilities": []any{"a1"},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("merged config = %#v, want %#v", got, want)
	}
	if len(imported) != 1 {
		t.Fatalf("input config was mutated: %#v", imported)
	}
}
