package dingtalk

import (
	"testing"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
)

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
	source, err := dingtalkAttachmentSource(channel.MsgTypeFile, dingtalkRawEvent{
		MessageFileName: "invoice.pdf",
	}, "https://files.example.test/download?token=secret")
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
	if _, err := dingtalkAttachmentSource(channel.MsgTypeFile, dingtalkRawEvent{}, "https://files.example.test/download"); err == nil {
		t.Fatal("expected a file callback without a filename to be rejected")
	}
}
