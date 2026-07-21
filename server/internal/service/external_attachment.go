package service

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"path"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/storage"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

const MaxExternalAttachmentBytes = 100 << 20

var (
	ErrExternalAttachmentStorageUnavailable = errors.New("external attachment storage unavailable")
	ErrExternalAttachmentExpired            = errors.New("external attachment download URL expired")
	ErrExternalAttachmentTooLarge           = errors.New("external attachment is too large")
)

type ExternalAttachmentSource struct {
	Name        string
	ContentType string
	SizeBytes   int64
	DownloadURL string
	ExpiresAt   *time.Time
}

type ExternalAttachmentImportParams struct {
	WorkspaceID   pgtype.UUID
	UploaderID    pgtype.UUID
	IssueID       pgtype.UUID
	ChatSessionID pgtype.UUID
	Sources       []ExternalAttachmentSource
}

// ExternalAttachmentService imports short-lived upstream URLs into Multica's
// normal attachment storage. The source URL is a transport detail and is never
// persisted in the attachment row.
type ExternalAttachmentService struct {
	Queries    *db.Queries
	Storage    storage.Storage
	HTTPClient *http.Client
	Now        func() time.Time
}

func NewExternalAttachmentService(q *db.Queries, store storage.Storage, httpClient *http.Client) *ExternalAttachmentService {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &ExternalAttachmentService{
		Queries:    q,
		Storage:    store,
		HTTPClient: httpClient,
		Now:        time.Now,
	}
}

func (s *ExternalAttachmentService) Import(ctx context.Context, params ExternalAttachmentImportParams) ([]db.Attachment, error) {
	if len(params.Sources) == 0 {
		return nil, nil
	}
	if s == nil || s.Queries == nil || s.Storage == nil {
		return nil, ErrExternalAttachmentStorageUnavailable
	}
	imported := make([]db.Attachment, 0, len(params.Sources))
	for _, source := range params.Sources {
		attachment, err := s.importOne(ctx, params, source)
		if err != nil {
			s.DeleteImported(ctx, imported)
			return nil, err
		}
		imported = append(imported, attachment)
	}
	return imported, nil
}

func (s *ExternalAttachmentService) importOne(ctx context.Context, params ExternalAttachmentImportParams, source ExternalAttachmentSource) (db.Attachment, error) {
	name := path.Base(strings.ReplaceAll(strings.TrimSpace(source.Name), "\\", "/"))
	if name == "" || name == "." || strings.TrimSpace(source.DownloadURL) == "" {
		return db.Attachment{}, errors.New("external attachment name and download URL are required")
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	if source.ExpiresAt != nil && !source.ExpiresAt.After(now()) {
		return db.Attachment{}, fmt.Errorf("%w: %s", ErrExternalAttachmentExpired, name)
	}
	if source.SizeBytes > MaxExternalAttachmentBytes {
		return db.Attachment{}, fmt.Errorf("%w: %s", ErrExternalAttachmentTooLarge, name)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(source.DownloadURL), nil)
	if err != nil {
		return db.Attachment{}, fmt.Errorf("build attachment download request for %s: %w", name, err)
	}
	resp, err := s.HTTPClient.Do(req)
	if err != nil {
		return db.Attachment{}, fmt.Errorf("download external attachment %s: %w", name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return db.Attachment{}, fmt.Errorf("download external attachment %s: HTTP %d", name, resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, MaxExternalAttachmentBytes+1))
	if err != nil {
		return db.Attachment{}, fmt.Errorf("read external attachment %s: %w", name, err)
	}
	if len(data) > MaxExternalAttachmentBytes {
		return db.Attachment{}, fmt.Errorf("%w: %s", ErrExternalAttachmentTooLarge, name)
	}

	contentType := preferredExternalContentType(resp.Header.Get("Content-Type"), source.ContentType, data)
	id, err := uuid.NewV7()
	if err != nil {
		return db.Attachment{}, fmt.Errorf("generate attachment id: %w", err)
	}
	attachmentID := pgtype.UUID{Bytes: id, Valid: true}
	key := "workspaces/" + uuidString(params.WorkspaceID) + "/" + id.String() + path.Ext(name)
	link, err := s.Storage.Upload(ctx, key, data, contentType, name)
	if err != nil {
		return db.Attachment{}, fmt.Errorf("store external attachment %s: %w", name, err)
	}
	attachment, err := s.Queries.CreateAttachment(ctx, db.CreateAttachmentParams{
		ID:            attachmentID,
		WorkspaceID:   params.WorkspaceID,
		IssueID:       params.IssueID,
		ChatSessionID: params.ChatSessionID,
		UploaderType:  "member",
		UploaderID:    params.UploaderID,
		Filename:      name,
		Url:           link,
		ContentType:   contentType,
		SizeBytes:     int64(len(data)),
	})
	if err != nil {
		s.Storage.Delete(ctx, key)
		return db.Attachment{}, fmt.Errorf("create external attachment %s: %w", name, err)
	}
	return attachment, nil
}

func (s *ExternalAttachmentService) DeleteImported(ctx context.Context, attachments []db.Attachment) {
	if s == nil {
		return
	}
	for _, attachment := range attachments {
		if s.Queries != nil {
			_ = s.Queries.DeleteAttachment(ctx, db.DeleteAttachmentParams{
				ID:          attachment.ID,
				WorkspaceID: attachment.WorkspaceID,
			})
		}
		if s.Storage != nil && attachment.Url != "" {
			s.Storage.Delete(ctx, s.Storage.KeyFromURL(attachment.Url))
		}
	}
}

func normalizedContentType(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	mediaType, _, err := mime.ParseMediaType(raw)
	if err != nil {
		return raw
	}
	return mediaType
}

func preferredExternalContentType(downloadHeader, sourceHint string, data []byte) string {
	downloadType := normalizedContentType(downloadHeader)
	if downloadType != "" && downloadType != "application/octet-stream" {
		return downloadType
	}
	if sourceType := normalizedContentType(sourceHint); sourceType != "" {
		return sourceType
	}
	return http.DetectContentType(data)
}

func uuidString(id pgtype.UUID) string {
	if !id.Valid {
		return ""
	}
	return uuid.UUID(id.Bytes).String()
}
