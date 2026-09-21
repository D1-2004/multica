package dshplugin

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParseSourceAcceptsEveryAdapterShape(t *testing.T) {
	cases := []struct {
		spec    string
		kind    SourceKind
		name    string
		version string
	}{
		{"dsh-mcp-lens", SourceNPM, "dsh-mcp-lens", ""},
		{"npm:dsh-mcp-lens@0.1.0-rc.9", SourceNPM, "dsh-mcp-lens", "0.1.0-rc.9"},
		{"@scope/plugin@1.2.3", SourceNPM, "@scope/plugin", "1.2.3"},
		{"@scope/plugin", SourceNPM, "@scope/plugin", ""},
		{"github:owner/repo#v1.0.0", SourceGitHub, "", ""},
		{"https://example.com/p.tgz", SourceURL, "", ""},
		{"file:/tmp/p.tgz", SourceFile, "", ""},
	}
	for _, tc := range cases {
		got, err := ParseSource(tc.spec)
		if err != nil {
			t.Fatalf("ParseSource(%q): %v", tc.spec, err)
		}
		if got.Kind != tc.kind {
			t.Errorf("ParseSource(%q).Kind = %q, want %q", tc.spec, got.Kind, tc.kind)
		}
		if got.Name != tc.name {
			t.Errorf("ParseSource(%q).Name = %q, want %q", tc.spec, got.Name, tc.name)
		}
		if got.Version != tc.version {
			t.Errorf("ParseSource(%q).Version = %q, want %q", tc.spec, got.Version, tc.version)
		}
	}
}

func TestParseSourceRejectsUnpinnedGitHubAndPlainHTTP(t *testing.T) {
	for _, spec := range []string{"github:owner/repo", "http://example.com/p.tgz", "file:relative.tgz", ""} {
		if _, err := ParseSource(spec); err == nil {
			t.Errorf("ParseSource(%q) accepted a source it should refuse", spec)
		}
	}
}

// The real dsh-mcp-lens patch: it uses DSH's `!!js` tag and names a row that
// differs from the package name. A strict YAML decode throws on the tag, which
// would have made every such plugin unimportable.
const mcpLensPatch = `
- insert:
    - id: mcp-lens
      name: dsh-mcp-lens
      config:
        servers: []
        cachePath: !!js dshHomePath('mcp-lens/catalog.json')
        id: this-is-config-not-a-row
`

func TestBundleRowIDsToleratesTheJsTagAndIgnoresConfig(t *testing.T) {
	rows, err := BundleRowIDs([]byte(mcpLensPatch))
	if err != nil {
		t.Fatalf("BundleRowIDs: %v", err)
	}
	if len(rows) != 1 || rows[0] != "mcp-lens" {
		t.Fatalf("rows = %v, want exactly [mcp-lens]", rows)
	}
}

func TestBundleRowIDsWalksNestedGroups(t *testing.T) {
	rows, err := BundleRowIDs([]byte(`
- insert:
    - id: outer
      group: true
      config:
        - id: inner-a
        - id: inner-b
`))
	if err != nil {
		t.Fatalf("BundleRowIDs: %v", err)
	}
	want := map[string]bool{"outer": true, "inner-a": true, "inner-b": true}
	if len(rows) != len(want) {
		t.Fatalf("rows = %v, want %d entries", rows, len(want))
	}
	for _, row := range rows {
		if !want[row] {
			t.Errorf("unexpected row %q", row)
		}
	}
}

