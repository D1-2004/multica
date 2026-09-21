package service

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestAttachWorkspaceCatalogSkippedWithoutPool(t *testing.T) {
	l := &FCE2BLauncher{}
	if got := l.attachWorkspaceCatalog(context.Background(), "sbx", pgtype.UUID{}, pgtype.UUID{}); got != "" {
		t.Fatalf("got %q, want empty", got)
	}
}

func TestPyQuoteJSON(t *testing.T) {
	if got := pyQuote(`a/b "c"`); got != `"a/b \"c\""` {
		t.Fatalf("pyQuote: %s", got)
	}
}
