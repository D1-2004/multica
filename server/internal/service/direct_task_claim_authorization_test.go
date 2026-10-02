package service

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDirectClaimDefaultsDenyAndScopesRuntimeAuthorization(t *testing.T) {
	for _, method := range []string{"agent", "runtime", "targeted", "batch"} {
		t.Run(method, func(t *testing.T) {
			f := directDatabase(t)
			ctx := context.Background()
			admitted, err := f.service.EnqueueDirectTask(ctx, f.request)
			if err != nil {
				t.Fatal(err)
			}
			claim := func(auth ...TaskClaimAuthorization) *db.AgentTaskQueue {
				t.Helper()
				var got *db.AgentTaskQueue
				var err error
				switch method {
				case "agent":
					got, err = f.service.ClaimTask(ctx, admitted.Task.AgentID, auth...)
				case "runtime":
					got, err = f.service.ClaimTaskForRuntime(ctx, admitted.Task.RuntimeID, auth...)
				case "targeted":
					got, err = f.service.ClaimTaskByIDForRuntime(ctx, admitted.Task.RuntimeID, admitted.Task.ID, auth...)
				case "batch":
					var rows []db.AgentTaskQueue
					rows, err = f.service.ClaimTasksForRuntimes(ctx, []pgtype.UUID{admitted.Task.RuntimeID}, 1, auth...)
					if len(rows) > 0 {
						got = &rows[0]
					}
				}
				if err != nil {
					t.Fatal(err)
				}
				return got
			}
			if got := claim(); got != nil {
				t.Fatal("omitted Host authorization claimed Direct")
			}
			if got := claim(TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{util.MustParseUUID(uuid.NewString())}}); got != nil {
				t.Fatal("another runtime's grant claimed Direct")
			}
			if got := claim(TaskClaimAuthorization{EmployeeDirectRuntimeIDs: []pgtype.UUID{admitted.Task.RuntimeID}}); got == nil || got.ID != admitted.Task.ID {
				t.Fatal("Host runtime grant lost Direct task", got)
			}
		})
	}
}
