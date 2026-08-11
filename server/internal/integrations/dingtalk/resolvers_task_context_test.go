package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/integrations/channel"
	"github.com/multica-ai/multica/server/internal/integrations/channel/engine"
	"github.com/multica-ai/multica/server/internal/integrations/orgemphsf"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	legacyDingTalkRobotIdentityJSONKey            = "dingtalk_robot_identity"
	legacyDingTalkRobotIdentityUnavailableJSONKey = "dingtalk_robot_identity_unavailable"
	testASBDWSRuntimeMetadata                     = `{
		"kind":"cloud-sandbox",
		"sandbox_backend":"asb",
		"provider":"hermes",
		"artifact_kind":"oci_image",
		"artifact_ref":"registry.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"artifact_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"capabilities":["hermes","dws"]
	}`
)

type taskContextQueriesStub struct {
	agent   db.Agent
	runtime db.AgentRuntime
}

func (s *taskContextQueriesStub) GetAgent(context.Context, pgtype.UUID) (db.Agent, error) {
	return s.agent, nil
}

func (s *taskContextQueriesStub) GetAgentRuntime(context.Context, pgtype.UUID) (db.AgentRuntime, error) {
	return s.runtime, nil
}

type robotEmployeeResolverStub struct {
	employee orgemphsf.Employee
	err      error
	corpID   string
	staffID  string
}

func (s *robotEmployeeResolverStub) ResolveEmployeeByCorpID(_ context.Context, corpID, staffID string) (orgemphsf.Employee, error) {
	s.corpID = corpID
	s.staffID = staffID
	return s.employee, s.err
}

func newRobotTaskContextResolver(employee *robotEmployeeResolverStub) *robotTaskContextResolver {
	return &robotTaskContextResolver{
		q: &taskContextQueriesStub{
			agent:   db.Agent{RuntimeID: pgtype.UUID{Bytes: [16]byte{1}, Valid: true}},
			runtime: db.AgentRuntime{RuntimeMode: "cloud", Metadata: []byte(testASBDWSRuntimeMetadata)},
		},
		employees: employee,
	}
}

func taskContextMessage(t *testing.T, corpID, staffID string) channel.InboundMessage {
	t.Helper()
	raw, err := json.Marshal(dingtalkRawEvent{
		SenderCorpID:  corpID,
		SenderStaffID: staffID,
		SenderNick:    "当前对话者",
		StreamSource: &protocol.DingTalkStreamSource{
			Hostname:     "stream-host-a",
			NodeID:       "node-a",
			ConnectionID: "node-a-g1",
		},
	})
	if err != nil {
		t.Fatalf("marshal raw message: %v", err)
	}
	return channel.InboundMessage{MessageID: "stream-message-1", Raw: raw}
}

