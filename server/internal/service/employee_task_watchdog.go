package service

import (
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeetask"
	"github.com/multica-ai/multica/server/pkg/redact"
)

// EmployeeWatchdogStateKind is the verified execution state a stall episode
// belongs to. Each kind has its own threshold and its own notice wording, so a
// wait or a pending stop is never reported as a silent execution.
type EmployeeWatchdogStateKind string

const (
	EmployeeWatchdogNone                 EmployeeWatchdogStateKind = ""
	EmployeeWatchdogExecutionQueued      EmployeeWatchdogStateKind = "execution_queued"
	EmployeeWatchdogExecutionRunning     EmployeeWatchdogStateKind = "execution_running"
	EmployeeWatchdogExecutionUnreachable EmployeeWatchdogStateKind = "execution_unreachable"
	EmployeeWatchdogWaitingInputs        EmployeeWatchdogStateKind = "waiting_inputs"
	EmployeeWatchdogScheduledWait        EmployeeWatchdogStateKind = "scheduled_wait"
	EmployeeWatchdogStopping             EmployeeWatchdogStateKind = "stopping"
)

// Unreachable reasons.
const (
	EmployeeWatchdogCredentialExpired = "credential_expired"
	EmployeeWatchdogRuntimeOffline    = "runtime_offline"
)

// EmployeeWatchdogThresholds are durations in seconds. Zero inherits the next
// level (agent override -> config default -> built-in default); a negative
// value disables that kind.
type EmployeeWatchdogThresholds struct {
	QueuedSilenceSeconds        int `json:"queued_silence_seconds,omitempty"`
	RunningSilenceSeconds       int `json:"running_silence_seconds,omitempty"`
	UnreachableGraceSeconds     int `json:"unreachable_grace_seconds,omitempty"`
	WaitingInputsSilenceSeconds int `json:"waiting_inputs_silence_seconds,omitempty"`
	ScheduledOverdueSeconds     int `json:"scheduled_overdue_seconds,omitempty"`
	StoppingUnconfirmedSeconds  int `json:"stopping_unconfirmed_seconds,omitempty"`
}

// EmployeeWatchdogConfig is decoded from runtime configuration. Agents holds
// scoped overrides (for example a shortened threshold for one test agent), so
// tests never change the shared live defaults.
type EmployeeWatchdogConfig struct {
	EmployeeWatchdogThresholds
	// ExecutionCredentialTTLSeconds is the lifetime of the daemon credential a
	// cloud sandbox execution receives at launch. After it, the execution can
	// no longer report output or a terminal result, so it is unreachable rather
	// than "still running". Negative disables the rule (after credentials are
	// renewed per task).
	ExecutionCredentialTTLSeconds int `json:"execution_credential_ttl_seconds,omitempty"`
	// MaxNoticesPerBoundary caps sent notices per Task state boundary (one Run,
	// one wait or one stop) across independent episodes.
	MaxNoticesPerBoundary int `json:"max_notices_per_boundary,omitempty"`
	// StoppingWindowSeconds bounds how long a stopped Task is watched for an
	// unconfirmed process exit.
	StoppingWindowSeconds int                                   `json:"stopping_window_seconds,omitempty"`
	Agents                map[string]EmployeeWatchdogThresholds `json:"agents,omitempty"`
}

var employeeWatchdogBuiltIn = EmployeeWatchdogConfig{
	EmployeeWatchdogThresholds: EmployeeWatchdogThresholds{
		QueuedSilenceSeconds:        600,
		RunningSilenceSeconds:       900,
		UnreachableGraceSeconds:     120,
		WaitingInputsSilenceSeconds: 3600,
		ScheduledOverdueSeconds:     600,
		StoppingUnconfirmedSeconds:  600,
	},
	// Matches fcE2BDaemonTokenTTL: cloud daemon tokens are minted per launch
	// and are not renewed.
	ExecutionCredentialTTLSeconds: int(fcE2BDaemonTokenTTL / time.Second),
	MaxNoticesPerBoundary:         3,
	StoppingWindowSeconds:         86400,
}

// DefaultEmployeeWatchdogConfig returns the built-in live defaults.
func DefaultEmployeeWatchdogConfig() EmployeeWatchdogConfig {
	return employeeWatchdogBuiltIn
}

