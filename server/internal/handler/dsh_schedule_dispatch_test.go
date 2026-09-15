package handler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/dshschedule"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type scheduleClaimDB struct {
	db.DBTX
	values []any
	err    error
	calls  int
}

func (d *scheduleClaimDB) QueryRow(context.Context, string, ...any) pgx.Row { d.calls++; return d }
func (d *scheduleClaimDB) Scan(dest ...any) error {
	if d.err != nil {
		return d.err
	}
	for i, v := range d.values {
		reflect.ValueOf(dest[i]).Elem().Set(reflect.ValueOf(v))
	}
	return nil
}

func TestDSHScheduleClaimRequiresReceiptAndNativeTransport(t *testing.T) {
	for _, mode := range []string{"valid", "other-provider", "other-backend", "incapable", "missing-receipt", "db-outage", "wrong-occurrence", "human-originator", "wrong-scope", "ordinary-task"} {
		t.Run(mode, func(t *testing.T) {
			uid := func() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
			rt := db.AgentRuntime{ID: uid(), WorkspaceID: uid(), Provider: "dsh", RuntimeMode: "cloud", Metadata: []byte(`{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc","template_id":"fixture"}`)}
			task := db.AgentTaskQueue{ID: uid(), AgentID: uid(), RuntimeID: rt.ID, TriggerEvidenceKind: pgtype.Text{String: dshschedule.EvidenceKind, Valid: true}, OriginatorSource: pgtype.Text{String: "trigger_owner", Valid: true}}
			r := dshschedule.Record{Key: dshschedule.Key{WorkspaceID: uuid.UUID(rt.WorkspaceID.Bytes), AgentID: uuid.UUID(task.AgentID.Bytes), SessionID: uuid.NewString(), ScheduleID: "schedule-1"}, OwnerMemberID: uuid.New(), SourceTaskID: uuid.New(), Prompt: "reminder", FirstDue: time.Date(2026, 9, 15, 0, 0, 0, 0, time.UTC)}
			due, err := dshschedule.Plan(r, r.FirstDue, r.FirstDue)
			if err != nil {
				t.Fatal(err)
			}
			task.TriggerEvidenceRefID = pgtype.UUID{Bytes: due.RequestID, Valid: true}
			database := &scheduleClaimDB{values: []any{r.SessionID, r.ScheduleID, due.At, due.RequestID, r.OwnerMemberID, r.SourceTaskID, r.Prompt, r.FirstDue, r.EverySeconds, "task", r.SourceTaskID}}
			h := &Handler{DB: database}
			request := httptest.NewRequest(http.MethodPost, "/claim", nil)
			request.Header.Set("X-Client-Capabilities", protocol.DaemonCapabilityDSHNativePromptV1)
			backend := service.SandboxBackendAliyunFC
			switch mode {
			case "other-provider":
				rt.Provider = "hermes"
			case "other-backend":
				backend = service.SandboxBackendASB
			case "incapable":
				request.Header.Del("X-Client-Capabilities")
			case "missing-receipt":
				database.err = pgx.ErrNoRows
			case "db-outage":
				database.err = errors.New("database unavailable")
			case "wrong-occurrence":
				task.TriggerEvidenceRefID = uid()
			case "human-originator":
				task.OriginatorUserID = uid()
			case "wrong-scope":
				database.values[9] = "chat"
			case "ordinary-task":
				task.TriggerEvidenceKind.String = "chat"
			}
			response := AgentTaskResponse{}
			failure := h.applyDSHScheduleClaim(request, task, rt, backend, uuidToString(rt.WorkspaceID), &response)
			if mode == "ordinary-task" {
				if failure != nil || database.calls != 0 || response.DSHNativePrompt != nil {
					t.Fatal("ordinary task changed")
				}
				return
			}
			if mode == "valid" {
				if failure != nil || response.WorkspaceID != uuidToString(rt.WorkspaceID) || response.DSHNativePrompt == nil || response.DSHNativePrompt.SessionID != r.SessionID || response.DSHNativePrompt.RequestID != due.RequestID.String() {
					t.Fatal("valid reminder not transported", failure)
				}
				return
			}
			if failure == nil || response.DSHNativePrompt != nil {
				t.Fatal("invalid task received reminder")
			}
			if mode == "db-outage" && failure.status != http.StatusServiceUnavailable {
				t.Fatal("transient read classified as invalid input")
			}
		})
	}
}
