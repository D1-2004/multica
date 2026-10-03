package employeeentry

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func setMemoryReset(t *testing.T, f fixture, scope Scope, kind, principal string, at time.Time) {
	t.Helper()
	if _, err := f.pool.Exec(context.Background(), `INSERT INTO employee_memory_state(workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id,revision,reset_at) VALUES($1::uuid,$2::uuid,$3,$4::uuid,$5,$6,1,$7)`,
		scope.WorkspaceID, scope.AgentID, scope.TenantOrgID, scope.SceneID, kind, principal, at); err != nil {
		t.Fatal(err)
	}
}

func historyTexts(t *testing.T, got RecentConversation) string {
	t.Helper()
	raw, _ := json.Marshal(got.Messages)
	return string(raw)
}

func TestDMResetBoundsHistory(t *testing.T) {
	const requester = "dingtalk:org-a:open_id:source-open"
	for _, tc := range []struct {
		name, kind, principal, memoryPrincipal string
		hidden                                 bool
	}{
		{"scene reset bounds every wake", "scene", "", "", true},
		{"DM requester private reset bounds the DM", "private", requester, requester, true},
		{"a personal reset never hides group dialogue", "private", requester, "", false},
		{"another requester's reset is ignored", "private", "dingtalk:org-a:open_id:someone-else", requester, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, before := recentHistoryDatabase(t)
			scope, principal := f.admission.Scope, f.admission.Item.PrincipalID
			_, oldJob := recentHistoryInput(t, f, scope, principal, "BEFORE_RESET 候选 K6、X3", "old", before.Add(-20*time.Minute))
			recentHistoryReply(t, f, scope, oldJob, "delivered", "BEFORE_RESET_REPLY", "old-reply", "cid-test", before.Add(-19*time.Minute))
			setMemoryReset(t, f, scope, tc.kind, tc.principal, before.Add(-10*time.Minute))
			recentHistoryInput(t, f, scope, principal, "AFTER_RESET 新的一轮", "new", before.Add(-5*time.Minute))
			got, err := f.store.RecentConversation(context.Background(), RecentConversationRequest{Scope: scope, PrincipalID: principal, Before: before, MemoryPrincipal: tc.memoryPrincipal})
			if err != nil {
				t.Fatal(err)
			}
			texts := historyTexts(t, got)
			if strings.Contains(texts, "BEFORE_RESET") != !tc.hidden || !strings.Contains(texts, "AFTER_RESET") {
				t.Fatalf("hidden=%v history=%s", tc.hidden, texts)
			}
			if tc.hidden && !got.Since.Equal(before.Add(-10*time.Minute)) {
				t.Fatalf("since must be the reset time: %v", got.Since)
			}
			if !tc.hidden && !got.Since.Equal(before.Add(-24*time.Hour)) {
				t.Fatalf("since must stay the 24h window: %v", got.Since)
			}
		})
	}
}

func TestRecentConversationIncludesUnlinkedSceneHostSends(t *testing.T) {
	f, before := recentHistoryDatabase(t)
	ctx := context.Background()
	scope, principal := f.admission.Scope, f.admission.Item.PrincipalID
	send := func(scope Scope, id, state string, extra map[string]any, at time.Time) {
		t.Helper()
		input := map[string]any{"workspace_id": scope.WorkspaceID, "agent_id": scope.AgentID, "scene_id": scope.SceneID, "dws_org_id": scope.TenantOrgID, "dws_uid": "999", "conversation_id": "cid-test", "callback_url": "", "text": "SEND_" + id}
		for k, v := range extra {
			input[k] = v
		}
		raw, _ := json.Marshal(input)
		if _, err := f.pool.Exec(ctx, `INSERT INTO response_action(id,workspace_id,agent_id,request_id,kind,input,state,provider_message_id,provider_conversation_id,created_at,updated_at) VALUES($1,$2::uuid,$3::uuid,$1,'message.send',$4,$5,$6,'cid-test',$7,$7)`,
			id, scope.WorkspaceID, scope.AgentID, raw, state, "provider-"+id, at); err != nil {
			t.Fatal(err)
		}
	}
	send(scope, "routine_start", "delivered", map[string]any{"routine_run_id": uuid.NewString()}, before.Add(-3*time.Hour))
	send(scope, "plain_host_send", "delivered", nil, before.Add(-2*time.Hour))
	// Still excluded: undelivered, foreign scene/org, other provenance kinds.
	send(scope, "not_delivered", "pending", map[string]any{"routine_run_id": uuid.NewString()}, before.Add(-time.Hour))
	other := scope
	other.SceneID = uuid.NewString()
	send(other, "other_scene", "delivered", nil, before.Add(-time.Hour))
	otherOrg := scope
	otherOrg.TenantOrgID = "org-b"
	send(otherOrg, "other_org", "delivered", nil, before.Add(-time.Hour))
	send(scope, "foreign_notice", "delivered", map[string]any{"scene_notice_id": uuid.NewString()}, before.Add(-time.Hour))
	send(scope, "foreign_callback", "delivered", map[string]any{"callback_url": "/api/v1/dispatch-tasks/x/response-receipt"}, before.Add(-time.Hour))
	send(scope, "foreign_run_notice_field", "delivered", map[string]any{"employee_run_notice_id": uuid.NewString()}, before.Add(-time.Hour))
	send(scope, "foreign_invitation", "delivered", map[string]any{"invitation_action_id": uuid.NewString()}, before.Add(-time.Hour))
	send(scope, "foreign_host_notice", "delivered", nil, before.Add(-time.Hour))
	if err := RecordHostNotice(ctx, f.pool, HostNotice{ActionID: "foreign_host_notice", Scope: scope, PrincipalID: uuid.NewString(), SourceKind: HostNoticeTaskWake, SourceID: uuid.NewString(), OriginReceiptID: f.admission.Item.ReceiptID}); err != nil {
		t.Fatal(err)
	}
	send(scope, "expired", "delivered", nil, before.Add(-25*time.Hour))
	got, err := f.store.RecentConversation(ctx, RecentConversationRequest{Scope: scope, PrincipalID: principal, Before: before})
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Messages) != 2 || got.Messages[0].Text != "SEND_routine_start" || got.Messages[1].Text != "SEND_plain_host_send" || got.Messages[0].Role != "assistant" || got.Messages[0].MessageID != "provider-routine_start" {
		t.Fatalf("unlinked scene sends: %s", historyTexts(t, got))
	}
	if got.Coverage != RecentConversationCoverage || !strings.Contains(got.Coverage, "scene_host_sends") {
		t.Fatalf("coverage=%q", got.Coverage)
	}
	// The same sends are scene history for any principal of the scene.
	if got, err = f.store.RecentConversation(ctx, RecentConversationRequest{Scope: scope, PrincipalID: uuid.NewString(), Before: before}); err != nil || len(got.Messages) != 2 {
		t.Fatalf("another principal of the scene: %v %s", err, historyTexts(t, got))
	}
}
