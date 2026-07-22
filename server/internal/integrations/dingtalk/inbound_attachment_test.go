package dingtalk

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type attachmentResolveCall struct {
	creds        channelCredentials
	downloadCode string
}

type recordingMessageFileResolver struct {
	calls []attachmentResolveCall
}

func (r *recordingMessageFileResolver) resolveMessageFileURL(_ context.Context, creds channelCredentials, downloadCode string) (string, error) {
	r.calls = append(r.calls, attachmentResolveCall{creds: creds, downloadCode: downloadCode})
	return "https://files.example.test/" + downloadCode + ".png", nil
}

type recordingExternalAttachmentImporter struct {
	calls []service.ExternalAttachmentImportParams
}

func (r *recordingExternalAttachmentImporter) Import(_ context.Context, params service.ExternalAttachmentImportParams) ([]db.Attachment, error) {
	r.calls = append(r.calls, params)
	return nil, nil
}

func (r *recordingExternalAttachmentImporter) DeleteImported(context.Context, []db.Attachment) {}

func TestDingTalkPictureName(t *testing.T) {
	name, contentType, err := dingtalkPictureName("https://files.example.test/path/object.png?token=secret")
	if err != nil {
		t.Fatalf("dingtalkPictureName: %v", err)
	}
	if name != "dingtalk-picture.png" || contentType != "image/png" {
		t.Fatalf("name/contentType = %q/%q", name, contentType)
	}
	if _, _, err := dingtalkPictureName("https://files.example.test/path/object.bin"); err == nil {
		t.Fatal("expected unsupported picture extension to be rejected")
	}
}

func TestDingTalkFileAttachmentSource(t *testing.T) {
	source, err := dingtalkAttachmentSource(channel.MsgTypeFile, "invoice.pdf", "https://files.example.test/download?token=secret")
	if err != nil {
		t.Fatalf("dingtalkAttachmentSource: %v", err)
	}
	if source.Name != "invoice.pdf" || source.ContentType != "application/pdf" {
		t.Fatalf("name/contentType = %q/%q", source.Name, source.ContentType)
	}
	if source.DownloadURL != "https://files.example.test/download?token=secret" {
		t.Fatalf("download URL = %q", source.DownloadURL)
	}
}

func TestDingTalkFileAttachmentSourceRequiresName(t *testing.T) {
	if _, err := dingtalkAttachmentSource(channel.MsgTypeFile, "", "https://files.example.test/download"); err == nil {
		t.Fatal("expected a file callback without a filename to be rejected")
	}
}

func TestRichTextTwoPicturesResolveAndImportInCallbackOrder(t *testing.T) {
	var callback botCallbackData
	fixture := `{"conversationId":"cid","msgId":"m-two-pictures","robotCode":"robot-from-callback","senderStaffId":"staff","conversationType":"2","msgtype":"richText","content":{"richText":[{"text":"依次看两张图"},{"type":"picture","downloadCode":"first-code"},{"type":"picture","downloadCode":"second-code"}]}}`
	if err := json.Unmarshal([]byte(fixture), &callback); err != nil {
		t.Fatalf("unmarshal callback fixture: %v", err)
	}
	message, ok := inboundFromBotCallback(callback, "client-id")
	if !ok {
		t.Fatal("callback was dropped")
	}

	config, err := json.Marshal(dingtalkInstallConfig{
		AppID:              "client-id",
		AppSecretEncrypted: base64.StdEncoding.EncodeToString([]byte("sealed-secret")),
		RobotCode:          "stale-installation-robot",
	})
	if err != nil {
		t.Fatalf("marshal installation config: %v", err)
	}
	resolver := &recordingMessageFileResolver{}
	attachmentService := &recordingExternalAttachmentImporter{}
	importer := &inboundAttachmentImporter{
		service:   attachmentService,
		messenger: resolver,
		decrypt: func(ciphertext []byte) ([]byte, error) {
			if string(ciphertext) != "sealed-secret" {
				t.Fatalf("ciphertext = %q", ciphertext)
			}
			return []byte("plain-secret"), nil
		},
	}
	workspaceID := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	senderID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	sessionID := pgtype.UUID{Bytes: [16]byte{3}, Valid: true}
	_, err = importer.Import(context.Background(), engine.AppendParams{
		WorkspaceID: workspaceID,
		Sender:      senderID,
		SessionID:   sessionID,
		Installation: engine.ResolvedInstallation{Platform: db.ChannelInstallation{
			Config: config,
		}},
		Message: message,
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(resolver.calls) != 2 {
		t.Fatalf("resolve calls = %#v", resolver.calls)
	}
	for index, wantCode := range []string{"first-code", "second-code"} {
		call := resolver.calls[index]
		if call.downloadCode != wantCode || call.creds.RobotCode != "robot-from-callback" {
			t.Fatalf("resolve call %d = %#v", index, call)
		}
	}
	if len(attachmentService.calls) != 1 {
		t.Fatalf("import calls = %#v", attachmentService.calls)
	}
	params := attachmentService.calls[0]
	if params.WorkspaceID != workspaceID || params.UploaderID != senderID || params.ChatSessionID != sessionID {
		t.Fatalf("import scope = %#v", params)
	}
	if len(params.Sources) != 2 || params.Sources[0].DownloadURL != "https://files.example.test/first-code.png" || params.Sources[1].DownloadURL != "https://files.example.test/second-code.png" {
		t.Fatalf("import source order = %#v", params.Sources)
	}
}
