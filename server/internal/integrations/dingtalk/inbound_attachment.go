package dingtalk

import (
	"context"
	"errors"
	"fmt"
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
	if params.Message.Type != channel.MsgTypeImage {
		return nil, nil
	}
	if i == nil || i.service == nil || i.messenger == nil || i.decrypt == nil {
		return nil, errors.New("dingtalk: picture attachment importer is not configured")
	}
	raw, err := decodeDingTalkRaw(params.Message)
	if err != nil {
		return nil, fmt.Errorf("dingtalk: decode picture callback metadata: %w", err)
	}
	if strings.TrimSpace(raw.MessageDownloadCode) == "" {
		return nil, errors.New("dingtalk: picture callback has no download code")
	}
	installation, ok := params.Installation.Platform.(db.ChannelInstallation)
	if !ok {
		return nil, errors.New("dingtalk: picture installation payload has an invalid type")
	}
	creds, err := decodeChannelCredentials(installation.Config, i.decrypt)
	if err != nil {
		return nil, fmt.Errorf("dingtalk: decode picture installation credentials: %w", err)
	}
	downloadURL, err := i.messenger.resolveMessageFileURL(ctx, creds, raw.MessageDownloadCode)
	if err != nil {
		return nil, fmt.Errorf("dingtalk: resolve picture download URL: %w", err)
	}
	name, contentType, err := dingtalkPictureName(downloadURL)
	if err != nil {
		return nil, err
	}
	return i.service.Import(ctx, service.ExternalAttachmentImportParams{
		WorkspaceID:   params.WorkspaceID,
		UploaderID:    params.Sender,
		ChatSessionID: params.SessionID,
		Sources: []service.ExternalAttachmentSource{{
			Name:        name,
			ContentType: contentType,
			DownloadURL: downloadURL,
		}},
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
