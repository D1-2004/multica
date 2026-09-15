package handler

import (
 "bytes"
 "context"
 "errors"
 "fmt"
 "net/http"
 "net/http/httptest"
 "strings"
 "testing"
)

func TestFetchFromClawHub_ContextCancelledMidDownloadAborts(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	slug := "review-helper"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v1/skills/"+slug:
			writeJSON(w, http.StatusOK, map[string]any{
				"skill": map[string]any{
					"slug": slug, "displayName": slug, "summary": "s",
					"tags": map[string]string{"latest": "1.0.0"},
				},
			})
		case r.URL.Path == "/api/v1/skills/"+slug+"/versions/1.0.0":
			writeJSON(w, http.StatusOK, map[string]any{
				"version": map[string]any{
					"version": "1.0.0",
					"files": []map[string]any{
						{"path": "SKILL.md", "size": 16},
						{"path": "ref.md", "size": 8},
					},
				},
			})
		case r.URL.Path == "/api/v1/skills/"+slug+"/file":
			switch r.URL.Query().Get("path") {
			case "SKILL.md":
				w.Write([]byte("# Imported\n"))
			case "ref.md":
				cancel()
				<-r.Context().Done()
			default:
				http.NotFound(w, r)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	prev := clawHubAPIBase
	clawHubAPIBase = srv.URL + "/api/v1"
	t.Cleanup(func() { clawHubAPIBase = prev; srv.Close() })

	_, err := fetchFromClawHub(ctx, &http.Client{}, "https://clawhub.ai/acme/"+slug)
	if err == nil {
		t.Fatal("expected ClawHub import to abort on cancellation")
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %q, want context.Canceled", err.Error())
	}
}

func TestImportFetchErrorResponse(t *testing.T) {
	capErr := fmt.Errorf("%w: import bundle would contain 999 files", errImportCapExceeded)
	if status, _ := importFetchErrorResponse(context.Background(), capErr); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("cap error status = %d, want 413", status)
	}
	if status, _ := importFetchErrorResponse(context.Background(), context.DeadlineExceeded); status != http.StatusGatewayTimeout {
		t.Fatalf("deadline error status = %d, want 504", status)
	}
	unavailErr := fmt.Errorf("%w: could not read tree", errImportSourceUnavailable)
	if status, _ := importFetchErrorResponse(context.Background(), unavailErr); status != http.StatusServiceUnavailable {
		t.Fatalf("source-unavailable status = %d, want 503", status)
	}
	if status, _ := importFetchErrorResponse(context.Background(), fmt.Errorf("boom")); status != http.StatusBadGateway {
		t.Fatalf("generic error status = %d, want 502", status)
	}
}

func TestFetchRawFile_ReturnsErrorOnOversizedFile(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write(bytes.Repeat([]byte("a"), maxImportFileSize+1024))
	}))
	t.Cleanup(server.Close)

	_, err := fetchRawFile(t.Context(), &http.Client{}, server.URL+"/big.bin")
	if err == nil {
		t.Fatal("expected error for oversized file, got nil")
	}
	if !strings.Contains(err.Error(), "byte limit") {
		t.Fatalf("error = %q, want byte limit message", err.Error())
	}
	if !isCapError(err) {
		t.Fatalf("error %q must be classified as a cap error so callers fail-fast", err.Error())
	}
}

func TestImportedSkill_AddFileEnforcesBundleLimits(t *testing.T) {
	t.Run("file count", func(t *testing.T) {
		s := &importedSkill{}
		for i := 0; i < maxImportFileCount; i++ {
			if err := s.addFile("f", "x"); err != nil {
				t.Fatalf("addFile %d: %v", i, err)
			}
		}
		err := s.addFile("overflow", "x")
		if err == nil {
			t.Fatal("expected file count cap error")
		}
		if !isCapError(err) {
			t.Fatalf("error %q must be a cap error", err.Error())
		}
	})
	t.Run("total bytes", func(t *testing.T) {
		s := &importedSkill{}
		big := strings.Repeat("y", maxImportTotalSize)
		if err := s.addFile("a", big); err != nil {
			t.Fatalf("addFile at cap: %v", err)
		}
		err := s.addFile("b", "x")
		if err == nil {
			t.Fatal("expected total bytes cap error")
		}
		if !isCapError(err) {
			t.Fatalf("error %q must be a cap error", err.Error())
		}
	})
}
