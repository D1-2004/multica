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
	agent              db.Agent
	agentErr           error
	contract           []byte
	contractErr        error
	inbound            bool
	persona            string
	replyTone          string
	skills             []db.ListEnabledAgentSkillCardMetadataRow
	skillsErr          error
	page               []db.ChatMessage
	listErr            error
	lastList           db.ListChatMessagesPageParams
	listCalls          int
	sceneFlags         db.AgentSceneMemoryFlags
	accountDisplayName string
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
	return s.agent, s.agentErr
}

func (s *coordQueriesStub) GetAgentCoordinatorContract(context.Context, pgtype.UUID) ([]byte, error) {
	return s.contract, s.contractErr
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

func (s *coordQueriesStub) GetAgentDingTalkIdentity(context.Context, db.GetAgentDingTalkIdentityParams) (db.AgentDingtalkIdentity, error) {
	return db.AgentDingtalkIdentity{AccountDisplayName: s.accountDisplayName}, nil
}

func (s *coordQueriesStub) ListEnabledAgentSkillCardMetadata(context.Context, pgtype.UUID) ([]db.ListEnabledAgentSkillCardMetadataRow, error) {
	if s.skillsErr != nil {
		return nil, s.skillsErr
	}
	return s.skills, nil
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

func TestSystemPromptHumanGroupFloodRules(t *testing.T) {
	turn := Turn{
		Source: SourceDigitalEmployee, ChatType: "group", Addressed: true,
		Utterances: []WindowUtterance{
			{Sender: "小明", Text: "哈哈哈"},
			{Sender: "小红", Text: "问一下 dxxh 周五三点有没有空"},
			{Sender: "小红", Text: "谢谢"},
		},
	}
	// F01/F02 retain response eligibility and speaker/context boundaries;
	// F05/F06 distinguish noise from an answer; F10/F17 retain all real work;
	// F14/F15 own the colleague voice. This checks disclosure, not model intent.
	assertDisclosedObligations(t, turn, false, map[string][]string{
		"group":   {"COORD.F01", "COORD.F02", "COORD.F05", "COORD.F06", "COORD.F14"},
		"window":  {"COORD.F01", "COORD.F02", "COORD.F10", "COORD.F17"},
		"voice":   {"COORD.F14", "COORD.F15"},
		"inbound": {"COORD.F04", "COORD.F05", "COORD.F06", "COORD.F09"},
	}, []string{"completion", "recall_match"})
	prompt := buildUserPrompt(turn)
	for _, utterance := range turn.Utterances {
		if strings.Count(prompt, utterance.Text) != 1 {
			t.Errorf("flood/thanks must not strip or duplicate current words %q", utterance.Text)
		}
	}
	turn.ChatType = "p2p"
	assertDisclosedObligations(t, turn, false, map[string][]string{
		"channel": {"COORD.F01", "COORD.F14"},
		"window":  {"COORD.F10", "COORD.F17"},
	}, []string{"group", "completion"})
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

func TestDecideTaskFinishedAlreadyToldSceneWithoutLLM(t *testing.T) {
	c := &Coordinator{LLM: llm.New(llm.Config{APIKey: "k", BaseURL: "http://127.0.0.1:1"})}
	got := c.Decide(context.Background(), Turn{
		Loop:             LoopTaskFinished,
		Source:           SourceDigitalEmployee,
		Addressed:        true,
		ChatType:         "group",
		Message:          "任务已完成，请向委托人汇报。",
		AlreadyToldScene: true,
	})
	if got.Action != ActionSilence || got.Reason != "already_told_scene" {
		t.Fatalf("got %#v", got)
	}
}

func TestDecideDefersWhenLLMDisabled(t *testing.T) {
	c := &Coordinator{LLM: llm.New(llm.Config{})}
	got := c.Decide(context.Background(), Turn{Source: SourceWeb, Addressed: true, Message: "你好"})
	if got.Action != ActionDeferred || got.Reason != "coordinator_model_unavailable" {
		t.Fatalf("missing model must preserve unresolved input without starting sandbox work: %#v", got)
	}
	if got.UserText != "" || got.IssueID != "" || len(got.Items) != 0 || got.IssueComment != nil {
		t.Fatalf("missing model must not invent a response or work effect: %#v", got)
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
	for _, want := range []string{"本轮拟向用户说明", "不是完成或送达证据", "帮我看截止时间", "当前可信钉钉派发事件里的发信人", "Issue 创建人或评论人只表示谁执行了 Issue 工具", "协助者", "数字员工事件", "机器人事件", "消息接收人", "当前能解除阻塞", "不要固定回复委托人", "必须实际给一个明确的人发送", "不得写“任务完成”"} {
		if !strings.Contains(desc, want) {
			t.Fatalf("description missing %q: %q", want, desc)
		}
	}
	if strings.Contains(desc, "前台已对用户说") {
		t.Fatal("planned reception speech must not become delivery evidence for the sandbox")
	}
	two := Decision{
		Action:      ActionIssue,
		PlanVersion: WindowPlanVersion,
		Purpose:     "向同事确认周五三点是否方便开会",
		UserText:    "我去问",
		Items:       []WindowItem{{Delegator: "测试号", LookInto: "周五三点"}, {Delegator: "dxxh", LookInto: "今日token"}},
	}
	onlyFirst := two
	onlyFirst.Items = []WindowItem{two.Items[0]}
	got := IssueDescription(onlyFirst, "窗口")
	if !strings.Contains(got, "委托人=测试号") || strings.Contains(got, "委托人=dxxh") {
		t.Fatalf("item body must not list the sibling: %q", got)
	}
	if !strings.HasPrefix(got, "本次子任务只执行这一个交付物："+onlyFirst.Purpose) || !strings.Contains(got, "其它工作由各自任务处理，不要重复执行") {
		t.Fatalf("shared source wording must not expand a child task beyond its own deliverable: %q", got)
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

func TestFillVoiceCopiesSkillSnapshots(t *testing.T) {
	t.Parallel()
	c := &Coordinator{Queries: &coordQueriesStub{skills: []db.ListEnabledAgentSkillCardMetadataRow{
		{Name: "dingtalk-minutes", Description: "查询听记并整理行动项"},
		{Name: "  ", Description: "ignored"},
		{Name: "dingtalk-calendar", Description: "约会议、查日程"},
	}}}
	turn := Turn{AgentID: testAgentID()}
	c.FillVoice(context.Background(), &turn)
	if len(turn.Skills) != 2 {
		t.Fatalf("skills=%v", turn.Skills)
	}
	if turn.Skills[0].Name != "dingtalk-minutes" || turn.Skills[1].Name != "dingtalk-calendar" {
		t.Fatalf("skills=%v", turn.Skills)
	}
}

func TestFillSkillsDoesNotOverwriteOnError(t *testing.T) {
	t.Parallel()
	c := &Coordinator{Queries: &coordQueriesStub{skillsErr: context.Canceled}}
	turn := Turn{AgentID: testAgentID(), Skills: []SkillSnapshot{{Name: "stale"}}}
	c.FillSkills(context.Background(), &turn)
	if len(turn.Skills) != 1 || turn.Skills[0].Name != "stale" {
		t.Fatalf("failed load must not wipe caller-provided skills: %v", turn.Skills)
	}
}

func TestFillVoiceKeepsPersonaWhenSkillsFail(t *testing.T) {
	t.Parallel()
	c := &Coordinator{Queries: &coordQueriesStub{
		persona: "靠谱同事", replyTone: "短句", skillsErr: context.Canceled,
	}}
	turn := Turn{AgentID: testAgentID()}
	c.FillVoice(context.Background(), &turn)
	if turn.Persona != "靠谱同事" || turn.ReplyTone != "短句" {
		t.Fatalf("voice=%q / %q", turn.Persona, turn.ReplyTone)
	}
	if len(turn.Skills) != 0 {
		t.Fatalf("skills=%v", turn.Skills)
	}
}

func TestTurnFromChatSessionLoadsVoice(t *testing.T) {
	q := &coordQueriesStub{
		persona: "靠谱同事", replyTone: "短句、不客套",
		skills: []db.ListEnabledAgentSkillCardMetadataRow{
			{Name: "dingtalk-minutes", Description: "查询听记并整理行动项"},
		},
	}
	c := &Coordinator{Queries: q}
	turn := c.TurnFromChatSession(context.Background(), testSession(), SourceRobot, true, "p2p", "", "", "你好")
	if turn.Persona != "靠谱同事" || turn.ReplyTone != "短句、不客套" {
		t.Fatalf("voice=%q / %q", turn.Persona, turn.ReplyTone)
	}
	if len(turn.Skills) != 1 || turn.Skills[0].Name != "dingtalk-minutes" {
		t.Fatalf("skills=%v", turn.Skills)
	}
	prompt := buildUserPrompt(turn)
	if !strings.Contains(prompt, "agent_persona: 靠谱同事") || !strings.Contains(prompt, "agent_reply_tone: 短句、不客套") {
		t.Fatalf("prompt=%q", prompt)
	}
	if !strings.Contains(prompt, "agent_skills:\n- dingtalk-minutes: 查询听记并整理行动项") {
		t.Fatalf("prompt missing skill snapshots:\n%s", prompt)
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
		if strings.Contains(prompt, "session_title") || strings.Contains(prompt, stale) || strings.Contains(prompt, "\nconversation: ") {
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
	if !strings.Contains(web, "session_title (label only): 网页会话标题") {
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
		if strings.Contains(prompt, stale) || strings.Contains(prompt, "session_title") {
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
	turn := Turn{
		Source: SourceDigitalEmployee, Addressed: true, ChatType: "p2p",
		Message:             "GoalMate 是什么",
		SceneMemory:         "## 稳定知识与约定\n- GoalMate 是工具，不是数字员工",
		SceneMemoryRevision: 4,
	}
	prompt := buildUserPrompt(turn)
	for _, field := range []string{
		"scene_memory_status: loaded; scope=this_conversation",
		"scene_memory_revision: 4",
		"scene_memory (Host-provided, this Scene only; never a source of issue_id):",
		turn.SceneMemory,
	} {
		if !strings.Contains(prompt, field) {
			t.Errorf("missing committed-scene source contract %q", field)
		}
	}
	host, history, current := splitCoordinatorPrompt(prompt)
	if !strings.Contains(host, turn.SceneMemory) || strings.Contains(history, turn.SceneMemory) || strings.Contains(current, turn.SceneMemory) {
		t.Fatal("stable memory must remain separate from dialogue evidence and current input")
	}
	// F03/F11 preserve memory inventory, correction and retraction obligations.
	// Missing payload and job briefs remain inbound; capacity waits cannot silence
	// communication (F09). Older-work matching is disclosed only after recall.
	assertDisclosedObligations(t, turn, false, map[string][]string{
		"core":    {"COORD.F03", "COORD.F11", "COORD.F13", "COORD.F17"},
		"memory":  {"COORD.F03", "COORD.F11"},
		"inbound": {"COORD.F04", "COORD.F06", "COORD.F07", "COORD.F09", "COORD.F13", "COORD.F17"},
	}, []string{"recall_match", "completion"})
	assertDisclosedObligations(t, turn, true, map[string][]string{
		"memory":       {"COORD.F03", "COORD.F11"},
		"recall_match": {"COORD.F02", "COORD.F03", "COORD.F07", "COORD.F08", "COORD.F17"},
	}, []string{"completion"})
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

func TestPrefetchSceneMemoryDropsDigitalEmployeeCitesOnGroup(t *testing.T) {
	mem := &sceneMemoryStub{rows: map[string]db.SceneMemory{
		"cid-a": {SceneKey: "cid-a", MemoryText: strings.Join([]string{
			"## 场域定位",
			"VOC群",
			"成员：璟琦、金龙",
			"用途：客户声音",
			"## 稳定知识与约定",
			"- 金龙擅长方向：VOC (来自金龙, 9月6日 16:45的发言)",
			"- 随风统一处理大模型技术问题 (来自璟琦, 9月7日 14:13的发言)",
			"## 纠正信号",
			"## 待确认",
		}, "\n"), MemoryRevision: 7},
	}}
	c := &Coordinator{
		Queries:     &coordQueriesStub{sceneFlags: db.AgentSceneMemoryFlags{RecallEnabled: true}, accountDisplayName: "金龙"},
		SceneMemory: mem,
	}
	turn := Turn{
		Source:         SourceDigitalEmployee,
		ChatType:       "group",
		AgentID:        testAgentID(),
		AgentName:      "VOC数字员工突击队",
		WorkspaceID:    "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa",
		ConversationID: "cid-a",
		DWSOrgID:       "org-1",
	}
	c.prefetchSceneMemory(context.Background(), &turn)
	if strings.Contains(turn.SceneMemory, "来自金龙") || strings.Contains(turn.SceneMemory, "金龙擅长方向") {
		t.Fatalf("host inject leaked digital-employee speech: %q", turn.SceneMemory)
	}
	if !strings.Contains(turn.SceneMemory, "随风统一处理大模型技术问题") {
		t.Fatalf("human fact stripped: %q", turn.SceneMemory)
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

func TestBuildUserPromptMarksSceneMemoryNotLoadedWhenUnset(t *testing.T) {
	turn := Turn{Source: SourceDigitalEmployee, Addressed: true, ChatType: "p2p", Message: "你好"}
	prompt := buildUserPrompt(turn)
	if !strings.Contains(prompt, "scene_memory_status: not_loaded; scope=this_conversation") {
		t.Fatalf("unset memory must explicitly retain the missing-state distinction: %q", prompt)
	}
	if !strings.Contains(prompt, "scene_memory_revision: 0") || strings.Contains(prompt, "scene_memory (Host-provided") || strings.Contains(prompt, "(empty)") {
		t.Fatalf("an absent snapshot must not be presented as a known-empty memory: %q", prompt)
	}
	assertDisclosedObligations(t, turn, false, map[string][]string{"core": {"COORD.F03"}}, []string{"memory"})
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
	got, err := parseValidatedWindowPlan(`{"actions":[{"kind":"start_work","source_refs":["u1"],"reply":"我来查今天的新闻。","purpose":"查询并整理今天的重要新闻摘要","intent":"lookup"}]}`, Turn{Source: SourceRobot, Message: "帮我看看今天有什么新闻"}, nil, nil)
	if err != nil || got.Action != ActionIssue {
		t.Fatalf("news must stay issue, got %s", got.Action)
	}
}
