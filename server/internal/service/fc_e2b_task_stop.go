package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	e2b "github.com/aliyun-fc/e2b-go-sdk"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A cancelled or failed task stops only its own runner: the in-sandbox daemon
// ends the agent, but agent tools start their commands in new sessions, and
// those survived the task until the sandbox expired or, worse, kept running
// into the next task that reused it (PRI-52). The server therefore ends an
// aborted task's processes inside each sandbox the task used. A completed
// task's processes are left to the sandbox release.
//
// For legacy aborts, runtime.fc_e2b_sdk_rollout gates the stop and runner marker together
// with the SDK transport, by the task's workspace, agent and runtime: a scope
// it does not select keeps the behavior from before the SDK change.
// Explicit steer cancellation requires exit proof through either transport.
//
// Only processes provably owned by the task are ended; a process whose owner
// cannot be proven is left alone (PRI-61: a start-time window took another
// task's orphan). Ownership is proven by the runner's command line and by the
// task id in a process's environment, which every tool inherits from the
// agent. FC sandboxes give root no CAP_SYS_PTRACE, so the environment is read
// under the process's own uid.
const (
	// fcE2BTaskMarkerEnv carries the task id through the runner, the daemon,
	// the agent and its tools. Unlike MULTICA_TASK_ID, which the daemon blanks
	// for A2A children, a non-MULTICA key without a credential-like suffix is
	// inherited unchanged.
	fcE2BTaskMarkerEnv = "FC_E2B_TASK_ID"
	// The in-sandbox daemon notices a cancellation within seconds and then
	// tears its tree down, orphaning tool commands. The first pass runs at
	// once to catch the tree intact; the second, 10 seconds after the task
	// ended (at once when the first pass took longer), catches what was
	// forked or orphaned in between.
	fcE2BTaskStopSecondPass  = 10 * time.Second
	fcE2BTaskStopExecTimeout = 45 * time.Second
	fcE2BTaskStopBudget      = 2 * time.Minute
	fcE2BTaskStopConcurrency = 4
	// fcE2BTaskStopMaxPending bounds the stops waiting for a slot, so a burst
	// of aborted tasks cannot pile up goroutines behind four slots.
	fcE2BTaskStopMaxPending = 512
)

var (
	fcE2BTaskStopSlots        = make(chan struct{}, fcE2BTaskStopConcurrency)
	fcE2BTaskStopPending      sync.Map
	fcE2BTaskStopPendingCount atomic.Int64
)

// fcE2BTaskAborted reports a terminal status whose processes are ended.
// Retries and reruns are new tasks, so an aborted task id never runs again.
func fcE2BTaskAborted(status string) bool {
	return status == "cancelled" || status == "failed"
}

