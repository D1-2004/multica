package inboundcoord

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
)

type coordQueriesStub struct {
	inbound   bool
	page      []db.ChatMessage
	listErr   error
	lastList  db.ListChatMessagesPageParams
	listCalls int
}

func (s *coordQueriesStub) ListChatMessagesPage(_ context.Context, arg db.ListChatMessagesPageParams) ([]db.ChatMessage, error) {
	s.lastList = arg
	s.listCalls++
	if s.listErr != nil {
		return nil, s.listErr
	}
	return s.page, nil
}

func (s *coordQueriesStub) GetAgent(context.Context, pgtype.UUID) (db.Agent, error) {
	return db.Agent{}, nil
}

func (s *coordQueriesStub) CountRunningTasks(context.Context, pgtype.UUID) (int64, error) {
	return 0, nil
}

func (s *coordQueriesStub) GetAgentInboundCoordinator(context.Context, pgtype.UUID) (bool, error) {
	return s.inbound, nil
}

func testAgentID() pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
}

func TestIssueTitlePrefersUserMessageWhenLookIntoIsShort(t *testing.T) {
	got := IssueTitle(Decision{LookInto: "报名表"}, "问一下冬翔，今天想吃什么")
	if got != "问一下冬翔，今天想吃什么" {
		t.Fatalf("title=%q", got)
	}
}

func TestParseDecisionReply(t *testing.T) {
	got := parseDecision(`{"action":"reply","text":"在的，今天想先对哪件事？","look_into":"","reason":"这是打招呼"}`, Turn{Source: SourceWeb})
	if got.Action != ActionReply || got.UserText == "" || got.Reason != "这是打招呼" {
		t.Fatalf("got %#v", got)
	}
}

func TestParseDecisionIssueFillsAck(t *testing.T) {
	got := parseDecision(`{"action":"issue","text":"","look_into":"报名截止时间"}`, Turn{Source: SourceDigitalEmployee, Message: "看下截止"})
	if got.Action != ActionIssue {
		t.Fatalf("action = %s", got.Action)
	}
	if !strings.Contains(got.UserText, "报名截止时间") {
		t.Fatalf("ack = %q", got.UserText)
	}
}

func TestParseDecisionSilenceRejectedOnWeb(t *testing.T) {
	got := parseDecision(`{"action":"silence","text":""}`, Turn{Source: SourceWeb, Addressed: true, Message: "你好"})
	if got.Action != ActionContinue {
		t.Fatalf("web silence should fail open, got %s", got.Action)
	}
}

func TestParseDecisionSilenceAllowedForDigitalEmployee(t *testing.T) {
	got := parseDecision(`{"action":"silence","text":""}`, Turn{Source: SourceDigitalEmployee, Addressed: true, ChatType: "group", Message: "晚上吃饭吗"})
	if got.Action != ActionSilence {
		t.Fatalf("got %s", got.Action)
	}
}

