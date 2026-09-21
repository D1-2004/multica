package handler

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/service"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

type nativeClaimBinding struct {
	session, request string
	err              error
	args             []any
	calls            int
}

func (r *nativeClaimBinding) QueryRow(_ context.Context, _ string, args ...any) pgx.Row {
	r.args = args
	r.calls++
	return r
}
func (r *nativeClaimBinding) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	*dest[0].(*string) = r.session
	*dest[1].(*string) = r.request
	return nil
}

func TestDSHNativeClaimPreservesPayloadAndChecksBinding(t *testing.T) {
	for _, mode := range []string{"valid", "plugin-request", "plugin-other-request", "plugin-other-session", "incapable", "other-provider", "other-backend", "other-workspace", "other-owner", "other-message", "other-request", "other-session", "missing-binding", "read-failure", "duplicate-input", "summary-changed", "null-input"} {
		t.Run(mode, func(t *testing.T) {
			id := func() pgtype.UUID { return pgtype.UUID{Bytes: uuid.New(), Valid: true} }
			workspace := id()
			rt := db.AgentRuntime{ID: id(), WorkspaceID: workspace, RuntimeMode: "cloud", Provider: "dsh", Metadata: []byte(`{"kind":"cloud-sandbox","sandbox_backend":"aliyun_fc","template_id":"test-template"}`)}
			task := db.AgentTaskQueue{ID: id(), AgentID: id(), RuntimeID: rt.ID, ChatSessionID: id()}
			task.ChatInputTaskID = task.ID
			text := "native"
			receipt := "upload"
			zone := "Asia/Shanghai"
			p := protocol.DSHNativePrompt{SessionID: "session-" + uuid.NewString(), RequestID: uuid.NewString(), Mode: "steer", Content: []protocol.DSHNativePromptPart{{Type: "text", Text: &text}, {Type: "file", ReceiptID: &receipt}}, ClientTimeZone: &zone}
			if mode == "plugin-request" || mode == "plugin-other-request" || mode == "plugin-other-session" {
				p.RequestID = "weixin-acceptance-" + uuid.NewString()
			}
			raw, err := json.Marshal(map[string]any{"dsh_native_prompt": p})
			if err != nil {
				t.Fatal(err)
			}
			msgs := []db.ChatMessage{{ID: id(), TaskID: task.ID, ChatSessionID: task.ChatSessionID, Role: "user", MessageKind: "message", Content: p.DisplayText(), SourcePayload: raw}}
			identity, err := protocol.DSHNativeRequestIdentity(p.SessionID, p.RequestID)
			if err != nil {
				t.Fatal(err)
			}
			reader := &nativeClaimBinding{session: p.SessionID, request: identity.String()}
			capable := true
			backend := service.SandboxBackendAliyunFC
			switch mode {
			case "plugin-other-request":
				other, _ := protocol.DSHNativeRequestIdentity(p.SessionID, p.RequestID+"-other")
				reader.request = other.String()
			case "plugin-other-session":
				other, _ := protocol.DSHNativeRequestIdentity("session-"+uuid.NewString(), p.RequestID)
				reader.request = other.String()
			case "incapable":
				capable = false
			case "other-provider":
				rt.Provider = "hermes"
			case "other-backend":
				backend = service.SandboxBackendASB
			case "other-workspace":
				workspace = id()
			case "other-owner":
				task.ChatInputTaskID = id()
			case "other-message":
				msgs[0].TaskID = id()
			case "other-request":
				reader.request = uuid.NewString()
			case "other-session":
				reader.session = uuid.NewString()
			case "missing-binding":
				reader.err = pgx.ErrNoRows
			case "read-failure":
				reader.err = errors.New("fixture database unavailable")
			case "duplicate-input":
				msgs = append(msgs, msgs[0])
			case "summary-changed":
				msgs[0].Content = "changed"
			case "null-input":
				msgs[0].SourcePayload = []byte(`{"dsh_native_prompt":null}`)
			}
			got, failure := loadDSHNativeClaim(context.Background(), reader, task, rt, backend, workspace, capable, msgs)
			if mode == "valid" || mode == "plugin-request" {
				if failure != nil || !reflect.DeepEqual(got, &p) {
					t.Fatalf("typed input not delivered: failure=%+v", failure)
				}
				want := []any{workspace, task.AgentID, task.ID, task.ChatSessionID}
				if !reflect.DeepEqual(reader.args, want) {
					t.Fatal("binding read did not use trusted scope")
				}
			} else if failure == nil || got != nil {
				t.Fatal("unsafe native claim accepted")
			}
			if mode == "incapable" && (reader.calls != 0 || failure.status != http.StatusServiceUnavailable) {
				t.Fatal("old daemon not refused before binding lookup")
			}
		})
	}
}

func TestDSHNativeClaimLeavesOrdinaryInputsUnchanged(t *testing.T) {
	for _, raw := range []string{``, `{}`, `{"dws":{"receipt":"fixture"}}`, `[]`} {
		msgs := []db.ChatMessage{{SourcePayload: []byte(raw)}}
		got, failure := loadDSHNativeClaim(context.Background(), nil, db.AgentTaskQueue{}, db.AgentRuntime{}, "", pgtype.UUID{}, false, msgs)
		if got != nil || failure != nil || string(withoutDSHNativeSource([]byte(raw))) != raw {
			t.Fatal("ordinary source changed")
		}
	}
	if len(withoutDSHNativeSource([]byte(`{"dsh_native_prompt":{}}`))) != 0 {
		t.Fatal("native input duplicated in generic source")
	}
	if string(withoutDSHNativeSource([]byte(`{"dsh_native_prompt":{},"dws":{"receipt":"fixture"}}`))) != `{"dws":{"receipt":"fixture"}}` {
		t.Fatal("unrelated metadata removed")
	}
}
