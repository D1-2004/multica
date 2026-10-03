package handler

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/contextcap"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
)

const ctxcapCoordinatorDirect = "cidCtxcapCoordDirect=="

// coordinatorLinkToken checks a Coordinator link URL and returns its token.
func coordinatorLinkToken(t *testing.T, link inboundcoord.ConfigLink) string {
	t.Helper()
	const prefix = "https://app.multica.example/dingtalk/configure?link="
	if !strings.HasPrefix(link.URL, prefix) {
		t.Fatalf("url=%q", link.URL)
	}
	token, err := url.QueryUnescape(strings.TrimPrefix(link.URL, prefix))
	if err != nil || !contextcap.ValidLinkTokenFormat(token) {
		t.Fatalf("token %q err=%v", token, err)
	}
	return token
}

// ctxcapDWSDispatch is the dispatch context a DingTalk digital employee (DWS)
// writes: the sender carries only its openDingTalkId and display name, never
// a staffId, and the conversation has no title. A fixture scene id resolves
// to its SceneRef and conversation id as in ctxcapDispatch.
func ctxcapDWSDispatch(conversationType, sceneOrCID string) []byte {
	cid := sceneOrCID
	payload := map[string]any{"dispatch_source": map[string]any{"platform": "dingtalk", "type": "digital_employee"}}
	if registered, ok := ctxcapDispatchCIDs[sceneOrCID]; ok {
		cid = registered
		payload["agent_scene"] = map[string]any{"scene_id": sceneOrCID}
	}
	payload["dispatch_event_data"] = map[string]any{
		"conversation": map[string]any{"openConversationId": cid, "type": conversationType},
		"sender":       map[string]any{"displayName": "冬翔", "openDingTalkId": "DpJnOpenSender"},
		"messages":     []map[string]any{{"openMsgId": "m-dws", "senderOpenDingTalkId": "DpJnOpenSender"}},
	}
	raw, _ := json.Marshal(payload)
	return raw
}

type coordinatorStoredLink struct {
	scopeType, orgID, scopeKey, title, sourceTask, extraScene string
}

func coordinatorStoredLinkFor(t *testing.T, token string) coordinatorStoredLink {
	t.Helper()
	var got coordinatorStoredLink
	if err := testPool.QueryRow(context.Background(), `SELECT scope_type, org_id, scope_key, scope_title, COALESCE(source_task_id::text, ''), extra_scene_key
		FROM context_config_link WHERE token_hash = $1`, contextcap.HashLinkToken(token)).
		Scan(&got.scopeType, &got.orgID, &got.scopeKey, &got.title, &got.sourceTask, &got.extraScene); err != nil {
		t.Fatal(err)
	}
	return got
}

