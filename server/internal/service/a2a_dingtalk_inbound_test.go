package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	a2aintegration "github.com/multica-ai/multica/server/internal/integrations/a2a"
	"github.com/multica-ai/multica/server/internal/integrations/agentidentityhsf"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/protocol"
)

func deapTestRequestMetadata(dialogType string, proactive bool) map[string]any {
	invocation := map[string]any{"invocationSource": "dingtalk-lui", "dialogType": dialogType}
	if proactive {
		invocation["isGroupProactiveResponse"] = true
	}
	return map[string]any{
		"context": map[string]any{
			"source": "AI_AGENT",
			"attributes": map[string]any{
				"userInfo": map[string]any{
					"userType": "employee",
					"userId":   "103262",
					"corpId":   "ding8196cd9a2b2405da24f2f5cc6abecb85",
					"userName": "冬翔",
				},
				"sessionInfo": map[string]any{
					"sessionId":          "94b0614e-session",
					"openConversationId": "cidCONTEXT==",
					"runId":              "run-213127",
					"history":            []any{},
				},
				"invocationInfo": invocation,
				"agentInfo": map[string]any{
					"agentCode": "18265b7f-ed66-42f7-b4be-c99dd20b2b62",
					"agentName": "Tagggg",
				},
			},
		},
		"a2a_passthrough": true,
	}
}

func deapTestMentionEvent() map[string]any {
	return map[string]any{
		a2aintegration.DingTalkEventExtensionURI: map[string]any{
			"eventId":    "msgMENTION==",
			"eventType":  "im.message.mention",
			"occurredAt": "2026-09-29T09:30:02Z",
			"data": map[string]any{
				"conversation": map[string]any{
					"type":               "group",
					"openConversationId": "cidGROUP==",
					"thread": map[string]any{
						"id":               "thread-1",
						"rootMessageId":    "msgROOT==",
						"openConvThreadId": "cidTHREAD==",
					},
				},
				"content":              "回复 PAR-C",
				"createTime":           "2026-09-29 17:30:02",
				"openMessageId":        "msgMENTION==",
				"sender":               "冬翔",
				"senderOpenDingTalkId": "DiiD-sender",
				"quotedMessage": map[string]any{
					"content":              "上一条",
					"openMessageId":        "msgQUOTED==",
					"sender":               "柚悠",
					"senderOpenDingTalkId": "DiiD-quoted",
				},
				"resources": []any{
					map[string]any{
						"resourceType":     "image",
						"resourceId":       "$media_1",
						"resourceIdType":   "mediaId",
						"url":              "https://example.test/image.png?x-oss-signature=secret",
						"expireTimeMillis": float64(1788172222094),
					},
				},
			},
		},
	}
}

func decodeTestInbound(t *testing.T, payload []byte) a2aDingTalkInbound {
	t.Helper()
	if payload == nil {
		t.Fatal("payload is nil")
	}
	var inbound a2aDingTalkInbound
	if err := json.Unmarshal(payload, &inbound); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	return inbound
}

