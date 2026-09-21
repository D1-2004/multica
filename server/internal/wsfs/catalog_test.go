package wsfs

import "testing"

func TestJoinAndParentPath(t *testing.T) {
	if got := JoinRel("", "docs"); got != "docs" {
		t.Fatalf("root join: %q", got)
	}
	if got := JoinRel("docs", "rfc.md"); got != "docs/rfc.md" {
		t.Fatalf("nested join: %q", got)
	}
	if got := ParentPath("docs/rfc.md"); got != "docs" {
		t.Fatalf("parent: %q", got)
	}
	if got := ParentPath("docs"); got != "" {
		t.Fatalf("root parent: %q", got)
	}
	if got := CatalogParentKey("."); got != "" {
		t.Fatalf("jail root key: %q", got)
	}
}
