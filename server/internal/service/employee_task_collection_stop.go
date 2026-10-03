package service

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/internal/taskinput"
)

// closeTaskCollectionsTx cancels every active cross-scene collection of a
// stopped Task in the stop transaction, which already holds the Task row, so
// no late answer, summary wake or invitation send outlives the stop. The
// same stop source replays to the same closes.
func closeTaskCollectionsTx(ctx context.Context, tx pgx.Tx, task employeetask.Task, source employeetask.Source) error {
	if task.Scope.Kind != employeetask.ScopeScene || task.Scope.Scene.SceneID == "" {
		return nil
	}
	scope := taskinput.Scope{WorkspaceID: task.Scope.WorkspaceID, AgentID: task.Scope.AgentID, TenantOrgID: task.Scope.TenantOrgID}
	store := taskinput.NewStore(tx)
	waits, err := store.TaskWaits(ctx, scope, task.ID)
	if err != nil {
		return err
	}
	for _, wait := range waits {
		_, _, err := store.CloseCollectionTx(ctx, scope, taskinput.CloseParams{CollectionID: wait.CollectionID, Mode: taskinput.CloseCancel, Reason: "task stopped",
			Source:           taskinput.Source{Namespace: "employee.task_stop", Key: source.Namespace + "/" + source.Key + "/" + wait.CollectionID},
			Authority:        taskinput.Authority{ActorRef: taskinput.HostActorPrefix + "task-stop", SceneID: task.Scope.Scene.SceneID, ReceiptRef: "employee-task-stop:" + task.ID, VerifiedAt: time.Now()},
			ExpectedRevision: wait.Revision})
		if err != nil && !errors.Is(err, taskinput.ErrClosed) {
			return err
		}
	}
	return nil
}
