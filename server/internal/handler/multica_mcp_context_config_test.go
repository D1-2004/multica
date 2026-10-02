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
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// ctxcapToolCall calls create_context_config_link as the fixture agent's task.
func (f *ctxcapFixture) ctxcapToolCall(t *testing.T, task db.AgentTaskQueue, args map[string]any) map[string]any {
	t.Helper()
	params := map[string]any{"name": multicaMCPContextConfigLinkTool}
	if args != nil {
		params["arguments"] = args
	}
	r := mcpRequest(t, "tools/call", 1, params)
	r.Header.Set("X-Workspace-ID", testWorkspaceID)
	r.Header.Set("X-Agent-ID", uuidToString(f.agent))
	r.Header.Set("X-Task-ID", uuidToString(task.ID))
	r.Header.Set("X-User-ID", testUserID)
	w := httptest.NewRecorder()
	f.h.MulticaMCP(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("tools/call status=%d body=%s", w.Code, w.Body.String())
	}
	return decodeMCPResponse(t, w)
}

type ctxcapLinkResult struct {
	URL         string `json:"url"`
	DingTalkURL string `json:"dingtalk_url"`
	Scope       string `json:"scope"`
	ExpiresAt   string `json:"expires_at"`
}

func ctxcapToolResult(t *testing.T, got map[string]any) (ctxcapLinkResult, bool, string) {
	t.Helper()
	result, ok := got["result"].(map[string]any)
	if !ok {
		t.Fatalf("no result: %#v", got)
	}
	content := result["content"].([]any)
	text := content[0].(map[string]any)["text"].(string)
	if isError, _ := result["isError"].(bool); isError {
		return ctxcapLinkResult{}, true, text
	}
	var out ctxcapLinkResult
	raw, _ := json.Marshal(result["structuredContent"])
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	var fromText ctxcapLinkResult
	if err := json.Unmarshal([]byte(text), &fromText); err != nil || fromText != out {
		t.Fatalf("text content %q does not match structuredContent %+v", text, out)
	}
	return out, false, text
}

// ctxcapLinkToken extracts the link token from a minted url and checks the
// DingTalk deep link wraps the same page.
func ctxcapLinkToken(t *testing.T, result ctxcapLinkResult) string {
	t.Helper()
	const prefix = "https://app.multica.example/dingtalk/configure?link="
	if !strings.HasPrefix(result.URL, prefix) {
		t.Fatalf("url=%q", result.URL)
	}
	wantDeepLink := "dingtalk://dingtalkclient/page/link?url=" + url.QueryEscape(result.URL) + "&pc_slide=true"
	if result.DingTalkURL != wantDeepLink {
		t.Fatalf("dingtalk_url=%q want %q", result.DingTalkURL, wantDeepLink)
	}
	token, err := url.QueryUnescape(strings.TrimPrefix(result.URL, prefix))
	if err != nil || !contextcap.ValidLinkTokenFormat(token) {
		t.Fatalf("token %q err=%v", token, err)
	}
	return token
}

func ctxcapExpiresWithin(t *testing.T, raw string, ttl time.Duration) {
	t.Helper()
	expires, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		t.Fatalf("expires_at=%q: %v", raw, err)
	}
	if left := time.Until(expires); left > ttl+time.Minute || left < ttl-2*time.Minute {
		t.Fatalf("expires_at=%q is %s away, want about %s", raw, left, ttl)
	}
}

