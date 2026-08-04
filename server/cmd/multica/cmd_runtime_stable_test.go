package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

func newRuntimeStableTestCmd(serverURL string) *cobra.Command {
	cmd := &cobra.Command{Use: "stable-test"}
	cmd.Flags().String("server-url", "", "")
	cmd.Flags().String("workspace-id", "", "")
	cmd.Flags().String("profile", "", "")
	cmd.Flags().String("backend", "asb", "")
	cmd.Flags().String("output", "json", "")
	_ = cmd.Flags().Set("server-url", serverURL)
	_ = cmd.Flags().Set("workspace-id", "ws-1")
	return cmd
}

func TestRuntimeStableCommandsRemainAvailable(t *testing.T) {
	for _, path := range [][]string{
		{"stable", "channel"},
		{"stable", "runtimes"},
		{"stable", "release", "list"},
		{"stable", "release", "create"},
		{"stable", "release", "advance-rollout"},
		{"stable", "release", "rollback"},
	} {
		if _, _, err := runtimeCmd.Find(path); err != nil {
			t.Fatalf("runtime %v command is unavailable: %v", path, err)
		}
	}
}

func TestRunRuntimeStableReleaseCreateSendsIdempotencyKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/runtimes/cloud-sandbox/stable-releases" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Idempotency-Key"); got != "release-build-42" {
			t.Fatalf("Idempotency-Key = %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		for key, want := range map[string]string{
			"sandbox_backend":   "asb",
			"artifact_ref":      "registry.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"artifact_build_id": "42",
			"artifact_built_at": "2026-07-30T20:34:18+08:00",
			"artifact_digest":   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			"git_commit":        "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		} {
			if got := body[key]; got != want {
				t.Fatalf("%s = %#v, want %q", key, got, want)
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":              "release-1",
			"sandbox_backend": "asb",
			"status":          "validating",
		})
	}))
	defer srv.Close()

	cmd := newRuntimeStableTestCmd(srv.URL)
	for name, value := range map[string]string{
		"artifact-ref":      "registry.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"artifact-build-id": "42",
		"artifact-built-at": "2026-07-30T20:34:18+08:00",
		"artifact-digest":   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"git-commit":        "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"idempotency-key":   "release-build-42",
	} {
		cmd.Flags().String(name, "", "")
		_ = cmd.Flags().Set(name, value)
	}
	cmd.Flags().String("template-id", "", "")
	cmd.Flags().String("expected-build-id", "", "")
	cmd.Flags().String("note", "", "")

	out, err := captureRuntimeStdout(t, func() error {
		return runRuntimeStableReleaseCreate(cmd, nil)
	})
	if err != nil {
		t.Fatalf("runRuntimeStableReleaseCreate: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if got["id"] != "release-1" || got["status"] != "validating" {
		t.Fatalf("output = %#v", got)
	}
}

func TestRunRuntimeStableReleaseListIncludesBackendAndFilters(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("MULTICA_TOKEN", "test-token")

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/runtimes/cloud-sandbox/stable-releases" {
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.Path)
		}
		if got := r.URL.Query().Get("sandbox_backend"); got != "asb" {
			t.Fatalf("sandbox_backend = %q", got)
		}
		if got := r.URL.Query().Get("status"); got != "failed" {
			t.Fatalf("status = %q", got)
		}
		if got := r.URL.Query().Get("limit"); got != "7" {
			t.Fatalf("limit = %q", got)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{{
			"id":               "release-1",
			"sandbox_backend":  "asb",
			"status":           "failed",
			"validation_error": "sandbox readiness timeout",
		}})
	}))
	defer srv.Close()

	cmd := newRuntimeStableTestCmd(srv.URL)
	cmd.Flags().String("status", "", "")
	cmd.Flags().Int("limit", 20, "")
	_ = cmd.Flags().Set("status", "failed")
	_ = cmd.Flags().Set("limit", "7")
	out, err := captureRuntimeStdout(t, func() error {
		return runRuntimeStableReleaseList(cmd, nil)
	})
	if err != nil {
		t.Fatalf("runRuntimeStableReleaseList: %v", err)
	}
	var got []map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(got) != 1 || got[0]["id"] != "release-1" {
		t.Fatalf("output = %#v", got)
	}
}