// The Coordinator's capability answer mints the same links as the executor's
// create_context_config_link tool: the scene is taken from the inbound turn's
// dispatch context, and a group and a 1:1 chat alike get the reusable
// 30-minute link of their own scene, keyed by the scene_id of the
// conversation, never by the sender.
func TestCoordinatorConfigLinkIssuerMintsConversationLinks(t *testing.T) {
	f := newCtxcapFixture(t)
	f.h.cfg.AppURL = "https://app.multica.example/"
	f.cleanupGrantsAndLinks(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	issuer := NewCoordinatorConfigLinkIssuer(f.h)
	issue := func(dispatch []byte) (inboundcoord.ConfigLink, error) {
		return issuer.IssueConfigLink(context.Background(), inboundcoord.ConfigLinkRequest{
			WorkspaceID: testWorkspaceID, AgentID: agentID, DispatchContext: dispatch, TraceID: "coord-trace-1",
		})
	}
	redeem := func(userID, token string) *httptest.ResponseRecorder {
		return ctxcapMobile(t, router, http.MethodPost, "/api/context-capabilities/links/redeem", userID, map[string]any{"token": token})
	}

	// Group chat: the scene link.
	scene, err := issue(ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff))
	if err != nil || scene.Scope != contextcap.ScopeScene || scene.SceneKind != contextcap.SceneKindGroup || scene.ValidFor != contextcap.LinkTTLScene {
		t.Fatalf("group link=%+v err=%v", scene, err)
	}
	ctxcapExpiresWithin(t, scene.ExpiresAt.UTC().Format(time.RFC3339), contextcap.LinkTTLScene)
	sceneToken := coordinatorLinkToken(t, scene)
	if got := coordinatorStoredLinkFor(t, sceneToken); got != (coordinatorStoredLink{
		scopeType: contextcap.ScopeScene, orgID: ctxcapOrg, scopeKey: ctxcapScene, title: "Ctxcap group",
	}) {
		t.Fatalf("stored scene link=%+v", got)
	}
	alice, bob := uuid.NewString(), uuid.NewString()
	for _, user := range []string{alice, bob} {
		ctxcapExpectStatus(t, redeem(user, sceneToken), http.StatusOK, "coordinator scene link redeem")
	}

	// 1:1 chat (冬翔 2026-10-03): the chat's link also carries the chat's
	// person, keyed by TriggerPersonKey (the staffId, else "odt:" + the
	// openDingTalkId), and opens their own level for the first account that
	// opens it; that account may open it again, nobody else may. A merged
	// window of several speakers names no person: the plain scene link.
	f.registerDirectScene(t)
	for _, tc := range []struct {
		name, personKey, title string
		dispatch               []byte
	}{
		{"robot sender with staffId", ctxcapStaff, "Alice", ctxcapDispatch("single", ctxcapDirectScene, ctxcapStaff, ctxcapStaff)},
		{"digital employee without staffId", "odt:DpJnOpenSender", "冬翔", ctxcapDWSDispatch("single", ctxcapDirectScene)},
		{"merged window of several speakers", "", "Ctxcap group", ctxcapDispatch("single", ctxcapDirectScene, ctxcapStaff, ctxcapStaff, ctxcapOtherStaff)},
	} {
		direct, err := issue(tc.dispatch)
		if err != nil || direct.Scope != contextcap.ScopeScene || direct.SceneKind != contextcap.SceneKindDM || direct.ValidFor != contextcap.LinkTTLScene {
			t.Fatalf("%s: 1:1 link=%+v err=%v", tc.name, direct, err)
		}
		ctxcapExpiresWithin(t, direct.ExpiresAt.UTC().Format(time.RFC3339), contextcap.LinkTTLScene)
		token := coordinatorLinkToken(t, direct)
		got := coordinatorStoredLinkFor(t, token)
		if tc.personKey == "" {
			if got.scopeType != contextcap.ScopeScene || got.scopeKey != ctxcapDirectScene || got.extraScene != "" || got.title != tc.title {
				t.Fatalf("%s: stored 1:1 link=%+v", tc.name, got)
			}
			for _, user := range []string{alice, bob} {
				ctxcapExpectStatus(t, redeem(user, token), http.StatusOK, tc.name+": 1:1 scene link redeem")
			}
			continue
		}
		if got.scopeType != contextcap.ScopePerson || got.orgID != ctxcapOrg || got.scopeKey != tc.personKey ||
			got.extraScene != ctxcapDirectScene || got.title != tc.title {
			t.Fatalf("%s: stored 1:1 link=%+v", tc.name, got)
		}
		ctxcapExpectStatus(t, redeem(alice, token), http.StatusOK, tc.name+": first account")
		ctxcapExpectStatus(t, redeem(alice, token), http.StatusOK, tc.name+": the same account again")
		ctxcapExpectStatus(t, redeem(bob, token), http.StatusGone, tc.name+": another account")
		if _, err := contextcap.GetLiveGrant(context.Background(), testPool, alice, agentID, contextcap.ScopePerson, ctxcapOrg, tc.personKey); err != nil {
			t.Fatalf("%s: the person was not granted: %v", tc.name, err)
		}
	}
	for _, user := range []string{alice, bob} {
		if _, err := contextcap.GetLiveGrant(context.Background(), testPool, user, agentID, contextcap.ScopeScene, ctxcapOrg, ctxcapDirectScene); err != nil {
			t.Fatalf("the 1:1 link did not grant the 1:1 scene: %v", err)
		}
	}
	if _, err := contextcap.GetLiveGrant(context.Background(), testPool, bob, agentID, contextcap.ScopePerson, ctxcapOrg, ctxcapStaff); err == nil {
		t.Fatal("another account took the person")
	}
}

