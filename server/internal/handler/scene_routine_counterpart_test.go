package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

// insertEmployeeDMMessage records one admitted user message of the fixture's
// 1:1 chat scene the way the EmployeeLoop entry stores it: a ready unified
// receipt and its Employee consumption carrying the dispatch envelope.
func insertEmployeeDMMessage(t *testing.T, f *ctxcapFixture, sceneID, sender, reason string, age time.Duration) {
	t.Helper()
	insertEmployeeDMWindow(t, f, sceneID, map[string]any{}, []map[string]any{{"senderOpenDingTalkId": sender}}, reason, age)
}

// insertEmployeeDMWindow stores one admitted window with an envelope sender
// and per-message identities, e.g. a DWS native message whose sender the
// address book resolved to a staffId while the envelope keeps the
// openDingTalkId.
func insertEmployeeDMWindow(t *testing.T, f *ctxcapFixture, sceneID string, envelopeSender map[string]any, messages []map[string]any, reason string, age time.Duration) {
	t.Helper()
	ctx := context.Background()
	agentID := uuidToString(f.agent)
	t.Cleanup(func() {
		bg := context.Background()
		_, _ = testPool.Exec(bg, `DELETE FROM employee_event_consumption WHERE agent_id = $1`, agentID)
		_, _ = testPool.Exec(bg, `DELETE FROM scene_event_receipt WHERE agent_id = $1`, agentID)
	})
	at := time.Now().Add(-age)
	var receiptID string
	if err := testPool.QueryRow(ctx, `INSERT INTO scene_event_receipt
		(workspace_id, agent_id, principal_id, tenant_org_id, source, source_event_id, fingerprint, envelope, scene_id, route, state, config_version, created_at)
		VALUES ($1, $2, $3, $4, 'messagerouter/test', $5, 'fp', '{"category":"user_message"}'::jsonb, $6, 'unified', 'ready', 'test', $7)
		RETURNING id::text`, testWorkspaceID, agentID, testUserID, ctxcapOrg, uuid.NewString(), sceneID, at).Scan(&receiptID); err != nil {
		t.Fatal(err)
	}
	payload, _ := json.Marshal(map[string]any{
		"principal_id": testUserID,
		"command": map[string]any{
			"event_receipt_id": receiptID,
			"agent_scene":      map[string]any{"scene_id": sceneID},
			"event": map[string]any{"data": map[string]any{
				"sender":   envelopeSender,
				"messages": windowMessages(messages),
			}},
		},
	})
	if _, err := testPool.Exec(ctx, `INSERT INTO employee_event_consumption
		(workspace_id, agent_id, tenant_org_id, scene_id, receipt_id, owner_loop, config_revision, principal_id, payload, state, reason, created_at)
		VALUES ($1, $2, $3, $4, $5, 'employee', 'test', $6, $7::jsonb, 'completed', $8, $9)`,
		testWorkspaceID, agentID, ctxcapOrg, sceneID, receiptID, testUserID, payload, reason, at); err != nil {
		t.Fatal(err)
	}
}