// fcE2BTaskStopScript ends one task's processes in the sandbox. It takes the
// runtime id, the task's health port and the task id. A process is the
// task's when it is proven to be:
//   - its runner: a command line carrying both --runtime-id <id> and
//     --health-port <port>, with an environment that can be read and names
//     no other task (health ports of two tasks can collide);
//   - marked: its environment carries MULTICA_TASK_ID=<task> or
//     FC_E2B_TASK_ID=<task>, read under the process's own uid;
//   - a descendant of a proven process;
//   - in a session or process group, other than 1, that holds a proven
//     process and whose leader is proven or gone: only the leader's
//     descendants can join it, so it was created by one of the task's
//     processes. A live leader that is not the task's, such as the session
//     the runner was started in, is not expanded.
//
// Never ended: a process whose environment names another task; sandbox
// services under /usr/local/libexec/multica-* and /.fce2b/ unless they
// descend from the runner (a shared service started by an earlier runner
// still carries that task's id); and anything unproven, such as an orphan
// that cleared its environment, which is reported rather than guessed.
//
// The tree is frozen with SIGSTOP and rescanned before it is ended, so no
// process escapes by forking or re-parenting meanwhile; then SIGTERM with
// SIGCONT, and SIGKILL after a grace. A process is known by its pid and start
// time, checked again after every SIGSTOP: a stopped process keeps its pid,
// so a pid that changed hands before the SIGSTOP reached it is resumed at
// once and is not sent SIGTERM or SIGKILL. What bash cannot close is a
// verified target that someone else kills and reaps in the instant between
// the check and the signal, its pid reused in that instant; Linux allocates
// pids cyclically, so that takes the pid space wrapping around meanwhile.
// Signalling through a pidfd would close it, but bash has no pidfd and the
// sandbox images promise no other interpreter. The last line is a JSON
// receipt. It runs as root under bash (mapfile, associative arrays) and
// needs setpriv to read other users' environments.
const fcE2BTaskStopScript = `rt=$1 port=$2 task=$3
uuid='^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$'
if [[ ! $rt =~ $uuid || ! $task =~ $uuid || ! $port =~ ^[0-9]{1,5}$ ]]; then
  echo '{"version":3,"error":"invalid arguments"}'; exit 2
fi
self=$$
# Prints "M <pid>" for this task's marker, "F <pid>" for another task's and
# "U <pid>" when the environment cannot be read. Runs as the processes' uid.
markers='task=$1; shift
for p; do
  mine=0 other=0
  if ! { while IFS= read -r -d "" kv || [[ -n $kv ]]; do
      case $kv in
        "MULTICA_TASK_ID=$task"|"FC_E2B_TASK_ID=$task") mine=1;;
        MULTICA_TASK_ID=?*|FC_E2B_TASK_ID=?*) other=1;;
      esac
    done <"/proc/$p/environ"; } 2>/dev/null; then echo "U $p"; continue; fi
  if (( mine )); then echo "M $p"; elif (( other )); then echo "F $p"; fi
done'
declare -A ppid=() pgid=() sid=() start=() runner=() infra=() marked=() foreign=() unreadable=() target=() tstart=()
scan() {
  ppid=() pgid=() sid=() start=() runner=() infra=() marked=() foreign=() unreadable=()
  local d p raw f argv i has_rt has_port key a rest uid gid out kind
  local -A byuid=()
  for d in /proc/[0-9]*; do
    p=${d#/proc/}
    [[ $p == 1 || $p == "$self" ]] && continue
    { read -r raw <"$d/stat"; } 2>/dev/null || continue
    read -r -a f <<<"${raw##*) }"
    # Zombies and kernel threads (PF_KTHREAD) own nothing.
    [[ ${f[0]} == Z ]] || (( f[6] & 0x200000 )) && continue
    ppid[$p]=${f[1]} pgid[$p]=${f[2]} sid[$p]=${f[3]} start[$p]=${f[19]} uid= gid=
    { while read -r key a rest; do
        case $key in Uid:) uid=$a;; Gid:) gid=$a;; esac
      done <"$d/status"; } 2>/dev/null
    argv=()
    { mapfile -d '' -t argv <"$d/cmdline"; } 2>/dev/null
    has_rt=0 has_port=0
    for ((i = 0; i + 1 < ${#argv[@]}; i++)); do
      [[ ${argv[i]} == --runtime-id && ${argv[i+1]} == "$rt" ]] && has_rt=1
      [[ ${argv[i]} == --health-port && ${argv[i+1]} == "$port" ]] && has_port=1
    done
    (( has_rt && has_port )) && runner[$p]=1
    case " ${argv[*]:0:3}" in *" /usr/local/libexec/multica-"*|*" /.fce2b/"*) infra[$p]=1;; esac
    [[ -n $uid && -n $gid ]] && byuid[$uid:$gid]+="$p "
  done
  for key in "${!byuid[@]}"; do
    uid=${key%%:*} gid=${key#*:}
    if [[ $uid == "$EUID" ]]; then
      out=$(bash -c "$markers" fc-e2b-task-markers "$task" ${byuid[$key]})
    elif [[ $EUID == 0 ]]; then
      out=$(setpriv --reuid="$uid" --regid="$gid" --clear-groups bash -c "$markers" fc-e2b-task-markers "$task" ${byuid[$key]} 2>/dev/null) ||
        out=$(printf 'U %s\n' ${byuid[$key]})
    else
      out=$(printf 'U %s\n' ${byuid[$key]})
    fi
    while read -r kind p; do
      case $kind in M) marked[$p]=1;; F) foreign[$p]=1;; U) unreadable[$p]=1;; esac
    done <<<"$out"
  done
}
# owned reports whether a session or group id was created by the task: its
# leader is proven, or it is gone and a proven process remains in it.
owned() { (( $1 > 1 )) && { [[ -n ${target[$1]:-} ]] || [[ -z ${ppid[$1]:-} ]]; }; }
collect() {
  local p changed=1 groups sessions
  for p in "${!runner[@]}"; do provenrunner "$p" && target[$p]=1; done
  for p in "${!marked[@]}"; do [[ -z ${infra[$p]:-} ]] && target[$p]=1; done
  while (( changed )); do
    changed=0 groups=" " sessions=" "
    for p in "${!target[@]}"; do
      [[ -n ${ppid[$p]:-} ]] || continue
      owned "${sid[$p]}" && sessions+="${sid[$p]} "
      owned "${pgid[$p]}" && groups+="${pgid[$p]} "
    done
    for p in "${!ppid[@]}"; do
      [[ -n ${target[$p]:-} || -n ${foreign[$p]:-} ]] && continue
      if [[ -n ${target[${ppid[$p]}]:-} ]] ||
        { [[ -z ${infra[$p]:-} ]] && [[ $sessions == *" ${sid[$p]} "* || $groups == *" ${pgid[$p]} "* ]]; }; then
        target[$p]=1 changed=1
      fi
    done
  done
  unset "target[$self]" "target[1]"
  for p in "${!target[@]}"; do [[ -n ${tstart[$p]:-} ]] || tstart[$p]=${start[$p]}; done
}
# provenrunner: a runner command line whose environment was read and names no
# other task. An unreadable one may be another task's on a colliding port.
provenrunner() { [[ -z ${foreign[$1]:-} && -z ${unreadable[$1]:-} ]]; }
# alive: the target is still the process that was proven, not a reused pid.
alive() {
  local raw f
  { read -r raw <"/proc/$1/stat"; } 2>/dev/null || return 1
  read -r -a f <<<"${raw##*) }"
  [[ ${f[0]} != Z && ${f[19]} == "${tstart[$1]:-}" ]]
}
survivors() { local p; for p in "${!target[@]}"; do alive "$p" && printf '%s ' "$p"; done; }
count() { set -- $1; echo $#; }
# freeze stops the live targets, then resumes every pid that is no longer the
# proven process: it changed hands before the SIGSTOP reached it. A stopped
# process keeps its pid, so the targets left alive are the proven ones.
freeze() {
  local p live
  live=$(survivors)
  kill -STOP $live 2>/dev/null
  for p in $live; do alive "$p" || kill -CONT "$p" 2>/dev/null; done
}
scan; collect
found=0 terminated=0 killed=0 remaining=0
if (( ${#target[@]} > 0 )); then
  freeze
  # Rescan the frozen tree: keep the targets that are still the same
  # process, then add what was forked or re-parented meanwhile.
  first=("${!target[@]}")
  target=()
  scan
  for p in "${first[@]}"; do
    if [[ ${start[$p]:-} == "${tstart[$p]}" ]]; then target[$p]=1; else unset "tstart[$p]"; fi
  done
  collect
  freeze
  live=$(survivors)
  found=$(count "$live")
  kill -TERM $live 2>/dev/null
  kill -CONT $live 2>/dev/null
  for ((i = 0; i < 20; i++)); do
    [[ -z $(survivors) ]] && break
    sleep 0.25
  done
  left=$(survivors)
  terminated=$(( found - $(count "$left") ))
  if [[ -n $left ]]; then
    freeze
    left=$(survivors)
    kill -KILL $left 2>/dev/null
    for ((i = 0; i < 8; i++)); do
      [[ -z $(survivors) ]] && break
      sleep 0.25
    done
    remaining=$(count "$(survivors)")
    killed=$(( $(count "$left") - remaining ))
  fi
fi
runners=0 unresolved_runners=0 quiescent=false
for p in "${!runner[@]}"; do
  provenrunner "$p" && runners=$((runners + 1))
  # A matching runtime/port with unreadable ownership remains ambiguous.
  # Unreadable unrelated sandbox services do not own this task's writer lane.
  [[ -n ${unreadable[$p]:-} && -z ${foreign[$p]:-} ]] && unresolved_runners=$((unresolved_runners + 1))
done
(( remaining == 0 && unresolved_runners == 0 )) && quiescent=true
printf '{"version":4,"quiescent":%s,"unresolved_runners":%d,"runners":%d,"marked":%d,"unreadable":%d,"found":%d,"terminated":%d,"killed":%d,"remaining":%d}\n' "$quiescent" "$unresolved_runners" "$runners" "${#marked[@]}" "${#unreadable[@]}" "$found" "$terminated" "$killed" "$remaining"
`

