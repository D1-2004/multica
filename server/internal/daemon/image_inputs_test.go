package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/cli"
)

func TestMaterializeChatImagesDownloadsTaskAttachment(t *testing.T) {
	t.Parallel()

	mux := http.NewServeMux()
	mux.HandleFunc("/api/attachments/image-id", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer mat_test" {
			t.Fatalf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(cli.AttachmentResponse{
			ID:          "image-id",
			Filename:    "../screen.png",
			ContentType: "image/png",
			DownloadURL: "/download/image-id",
		})
	})
	mux.HandleFunc("/download/image-id", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("png-bytes"))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	inputs, err := materializeChatImages(
		context.Background(),
		cli.NewAPIClient(server.URL, "workspace-id", "mat_test"),
		[]ChatAttachmentMeta{
			{ID: "document-id", Filename: "notes.txt", ContentType: "text/plain"},
			{ID: "image-id", Filename: "screen.png", ContentType: "image/png"},
		},
		t.TempDir(),
	)
	if err != nil {
		t.Fatalf("materializeChatImages: %v", err)
	}
	if len(inputs) != 1 {
		t.Fatalf("inputs = %#v", inputs)
	}
	if inputs[0].Name != "screen.png" || inputs[0].ContentType != "image/png" {
		t.Fatalf("input metadata = %#v", inputs[0])
	}
	if filepath.Base(inputs[0].Path) != "screen.png" || strings.Contains(inputs[0].Path, "..") {
		t.Fatalf("unsafe input path = %q", inputs[0].Path)
	}
	data, err := os.ReadFile(inputs[0].Path)
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	if string(data) != "png-bytes" {
		t.Fatalf("image data = %q", data)
	}
	info, err := os.Stat(inputs[0].Path)
	if err != nil {
		t.Fatalf("stat image: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("image mode = %o, want 600", got)
	}
}

func TestMaterializeChatImagesRejectsNonImageMetadata(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cli.AttachmentResponse{
			ID:          "image-id",
			Filename:    "payload.txt",
			ContentType: "text/plain",
			DownloadURL: "/download/image-id",
		})
	}))
	defer server.Close()

	_, err := materializeChatImages(
		context.Background(),
		cli.NewAPIClient(server.URL, "workspace-id", "mat_test"),
		[]ChatAttachmentMeta{{ID: "image-id", Filename: "screen.png", ContentType: "image/png"}},
		t.TempDir(),
	)
	if err == nil || !strings.Contains(err.Error(), "is not an image") {
		t.Fatalf("error = %v, want content-type rejection", err)
	}
}
