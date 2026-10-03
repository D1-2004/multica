package a2ui

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestInteractionFromRow(t *testing.T) {
	id := uuid.MustParse("33333333-3333-4333-8333-333333333333")
	when := time.Date(2026, 10, 3, 1, 2, 3, 0, time.UTC)
	got, err := interactionFrom(db.A2uiInteraction{
		ID: pgUUID(id), PublicID: "appr:" + id.String(), WorkspaceID: pgUUID(testWorkspace), AgentID: pgUUID(testAgent),
		SenderUid: "103262", SenderOrgID: "439446171", SceneID: "scene-1", ConversationID: "cid-1",
		MessageID: "msg-1", ThreadID: "thread-1", SourceRef: "receipt-1/msg-1",
		Kind: "approval", Status: "approved", Header: "待审批", Question: "发吗",
		Request:        []byte(`{"options":[{"id":"o0","label":"同意"},{"id":"o1","label":"驳回"}],"surface_id":"s-1","operator_uid":"7"}`),
		CardBizID:      "transformer_card_x",
		EventID:        "ev-1",
		Result:         []byte(`{"outcome":"approved","selected":["o0"],"labels":["同意"]}`),
		OperatorUid:    "10001",
		IdempotencyKey: "once",
		CreatedAt:      pgtype.Timestamptz{Time: when, Valid: true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != id || got.PublicID != "appr:"+id.String() || got.Kind != KindApproval || got.Status != StatusApproved || got.MessageID != "msg-1" || got.ThreadID != "thread-1" || got.SourceRef != "receipt-1/msg-1" {
		t.Fatalf("identity = %+v", got)
	}
	if got.Result.Outcome != "approved" || got.Result.Labels[0] != "同意" || got.spec.SurfaceID != "s-1" || got.spec.OperatorUID != "7" {
		t.Fatalf("payload = %+v %#v", got.Result, got.spec)
	}
	if !got.CreatedAt.Equal(when) || !got.ResolvedAt.IsZero() {
		t.Fatalf("times = %s %s", got.CreatedAt, got.ResolvedAt)
	}
}