func TestContextCapabilitiesLinkToolListingAndGating(t *testing.T) {
	f := newCtxcapFixture(t)
	f.h.cfg.AppURL = "https://app.multica.example"
	f.cleanupGrantsAndLinks(t)
	listed := func(r *http.Request) bool {
		w := httptest.NewRecorder()
		f.h.MulticaMCP(w, r)
		for _, tool := range decodeMCPResponse(t, w)["result"].(map[string]any)["tools"].([]any) {
			if tool.(map[string]any)["name"] == multicaMCPContextConfigLinkTool {
				return true
			}
		}
		return false
	}
	if !listed(mcpRequest(t, "tools/list", 1, map[string]any{})) {
		t.Fatal("tool missing from task-token tools/list with the flag on")
	}
	if listed(personalMCPRequest(t, "tools/list", 1, map[string]any{})) {
		t.Fatal("tool listed for a personal access token")
	}
	pat := personalMCPRequest(t, "tools/call", 1, map[string]any{"name": multicaMCPContextConfigLinkTool, "arguments": map[string]any{}})
	w := httptest.NewRecorder()
	f.h.MulticaMCP(w, pat)
	if _, isError, text := ctxcapToolResult(t, decodeMCPResponse(t, w)); !isError || !strings.Contains(text, "task token") {
		t.Fatalf("PAT call isError=%v text=%q", isError, text)
	}

	task := f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff))
	if _, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, task, map[string]any{"scope": "everyone"})); !isError || !strings.Contains(text, "scope") {
		t.Fatalf("bad scope isError=%v text=%q", isError, text)
	}
	got := f.ctxcapToolCall(t, task, map[string]any{"scope": "scene", "extra": true})
	if errObj, ok := got["error"].(map[string]any); !ok || errObj["code"].(float64) != -32602 {
		t.Fatalf("unknown argument accepted: %#v", got)
	}

	// Another agent's token cannot mint for this task.
	r := mcpRequest(t, "tools/call", 1, map[string]any{"name": multicaMCPContextConfigLinkTool})
	r.Header.Set("X-Workspace-ID", testWorkspaceID)
	r.Header.Set("X-Agent-ID", uuid.NewString())
	r.Header.Set("X-Task-ID", uuidToString(task.ID))
	w = httptest.NewRecorder()
	f.h.MulticaMCP(w, r)
	if _, isError, _ := ctxcapToolResult(t, decodeMCPResponse(t, w)); !isError {
		t.Fatal("foreign agent minted a link")
	}

	// Inactive tasks cannot mint.
	if _, err := testPool.Exec(context.Background(), `UPDATE agent_task_queue SET status = 'completed' WHERE id = $1`, uuidToString(task.ID)); err != nil {
		t.Fatal(err)
	}
	if _, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, task, nil)); !isError || !strings.Contains(text, "not active") {
		t.Fatalf("completed task isError=%v text=%q", isError, text)
	}

	var links int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM context_config_link WHERE agent_id = $1`, uuidToString(f.agent)).Scan(&links); err != nil {
		t.Fatal(err)
	}
	if links != 0 {
		t.Fatalf("refused calls stored %d links", links)
	}
}

func TestContextCapabilitiesLinkMintAndRedeem(t *testing.T) {
	f := newCtxcapFixture(t)
	f.h.cfg.AppURL = "https://app.multica.example/"
	f.cleanupGrantsAndLinks(t)
	router := ctxcapRouter(f.h)
	agentID := uuidToString(f.agent)
	redeem := func(userID, token string) *httptest.ResponseRecorder {
		return ctxcapMobile(t, router, http.MethodPost, "/api/context-capabilities/links/redeem", userID, map[string]any{"token": token})
	}

	// Group run: the default is a scene link bound to the dispatch scene.
	groupTask := f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff))
	sceneLink, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, groupTask, nil))
	if isError || sceneLink.Scope != contextcap.ScopeScene {
		t.Fatalf("group mint isError=%v text=%q", isError, text)
	}
	ctxcapExpiresWithin(t, sceneLink.ExpiresAt, contextcap.LinkTTLScene)
	sceneToken := ctxcapLinkToken(t, sceneLink)
	var stored contextcap.Link
	if err := testPool.QueryRow(context.Background(), `SELECT scope_type, org_id, scope_key, scope_title, COALESCE(source_task_id::text, '')
		FROM context_config_link WHERE token_hash = $1`, contextcap.HashLinkToken(sceneToken)).
		Scan(&stored.ScopeType, &stored.OrgID, &stored.ScopeKey, &stored.ScopeTitle, &stored.SourceTaskID); err != nil {
		t.Fatal(err)
	}
	if stored.ScopeType != contextcap.ScopeScene || stored.OrgID != ctxcapOrg || stored.ScopeKey != ctxcapScene ||
		stored.ScopeTitle != "Ctxcap group" || stored.SourceTaskID != uuidToString(groupTask.ID) {
		t.Fatalf("stored link=%+v", stored)
	}
	var plaintextRows int
	if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM context_config_link WHERE token_hash = $1`, sceneToken).Scan(&plaintextRows); err != nil || plaintextRows != 0 {
		t.Fatalf("token stored in plaintext: rows=%d err=%v", plaintextRows, err)
	}

	// A personal link is never issued in a group.
	if _, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, groupTask, map[string]any{"scope": "person"})); !isError || !strings.Contains(text, "私聊") {
		t.Fatalf("person in group isError=%v text=%q", isError, text)
	}
	oddCaseGroup := f.task(t, ctxcapDispatch("Group", ctxcapScene, ctxcapStaff, ctxcapStaff))
	if _, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, oddCaseGroup, map[string]any{"scope": "person"})); !isError || !strings.Contains(text, "私聊") {
		t.Fatalf("person in odd-case group isError=%v text=%q", isError, text)
	}
	// Only a positively 1:1 conversation gets a personal link: an empty or
	// unknown conversation type may be shared, so both the default and an
	// explicit person scope are refused there.
	for _, kind := range []string{"", "channel"} {
		unknownTask := f.task(t, ctxcapDispatch(kind, "", ctxcapStaff, ctxcapStaff))
		for _, args := range []map[string]any{nil, {"scope": "person"}} {
			if _, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, unknownTask, args)); !isError {
				t.Fatalf("person link minted for conversation type %q (args %v): %s", kind, args, text)
			}
		}
	}
	// A manual rerun copies the source task's dispatch context but is
	// triggered by whoever reran it, so it can mint no link for that sender.
	for _, rerun := range []db.AgentTaskQueue{f.rerunTask(t, ctxcapDispatch("single", "", ctxcapStaff, ctxcapStaff)), f.task(t, ctxcapReplayed(ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff)))} {
		for _, args := range []map[string]any{nil, {"scope": "person"}, {"scope": "scene"}} {
			if _, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, rerun, args)); !isError {
				t.Fatalf("rerun task minted a link (args %v): %s", args, text)
			}
		}
	}
	// A 1:1 chat gets no reusable scene link, with or without its scene:
	// the personal link is the way in and also grants the chat. A merged
	// multi-sender run has no person.
	dmTask := f.task(t, ctxcapDispatch("single", "", ctxcapStaff, ctxcapStaff))
	f.registerDirectScene(t)
	for _, task := range []db.AgentTaskQueue{dmTask, f.task(t, ctxcapDispatch("single", ctxcapDirectScene, ctxcapStaff, ctxcapStaff))} {
		if _, isError, _ := ctxcapToolResult(t, f.ctxcapToolCall(t, task, map[string]any{"scope": "scene"})); !isError {
			t.Fatal("scene link minted in a 1:1 chat")
		}
	}
	mixedTask := f.task(t, ctxcapDispatch("single", "", ctxcapStaff, ctxcapStaff, ctxcapOtherStaff))
	if _, isError, _ := ctxcapToolResult(t, f.ctxcapToolCall(t, mixedTask, nil)); !isError {
		t.Fatal("person link minted for a multi-sender run")
	}
	personLink, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, dmTask, nil))
	if isError || personLink.Scope != contextcap.ScopePerson {
		t.Fatalf("dm mint isError=%v text=%q", isError, text)
	}
	ctxcapExpiresWithin(t, personLink.ExpiresAt, contextcap.LinkTTLPerson)
	personToken := ctxcapLinkToken(t, personLink)

	// Scene links are reusable by every group member until expiry.
	alice, bob := uuid.NewString(), uuid.NewString()
	for _, user := range []string{alice, bob, alice} {
		w := redeem(user, sceneToken)
		ctxcapExpectStatus(t, w, http.StatusOK, "scene redeem")
		var got map[string]string
		ctxcapDecode(t, w, &got)
		if got["agent_id"] != agentID || got["workspace_id"] != testWorkspaceID || got["scope_type"] != contextcap.ScopeScene ||
			got["scope_key"] != ctxcapScene || got["scope_title"] != "Ctxcap group" {
			t.Fatalf("scene redeem=%+v", got)
		}
	}
	grant, err := contextcap.GetLiveGrant(context.Background(), testPool, bob, agentID, contextcap.ScopeScene, ctxcapOrg, ctxcapScene)
	if err != nil || grant.Source != contextcap.GrantSourceAgentLink {
		t.Fatalf("bob scene grant=%+v err=%v", grant, err)
	}
	ctxcapExpiresWithin(t, grant.ExpiresAt.UTC().Format(time.RFC3339), contextcap.GrantTTLScene)

	// Person links are single use.
	w := redeem(alice, personToken)
	ctxcapExpectStatus(t, w, http.StatusOK, "person redeem")
	var personGot map[string]string
	ctxcapDecode(t, w, &personGot)
	if personGot["scope_type"] != contextcap.ScopePerson || personGot["scope_key"] != ctxcapStaff || personGot["scope_title"] != "Alice" {
		t.Fatalf("person redeem=%+v", personGot)
	}
	ctxcapExpectStatus(t, redeem(bob, personToken), http.StatusGone, "person link reuse")
	ctxcapExpectStatus(t, redeem(alice, personToken), http.StatusGone, "person link reuse by redeemer")
	if _, err := contextcap.GetLiveGrant(context.Background(), testPool, bob, agentID, contextcap.ScopePerson, ctxcapOrg, ctxcapStaff); err == nil {
		t.Fatal("second redeemer received a person grant")
	}
	personGrant, err := contextcap.GetLiveGrant(context.Background(), testPool, alice, agentID, contextcap.ScopePerson, ctxcapOrg, ctxcapStaff)
	if err != nil {
		t.Fatal(err)
	}
	ctxcapExpiresWithin(t, personGrant.ExpiresAt.UTC().Format(time.RFC3339), contextcap.GrantTTLPerson)

	// Once Alice holds her personal scope, a later personal link for the same
	// person that reaches another account (forwarded, leaked) cannot take it
	// over; it stays unconsumed, and Alice can still redeem it herself.
	forwardedLink, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, dmTask, nil))
	if isError || forwardedLink.Scope != contextcap.ScopePerson {
		t.Fatalf("second dm mint isError=%v text=%q", isError, text)
	}
	forwardedToken := ctxcapLinkToken(t, forwardedLink)
	ctxcapExpectStatus(t, redeem(bob, forwardedToken), http.StatusConflict, "person link redeemed by another account")
	if _, err := contextcap.GetLiveGrant(context.Background(), testPool, bob, agentID, contextcap.ScopePerson, ctxcapOrg, ctxcapStaff); err == nil {
		t.Fatal("another account took over the personal scope")
	}
	ctxcapExpectStatus(t, redeem(alice, forwardedToken), http.StatusOK, "person link redeemed by its holder")

	// Grants from the links authorize the mobile page.
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodGet, ctxcapScenePath(agentID, ctxcapScene), bob, nil), http.StatusOK, "bob scene via link")

	// Unknown, malformed and expired tokens are gone.
	unknown, _ := contextcap.NewLinkToken()
	ctxcapExpectStatus(t, redeem(alice, unknown), http.StatusGone, "unknown token")
	ctxcapExpectStatus(t, redeem(alice, "not-a-token"), http.StatusGone, "malformed token")
	if _, err := testPool.Exec(context.Background(), `UPDATE context_config_link SET expires_at = now() - interval '1 second' WHERE token_hash = $1`,
		contextcap.HashLinkToken(sceneToken)); err != nil {
		t.Fatal(err)
	}
	ctxcapExpectStatus(t, redeem(uuid.NewString(), sceneToken), http.StatusGone, "expired scene link")
	ctxcapExpectStatus(t, ctxcapMobile(t, router, http.MethodPost, "/api/context-capabilities/links/redeem", alice, `{"token":"x","extra":1}`), http.StatusBadRequest, "unknown field")
}