// fcE2BTaskStopReceipt is the script's report for one sandbox. Unreadable
// counts processes whose environment could not be read and so could not be
// proven to be anyone's.
type fcE2BTaskStopReceipt struct {
	Quiescent         bool   `json:"quiescent"`
	UnresolvedRunners int    `json:"unresolved_runners"`
	Version           int    `json:"version"`
	Runners           int    `json:"runners"`
	Marked            int    `json:"marked"`
	Unreadable        int    `json:"unreadable"`
	Found             int    `json:"found"`
	Terminated        int    `json:"terminated"`
	Killed            int    `json:"killed"`
	Remaining         int    `json:"remaining"`
	Error             string `json:"error"`
}

// fcE2BTaskStopArgs ends the task's processes as root with the same loader
// hardening as the root runner launch.
func fcE2BTaskStopArgs(sandboxID, runtimeID string, healthPort int, taskID string) []string {
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
		"bash", "-c", fcE2BTaskStopScript, "fc-e2b-task-stop",
		runtimeID, strconv.Itoa(healthPort), taskID,
	}
}

func parseFCE2BTaskStopReceipt(out string) (fcE2BTaskStopReceipt, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var receipt fcE2BTaskStopReceipt
	if err := json.Unmarshal([]byte(strings.TrimSpace(lines[len(lines)-1])), &receipt); err != nil || (receipt.Version != 3 && receipt.Version != 4) {
		return fcE2BTaskStopReceipt{}, errors.New("FC/E2B task stop returned no receipt")
	}
	if receipt.Error != "" {
		return receipt, fmt.Errorf("FC/E2B task stop refused: %s", receipt.Error)
	}
	return receipt, nil
}

