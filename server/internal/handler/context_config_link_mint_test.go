package handler

import (
	"context"
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
// dispatch context, a group gets the reusable 30-minute scene link and a 1:1
// chat the single-use 15-minute personal link that also grants the 1:1 scene.
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
	if err != nil || scene.Scope != contextcap.ScopeScene || scene.ValidFor != contextcap.LinkTTLScene || scene.SingleUse {
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

	// 1:1 chat: the single-use personal link, which also grants the DM scene.
	f.registerDirectScene(t)
	person, err := issue(ctxcapDispatch("single", ctxcapDirectScene, ctxcapStaff, ctxcapStaff))
	if err != nil || person.Scope != contextcap.ScopePerson || person.ValidFor != contextcap.LinkTTLPerson || !person.SingleUse {
		t.Fatalf("1:1 link=%+v err=%v", person, err)
	}
	ctxcapExpiresWithin(t, person.ExpiresAt.UTC().Format(time.RFC3339), contextcap.LinkTTLPerson)
	personToken := coordinatorLinkToken(t, person)
	if got := coordinatorStoredLinkFor(t, personToken); got != (coordinatorStoredLink{
		scopeType: contextcap.ScopePerson, orgID: ctxcapOrg, scopeKey: ctxcapStaff, title: "Alice", extraScene: ctxcapDirectScene,
	}) {
		t.Fatalf("stored person link=%+v", got)
	}
	ctxcapExpectStatus(t, redeem(alice, personToken), http.StatusOK, "coordinator person link redeem")
	ctxcapExpectStatus(t, redeem(alice, personToken), http.StatusGone, "coordinator person link reuse")
	if _, err := contextcap.GetLiveGrant(context.Background(), testPool, alice, agentID, contextcap.ScopeScene, ctxcapOrg, ctxcapDirectScene); err != nil {
		t.Fatalf("personal link did not grant the 1:1 scene: %v", err)
	}
}

// decideDispatchCoordinator hands the Coordinator dispatchRuntimeContext, the
// same envelope the resulting task stores; the issuer reads scene and person
// from it, and a dispatch recorded under another org gets no link.
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
	direct, err := issue(command("single", ctxcapDirectScene, ctxcapOrg))
	if err != nil || direct.Scope != contextcap.ScopePerson {
		t.Fatalf("1:1 envelope link=%+v err=%v", direct, err)
	}
	if got := coordinatorStoredLinkFor(t, coordinatorLinkToken(t, direct)); got.scopeKey != ctxcapStaff || got.extraScene != ctxcapDirectScene {
		t.Fatalf("1:1 envelope stored=%+v", got)
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
	// Unknown or shared conversation types, merged senders, replayed
	// contexts and missing context never yield a link.
	refuse("unknown conversation type", request(ctxcapDispatch("", "", ctxcapStaff, ctxcapStaff)))
	refuse("channel conversation", request(ctxcapDispatch("channel", "cidCtxcapChannel==", ctxcapStaff, ctxcapStaff)))
	refuse("multi-sender 1:1", request(ctxcapDispatch("single", ctxcapCoordinatorDirect, ctxcapStaff, ctxcapStaff, ctxcapOtherStaff)))
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
