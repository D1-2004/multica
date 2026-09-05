package inboundcoord

import (
	"testing"

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

func TestTaskFinishedWrapupRedundant(t *testing.T) {
	t.Parallel()
	redundant := []string{
		"劳动合同法第三条的大白话解释已发到群里，你查收一下。",
		"@冬翔 W5B-SLOT-1102 没查到匹配机票，已私信你确认编号或补充航班细节。",
		"已私信向你确认周五下午三点开会。",
		"结果已发送，请看群。",
	}
	for _, text := range redundant {
		if !TaskFinishedWrapupRedundant(text) {
			t.Fatalf("want redundant: %q", text)
		}
	}
	keep := []string{
		"我问了 dxxh 周五三点，等他回。",
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
		Action: ActionReply, UserText: "我问了 dxxh，等他回。",
	})
	if kept.Action != ActionReply || kept.UserText == "" {
		t.Fatalf("kept %#v", kept)
	}
	silent := FilterTaskFinishedWrapup(Decision{Action: ActionSilence})
	if silent.Action != ActionSilence {
		t.Fatalf("silence %#v", silent)
	}
}
