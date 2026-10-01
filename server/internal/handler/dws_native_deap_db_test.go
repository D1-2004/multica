package handler

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// The DEAP link queries: a link applies only to the account it was made
// for, and the native source sees it.
func TestNativeDEAPLinkQueries(t *testing.T) {
	f := newNativeDBFixture(t)
	ctx := context.Background()
	agentID := parseUUID(f.agentID)
	t.Cleanup(func() {
		_, _ = testPool.Exec(ctx, "DELETE FROM agent_dws_native_deap_link WHERE agent_id=$1", f.agentID)
	})
	q := f.h.Queries
	if _, err := q.UpsertDWSNativeDEAPLink(ctx, db.UpsertDWSNativeDEAPLinkParams{AgentID: agentID, WorkspaceID: parseUUID(testWorkspaceID),
		DwsUid: f.identity.UID, OrgID: "2002", DeapAgentUuid: "de2a8cc1-413c-47f0-a79b-fede1b853847", SupervisorUid: "6753994909",
		UpdatedBy: parseUUID(testUserID)}); err != nil {
		t.Fatal(err)
	}
	link, err := q.GetDWSNativeDEAPLink(ctx, db.GetDWSNativeDEAPLinkParams{AgentID: agentID, DwsUid: f.identity.UID, OrgID: "2002"})
	if err != nil || link.SupervisorUid != "6753994909" {
		t.Fatalf("link = %+v, %v", link, err)
	}
	if _, err := q.GetDWSNativeDEAPLink(ctx, db.GetDWSNativeDEAPLinkParams{AgentID: agentID, DwsUid: "1", OrgID: "2002"}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("a link applied to another account: %v", err)
	}
	ids, err := f.h.NativeSubscriptionIdentities(ctx)
	if err != nil {
		t.Fatal(err)
	}
	versioned := false
	for _, id := range ids {
		versioned = versioned || (id.AgentID == f.agentID && strings.HasPrefix(id.CredentialVersion, "deap-"))
	}
	if !versioned {
		t.Fatalf("the native source does not see the link: %+v", ids)
	}
	if err := q.DeleteDWSNativeDEAPLink(ctx, db.DeleteDWSNativeDEAPLinkParams{WorkspaceID: parseUUID(testWorkspaceID), AgentID: agentID}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetWorkspaceDWSNativeDEAPLink(ctx, db.GetWorkspaceDWSNativeDEAPLinkParams{WorkspaceID: parseUUID(testWorkspaceID), AgentID: agentID}); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("link not deleted: %v", err)
	}
}
