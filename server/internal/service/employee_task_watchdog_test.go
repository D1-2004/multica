package service

import (
	"strings"
	"testing"
	"time"

	"github.com/multica-ai/multica/server/internal/employeetask"
)

func TestEmployeeWatchdogStatePrecedence(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	ttl := time.Hour
	running := func(started time.Duration) *EmployeeWatchdogExecution {
		return &EmployeeWatchdogExecution{RunID: "run", RunState: employeetask.StateRunning, RunCreatedAt: now.Add(-started - time.Minute), QueueTaskID: "queue", QueueStatus: "running", QueueStartedAt: now.Add(-started)}
	}
	cases := []struct {
		name   string
		facts  EmployeeWatchdogFacts
		kind   EmployeeWatchdogStateKind
		reason string
		quiet  time.Time
	}{
		{
			name:  "stop pending exit is stopping, not an execution stall",
			facts: EmployeeWatchdogFacts{TaskState: employeetask.StateCancelled, Execution: running(30 * time.Minute), Stop: &EmployeeWatchdogStop{Seq: 4, At: now.Add(-20 * time.Minute)}},
			kind:  EmployeeWatchdogStopping, quiet: now.Add(-20 * time.Minute),
		},
		{
			name:  "confirmed exit has nothing to watch",
			facts: EmployeeWatchdogFacts{TaskState: employeetask.StateCancelled, Execution: running(30 * time.Minute), Stop: &EmployeeWatchdogStop{Seq: 4, At: now.Add(-20 * time.Minute), ExitConfirmed: true}},
			kind:  EmployeeWatchdogNone,
		},
		{
			name:  "succeeded goal",
			facts: EmployeeWatchdogFacts{TaskState: employeetask.StateSucceeded},
			kind:  EmployeeWatchdogNone,
		},
		{
			name:  "input wait ranks above a running execution",
			facts: EmployeeWatchdogFacts{TaskState: "waiting", ActiveRunID: "run", Execution: running(time.Hour), Wait: &EmployeeTaskWait{Kind: EmployeeTaskWaitInputs, Key: "collection:1", Since: now.Add(-50 * time.Minute), Expected: 3, Received: 2}},
			kind:  EmployeeWatchdogWaitingInputs, quiet: now.Add(-50 * time.Minute),
		},
		{
			name:  "scheduled wait is quiet until it is due",
			facts: EmployeeWatchdogFacts{TaskState: "waiting", Wait: &EmployeeTaskWait{Kind: EmployeeTaskWaitSchedule, Key: "schedule:1", Since: now.Add(-3 * time.Hour), DueAt: now.Add(-5 * time.Minute)}},
			kind:  EmployeeWatchdogScheduledWait, quiet: now.Add(-5 * time.Minute),
		},
		{
			name:   "declared wait without readable facts is not aged",
			facts:  EmployeeWatchdogFacts{TaskState: "waiting"},
			kind:   EmployeeWatchdogNone,
			reason: "wait_facts_unavailable",
		},
		{
			name:  "unclaimed execution is queued",
			facts: EmployeeWatchdogFacts{TaskState: employeetask.StateRunning, ActiveRunID: "run", Execution: &EmployeeWatchdogExecution{RunID: "run", RunState: employeetask.StateRunning, RunCreatedAt: now.Add(-12 * time.Minute), QueueTaskID: "queue", QueueStatus: "queued"}},
			kind:  EmployeeWatchdogExecutionQueued, quiet: now.Add(-12 * time.Minute),
		},
		{
			name:   "unclaimed execution on an offline runtime is unreachable",
			facts:  EmployeeWatchdogFacts{TaskState: employeetask.StateRunning, ActiveRunID: "run", Execution: &EmployeeWatchdogExecution{RunID: "run", RunState: employeetask.StateRunning, RunCreatedAt: now.Add(-12 * time.Minute), QueueTaskID: "queue", QueueStatus: "dispatched", RuntimeOffline: true, RuntimeChangedAt: now.Add(-8 * time.Minute)}},
			kind:   EmployeeWatchdogExecutionUnreachable,
			reason: "runtime_offline", quiet: now.Add(-8 * time.Minute),
		},
		{
			name:   "cloud claim past the credential lifetime is unreachable",
			facts:  EmployeeWatchdogFacts{TaskState: employeetask.StateRunning, ActiveRunID: "run", Execution: func() *EmployeeWatchdogExecution { e := running(61 * time.Minute); e.CredentialBound = true; return e }()},
			kind:   EmployeeWatchdogExecutionUnreachable,
			reason: "credential_expired", quiet: now.Add(-61 * time.Minute).Add(ttl),
		},
		{
			name: "progress after the credential lifetime proves the execution still reports",
			facts: EmployeeWatchdogFacts{TaskState: employeetask.StateRunning, ActiveRunID: "run", Execution: func() *EmployeeWatchdogExecution { e := running(61 * time.Minute); e.CredentialBound = true; return e }(),
				Progress: EmployeeActivityWatermark{At: now.Add(-30 * time.Second), Source: EmployeeActivityTaskMessage, Ref: "m"}},
			kind: EmployeeWatchdogExecutionRunning, quiet: now.Add(-61 * time.Minute),
		},
		{
			name:  "local claim within reach is running",
			facts: EmployeeWatchdogFacts{TaskState: employeetask.StateRunning, ActiveRunID: "run", Execution: running(2 * time.Hour)},
			kind:  EmployeeWatchdogExecutionRunning, quiet: now.Add(-2 * time.Hour),
		},
		{
			name:  "deferred queue waits for its fire time",
			facts: EmployeeWatchdogFacts{TaskState: employeetask.StateRunning, ActiveRunID: "run", Execution: &EmployeeWatchdogExecution{RunID: "run", RunState: employeetask.StateRunning, RunCreatedAt: now.Add(-time.Hour), QueueTaskID: "queue", QueueStatus: "deferred", QueueFireAt: now.Add(10 * time.Minute)}},
			kind:  EmployeeWatchdogScheduledWait, quiet: now.Add(10 * time.Minute),
		},
		{
			name:   "terminal queue awaiting run reconciliation",
			facts:  EmployeeWatchdogFacts{TaskState: employeetask.StateRunning, ActiveRunID: "run", Execution: &EmployeeWatchdogExecution{RunID: "run", RunState: employeetask.StateRunning, QueueTaskID: "queue", QueueStatus: "completed"}},
			kind:   EmployeeWatchdogNone,
			reason: "execution_terminal",
		},
		{
			name:   "running goal without an active run",
			facts:  EmployeeWatchdogFacts{TaskState: employeetask.StateRunning},
			kind:   EmployeeWatchdogNone,
			reason: "no_active_execution",
		},
		{
			name:   "unknown task state",
			facts:  EmployeeWatchdogFacts{TaskState: "paused"},
			kind:   EmployeeWatchdogNone,
			reason: "unknown_task_state",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ClassifyEmployeeWatchdogState(c.facts, now, ttl)
			if got.Kind != c.kind {
				t.Fatalf("kind=%q reason=%q, want %q", got.Kind, got.Reason, c.kind)
			}
			if c.reason != "" && got.Reason != c.reason {
				t.Fatalf("reason=%q, want %q", got.Reason, c.reason)
			}
			if c.kind != EmployeeWatchdogNone && (!got.QuietFrom.Equal(c.quiet) || got.Boundary == "" || got.StartedAt.IsZero()) {
				t.Fatalf("quiet=%s boundary=%q started=%s, want quiet %s", got.QuietFrom, got.Boundary, got.StartedAt, c.quiet)
			}
		})
	}
}

