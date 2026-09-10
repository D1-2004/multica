package inboundcoord

import (
	"context"
	"strings"
	"testing"
)

func TestIssueDescriptionUsesSourceRetainedByHostAndWindowItem(t *testing.T) {
	for _, tc := range []struct {
		source Source
		want   string
		absent []string
	}{
		{SourceWeb, "通过当前任务结果或已有会话回传渠道返回结果", []string{"本任务来自钉钉", "本任务来源未确认", "必须实际给一个明确的人发送"}},
		{SourceDigitalEmployee, "本任务来自钉钉，委托人采用当前可信派发事件里的发信人", []string{"本任务来自 Web", "本任务来源未确认", "必须实际给一个明确的人发送"}},
		{SourceRobot, "缺少用户标识时不虚构身份或改用 Issue 署名", []string{"本任务来自 Web", "用户身份完整", "必须实际给一个明确的人发送"}},
		{"", "本任务来源未确认", []string{"本任务来自 Web", "本任务来自钉钉", "委托人采用当前可信派发事件里的发信人"}},
		{"future_surface", "本任务来源未确认", []string{"本任务来自 Web", "本任务来自钉钉", "委托人采用当前可信派发事件里的发信人"}},
	} {
		t.Run(string(tc.source), func(t *testing.T) {
			item := WindowItem{Delegator: "用户", Purpose: "执行明确指定的命令并返回输出", LookInto: "执行明确指定的命令并返回输出", Content: "打印 git status"}
			saved := Decision{Action: ActionIssue, PlanVersion: WindowPlanVersion, Items: []WindowItem{item}}
			// Old checkpoints can omit Source; Decide restores it from the trusted
			// inbound Turn and ForWindowItem must retain it for both real callers.
			ctx := ContextWithPlanCheckpoint(context.Background(), &saved, nil)
			decision := (&Coordinator{}).Decide(ctx, Turn{Source: tc.source, Message: item.Content})
			selected := decision.ForWindowItem(item)
			if selected.Source != tc.source {
				t.Fatalf("source lost before issue body: got %q want %q", selected.Source, tc.source)
			}
			body := IssueDescription(selected, item.Content)
			if !strings.Contains(body, tc.want) {
				t.Fatalf("source-specific handoff missing %q: %s", tc.want, body)
			}
			for _, unwanted := range tc.absent {
				if strings.Contains(body, unwanted) {
					t.Fatalf("invented source/delivery %q: %s", unwanted, body)
				}
			}
		})
	}
}

func TestIssueDescriptionKeepsOneGoalAndNecessaryContext(t *testing.T) {
	for _, command := range []string{"dws auth status", "git status", "jq --version"} {
		t.Run(command, func(t *testing.T) {
			goal := "执行用户指定的只读命令并返回标准输出"
			item := WindowItem{Delegator: "当前用户", Purpose: goal, LookInto: goal + "\nscene_cid=cid-current", Content: "只执行 `" + command + "`；失败时返回原始报错。"}
			decision := (Decision{Source: SourceWeb, PlanVersion: WindowPlanVersion, UserText: "我来执行并返回输出。"}).ForWindowItem(item)
			body := IssueDescription(decision, item.Content)
			if strings.Count(body, goal) != 1 || strings.Count(body, "scene_cid=cid-current") != 1 || strings.Count(body, item.Content) != 1 {
				t.Fatalf("repeated goal/scope or changed original command: %s", body)
			}
			for _, want := range []string{"委托人=当前用户", "不是完成或送达证据", "最新约束和原始请求", "不自行寻找或修改凭证", "未经授权的登录或环境维修"} {
				if !strings.Contains(body, want) {
					t.Fatalf("lost bounded execution requirement %q: %s", want, body)
				}
			}
		})
	}
	goal := "核对指定审批导出规则"
	context := "只读官方材料；不要执行审批。\nscene_cid=cid-current"
	body := IssueDescription(Decision{Source: SourceWeb, PlanVersion: WindowPlanVersion, Purpose: goal, LookInto: context}, "请核对这个问题。")
	if !strings.HasPrefix(body, "本次子任务只执行这一个交付物："+goal) || !strings.Contains(body, "执行上下文："+context) {
		t.Fatalf("independent context replaced the deliverable or was dropped: %s", body)
	}
}

func TestIssueDescriptionPreservesExplicitDeliveryAndOriginalHandoff(t *testing.T) {
	turn := historyHandoffTurn(t)
	history, err := continuationHistoryHandoff(turn)
	if err != nil {
		t.Fatal(err)
	}
	message := "当前用户：将已审核通知发给本群的负责人，不发到其他群。\nquoted_context: [图片消息](mediaId=original-media)\n" + history
	for _, source := range []Source{SourceWeb, SourceDigitalEmployee, SourceRobot, ""} {
		t.Run(string(source), func(t *testing.T) {
			item := WindowItem{Delegator: "当前用户", Purpose: "将已审核通知发送给明确指定的负责人", LookInto: "只发送已审核版本，不改通知内容。", Content: message}
			body := IssueDescription((Decision{Source: source, PlanVersion: WindowPlanVersion}).ForWindowItem(item), item.Content)
			for _, want := range []string{message, item.Purpose, item.LookInto, handoffScope, "original-author", "scope-confirmation", "original-media", "当前请求明确授权外发、代问或转达时，仍按原文指定的对象、渠道和范围执行", "未确认发送成功不得声称已送达", "禁止打开或引用其它群的内容"} {
				if !strings.Contains(body, want) {
					t.Fatalf("delivery/source/constraint evidence lost %q: %s", want, body)
				}
			}
		})
	}
}