// scheduleAbortedTaskStop starts ending a cancelled or failed task's
// processes off the transition path and reports whether it did. The event and
// the metrics path both notify for one terminal write; only one stop runs per
// task at a time.
//
// One stop is one operation: both passes run under the configuration
// snapshot in force when the task ended. Switching runtime.fc_e2b_sdk_rollout
// meanwhile applies to stops scheduled later, so a stop it selected still
// runs its second pass after a switch-off, and a stop it did not select is
// never started.
func (l *FCE2BLauncher) scheduleAbortedTaskStop(task db.AgentTaskQueue) bool {
	if l == nil || l.Pool == nil || !fcE2BTaskAborted(task.Status) || !task.ID.Valid || !task.RuntimeID.Valid {
		return false
	}
	frozen := l.withCurrentConfig()
	// The task's workspace is known only once its runtime is read, so a
	// workspace list may still select a task that its agent and runtime do
	// not.
	rollout := frozen.Config.SDKRollout
	scope := FCE2BScope{AgentID: pgFCE2BScopeID(task.AgentID), RuntimeID: pgFCE2BScopeID(task.RuntimeID)}
	if !frozen.Config.Enabled || !(fcE2BRolloutSelects(rollout, scope) || rollout.Enabled && len(rollout.WorkspaceIDs) > 0 || taskProcessStopPending(task)) {
		return false
	}
	taskKey := util.UUIDToString(task.ID)
	if _, inFlight := fcE2BTaskStopPending.LoadOrStore(taskKey, struct{}{}); inFlight {
		return false
	}
	if fcE2BTaskStopPendingCount.Add(1) > fcE2BTaskStopMaxPending {
		fcE2BTaskStopPendingCount.Add(-1)
		fcE2BTaskStopPending.Delete(taskKey)
		slog.Warn("FC/E2B task processes not stopped", "event", "fc_e2b_task_processes_stopped", "task_id", taskKey, "outcome", "backlog_full")
		return false
	}
	stopPass := l.stopPass
	if stopPass == nil {
		stopPass = func(ctx context.Context, frozen *FCE2BLauncher, taskID pgtype.UUID, pass int) bool {
			return frozen.stopAbortedTaskProcesses(ctx, taskID, pass)
		}
	}
	ended := time.Now()
	go func() {
		defer func() {
			fcE2BTaskStopPendingCount.Add(-1)
			fcE2BTaskStopPending.Delete(taskKey)
		}()
		defer func() {
			if r := recover(); r != nil {
				slog.Error("FC/E2B task stop panicked", "task_id", taskKey, "recovered", r)
			}
		}()
		ctx, cancel := context.WithTimeout(context.Background(), fcE2BTaskStopSecondPass+2*fcE2BTaskStopBudget)
		defer cancel()
		sleep := sleepWithContext
		if l.sleep != nil {
			sleep = l.sleep
		}
		for pass, at := range []time.Time{ended, ended.Add(fcE2BTaskStopSecondPass)} {
			if err := sleep(ctx, max(time.Until(at), 0)); err != nil {
				return
			}
			more, ran := func() (bool, bool) {
				select {
				case fcE2BTaskStopSlots <- struct{}{}:
					defer func() { <-fcE2BTaskStopSlots }()
				case <-ctx.Done():
					return false, false
				}
				return stopPass(ctx, frozen, task.ID, pass+1), true
			}()
			if !ran {
				slog.Warn("FC/E2B task stop skipped: stop slots busy", "event", "fc_e2b_task_processes_stopped", "task_id", taskKey)
				return
			}
			if !more {
				return
			}
		}
	}()
	return true
}