func TestA2ADingTalkInboundMergesExtensionAndContext(t *testing.T) {
	payload := buildA2ADingTalkInboundPayload(
		deapTestRequestMetadata("group", false),
		deapTestMentionEvent(),
		a2aDingTalkBoundIdentity{UID: "5550001", OrgID: "7770001", DEAPAgentUUID: "bound-uuid"},
	)
	if strings.Contains(string(payload), "x-oss-signature") {
		t.Fatalf("signed download URL leaked into the payload: %s", payload)
	}
	inbound := decodeTestInbound(t, payload)
	if inbound.Schema != a2aDingTalkInboundSchema || inbound.Source.Transport != "a2a" || inbound.Source.Type != "digital_employee" {
		t.Fatalf("envelope header = %+v / %+v", inbound.Schema, inbound.Source)
	}
	if got := strings.Join(inbound.FactsFrom, ","); got != "dingtalk_event_extension,deap_request_context" {
		t.Fatalf("factsFrom = %q", got)
	}
	// The standard extension wins for the conversation even when the private
	// context names another one.
	if inbound.Conversation == nil || inbound.Conversation.OpenConversationID != "cidGROUP==" || inbound.Conversation.Type != "group" {
		t.Fatalf("conversation = %+v", inbound.Conversation)
	}
	if inbound.Conversation.Thread == nil || inbound.Conversation.Thread.OpenConvThreadID != "cidTHREAD==" {
		t.Fatalf("thread = %+v", inbound.Conversation.Thread)
	}
	if inbound.Event.DingTalkEventType != "im.message.mention" || inbound.Event.Addressed == nil || !*inbound.Event.Addressed || inbound.Event.ProactiveConversation {
		t.Fatalf("event = %+v", inbound.Event)
	}
	if inbound.Sender == nil || inbound.Sender.DisplayName != "冬翔" || inbound.Sender.OpenDingTalkID != "DiiD-sender" ||
		inbound.Sender.StaffID != "103262" || inbound.Sender.CorpID != "ding8196cd9a2b2405da24f2f5cc6abecb85" || inbound.Sender.UserType != "employee" {
		t.Fatalf("sender = %+v", inbound.Sender)
	}
	if inbound.Message == nil || inbound.Message.OpenMsgID != "msgMENTION==" || inbound.Message.ReferencedMessage == nil ||
		inbound.Message.ReferencedMessage.OpenMsgID != "msgQUOTED==" || len(inbound.Message.Attachments) != 1 ||
		inbound.Message.Attachments[0].ResourceID != "$media_1" {
		t.Fatalf("message = %+v", inbound.Message)
	}
	if len(inbound.RedactedFields) != 1 || inbound.RedactedFields[0] != "message.attachments[].url" {
		t.Fatalf("redacted_fields = %v", inbound.RedactedFields)
	}
	if inbound.Receiver == nil || inbound.Receiver.DEAPAgentUUID != "18265b7f-ed66-42f7-b4be-c99dd20b2b62" ||
		inbound.Receiver.Name != "Tagggg" || inbound.Receiver.DWSUID != "5550001" || inbound.Receiver.DWSOrgID != "7770001" {
		t.Fatalf("receiver = %+v", inbound.Receiver)
	}
	if inbound.DEAP == nil || inbound.DEAP.SessionID != "94b0614e-session" || inbound.DEAP.RunID != "run-213127" {
		t.Fatalf("deap = %+v", inbound.DEAP)
	}
	if got := a2aDingTalkChatType(payload); got != "group" {
		t.Fatalf("chat type = %q, want group", got)
	}
}

func TestA2ADingTalkInboundContextOnlyProactiveGroup(t *testing.T) {
	inbound := decodeTestInbound(t, buildA2ADingTalkInboundPayload(
		deapTestRequestMetadata("group", true),
		nil,
		a2aDingTalkBoundIdentity{},
	))
	if got := strings.Join(inbound.FactsFrom, ","); got != "deap_request_context" {
		t.Fatalf("factsFrom = %q", got)
	}
	if inbound.Conversation == nil || inbound.Conversation.OpenConversationID != "cidCONTEXT==" || inbound.Conversation.Type != "group" {
		t.Fatalf("conversation = %+v", inbound.Conversation)
	}
	if inbound.Event.Addressed == nil || *inbound.Event.Addressed || !inbound.Event.ProactiveConversation {
		t.Fatalf("proactive group event = %+v", inbound.Event)
	}
	if inbound.Receiver == nil || inbound.Receiver.DWSUID != "" {
		t.Fatalf("receiver must not carry an identity without a binding: %+v", inbound.Receiver)
	}
}

func TestA2ADingTalkInboundSingleChatMapsToDirectAudience(t *testing.T) {
	payload := buildA2ADingTalkInboundPayload(deapTestRequestMetadata("single", false), nil, a2aDingTalkBoundIdentity{})
	inbound := decodeTestInbound(t, payload)
	if inbound.Event.Addressed == nil || !*inbound.Event.Addressed || inbound.Event.ProactiveConversation {
		t.Fatalf("single chat event = %+v", inbound.Event)
	}
	if got := a2aDingTalkChatType(payload); got != "p2p" {
		t.Fatalf("chat type = %q, want p2p", got)
	}
}

