package assoc

import (
	"strings"
	"testing"
)

func TestEdgeTouchSQLMergesProps(t *testing.T) {
	t.Parallel()
	if !strings.Contains(edgeTouchSQL, "assoc_edge.props") || !strings.Contains(edgeTouchSQL, "||") {
		t.Fatalf("edgeTouchSQL must shallow-merge jsonb, got %s", edgeTouchSQL)
	}
	if strings.Contains(edgeTouchSQL, "props = COALESCE($2") {
		t.Fatal("edgeTouchSQL must not overwrite props with COALESCE($2)")
	}
}

func TestRequireUUIDRejectsEmpty(t *testing.T) {
	t.Parallel()
	if _, err := requireUUID(""); err == nil {
		t.Fatal("expected error")
	}
	if _, err := requireUUID("not-a-uuid"); err == nil {
		t.Fatal("expected error")
	}
}
