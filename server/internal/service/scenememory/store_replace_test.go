package scenememory

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestReplaceTextRejectsOverBudget(t *testing.T) {
	t.Parallel()
	store := NewStore(nil)
	_, err := store.ReplaceText(
		context.Background(),
		pgtype.UUID{Valid: true},
		pgtype.UUID{Valid: true},
		pgtype.UUID{Valid: true},
		1,
		strings.Repeat("啊", MaxMemoryCodePoints+1),
	)
	if !errors.Is(err, ErrMemoryText) {
		t.Fatalf("err=%v", err)
	}
}