func TestEmployeeWatchdogNoticeTextIsHonest(t *testing.T) {
	goal := "整理本周客户反馈并生成一份包含所有渠道数据的汇总表格，按优先级排序后发给我"
	forbidden := []string{"%", "％", "卡住", "卡死", "预计", "ETA", "马上", "即将", "进度"}
	cases := []struct {
		state EmployeeWatchdogState
		must  []string
		never []string
	}{
		{EmployeeWatchdogState{Kind: EmployeeWatchdogExecutionRunning}, []string{"还在执行", "17 分钟", "没有新的可观测输出"}, nil},
		{EmployeeWatchdogState{Kind: EmployeeWatchdogExecutionQueued}, []string{"排队", "尚未开始"}, []string{"还在执行"}},
		{EmployeeWatchdogState{Kind: EmployeeWatchdogExecutionUnreachable, Reason: "credential_expired"}, []string{"无法确认", "不是工作内容本身的问题"}, []string{"还在执行", "仍在运行"}},
		{EmployeeWatchdogState{Kind: EmployeeWatchdogExecutionUnreachable, Reason: "runtime_offline"}, []string{"离线", "无法确认"}, []string{"还在执行", "仍在运行"}},
		{EmployeeWatchdogState{Kind: EmployeeWatchdogWaitingInputs, Expected: 3, Received: 2}, []string{"2/3"}, []string{"还在执行"}},
		{EmployeeWatchdogState{Kind: EmployeeWatchdogScheduledWait}, []string{"预定"}, []string{"还在执行"}},
		{EmployeeWatchdogState{Kind: EmployeeWatchdogStopping}, []string{"停止", "尚未确认退出"}, []string{"还在执行", "已停止"}},
	}
	for _, c := range cases {
		t.Run(string(c.state.Kind)+c.state.Reason, func(t *testing.T) {
			text := ComposeEmployeeWatchdogNotice(c.state, goal, 17*time.Minute+40*time.Second)
			if text == "" || len([]rune(text)) > 160 {
				t.Fatalf("notice length %d: %q", len([]rune(text)), text)
			}
			if !strings.Contains(text, "「整理本周客户反馈并生成一份包含所有渠道数据的汇总表格…」") {
				t.Fatalf("goal was not quoted and shortened: %q", text)
			}
			for _, s := range append(append([]string{}, c.never...), forbidden...) {
				if strings.Contains(text, s) {
					t.Fatalf("notice %q contains %q", text, s)
				}
			}
			for _, s := range c.must {
				if !strings.Contains(text, s) {
					t.Fatalf("notice %q lacks %q", text, s)
				}
			}
		})
	}
	if ComposeEmployeeWatchdogNotice(EmployeeWatchdogState{Kind: EmployeeWatchdogNone}, goal, time.Hour) != "" {
		t.Fatal("no state must not produce a notice")
	}
	if got := ComposeEmployeeWatchdogNotice(EmployeeWatchdogState{Kind: EmployeeWatchdogExecutionRunning}, "  \n ", 30*time.Second); !strings.Contains(got, "这项工作") || !strings.Contains(got, "1 分钟") {
		t.Fatalf("empty goal or sub-minute silence rendered badly: %q", got)
	}
}

