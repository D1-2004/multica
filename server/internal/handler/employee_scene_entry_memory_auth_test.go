package handler

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestEmployeeSceneResetCompletesOnSingleConnection(t *testing.T) {
	f, _ := employeeResetFixture(t)
	receipt := submitEmployeeReset(t, f, []DispatchMessage{{OpenMsgID: "one-connection-reset", Text: "/reset-memory"}})
	cfg := testPool.Config().Copy()
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	h := *f.response.h
	h.DB, h.TxStarter, h.Queries = pool, pool, db.New(pool)
	h.EmployeeMemory = employeememory.NewStore(pool)
	worker := NewEmployeeSceneWorker(&h, f.model)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if worked, err := worker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("reset waits on its own pool connection: worked=%v err=%v", worked, err)
	}
	var state string
	if err = testPool.QueryRow(context.Background(), `SELECT state FROM employee_event_consumption WHERE receipt_id=$1`, receipt).Scan(&state); err != nil || state != "completed" {
		t.Fatalf("single-connection reset incomplete: %s %v", state, err)
	}
}

func TestEmployeeSceneResetChecksCurrentWorkspaceMembership(t *testing.T) {
	for _, departed := range []bool{false, true} {
		name := "current_member"
		if departed {
			name = "departed_owner_on_shared_runtime"
		}
		t.Run(name, func(t *testing.T) {
			f, _ := employeeResetFixtureIn(t, "single")
			ctx := context.Background()
			user := uuid.NewString()
			if _, err := testPool.Exec(ctx, `INSERT INTO "user"(id,name,email) VALUES($1,'Reset operator',$2)`, user, user+"@memory.test"); err != nil {
				t.Fatal(err)
			}
			var memberID string
			if err := testPool.QueryRow(ctx, `INSERT INTO member(workspace_id,user_id,role) VALUES($1,$2,'member') RETURNING id::text`, testWorkspaceID, user).Scan(&memberID); err != nil {
				t.Fatal(err)
			}
			if _, err := testPool.Exec(ctx, `UPDATE agent SET owner_id=$2 WHERE id=$1`, f.scope.AgentID, user); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				_, _ = testPool.Exec(ctx, `UPDATE agent SET owner_id=$2 WHERE id=$1`, f.scope.AgentID, testUserID)
				_, _ = testPool.Exec(ctx, `DELETE FROM member WHERE user_id=$1`, user)
				_, _ = testPool.Exec(ctx, `DELETE FROM "user" WHERE id=$1`, user)
			})
			f.dc.UserID = parseUUID(user)
			receipt := submitEmployeeReset(t, f, []DispatchMessage{{OpenMsgID: "queued-before-membership-check", Text: "/reset-memory"}})
			if departed {
				result, err := f.response.h.revokeAndRemoveMember(ctx, parseUUID(testWorkspaceID), parseUUID(user), parseUUID(memberID), parseUUID(testUserID))
				if err != nil {
					t.Fatal(err)
				}
				if len(result.Runtimes) != 0 {
					t.Fatal("fixture must use another owner's shared Runtime")
				}
			}
			if _, err := f.response.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
				t.Fatal(err)
			}
			shared := f.privateScope()
			shared.Kind = employeememory.ScopeScene
			shared.PrincipalID = ""
			brief, err := f.response.h.EmployeeMemory.Brief(ctx, shared, "", 8)
			if err != nil {
				t.Fatal(err)
			}
			var state, reason string
			if err = testPool.QueryRow(ctx, `SELECT state,reason FROM employee_event_consumption WHERE receipt_id=$1`, receipt).Scan(&state, &reason); err != nil {
				t.Fatal(err)
			}
			if departed {
				if !strings.Contains(brief, "shared-preference") || state != "held" || reason != "admission_principal_revoked" {
					t.Fatalf("departed owner executed old reset: state=%s reason=%s brief=%q", state, reason, brief)
				}
			} else if brief != "" || state != "completed" {
				t.Fatalf("normal member blocked: state=%s brief=%q", state, brief)
			}
		})
	}
}
