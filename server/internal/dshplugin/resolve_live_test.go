package dshplugin

import (
	"context"
	"os"
	"testing"
)

// TestResolveLivePublishedPlugin runs the resolver against a real published
// plugin. It is the same package the runtime adapter is verified against, and
// it exercises the two things fixtures cannot prove on their own: that the
// registry's own metadata shape parses, and that a real `!!js` patch yields a
// row id which differs from the package name.
//
// Network-gated so the default suite stays hermetic:
//
//	MULTICA_RUN_LIVE_REGISTRY_TESTS=1 go test ./internal/dshplugin -run Live -v
func TestResolveLivePublishedPlugin(t *testing.T) {
	if os.Getenv("MULTICA_RUN_LIVE_REGISTRY_TESTS") != "1" {
		t.Skip("set MULTICA_RUN_LIVE_REGISTRY_TESTS=1 to reach the npm registry")
	}
	src, err := ParseSource("npm:dsh-mcp-lens@0.1.0-rc.9")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := NewResolver("").Resolve(context.Background(), src)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.PackageName != "dsh-mcp-lens" || resolved.Version != "0.1.0-rc.9" {
		t.Errorf("identity = %s@%s", resolved.PackageName, resolved.Version)
	}
	// The row the bundle declares is `mcp-lens`, not the package name.
	found := false
	for _, row := range resolved.BundleRows {
		if row == "mcp-lens" {
			found = true
		}
	}
	if !found {
		t.Errorf("BundleRows = %v, want it to contain mcp-lens", resolved.BundleRows)
	}
	const want = "sha256-a8e4bf8389d0107379c13c845feb3c7c0c26d4aa3312391640e1fed074d39dbc"
	if resolved.Integrity != want {
		t.Errorf("Integrity = %q, want %q", resolved.Integrity, want)
	}
	t.Logf("entry=%s rows=%v size=%d warnings=%v",
		resolved.Entry, resolved.BundleRows, resolved.SizeBytes, resolved.Warnings)
}
