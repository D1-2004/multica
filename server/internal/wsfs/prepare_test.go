package wsfs

import (
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/multica-ai/multica/server/internal/dshhost"
)

type noneGrantDB struct{}

func (noneGrantDB) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.NewCommandTag(""), nil
}
func (noneGrantDB) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, pgx.ErrNoRows
}
func (noneGrantDB) QueryRow(context.Context, string, ...any) pgx.Row {
	return noneRow{}
}

type noneRow struct{}

func (noneRow) Scan(...any) error { return pgx.ErrNoRows }

func TestPrepareMountGrantNoneDoesNotCallCloud(t *testing.T) {
	called := false
	c := Controller{
		Spec: testSpec(),
		NewAPI: func(context.Context) (dshhost.CloudCaller, error) {
			called = true
			return nil, nil
		},
	}
	employee := &dshhost.Host{
		Key:     dshhost.Key{WorkspaceID: uuid.New(), AgentID: uuid.New()},
		Storage: dshhost.Storage{VolumeName: "vol-employee", RoleARN: "role-employee", AccessPointARN: "ap-employee"},
	}
	got, err := c.PrepareMount(context.Background(), noneGrantDB{}, employee.WorkspaceID, employee.AgentID, employee)
	if err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("grant none opened the cloud API")
	}
	if got.Shared != nil || got.RoleARN != "role-employee" {
		t.Fatalf("DSH single-mount changed: %+v", got)
	}
}

func TestCompositePolicyReadOmitsSharedWrite(t *testing.T) {
	doc := compositePolicy("ap-employee", "ap-shared-ro", AccessRead)
	if strings.Count(doc, "nas:ClientWrite") != 1 {
		t.Fatalf("read policy should keep ClientWrite only on the employee AP: %s", doc)
	}
	if !strings.Contains(doc, "ap-shared-ro") {
		t.Fatal("read policy must bind the RO access point")
	}
	write := compositePolicy("ap-employee", "ap-shared-rw", AccessWrite)
	if strings.Count(write, "nas:ClientWrite") != 2 {
		t.Fatalf("write policy should grant ClientWrite on both APs: %s", write)
	}
}