// stopAbortedTaskProcesses ends the task's processes in every sandbox its
// start attempts used. It rereads the task: only an aborted task on an FC/E2B
// runtime is stopped. It reports whether another pass may find more: false
// when the task is not one to stop or used no sandbox.
func (l *FCE2BLauncher) stopAbortedTaskProcesses(ctx context.Context, taskID pgtype.UUID, pass int) bool {
	if l == nil || l.Queries == nil || l.Pool == nil || !l.Config.Enabled {
		return false
	}
	task, err := l.Queries.GetAgentTask(ctx, taskID)
	if err != nil {
		return !errors.Is(err, pgx.ErrNoRows)
	}
	if !fcE2BTaskAborted(task.Status) {
		return false
	}
	runtime, err := l.Queries.GetAgentRuntime(ctx, task.RuntimeID)
	if err != nil {
		return !errors.Is(err, pgx.ErrNoRows)
	}
	if !IsFCE2BRuntime(runtime) {
		return false
	}
	conn, err := l.Pool.Acquire(ctx)
	if err != nil {
		slog.Warn("FC/E2B task stop could not acquire a connection", "task_id", util.UUIDToString(taskID), "error", err)
		return true
	}
	sandboxes, err := taskSandboxIDs(ctx, conn, task.ID)
	conn.Release()
	if err != nil {
		slog.Warn("FC/E2B task stop could not list sandboxes", "task_id", util.UUIDToString(taskID), "error", err)
		return true
	}
	confirmed := len(sandboxes) > 0 && pass >= 2
	for _, sandboxID := range sandboxes {
		receipt, stopErr := l.stopTaskProcessesInSandbox(ctx, task, runtime, sandboxID, pass)
		confirmed = confirmed && stopErr == nil && receipt.Version == 4 && receipt.Quiescent && receipt.Remaining == 0 && receipt.UnresolvedRunners == 0
	}
	// A server-side receipt is also positive exit proof for older sandbox
	// daemons. Never infer it from an empty, disabled or failed stop response.
	if confirmed && taskProcessStopPending(task) && l.Tasks != nil {
		if err := l.Tasks.AcknowledgeTaskProcessStopped(ctx, task.ID); err != nil {
			slog.Warn("FC/E2B process-stop acknowledgement failed", "task_id", util.UUIDToString(task.ID), "error", err)
		}
	}
	return len(sandboxes) > 0
}

