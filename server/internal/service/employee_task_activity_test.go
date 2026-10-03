package service

import (
	"testing"
	"time"
)

func TestEmployeeWatchdogActivityClassification(t *testing.T) {
	at := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name     string
		activity EmployeeActivity
		progress bool
	}{
		{"assistant text", EmployeeActivity{Source: EmployeeActivityTaskMessage, Kind: "text", At: at}, true},
		{"thinking", EmployeeActivity{Source: EmployeeActivityTaskMessage, Kind: "thinking", At: at}, true},
		{"tool call", EmployeeActivity{Source: EmployeeActivityTaskMessage, Kind: "tool_use", At: at}, true},
		{"tool result", EmployeeActivity{Source: EmployeeActivityTaskMessage, Kind: "tool_result", At: at}, true},
		{"daemon status", EmployeeActivity{Source: EmployeeActivityTaskMessage, Kind: "status", At: at}, false},
		{"daemon log", EmployeeActivity{Source: EmployeeActivityTaskMessage, Kind: "log", At: at}, false},
		{"execution error", EmployeeActivity{Source: EmployeeActivityTaskMessage, Kind: "error", At: at}, false},
		{"unknown message type", EmployeeActivity{Source: EmployeeActivityTaskMessage, Kind: "keepalive", At: at}, false},
		{"accepted human input", EmployeeActivity{Source: EmployeeActivityTaskEntry, Kind: "input", ActorRef: "member:a", At: at}, true},
		{"human correction", EmployeeActivity{Source: EmployeeActivityTaskEntry, Kind: "amendment", ActorRef: "member:a", At: at}, true},
		{"steer", EmployeeActivity{Source: EmployeeActivityTaskEntry, Kind: "steer", ActorRef: "member:a", At: at}, true},
		{"continuation", EmployeeActivity{Source: EmployeeActivityTaskEntry, Kind: "resumed", ActorRef: "member:a", At: at}, true},
		{"stop request", EmployeeActivity{Source: EmployeeActivityTaskEntry, Kind: "input", Operation: "stop", ActorRef: "member:a", At: at}, false},
		{"unknown operation", EmployeeActivity{Source: EmployeeActivityTaskEntry, Kind: "input", Operation: "ping", ActorRef: "member:a", At: at}, false},
		{"anonymous input", EmployeeActivity{Source: EmployeeActivityTaskEntry, Kind: "input", At: at}, false},
		{"task creation", EmployeeActivity{Source: EmployeeActivityTaskEntry, Kind: "request", ActorRef: "member:a", At: at}, false},
		{"run_started ledger", EmployeeActivity{Source: EmployeeActivityTaskEntry, Kind: "run_started", At: at}, false},
		{"result ledger", EmployeeActivity{Source: EmployeeActivityTaskEntry, Kind: "result", At: at}, false},
		{"issue_bound ledger", EmployeeActivity{Source: EmployeeActivityTaskEntry, Kind: "issue_bound", At: at}, false},
		{"writer fence ledger", EmployeeActivity{Source: EmployeeActivityTaskEntry, Kind: "writer_fenced", At: at}, false},
		{"future entry kind", EmployeeActivity{Source: EmployeeActivityTaskEntry, Kind: "waited", ActorRef: "member:a", At: at}, false},
		{"verified artifact", EmployeeActivity{Source: EmployeeActivityArtifact, Kind: "ready", At: at}, true},
		{"pending artifact", EmployeeActivity{Source: EmployeeActivityArtifact, Kind: "pending", At: at}, false},
		{"accepted collected input", EmployeeActivity{Source: EmployeeActivityTaskInput, Kind: "answer", At: at}, true},
		{"runtime heartbeat", EmployeeActivity{Source: EmployeeActivityRuntimeHeartbeat, At: at}, false},
		{"lease renewal", EmployeeActivity{Source: EmployeeActivityLeaseRenewal, At: at}, false},
		{"routine scan", EmployeeActivity{Source: EmployeeActivityRoutineScan, At: at}, false},
		{"trace export", EmployeeActivity{Source: EmployeeActivityTraceExport, At: at}, false},
		{"watchdog notice", EmployeeActivity{Source: EmployeeActivityHostNotice, Kind: "execution_running", At: at}, false},
		{"progress question in chat", EmployeeActivity{Source: EmployeeActivityHumanChat, Kind: "进展如何？", ActorRef: "member:a", At: at}, false},
		{"output without host time", EmployeeActivity{Source: EmployeeActivityTaskMessage, Kind: "text"}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, reason := ClassifyEmployeeActivity(c.activity)
			if got != c.progress || reason == "" {
				t.Fatalf("progress=%v reason=%q, want %v", got, reason, c.progress)
			}
		})
	}
}

func TestEmployeeWatchdogWatermarkIsMonotonic(t *testing.T) {
	base := time.Date(2026, 10, 3, 10, 0, 0, 0, time.UTC)
	w := EmployeeActivityWatermark{}
	w, moved := w.Observe(EmployeeActivity{Source: EmployeeActivityTaskMessage, Ref: "m2", Kind: "text", At: base.Add(2 * time.Minute)})
	if !moved || w.Ref != "m2" {
		t.Fatal("first progress did not set the watermark", w)
	}
	// A late row with an older Host time and an equal-time row do not move it.
	for _, late := range []EmployeeActivity{
		{Source: EmployeeActivityTaskMessage, Ref: "m1", Kind: "tool_result", At: base.Add(time.Minute)},
		{Source: EmployeeActivityArtifact, Ref: "a1", Kind: "ready", At: base.Add(2 * time.Minute)},
	} {
		if next, moved := w.Observe(late); moved || next != w {
			t.Fatal("older or equal progress moved the watermark back", next)
		}
	}
	// Bookkeeping never moves it, however new.
	if next, moved := w.Observe(EmployeeActivity{Source: EmployeeActivityTaskEntry, Ref: "t:9", Kind: "run_started", At: base.Add(time.Hour)}); moved || next != w {
		t.Fatal("bookkeeping moved the watermark", next)
	}
	if got := w.Merge(EmployeeActivityWatermark{At: base, Source: EmployeeActivityTaskInput, Ref: "old"}); got != w {
		t.Fatal("merge regressed", got)
	}
	newer := EmployeeActivityWatermark{At: base.Add(3 * time.Minute), Source: EmployeeActivityTaskInput, Ref: "new"}
	if got := w.Merge(newer); got != newer {
		t.Fatal("merge ignored newer progress", got)
	}
}
