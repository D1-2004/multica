package dshplugin

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"strings"
	"testing"
)

func zipArchive(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	for name, body := range files {
		entry, err := writer.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := entry.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func tarballFrom(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
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

const uploadManifest = `{"name":"demo-plugin","version":"1.0.0","main":"lib/index.js",
	"dsh":{"bundle":{"patch":"./cordis.patch.yml"}}}`
const uploadPatch = "- insert:\n    - id: demo-row\n      name: demo-plugin\n"

// The three shapes a package actually arrives in.
func TestNormalizeUploadAcceptsEveryRealLayout(t *testing.T) {
	cases := []struct {
		name     string
		filename string
		build    func(*testing.T) []byte
	}{
		{
			// `npm pack` output.
			name: "tarball with npm's package/ wrapper", filename: "demo.tgz",
			build: func(t *testing.T) []byte {
				return tarballFrom(t, map[string]string{
					"package/package.json":     uploadManifest,
					"package/lib/index.js":     "export default {}",
					"package/cordis.patch.yml": uploadPatch,
				})
			},
		},
		{
			// A hand-made zip of the package folder's contents.
			name: "zip with the manifest at the root", filename: "demo.zip",
			build: func(t *testing.T) []byte {
				return zipArchive(t, map[string]string{
					"package.json":     uploadManifest,
					"lib/index.js":     "export default {}",
					"cordis.patch.yml": uploadPatch,
				})
			},
		},
		{
			// GitHub's "Download ZIP" nests under <repo>-<ref>/.
			name: "zip nested under a repo directory", filename: "demo-main.zip",
			build: func(t *testing.T) []byte {
				return zipArchive(t, map[string]string{
					"demo-plugin-main/package.json":     uploadManifest,
					"demo-plugin-main/lib/index.js":     "export default {}",
					"demo-plugin-main/cordis.patch.yml": uploadPatch,
				})
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			normalized, err := NormalizeUpload(tc.build(t), tc.filename)
			if err != nil {
				t.Fatalf("NormalizeUpload: %v", err)
			}
			resolved, err := InspectUploaded(normalized)
			if err != nil {
				t.Fatalf("InspectUploaded: %v", err)
			}
			if resolved.PackageName != "demo-plugin" || resolved.Version != "1.0.0" {
				t.Errorf("identity = %s@%s", resolved.PackageName, resolved.Version)
			}
			if len(resolved.BundleRows) != 1 || resolved.BundleRows[0] != "demo-row" {
				t.Errorf("BundleRows = %v", resolved.BundleRows)
			}
			// Whatever went in, the sandbox sees npm's own layout.
			files, err := archiveFiles(normalized)
			if err != nil {
				t.Fatalf("archiveFiles: %v", err)
			}
			if _, ok := files["package.json"]; !ok {
				t.Errorf("normalised archive has no package.json at the package root")
			}
		})
	}
}

func TestNormalizeUploadIsDeterministic(t *testing.T) {
	// The same upload has to produce the same bytes, or the integrity digest
	// recorded at import would not match a later re-import of the same file.
	files := map[string]string{
		"package.json":     uploadManifest,
		"lib/index.js":     "export default {}",
		"cordis.patch.yml": uploadPatch,
	}
	first, err := NormalizeUpload(zipArchive(t, files), "demo.zip")
	if err != nil {
		t.Fatal(err)
	}
	second, err := NormalizeUpload(zipArchive(t, files), "demo.zip")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, second) {
		t.Fatal("the same upload produced different bytes")
	}
}

func TestNormalizeUploadPrefersTheShallowestManifest(t *testing.T) {
	// A bundled dependency carries its own package.json; the package being
	// imported is the shallowest one.
	normalized, err := NormalizeUpload(zipArchive(t, map[string]string{
		"package.json":                  uploadManifest,
		"cordis.patch.yml":              uploadPatch,
		"lib/index.js":                  "export default {}",
		"node_modules/dep/package.json": `{"name":"dep","version":"9.9.9"}`,
	}), "demo.zip")
	if err != nil {
		t.Fatalf("NormalizeUpload: %v", err)
	}
	resolved, err := InspectUploaded(normalized)
	if err != nil {
		t.Fatalf("InspectUploaded: %v", err)
	}
	if resolved.PackageName != "demo-plugin" {
		t.Fatalf("picked %q, want demo-plugin", resolved.PackageName)
	}
}

func TestNormalizeUploadRefusesWhatCannotBeAPackage(t *testing.T) {
	cases := []struct {
		name string
		data []byte
		file string
		want string
	}{
		{"empty", nil, "demo.zip", "empty"},
		{"not an archive", []byte("hello there"), "demo.zip", "not a zip archive"},
		{
			"no manifest anywhere",
			zipArchive(t, map[string]string{"readme.md": "# hi"}),
			"demo.zip", "no package.json",
		},
		{
			"manifest buried too deep",
			zipArchive(t, map[string]string{"a/b/c/package.json": uploadManifest}),
			"demo.zip", "too deeply",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := NormalizeUpload(tc.data, tc.file)
			if err == nil {
				t.Fatal("NormalizeUpload accepted something it should refuse")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error = %q, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestNormalizeUploadRefusesAnEscapingZipPath(t *testing.T) {
	var buf bytes.Buffer
	writer := zip.NewWriter(&buf)
	entry, err := writer.Create("../escape.js")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("x"))
	_ = writer.Close()

	if _, err := NormalizeUpload(buf.Bytes(), "demo.zip"); err == nil ||
		!strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("error = %v, want an unsafe-path refusal", err)
	}
}

func TestNormalizeUploadStillAppliesTheBootGates(t *testing.T) {
	// An upload is held to exactly the same rules as a registry package: the
	// point of normalising first is that there is only one set of gates.
	normalized, err := NormalizeUpload(zipArchive(t, map[string]string{
		"package.json": `{"name":"demo-plugin","version":"1.0.0","main":"lib/index.js"}`,
	}), "demo.zip")
	if err != nil {
		t.Fatalf("NormalizeUpload: %v", err)
	}
	if _, err := InspectUploaded(normalized); err == nil ||
		!strings.Contains(err.Error(), "dsh.bundle.patch") {
		t.Fatalf("error = %v, want a not-a-bundle refusal", err)
	}
}