func TestEmployeeWatchdogConfigResolvesAgentOverrides(t *testing.T) {
	cfg := DefaultEmployeeWatchdogConfig()
	cfg.Agents = map[string]EmployeeWatchdogThresholds{"33af235e-e03b-4be2-be3b-bbae8b97fce5": {RunningSilenceSeconds: 120, StoppingUnconfirmedSeconds: -1}}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := cfg.Threshold("33af235e-e03b-4be2-be3b-bbae8b97fce5", EmployeeWatchdogExecutionRunning); got != 2*time.Minute {
		t.Fatal("agent override ignored", got)
	}
	if got := cfg.Threshold("33af235e-e03b-4be2-be3b-bbae8b97fce5", EmployeeWatchdogStopping); got != 0 {
		t.Fatal("negative override must disable the kind", got)
	}
	if got := cfg.Threshold("33af235e-e03b-4be2-be3b-bbae8b97fce5", EmployeeWatchdogWaitingInputs); got != cfg.Threshold("other", EmployeeWatchdogWaitingInputs) || got <= 0 {
		t.Fatal("unset override must fall back to the default", got)
	}
	if got := cfg.Threshold("other", EmployeeWatchdogExecutionRunning); got != 15*time.Minute {
		t.Fatal("default running threshold changed", got)
	}
	bad := DefaultEmployeeWatchdogConfig()
	bad.Agents = map[string]EmployeeWatchdogThresholds{"not-a-uuid": {}}
	if bad.Validate() == nil {
		t.Fatal("invalid agent key accepted")
	}
	bad = DefaultEmployeeWatchdogConfig()
	bad.RunningSilenceSeconds = 10
	if bad.Validate() == nil {
		t.Fatal("a sub-minute live threshold was accepted")
	}
}
