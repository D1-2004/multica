package inboundcoord

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/assoc"
	"github.com/multica-ai/multica/server/internal/service/scenememory"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
	"github.com/multica-ai/multica/server/pkg/llm"
)

type coordQueriesStub struct {
	inbound    bool
	persona    string
	replyTone  string
	page       []db.ChatMessage
	listErr    error
	lastList   db.ListChatMessagesPageParams
	listCalls  int
	sceneFlags db.AgentSceneMemoryFlags
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

func (s *coordQueriesStub) GetAgentVoice(context.Context, pgtype.UUID) (db.GetAgentVoiceRow, error) {
	return db.GetAgentVoiceRow{Persona: s.persona, ReplyTone: s.replyTone}, nil
}

func (s *coordQueriesStub) GetAgentSceneMemoryFlags(context.Context, pgtype.UUID) (db.AgentSceneMemoryFlags, error) {
	return s.sceneFlags, nil
}

type sceneMemoryStub struct {
	rows map[string]db.SceneMemory
	last scenememory.Identity
}

func (s *sceneMemoryStub) Get(_ context.Context, id scenememory.Identity) (db.SceneMemory, error) {
	s.last = id
	row, ok := s.rows[id.SceneKey]
	if !ok {
		return db.SceneMemory{}, context.Canceled
	}
	return row, nil
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

func TestIssueTitleUsesDeliverableNotDelegatorPrefix(t *testing.T) {
	got := IssueTitle(Decision{Purpose: "冬翔委托：向dxxh确认明天上午有没有空"}, "原文")
	if got != "向dxxh确认明天上午有没有空" {
		t.Fatalf("title=%q", got)
	}
	got = IssueTitle(Decision{Purpose: "冬翔委托：<@abc> 向dxxh确认明天有空"}, "")
	if got != "向dxxh确认明天有空" {
		t.Fatalf("mention title=%q", got)
	}
}

func TestParseDecisionReply(t *testing.T) {
	got := parseDecision(`{"action":"reply","text":"在的，今天想先对哪件事？","look_into":"","reason":"这是打招呼"}`, Turn{Source: SourceWeb})
	if got.Action != ActionReply || got.UserText == "" || got.Reason != "这是打招呼" {
		t.Fatalf("got %#v", got)
	}
}

func TestParseDecisionIssueEmptyTextContinues(t *testing.T) {
	got := parseDecision(`{"action":"issue","text":"","look_into":"报名截止时间"}`, Turn{Source: SourceDigitalEmployee, Message: "看下截止"})
	if got.Action != ActionContinue {
		t.Fatalf("empty issue text must not synthesize an ack, got %s text=%q", got.Action, got.UserText)
	}
	if strings.Contains(got.UserText, "核对") {
		t.Fatalf("ack fallback leaked: %q", got.UserText)
	}
}

func TestParseDecisionIssueDropsFinishIssueID(t *testing.T) {
	got := parseDecision(`{"action":"issue","text":"我去问冬翔晚上打不打球","issue_id":"8aae2a90-009e-4338-b17a-13ce6ff2f82a","delegator":"冬翔","purpose":"向冬翔确认晚上是否打球","intent":"ask"}`, Turn{
		Source:     SourceRobot,
		SenderName: "冬翔",
		Message:    "问下冬翔晚上打球",
	})
	if got.Action != ActionIssue {
		t.Fatalf("action=%s", got.Action)
	}
	if got.IssueID != "" {
		t.Fatalf("finish must not continue via issue_id, issue_id=%q", got.IssueID)
	}
	if got.UserText != "我去问冬翔晚上打不打球" {
		t.Fatalf("text=%q", got.UserText)
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

func TestParseDecisionSilenceAllowedForAddressedGroupFlood(t *testing.T) {
	got := parseDecision(`{"action":"silence","text":""}`, Turn{
		Source: SourceDigitalEmployee, Addressed: true, ChatType: "group",
		Message: "R9-P8-FLOOD-3 unrelated noise",
	})
	if got.Action != ActionSilence {
		t.Fatalf("addressed group flood may silence, got %s", got.Action)
	}
}

func TestParseDecisionSilenceAllowedForDMFloodNoise(t *testing.T) {
	got := parseDecision(`{"action":"silence","text":""}`, Turn{
		Source: SourceDigitalEmployee, Addressed: true, ChatType: "p2p",
		Message: "R9-P8-FLOOD-7 unrelated noise",
	})
	if got.Action != ActionSilence {
		t.Fatalf("DM numbered flood may silence, got %s", got.Action)
	}
}

func TestParseDecisionSilenceAllowedForAddressedEmojiAndThanks(t *testing.T) {
	emoji := parseDecision(`{"action":"silence","text":""}`, Turn{
		Source: SourceDigitalEmployee, Addressed: true, ChatType: "group",
		Message: "👍",
	})
	if emoji.Action != ActionSilence {
		t.Fatalf("addressed emoji may silence, got %s", emoji.Action)
	}
	thanks := parseDecision(`{"action":"reply","text":"嗯"}`, Turn{
		Source: SourceDigitalEmployee, Addressed: true, ChatType: "group",
		Message: "谢谢",
	})
	if thanks.Action != ActionReply || thanks.UserText != "嗯" {
		t.Fatalf("addressed thanks should stay a short reply, got %#v", thanks)
	}
}

func TestSystemPromptHumanGroupFloodRules(t *testing.T) {
	must := []string{
		"You are a colleague in the group, not a minute-taker",
		"Never one Issue per flood line",
		"Addressed sticker, emoji-only",
		"Addressed 在吗 / 你好 / 还在吗",
		"Addressed thanks / 谢谢 / 好的 / 辛苦了",
		"A collected current_message that mixes flood and one real ask",
		"current_message may be several inbound lines collected while the person was still typing",
		"Two colleagues talking to each other",
		"Do not volunteer 我来帮你们建事项",
		"我去问 dxxh 周五三点",
		"a real teammate, not a helpdesk",
	}
	for _, needle := range must {
		if !strings.Contains(systemPrompt, needle) {
			t.Fatalf("system prompt missing human/flood rule %q", needle)
		}
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

func TestDecisionObserverReceivesVerdictFromEveryIngress(t *testing.T) {
	t.Parallel()
	var observed Decision
	ctx := WithDecisionObserver(context.Background(), func(decision Decision) {
		observed = decision
	})
	c := &Coordinator{LLM: llm.New(llm.Config{})}
	got := c.Decide(ctx, Turn{Source: SourceDigitalEmployee, Addressed: true, Message: "你好"})
	if observed.Action != got.Action || observed.Source != SourceDigitalEmployee {
		t.Fatalf("observed=%#v got=%#v", observed, got)
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
	for _, want := range []string{"前台已对用户说", "帮我看截止时间", "当前可信钉钉派发事件里的发信人", "Issue 创建人或评论人只表示谁执行了 Issue 工具", "协助者", "数字员工事件", "机器人事件", "消息接收人", "当前能解除阻塞", "不要固定回复委托人", "必须实际给一个明确的人发送", "不得写“任务完成”"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q: %q", want, desc)
		}
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

func TestFillVoiceCopiesPersonaAndTone(t *testing.T) {
	t.Parallel()
	c := &Coordinator{Queries: &coordQueriesStub{persona: "靠谱同事", replyTone: "短句、不客套"}}
	turn := Turn{AgentID: testAgentID()}
	c.FillVoice(context.Background(), &turn)
	if turn.Persona != "靠谱同事" || turn.ReplyTone != "短句、不客套" {
		t.Fatalf("voice=%q / %q", turn.Persona, turn.ReplyTone)
	}
}

func TestTurnFromChatSessionLoadsVoice(t *testing.T) {
	q := &coordQueriesStub{persona: "靠谱同事", replyTone: "短句、不客套"}
	c := &Coordinator{Queries: q}
	turn := c.TurnFromChatSession(context.Background(), testSession(), SourceRobot, true, "p2p", "", "", "你好")
	if turn.Persona != "靠谱同事" || turn.ReplyTone != "短句、不客套" {
		t.Fatalf("voice=%q / %q", turn.Persona, turn.ReplyTone)
	}
	prompt := buildUserPrompt(turn)
	if !strings.Contains(prompt, "agent_persona: 靠谱同事") || !strings.Contains(prompt, "agent_reply_tone: 短句、不客套") {
		t.Fatalf("prompt=%q", prompt)
	}
}

func TestTurnFromChatSessionCopiesWorkspaceID(t *testing.T) {
	c := &Coordinator{Queries: &coordQueriesStub{}}
	turn := c.TurnFromChatSession(context.Background(), testSession(), SourceWeb, true, "p2p", "网页", "", "你好")
	if turn.WorkspaceID == "" {
		t.Fatal("workspace_id should come from the chat session")
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
	if strings.Contains(prompt, "recent_dingtalk_history") || !strings.Contains(prompt, "recent_multica_history:") {
		t.Fatalf("web prompt = %q", prompt)
	}
}

func TestBuildUserPromptOmitsTitleForRobotAndDigitalEmployee(t *testing.T) {
	stale := "须莫🥥：你有阿里内外cli吗,有哪些功能"
	for _, src := range []Source{SourceRobot, SourceDigitalEmployee} {
		prompt := buildUserPrompt(Turn{
			Source:            src,
			Addressed:         true,
			ChatType:          "p2p",
			ConversationTitle: stale,
			Message:           "我们前面说啥来着，直接回复我不要去做issue",
			DingTalkHistory: []HistoryLine{
				{Role: "user", Content: "看看今天的新闻"},
				{Role: "assistant", Content: "我先去搜一下今天的热点新闻。"},
			},
		})
		if strings.Contains(prompt, "session_title:") || strings.Contains(prompt, stale) || strings.Contains(prompt, "\nconversation: ") {
			t.Fatalf("%s prompt leaked dingTalk title: %q", src, prompt)
		}
		if !strings.Contains(prompt, "recent_dingtalk_history") || !strings.Contains(prompt, "看看今天的新闻") {
			t.Fatalf("%s recent dingtalk history missing: %q", src, prompt)
		}
	}
	web := buildUserPrompt(Turn{
		Source:            SourceWeb,
		Addressed:         true,
		ConversationTitle: "网页会话标题",
		Message:           "你好",
		History:           []HistoryLine{{Role: "user", Content: "昨天那个表"}},
	})
	if !strings.Contains(web, "session_title: 网页会话标题") {
		t.Fatalf("web may keep session title, prompt=%q", web)
	}
}

func TestTurnFromChatSessionRobotAndDigitalEmployeeDropTitle(t *testing.T) {
	q := &coordQueriesStub{page: newestFirstPage(2)}
	c := &Coordinator{Queries: q}
	stale := "须莫🥥：你有阿里内外cli吗,有哪些功能"
	for _, src := range []Source{SourceRobot, SourceDigitalEmployee} {
		turn := c.TurnFromChatSession(context.Background(), testSession(), src, true, "p2p", stale, "须莫", "我们前面说啥来着")
		if turn.ConversationTitle != "" {
			t.Fatalf("%s ConversationTitle = %q, want empty", src, turn.ConversationTitle)
		}
		prompt := buildUserPrompt(turn)
		if strings.Contains(prompt, stale) || strings.Contains(prompt, "session_title:") {
			t.Fatalf("%s loaded title into prompt: %q", src, prompt)
		}
	}
}

func TestBuildUserPromptDingTalkHistoryNewestFirst(t *testing.T) {
	prompt := buildUserPrompt(Turn{
		Source:  SourceRobot,
		Message: "当前",
		DingTalkHistory: []HistoryLine{
			{Role: "user", Content: "更早的消息"},
			{Role: "user", Content: "较新的消息"},
		},
	})
	older := strings.Index(prompt, "更早的消息")
	newer := strings.Index(prompt, "较新的消息")
	if older < 0 || newer < 0 || newer > older {
		t.Fatalf("want newest-first dingtalk history, prompt=%q", prompt)
	}
}

func TestBuildUserPromptIncludesHostSceneMemory(t *testing.T) {
	prompt := buildUserPrompt(Turn{
		Source:              SourceDigitalEmployee,
		Addressed:           true,
		ChatType:            "p2p",
		Message:             "GoalMate 是什么",
		SceneMemory:         "## 稳定知识与约定\n- GoalMate 是工具，不是数字员工",
		SceneMemoryRevision: 4,
	})
	if !strings.Contains(prompt, "scene_memory_revision: 4") {
		t.Fatalf("missing revision: %q", prompt)
	}
	if !strings.Contains(prompt, "Host-provided") || !strings.Contains(prompt, "GoalMate 是工具，不是数字员工") {
		t.Fatalf("missing scene memory: %q", prompt)
	}
	if !strings.Contains(systemPrompt, "assoc_recall remains the only Issue truth") {
		t.Fatal("system prompt must keep assoc as the only issue truth")
	}
	if !strings.Contains(systemPrompt, "finish action=reply from scene_memory only") {
		t.Fatal("system prompt must allow scene_memory to answer scene questions")
	}
	if !strings.Contains(systemPrompt, "do not call issue_comment_add") {
		t.Fatal("system prompt must not comment onto a busy Issue")
	}
	if !strings.Contains(systemPrompt, "Teaching or correcting this scene") {
		t.Fatal("system prompt must not open an Issue for scene teaching")
	}
	if !strings.Contains(systemPrompt, "从记忆里去掉 X") {
		t.Fatal("system prompt must treat dropping a scene fact as memory rewrite, not an Issue")
	}
	if !strings.Contains(systemPrompt, "reply from scene_memory only") {
		t.Fatal("system prompt must answer 有哪些记忆 from scene_memory only")
	}
	if !strings.Contains(systemPrompt, "手头有哪些事情") {
		t.Fatal("system prompt must not list scene_memory bullets as open work")
	}
	if !strings.Contains(systemPrompt, "当前记忆为空") {
		t.Fatal("system prompt must not claim Host memory is empty while 稳定知识 remains")
	}
	if !strings.Contains(systemPrompt, "给X发一条消息") {
		t.Fatal("system prompt must ask for a missing send payload instead of opening an Issue")
	}
	if !strings.Contains(systemPrompt, "交付物一条笑话") {
		t.Fatal("system prompt must pack a job brief, not dump scene_memory, for a complete send")
	}
	if !strings.Contains(systemPrompt, "short burst") {
		t.Fatal("system prompt must answer a collected burst in one reply")
	}
	if !strings.Contains(systemPrompt, "since=7d") {
		t.Fatal("system prompt must recall older work beyond the default 48h window")
	}
}

func TestParseDecisionCoercesMissingSendPayloadToReply(t *testing.T) {
	got := parseDecision(`{"action":"issue","text":"我去给须莫发消息，请问要说什么？","look_into":"冬翔委托：向须莫发送消息","delegator":"冬翔","purpose":"向须莫发送消息","intent":"other","reason":"要发消息"}`, Turn{
		Source:     SourceDigitalEmployee,
		SenderName: "冬翔",
		ChatType:   "p2p",
		Message:    "给须莫发一条消息",
	})
	if got.Action != ActionReply {
		t.Fatalf("missing payload must reply, got %s", got.Action)
	}
	if got.LookInto != "" {
		t.Fatalf("reply must not keep look_into=%q", got.LookInto)
	}
	if !strings.Contains(got.UserText, "要说什么") {
		t.Fatalf("text=%q", got.UserText)
	}

	joke := parseDecision(`{"action":"issue","text":"我去给须莫发个笑话","look_into":"委托人冬翔；对象须莫；交付物一条笑话","delegator":"冬翔","purpose":"向须莫发送一个笑话","intent":"other"}`, Turn{
		Source:     SourceDigitalEmployee,
		SenderName: "冬翔",
		Message:    "发个笑话给他",
	})
	if joke.Action != ActionIssue {
		t.Fatalf("named payload must stay issue, got %s", joke.Action)
	}
}

func TestBuildUserPromptResetShowsEmptyHostBlock(t *testing.T) {
	prompt := buildUserPrompt(Turn{
		Source:              SourceDigitalEmployee,
		Addressed:           true,
		ChatType:            "p2p",
		Message:             "ALPHA-7749 是什么",
		SceneMemory:         "",
		SceneMemoryRevision: 3,
		DingTalkHistory: []HistoryLine{
			{Role: "user", Content: "灌水12：食堂窗口12 今天供应番茄炒蛋，与探针无关。"},
		},
	})
	if !strings.Contains(prompt, "scene_memory_revision: 3") {
		t.Fatalf("reset still injects revision: %q", prompt)
	}
	if !strings.Contains(prompt, "(empty)") {
		t.Fatalf("reset Host block must be empty: %q", prompt)
	}
	if strings.Contains(prompt, "ALPHA-7749 是会议室预约脚本") {
		t.Fatalf("cleared text must not reappear in Host block: %q", prompt)
	}
}

func TestBuildUserPromptHostFactOutsideLastNHistory(t *testing.T) {
	prompt := buildUserPrompt(Turn{
		Source:              SourceDigitalEmployee,
		Addressed:           true,
		ChatType:            "p2p",
		Message:             "ALPHA-7749 是什么",
		SceneMemory:         "蓝鲸探针 ALPHA-7749 是会议室预约脚本，不是数字员工。",
		SceneMemoryRevision: 5,
		DingTalkHistory: []HistoryLine{
			{Role: "user", Content: "灌水12：食堂窗口12 今天供应番茄炒蛋，与探针无关。"},
		},
	})
	host, history, current := splitCoordinatorPrompt(prompt)
	if !strings.Contains(host, "ALPHA-7749") || !strings.Contains(host, "会议室预约脚本") {
		t.Fatalf("host missing probe: %q", host)
	}
	if strings.Contains(history, "ALPHA-7749") {
		t.Fatalf("last-N history leaked probe: %q", history)
	}
	if !strings.Contains(current, "ALPHA-7749 是什么") {
		t.Fatalf("current message missing ask: %q", current)
	}
}

func TestPrefetchSceneMemoryInjectsMatchingSceneOnly(t *testing.T) {
	mem := &sceneMemoryStub{rows: map[string]db.SceneMemory{
		"cid-a": {SceneKey: "cid-a", MemoryText: "GAMMA-A-881 是报表工具", MemoryRevision: 2},
		"cid-b": {SceneKey: "cid-b", MemoryText: "这个群还没有口径", MemoryRevision: 1},
	}}
	c := &Coordinator{
		Queries:     &coordQueriesStub{sceneFlags: db.AgentSceneMemoryFlags{RecallEnabled: true}},
		SceneMemory: mem,
	}
	turn := Turn{
		Source:         SourceDigitalEmployee,
		ChatType:       "group",
		AgentID:        testAgentID(),
		WorkspaceID:    "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		ConversationID: "cid-b",
		DWSOrgID:       "org-1",
	}
	c.prefetchSceneMemory(context.Background(), &turn)
	if turn.SceneMemory != "这个群还没有口径" || turn.SceneMemoryRevision != 1 {
		t.Fatalf("got %q rev=%d", turn.SceneMemory, turn.SceneMemoryRevision)
	}
	if strings.Contains(turn.SceneMemory, "报表工具") {
		t.Fatal("group A leaked into group B")
	}
	if mem.last.SceneKind != scenememory.KindGroup || mem.last.SceneKey != "cid-b" {
		t.Fatalf("lookup identity=%+v", mem.last)
	}
}

func TestPrefetchSceneMemorySanitizesHostDebris(t *testing.T) {
	mem := &sceneMemoryStub{rows: map[string]db.SceneMemory{
		"cid-a": {SceneKey: "cid-a", MemoryText: strings.Join([]string{
			"## 场域定位",
			"冬翔",
			"成员：冬翔",
			"## 稳定知识与约定",
			"- feat/agentic-memory-view 已合入 commit d2d5c86ed",
			"- 多件事情沟通时使用 markdown 无序列表格式 (来自冬翔, 9月3日 13:34的发言)",
			"- 回复偏好：简短直接 (来自东翔测试号, 9月3日 17:27的发言)",
			"## 纠正信号",
			"## 待确认",
		}, "\n"), MemoryRevision: 18},
	}}
	c := &Coordinator{
		Queries:     &coordQueriesStub{sceneFlags: db.AgentSceneMemoryFlags{RecallEnabled: true}},
		SceneMemory: mem,
	}
	turn := Turn{
		Source:         SourceDigitalEmployee,
		ChatType:       "p2p",
		AgentID:        testAgentID(),
		WorkspaceID:    "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		ConversationID: "cid-a",
		DWSOrgID:       "org-1",
	}
	c.prefetchSceneMemory(context.Background(), &turn)
	if strings.Contains(turn.SceneMemory, "d2d5c86ed") || strings.Contains(turn.SceneMemory, "东翔测试号") {
		t.Fatalf("host inject leaked debris: %q", turn.SceneMemory)
	}
	if !strings.Contains(turn.SceneMemory, "markdown 无序列表") {
		t.Fatalf("human fact stripped: %q", turn.SceneMemory)
	}
	if turn.SceneMemoryRevision != 18 {
		t.Fatalf("revision=%d", turn.SceneMemoryRevision)
	}
}

func TestPrefetchSceneMemorySkippedWhenRecallDisabled(t *testing.T) {
	mem := &sceneMemoryStub{rows: map[string]db.SceneMemory{
		"cid-a": {SceneKey: "cid-a", MemoryText: "不该出现", MemoryRevision: 4},
	}}
	c := &Coordinator{
		Queries:     &coordQueriesStub{},
		SceneMemory: mem,
	}
	turn := Turn{
		Source:         SourceDigitalEmployee,
		ChatType:       "p2p",
		AgentID:        testAgentID(),
		WorkspaceID:    "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		ConversationID: "cid-a",
		DWSOrgID:       "org-1",
	}
	c.prefetchSceneMemory(context.Background(), &turn)
	if turn.SceneMemory != "" || turn.SceneMemoryRevision != 0 {
		t.Fatalf("recall-off injected %q rev=%d", turn.SceneMemory, turn.SceneMemoryRevision)
	}
}

func TestPrefetchSceneMemorySkippedForWebAndRobot(t *testing.T) {
	mem := &sceneMemoryStub{rows: map[string]db.SceneMemory{
		"cid-a": {SceneKey: "cid-a", MemoryText: "不该出现", MemoryRevision: 4},
	}}
	c := &Coordinator{
		Queries:     &coordQueriesStub{sceneFlags: db.AgentSceneMemoryFlags{RecallEnabled: true}},
		SceneMemory: mem,
	}
	for _, source := range []Source{SourceWeb, SourceRobot} {
		turn := Turn{
			Source:         source,
			ChatType:       "p2p",
			AgentID:        testAgentID(),
			WorkspaceID:    "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
			ConversationID: "cid-a",
			DWSOrgID:       "org-1",
		}
		c.prefetchSceneMemory(context.Background(), &turn)
		if turn.SceneMemory != "" {
			t.Fatalf("%s injected %q", source, turn.SceneMemory)
		}
	}
}

func splitCoordinatorPrompt(prompt string) (host, history, current string) {
	const currentMark = "\ncurrent_message:\n"
	const histMark = "\nrecent_dingtalk_history (newest first):\n"
	if i := strings.Index(prompt, currentMark); i >= 0 {
		current = prompt[i+len(currentMark):]
		prompt = prompt[:i]
	}
	if i := strings.Index(prompt, histMark); i >= 0 {
		history = prompt[i+len(histMark):]
		prompt = prompt[:i]
	}
	host = prompt
	return
}

func TestBuildUserPromptOmitsSceneMemoryWhenUnset(t *testing.T) {
	prompt := buildUserPrompt(Turn{
		Source:    SourceDigitalEmployee,
		Addressed: true,
		ChatType:  "p2p",
		Message:   "你好",
	})
	if strings.Contains(prompt, "scene_memory") {
		t.Fatalf("unset memory must not appear: %q", prompt)
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
	if !strings.Contains(prompt, "recent_dingtalk_history") || !strings.Contains(prompt, "昨天那个表") {
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
