package daemon

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image/jpeg"
	"image/png"
	"mime"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/agent"
)

type imageAttachmentClient interface {
	GetJSON(context.Context, string, any) error
	DownloadFileLimited(context.Context, string, int64) ([]byte, error)
}

const (
	maxNativeImageCount       = 4
	maxNativeImageBytes int64 = 10 << 20
	maxNativeImageTotal int64 = 20 << 20
)

func providerSupportsNativeImageInput(provider string) bool {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case "hermes", "pi", "opencode":
		return true
	default:
		return false
	}
}

type preparedImageAttachment struct {
	id          string
	filename    string
	contentType string
	sizeBytes   int64
	downloadURL string
}

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
	if len(images) > maxNativeImageCount {
		return nil, fmt.Errorf("native image count %d exceeds limit %d", len(images), maxNativeImageCount)
	}
	if client == nil {
		return nil, errors.New("image attachment client is not configured")
	}

	prepared := make([]preparedImageAttachment, 0, len(images))
	var declaredTotal int64
	for _, attachment := range images {
		attachmentID := strings.TrimSpace(attachment.ID)
		if attachmentID == "" {
			return nil, errors.New("image attachment id is empty")
		}

		var metadata cli.AttachmentResponse
		if err := client.GetJSON(ctx, "/api/attachments/"+url.PathEscape(attachmentID), &metadata); err != nil {
			return nil, fmt.Errorf("get image attachment %s metadata: %w", attachmentID, err)
		}
		contentType, _, err := mime.ParseMediaType(strings.TrimSpace(metadata.ContentType))
		if err != nil {
			return nil, fmt.Errorf("attachment %s has invalid content type", attachmentID)
		}
		contentType = strings.ToLower(contentType)
		if contentType != "image/jpeg" && contentType != "image/png" {
			return nil, fmt.Errorf("attachment %s content type %q is not supported for native image input; use JPEG or PNG", attachmentID, contentType)
		}
		downloadURL := strings.TrimSpace(metadata.DownloadURL)
		if downloadURL == "" {
			return nil, fmt.Errorf("image attachment %s has no download URL", attachmentID)
		}
		filename := filepath.Base(strings.ReplaceAll(strings.TrimSpace(metadata.Filename), "\\", "/"))
		if filename == "" || filename == "." || filename == string(filepath.Separator) {
			return nil, fmt.Errorf("image attachment %s has no safe filename", attachmentID)
		}
		filename = canonicalNativeImageFilename(filename, contentType)
		if metadata.SizeBytes < 0 {
			return nil, fmt.Errorf("image attachment %s has invalid size", attachmentID)
		}
		if metadata.SizeBytes > maxNativeImageBytes {
			return nil, fmt.Errorf("image attachment %s is %d bytes; per-image native input limit is %d bytes", attachmentID, metadata.SizeBytes, maxNativeImageBytes)
		}
		declaredTotal += metadata.SizeBytes
		if declaredTotal > maxNativeImageTotal {
			return nil, fmt.Errorf("declared native image total is %d bytes; limit is %d bytes", declaredTotal, maxNativeImageTotal)
		}
		prepared = append(prepared, preparedImageAttachment{
			id: attachmentID, filename: filename, contentType: contentType,
			sizeBytes: metadata.SizeBytes, downloadURL: downloadURL,
		})
	}

	root := filepath.Join(taskTempDir, "input-images")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create image input directory: %w", err)
	}

	inputs := make([]agent.InputImage, 0, len(prepared))
	var actualTotal int64
	for index, image := range prepared {
		remaining := maxNativeImageTotal - actualTotal
		downloadLimit := maxNativeImageBytes
		if remaining < downloadLimit {
			downloadLimit = remaining
		}
		if downloadLimit <= 0 {
			return nil, fmt.Errorf("native image total exceeds %d bytes", maxNativeImageTotal)
		}

		data, err := client.DownloadFileLimited(ctx, image.downloadURL, downloadLimit)
		if err != nil {
			return nil, fmt.Errorf("download image attachment %s: %w", image.id, err)
		}
		if image.sizeBytes > 0 && int64(len(data)) != image.sizeBytes {
			return nil, fmt.Errorf("image attachment %s size mismatch: metadata=%d downloaded=%d", image.id, image.sizeBytes, len(data))
		}
		if err := validateNativeImageBytes(image.contentType, data); err != nil {
			return nil, fmt.Errorf("image attachment %s: %w", image.id, err)
		}
		actualTotal += int64(len(data))
		dir := filepath.Join(root, fmt.Sprintf("%02d", index+1))
		if err := os.Mkdir(dir, 0o700); err != nil {
			return nil, fmt.Errorf("create image attachment directory: %w", err)
		}
		path := filepath.Join(dir, image.filename)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return nil, fmt.Errorf("write image attachment %s: %w", image.id, err)
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve image attachment %s path: %w", image.id, err)
		}
		inputs = append(inputs, agent.InputImage{Path: absPath, Name: image.filename, ContentType: image.contentType})
	}
	return inputs, nil
}

func canonicalNativeImageFilename(filename, contentType string) string {
	stem := strings.TrimSuffix(filename, filepath.Ext(filename))
	if strings.TrimSpace(stem) == "" {
		stem = "image"
	}
	if contentType == "image/jpeg" {
		return stem + ".jpg"
	}
	return stem + ".png"
}

func validateNativeImageBytes(contentType string, data []byte) error {
	switch contentType {
	case "image/jpeg":
		if _, err := jpeg.DecodeConfig(bytes.NewReader(data)); err != nil {
			return errors.New("downloaded bytes are not a valid JPEG")
		}
	case "image/png":
		if _, err := png.DecodeConfig(bytes.NewReader(data)); err != nil {
			return errors.New("downloaded bytes are not a valid PNG")
		}
		if pngContainsAnimationControl(data) {
			return errors.New("animated PNG is not supported for native image input")
		}
	default:
		return fmt.Errorf("content type %q is not supported", contentType)
	}
	return nil
}

func pngContainsAnimationControl(data []byte) bool {
	const pngHeaderBytes = 8
	if len(data) < pngHeaderBytes {
		return false
	}
	for offset := pngHeaderBytes; offset+12 <= len(data); {
		length := int64(binary.BigEndian.Uint32(data[offset : offset+4]))
		if string(data[offset+4:offset+8]) == "acTL" {
			return true
		}
		next := int64(offset) + 12 + length
		if next <= int64(offset) || next > int64(len(data)) {
			return false
		}
		offset = int(next)
	}
	return false
}
