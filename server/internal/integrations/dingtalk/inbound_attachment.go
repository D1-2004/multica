package dingtalk

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"net/url"
	"path"
	"strings"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type inboundAttachmentImporter struct {
	service   *service.ExternalAttachmentService
	decrypt   Decrypter
	messenger *RobotMessenger
}

func (i *inboundAttachmentImporter) Import(ctx context.Context, params engine.AppendParams) ([]db.Attachment, error) {
	raw, err := decodeDingTalkRaw(params.Message)
	if err != nil {
		return nil, fmt.Errorf("dingtalk: decode attachment callback metadata: %w", err)
	}
	if len(raw.MessageAttachments) == 0 {
		return nil, nil
	}
	if i == nil || i.service == nil || i.messenger == nil || i.decrypt == nil {
		return nil, errors.New("dingtalk: attachment importer is not configured")
	}
	installation, ok := params.Installation.Platform.(db.ChannelInstallation)
	if !ok {
		return nil, errors.New("dingtalk: attachment installation payload has an invalid type")
	}
	creds, err := decodeChannelCredentials(installation.Config, i.decrypt)
	if err != nil {
		return nil, fmt.Errorf("dingtalk: decode attachment installation credentials: %w", err)
	}
	creds.RobotCode = strings.TrimSpace(raw.RobotCode)
	if creds.RobotCode == "" {
		return nil, errors.New("dingtalk: attachment callback has no robot code")
	}
	sources := make([]service.ExternalAttachmentSource, 0, len(raw.MessageAttachments))
	for _, attachment := range raw.MessageAttachments {
		if strings.TrimSpace(attachment.DownloadCode) == "" {
			return nil, errors.New("dingtalk: attachment callback has no download code")
		}
		downloadURL, err := i.messenger.resolveMessageFileURL(ctx, creds, attachment.DownloadCode)
		if err != nil {
			return nil, fmt.Errorf("dingtalk: resolve attachment download URL: %w", err)
		}
		source, err := dingtalkAttachmentSource(attachment.Type, attachment.FileName, downloadURL)
		if err != nil {
			return nil, err
		}
		sources = append(sources, source)
	}
	return i.service.Import(ctx, service.ExternalAttachmentImportParams{
		WorkspaceID:   params.WorkspaceID,
		UploaderID:    params.Sender,
		ChatSessionID: params.SessionID,
		Sources:       sources,
	})
}

func (i *inboundAttachmentImporter) DeleteImported(ctx context.Context, attachments []db.Attachment) {
	if i == nil || i.service == nil || len(attachments) == 0 {
		return
	}
	i.service.DeleteImported(ctx, attachments)
}

func dingtalkPictureName(downloadURL string) (string, string, error) {
	parsed, err := url.Parse(downloadURL)
	if err != nil {
		return "", "", errors.New("dingtalk: picture download URL is invalid")
	}
	switch ext := strings.ToLower(path.Ext(parsed.Path)); ext {
	case ".png":
		return "dingtalk-picture.png", "image/png", nil
	case ".jpg", ".jpeg":
		return "dingtalk-picture" + ext, "image/jpeg", nil
	case ".gif":
		return "dingtalk-picture.gif", "image/gif", nil
	case ".webp":
		return "dingtalk-picture.webp", "image/webp", nil
	case ".heic":
		return "dingtalk-picture.heic", "image/heic", nil
	default:
		return "", "", errors.New("dingtalk: picture download URL has an unsupported image extension")
	}
}

func dingtalkAttachmentSource(msgType channel.MsgType, fileName, downloadURL string) (service.ExternalAttachmentSource, error) {
	source := service.ExternalAttachmentSource{DownloadURL: downloadURL}
	switch msgType {
	case channel.MsgTypeImage:
		name, contentType, err := dingtalkPictureName(downloadURL)
		if err != nil {
			return service.ExternalAttachmentSource{}, err
		}
		source.Name = name
		source.ContentType = contentType
	case channel.MsgTypeFile:
		name := strings.TrimSpace(fileName)
		if name == "" {
			return service.ExternalAttachmentSource{}, errors.New("dingtalk: file callback has no filename")
		}
		source.Name = name
		source.ContentType = mime.TypeByExtension(strings.ToLower(path.Ext(name)))
	default:
		return service.ExternalAttachmentSource{}, fmt.Errorf("dingtalk: unsupported attachment type %q", msgType)
	}
	return source, nil
}
