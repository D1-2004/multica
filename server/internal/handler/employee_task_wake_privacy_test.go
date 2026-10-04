package handler

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/scene"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestEmployeeTaskWakePrivateRequesterQualification(t *testing.T) {
	id := uuid.NewString()
	owner := "dingtalk:456:open_id:requester"
	registered := db.AgentScene{ID: parseUUID(id), SceneKind: scene.KindDM, TenantOrgID: "456"}
	origin := employeeentry.TaskOrigin{Anchor: employeeentry.DeliveryAnchor{SceneID: id, Conversation: true, RequesterRef: owner}}
	origin.Task.RequesterRef = owner
	cases := []string{"valid", "uid", "group", "enterprise", "other-owner", "automation", "other-tenant", "staff-only", "no-conversation", "wrong-scene", "empty"}
	for _, name := range cases {
		t.Run(name, func(t *testing.T) {
			row, o := registered, origin
			want := owner
			switch name {
			case "uid":
				o.Anchor.RequesterRef = "dingtalk:456:uid:requester"
				o.Task.RequesterRef = o.Anchor.RequesterRef
				want = o.Anchor.RequesterRef
			case "group":
				row.SceneKind = scene.KindGroup
				want = ""
			case "enterprise":
				row.SceneKind = scene.KindEnterprise
				want = ""
			case "other-owner":
				o.Task.RequesterRef = "dingtalk:456:open_id:other"
				want = ""
			case "automation":
				o.Anchor.RequesterRef = "routine:test"
				o.Task.RequesterRef = o.Anchor.RequesterRef
				want = ""
			case "other-tenant":
				row.TenantOrgID = "789"
				want = ""
			case "staff-only":
				o.Anchor.RequesterRef = "dingtalk:456:staff_id:requester"
				o.Task.RequesterRef = o.Anchor.RequesterRef
				want = ""
			case "no-conversation":
				o.Anchor.Conversation = false
				want = ""
			case "wrong-scene":
				o.Anchor.SceneID = uuid.NewString()
				want = ""
			case "empty":
				o.Anchor.RequesterRef = ""
				o.Task.RequesterRef = ""
				want = ""
			}
			if got := employeeTaskWakePrivateRequester(row, o); got != want {
				t.Fatalf("requester=%q want %q", got, want)
			}
		})
	}
}

// A real TaskWake builder must respect the same private reset as chat. The
// owner reset affects history only when MemoryPrincipal actually reaches Store.
func TestEmployeeTaskWakeHistoryUsesPrivateOwnerReset(t *testing.T) {
	f := employeeWakeDatabase(t)
	ctx := context.Background()
	if _, err := testPool.Exec(ctx, `UPDATE agent_scene SET scene_kind='dm' WHERE id=$1::uuid`, f.scope.SceneID); err != nil {
		t.Fatal(err)
	}
	w := f.h.EmployeeSceneWorker
	origin, err := w.TaskOrigin(ctx, testPool, f.scope, f.task.ID)
	if err != nil {
		t.Fatal(err)
	}
	row, err := employeeSceneFence(ctx, f.h, employeeentry.Job{Scope: f.scope})
	if err != nil {
		t.Fatal(err)
	}
	owner := employeeTaskWakePrivateRequester(row, origin)
	if owner == "" {
		t.Fatalf("fixture did not resolve real owner: %+v %s", origin.Anchor, origin.Task.RequesterRef)
	}
	now := time.Now().UTC().Add(time.Second)
	job := employeeentry.Job{ID: uuid.NewString(), Scope: f.scope, PrincipalID: origin.PrincipalID, CreatedAt: now, Items: []employeeentry.Item{{ReceiptID: origin.ReceiptID}}}
	before, err := w.store.RecentConversation(ctx, employeeentry.RecentConversationRequest{Scope: f.scope, PrincipalID: origin.HistoryPrincipalID, Before: now})
	if err != nil || len(before.Messages) == 0 {
		t.Fatalf("history precondition: %+v %v", before, err)
	}
	if _, err := testPool.Exec(ctx, `INSERT INTO employee_memory_state(workspace_id,agent_id,tenant_org_id,scene_id,scope_kind,principal_id,reset_at) VALUES($1::uuid,$2::uuid,$3,$4::uuid,'private',$5,$6)`, f.scope.WorkspaceID, f.scope.AgentID, f.scope.TenantOrgID, f.scope.SceneID, owner, now.Add(-500*time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = testPool.Exec(context.Background(), `DELETE FROM employee_memory_state WHERE agent_id=$1::uuid`, f.scope.AgentID)
	})
	wake := f.admission("privacy-builder").Wake
	input, err := w.buildTaskWakeInput(ctx, job, wake, employeeTaskWakeBinding{origin: origin})
	if err != nil {
		t.Fatal(err)
	}
	var history employeeentry.RecentConversation
	if err = json.Unmarshal([]byte(input.Input.RecentConversation), &history); err != nil {
		t.Fatalf("history was unavailable: %q %v", input.Input.RecentConversation, err)
	}
	if len(history.Messages) != 0 {
		t.Fatalf("private reset omitted from TaskWake history: %+v", history.Messages)
	}
	if _, err = testPool.Exec(ctx, `UPDATE employee_memory_state SET principal_id='dingtalk:456:open_id:other' WHERE agent_id=$1::uuid`, f.scope.AgentID); err != nil {
		t.Fatal(err)
	}
	input, err = w.buildTaskWakeInput(ctx, job, wake, employeeTaskWakeBinding{origin: origin})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(input.Input.RecentConversation, before.Messages[0].Text) {
		t.Fatalf("other owner widened reset: %s", input.Input.RecentConversation)
	}
}