func TestDeclaredEntryFollowsNodeResolution(t *testing.T) {
	cases := []struct {
		name     string
		manifest string
		want     string
		wantErr  string
	}{
		{"main only", `{"main":"lib/index.js"}`, "lib/index.js", ""},
		{"main with a leading dot slash", `{"main":"./lib/index.js"}`, "lib/index.js", ""},
		{"exports as a bare string", `{"exports":"./lib/index.js"}`, "lib/index.js", ""},
		{"root export wins over main", `{"main":"other.js","exports":{".":"./lib/index.js"}}`, "lib/index.js", ""},
		{"conditional root export", `{"exports":{".":{"import":"./dist/x.js"}}}`, "dist/x.js", ""},
		{"types is never the entry", `{"exports":{".":{"types":"./x.d.ts","default":"./lib/index.js"}}}`, "lib/index.js", ""},
		{
			// Node matches conditions in declaration order, so a package that
			// lists default first gets default — ranking import higher would
			// validate a file the runtime never loads.
			"declaration order decides between conditions",
			`{"exports":{".":{"default":"./lib/index.js","import":"./esm.js"}}}`,
			"lib/index.js", "",
		},
		{"array is a fallback list", `{"exports":{".":["./first.js","./second.js"]}}`, "first.js", ""},
		{"nothing declared", `{}`, "", ""},
		{
			// Subpaths but no ".": importing the package by name fails, so
			// falling back to main would validate an unreachable file.
			"subpath exports without a root export",
			`{"main":"lib/index.js","exports":{"./sub":"./lib/sub.js"}}`,
			"", "no root export",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var manifest packageManifest
			if err := json.Unmarshal([]byte(tc.manifest), &manifest); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			got, err := declaredEntry(manifest)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("error = %v, want it to mention %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("declaredEntry: %v", err)
			}
			if got != tc.want {
				t.Errorf("declaredEntry = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBundleRowIDsReportsTheFinalTree(t *testing.T) {
	// A later patch replaces the group's children wholesale, so the row that
	// exists afterwards is `new` — offering `old` would point configuration at
	// a row the loader never registers.
	rows, err := BundleRowIDs([]byte(`
- insert:
    - id: outer
      name: demo-plugin
      group: true
      config:
        - id: old
- id: outer
  name: demo-plugin
  config:
    - id: new
`))
	if err != nil {
		t.Fatalf("BundleRowIDs: %v", err)
	}
	joined := strings.Join(rows, ",")
	if joined != "outer,new" {
		t.Fatalf("rows = %v, want [outer new]", rows)
	}
}

func TestBundleRowIDsAppendsAnInsertIntoAGroup(t *testing.T) {
	rows, err := BundleRowIDs([]byte(`
- insert:
    - id: outer
      group: true
      config: []
- id: outer
  insert:
    - id: child
`))
	if err != nil {
		t.Fatalf("BundleRowIDs: %v", err)
	}
	if strings.Join(rows, ",") != "outer,child" {
		t.Fatalf("rows = %v, want [outer child]", rows)
	}
}

func TestBundleRowIDsHonoursTheNameGuard(t *testing.T) {
	// The loader skips a patch whose `name` does not match the target, so a
	// mismatched override must not change the tree.
	rows, err := BundleRowIDs([]byte(`
- insert:
    - id: outer
      name: demo-plugin
      group: true
      config:
        - id: kept
- id: outer
  name: someone-else
  config:
    - id: ignored
`))
	if err != nil {
		t.Fatalf("BundleRowIDs: %v", err)
	}
	if strings.Join(rows, ",") != "outer,kept" {
		t.Fatalf("rows = %v, want [outer kept]", rows)
	}
}

// tarball builds a gzipped npm-shaped archive from a path -> content map.
func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		header := &tar.Header{
			Name:     "package/" + name,
			Mode:     0o644,
			Size:     int64(len(body)),
			Typeflag: tar.TypeReg,
		}
		if err := tw.WriteHeader(header); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func goodPackage() map[string]string {
	return map[string]string{
		"package.json": `{"name":"demo-plugin","version":"1.0.0","main":"lib/index.js",
			"description":"a demo","dsh":{"bundle":{"patch":"./cordis.patch.yml"}}}`,
		"lib/index.js":     "export default {}",
		"cordis.patch.yml": "- insert:\n    - id: demo-row\n      name: demo-plugin\n",
	}
}

// serveTarball stands in for the npm registry: the metadata document and the
// tarball it points at. The metadata publishes a real sha512, because an
// unverifiable download is refused rather than trusted.
func serveTarball(t *testing.T, name, version string, data []byte, integrity string) *httptest.Server {
	t.Helper()
	if integrity == "" {
		sum := sha512.Sum512(data)
		integrity = "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
	}
	mux := http.NewServeMux()
	var base string
	mux.HandleFunc("/tarball.tgz", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(data)
	})
	mux.HandleFunc("/"+name+"/"+version, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"name":    name,
			"version": version,
			"dist": map[string]string{
				"tarball":   base + "/tarball.tgz",
				"integrity": integrity,
			},
		})
	})
	server := httptest.NewServer(mux)
	base = server.URL
	t.Cleanup(server.Close)
	return server
}

// testResolver points at the fixture registry with the address guard removed.
// httptest binds loopback, which the guard blocks on purpose; TestSafeClient…
// covers the guard itself.
func testResolver(registry string) *Resolver {
	resolver := NewResolver(registry)
	resolver.HTTP = &http.Client{Timeout: 30 * time.Second}
	return resolver
}