func TestGroupUnaddressedSilenceWithoutLLM(t *testing.T) {
	c := &Coordinator{LLM: llm.New(llm.Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"})}
	got := c.Decide(context.Background(), Turn{
		Source:    SourceDigitalEmployee,
		Addressed: false,
		ChatType:  "group",
		Message:   "你们晚上吃饭吗",
	})
	if got.Action != ActionSilence {
		t.Fatalf("got %s", got.Action)
	}
}

func TestDecideContinueWhenLLMDisabled(t *testing.T) {
	c := &Coordinator{LLM: llm.New(llm.Config{})}
	got := c.Decide(context.Background(), Turn{Source: SourceWeb, Addressed: true, Message: "你好"})
	if got.Action != ActionContinue {
		t.Fatalf("got %s", got.Action)
	}
}

func TestDecideSkipsWhenAgentSwitchOff(t *testing.T) {
	c := &Coordinator{
		LLM:     llm.New(llm.Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"}),
		Queries: &coordQueriesStub{inbound: false},
	}
	started := time.Now()
	got := c.Decide(context.Background(), Turn{
		Source:    SourceWeb,
		Addressed: true,
		Message:   "你好",
		AgentID:   testAgentID(),
	})
	if got.Action != ActionContinue {
		t.Fatalf("got %s", got.Action)
	}
	if time.Since(started) > 200*time.Millisecond {
		t.Fatalf("switch-off should not call the LLM")
	}
}

func TestIssueTitleAndDescription(t *testing.T) {
	d := Decision{Action: ActionIssue, UserText: "我先核对报名表", LookInto: "报名表截止"}
	if IssueTitle(d, "长正文") != "报名表截止" {
		t.Fatalf("title = %q", IssueTitle(d, "长正文"))
	}
	desc := IssueDescription(d, "帮我看截止时间")
	if !strings.Contains(desc, "前台已对用户说") || !strings.Contains(desc, "帮我看截止时间") {
		t.Fatalf("description = %q", desc)
	}
}

func testSession() db.ChatSession {
	return db.ChatSession{
		ID:          pgtype.UUID{Bytes: [16]byte{9}, Valid: true},
		WorkspaceID: pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		AgentID:     testAgentID(),
	}
}

func newestFirstPage(n int) []db.ChatMessage {
	page := make([]db.ChatMessage, n)
	for i := 0; i < n; i++ {
		// ListChatMessagesPage is newest-first.
		page[i] = db.ChatMessage{Role: "user", Content: "钉钉历史" + string(rune('A'+n-1-i))}
	}
	return page
}

func TestInjectRelatedTasksFormatsRecallHits(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := assoc.NewMemory()
	agent := testAgentID()
	agentID := util.UUIDToString(agent)
	now := time.Now().UTC()
	task, err := store.InsertTask(ctx, assoc.Task{
		WorkspaceID:   "ws",
		AgentID:       agentID,
		IssueID:       "issue-eat",
		Purpose:       "向冬翔确认今晚吃什么",
		Status:        assoc.StatusWaiting,
		LastTouchedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.InsertEdge(ctx, assoc.Edge{
		WorkspaceID: "ws",
		AgentID:     agentID,
		SrcType:     assoc.NodeTask,
		SrcID:       task.ID,
		DstType:     assoc.NodeScene,
		DstID:       "cid-dongxiang",
		Rel:         assoc.RelOutreach,
	}); err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{Assoc: assoc.NewService(store)}
	got := c.injectRelatedTasks(ctx, Turn{
		WorkspaceID:    "ws",
		AgentID:        agent,
		ConversationID: "cid-dongxiang",
	})
	if !strings.Contains(got.RelatedTasks, "向冬翔确认今晚吃什么") {
		t.Fatalf("related_tasks=%q", got.RelatedTasks)
	}
	if !strings.Contains(got.RelatedTasks, "issue-eat") {
		t.Fatalf("related_tasks missing issue: %q", got.RelatedTasks)
	}
}

func TestTurnFromChatSessionCopiesWorkspaceID(t *testing.T) {
	c := &Coordinator{Queries: &coordQueriesStub{}}
	turn := c.TurnFromChatSession(context.Background(), testSession(), SourceWeb, true, "p2p", "网页", "", "你好")
	if turn.WorkspaceID == "" {
		t.Fatal("workspace_id should come from the chat session")
	}
}

func TestTurnFromChatSessionRobotLoadsTenDingTalkMessages(t *testing.T) {
	q := &coordQueriesStub{page: newestFirstPage(10)}
	c := &Coordinator{Queries: q}
	turn := c.TurnFromChatSession(context.Background(), testSession(), SourceRobot, true, "p2p", "须莫", "须莫", "帮我看看今天有什么新闻")
	if q.listCalls != 1 || q.lastList.Limit != dingtalkHistoryLimit {
		t.Fatalf("robot history lookup = calls %d limit %d, want 1 call limit %d", q.listCalls, q.lastList.Limit, dingtalkHistoryLimit)
	}
	if len(turn.History) != 0 {
		t.Fatalf("robot must not use web Multica history, got %d", len(turn.History))
	}
	if len(turn.DingTalkHistory) != 10 {
		t.Fatalf("dingtalk history = %d, want 10", len(turn.DingTalkHistory))
	}
	if turn.DingTalkHistory[0].Content != "钉钉历史A" || turn.DingTalkHistory[9].Content != "钉钉历史J" {
		t.Fatalf("history order = %#v", turn.DingTalkHistory)
	}
	prompt := buildUserPrompt(turn)
	if !strings.Contains(prompt, "recent_dingtalk_history:") || strings.Contains(prompt, "recent_multica_history:") {
		t.Fatalf("prompt = %q", prompt)
	}
	if !strings.Contains(prompt, "钉钉历史A") || !strings.Contains(prompt, "钉钉历史J") {
		t.Fatalf("prompt missing loaded dingtalk rows: %q", prompt)
	}
}

func TestTurnFromChatSessionDigitalEmployeeLoadsTenDingTalkMessages(t *testing.T) {
	q := &coordQueriesStub{page: newestFirstPage(10)}
	c := &Coordinator{Queries: q}
	turn := c.TurnFromChatSession(context.Background(), testSession(), SourceDigitalEmployee, true, "group", "项目群", "同事", "帮我看看今天有什么新闻")
	if q.lastList.Limit != dingtalkHistoryLimit || len(turn.DingTalkHistory) != 10 || len(turn.History) != 0 {
		t.Fatalf("de history limit=%d dingtalk=%d web=%d", q.lastList.Limit, len(turn.DingTalkHistory), len(turn.History))
	}
}

func TestTurnFromChatSessionWebDoesNotLoadDingTalkHistory(t *testing.T) {
	q := &coordQueriesStub{page: newestFirstPage(4)}
	c := &Coordinator{Queries: q}
	turn := c.TurnFromChatSession(context.Background(), testSession(), SourceWeb, true, "p2p", "网页", "", "你好")
	if q.listCalls != 1 || q.lastList.Limit != historyLimit {
		t.Fatalf("web history lookup = calls %d limit %d, want 1 call limit %d", q.listCalls, q.lastList.Limit, historyLimit)
	}
	if len(turn.DingTalkHistory) != 0 {
		t.Fatalf("web must not load dingtalk history, got %d", len(turn.DingTalkHistory))
	}
	if len(turn.History) != 4 {
		t.Fatalf("web history = %d, want 4", len(turn.History))
	}
	prompt := buildUserPrompt(turn)
	if strings.Contains(prompt, "recent_dingtalk_history:") || !strings.Contains(prompt, "recent_multica_history:") {
		t.Fatalf("web prompt = %q", prompt)
	}
}

func TestAttachDingTalkConversationUsesWindowWhenSessionMissing(t *testing.T) {
	window := make([]HistoryLine, 12)
	for i := range window {
		window[i] = HistoryLine{Role: "user", Content: "窗" + string(rune('A'+i))}
	}
	turn := AttachDingTalkConversation(context.Background(), nil, Turn{Source: SourceRobot, Message: "帮我看看今天有什么新闻"}, pgtype.UUID{}, window)
	if len(turn.DingTalkHistory) != dingtalkHistoryLimit {
		t.Fatalf("window history = %d, want %d", len(turn.DingTalkHistory), dingtalkHistoryLimit)
	}
	if turn.DingTalkHistory[0].Content != "窗C" || turn.DingTalkHistory[9].Content != "窗L" {
		t.Fatalf("window clipped = %#v", turn.DingTalkHistory)
	}
}

func TestAttachDingTalkConversationSkippedForWeb(t *testing.T) {
	q := &coordQueriesStub{page: newestFirstPage(10)}
	c := &Coordinator{Queries: q}
	turn := AttachDingTalkConversation(context.Background(), c, Turn{Source: SourceWeb, Message: "你好"}, testSession().ID, []HistoryLine{{Role: "user", Content: "钉钉不该出现"}})
	if q.listCalls != 0 || len(turn.DingTalkHistory) != 0 {
		t.Fatalf("web attach leaked dingtalk history calls=%d lines=%d", q.listCalls, len(turn.DingTalkHistory))
	}
}

func TestFillDingTalkHistorySkippedForWeb(t *testing.T) {
	q := &coordQueriesStub{page: newestFirstPage(10)}
	c := &Coordinator{Queries: q}
	turn := c.FillDingTalkHistory(context.Background(), Turn{Source: SourceWeb, Message: "你好"}, testSession().ID)
	if q.listCalls != 0 || len(turn.DingTalkHistory) != 0 {
		t.Fatalf("web FillDingTalkHistory leaked dingtalk history calls=%d lines=%d", q.listCalls, len(turn.DingTalkHistory))
	}
}

func TestFillDingTalkHistoryFailureLeavesEmptyAndDecideStillCallable(t *testing.T) {
	q := &coordQueriesStub{listErr: context.DeadlineExceeded}
	c := &Coordinator{Queries: q, LLM: llm.New(llm.Config{})}
	turn := c.FillDingTalkHistory(context.Background(), Turn{Source: SourceRobot, Message: "帮我看看今天有什么新闻"}, testSession().ID)
	if q.listCalls != 1 || q.lastList.Limit != dingtalkHistoryLimit {
		t.Fatalf("failed lookup still requests last %d, calls=%d limit=%d", dingtalkHistoryLimit, q.listCalls, q.lastList.Limit)
	}
	if len(turn.DingTalkHistory) != 0 {
		t.Fatalf("failed lookup must leave empty history, got %#v", turn.DingTalkHistory)
	}
	got := c.Decide(context.Background(), turn)
	if got.Action != ActionContinue {
		t.Fatalf("empty history must not block Decide, action=%s", got.Action)
	}
}

func TestTurnFromChatSessionEmptyPageStillListsTenForRobotAndDigitalEmployee(t *testing.T) {
	for _, src := range []Source{SourceRobot, SourceDigitalEmployee} {
		q := &coordQueriesStub{}
		c := &Coordinator{Queries: q}
		turn := c.TurnFromChatSession(context.Background(), testSession(), src, true, "p2p", "须莫", "须莫", "帮我看看今天有什么新闻")
		if q.listCalls != 1 || q.lastList.Limit != dingtalkHistoryLimit {
			t.Fatalf("%s empty page lookup = calls %d limit %d, want 1 call limit %d", src, q.listCalls, q.lastList.Limit, dingtalkHistoryLimit)
		}
		if len(turn.DingTalkHistory) != 0 {
			t.Fatalf("%s empty page must leave DingTalkHistory empty, got %#v", src, turn.DingTalkHistory)
		}
		if len(turn.History) != 0 {
			t.Fatalf("%s must not load web Multica history, got %d", src, len(turn.History))
		}
	}
}

func TestAttachDingTalkConversationPrefersBoundSessionOverWindow(t *testing.T) {
	q := &coordQueriesStub{page: newestFirstPage(10)}
	c := &Coordinator{Queries: q}
	window := []HistoryLine{{Role: "user", Content: "窗口不该出现"}}
	turn := AttachDingTalkConversation(
		context.Background(),
		c,
		Turn{Source: SourceDigitalEmployee, Message: "帮我看看今天有什么新闻"},
		testSession().ID,
		window,
	)
	if q.listCalls != 1 || q.lastList.Limit != dingtalkHistoryLimit {
		t.Fatalf("digital-employee attach lookup = calls %d limit %d, want 1 call limit %d", q.listCalls, q.lastList.Limit, dingtalkHistoryLimit)
	}
	prompt := buildUserPrompt(turn)
	if !strings.Contains(prompt, "recent_dingtalk_history:") || !strings.Contains(prompt, "钉钉历史A") || !strings.Contains(prompt, "钉钉历史J") {
		t.Fatalf("prompt missing bound session history: %q", prompt)
	}
	if strings.Contains(prompt, "窗口不该出现") {
		t.Fatalf("bound session must win over window: %q", prompt)
	}
}

func TestWindowHistorySkipsCurrentAndAttachKeepsLastTenForDigitalEmployee(t *testing.T) {
	texts := make([]string, 0, 13)
	for i := 0; i < 12; i++ {
		texts = append(texts, "窗"+string(rune('A'+i)))
	}
	current := "帮我看看今天有什么新闻"
	texts = append(texts, current, "  ")
	window := WindowHistory(texts, current)
	turn := AttachDingTalkConversation(
		context.Background(),
		nil,
		Turn{Source: SourceDigitalEmployee, Message: current},
		pgtype.UUID{},
		window,
	)
	if len(turn.DingTalkHistory) != dingtalkHistoryLimit {
		t.Fatalf("digital-employee window history = %d, want %d", len(turn.DingTalkHistory), dingtalkHistoryLimit)
	}
	if turn.DingTalkHistory[0].Content != "窗C" || turn.DingTalkHistory[9].Content != "窗L" {
		t.Fatalf("clipped window = %#v", turn.DingTalkHistory)
	}
	prompt := buildUserPrompt(turn)
	if !strings.Contains(prompt, "recent_dingtalk_history:") || !strings.Contains(prompt, "窗C") || !strings.Contains(prompt, "窗L") {
		t.Fatalf("prompt missing clipped dingtalk window: %q", prompt)
	}
	if strings.Contains(prompt, "user: "+current) {
		t.Fatalf("current message leaked into dingtalk history: %q", prompt)
	}
}

func TestAttachDingTalkConversationDigitalEmployeeListsTenEvenWhenPageEmpty(t *testing.T) {
	q := &coordQueriesStub{}
	c := &Coordinator{Queries: q}
	window := []HistoryLine{{Role: "user", Content: "窗里旧话"}}
	turn := AttachDingTalkConversation(
		context.Background(),
		c,
		Turn{Source: SourceDigitalEmployee, Message: "帮我看看今天有什么新闻"},
		testSession().ID,
		window,
	)
	if q.listCalls != 1 || q.lastList.Limit != dingtalkHistoryLimit {
		t.Fatalf("empty page still lists last %d, calls=%d limit=%d", dingtalkHistoryLimit, q.listCalls, q.lastList.Limit)
	}
	if len(turn.DingTalkHistory) != 1 || turn.DingTalkHistory[0].Content != "窗里旧话" {
		t.Fatalf("empty session must fall back to window, got %#v", turn.DingTalkHistory)
	}
}

func TestBuildUserPromptStaleSessionTitleIsNotRecentTopic(t *testing.T) {
	prompt := buildUserPrompt(Turn{
		Source:            SourceRobot,
		Addressed:         true,
		ChatType:          "p2p",
		ConversationTitle: "须莫🥥：你有阿里内外cli吗,有哪些功能",
		Message:           "我们前面说啥来着，直接回复我不要去做issue",
		DingTalkHistory: []HistoryLine{
			{Role: "user", Content: "你好"},
			{Role: "assistant", Content: "哈喽，我在呢。"},
			{Role: "user", Content: "看看今天的新闻"},
			{Role: "assistant", Content: "我先去搜一下今天的热点新闻。"},
			{Role: "user", Content: "我们前面说啥来着"},
			{Role: "assistant", Content: "我去翻一下咱们刚才的聊天记录。"},
		},
	})
	if !strings.Contains(prompt, "session_title:") || strings.Contains(prompt, "\nconversation: ") {
		t.Fatalf("stale title must be labeled session_title, prompt=%q", prompt)
	}
	if !strings.Contains(prompt, "recent_dingtalk_history:") || !strings.Contains(prompt, "看看今天的新闻") {
		t.Fatalf("recent dingtalk history missing: %q", prompt)
	}
	if !strings.Contains(systemPrompt, "Never invent a topic from session_title") {
		t.Fatal("system prompt must forbid answering recency from session_title")
	}
}

func TestBuildUserPromptNewsTurnKeepsIssueContract(t *testing.T) {
	prompt := buildUserPrompt(Turn{
		Source:          SourceRobot,
		Addressed:       true,
		ChatType:        "p2p",
		Message:         "帮我看看今天有什么新闻",
		DingTalkHistory: []HistoryLine{{Role: "user", Content: "昨天那个表"}, {Role: "assistant", Content: "我去对一下"}},
	})
	if !strings.Contains(prompt, "recent_dingtalk_history:") || !strings.Contains(prompt, "昨天那个表") {
		t.Fatalf("prompt = %q", prompt)
	}
	got := parseDecision(`{"action":"issue","text":"我先去看今天新闻","look_into":"今天新闻","reason":"要查实时资讯"}`, Turn{Source: SourceRobot, Message: "帮我看看今天有什么新闻"})
	if got.Action != ActionIssue {
		t.Fatalf("news must stay issue, got %s", got.Action)
	}
}

func TestParseDecisionInvalidJSON(t *testing.T) {
	got := parseDecision("not-json", Turn{Source: SourceWeb, Message: "hi"})
	if got.Action != ActionContinue {
		t.Fatalf("got %s", got.Action)
	}
}