func TestA2ADingTalkInboundDropsInconsistentExtension(t *testing.T) {
	event := deapTestMentionEvent()
	raw := event[a2aintegration.DingTalkEventExtensionURI].(map[string]any)
	raw["eventType"] = "im.message.single" // a single-chat event with a group conversation
	inbound := decodeTestInbound(t, buildA2ADingTalkInboundPayload(
		deapTestRequestMetadata("group", false), event, a2aDingTalkBoundIdentity{},
	))
	if got := strings.Join(inbound.FactsFrom, ","); got != "deap_request_context" {
		t.Fatalf("inconsistent extension was trusted: factsFrom = %q", got)
	}
	if inbound.Conversation.OpenConversationID != "cidCONTEXT==" || inbound.Message != nil {
		t.Fatalf("fell back incorrectly: conversation=%+v message=%+v", inbound.Conversation, inbound.Message)
	}
}

func TestA2ADingTalkInboundIgnoresOrdinaryClients(t *testing.T) {
	if payload := buildA2ADingTalkInboundPayload(nil, nil, a2aDingTalkBoundIdentity{UID: "1", OrgID: "2"}); payload != nil {
		t.Fatalf("ordinary A2A request produced a payload: %s", payload)
	}
	if payload := buildA2ADingTalkInboundPayload(map[string]any{"context": map[string]any{"source": "x"}}, map[string]any{"other": 1}, a2aDingTalkBoundIdentity{}); payload != nil {
		t.Fatalf("unrelated metadata produced a payload: %s", payload)
	}
	if got := a2aDingTalkChatType([]byte(`{"schema":"other","conversation":{"type":"group"}}`)); got != "" {
		t.Fatalf("foreign source payload mapped to chat type %q", got)
	}
}

func TestA2ADingTalkInboundDropsOversizedIdentifiers(t *testing.T) {
	metadata := deapTestRequestMetadata("single", false)
	attributes := metadata["context"].(map[string]any)["attributes"].(map[string]any)
	attributes["sessionInfo"].(map[string]any)["openConversationId"] = strings.Repeat("c", a2aDingTalkMaxIDBytes+1)
	inbound := decodeTestInbound(t, buildA2ADingTalkInboundPayload(metadata, nil, a2aDingTalkBoundIdentity{}))
	if inbound.Conversation == nil || inbound.Conversation.OpenConversationID != "" {
		t.Fatalf("oversized conversation id was kept: %+v", inbound.Conversation)
	}
}

func TestA2ATaskContextOperatorBindingMarker(t *testing.T) {
	bound := a2aDingTalkBoundIdentity{UID: "5550001", OrgID: "7770001"}
	if marked := newA2ATaskContextWithBinding(a2aintegration.InvocationIdentity{}, bound); !UsesA2AOperatorDWSIdentity(marked) || !IsA2ATaskOrigin(marked) {
		t.Fatalf("bound turn is not marked: %s", marked)
	}
	if plain := newA2ATaskContextWithBinding(a2aintegration.InvocationIdentity{}, a2aDingTalkBoundIdentity{}); UsesA2AOperatorDWSIdentity(plain) {
		t.Fatalf("unbound turn is marked: %s", plain)
	}
	deap := newA2ATaskContextWithBinding(a2aintegration.InvocationIdentity{DEAPDWSToken: "tok"}, bound)
	if UsesA2AOperatorDWSIdentity(deap) || !requiresA2ADEAPDWSToken(deap) {
		t.Fatalf("DEAP token must win over the operator binding: %s", deap)
	}
	external := newA2ATaskContextWithBinding(a2aintegration.InvocationIdentity{ContextToken: "ctx", ExpiresAtUnixMS: 1}, bound)
	if UsesA2AOperatorDWSIdentity(external) {
		t.Fatalf("external ContextToken must win over the operator binding: %s", external)
	}
	if UsesA2AOperatorDWSIdentity([]byte(`{"a2a_operator_dws_identity":true}`)) {
		t.Fatal("marker without the A2A origin was accepted")
	}
}