// decideDispatchCoordinator hands the Coordinator dispatchRuntimeContext, the
// same envelope the resulting task stores; the issuer reads the scene from
// it, and a dispatch recorded under another org gets no link.
func TestCoordinatorConfigLinkIssuerReadsTheDispatchEnvelope(t *testing.T) {
	f := newCtxcapFixture(t)
	f.h.cfg.AppURL = "https://app.multica.example"
	f.cleanupGrantsAndLinks(t)
	f.registerDirectScene(t)
	command := func(conversationType, sceneID, orgID string) DispatchCommand {
		return DispatchCommand{
			SchemaVersion: "2.0",
			AgentScene:    &scene.Ref{SceneID: sceneID},
			Source:        DispatchSource{Platform: "dingtalk", Type: "digital_employee"},
			Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
				Conversation: DispatchConversation{OpenConversationID: ctxcapDispatchCIDs[sceneID], Type: conversationType, Title: "Envelope group"},
				Sender:       DispatchSender{StaffID: ctxcapStaff, DisplayName: "Alice", UID: "uid-alice"},
				Messages:     []DispatchMessage{{OpenMsgID: "msg-1", Text: "你有哪些能力？", SenderStaffID: ctxcapStaff, SenderUID: "uid-alice"}},
			}},
			ExternalIdentity: AgentDispatchExternalIdentity{DWS: &AgentDispatchDWSIdentity{UID: "dws-agent", OrgID: orgID}},
		}
	}
	issue := func(c DispatchCommand) (inboundcoord.ConfigLink, error) {
		return NewCoordinatorConfigLinkIssuer(f.h).IssueConfigLink(context.Background(), inboundcoord.ConfigLinkRequest{
			WorkspaceID: testWorkspaceID, AgentID: uuidToString(f.agent), DispatchContext: dispatchRuntimeContext(c, "dispatch-window:config-link"),
		})
	}
	group, err := issue(command("group", ctxcapScene, ctxcapOrg))
	if err != nil || group.Scope != contextcap.ScopeScene {
		t.Fatalf("group envelope link=%+v err=%v", group, err)
	}
	if got := coordinatorStoredLinkFor(t, coordinatorLinkToken(t, group)); got.scopeKey != ctxcapScene || got.title != "Envelope group" || got.orgID != ctxcapOrg {
		t.Fatalf("group envelope stored=%+v", got)
	}
	// A digital employee's 1:1 chat: the sender has no staffId, so the
	// link carries the person by "odt:" + openDingTalkId, with the chat as
	// its extra scene; with a proved staffId, by the staffId.
	dws := command("single", ctxcapDirectScene, ctxcapOrg)
	dws.Event.Data.Sender = DispatchSender{DisplayName: "冬翔", OpenDingTalkID: "DpJnOpenSender"}
	dws.Event.Data.Messages = []DispatchMessage{{OpenMsgID: "msg-dws", Text: "给我你的场域配置链接", SenderOpenDingTalkID: "DpJnOpenSender"}}
	direct, err := issue(dws)
	if err != nil || direct.Scope != contextcap.ScopeScene || direct.SceneKind != contextcap.SceneKindDM {
		t.Fatalf("1:1 envelope link=%+v err=%v", direct, err)
	}
	if got := coordinatorStoredLinkFor(t, coordinatorLinkToken(t, direct)); got.scopeType != contextcap.ScopePerson ||
		got.scopeKey != "odt:DpJnOpenSender" || got.extraScene != ctxcapDirectScene {
		t.Fatalf("1:1 envelope stored=%+v", got)
	}
	dws.Event.Data.Sender.StaffID = ctxcapStaff
	dws.Event.Data.Messages[0].SenderStaffID = ctxcapStaff
	direct, err = issue(dws)
	if err != nil {
		t.Fatal(err)
	}
	if got := coordinatorStoredLinkFor(t, coordinatorLinkToken(t, direct)); got.scopeType != contextcap.ScopePerson ||
		got.scopeKey != ctxcapStaff || got.extraScene != ctxcapDirectScene {
		t.Fatalf("1:1 envelope with a proved staffId stored=%+v", got)
	}
	if link, err := issue(command("group", ctxcapScene, "org-someone-else")); err == nil {
		t.Fatalf("dispatch under another org minted %+v", link)
	}
}

