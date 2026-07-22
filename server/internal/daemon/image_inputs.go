package daemon

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/agent"
)

type imageAttachmentClient interface {
	GetJSON(context.Context, string, any) error
	DownloadFile(context.Context, string) ([]byte, error)
}

const maxNativeImageBytes = 20 << 20

// materializeChatImages downloads image attachments with the task-scoped API
// credential before any provider starts. This makes the provider contract
// deterministic: Hermes, Pi, and OpenCode all receive the same authenticated
// bytes instead of each agent deciding whether to run the attachment CLI.
func materializeChatImages(ctx context.Context, client imageAttachmentClient, attachments []ChatAttachmentMeta, taskTempDir string) ([]agent.InputImage, error) {
	images := make([]ChatAttachmentMeta, 0, len(attachments))
	for _, attachment := range attachments {
		if strings.HasPrefix(strings.ToLower(strings.TrimSpace(attachment.ContentType)), "image/") {
			images = append(images, attachment)
		}
	}
	if len(images) == 0 {
		return nil, nil
	}
	if client == nil {
		return nil, errors.New("image attachment client is not configured")
	}

	root := filepath.Join(taskTempDir, "input-images")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create image input directory: %w", err)
	}

	inputs := make([]agent.InputImage, 0, len(images))
	for index, attachment := range images {
		attachmentID := strings.TrimSpace(attachment.ID)
		if attachmentID == "" {
			return nil, errors.New("image attachment id is empty")
		}

		var metadata cli.AttachmentResponse
		if err := client.GetJSON(ctx, "/api/attachments/"+url.PathEscape(attachmentID), &metadata); err != nil {
			return nil, fmt.Errorf("get image attachment %s metadata: %w", attachmentID, err)
		}
		contentType := strings.ToLower(strings.TrimSpace(metadata.ContentType))
		if !strings.HasPrefix(contentType, "image/") {
			return nil, fmt.Errorf("attachment %s content type %q is not an image", attachmentID, metadata.ContentType)
		}
		downloadURL := strings.TrimSpace(metadata.DownloadURL)
		if downloadURL == "" {
			return nil, fmt.Errorf("image attachment %s has no download URL", attachmentID)
		}
		filename := filepath.Base(strings.ReplaceAll(strings.TrimSpace(metadata.Filename), "\\", "/"))
		if filename == "" || filename == "." || filename == string(filepath.Separator) {
			return nil, fmt.Errorf("image attachment %s has no safe filename", attachmentID)
		}

		data, err := client.DownloadFile(ctx, downloadURL)
		if err != nil {
			return nil, fmt.Errorf("download image attachment %s: %w", attachmentID, err)
		}
		if len(data) > maxNativeImageBytes {
			return nil, fmt.Errorf("image attachment %s is %d bytes; native image input limit is %d bytes", attachmentID, len(data), maxNativeImageBytes)
		}
		dir := filepath.Join(root, fmt.Sprintf("%02d", index+1))
		if err := os.Mkdir(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create image attachment directory: %w", err)
		}
		path := filepath.Join(dir, filename)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, fmt.Errorf("write image attachment %s: %w", attachmentID, err)
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve image attachment %s path: %w", attachmentID, err)
		}
		inputs = append(inputs, agent.InputImage{Path: absPath, Name: filename, ContentType: contentType})
	}
	return inputs, nil
}