func resolveFixture(t *testing.T, files map[string]string) (*Resolved, error) {
	t.Helper()
	data := tarball(t, files)
	server := serveTarball(t, "demo-plugin", "1.0.0", data, "")
	src, err := ParseSource("npm:demo-plugin@1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	return testResolver(server.URL).Resolve(context.Background(), src)
}

func TestResolveRefusesAMismatchedPublishedDigest(t *testing.T) {
	// A mirror that serves different bytes than the version published must be
	// refused on the FIRST fetch; recomputing a digest over whatever arrived
	// would happily pin the wrong package.
	data := tarball(t, goodPackage())
	wrong := sha512.Sum512([]byte("not this package"))
	server := serveTarball(t, "demo-plugin", "1.0.0", data,
		"sha512-"+base64.StdEncoding.EncodeToString(wrong[:]))
	src, err := ParseSource("npm:demo-plugin@1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	_, err = testResolver(server.URL).Resolve(context.Background(), src)
	if err == nil || !strings.Contains(err.Error(), "digest the registry published") {
		t.Fatalf("error = %v, want a published-digest mismatch", err)
	}
}

func TestSafeClientRefusesNonPublicAddresses(t *testing.T) {
	for _, address := range []string{"127.0.0.1", "10.0.0.1", "192.168.1.1", "169.254.169.254", "100.64.0.1", "::1"} {
		if isPublicIP(netParse(t, address)) {
			t.Errorf("%s must not be treated as a public address", address)
		}
	}
	for _, address := range []string{"1.1.1.1", "93.184.216.34", "2606:4700:4700::1111"} {
		if !isPublicIP(netParse(t, address)) {
			t.Errorf("%s should be reachable", address)
		}
	}
}

func netParse(t *testing.T, value string) net.IP {
	t.Helper()
	ip := net.ParseIP(value)
	if ip == nil {
		t.Fatalf("bad test address %q", value)
	}
	return ip
}

func TestResolveAcceptsAPrebuiltBundle(t *testing.T) {
	resolved, err := resolveFixture(t, goodPackage())
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.PackageName != "demo-plugin" || resolved.Version != "1.0.0" {
		t.Errorf("identity = %s@%s", resolved.PackageName, resolved.Version)
	}
	if len(resolved.BundleRows) != 1 || resolved.BundleRows[0] != "demo-row" {
		t.Errorf("BundleRows = %v, want [demo-row]", resolved.BundleRows)
	}
	if resolved.Entry != "lib/index.js" {
		t.Errorf("Entry = %q", resolved.Entry)
	}
	if !strings.HasPrefix(resolved.Integrity, "sha256-") || len(resolved.Integrity) != 71 {
		t.Errorf("Integrity = %q, want sha256- plus 64 hex", resolved.Integrity)
	}
}

func TestResolveAcceptsLargeBundledEntryWithinArchiveBudget(t *testing.T) {
	files := goodPackage()
	files["lib/index.js"] = "/*" + strings.Repeat("x", 9<<20) + "*/"
	resolved, err := resolveFixture(t, files)
	if err != nil || resolved.Entry != "lib/index.js" {
		t.Fatalf("large prebuilt bundle rejected: %v", err)
	}
}

func TestArchiveStillRejectsOversizedMembersAndExpandedTotals(t *testing.T) {
	t.Run("single member", func(t *testing.T) {
		_, err := archiveFiles(tarball(t, map[string]string{"lib/index.js": strings.Repeat("x", (16<<20)+1)}))
		if err == nil || !strings.Contains(err.Error(), "file over") {
			t.Fatalf("member budget not enforced: %v", err)
		}
	})
	t.Run("expanded total", func(t *testing.T) {
		body := strings.Repeat("x", 14<<20)
		_, err := archiveFiles(tarball(t, map[string]string{"a.js": body, "b.js": body, "c.js": body, "d.js": body}))
		if err == nil {
			t.Fatal("expanded archive budget not enforced")
		}
	})
}

func TestResolveRefusesPackagesThatCannotBoot(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(map[string]string)
		want   string
	}{
		{
			name:   "no built output",
			mutate: func(f map[string]string) { delete(f, "lib/index.js") },
			want:   "ships no built output",
		},
		{
			name: "source snapshot that builds in prepare",
			mutate: func(f map[string]string) {
				delete(f, "lib/index.js")
				f["package.json"] = `{"name":"demo-plugin","version":"1.0.0","main":"lib/index.js",
					"scripts":{"prepare":"tsdown"},"dsh":{"bundle":{"patch":"./cordis.patch.yml"}}}`
			},
			want: "published npm release",
		},
		{
			name: "install script",
			mutate: func(f map[string]string) {
				f["package.json"] = `{"name":"demo-plugin","version":"1.0.0","main":"lib/index.js",
					"scripts":{"postinstall":"node evil.js"},"dsh":{"bundle":{"patch":"./cordis.patch.yml"}}}`
			},
			want: "postinstall script",
		},
		{
			name: "not a dsh bundle",
			mutate: func(f map[string]string) {
				f["package.json"] = `{"name":"demo-plugin","version":"1.0.0","main":"lib/index.js"}`
			},
			want: "dsh.bundle.patch",
		},
		{
			name:   "patch file missing",
			mutate: func(f map[string]string) { delete(f, "cordis.patch.yml") },
			want:   "not in the package",
		},
		{
			name: "patch inserts no row",
			mutate: func(f map[string]string) {
				f["cordis.patch.yml"] = "[]\n"
			},
			want: "inserts no loader row",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			files := goodPackage()
			tc.mutate(files)
			_, err := resolveFixture(t, files)
			if err == nil {
				t.Fatalf("Resolve accepted a package it should refuse")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestResolveRefusesAMismatchedPackageName(t *testing.T) {
	files := goodPackage()
	files["package.json"] = `{"name":"other-plugin","version":"1.0.0","main":"lib/index.js",
		"dsh":{"bundle":{"patch":"./cordis.patch.yml"}}}`
	_, err := resolveFixture(t, files)
	if err == nil || !strings.Contains(err.Error(), "when") {
		t.Fatalf("error = %v, want a name-mismatch refusal", err)
	}
}

func TestResolveFlagsAWebOnlyPlugin(t *testing.T) {
	files := goodPackage()
	files["package.json"] = `{"name":"demo-plugin","version":"1.0.0","main":"lib/index.js",
		"dsh":{"bundle":{"patch":"./cordis.patch.yml"},"client":{"platform":"web"}}}`
	resolved, err := resolveFixture(t, files)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !resolved.WebOnly {
		t.Fatal("a package declaring dsh.client should be flagged as web-only")
	}
	if len(resolved.Warnings) == 0 {
		t.Fatal("a web-only package should carry a warning for the operator")
	}
}

func TestArchiveFilesRefusesPathTraversal(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "x"
	_ = tw.WriteHeader(&tar.Header{Name: "../escape.js", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()
	if _, err := archiveFiles(buf.Bytes()); err == nil {
		t.Fatal("archiveFiles accepted a path that escapes the archive root")
	}
}

func TestArchiveFilesRefusesAGlobalHeader(t *testing.T) {
	// Go skips a PAX global header; Python's tarfile applies its `path`
	// override to every following member. The adapter unpacks with Python, so
	// one tarball could show the validator a different package.json than the
	// sandbox loads — with a matching digest, because the bytes are identical.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{
		Typeflag:   tar.TypeXGlobalHeader,
		Name:       "pax_global_header",
		PAXRecords: map[string]string{"path": "package/package.json"},
		Format:     tar.FormatPAX,
	})
	body := "{}"
	_ = tw.WriteHeader(&tar.Header{Name: "package/other.json", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.Close()
	_ = gz.Close()

	if _, err := archiveFiles(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "global header") {
		t.Fatalf("error = %v, want a global-header refusal", err)
	}
}

func TestArchiveFilesRefusesASecondRootDirectory(t *testing.T) {
	// A bare second top-level directory has no slash, so an implementation
	// that only checks prefixed paths lets it through — and then the adapter
	// refuses to boot, far from anyone who could read the error.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := "{}"
	_ = tw.WriteHeader(&tar.Header{Name: "package/package.json", Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte(body))
	_ = tw.WriteHeader(&tar.Header{Name: "extra/", Mode: 0o755, Typeflag: tar.TypeDir})
	_ = tw.Close()
	_ = gz.Close()

	if _, err := archiveFiles(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "more than one root") {
		t.Fatalf("error = %v, want a multiple-root refusal", err)
	}
}

func TestArchiveFilesRefusesAnOverLongName(t *testing.T) {
	// A PAX long name is stored as member data, so an unbounded name is an
	// unbounded allocation that no per-file content cap would notice.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	long := "package/" + strings.Repeat("a", maxMemberNameBytes+1)
	_ = tw.WriteHeader(&tar.Header{Name: long, Mode: 0o644, Size: 0, Typeflag: tar.TypeReg, Format: tar.FormatPAX})
	_ = tw.Close()
	_ = gz.Close()

	if _, err := archiveFiles(buf.Bytes()); err == nil || !strings.Contains(err.Error(), "over-long file name") {
		t.Fatalf("error = %v, want an over-long-name refusal", err)
	}
}