func TestCoordinatorConfigLinkIssuerFailsClosed(t *testing.T) {
	f := newCtxcapFixture(t)
	f.h.cfg.AppURL = "https://app.multica.example"
	f.cleanupGrantsAndLinks(t)
	agentID := uuidToString(f.agent)
	issuer := NewCoordinatorConfigLinkIssuer(f.h)
	refuse := func(name string, req inboundcoord.ConfigLinkRequest) {
		t.Helper()
		if link, err := issuer.IssueConfigLink(context.Background(), req); err == nil {
			t.Fatalf("%s: minted %+v", name, link)
		}
	}
	request := func(dispatch []byte) inboundcoord.ConfigLinkRequest {
		return inboundcoord.ConfigLinkRequest{WorkspaceID: testWorkspaceID, AgentID: agentID, DispatchContext: dispatch}
	}
	// A conversation without a scene of this agent (unknown type, a channel,
	// a 1:1 chat that never resolved one), replayed contexts and missing
	// context never yield a link.
	refuse("unknown conversation type", request(ctxcapDispatch("", "", ctxcapStaff, ctxcapStaff)))
	refuse("channel conversation", request(ctxcapDispatch("channel", "cidCtxcapChannel==", ctxcapStaff, ctxcapStaff)))
	refuse("1:1 chat without a scene", request(ctxcapDWSDispatch("single", ctxcapCoordinatorDirect)))
	// A SceneRef the agent's directory does not hold in the turn's org is
	// dropped by the use-time fence: no link for it.
	var unknownRef map[string]any
	if err := json.Unmarshal(ctxcapDWSDispatch("single", ctxcapCoordinatorDirect), &unknownRef); err != nil {
		t.Fatal(err)
	}
	unknownRef["agent_scene"] = map[string]any{"scene_id": ctxcapUnknownScene}
	unknownRaw, _ := json.Marshal(unknownRef)
	refuse("scene the agent does not have", request(unknownRaw))
	refuse("replayed dispatch", request(ctxcapReplayed(ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff))))
	refuse("no dispatch context", request(nil))
	refuse("malformed dispatch context", request([]byte(`{"dispatch_event_data":`)))
	refuse("invalid agent", inboundcoord.ConfigLinkRequest{WorkspaceID: testWorkspaceID, AgentID: "not-a-uuid", DispatchContext: ctxcapDispatch("group", ctxcapScene, ctxcapStaff)})
	refuse("no handler", inboundcoord.ConfigLinkRequest{})
	if _, err := NewCoordinatorConfigLinkIssuer(nil).IssueConfigLink(context.Background(), request(ctxcapDispatch("group", ctxcapScene, ctxcapStaff))); err == nil {
		t.Fatal("nil handler minted a link")
	}
	f.h.cfg.AppURL = ""
	refuse("no app url", request(ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)))

	var links int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM context_config_link WHERE agent_id = $1`, agentID).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 0 {
		t.Fatalf("refused requests stored %d links", links)
	}
}

// The Coordinator transcript keeps a placeholder for a configuration link;
// only the DingTalk reply carries the bearer URL.
func TestRedactContextConfigLinks(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"no link here", "no link here"},
		{"你的个人能力配置（15 分钟内有效，限用一次）：https://app.multica.example/dingtalk/configure?link=AbC_d-9",
			"你的个人能力配置（15 分钟内有效，限用一次）：[configuration link]"},
		{"a https://x.example/dingtalk/configure?link=t1 and https://x.example/dingtalk/configure?link=t2%3D.",
			"a [configuration link] and [configuration link]."},
		{"relative /dingtalk/configure?link=tok end", "relative [configuration link] end"},
		{"https://x.example/dingtalk/configure?agent=1", "https://x.example/dingtalk/configure?agent=1"},
	} {
		if got := redactContextConfigLinks(tc.in); got != tc.want {
			t.Fatalf("redactContextConfigLinks(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
