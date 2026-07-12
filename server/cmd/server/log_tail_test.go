package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLogFixture(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLogTailRequiresToken(t *testing.T) {
	dir := t.TempDir()
	writeLogFixture(t, dir, "backend.log", "line1\nline2\n")
	handler := logTailHandler("secret-token", dir)

	req := httptest.NewRequest(http.MethodGet, "/api/internal/logs/tail", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 without token, got %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/internal/logs/tail", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec = httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 with token, got %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "line2") {
		t.Fatalf("expected log content, got %q", rec.Body.String())
	}
}

func TestLogTailHiddenWhenUnconfigured(t *testing.T) {
	dir := t.TempDir()
	writeLogFixture(t, dir, "backend.log", "line1\n")

	// No token: proxied callers must get 404.
	handler := logTailHandler("", dir)
	req := httptest.NewRequest(http.MethodGet, "/api/internal/logs/tail", nil)
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	rec := httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for proxied caller without token, got %d", rec.Code)
	}

	// No log dir: endpoint disabled outright.
	handler = logTailHandler("secret", "")
	req = httptest.NewRequest(http.MethodGet, "/api/internal/logs/tail", nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec = httptest.NewRecorder()
	handler(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 when log dir unset, got %d", rec.Code)
	}
}

func TestLogTailFileWhitelistAndFilters(t *testing.T) {
	dir := t.TempDir()
	var b strings.Builder
	for i := 1; i <= 500; i++ {
		fmt.Fprintf(&b, "entry %d %s\n", i, map[bool]string{true: "dingtalk", false: "other"}[i%10 == 0])
	}
	writeLogFixture(t, dir, "backend.log", b.String())
	handler := logTailHandler("secret", dir)

	call := func(target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, target, nil)
		req.Header.Set("Authorization", "Bearer secret")
		rec := httptest.NewRecorder()
		handler(rec, req)
		return rec
	}

	if rec := call("/api/internal/logs/tail?file=../etc/passwd"); rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for non-whitelisted file, got %d", rec.Code)
	}
	if rec := call("/api/internal/logs/tail?file=frontend"); rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for missing whitelisted file, got %d", rec.Code)
	}

	rec := call("/api/internal/logs/tail?lines=3")
	got := strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(got) != 3 || !strings.HasPrefix(got[2], "entry 500") {
		t.Fatalf("expected last 3 lines ending at entry 500, got %v", got)
	}

	rec = call("/api/internal/logs/tail?contains=dingtalk&lines=5")
	got = strings.Split(strings.TrimSpace(rec.Body.String()), "\n")
	if len(got) != 5 {
		t.Fatalf("expected 5 filtered lines, got %d: %v", len(got), got)
	}
	for _, line := range got {
		if !strings.Contains(line, "dingtalk") {
			t.Fatalf("filter leaked non-matching line: %q", line)
		}
	}
	if !strings.HasPrefix(got[4], "entry 500") {
		t.Fatalf("expected newest filtered line to be entry 500, got %q", got[4])
	}
}
