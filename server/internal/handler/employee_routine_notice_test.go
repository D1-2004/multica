package handler

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

// Result-only delivery must not turn empty output or failure into success.
func TestEmployeeRoutineResultDelivery(t *testing.T) {
	encode := func(output string) []byte {
		raw, err := json.Marshal(protocol.TaskCompletedPayload{Output: output})
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	for _, tc := range []struct {
		name, status  string
		result        []byte
		failure, want string
	}{
		{"business text", "completed", encode("部署结果：全部健康。"), "", "部署结果：全部健康。"},
		{"empty", "completed", encode(" \n"), "", "「部署检查」未返回可交付的结果，请检查运行记录。"},
		{"invalid result", "completed", []byte(`invalid`), "", "「部署检查」未返回可交付的结果，请检查运行记录。"},
		{"failure", "failed", nil, "授权已撤销", "「部署检查」未能完成：授权已撤销"},
		{"cancelled", "cancelled", nil, "", "「部署检查」已取消。"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := employeeRoutineResultText("部署检查", tc.status, tc.result, tc.failure); got != tc.want {
				t.Fatalf("got %q want %q", got, tc.want)
			}
		})
	}
	long := employeeRoutineResultText("部署检查", "completed", encode(strings.Repeat("好", sceneRoutineNoticeMax+10)), "")
	if !strings.HasPrefix(long, strings.Repeat("好", sceneRoutineNoticeMax)+"\n") || !strings.Contains(long, "完整结果见") {
		t.Fatal("long output did not preserve the clipped result and history link")
	}
}