// stopTaskProcessesInSandbox runs the stop script in one sandbox and logs the
// receipt. runtime.fc_e2b_sdk_rollout must select the task's scope; the
// command then carries that scope, so it also takes the SDK transport.
func (l *FCE2BLauncher) stopTaskProcessesInSandbox(ctx context.Context, task db.AgentTaskQueue, rt db.AgentRuntime, sandboxID string, pass int) (fcE2BTaskStopReceipt, error) {
	attrs := []any{"event", "fc_e2b_task_processes_stopped", "task_id", util.UUIDToString(task.ID),
		"agent_id", util.UUIDToString(task.AgentID), "runtime_id", util.UUIDToString(task.RuntimeID), "sandbox_id", sandboxID, "pass", pass}
	if !fcE2BAPISandboxIDPattern.MatchString(sandboxID) {
		err := errors.New("invalid FC/E2B sandbox id")
		slog.Warn("FC/E2B task processes not stopped", append(attrs, "error", err.Error())...)
		return fcE2BTaskStopReceipt{}, err
	}
	scope := FCE2BScope{
		WorkspaceID: pgFCE2BScopeID(rt.WorkspaceID),
		AgentID:     pgFCE2BScopeID(task.AgentID),
		RuntimeID:   pgFCE2BScopeID(task.RuntimeID),
	}
	if !fcE2BRolloutSelects(l.Config.SDKRollout, scope) && !taskProcessStopPending(task) {
		slog.Info("FC/E2B task processes not stopped", append(attrs, "outcome", "disabled")...)
		return fcE2BTaskStopReceipt{}, nil
	}
	ctx = WithFCE2BScope(ctx, scope)
	started := time.Now()
	args := fcE2BTaskStopArgs(sandboxID, util.UUIDToString(task.RuntimeID), fcE2BHealthPortForTask(task.ID), util.UUIDToString(task.ID))
	out, err := l.runE2BCommandWithTimeout(ctx, fcE2BTaskStopExecTimeout, args)
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
	attrs = append(attrs, "runners", receipt.Runners, "marked", receipt.Marked, "unreadable", receipt.Unreadable, "found", receipt.Found,
		"terminated", receipt.Terminated, "killed", receipt.Killed, "remaining", receipt.Remaining)
	if receipt.Remaining > 0 {
		slog.Warn("FC/E2B task processes stopped", append(attrs, "outcome", "survivors")...)
		return receipt, nil
	}
	slog.Info("FC/E2B task processes stopped", append(attrs, "outcome", "ok")...)
	return receipt, nil
}

// fcE2BSandboxMissing reports a sandbox that no longer exists. The SDK
// reports it as a typed error; the CLI only as text, where a bare "404" can
// come from anywhere in the output, so the text must also say "not found".
func fcE2BSandboxMissing(err error) bool {
	var sandboxNotFound *e2b.SandboxNotFoundError
	var notFound *e2b.NotFoundError
	if errors.As(err, &sandboxNotFound) || errors.As(err, &notFound) || errors.Is(err, errFCE2BSandboxGone) {
		return true
	}
	var sdkErr *fcE2BSDKError
	if errors.As(err, &sdkErr) {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "sandbox not found") || strings.Contains(text, "404") && strings.Contains(text, "not found")
}