const (
	employeeWatchdogMinSeconds = 60
	employeeWatchdogMaxSeconds = 7 * 86400
)

func validEmployeeWatchdogSeconds(v int) bool {
	return v <= 0 || (v >= employeeWatchdogMinSeconds && v <= employeeWatchdogMaxSeconds)
}

func (t EmployeeWatchdogThresholds) validate() error {
	for _, v := range []int{t.QueuedSilenceSeconds, t.RunningSilenceSeconds, t.UnreachableGraceSeconds, t.WaitingInputsSilenceSeconds, t.ScheduledOverdueSeconds, t.StoppingUnconfirmedSeconds} {
		if !validEmployeeWatchdogSeconds(v) {
			return fmt.Errorf("employee watchdog threshold %ds is outside [%d,%d]", v, employeeWatchdogMinSeconds, employeeWatchdogMaxSeconds)
		}
	}
	return nil
}

// Validate rejects thresholds below one minute, invalid agent keys and
// negative caps or windows.
func (c EmployeeWatchdogConfig) Validate() error {
	if err := c.EmployeeWatchdogThresholds.validate(); err != nil {
		return err
	}
	if !validEmployeeWatchdogSeconds(c.ExecutionCredentialTTLSeconds) || c.MaxNoticesPerBoundary < 0 || c.MaxNoticesPerBoundary > 20 || c.StoppingWindowSeconds < 0 || c.StoppingWindowSeconds > employeeWatchdogMaxSeconds {
		return fmt.Errorf("employee watchdog limits are invalid")
	}
	for id, t := range c.Agents {
		parsed, err := uuid.Parse(id)
		if err != nil || parsed == uuid.Nil || parsed.String() != id {
			return fmt.Errorf("employee watchdog agent override %q is not a canonical UUID", id)
		}
		if err := t.validate(); err != nil {
			return err
		}
	}
	return nil
}

func employeeWatchdogPick(values ...int) time.Duration {
	for _, v := range values {
		if v < 0 {
			return 0
		}
		if v > 0 {
			return time.Duration(v) * time.Second
		}
	}
	return 0
}

// Threshold returns the silence threshold for one agent and state kind; zero
// means the kind is disabled for that agent.
func (c EmployeeWatchdogConfig) Threshold(agentID string, kind EmployeeWatchdogStateKind) time.Duration {
	field := func(t EmployeeWatchdogThresholds) int {
		switch kind {
		case EmployeeWatchdogExecutionQueued:
			return t.QueuedSilenceSeconds
		case EmployeeWatchdogExecutionRunning:
			return t.RunningSilenceSeconds
		case EmployeeWatchdogExecutionUnreachable:
			return t.UnreachableGraceSeconds
		case EmployeeWatchdogWaitingInputs:
			return t.WaitingInputsSilenceSeconds
		case EmployeeWatchdogScheduledWait:
			return t.ScheduledOverdueSeconds
		case EmployeeWatchdogStopping:
			return t.StoppingUnconfirmedSeconds
		}
		return -1
	}
	return employeeWatchdogPick(field(c.Agents[agentID]), field(c.EmployeeWatchdogThresholds), field(employeeWatchdogBuiltIn.EmployeeWatchdogThresholds))
}

func (c EmployeeWatchdogConfig) credentialTTL() time.Duration {
	return employeeWatchdogPick(c.ExecutionCredentialTTLSeconds, employeeWatchdogBuiltIn.ExecutionCredentialTTLSeconds)
}

func (c EmployeeWatchdogConfig) maxNotices() int {
	if c.MaxNoticesPerBoundary > 0 {
		return c.MaxNoticesPerBoundary
	}
	return employeeWatchdogBuiltIn.MaxNoticesPerBoundary
}

func (c EmployeeWatchdogConfig) stoppingWindow() time.Duration {
	return employeeWatchdogPick(c.StoppingWindowSeconds, employeeWatchdogBuiltIn.StoppingWindowSeconds)
}

// EmployeeTaskWaitKind is a durable wait a Task declared (P/A/B own the facts).
type EmployeeTaskWaitKind string

const (
	EmployeeTaskWaitInputs   EmployeeTaskWaitKind = "inputs"
	EmployeeTaskWaitSchedule EmployeeTaskWaitKind = "schedule"
)

