package db_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// Temporary tables isolate these projection tests from application data and migrations.
func TestCoordinatorConversations(t *testing.T) {
	url := os.Getenv("COORDINATOR_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("COORDINATOR_TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, `
 CREATE TEMP TABLE chat_session (id uuid PRIMARY KEY, workspace_id uuid, agent_id uuid, title text NOT NULL DEFAULT '', updated_at timestamptz NOT NULL DEFAULT now());
 CREATE TEMP TABLE inbound_coordinator_job (chat_session_id uuid PRIMARY KEY, workspace_id uuid, agent_id uuid, endpoint_namespace_id uuid, command jsonb);
 CREATE TEMP TABLE chat_message (
 id uuid PRIMARY KEY, chat_session_id uuid, role text NOT NULL DEFAULT 'assistant', content text NOT NULL DEFAULT '',
 task_id uuid, created_at timestamptz NOT NULL, failure_reason text, elapsed_ms bigint,
 message_kind text NOT NULL DEFAULT 'coordinator', channel_media_pending_until timestamptz,
 channel_ingested boolean NOT NULL DEFAULT false, quick_actions jsonb NOT NULL DEFAULT '[]', client_receipt_recorded_at timestamptz, source_payload jsonb);
 `)
	if err != nil {
		t.Fatal(err)
	}
	q := db.New(conn)
	uid := func() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
	workspace, agent, namespace := uid(), uid(), uid()
	add := func(ws, ag, ns pgtype.UUID, source, cid string) pgtype.UUID {
		t.Helper()
		id := uid()
		command, _ := json.Marshal(map[string]any{"source": map[string]any{"platform": "dingtalk", "type": source}, "event": map[string]any{"data": map[string]any{"conversation": map[string]any{"openConversationId": cid, "title": "same title", "type": "group"}}}})
		if _, err := conn.Exec(ctx, `INSERT INTO chat_session (id,workspace_id,agent_id) VALUES ($1,$2,$3)`, id, ws, ag); err != nil {
			t.Fatal(err)
		}
		if _, err := conn.Exec(ctx, `INSERT INTO inbound_coordinator_job VALUES ($1,$2,$3,$4,$5)`, id, ws, ag, ns, command); err != nil {
			t.Fatal(err)
		}
		return id
	}
	first := add(workspace, agent, namespace, "digital_employee", "cid-a")
	second := add(workspace, agent, namespace, "digital_employee", "cid-a")
	add(workspace, agent, namespace, "digital_employee", "cid-b")
	add(workspace, agent, namespace, "robot", "cid-a")
	add(workspace, agent, uid(), "digital_employee", "cid-a")
	missing1 := add(workspace, agent, namespace, "digital_employee", "")
	add(workspace, agent, namespace, "digital_employee", "  ")
	foreignAgent := add(workspace, uid(), namespace, "digital_employee", "cid-a")
	add(uid(), agent, namespace, "digital_employee", "cid-a")
	params := db.ListCoordinatorConversationsParams{WorkspaceID: workspace, AgentID: agent, PageLimit: 2}
	groups := map[string]int64{}
	for {
		page, err := q.ListCoordinatorConversations(ctx, params)
		if err != nil {
			t.Fatal(err)
		}
		if len(page) > 2 {
			t.Fatal("unbounded page")
		}
		for _, row := range page {
			if _, exists := groups[row.ID]; exists {
				t.Fatal("duplicate conversation across pages")
			}
			groups[row.ID] = row.SessionCount
		}
		if len(page) < 2 {
			break
		}
		params.PageOffset += int32(len(page))
	}
	if len(groups) != 6 {
		t.Fatalf("got %d conversations, want 6 (scope, source, namespace and missing ID isolation)", len(groups))
	}
	merged := 0
	for _, count := range groups {
		if count == 2 {
			merged++
		} else if count != 1 {
			t.Fatalf("unexpected count %d", count)
		}
	}
	if merged != 1 {
		t.Fatalf("merged groups=%d", merged)
	}
	at := time.Now().UTC().Truncate(time.Microsecond)
	for i := 0; i < 53; i++ {
		session := first
		if i%2 == 1 {
			session = second
		}
		if _, err := conn.Exec(ctx, `INSERT INTO chat_message (id,chat_session_id,created_at,content,source_payload) VALUES ($1,$2,$3,$4,'{"action":"reply","steps":[{"seq":1,"type":"thinking","content":"reason"}]}')`, uid(), session, at, fmt.Sprintf("message-%d", i)); err != nil {
			t.Fatal(err)
		}
	}
	for _, kind := range []string{"channel_command", "onboarding_kickoff"} {
		if _, err := conn.Exec(ctx, `INSERT INTO chat_message (id,chat_session_id,created_at,message_kind) VALUES ($1,$2,$3,$4)`, uid(), first, at, kind); err != nil {
			t.Fatal(err)
		}
	}
	mp := db.ListCoordinatorConversationMessagesParams{WorkspaceID: workspace, AgentID: agent, SessionID: second, PageLimit: 50}
	page, err := q.ListCoordinatorConversationMessages(ctx, mp)
	if err != nil {
		t.Fatal(err)
	}
	if len(page) != 50 {
		t.Fatalf("messages page=%d", len(page))
	}
	seen := map[pgtype.UUID]bool{}
	sessions := map[pgtype.UUID]bool{}
	for _, m := range page {
		seen[m.ID] = true
		sessions[m.ChatSessionID] = true
		if len(m.SourcePayload) == 0 {
			t.Fatal("lost coordinator trace")
		}
	}
	if len(sessions) != 2 {
		t.Fatal("transcript did not span both judgments")
	}
	mp.BeforeCreatedAt = page[49].CreatedAt
	mp.BeforeID = page[49].ID
	older, err := q.ListCoordinatorConversationMessages(ctx, mp)
	if err != nil {
		t.Fatal(err)
	}
	if len(older) != 3 {
		t.Fatalf("older messages=%d", len(older))
	}
	for _, m := range older {
		if seen[m.ID] {
			t.Fatal("duplicate at equal-timestamp cursor boundary")
		}
	}
	for _, anchor := range []pgtype.UUID{missing1, foreignAgent, uid()} {
		mp.SessionID = anchor
		mp.BeforeCreatedAt = pgtype.Timestamptz{}
		mp.BeforeID = pgtype.UUID{}
		rows, err := q.ListCoordinatorConversationMessages(ctx, mp)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) != 0 {
			t.Fatal("conversation isolation leaked messages")
		}
	}
}
