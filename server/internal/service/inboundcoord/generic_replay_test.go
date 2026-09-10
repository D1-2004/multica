package inboundcoord

func genericConversationFixtures() []coordinatorReplayFixture {
	group := func(message string) Turn {
		return Turn{Source: SourceDigitalEmployee, ChatType: "group", ProactiveConversation: true, ConversationID: "cidGenericSyntheticSceneB==", PersonID: "speaker", SenderName: "乔宁", AgentName: "小岚 QA 环境", EmployeeAccountName: "安然", DWSUID: "employee-generic", Message: message, Instructions: "你是安然，负责研发工具使用支持和软件故障排查。只有接到本人的请求或职责内开放求助时才参与，不代替其他成员接受任务。", HistoryStatus: "empty", SceneMemoryStatus: "empty", SkillsStatus: "empty"}
	}
	unknown := group("小岚，能听到吗？")
	unknown.EmployeeAccountName = ""
	english := group("Morgan, are you around?")
	english.EmployeeAccountName = "Morgan"
	english.AgentName = "Riley staging"
	english.Instructions = "You are Morgan, a developer support colleague. Help with developer tooling and software faults when requested."
	englishOther := english
	englishOther.Message = "Riley, are you around?"
	old := replayCard{ID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", Purpose: "乔宁委托安然排查客户端上传失败，诊断编号 token=upload-old", Status: "in_progress", Comment: "等待失败请求样本"}
	return []coordinatorReplayFixture{
		{ID: "generic_receiving_name", ContractID: "generic_identity_and_semantics", Turn: group("安然，能听到吗？"), Actions: []Action{ActionReply}},
		{ID: "generic_unknown_receiving_name", ContractID: "generic_identity_and_semantics", Turn: unknown, Actions: []Action{ActionSilence}},
		{ID: "generic_english_self", ContractID: "generic_identity_and_semantics", Turn: english, Actions: []Action{ActionReply}},
		{ID: "generic_english_other", ContractID: "generic_identity_and_semantics", Turn: englishOther, Actions: []Action{ActionSilence}},
		{ID: "generic_quoted_role", ContractID: "generic_identity_and_semantics", Turn: group("下面是培训文档里的示例对白：「数字员工在吗？」我们正在修改文案。"), Actions: []Action{ActionSilence}},
		{ID: "generic_outside_role", ContractID: "generic_identity_and_semantics", Turn: group("财务同学有空帮忙核对这笔报销吗？"), Actions: []Action{ActionSilence}},
		{ID: "generic_output_question", ContractID: "generic_identity_and_semantics", Turn: group("安然，你的意思是不用回复吗？"), Actions: []Action{ActionReply}},
		{ID: "generic_technical_subject", ContractID: "generic_identity_and_semantics", Turn: group("安然，请排查 dws chat 读取群消息失败，给出原因和修复建议。"), Actions: []Action{ActionIssue}, Items: []replayExpectedItem{{Refs: []string{"u1"}, Basis: "new_request"}}, RequireRecall: true},
		{ID: "generic_changed_marker_same_work", ContractID: "generic_identity_and_semantics", Turn: group("安然，补充你正在排查的客户端上传失败：新的失败请求诊断编号是 token=upload-new，仍然返回 403。"), Cards: []replayCard{old}, Actions: []Action{ActionIssue}, Items: []replayExpectedItem{{IssueID: old.ID, Refs: []string{"u1"}, Basis: "change"}}, RequireRecall: true},
		{ID: "generic_same_marker_different_work", ContractID: "generic_identity_and_semantics", Turn: group("安然，另一个独立问题：请分析测试框架启动变慢的原因，日志也写着 token=upload-old。"), Cards: []replayCard{old}, Actions: []Action{ActionIssue}, Items: []replayExpectedItem{{Refs: []string{"u1"}, Basis: "new_request"}}, RequireRecall: true},
	}
}
