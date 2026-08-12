package daemon

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/pkg/agent"
)

const maxA2ARuntimeFileBytes int64 = 100 << 20

type a2aAttachmentDownloader interface {
	DownloadA2AAttachment(context.Context, string, string, int64) ([]byte, error)
}

// materializeA2AInputAttachments resolves every protocol file through the
// daemon-authenticated server route into a task-private directory. The Agent
// receives local paths only; it never receives storage or Multica credentials.
func materializeA2AInputAttachments(
	ctx context.Context,
	client a2aAttachmentDownloader,
	taskID string,
	attachments []ChatAttachmentMeta,
	taskTempDir string,
	provider string,
) ([]ChatAttachmentMeta, []agent.InputImage, error) {
	if len(attachments) == 0 {
		return attachments, nil, nil
	}
	if len(attachments) > 32 {
		return nil, nil, errors.New("A2A input has more than 32 attachments")
	}
	if client == nil {
		return nil, nil, errors.New("A2A attachment downloader is not configured")
	}
	root := filepath.Join(taskTempDir, "a2a-inputs")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create A2A input directory: %w", err)
	}
	rootInfo, err := os.Lstat(root)
	if err != nil || !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return nil, nil, errors.New("A2A input directory is not a private directory")
	}
	result := append([]ChatAttachmentMeta(nil), attachments...)
	images := make([]agent.InputImage, 0)
	for index := range result {
		attachment := &result[index]
		if strings.TrimSpace(attachment.ID) == "" {
			return nil, nil, errors.New("A2A attachment id is empty")
		}
		if attachment.SizeBytes < 0 || attachment.SizeBytes > maxA2ARuntimeFileBytes {
			return nil, nil, fmt.Errorf("A2A attachment %s has an invalid size", attachment.ID)
		}
		filename := strings.TrimSpace(attachment.Filename)
		if filename == "" {
			filename = fmt.Sprintf("part-%02d.bin", index+1)
		} else if filename != attachment.Filename || strings.ContainsAny(filename, "/\\") ||
			filepath.Base(filename) != filename || filename == "." || filename == ".." {
			return nil, nil, fmt.Errorf("A2A attachment %s has an invalid filename", attachment.ID)
		}
		data, err := client.DownloadA2AAttachment(ctx, taskID, attachment.ID, maxA2ARuntimeFileBytes)
		if err != nil {
			return nil, nil, fmt.Errorf("download A2A attachment %s: %w", attachment.ID, err)
		}
		if attachment.SizeBytes > 0 && attachment.SizeBytes != int64(len(data)) {
			return nil, nil, fmt.Errorf("A2A attachment %s size mismatch", attachment.ID)
		}
		dir := filepath.Join(root, fmt.Sprintf("%02d", index+1))
		if err := os.Mkdir(dir, 0o700); err != nil {
			return nil, nil, fmt.Errorf("create A2A attachment directory: %w", err)
		}
		localPath := filepath.Join(dir, filename)
		file, err := os.OpenFile(localPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err != nil {
			return nil, nil, fmt.Errorf("write A2A attachment %s: %w", attachment.ID, err)
		}
		written, writeErr := file.Write(data)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil || written != len(data) {
			_ = os.Remove(localPath)
			return nil, nil, fmt.Errorf("write A2A attachment %s", attachment.ID)
		}
		attachment.Filename = filename
		attachment.LocalPath = localPath

		mediaType, _, _ := mime.ParseMediaType(strings.TrimSpace(attachment.ContentType))
		mediaType = strings.ToLower(mediaType)
		if providerSupportsNativeImageInput(provider) && (mediaType == "image/jpeg" || mediaType == "image/png") {
			if err := validateNativeImageBytes(mediaType, data); err != nil {
				return nil, nil, fmt.Errorf("A2A image attachment %s: %w", attachment.ID, err)
			}
			images = append(images, agent.InputImage{Name: filename, ContentType: mediaType, Path: localPath})
		}
	}
	return result, images, nil
}
