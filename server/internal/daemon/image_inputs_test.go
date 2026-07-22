package daemon

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
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
	pngData := testPNGBytes(t)

	mux := http.NewServeMux()
	mux.HandleFunc("/api/attachments/image-id", func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer mat_test" {
			t.Fatalf("Authorization = %q", got)
		}
		_ = json.NewEncoder(w).Encode(cli.AttachmentResponse{
			ID:          "image-id",
			Filename:    "../screen.dat",
			ContentType: "image/png",
			SizeBytes:   int64(len(pngData)),
			DownloadURL: "/download/image-id",
		})
	})
	mux.HandleFunc("/download/image-id", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(pngData)
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
	if !bytes.Equal(data, pngData) {
		t.Fatal("materialized image data changed")
	}
	info, err := os.Stat(inputs[0].Path)
	if err != nil {
		t.Fatalf("stat image: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("image mode = %o, want 600", got)
	}
}

func TestMaterializeChatImagesRejectsUnsupportedImageFormat(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(cli.AttachmentResponse{
			ID:          "image-id",
			Filename:    "photo.heic",
			ContentType: "image/heic",
			SizeBytes:   4,
			DownloadURL: "/download/image-id",
		})
	}))
	defer server.Close()

	_, err := materializeChatImages(
		context.Background(),
		cli.NewAPIClient(server.URL, "workspace-id", "mat_test"),
		[]ChatAttachmentMeta{{ID: "image-id", Filename: "photo.heic", ContentType: "image/heic"}},
		t.TempDir(),
	)
	if err == nil || !strings.Contains(err.Error(), "use JPEG or PNG") {
		t.Fatalf("error = %v, want unsupported-format rejection", err)
	}
}

func TestMaterializeChatImagesRejectsTooManyImagesBeforeDownload(t *testing.T) {
	t.Parallel()
	attachments := make([]ChatAttachmentMeta, maxNativeImageCount+1)
	for i := range attachments {
		attachments[i] = ChatAttachmentMeta{ID: "image", ContentType: "image/png"}
	}
	_, err := materializeChatImages(context.Background(), nil, attachments, t.TempDir())
	if err == nil || !strings.Contains(err.Error(), "native image count") {
		t.Fatalf("error = %v, want image count rejection", err)
	}
}

func TestProviderSupportsNativeImageInputIsExplicit(t *testing.T) {
	for _, provider := range []string{"hermes", "pi", "opencode"} {
		if !providerSupportsNativeImageInput(provider) {
			t.Errorf("provider %q must support native image input", provider)
		}
	}
	for _, provider := range []string{"", "claude", "codex", "qoder"} {
		if providerSupportsNativeImageInput(provider) {
			t.Errorf("provider %q must not be enabled implicitly", provider)
		}
	}
}

func testPNGBytes(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{R: 255, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
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
	if err == nil || !strings.Contains(err.Error(), "not supported") {
		t.Fatalf("error = %v, want content-type rejection", err)
	}
}