func TestRobotTaskContextResolverCarriesStreamSessionReplyLocator(t *testing.T) {
	raw, err := json.Marshal(dingtalkRawEvent{
		AgentIdentityContextToken:          "trusted-token",
		AgentIdentityContextTokenExpiresAt: 4102444800000,
		SessionWebhook:                     "https://oapi.example/session-reply",
		SessionWebhookExpiredTime:          123456789,
	})
	if err != nil {
		t.Fatal(err)
	}
	contextJSON, err := (&robotTaskContextResolver{}).ResolveTaskContext(
		context.Background(), engine.ResolvedInstallation{}, channel.InboundMessage{Raw: raw},
	)
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(contextJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload["completion_callback"]; present {
		t.Fatalf("direct Stream task context unexpectedly contains completion_callback: %s", contextJSON)
	}
	var reply dingtalkSessionReplyContext
	if err := json.Unmarshal(payload[dingtalkSessionReplyContextKey], &reply); err != nil {
		t.Fatalf("decode Stream reply context: %v", err)
	}
	if reply.Webhook != "https://oapi.example/session-reply" || reply.ExpiresAt != 123456789 {
		t.Fatalf("Stream reply context = %#v", reply)
	}
}

func TestNewDingTalkResolverSetWiresEmployeeResolver(t *testing.T) {
	employees := &robotEmployeeResolverStub{}
	set := NewDingTalkResolverSet(nil, nil, nil, nil, nil, employees, nil, nil, nil)
	resolver, ok := set.TaskContext.(*robotTaskContextResolver)
	if !ok {
		t.Fatalf("TaskContext resolver = %T", set.TaskContext)
	}
	if resolver.employees != employees {
		t.Fatal("DingTalk resolver set did not wire the employee resolver")
	}
}

func TestRobotTaskContextResolverContinuesWithoutMissingOrganizationIdentity(t *testing.T) {
	employee := &robotEmployeeResolverStub{}
	resolver := newRobotTaskContextResolver(employee)
	contextJSON, err := resolver.ResolveTaskContext(context.Background(), engine.ResolvedInstallation{}, taskContextMessage(t, "", "Staff-A_106201"))
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	assertNoTaskIdentityToken(t, contextJSON)
	if employee.corpID != "" || employee.staffID != "" {
		t.Fatalf("employee resolver must not run without organization identity: corp=%q staff=%q", employee.corpID, employee.staffID)
	}
}

func TestRobotTaskContextResolverPrefersTrustedExternalIdentityToken(t *testing.T) {
	message, err := InboundFromHTTPCallback(HTTPCallbackMessage{
		ConversationID:                "conversation-1",
		ConversationType:              "single",
		MessageID:                     "message-1",
		SenderUID:                     "123456",
		SenderOrgID:                   "654321",
		SenderName:                    "黄谣",
		Text:                          "hello",
		IdentityContextToken:          "sealed-router-context",
		IdentityContextTokenExpiresAt: 4102444800000,
	}, "client-1", "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("InboundFromHTTPCallback: %v", err)
	}

	contextJSON, err := (&robotTaskContextResolver{}).ResolveTaskContext(
		context.Background(),
		engine.ResolvedInstallation{},
		message,
	)
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(contextJSON, &payload); err != nil {
		t.Fatalf("decode task context: %v", err)
	}
	var token string
	if err := json.Unmarshal(payload[protocol.AgentIdentityContextTokenJSONKey], &token); err != nil {
		t.Fatalf("decode external identity token: %v", err)
	}
	if token != "sealed-router-context" {
		t.Fatalf("external identity token = %q", token)
	}
	var expiresAt int64
	if err := json.Unmarshal(payload[protocol.AgentIdentityContextTokenExpiresAtJSONKey], &expiresAt); err != nil {
		t.Fatalf("decode external identity expiry: %v", err)
	}
	if expiresAt != 4102444800000 {
		t.Fatalf("external identity expiry = %d", expiresAt)
	}
	var source string
	if err := json.Unmarshal(payload[protocol.AgentIdentityContextTokenSourceJSONKey], &source); err != nil {
		t.Fatalf("decode external identity source: %v", err)
	}
	if source != protocol.AgentIdentityContextTokenSourceExternal {
		t.Fatalf("external identity source = %q", source)
	}
	if _, present := payload[legacyDingTalkRobotIdentityJSONKey]; present {
		t.Fatal("local robot identity must not override external identity")
	}
	assertDingTalkConversationInitiator(t, contextJSON, "黄谣")
}

func TestRobotTaskContextResolverOverridesDispatchInitiatorWithCurrentSender(t *testing.T) {
	fakeInitiator, err := json.Marshal(map[string]any{
		protocol.DingTalkConversationInitiatorJSONKey: map[string]string{"display_name": "机器人创建者"},
	})
	if err != nil {
		t.Fatal(err)
	}
	message, err := InboundFromHTTPCallback(HTTPCallbackMessage{
		ConversationID:                "conversation-1",
		ConversationType:              "single",
		MessageID:                     "message-1",
		SenderUID:                     "123456",
		SenderOrgID:                   "654321",
		SenderName:                    "当前对话者",
		Text:                          "hello",
		IdentityContextToken:          "sealed-router-context",
		IdentityContextTokenExpiresAt: 4102444800000,
		DispatchContext:               fakeInitiator,
	}, "client-1", "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("InboundFromHTTPCallback: %v", err)
	}
	contextJSON, err := (&robotTaskContextResolver{}).ResolveTaskContext(context.Background(), engine.ResolvedInstallation{}, message)
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	assertDingTalkConversationInitiator(t, contextJSON, "当前对话者")
}

func TestRobotTaskContextResolverKeepsExplicitEmptyConversationInitiator(t *testing.T) {
	raw, err := json.Marshal(dingtalkRawEvent{
		AgentIdentityContextToken:          "trusted-token",
		AgentIdentityContextTokenExpiresAt: 4102444800000,
	})
	if err != nil {
		t.Fatal(err)
	}
	contextJSON, err := (&robotTaskContextResolver{}).ResolveTaskContext(
		context.Background(), engine.ResolvedInstallation{}, channel.InboundMessage{Raw: raw},
	)
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	assertDingTalkConversationInitiator(t, contextJSON, "")
}

func TestRobotTaskContextResolverLeavesHTTPIdentityToLauncherFallback(t *testing.T) {
	message, err := InboundFromHTTPCallback(HTTPCallbackMessage{
		ConversationID:   "conversation-1",
		ConversationType: "single",
		MessageID:        "message-1",
		SenderID:         "$:opaque-open-dingtalk-id",
		Text:             "hello",
	}, "client-1", "11111111-1111-1111-1111-111111111111")
	if err != nil {
		t.Fatalf("InboundFromHTTPCallback: %v", err)
	}
	if message.Source.SenderID != "$:opaque-open-dingtalk-id" {
		t.Fatalf("route sender id = %q", message.Source.SenderID)
	}

	contextJSON, err := newRobotTaskContextResolver(&robotEmployeeResolverStub{}).ResolveTaskContext(
		context.Background(),
		engine.ResolvedInstallation{},
		message,
	)
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	assertNoTaskIdentityToken(t, contextJSON)
}

func TestRobotTaskContextResolverContinuesWithoutIdentityAfterValidationError(t *testing.T) {
	validationErr := &orgemphsf.ValidationError{Field: "staff_id"}
	resolver := newRobotTaskContextResolver(&robotEmployeeResolverStub{err: validationErr})

	contextJSON, err := resolver.ResolveTaskContext(
		context.Background(),
		engine.ResolvedInstallation{},
		taskContextMessage(t, "ding-corp", "Staff-A_106201"),
	)
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	assertNoTaskIdentityToken(t, contextJSON)
}

func TestRobotTaskContextResolverRetriesIdentityInfrastructureError(t *testing.T) {
	infraErr := errors.New("HSF unavailable")
	resolver := newRobotTaskContextResolver(&robotEmployeeResolverStub{err: infraErr})

	contextJSON, err := resolver.ResolveTaskContext(
		context.Background(),
		engine.ResolvedInstallation{},
		taskContextMessage(t, "ding-corp", "Staff-A_106201"),
	)
	if err == nil || !strings.Contains(err.Error(), "HSF unavailable") {
		t.Fatalf("ResolveTaskContext error = %v", err)
	}
	if contextJSON != nil {
		t.Fatalf("failed identity resolution returned task context: %s", contextJSON)
	}
}

func TestRobotTaskContextResolverBuildsDeferredIdentityForOpaqueStaffID(t *testing.T) {
	employee := &robotEmployeeResolverStub{employee: orgemphsf.Employee{
		UID:     "24710833",
		OrgID:   "439446171",
		StaffID: "Staff-A_106201",
	}}
	resolver := newRobotTaskContextResolver(employee)

	contextJSON, err := resolver.ResolveTaskContext(
		context.Background(),
		engine.ResolvedInstallation{},
		taskContextMessage(t, "ding-corp", "Staff-A_106201"),
	)
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	if employee.corpID != "ding-corp" || employee.staffID != "Staff-A_106201" {
		t.Fatalf("resolver arguments = corpID %q staffID %q", employee.corpID, employee.staffID)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(contextJSON, &payload); err != nil {
		t.Fatalf("decode task context: %v", err)
	}
	if _, present := payload[protocol.AgentIdentityContextTokenJSONKey]; present {
		t.Fatalf("task context contains an eagerly issued ContextToken: %s", contextJSON)
	}
	var externalIdentity dingtalkTaskExternalIdentity
	if err := json.Unmarshal(payload["external_identity"], &externalIdentity); err != nil {
		t.Fatalf("decode deferred external identity: %v", err)
	}
	if externalIdentity.DWS.UID != "24710833" || externalIdentity.DWS.OrgID != "439446171" {
		t.Fatalf("deferred external identity = %#v", externalIdentity)
	}
	if _, present := payload[legacyDingTalkRobotIdentityJSONKey]; present {
		t.Fatal("raw sender identity leaked into task context")
	}
}

func TestRobotTaskContextResolverDefersStreamContextTokenUntilRuntimeLaunch(t *testing.T) {
	employee := &robotEmployeeResolverStub{employee: orgemphsf.Employee{
		UID:     "24710833",
		OrgID:   "439446171",
		StaffID: "Staff-A_106201",
	}}
	runtimeID := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	resolver := &robotTaskContextResolver{
		q: &taskContextQueriesStub{
			agent:   db.Agent{RuntimeID: runtimeID},
			runtime: db.AgentRuntime{RuntimeMode: "cloud", Metadata: []byte(testASBDWSRuntimeMetadata)},
		},
		employees: employee,
	}
	raw, err := json.Marshal(dingtalkRawEvent{
		SenderCorpID:  "ding-corp",
		SenderStaffID: "Staff-A_106201",
		StreamSource: &protocol.DingTalkStreamSource{
			Hostname:     "stream-host-a",
			NodeID:       "node-a",
			ConnectionID: "node-a-g1",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	installation := engine.ResolvedInstallation{
		ID:      pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
		AgentID: pgtype.UUID{Bytes: [16]byte{4}, Valid: true},
	}
	contextJSON, err := resolver.ResolveTaskContext(context.Background(), installation, channel.InboundMessage{
		MessageID: "stream-message-1",
		Raw:       raw,
	})
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(contextJSON, &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload[protocol.AgentIdentityContextTokenJSONKey]; present {
		t.Fatalf("task context contains an eagerly issued ContextToken: %s", contextJSON)
	}
	if _, present := payload[protocol.AgentIdentityContextTokenExpiresAtJSONKey]; present {
		t.Fatalf("task context contains an eager ContextToken expiry: %s", contextJSON)
	}
	if _, present := payload[protocol.AgentIdentityContextTokenSourceJSONKey]; present {
		t.Fatalf("task context contains an eager ContextToken source: %s", contextJSON)
	}
	var externalIdentity dingtalkTaskExternalIdentity
	if err := json.Unmarshal(payload["external_identity"], &externalIdentity); err != nil {
		t.Fatalf("decode deferred external identity: %v", err)
	}
	if externalIdentity.DWS.UID != "24710833" || externalIdentity.DWS.OrgID != "439446171" {
		t.Fatalf("deferred external identity = %#v", externalIdentity)
	}
}

func TestRobotTaskContextResolverCarriesStreamSourceWithIdentity(t *testing.T) {
	employee := &robotEmployeeResolverStub{employee: orgemphsf.Employee{
		UID:   "24710833",
		OrgID: "439446171",
	}}
	resolver := newRobotTaskContextResolver(employee)
	raw, err := json.Marshal(dingtalkRawEvent{
		SenderCorpID:  "ding-corp",
		SenderStaffID: "Staff-A_106201",
		StreamSource: &protocol.DingTalkStreamSource{
			Hostname:     "dt-fde-multica033008056137.pre.na620",
			NodeID:       "node-a",
			ConnectionID: "node-a-g3",
		},
	})
	if err != nil {
		t.Fatalf("marshal raw message: %v", err)
	}
	contextJSON, err := resolver.ResolveTaskContext(
		context.Background(),
		engine.ResolvedInstallation{},
		channel.InboundMessage{Raw: raw},
	)
	if err != nil {
		t.Fatalf("ResolveTaskContext: %v", err)
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(contextJSON, &payload); err != nil {
		t.Fatalf("decode task context: %v", err)
	}
	var source protocol.DingTalkStreamSource
	if err := json.Unmarshal(payload[protocol.DingTalkStreamSourceJSONKey], &source); err != nil {
		t.Fatalf("decode Stream source: %v", err)
	}
	if source.Hostname != "dt-fde-multica033008056137.pre.na620" || source.NodeID != "node-a" || source.ConnectionID != "node-a-g3" {
		t.Fatalf("Stream source = %+v", source)
	}
	if _, ok := payload["external_identity"]; !ok {
		t.Fatal("deferred DWS identity missing from combined task context")
	}
	if _, ok := payload[protocol.AgentIdentityContextTokenJSONKey]; ok {
		t.Fatal("combined task context contains an eagerly issued ContextToken")
	}
}

func assertNoTaskIdentityToken(t *testing.T, contextJSON []byte) {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(contextJSON, &payload); err != nil {
		t.Fatalf("decode task context: %v", err)
	}
	for _, key := range []string{
		protocol.AgentIdentityContextTokenJSONKey,
		protocol.AgentIdentityContextTokenExpiresAtJSONKey,
		protocol.AgentIdentityContextTokenSourceJSONKey,
		"external_identity",
		legacyDingTalkRobotIdentityJSONKey,
		legacyDingTalkRobotIdentityUnavailableJSONKey,
	} {
		if _, present := payload[key]; present {
			t.Fatalf("unexpected task identity field %q in %s", key, contextJSON)
		}
	}
}

func assertDingTalkConversationInitiator(t *testing.T, contextJSON []byte, wantName string) {
	t.Helper()
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(contextJSON, &payload); err != nil {
		t.Fatalf("decode task context: %v", err)
	}
	encoded, present := payload[protocol.DingTalkConversationInitiatorJSONKey]
	if !present {
		t.Fatal("DingTalk conversation initiator missing from task context")
	}
	var initiator protocol.DingTalkConversationInitiator
	if err := json.Unmarshal(encoded, &initiator); err != nil {
		t.Fatalf("decode DingTalk conversation initiator: %v", err)
	}
	if initiator.DisplayName != wantName {
		t.Fatalf("conversation initiator = %q, want %q", initiator.DisplayName, wantName)
	}
}
