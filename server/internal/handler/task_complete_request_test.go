package handler

import (
	"encoding/json"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestTaskCompleteRequestPreservesResultMessageInProtocolPayload(t *testing.T) {
	request := TaskCompleteRequest{
		Output:        "agent execution summary",
		ResultMessage: "最终回复正文",
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	var payload protocol.TaskCompletedPayload
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Output != "agent execution summary" {
		t.Fatalf("output = %q", payload.Output)
	}
	if payload.ResultMessage != "最终回复正文" {
		t.Fatalf("result message = %q", payload.ResultMessage)
	}
}

func TestTaskFailRequestPreservesResultMessage(t *testing.T) {
	request := TaskFailRequest{
		Error:         "runtime timed out",
		ResultMessage: "已向用户说明任务超时",
	}
	data, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}

	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatal(err)
	}
	if payload["result_message"] != "已向用户说明任务超时" {
		t.Fatalf("result_message = %#v", payload["result_message"])
	}
}
