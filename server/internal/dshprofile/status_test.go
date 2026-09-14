package dshprofile

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

type statusRow func(...any) error

func (r statusRow) Scan(dest ...any) error { return r(dest...) }

type statusDatabase struct {
	key    dshhost.Key
	source Source
	builds []BuildStatus
}

func (*statusDatabase) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("unexpected transaction")
}
func (d *statusDatabase) QueryRow(_ context.Context, query string, args ...any) pgx.Row {
	return statusRow(func(dest ...any) error {
		if args[0] != d.key.WorkspaceID {
			return pgx.ErrNoRows
		}
		switch {
		case strings.Contains(query, "SELECT p.desired_revision"):
			if args[1] != d.key.AgentID {
				return pgx.ErrNoRows
			}
			*dest[0].(*int64) = 7
			*dest[1].(*int64) = 0
			*dest[2].(*int64) = 0
			*dest[3].(*string) = ""
			*dest[4].(*string) = "source-digest"
			*dest[5].(*string) = ""
			*dest[6].(*bool) = false
		case strings.Contains(query, "SELECT source_json"):
			if args[1] != d.key.AgentID || args[2] != int64(7) {
				return pgx.ErrNoRows
			}
			raw, _ := json.Marshal(d.source)
			*dest[0].(*string) = string(raw)
		case strings.Contains(query, "jsonb_agg"):
			expected := []string{BuildKey(d.source.TemplateID, d.source.Plugins[0])}
			if args[1] != d.source.TemplateID || !reflect.DeepEqual(args[2], expected) {
				return errors.New("unscoped build query")
			}
			raw, _ := json.Marshal(d.builds)
			*dest[0].(*[]byte) = raw
		default:
			return errors.New("unexpected query")
		}
		return nil
	})
}

func TestStatusShowsFailedBuildWithoutPrivateConfiguration(t *testing.T) {
	key := dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()}
	source := Source{TemplateID: "template-a", Plugins: []SourcePlugin{
		{ID: uuid.New(), Enabled: true, PackageName: "fixture", Version: "1.0.0", Config: map[string]any{"secret": "private-canary"}},
		{ID: uuid.New(), Enabled: false, PackageName: "disabled", Version: "1.0.0"},
	}}
	database := &statusDatabase{key: key, source: source, builds: []BuildStatus{{PackageName: "fixture", Version: "1.0.0", State: "failed"}}}
	store := Store{DB: database}
	value, err := store.Status(context.Background(), key)
	if err != nil || value.State != "build_failed" || value.Current || len(value.Builds) != 1 {
		t.Fatalf("status=%+v err=%v", value, err)
	}
	raw, _ := json.Marshal(value)
	if strings.Contains(string(raw), "private-canary") || strings.Contains(string(raw), "source-digest") || strings.Contains(string(raw), "template-a") {
		t.Fatal("private source escaped status")
	}
	database.builds[0].State = "ready"
	value, err = store.Status(context.Background(), key)
	if err != nil || value.State != "pending_host" || value.Current {
		t.Fatal("completed builds shown as still building or already applied", err)
	}
	database.builds = nil
	if _, err := store.Status(context.Background(), key); err == nil {
		t.Fatal("missing build status silently accepted")
	}
	alien := key
	alien.AgentID = uuid.New()
	value, err = store.Status(context.Background(), alien)
	if err != nil || value.State != "unprepared" || len(value.Builds) != 0 {
		t.Fatal("another employee status leaked")
	}
}
