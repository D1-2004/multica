package service

import "testing"

func TestPreferredExternalContentTypeUsesSourceForGenericDownload(t *testing.T) {
	got := preferredExternalContentType(
		"application/octet-stream",
		"text/markdown; charset=utf-8",
		[]byte("# imported attachment"),
	)
	if got != "text/markdown" {
		t.Fatalf("content type = %q, want text/markdown", got)
	}
}
