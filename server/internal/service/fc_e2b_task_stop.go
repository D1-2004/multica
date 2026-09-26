package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A cancelled task stops only its own runner: the in-sandbox daemon kills the
// agent's process group, but agent tools start their commands in new process
// groups or sessions, and those survived cancellation until the sandbox
// expired or, worse, kept running into the next task that reused it. The
// server therefore ends every process of the cancelled task inside each
// sandbox the task used, through the same FC/E2B transport as every other
// command, so the CLI and the SDK behave alike.
const (
	// fcE2BTaskMarkerEnv marks the runner and everything it starts. Unlike
	// MULTICA_TASK_ID, which the daemon re-sets per child and blanks for A2A
	// children, a non-MULTICA key without a credential-like suffix is
	// inherited through the daemon, the agent and its tools unchanged.
	fcE2BTaskMarkerEnv = "FC_E2B_TASK_ID"
	// The in-sandbox daemon polls for cancellation; give it a moment to stop
	// its agent and report before the remaining processes are ended.
	fcE2BTaskStopDelay       = 5 * time.Second
	fcE2BTaskStopExecTimeout = 45 * time.Second
	fcE2BTaskStopBudget      = 2 * time.Minute
	fcE2BTaskStopConcurrency = 4
)

var (
	fcE2BTaskStopSlots   = make(chan struct{}, fcE2BTaskStopConcurrency)
	fcE2BTaskStopPending sync.Map
)

// fcE2BTaskStopScript ends every process of one task in the sandbox. A
// process belongs to the task when its environment carries the task's
// marker, when it is in a process group or session whose leader carries the
// marker, or when it descends from such a process. Group and session members
// count only under a marked leader, so envd, init and other tasks sharing a
// group or session with this script are never included. SIGTERM goes first;
// after a short grace SIGKILL ends what is left. The last line is a JSON
// receipt. It needs bash for NUL-separated reads and associative arrays and
// runs as root to read every process environment.
const fcE2BTaskStopScript = `task=$1
case $task in ""|*[!0-9a-f-]*) echo '{"version":1,"error":"invalid task id"}'; exit 2;; esac
self=$$
declare -A ppid=() pgid=() sid=() marked=() target=()
stat_of() {
  local raw
  { read -r raw <"/proc/$1/stat"; } 2>/dev/null || return 1
  raw=${raw##*) }
  read -r st pp pg ss _ <<<"$raw"
}
has_marker() {
  local kv
  while IFS= read -r -d '' kv; do
    [[ $kv == "MULTICA_TASK_ID=$task" || $kv == "FC_E2B_TASK_ID=$task" ]] && return 0
  done
  return 1
}
for d in /proc/[0-9]*; do
  p=${d#/proc/}
  [[ $p == 1 || $p == "$self" ]] && continue
  stat_of "$p" || continue
  [[ $st == Z ]] && continue
  ppid[$p]=$pp; pgid[$p]=$pg; sid[$p]=$ss
  { has_marker <"$d/environ"; } 2>/dev/null && marked[$p]=1
done
for p in "${!marked[@]}"; do target[$p]=1; done
for p in "${!ppid[@]}"; do
  if [[ -n ${marked[${sid[$p]}]:-} || -n ${marked[${pgid[$p]}]:-} ]]; then target[$p]=1; fi
done
changed=1
while (( changed )); do
  changed=0
  for p in "${!ppid[@]}"; do
    if [[ -z ${target[$p]:-} && -n ${target[${ppid[$p]}]:-} ]]; then target[$p]=1; changed=1; fi
  done
done
unset "target[$self]" "target[1]"
alive() { stat_of "$1" && [[ $st != Z ]]; }
survivors() { local p; for p in "${!target[@]}"; do alive "$p" && printf '%s ' "$p"; done; }
count() { set -- $1; echo $#; }
found=${#target[@]} terminated=0 killed=0 remaining=0
if (( found > 0 )); then
  kill -TERM "${!target[@]}" 2>/dev/null
  for ((i = 0; i < 20; i++)); do
    left=$(survivors)
    [[ -z $left ]] && break
    sleep 0.25
  done
  left=$(survivors)
  terminated=$(( found - $(count "$left") ))
  if [[ -n $left ]]; then
    kill -KILL $left 2>/dev/null
    for ((i = 0; i < 8; i++)); do
      after=$(survivors)
      [[ -z $after ]] && break
      sleep 0.25
    done
    remaining=$(count "$(survivors)")
    killed=$(( $(count "$left") - remaining ))
  fi
fi
printf '{"version":1,"found":%d,"terminated":%d,"killed":%d,"remaining":%d}\n' "$found" "$terminated" "$killed" "$remaining"
`

