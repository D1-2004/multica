package sitehosting

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type zipEntry struct {
	name string
	body string
	mode os.FileMode
}

func writeTestZIP(t *testing.T, entries ...zipEntry) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "site.zip")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(file)
	for _, entry := range entries {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		if entry.mode != 0 {
			header.SetMode(entry.mode)
		}
		writer, err := zw.CreateHeader(header)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.Write([]byte(entry.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestValidateArchiveAcceptsStaticSite(t *testing.T) {
	archivePath := writeTestZIP(t,
		zipEntry{name: "index.html", body: "<link rel=stylesheet href=assets/site.css>"},
		zipEntry{name: "assets/site.css", body: "body{color:#123}"},
		zipEntry{name: "assets/app.js", body: "console.log('ok')"},
	)

	manifest, err := ValidateArchive(archivePath, DefaultLimits(), "index.html")
	if err != nil {
		t.Fatalf("ValidateArchive: %v", err)
	}
	if len(manifest.Files) != 3 || manifest.TotalBytes == 0 {
		t.Fatalf("manifest=%#v", manifest)
	}
	if manifest.Files["index.html"].ContentType != "text/html; charset=utf-8" {
		t.Fatalf("index content type=%q", manifest.Files["index.html"].ContentType)
	}
}

func TestValidateArchiveRejectsUnsafePathsAndLinks(t *testing.T) {
	tests := []struct {
		name      string
		entry     zipEntry
		wantError string
	}{
		{name: "parent traversal", entry: zipEntry{name: "../bad.html", body: "bad"}, wantError: "unsafe archive path"},
		{name: "absolute path", entry: zipEntry{name: "/bad.html", body: "bad"}, wantError: "unsafe archive path"},
		{name: "windows drive absolute path", entry: zipEntry{name: "C:/bad.html", body: "bad"}, wantError: "unsafe archive path"},
		{name: "backslash", entry: zipEntry{name: `assets\bad.html`, body: "bad"}, wantError: "unsafe archive path"},
		{name: "encoded traversal", entry: zipEntry{name: "%2e%2e/bad.html", body: "bad"}, wantError: "unsafe archive path"},
		{name: "symlink", entry: zipEntry{name: "link", body: "target", mode: os.ModeSymlink | 0o777}, wantError: "not a regular file"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateArchive(writeTestZIP(t, zipEntry{name: "index.html", body: "ok"}, tc.entry), DefaultLimits(), "index.html")
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("unsafe archive error=%v want substring %q", err, tc.wantError)
			}
		})
	}
}

func TestValidateArchiveRejectsDuplicateSensitiveAndMissingEntrypoint(t *testing.T) {
	tests := []struct {
		name    string
		entries []zipEntry
	}{
		{name: "case conflict", entries: []zipEntry{{name: "index.html", body: "ok"}, {name: "INDEX.HTML", body: "bad"}}},
		{name: "unicode normalization conflict", entries: []zipEntry{{name: "index.html", body: "ok"}, {name: "caf\u00e9.js", body: "a"}, {name: "cafe\u0301.js", body: "b"}}},
		{name: "environment file", entries: []zipEntry{{name: "index.html", body: "ok"}, {name: ".env", body: "SECRET=x"}}},
		{name: "git internals", entries: []zipEntry{{name: "index.html", body: "ok"}, {name: ".git/config", body: "bad"}}},
		{name: "private key", entries: []zipEntry{{name: "index.html", body: "ok"}, {name: "deploy.pem", body: "-----BEGIN PRIVATE KEY-----"}}},
		{name: "disguised private key", entries: []zipEntry{{name: "index.html", body: "ok"}, {name: "notes.txt", body: "-----BEGIN OPENSSH PRIVATE KEY-----\nsecret"}}},
		{name: "credential file", entries: []zipEntry{{name: "index.html", body: "ok"}, {name: ".npmrc", body: "//registry/:_authToken=secret"}}},
		{name: "missing entrypoint", entries: []zipEntry{{name: "main.html", body: "no"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ValidateArchive(writeTestZIP(t, tc.entries...), DefaultLimits(), "index.html")
			if err == nil {
				t.Fatal("invalid archive was accepted")
			}
		})
	}
}

func TestValidateArchiveEnforcesFileAndExpandedSizeLimits(t *testing.T) {
	limits := DefaultLimits()
	limits.MaxFiles = 1
	_, err := ValidateArchive(writeTestZIP(t,
		zipEntry{name: "index.html", body: "ok"},
		zipEntry{name: "app.js", body: "x"},
	), limits, "index.html")
	if err == nil || !strings.Contains(err.Error(), "file count") {
		t.Fatalf("file count error=%v", err)
	}

	limits = DefaultLimits()
	limits.MaxFileBytes = 32
	_, err = ValidateArchive(writeTestZIP(t, zipEntry{name: "index.html", body: strings.Repeat("x", 33)}), limits, "index.html")
	if err == nil || !strings.Contains(err.Error(), "file size") {
		t.Fatalf("file size error=%v", err)
	}

	limits = DefaultLimits()
	limits.MaxExpandedBytes = 64
	_, err = ValidateArchive(writeTestZIP(t,
		zipEntry{name: "index.html", body: strings.Repeat("a", 40)},
		zipEntry{name: "app.js", body: strings.Repeat("b", 40)},
	), limits, "index.html")
	if err == nil || !strings.Contains(err.Error(), "expanded size") {
		t.Fatalf("expanded size error=%v", err)
	}
}

func TestValidateArchiveReadsViaReaderAt(t *testing.T) {
	archivePath := writeTestZIP(t, zipEntry{name: "index.html", body: strings.Repeat("x", 256*1024)})
	manifest, err := ValidateArchive(archivePath, DefaultLimits(), "index.html")
	if err != nil {
		t.Fatal(err)
	}
	var copied bytes.Buffer
	if err := StreamArchiveFile(archivePath, manifest.Files["index.html"], &copied); err != nil {
		t.Fatal(err)
	}
	if copied.Len() != 256*1024 {
		t.Fatalf("copied=%d", copied.Len())
	}
}
