package handler

import (
	"net/http"
	"testing"
)

func TestSetDaemonA2AAttachmentHeadersUsesOpaqueTransportType(t *testing.T) {
	header := make(http.Header)
	setDaemonA2AAttachmentHeaders(header, "text/html; charset=utf-8", "example.html")

	if got := header.Get("Content-Type"); got != "application/octet-stream" {
		t.Fatalf("Content-Type = %q, want application/octet-stream", got)
	}
	if got := header.Get("Content-Disposition"); got == "" {
		t.Fatal("Content-Disposition is empty")
	}
	if got := header.Get("Content-Length"); got != "" {
		t.Fatalf("Content-Length = %q, want empty streaming response", got)
	}
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
	if got := header.Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options = %q, want nosniff", got)
	}
}