// fcE2BTaskStopReceipt is the script's report for one sandbox.
type fcE2BTaskStopReceipt struct {
	Version    int    `json:"version"`
	Found      int    `json:"found"`
	Terminated int    `json:"terminated"`
	Killed     int    `json:"killed"`
	Remaining  int    `json:"remaining"`
	Error      string `json:"error"`
}

// fcE2BTaskStopArgs ends the task's processes as root with the same loader
// hardening as the root runner launch.
func fcE2BTaskStopArgs(sandboxID, taskID string) []string {
	return []string{
		"sandbox", "exec",
		"--user", "root",
		"-e", "LD_PRELOAD=",
		"-e", "LD_LIBRARY_PATH=",
		"-e", "LD_AUDIT=",
		"-e", "GCONV_PATH=",
		"-e", "BASH_ENV=",
		"-e", "ENV=",
		sandboxID,
		"--",
		"bash", "-c", fcE2BTaskStopScript, "fc-e2b-task-stop", taskID,
	}
}

func parseFCE2BTaskStopReceipt(out string) (fcE2BTaskStopReceipt, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var receipt fcE2BTaskStopReceipt
	if err := json.Unmarshal([]byte(strings.TrimSpace(lines[len(lines)-1])), &receipt); err != nil || receipt.Version != 1 {
		return fcE2BTaskStopReceipt{}, errors.New("FC/E2B task stop returned no receipt")
	}
	if receipt.Error != "" {
		return receipt, fmt.Errorf("FC/E2B task stop refused: %s", receipt.Error)
	}
	return receipt, nil
}

