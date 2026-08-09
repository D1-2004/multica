package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func TestDeriveSquadMemberStatus(t *testing.T) {
	now := time.Date(2026, 5, 18, 12, 0, 0, 0, time.UTC)
	online := pgtype.Text{String: "online", Valid: true}
	offline := pgtype.Text{String: "offline", Valid: true}
	missing := pgtype.Text{}

	tsAgo := func(d time.Duration) pgtype.Timestamptz {
		return pgtype.Timestamptz{Time: now.Add(-d), Valid: true}
	}
	tsNone := pgtype.Timestamptz{}

	cases := []struct {
		name          string
		archived      bool
		runtimeStatus pgtype.Text
		lastSeen      pgtype.Timestamptz
		hasActiveTask bool
		want          string
	}{
		{"active wins over offline runtime", false, offline, tsAgo(time.Hour), true, "working"},
		{"active wins over missing runtime", false, missing, tsNone, true, "working"},
		{"online runtime, no task", false, online, tsAgo(2 * time.Second), false, "idle"},
		{"offline runtime, recent heartbeat", false, offline, tsAgo(2 * time.Minute), false, "unstable"},
		{"offline runtime, stale heartbeat", false, offline, tsAgo(2 * time.Hour), false, "offline"},
		{"offline runtime, no heartbeat", false, offline, tsNone, false, "offline"},
		{"no runtime row", false, missing, tsNone, false, "offline"},
		// Archived agents always report archived regardless of any leftover
		// runtime row or task — they should appear in the squad listing
		// but never look like they're still working or merely offline.
		{"archived agent with active task", true, online, tsAgo(time.Second), true, "archived"},
		{"archived agent with online runtime", true, online, tsAgo(time.Second), false, "archived"},
		{"archived agent already offline", true, offline, tsAgo(time.Hour), false, "archived"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := deriveSquadMemberStatus(tc.archived, tc.runtimeStatus, tc.lastSeen, tc.hasActiveTask, now)
			if got != tc.want {
				t.Fatalf("deriveSquadMemberStatus = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestListSquadMemberStatusHidesA2ATasksFromHumanProjection(t *testing.T) {
	if testHandler == nil || testPool == nil {
		t.Skip("database not available")
	}

	ctx := context.Background()
	var a2aSchemaAvailable bool
	if err := testPool.QueryRow(ctx, `SELECT to_regclass('a2a_context') IS NOT NULL`).Scan(&a2aSchemaAvailable); err != nil || !a2aSchemaAvailable {
		t.Skip("A2A schema is not migrated in the handler test database")
	}

	viewerID := createPlainMember(t, "squad-status-a2a-viewer@multica.test")
	runtimeID := seedIsolatedRuntime(t, "Squad Status A2A Projection Runtime")
	runtimeSeenAt := time.Now().UTC().Add(-24 * time.Hour).Truncate(time.Second)
	if _, err := testPool.Exec(ctx, `
		UPDATE agent_runtime
		SET status = 'offline', last_seen_at = $1
		WHERE id = $2
	`, runtimeSeenAt, runtimeID); err != nil {
		t.Fatalf("set isolated runtime status: %v", err)
	}

	hiddenOnlyAgentID := seedAgentOnRuntime(t, runtimeID, "Squad Status A2A Hidden Agent", false)
	visibleAgentID := seedAgentOnRuntime(t, runtimeID, "Squad Status Human Visible Agent", false)
	squad := createSquadAs(t, "", "Squad Status A2A Projection", hiddenOnlyAgentID)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO squad_member (squad_id, member_type, member_id, role)
		VALUES ($1, 'agent', $2, '')
	`, squad.ID, visibleAgentID); err != nil {
		t.Fatalf("add visible control agent to squad: %v", err)
	}

	visibleDispatchedAt := runtimeSeenAt.Add(time.Hour)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, status, priority, dispatched_at, context
		)
		VALUES ($1, $2, 'running', 0, $3, '{}'::jsonb)
	`, visibleAgentID, runtimeID, visibleDispatchedAt); err != nil {
		t.Fatalf("create ordinary visible task: %v", err)
	}

	markerDispatchedAt := runtimeSeenAt.Add(2 * time.Hour)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, status, priority, dispatched_at, context
		)
		VALUES ($1, $2, 'running', 0, $3, '{"multica_origin":"a2a"}'::jsonb)
	`, hiddenOnlyAgentID, runtimeID, markerDispatchedAt); err != nil {
		t.Fatalf("create context-marked A2A task: %v", err)
	}

	publicAgentID := "squad_status_" + strings.ReplaceAll(hiddenOnlyAgentID, "-", "")
	var endpointID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO agent_a2a_endpoint (
			workspace_id, agent_id, public_agent_id, enabled,
			delegated_by_user_id, card_name
		)
		VALUES ($1, $2, $3, TRUE, $4, 'Squad Status A2A Agent')
		RETURNING id
	`, testWorkspaceID, hiddenOnlyAgentID, publicAgentID, testUserID).Scan(&endpointID); err != nil {
		t.Fatalf("create A2A endpoint: %v", err)
	}

	var clientID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO a2a_client (
			endpoint_id, name, status, scopes, created_by, updated_by
		)
		VALUES ($1, 'squad-status-client', 'active', ARRAY['send', 'read']::text[], $2, $2)
		RETURNING id
	`, endpointID, testUserID).Scan(&clientID); err != nil {
		t.Fatalf("create A2A client: %v", err)
	}

	var chatSessionID string
	if err := testPool.QueryRow(ctx, `
		INSERT INTO chat_session (workspace_id, agent_id, creator_id, title, status)
		VALUES ($1, $2, $3, 'Squad status A2A context', 'active')
		RETURNING id
	`, testWorkspaceID, hiddenOnlyAgentID, testUserID).Scan(&chatSessionID); err != nil {
		t.Fatalf("create A2A chat session: %v", err)
	}
	if _, err := testPool.Exec(ctx, `
		INSERT INTO a2a_context (
			endpoint_id, client_id, public_context_id, chat_session_id
		)
		VALUES ($1, $2, 'squad-status-context', $3)
	`, endpointID, clientID, chatSessionID); err != nil {
		t.Fatalf("create A2A context: %v", err)
	}

	boundDispatchedAt := runtimeSeenAt.Add(3 * time.Hour)
	if _, err := testPool.Exec(ctx, `
		INSERT INTO agent_task_queue (
			agent_id, runtime_id, status, priority, dispatched_at,
			chat_session_id, context
		)
		VALUES ($1, $2, 'running', 0, $3, $4, '{}'::jsonb)
	`, hiddenOnlyAgentID, runtimeID, boundDispatchedAt, chatSessionID); err != nil {
		t.Fatalf("create context-bound A2A task: %v", err)
	}

	w := httptest.NewRecorder()
	testHandler.ListSquadMemberStatus(w, squadScopeReq(
		viewerID,
		http.MethodGet,
		"/api/squads/"+squad.ID+"/members/status",
		nil,
		map[string]string{"id": squad.ID},
	))
	if w.Code != http.StatusOK {
		t.Fatalf("ListSquadMemberStatus: expected 200, got %d: %s", w.Code, w.Body.String())
	}

	var response SquadMemberStatusListResponse
	if err := json.NewDecoder(w.Body).Decode(&response); err != nil {
		t.Fatalf("decode squad member status response: %v", err)
	}
	byMemberID := make(map[string]SquadMemberStatusResponse, len(response.Members))
	for _, member := range response.Members {
		byMemberID[member.MemberID] = member
	}

	hidden := byMemberID[hiddenOnlyAgentID]
	if hidden.MemberID == "" {
		t.Fatalf("hidden-task agent missing from response: %#v", response.Members)
	}
	if hidden.Status == nil || *hidden.Status != "offline" {
		t.Fatalf("A2A-only agent status = %v, want offline without revealing active A2A work", hidden.Status)
	}
	if len(hidden.ActiveIssues) != 0 {
		t.Fatalf("A2A-only agent exposed active issues: %#v", hidden.ActiveIssues)
	}
	assertSquadStatusTimestamp(t, hidden.LastActiveAt, runtimeSeenAt, "A2A-only agent last_active_at")

	visible := byMemberID[visibleAgentID]
	if visible.MemberID == "" {
		t.Fatalf("ordinary-task control agent missing from response: %#v", response.Members)
	}
	if visible.Status == nil || *visible.Status != "working" {
		t.Fatalf("ordinary-task control status = %v, want working", visible.Status)
	}
	assertSquadStatusTimestamp(t, visible.LastActiveAt, visibleDispatchedAt, "ordinary-task control last_active_at")
}

func assertSquadStatusTimestamp(t *testing.T, got *string, want time.Time, field string) {
	t.Helper()
	if got == nil {
		t.Fatalf("%s = nil, want %s", field, want.Format(time.RFC3339Nano))
	}
	parsed, err := time.Parse(time.RFC3339Nano, *got)
	if err != nil {
		t.Fatalf("parse %s %q: %v", field, *got, err)
	}
	if !parsed.Equal(want) {
		t.Fatalf("%s = %s, want %s", field, parsed.Format(time.RFC3339Nano), want.Format(time.RFC3339Nano))
	}
}