type fakeA2AOperatorIdentityReader struct {
	config      db.GetAgentA2AOperatorConfigRow
	err         error
	identity    db.AgentDingtalkIdentity
	identityErr error
	calls       int
}

func (f *fakeA2AOperatorIdentityReader) GetAgentA2AOperatorConfig(context.Context, db.GetAgentA2AOperatorConfigParams) (db.GetAgentA2AOperatorConfigRow, error) {
	f.calls++
	return f.config, f.err
}

func (f *fakeA2AOperatorIdentityReader) GetAgentDingTalkIdentity(context.Context, db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error) {
	f.calls++
	return f.identity, f.identityErr
}

func enabledTagggIdentityReader() *fakeA2AOperatorIdentityReader {
	return &fakeA2AOperatorIdentityReader{
		config:   db.GetAgentA2AOperatorConfigRow{A2aIdentityEnabled: true},
		identity: db.AgentDingtalkIdentity{DwsUid: "5550001", OrgID: "7770001"},
	}
}

func operatorIdentityTestLauncher(reader A2AOperatorIdentityReader, creator *fakeAgentIdentityContextCreator) *FCE2BLauncher {
	return &FCE2BLauncher{
		Config: FCE2BConfig{
			AgentIdentityBaseURL: "https://pre-agent-identity.dingtalk.com",
			AgentIdentityTimeout: 2 * time.Second,
			DWSClientSecret:      "dws-client-secret",
		},
		AgentIdentity:         creator,
		A2AOperatorIdentities: reader,
	}
}

func TestA2AOperatorBindingMintsRunnerContextToken(t *testing.T) {
	reader := enabledTagggIdentityReader()
	creator := &fakeAgentIdentityContextCreator{result: agentidentityhsf.CreateContextResult{
		ContextToken: "ctx_from_operator_binding",
		ExpiresAt:    time.Now().Add(15 * time.Minute).UnixMilli(),
	}}
	runtime := db.AgentRuntime{
		WorkspaceID: util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
		RuntimeMode: "cloud",
		Metadata:    []byte(`{"kind":"fc-e2b","capabilities":["dws"]}`),
	}
	task := db.AgentTaskQueue{
		ID:      util.MustParseUUID("11111111-1111-1111-1111-111111111111"),
		AgentID: util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
		Context: newA2ATaskContextWithBinding(a2aintegration.InvocationIdentity{}, a2aDingTalkBoundIdentity{UID: "5550001", OrgID: "7770001"}),
	}
	env, err := operatorIdentityTestLauncher(reader, creator).identityEnvForTask(context.Background(), task, runtime, "sbx-a2a-bound", db.Agent{})
	if err != nil {
		t.Fatal(err)
	}
	if env[protocol.AgentIdentityContextTokenEnvKey] != "ctx_from_operator_binding" {
		t.Fatalf("identity env = %#v", env)
	}
	if _, leaked := env[protocol.DEAPDWSTokenEnvKey]; leaked {
		t.Fatalf("operator binding must use the ContextToken path: %#v", env)
	}
	if len(creator.requests) != 1 || creator.requests[0].UID != "5550001" || creator.requests[0].OrgID != "7770001" ||
		creator.requests[0].Source["identity_source"] != "a2a_operator_binding" || creator.requests[0].GithubConnectionID != "" {
		t.Fatalf("Agent Identity requests = %#v", creator.requests)
	}
}