// EmployeeTaskWait carries counts only. It deliberately has no field for an
// answer or a participant, so a notice cannot leak anyone's input.
type EmployeeTaskWait struct {
	Kind EmployeeTaskWaitKind
	// Key is a stable wait identity (for example "collection:<id>:<revision>").
	Key      string
	Since    time.Time
	DueAt    time.Time
	Expected int
	Received int
	// Progress is the newest input the Host accepted for this wait.
	Progress EmployeeActivityWatermark
}

// EmployeeWatchdogExecution is the Host's view of one Run and its queue claim.
type EmployeeWatchdogExecution struct {
	RunID            string
	RunState         employeetask.State
	RunCreatedAt     time.Time
	QueueTaskID      string
	QueueStatus      string
	QueueStartedAt   time.Time
	QueueFireAt      time.Time
	RuntimeOffline   bool
	RuntimeChangedAt time.Time
	// CredentialBound marks a cloud sandbox execution whose daemon credential
	// expires a fixed time after launch.
	CredentialBound bool
}

// EmployeeWatchdogStop is the newest Host-recorded stop of a Task.
type EmployeeWatchdogStop struct {
	Seq           int64
	At            time.Time
	ExitConfirmed bool
}

// EmployeeWatchdogFacts are verified PostgreSQL facts for one Task.
type EmployeeWatchdogFacts struct {
	TaskState   employeetask.State
	ActiveRunID string
	// Execution is the active Run, or the latest Run of a stopped Task.
	Execution *EmployeeWatchdogExecution
	Stop      *EmployeeWatchdogStop
	Wait      *EmployeeTaskWait
	Progress  EmployeeActivityWatermark
}

// EmployeeWatchdogState is the classified state. A zero Kind carries the reason
// nothing is watched (a metric, never a notice).
type EmployeeWatchdogState struct {
	Kind     EmployeeWatchdogStateKind
	Reason   string
	RunID    string
	Boundary string
	// StartedAt is when this state began; it is compared with the enable
	// watermark.
	StartedAt time.Time
	// QuietFrom is when silence starts counting for this state.
	QuietFrom      time.Time
	ProgressResets bool
	Expected       int
	Received       int
}

// SilentSince is the first no-progress time: the later of the state's quiet
// start and the newest progress, when progress counts for this state.
func (s EmployeeWatchdogState) SilentSince(p EmployeeActivityWatermark) time.Time {
	if s.ProgressResets && p.At.After(s.QuietFrom) {
		return p.At
	}
	return s.QuietFrom
}

func employeeWatchdogNoState(reason string) EmployeeWatchdogState {
	return EmployeeWatchdogState{Reason: reason}
}

func latestTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// ClassifyEmployeeWatchdogState applies the fixed precedence: stopping, input
// wait, scheduled wait, unreachable execution, queued/running execution, then
// nothing. It is pure; now only decides whether a credential has expired.
func ClassifyEmployeeWatchdogState(f EmployeeWatchdogFacts, now time.Time, credentialTTL time.Duration) EmployeeWatchdogState {
	switch f.TaskState {
	case employeetask.StateCancelled:
		if f.Stop == nil {
			return employeeWatchdogNoState("task_closed")
		}
		if f.Stop.ExitConfirmed || f.Execution == nil {
			return employeeWatchdogNoState("stop_confirmed")
		}
		if f.Stop.At.IsZero() {
			return employeeWatchdogNoState("no_activity_source")
		}
		return EmployeeWatchdogState{Kind: EmployeeWatchdogStopping, RunID: f.Execution.RunID, Boundary: "stop:" + strconv.FormatInt(f.Stop.Seq, 10), StartedAt: f.Stop.At, QuietFrom: f.Stop.At}
	case employeetask.StateSucceeded, employeetask.StateFailed:
		return employeeWatchdogNoState("task_closed")
	case employeetask.StateReady, employeetask.StateRunning, "waiting":
	default:
		return employeeWatchdogNoState("unknown_task_state")
	}
	if w := f.Wait; w != nil {
		if w.Key == "" {
			return employeeWatchdogNoState("no_activity_source")
		}
		state := EmployeeWatchdogState{RunID: f.ActiveRunID, Boundary: "wait:" + w.Key, StartedAt: w.Since}
		switch w.Kind {
		case EmployeeTaskWaitInputs:
			if w.Since.IsZero() || w.Expected < 1 || w.Received < 0 || w.Received > w.Expected {
				return employeeWatchdogNoState("no_activity_source")
			}
			state.Kind, state.QuietFrom, state.ProgressResets = EmployeeWatchdogWaitingInputs, w.Since, true
			state.Expected, state.Received = w.Expected, w.Received
			return state
		case EmployeeTaskWaitSchedule:
			if w.DueAt.IsZero() {
				return employeeWatchdogNoState("no_activity_source")
			}
			if state.StartedAt.IsZero() {
				state.StartedAt = w.DueAt
			}
			state.Kind, state.QuietFrom = EmployeeWatchdogScheduledWait, w.DueAt
			return state
		}
		return employeeWatchdogNoState("unknown_wait_kind")
	}
	if f.TaskState == "waiting" {
		return employeeWatchdogNoState("wait_facts_unavailable")
	}
	e := f.Execution
	if f.ActiveRunID == "" || e == nil || e.RunID != f.ActiveRunID || e.RunState != employeetask.StateRunning {
		return employeeWatchdogNoState("no_active_execution")
	}
	run := EmployeeWatchdogState{RunID: e.RunID, Boundary: "run:" + e.RunID, StartedAt: e.RunCreatedAt, ProgressResets: true}
	unreachable := func(reason string, since time.Time) (EmployeeWatchdogState, bool) {
		if f.Progress.At.After(since) {
			// The execution reported after the point it was deemed unreachable.
			return EmployeeWatchdogState{}, false
		}
		run.Kind, run.Reason, run.QuietFrom = EmployeeWatchdogExecutionUnreachable, reason, since
		return run, true
	}
	switch e.QueueStatus {
	case "queued", "dispatched", "waiting_local_directory":
		if e.RunCreatedAt.IsZero() {
			return employeeWatchdogNoState("no_activity_source")
		}
		if e.RuntimeOffline {
			if s, ok := unreachable(EmployeeWatchdogRuntimeOffline, latestTime(e.RunCreatedAt, e.RuntimeChangedAt)); ok {
				return s
			}
		}
		run.Kind, run.QuietFrom = EmployeeWatchdogExecutionQueued, e.RunCreatedAt
		return run
	case "deferred":
		if e.QueueFireAt.IsZero() || e.RunCreatedAt.IsZero() {
			return employeeWatchdogNoState("no_activity_source")
		}
		run.Kind, run.Boundary, run.QuietFrom, run.ProgressResets = EmployeeWatchdogScheduledWait, "deferred:"+e.RunID, e.QueueFireAt, false
		return run
	case "running":
		started := e.QueueStartedAt
		if started.IsZero() {
			started = e.RunCreatedAt
		}
		if started.IsZero() {
			return employeeWatchdogNoState("no_activity_source")
		}
		if run.StartedAt.IsZero() {
			run.StartedAt = started
		}
		if e.CredentialBound && credentialTTL > 0 {
			if expiry := started.Add(credentialTTL); !now.Before(expiry) {
				if s, ok := unreachable(EmployeeWatchdogCredentialExpired, expiry); ok {
					return s
				}
			}
		}
		if e.RuntimeOffline {
			if s, ok := unreachable(EmployeeWatchdogRuntimeOffline, latestTime(started, e.RuntimeChangedAt)); ok {
				return s
			}
		}
		run.Kind, run.QuietFrom = EmployeeWatchdogExecutionRunning, started
		return run
	case "completed", "failed", "cancelled":
		return employeeWatchdogNoState("execution_terminal")
	}
	return employeeWatchdogNoState("unknown_execution_state")
}

const employeeWatchdogGoalRunes = 30

// employeeWatchdogSubject quotes the first clause of the Task goal so the
// requester can tell which work the notice is about.
func employeeWatchdogSubject(goal string) string {
	goal = strings.Join(strings.Fields(redact.Text(goal)), " ")
	if goal == "" {
		return "这项工作"
	}
	cut := false
	if i := strings.IndexAny(goal, "，。；！？,.;!?"); i > 0 {
		goal, cut = goal[:i], true
	}
	if utf8.RuneCountInString(goal) > employeeWatchdogGoalRunes {
		goal, cut = string([]rune(goal)[:employeeWatchdogGoalRunes]), true
	}
	if cut {
		goal += "…"
	}
	return "「" + goal + "」"
}

