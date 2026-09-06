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

func TestDeclaredEntryPrefersMainThenExports(t *testing.T) {
	cases := []struct {
		manifest string
		want     string
	}{
		{`{"main":"lib/index.js"}`, "lib/index.js"},
		{`{"main":"./lib/index.js"}`, "lib/index.js"},
		{`{"exports":"./lib/index.js"}`, "lib/index.js"},
		{`{"exports":{".":{"import":"./dist/x.js"}}}`, "dist/x.js"},
		{`{"exports":{".":{"types":"./x.d.ts","default":"./lib/index.js"}}}`, "lib/index.js"},
		{`{}`, ""},
	}
	for _, tc := range cases {
		var manifest packageManifest
		if err := json.Unmarshal([]byte(tc.manifest), &manifest); err != nil {
			t.Fatalf("unmarshal %s: %v", tc.manifest, err)
		}
		if got := declaredEntry(manifest); got != tc.want {
			t.Errorf("declaredEntry(%s) = %q, want %q", tc.manifest, got, tc.want)
		}
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