// A link may open one of the configure page's tabs: the tab follows the
// token (so link redaction still covers the token) and the page reads it;
// an unknown tab is refused. Redaction still hides the token.
func TestContextCapabilitiesLinkOpensATab(t *testing.T) {
	f := newCtxcapFixture(t)
	f.h.cfg.AppURL = "https://app.multica.example/"
	f.cleanupGrantsAndLinks(t)
	groupTask := f.task(t, ctxcapDispatch("group", ctxcapScene, ctxcapStaff, ctxcapStaff))
	link, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, groupTask, map[string]any{"tab": "routines"}))
	if isError || !strings.HasPrefix(link.URL, "https://app.multica.example/dingtalk/configure?link=") || !strings.HasSuffix(link.URL, "&tab=routines") {
		t.Fatalf("tabbed link isError=%v url=%q text=%q", isError, link.URL, text)
	}
	if redacted := redactContextConfigLinks("open " + link.URL); strings.Contains(redacted, strings.TrimSuffix(strings.TrimPrefix(link.URL, "https://app.multica.example/dingtalk/configure?link="), "&tab=routines")) {
		t.Fatalf("token survives redaction: %q", redacted)
	}
	if _, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, groupTask, map[string]any{"tab": "settings"})); !isError || !strings.Contains(text, "tab must be one of") {
		t.Fatalf("unknown tab isError=%v text=%q", isError, text)
	}
	// The removed 公开能力 tab opens the default tab instead of failing a run
	// that listed the tools before it went away.
	if link, isError, text := ctxcapToolResult(t, f.ctxcapToolCall(t, groupTask, map[string]any{"tab": "public"})); isError || strings.Contains(link.URL, "&tab=") {
		t.Fatalf("public tab isError=%v url=%q text=%q", isError, link.URL, text)
	}
}