// An agent in employee mode never has Coordinator jobs: its 1:1 chat
// counterpart is the sender of the scene's admitted Employee messages. The
// configure page can then create a dm routine that sends to that person.
func TestSceneRoutineDMCounterpartFromEmployeeFacts(t *testing.T) {
	f, a := routineFixture(t)
	f.registerDirectScene(t)
	ctx := context.Background()

	if _, err := f.h.sceneRoutineDMCounterpart(ctx, a, ctxcapDirectScene); routineCode(err) != "dm_target_unknown" {
		t.Fatalf("no message yet: %v", err)
	}
	// A held message (the agent's own echo, an unsupported source) names
	// nobody; another scene's messages never count.
	insertEmployeeDMMessage(t, f, ctxcapDirectScene, "$:LWCP_v1:$self", "self_message", time.Minute)
	insertEmployeeDMMessage(t, f, ctxcapScene, "$:LWCP_v1:$group-member", "", time.Minute)
	if _, err := f.h.sceneRoutineDMCounterpart(ctx, a, ctxcapDirectScene); routineCode(err) != "dm_target_unknown" {
		t.Fatalf("held or foreign messages must not name a counterpart: %v", err)
	}

	insertEmployeeDMMessage(t, f, ctxcapDirectScene, "$:LWCP_v1:$alice", "", 30*time.Second)
	insertEmployeeDMMessage(t, f, ctxcapDirectScene, "$:LWCP_v1:$alice", "", 10*time.Second)
	cp, err := f.h.sceneRoutineDMCounterpart(ctx, a, ctxcapDirectScene)
	if err != nil || cp.OpenDingTalkID != "$:LWCP_v1:$alice" {
		t.Fatalf("counterpart = %+v %v", cp, err)
	}

	// The configure page creates the dm routine with that counterpart.
	body := map[string]any{"title": "Nightly check", "instructions": "Check my calendar.", "trigger": map[string]any{"kind": "schedule", "cron": "0 21 * * *"}}
	req := newRequest(http.MethodPost, "/api/agents/"+a.ID+"/tenants/"+ctxcapOrg+"/context/scene/"+ctxcapDirectScene+"/routines", body)
	rctx := chi.NewRouteContext()
	rctx.URLParams.Add("id", a.ID)
	rctx.URLParams.Add("orgId", ctxcapOrg)
	rctx.URLParams.Add("scopeType", "scene")
	rctx.URLParams.Add("scopeKey", ctxcapDirectScene)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
	rec := httptest.NewRecorder()
	f.h.createSceneRoutineHTTP(rec, req, a, ctxcapDirectScene, routineMember(), sceneRoutineInput{
		Title: "Nightly check", Instructions: "Check my calendar.", Trigger: sceneRoutineTrigger{Kind: "schedule", Cron: "0 21 * * *"},
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("create dm routine: %d %s", rec.Code, rec.Body.String())
	}
	var created sceneRoutineResult
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	notice, err := f.h.routineNoticeInput(ctx, f.h.Queries, mustRoutine(t, f, a, created.Routine.ID), "hello")
	if err != nil || notice.IsGroup || notice.SenderOpenDingTalkID != "$:LWCP_v1:$alice" {
		t.Fatalf("dm notice = %+v %v", notice, err)
	}
}

// Two different people among a 1:1 chat's recent senders (or a Coordinator
// sender that disagrees with the Employee one) cannot be told apart, so the
// routine has no target instead of a guessed one.
func TestSceneRoutineDMCounterpartNeverGuesses(t *testing.T) {
	f, a := routineFixture(t)
	f.registerDirectScene(t)
	ctx := context.Background()
	insertEmployeeDMMessage(t, f, ctxcapDirectScene, "$:LWCP_v1:$alice", "", 30*time.Second)
	insertEmployeeDMMessage(t, f, ctxcapDirectScene, "$:LWCP_v1:$mallory", "", 10*time.Second)
	if _, err := f.h.sceneRoutineDMCounterpart(ctx, a, ctxcapDirectScene); routineCode(err) != "dm_target_ambiguous" {
		t.Fatalf("two senders: %v", err)
	}
}

// A Coordinator agent keeps resolving its counterpart from its dispatch jobs,
// and agrees with Employee facts of the same person.
func TestSceneRoutineDMCounterpartFromCoordinatorJobs(t *testing.T) {
	f, a := routineFixture(t)
	f.cleanupScenes(t)
	ctx := context.Background()
	f.insertCoordinatorJob(t, testWorkspaceID, "digital_employee", ctxcapCoordinatorDirect, "single", "",
		map[string]any{"displayName": "Alice", "openDingTalkId": "$:LWCP_v1:$alice"}, ctxcapOrg, time.Minute)
	sceneID := directSceneOf(t, f, ctxcapCoordinatorDirect)
	cp, err := f.h.sceneRoutineDMCounterpart(ctx, a, sceneID)
	if err != nil || cp.OpenDingTalkID != "$:LWCP_v1:$alice" {
		t.Fatalf("coordinator counterpart = %+v %v", cp, err)
	}
	insertEmployeeDMMessage(t, f, sceneID, "$:LWCP_v1:$alice", "", 10*time.Second)
	if cp, err := f.h.sceneRoutineDMCounterpart(ctx, a, sceneID); err != nil || cp.OpenDingTalkID != "$:LWCP_v1:$alice" {
		t.Fatalf("same person in both loops = %+v %v", cp, err)
	}
	insertEmployeeDMMessage(t, f, sceneID, "$:LWCP_v1:$bob", "", 5*time.Second)
	if _, err := f.h.sceneRoutineDMCounterpart(ctx, a, sceneID); routineCode(err) != "dm_target_ambiguous" {
		t.Fatalf("loops disagree: %v", err)
	}
}

func directSceneOf(t *testing.T, f *ctxcapFixture, cid string) string {
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `SELECT id::text FROM agent_scene WHERE agent_id = $1 AND external_scene_id = $2`, uuidToString(f.agent), cid).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func windowMessages(messages []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(messages))
	for _, m := range messages {
		msg := map[string]any{"openMsgId": "msg-" + uuid.NewString()[:8], "text": "hello"}
		for k, v := range m {
			msg[k] = v
		}
		out = append(out, msg)
	}
	return out
}

// A DWS native message names its sender by staffId (resolved through the
// address book), so the window's stamping keeps it and the openDingTalkId
// stays on the envelope. A single-message window's envelope sender is that
// message's sender; a multi-person window's envelope names nobody.
func TestSceneRoutineDMCounterpartFromNativeEmployeeMessages(t *testing.T) {
	f, a := routineFixture(t)
	f.registerDirectScene(t)
	ctx := context.Background()
	insertEmployeeDMWindow(t, f, ctxcapDirectScene, map[string]any{"openDingTalkId": "$:LWCP_v1:$native-alice", "staffId": "staff-alice"},
		[]map[string]any{{"senderStaffId": "staff-alice"}}, "", 20*time.Second)
	cp, err := f.h.sceneRoutineDMCounterpart(ctx, a, ctxcapDirectScene)
	if err != nil || cp.OpenDingTalkID != "$:LWCP_v1:$native-alice" {
		t.Fatalf("native counterpart = %+v %v", cp, err)
	}
	// A two-message window's envelope sender is not attributed to its
	// messages, so it adds no second name.
	insertEmployeeDMWindow(t, f, ctxcapDirectScene, map[string]any{"openDingTalkId": "$:LWCP_v1:$someone-else"},
		[]map[string]any{{"senderStaffId": "staff-alice"}, {"senderStaffId": "staff-alice"}}, "", 10*time.Second)
	if cp, err := f.h.sceneRoutineDMCounterpart(ctx, a, ctxcapDirectScene); err != nil || cp.OpenDingTalkID != "$:LWCP_v1:$native-alice" {
		t.Fatalf("multi-message envelope leaked into the counterpart: %+v %v", cp, err)
	}
}
