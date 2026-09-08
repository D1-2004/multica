package handler

import (
	"context"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/middleware"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type identityReuseFake struct {
	agent db.Agent
	calls int
	err   error
}

func (f *identityReuseFake) GetAgentInWorkspace(_ context.Context, p db.GetAgentInWorkspaceParams) (db.Agent, error) {
	if p.ID != f.agent.ID || p.WorkspaceID != f.agent.WorkspaceID {
		return db.Agent{}, pgx.ErrNoRows
	}
	return f.agent, nil
}
func (f *identityReuseFake) ListReusableDingTalkIdentities(context.Context, db.ListReusableDingTalkIdentitiesParams) ([]db.ListReusableDingTalkIdentitiesRow, error) {
	f.calls++
	return []db.ListReusableDingTalkIdentitiesRow{{SourceAgentID: f.agent.ID, AccountDisplayName: "display", OrganizationName: "org"}}, nil
}
func (f *identityReuseFake) ReuseDingTalkIdentity(_ context.Context, p db.ReuseDingTalkIdentityParams) (pgtype.UUID, error) {
	f.calls++
	return p.TargetAgentID, f.err
}
func TestDingTalkIdentityReuseAccess(t *testing.T) {
	ws := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	user := "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	target := "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
	source := "dddddddd-dddd-4ddd-8ddd-dddddddddddd"
	for _, tt := range []struct {
		name, actor, owner, sourceID string
		want                         int
		conflict                     bool
	}{
		{name: "owner", owner: user, sourceID: source, want: 204},
		{name: "workspace admin cannot reuse another owner's identity", owner: source, sourceID: source, want: 403},
		{name: "agent token", actor: "task_token", owner: user, sourceID: source, want: 403},
		{name: "workspace token", actor: "workspace_access_token", owner: user, sourceID: source, want: 403},
		{name: "cloud token", actor: "cloud_pat", owner: user, sourceID: source, want: 403},
		{name: "invalid UUID", owner: user, sourceID: "invalid", want: 400},
		{name: "stale source or bound target", owner: user, sourceID: source, want: 409, conflict: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := &identityReuseFake{agent: db.Agent{ID: parseUUID(target), WorkspaceID: parseUUID(ws), OwnerID: parseUUID(tt.owner)}}
			if tt.conflict {
				f.err = pgx.ErrNoRows
			}
			h := &Handler{dingTalkIdentityReuse: f}
			req := httptest.NewRequest("POST", "/", strings.NewReader(`{"agent_id":"`+target+`","source_agent_id":"`+tt.sourceID+`"}`))
			req.Header.Set("X-User-ID", user)
			req.Header.Set("X-Actor-Source", tt.actor)
			req = withURLParams(req, "id", ws)
			req = req.WithContext(middleware.SetMemberContext(req.Context(), ws, db.Member{WorkspaceID: parseUUID(ws), UserID: parseUUID(user), Role: "admin"}))
			w := httptest.NewRecorder()
			RequireHumanActor(http.HandlerFunc(h.ReuseDingTalkIdentity)).ServeHTTP(w, req)
			if w.Code != tt.want {
				t.Fatalf("status %d want %d: %s", w.Code, tt.want, w.Body.String())
			}
			if tt.want == 403 || tt.want == 400 {
				if f.calls != 0 {
					t.Fatal("write reached store despite guard")
				}
			}
		})
	}
}