// ComposeEmployeeWatchdogNotice renders the deterministic notice for a verified
// state. It states only Host facts: no percentages, no ETA and no claim that
// the work is stuck or still running when the Host cannot know it.
func ComposeEmployeeWatchdogNotice(s EmployeeWatchdogState, goal string, silentFor time.Duration) string {
	subject := employeeWatchdogSubject(goal)
	minutes := int(silentFor / time.Minute)
	if minutes < 1 {
		minutes = 1
	}
	switch s.Kind {
	case EmployeeWatchdogExecutionRunning:
		return fmt.Sprintf("%s还在执行，最近约 %d 分钟没有新的可观测输出。完成后会在这里告诉你结果。", subject, minutes)
	case EmployeeWatchdogExecutionQueued:
		return fmt.Sprintf("%s还在排队等待执行环境，尚未开始执行。", subject)
	case EmployeeWatchdogExecutionUnreachable:
		if s.Reason == EmployeeWatchdogRuntimeOffline {
			return fmt.Sprintf("%s所在的执行环境目前离线，我无法确认这次执行是否还在进行。", subject)
		}
		return fmt.Sprintf("%s的执行环境已无法回报进展，我无法确认这次执行是否还在进行。这是执行环境的限制，不是工作内容本身的问题。", subject)
	case EmployeeWatchdogWaitingInputs:
		return fmt.Sprintf("%s还在等待补充信息，目前已收到 %d/%d 份。", subject, s.Received, s.Expected)
	case EmployeeWatchdogScheduledWait:
		return fmt.Sprintf("%s已过预定的继续时间，目前还没有开始。", subject)
	case EmployeeWatchdogStopping:
		return fmt.Sprintf("已记录停止%s的请求，执行进程尚未确认退出。", subject)
	}
	return ""
}

// employeeWatchdogOpen is the open episode a scan or BeforeSend found.
type employeeWatchdogOpen struct {
	ID        string
	Kind      EmployeeWatchdogStateKind
	Boundary  string
	Since     time.Time
	Watermark EmployeeActivityWatermark
}

// employeeWatchdogDecision is the pure outcome of one evaluation.
type employeeWatchdogDecision struct {
	Close          bool
	CloseState     string // cleared | closed
	CloseReason    string
	CloseWatermark EmployeeActivityWatermark
	Open           bool
	Since          time.Time
	Watermark      EmployeeActivityWatermark
	Skip           string
}

// decideEmployeeWatchdog closes an episode whose state ended or changed, clears
// it silently on fresh progress, and opens at most one new episode when the
// current state has been silent for its threshold. States that began before
// the agent's enable watermark are never aged.
func decideEmployeeWatchdog(open *employeeWatchdogOpen, s EmployeeWatchdogState, progress EmployeeActivityWatermark, enabledAt time.Time, threshold time.Duration, now time.Time) employeeWatchdogDecision {
	var d employeeWatchdogDecision
	if open != nil {
		progress = progress.Merge(open.Watermark)
		closeAs := func(state, reason string) {
			d.Close, d.CloseState, d.CloseReason, d.CloseWatermark = true, state, reason, progress
		}
		switch {
		case s.Kind == EmployeeWatchdogNone:
			closeAs("closed", "state_ended")
		case open.Kind != s.Kind || open.Boundary != s.Boundary:
			closeAs("closed", "superseded")
		case threshold <= 0:
			closeAs("closed", "kind_disabled")
		default:
			since := s.SilentSince(progress)
			if !since.After(open.Since) {
				return d
			}
			if now.Sub(since) < threshold {
				closeAs("cleared", "progress")
				return d
			}
			// Progress landed, then silence resumed for a full threshold: a
			// newer independent episode replaces this one.
			closeAs("closed", "superseded")
		}
	}
	switch {
	case s.Kind == EmployeeWatchdogNone:
		d.Skip = s.Reason
	case s.StartedAt.IsZero():
		d.Skip = "no_activity_source"
	case s.StartedAt.Before(enabledAt):
		d.Skip = "before_enable"
	case threshold <= 0:
		d.Skip = "kind_disabled"
	default:
		since := s.SilentSince(progress)
		if now.Sub(since) >= threshold {
			d.Open, d.Since, d.Watermark = true, since, progress
		}
	}
	return d
}