func TestA2AOperatorBindingClearedLaunchesWithoutIdentity(t *testing.T) {
	reader := &fakeA2AOperatorIdentityReader{err: pgx.ErrNoRows}
	creator := &fakeAgentIdentityContextCreator{}
	runtime := db.AgentRuntime{
		WorkspaceID: util.MustParseUUID("33333333-3333-3333-3333-333333333333"),
		RuntimeMode: "cloud",
		Metadata:    []byte(`{"kind":"fc-e2b","capabilities":["dws"]}`),
	}
	task := db.AgentTaskQueue{
		AgentID: util.MustParseUUID("44444444-4444-4444-4444-444444444444"),
		Context: newA2ATaskContextWithBinding(a2aintegration.InvocationIdentity{}, a2aDingTalkBoundIdentity{UID: "5550001", OrgID: "7770001"}),
	}
	env, err := operatorIdentityTestLauncher(reader, creator).identityEnvForTask(context.Background(), task, runtime, "sbx-a2a-cleared", db.Agent{})
	if err != nil || len(env) != 0 || len(creator.requests) != 0 {
		t.Fatalf("cleared binding: env=%#v err=%v requests=%d", env, err, len(creator.requests))
	}

	noDWS := runtime
	noDWS.Metadata = []byte(`{"kind":"fc-e2b","capabilities":[]}`)
	reader.err = nil
	if _, err := operatorIdentityTestLauncher(reader, creator).identityEnvForTask(context.Background(), task, noDWS, "sbx-no-dws", db.Agent{}); err == nil {
		t.Fatal("operator identity was accepted on a runtime without DWS capability")
	}

	asb := runtime
	asb.Metadata = []byte(`{"kind":"cloud-sandbox","sandbox_backend":"asb","provider":"pi","artifact_kind":"oci_image","artifact_ref":"registry.example/runtime@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","artifact_digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","capabilities":["dws"]}`)
	reader.calls = 0
	if env, err := operatorIdentityTestLauncher(reader, creator).identityEnvForTask(context.Background(), task, asb, "sbx-asb", db.Agent{}); err != nil || len(env) != 0 || len(creator.requests) != 0 || reader.calls != 0 {
		t.Fatalf("ASB minted an operator identity it cannot deliver: env=%#v err=%v requests=%d calls=%d", env, err, len(creator.requests), reader.calls)
	}

	unmarked := task
	unmarked.Context = newA2ATaskContext()
	reader.calls = 0
	if env, err := operatorIdentityTestLauncher(reader, creator).identityEnvForTask(context.Background(), unmarked, runtime, "sbx-unmarked", db.Agent{}); err != nil || len(env) != 0 || reader.calls != 0 {
		t.Fatalf("unmarked A2A task read the binding: env=%#v err=%v calls=%d", env, err, reader.calls)
	}
}

func TestLoadA2AOperatorDWSIdentityFailsOpen(t *testing.T) {
	ws := util.MustParseUUID("33333333-3333-3333-3333-333333333333")
	agent := util.MustParseUUID("44444444-4444-4444-4444-444444444444")
	if got := loadA2AOperatorDWSIdentity(context.Background(), &fakeA2AOperatorIdentityReader{err: errors.New("boom")}, ws, agent); got.UID != "" {
		t.Fatalf("read failure produced identity %+v", got)
	}
	if got := loadA2AOperatorDWSIdentity(context.Background(), &fakeA2AOperatorIdentityReader{}, ws, agent); got.UID != "" {
		t.Fatalf("cleared row produced identity %+v", got)
	}
	// An identity bound only through Integrations stays out of A2A until an
	// operator enables it.
	integrationsOnly := enabledTagggIdentityReader()
	integrationsOnly.config.A2aIdentityEnabled = false
	if got := loadA2AOperatorDWSIdentity(context.Background(), integrationsOnly, ws, agent); got.UID != "" {
		t.Fatalf("identity without the A2A switch produced %+v", got)
	}
	unbound := enabledTagggIdentityReader()
	unbound.identityErr = pgx.ErrNoRows
	if got := loadA2AOperatorDWSIdentity(context.Background(), unbound, ws, agent); got.UID != "" {
		t.Fatalf("switch without identity produced %+v", got)
	}
	if got := loadA2AOperatorDWSIdentity(context.Background(), enabledTagggIdentityReader(), ws, agent); got.UID != "5550001" || got.OrgID != "7770001" {
		t.Fatalf("enabled identity = %+v", got)
	}
	if got := loadA2AOperatorDWSIdentity(context.Background(), nil, ws, agent); got.UID != "" {
		t.Fatalf("nil reader produced identity %+v", got)
	}
}
