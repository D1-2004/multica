package inboundcoord

import (
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/assoc"
)

func TestTaskFinishedAlreadyToldScene(t *testing.T) {
	t.Parallel()
	cid := "cid52dllVmkRJLpUZPwxi0jtw=="
	taskID := "87d0a35b-1e88-47a4-80e7-c63968aba9cb"
	events := []assoc.Event{
		{Direction: assoc.DirInbound, SceneKey: cid, TaskID: taskID},
		{Direction: assoc.DirOutbound, SceneKey: cid, TaskID: taskID},
	}
	if !TaskFinishedAlreadyToldScene(events, cid, taskID) {
		t.Fatal("same-scene outbound from this task must skip wrap-up")
	}
	// Production wrap-up passes agent_task_queue.id; assoc events store the
	// graph task node id. A mismatch must not keep wrap-up talking.
	if !TaskFinishedAlreadyToldScene(events, cid, "other-task") {
		t.Fatal("outbound on this cid during the run must skip wrap-up even when task ids differ")
	}
	dm := []assoc.Event{
		{Direction: assoc.DirOutbound, SceneKey: "cid-dxxh-dm", TaskID: taskID},
	}
	if TaskFinishedAlreadyToldScene(dm, cid, taskID) {
		t.Fatal("DM to someone else is not telling this group")
	}
	if TaskFinishedAlreadyToldScene(nil, cid, taskID) {
		t.Fatal("no events means wrap-up may still speak")
	}
}

func TestWrapupAlreadyToldSinceUsesIssueCreatedAt(t *testing.T) {
	t.Parallel()
	issueCreated := time.Date(2026, 9, 6, 4, 30, 0, 0, time.UTC)
	followUpCreated := time.Date(2026, 9, 6, 4, 35, 0, 0, time.UTC)
	got := WrapupAlreadyToldSince(followUpCreated, issueCreated)
	if !got.Equal(issueCreated) {
		t.Fatalf("since=%s want issue created_at so prior sandbox outbound is visible", got)
	}
	onlyTask := WrapupAlreadyToldSince(followUpCreated, time.Time{})
	if !onlyTask.Equal(followUpCreated) {
		t.Fatalf("no issue timestamp should keep task created_at, got %s", onlyTask)
	}
}

func TestTaskFinishedWrapupRedundant(t *testing.T) {
	t.Parallel()
	redundant := []string{
		"劳动合同法第三条的大白话解释已发到群里，你查收一下。",
		"@冬翔 W5B-SLOT-1102 没查到匹配机票，已私信你确认编号或补充航班细节。",
		"已私信向你确认周五下午三点开会。",
		"结果已发送，请看群。",
		"已问 dxxh 明天开会时间，等他回。",
		"已私信 dxxh 询问下周排期，等他回复。",
		"已确认线上开会，等待 dxxh。",
		"已补充告知线上开会时间。",
	}
	for _, text := range redundant {
		if !TaskFinishedWrapupRedundant(text) {
			t.Fatalf("want redundant: %q", text)
		}
	}
	keep := []string{
		"机票没查到，需要航班号或航司。",
		"会议室系统里没有这个编号，换一个？",
	}
	for _, text := range keep {
		if TaskFinishedWrapupRedundant(text) {
			t.Fatalf("must keep useful wrap-up: %q", text)
		}
	}
}

func TestFilterTaskFinishedWrapup(t *testing.T) {
	t.Parallel()
	got := FilterTaskFinishedWrapup(Decision{
		Action: ActionReply, UserText: "解释已发到群里，你查收一下。",
	})
	if got.Action != ActionSilence || got.UserText != "" {
		t.Fatalf("got %#v", got)
	}
	kept := FilterTaskFinishedWrapup(Decision{
		Action: ActionReply, UserText: "机票没查到，需要航班号或航司。",
	})
	if kept.Action != ActionReply || kept.UserText == "" {
		t.Fatalf("kept %#v", kept)
	}
	statusPing := FilterTaskFinishedWrapup(Decision{
		Action: ActionReply, UserText: "已问 dxxh 明天开会时间，等他回。",
	})
	if statusPing.Action != ActionSilence || statusPing.UserText != "" {
		t.Fatalf("status ping %#v", statusPing)
	}
	silent := FilterTaskFinishedWrapup(Decision{Action: ActionSilence})
	if silent.Action != ActionSilence {
		t.Fatalf("silence %#v", silent)
	}
}
