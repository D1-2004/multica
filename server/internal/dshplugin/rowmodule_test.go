package dshplugin

import (
	"strings"
	"testing"
)

// A package whose patch names a module the package neither is nor depends on
// installs cleanly and then dies at boot, and the error it dies with names Node
// internals rather than the plugin:
//
//	plugin tree failed to load: failed to apply loader entry include
//	(cordis:include): failed to import loader entry mcp-lens (dsh-mcp-lens):
//	Cannot find package 'dsh-mcp-lens' imported from .../multica-plugins/
//
// Observed on 预发 after a package was renamed in package.json without renaming
// cordis.patch.yml. The rename is the ordinary way this happens -- the two
// files agree in every published package and nothing but a failed task tells
// you when they stop agreeing.
func TestUnresolvableRowIsWarnedAboutAtImport(t *testing.T) {
	rows := []BundleRow{{ID: "mcp-lens", Module: "dsh-mcp-lens"}}
	manifest := packageManifest{Name: "dsh-mcp-lens-uploaded"}

	warnings := unresolvableRowWarnings(rows, manifest)
	if len(warnings) != 1 {
		t.Fatalf("warnings = %v, want exactly one", warnings)
	}
	for _, want := range []string{"mcp-lens", "dsh-mcp-lens", "fail to start"} {
		if !strings.Contains(warnings[0], want) {
			t.Errorf("warning %q does not mention %q", warnings[0], want)
		}
	}
}

func TestResolvableRowsAreNotWarnedAbout(t *testing.T) {
	cases := []struct {
		name     string
		rows     []BundleRow
		manifest packageManifest
	}{
		{
			name:     "the row imports the package itself",
			rows:     []BundleRow{{ID: "mcp-lens", Module: "dsh-mcp-lens"}},
			manifest: packageManifest{Name: "dsh-mcp-lens"},
		},
		{
			// A bundle may legitimately mount a module it ships alongside.
			name: "the row imports a declared dependency",
			rows: []BundleRow{{ID: "lens", Module: "@deepseek-ai/schemastery"}},
			manifest: packageManifest{
				Name:         "dsh-bundle",
				Dependencies: map[string]string{"@deepseek-ai/schemastery": "^1"},
			},
		},
		{
			name: "the row imports a peer dependency",
			rows: []BundleRow{{ID: "lens", Module: "@modelcontextprotocol/sdk"}},
			manifest: packageManifest{
				Name:             "dsh-bundle",
				PeerDependencies: map[string]string{"@modelcontextprotocol/sdk": "*"},
			},
		},
		{
			// A row with no name inherits the bundle's own module, so there is
			// no specifier to be wrong about.
			name:     "the row names no module",
			rows:     []BundleRow{{ID: "group"}},
			manifest: packageManifest{Name: "dsh-bundle"},
		},
		{
			// DSH resolves a path against the patch file, so a package that
			// ships the file needs to declare nothing.
			name:     "the row names a relative path",
			rows:     []BundleRow{{ID: "lens", Module: "./lib/index.js"}},
			manifest: packageManifest{Name: "dsh-bundle"},
		},
		{
			name:     "the row names an absolute path",
			rows:     []BundleRow{{ID: "lens", Module: "/opt/dsh/thing.js"}},
			manifest: packageManifest{Name: "dsh-bundle"},
		},
		{
			// The loader supplies cordis: itself, as the runtime supplies node:.
			name:     "the row names a loader built-in",
			rows:     []BundleRow{{ID: "group", Module: "cordis:group"}},
			manifest: packageManifest{Name: "dsh-bundle"},
		},
		{
			// A subpath needs its package half installed, and that half is the
			// package itself.
			name:     "the row names a subpath of this package",
			rows:     []BundleRow{{ID: "lens", Module: "dsh-mcp-lens/tools"}},
			manifest: packageManifest{Name: "dsh-mcp-lens"},
		},
		{
			name: "the row names a subpath of a scoped dependency",
			rows: []BundleRow{{ID: "lens", Module: "@deepseek-ai/schemastery/lib"}},
			manifest: packageManifest{
				Name:         "dsh-bundle",
				Dependencies: map[string]string{"@deepseek-ai/schemastery": "^1"},
			},
		},
		{
			// The loader returns before resolving a disabled row, so its module
			// never has to exist.
			name:     "the row is disabled",
			rows:     []BundleRow{{ID: "optional", Module: "absent-pkg", Disabled: true}},
			manifest: packageManifest{Name: "dsh-bundle"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := unresolvableRowWarnings(c.rows, c.manifest); len(got) != 0 {
				t.Errorf("warnings = %v, want none", got)
			}
		})
	}
}

// A subpath of a package that is genuinely absent is still a problem, and the
// warning names the specifier the author wrote rather than the package half.
func TestUnresolvableSubpathIsStillWarnedAbout(t *testing.T) {
	rows := []BundleRow{{ID: "lens", Module: "absent-pkg/tools"}}
	got := unresolvableRowWarnings(rows, packageManifest{Name: "dsh-bundle"})
	if len(got) != 1 {
		t.Fatalf("warnings = %v, want exactly one", got)
	}
	if !strings.Contains(got[0], "absent-pkg/tools") {
		t.Errorf("warning %q does not quote the specifier the author wrote", got[0])
	}
}

// Two rows importing the same missing module is one problem, not two.
func TestOneWarningPerMissingModule(t *testing.T) {
	rows := []BundleRow{
		{ID: "a", Module: "missing-pkg"},
		{ID: "b", Module: "missing-pkg"},
		{ID: "c", Module: "other-missing"},
	}
	got := unresolvableRowWarnings(rows, packageManifest{Name: "bundle"})
	if len(got) != 2 {
		t.Fatalf("warnings = %v, want two", got)
	}
}

// ComposeBundleRows must keep the module beside the id; BundleRowIDs, which the
// rest of the pipeline still calls, must be unchanged by that.
func TestComposeBundleRowsKeepsTheModuleName(t *testing.T) {
	composed, err := ComposeBundleRows([]byte(mcpLensPatch))
	if err != nil {
		t.Fatalf("ComposeBundleRows: %v", err)
	}
	if len(composed) != 1 {
		t.Fatalf("rows = %v, want exactly one", composed)
	}
	if composed[0].ID != "mcp-lens" || composed[0].Module != "dsh-mcp-lens" {
		t.Errorf("row = %+v, want {mcp-lens dsh-mcp-lens}", composed[0])
	}
}
