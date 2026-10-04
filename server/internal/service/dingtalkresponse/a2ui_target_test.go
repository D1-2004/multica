package dingtalkresponse

import "testing"

// The same business scene has observer-relative direct chat ids. Only groups
// use the conversation target; a DM must address the current source speaker.
func TestEmployeeA2UIQuestionTargetsGroupOrTrustedDMRecipient(t *testing.T) {
	in := ActionInput{A2UICard: &A2UIQuestionCard{QuestionID: "question"}, ConversationID: "cid-observer", SenderOpenDingTalkID: "source-employee-view", Text: "question"}
	dm := employeeQuestionSendTarget(in, "stable-action")
	if dm.ConversationID != "" || dm.ReceiverOpenDingTalkID != in.SenderOpenDingTalkID || dm.BizID != "question" || dm.RequestID != "stable-action" {
		t.Fatal("DM used a group/observer-relative target", dm)
	}
	in.IsGroup = true
	group := employeeQuestionSendTarget(in, "stable-action")
	if group.ConversationID != in.ConversationID || group.ReceiverOpenDingTalkID != "" {
		t.Fatal("group used a recipient target", group)
	}
}
