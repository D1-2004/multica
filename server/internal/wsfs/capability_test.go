package wsfs

import "testing"

func TestDeclaresSharedDisk(t *testing.T) {
	if DeclaresSharedDisk(nil) || DeclaresSharedDisk([]byte(`{"kind":"fc-e2b"}`)) {
		t.Fatal("missing capability must skip shared disk")
	}
	if DeclaresSharedDisk([]byte(`{"capabilities":["dws"]}`)) {
		t.Fatal("another capability is not shared disk")
	}
	if !DeclaresSharedDisk([]byte(`{"capabilities":["workspace_shared_disk"]}`)) {
		t.Fatal("declared capability was ignored")
	}
	if DeclaresSharedDisk([]byte(`not-json`)) {
		t.Fatal("unreadable metadata must not count as capable")
	}
}
