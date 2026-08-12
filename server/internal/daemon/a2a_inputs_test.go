package daemon

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

type stubA2AAttachmentDownloader struct {
	data map[string][]byte
	err  error
}

func (stub stubA2AAttachmentDownloader) DownloadA2AAttachment(_ context.Context, _, attachmentID string, _ int64) ([]byte, error) {
	if stub.err != nil {
		return nil, stub.err
	}
	return append([]byte(nil), stub.data[attachmentID]...), nil
}

func TestMaterializeA2AInputAttachmentsWritesPrivateFilesAndNativeImages(t *testing.T) {
	png, err := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNk+A8AAQUBAScY42YAAAAASUVORK5CYII=")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	attachments, images, err := materializeA2AInputAttachments(
		context.Background(),
		stubA2AAttachmentDownloader{data: map[string][]byte{"attachment-1": png}},
		"task-1",
		[]ChatAttachmentMeta{{ID: "attachment-1", Filename: "pixel.png", ContentType: "image/png", SizeBytes: int64(len(png))}},
		root,
		"opencode",
	)
	if err != nil {
		t.Fatalf("materializeA2AInputAttachments() error = %v", err)
	}
	if len(attachments) != 1 || len(images) != 1 || attachments[0].LocalPath == "" || images[0].Path != attachments[0].LocalPath {
		t.Fatalf("attachments=%#v images=%#v", attachments, images)
	}
	info, err := os.Stat(attachments[0].LocalPath)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("file mode = %o, want 600", got)
	}
	if filepath.Dir(filepath.Dir(attachments[0].LocalPath)) != filepath.Join(root, "a2a-inputs") {
		t.Fatalf("attachment escaped private root: %s", attachments[0].LocalPath)
	}
}

func TestMaterializeA2AInputAttachmentsRejectsUnsafePathsAndOverwrite(t *testing.T) {
	tests := []string{"../secret", "sub/file", `sub\\file`, ".", "..", " padded "}
	for _, filename := range tests {
		t.Run(filename, func(t *testing.T) {
			_, _, err := materializeA2AInputAttachments(
				context.Background(),
				stubA2AAttachmentDownloader{data: map[string][]byte{"attachment-1": []byte("data")}},
				"task-1",
				[]ChatAttachmentMeta{{ID: "attachment-1", Filename: filename, SizeBytes: 4}},
				t.TempDir(),
				"opencode",
			)
			if err == nil {
				t.Fatalf("unsafe filename %q was accepted", filename)
			}
		})
	}

	root := t.TempDir()
	download := stubA2AAttachmentDownloader{data: map[string][]byte{"attachment-1": []byte("data")}}
	input := []ChatAttachmentMeta{{ID: "attachment-1", Filename: "file.bin", SizeBytes: 4}}
	if _, _, err := materializeA2AInputAttachments(context.Background(), download, "task-1", input, root, "opencode"); err != nil {
		t.Fatalf("first materialization: %v", err)
	}
	if _, _, err := materializeA2AInputAttachments(context.Background(), download, "task-1", input, root, "opencode"); err == nil {
		t.Fatal("second materialization overwrote an existing task file")
	}
}

func TestMaterializeA2AInputAttachmentsRejectsSymlinkRootAndDownloadFailure(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "a2a-inputs")); err != nil {
		t.Fatal(err)
	}
	input := []ChatAttachmentMeta{{ID: "attachment-1", Filename: "file.bin", SizeBytes: 4}}
	if _, _, err := materializeA2AInputAttachments(
		context.Background(),
		stubA2AAttachmentDownloader{data: map[string][]byte{"attachment-1": []byte("data")}},
		"task-1", input, root, "opencode",
	); err == nil {
		t.Fatal("symlinked A2A input root was accepted")
	}

	if _, _, err := materializeA2AInputAttachments(
		context.Background(), stubA2AAttachmentDownloader{err: errors.New("denied")},
		"task-1", input, t.TempDir(), "opencode",
	); err == nil {
		t.Fatal("attachment download failure was ignored")
	}
}
