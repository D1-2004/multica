package service

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestDirectConnectionReuseFollowsConversationSceneDatabase(t *testing.T) {
	f := directDatabase(t)
	ctx := context.Background()
	owner := scene.Owner{WorkspaceID: util.MustParseUUID(f.request.Task.Scope.WorkspaceID), AgentID: util.MustParseUUID(f.request.Task.Scope.AgentID)}
	resolve := func(org, kind, conversation, uid, staff string) fcE2BTaskScope {
		t.Helper()
		registered, err := scene.Resolve(ctx, f.service.Queries, owner, scene.DingTalkConversation(org, kind, conversation), scene.Observation{KindStated: true})
		if err != nil {
			t.Fatal(err)
		}
		request := f.request
		request.Task.ID = uuid.NewString()
		request.Task.Scope.TenantOrgID = org
		request.Task.Scope.Scene = scene.RefOf(registered)
		request.Task.RequesterRef = "dingtalk:" + org + ":uid:" + uid
		request.Context, err = json.Marshal(map[string]any{"dispatch_event_data": map[string]any{
			"conversation": map[string]string{"type": kind, "openConversationId": conversation},
			"sender":       map[string]string{"uid": uid, "staffId": staff},
		}})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := directTaskContext(request)
		if err != nil {
			t.Fatal(err)
		}
		queue := db.AgentTaskQueue{ID: util.MustParseUUID(uuid.NewString()), AgentID: owner.AgentID, Context: raw}
		if _, valid := ParseDirectTaskContext(queue); !valid {
			t.Fatal("fixture is not a valid Direct execution")
		}
		scope, selected, reason := connectionReuseScope(queue, true)
		if !selected || reason != "" || scope.typ != fcE2BScopeTypeScene || scope.sceneID != util.UUIDToString(registered.ID) {
			t.Fatalf("Direct should retain the conversation's scene scope: %+v selected=%v reason=%s", scope, selected, reason)
		}
		return scope
	}

	groupA := resolve("direct-org", scene.KindGroup, "cidSharedGroup", "alice", "")
	groupB := resolve("direct-org", scene.KindGroup, "cidSharedGroup", "bob", "")
	if groupA.id != groupB.id || groupA.actorKey != "" || groupB.actorKey != "" {
		t.Fatal("same conversation without a personal layer must keep its public scene bucket")
	}
	dmA := resolve("direct-org", scene.KindDM, "cidAliceDM", "alice", "")
	dmB := resolve("direct-org", scene.KindDM, "cidBobDM", "bob", "")
	if dmA.sceneID == dmB.sceneID || dmA.id == dmB.id || dmA.id == groupA.id || dmB.id == groupA.id {
		t.Fatal("different conversation IDs shared a scene or sandbox bucket")
	}
	otherTenant := resolve("other-org", scene.KindDM, "cidAliceDM", "alice", "")
	if otherTenant.sceneID == dmA.sceneID || otherTenant.id == dmA.id {
		t.Fatal("the same external conversation ID crossed the tenant boundary")
	}
	personal := resolve("direct-org", scene.KindGroup, "cidSharedGroup", "alice", "alice-staff")
	if personal.sceneID != groupA.sceneID || personal.id == groupA.id || personal.actorKey != "alice-staff" {
		t.Fatal("verified personal configuration must remain an additional dimension of the same scene")
	}
}