// scheduleCancelledTaskStop starts ending a cancelled task's processes off
// the transition path. The event and the metrics path both notify for one
// terminal write; only one stop runs per task at a time.
func (l *FCE2BLauncher) scheduleCancelledTaskStop(task db.AgentTaskQueue) {
	if l == nil || l.Pool == nil || task.Status != "cancelled" || !task.ID.Valid || !task.RuntimeID.Valid {
		return
	}
	taskKey := util.UUIDToString(task.ID)
	if _, inFlight := fcE2BTaskStopPending.LoadOrStore(taskKey, struct{}{}); inFlight {
		return
	}
	go func() {
		defer fcE2BTaskStopPending.Delete(taskKey)
		defer func() {
			if r := recover(); r != nil {
				slog.Error("FC/E2B task stop panicked", "task_id", taskKey, "recovered", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), fcE2BTaskStopDelay+fcE2BTaskStopBudget)
		defer cancel()
		sleep := sleepWithContext
		if l.sleep != nil {
			sleep = l.sleep
		}
		if err := sleep(ctx, fcE2BTaskStopDelay); err != nil {
			return
		}
		select {
		case fcE2BTaskStopSlots <- struct{}{}:
			defer func() { <-fcE2BTaskStopSlots }()
		case <-ctx.Done():
			slog.Warn("FC/E2B task stop skipped: stop slots busy", "event", "fc_e2b_task_processes_stopped", "task_id", taskKey)
			return
		}
		l.withCurrentConfig().stopCancelledTaskProcesses(ctx, task.ID)
	}()
}

// stopCancelledTaskProcesses ends the task's processes in every sandbox its
// start attempts used. It rereads the task: only a cancelled task is stopped.
func (l *FCE2BLauncher) stopCancelledTaskProcesses(ctx context.Context, taskID pgtype.UUID) {
	if l == nil || l.Queries == nil || l.Pool == nil || !l.Config.Enabled {
		return
	}
	task, err := l.Queries.GetAgentTask(ctx, taskID)
	if err != nil || task.Status != "cancelled" {
		return
	}
	runtime, err := l.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil || !IsFCE2BRuntime(runtime) {
		return
	}
	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		slog.Warn("FC/E2B task stop could not acquire a connection", "task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	sandboxes, err := taskSandboxIDs(ctx, conn, task.ID)
	conn.Release()
	if err != nil {
		slog.Warn("FC/E2B task stop could not list sandboxes", "task_id", util.UUIDToString(taskID), "error", err)
		return
	}
	for _, sandboxID := range sandboxes {
		l.stopTaskProcessesInSandbox(ctx, task, runtime, sandboxID)
	}
}

// stopTaskProcessesInSandbox runs the stop script in one sandbox and logs the
// receipt. The command carries the task's scope, so the SDK rollout applies.
func (l *FCE2BLauncher) stopTaskProcessesInSandbox(ctx context.Context, task db.AgentTaskQueue, rt db.AgentRuntime, sandboxID string) (fcE2BTaskStopReceipt, error) {
	attrs := []any{"event", "fc_e2b_task_processes_stopped", "task_id", util.UUIDToString(task.ID),
		"agent_id", util.UUIDToString(task.AgentID), "runtime_id", util.UUIDToString(task.RuntimeID), "sandbox_id", sandboxID}
	if !fcE2BAPISandboxIDPattern.MatchString(sandboxID) {
		err := errors.New("invalid FC/E2B sandbox id")
		slog.Warn("FC/E2B task processes not stopped", append(attrs, "error", err.Error())...)
		return fcE2BTaskStopReceipt{}, err
	}
	ctx = WithFCE2BScope(ctx, FCE2BScope{
		WorkspaceID: pgFCE2BScopeID(rt.WorkspaceID),
		AgentID:     pgFCE2BScopeID(task.AgentID),
		RuntimeID:   pgFCE2BScopeID(task.RuntimeID),
	})
	started := time.Now()
	out, err := l.runE2BCommandWithTimeout(ctx, fcE2BTaskStopExecTimeout, fcE2BTaskStopArgs(sandboxID, util.UUIDToString(task.ID)))
	attrs = append(attrs, "duration_ms", time.Since(started).Milliseconds())
	if err != nil {
		if fcE2BSandboxMissing(err) {
			slog.Info("FC/E2B task processes stopped", append(attrs, "outcome", "sandbox_gone")...)
			return fcE2BTaskStopReceipt{}, nil
		}
		// The error text can carry command output; keep it out of the log.
		slog.Warn("FC/E2B task processes not stopped", append(attrs, "outcome", "error")...)
		return fcE2BTaskStopReceipt{}, err
	}
	receipt, err := parseFCE2BTaskStopReceipt(out)
	if err != nil {
		slog.Warn("FC/E2B task processes not stopped", append(attrs, "outcome", "invalid_receipt")...)
		return receipt, err
	}
	attrs = append(attrs, "found", receipt.Found, "terminated", receipt.Terminated, "killed", receipt.Killed, "remaining", receipt.Remaining)
	if receipt.Remaining > 0 {
		slog.Warn("FC/E2B task processes stopped", append(attrs, "outcome", "survivors")...)
		return receipt, nil
	}
	slog.Info("FC/E2B task processes stopped", append(attrs, "outcome", "ok")...)
	return receipt, nil
}

// fcE2BSandboxMissing reports a sandbox that no longer exists. Both
// transports surface the control plane's 404 for the connect.
func fcE2BSandboxMissing(err error) bool {
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "sandbox not found") || strings.Contains(text, "404")
}
