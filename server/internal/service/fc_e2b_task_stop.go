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
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

// A cancelled task stops only its own runner: the in-sandbox daemon ends the
// agent, but agent tools start their commands in new sessions, and those
// survived cancellation until the sandbox expired or, worse, kept running
// into the next task that reused it (PRI-52). The server therefore ends every
// process of the cancelled task inside each sandbox the task used, through
// the same FC/E2B transport as every other command, so the CLI and the SDK
// behave alike.
//
// FC sandboxes deny reading another process's environment even to root (no
// CAP_SYS_PTRACE), so a task is recognised by what /proc/<pid>/stat and
// /proc/<pid>/cmdline expose: its runner carries the task's runtime id and
// per-task health port on the command line.
const (
	// The in-sandbox daemon notices a cancellation within seconds and then
	// tears its tree down, orphaning tool commands. The first pass runs at
	// once to catch the tree intact; the second catches what was forked or
	// orphaned in between.
	fcE2BTaskStopSecondPass  = 10 * time.Second
	fcE2BTaskStopExecTimeout = 45 * time.Second
	fcE2BTaskStopBudget      = 2 * time.Minute
	fcE2BTaskStopConcurrency = 4
	// fcE2BTaskStopSinceSlack widens the orphan window when the runner is
	// already gone and only the server's start time is known.
	fcE2BTaskStopSinceSlack = 5 * time.Second
)

var (
	fcE2BTaskStopSlots   = make(chan struct{}, fcE2BTaskStopConcurrency)
	fcE2BTaskStopPending sync.Map
)

// fcE2BTaskStopScript ends one task's processes in the sandbox. It takes the
// runtime id, the task's health port and a start time (epoch seconds). The
// task's processes are:
//   - its runner: the command lines carrying both --runtime-id <id> and
//     --health-port <port>, and every descendant of those;
//   - every member of a session or process group one of those leads, other
//     than session and group 1, where envd, the runner and shared services
//     live;
//   - orphans re-parented to PID 1 outside session 1 that started after the
//     runner (or, without a runner, after the given start time), except
//     sandbox services under /usr/local/libexec/multica-* and /.fce2b/.
//
// The tree is frozen with SIGSTOP and rescanned before it is ended, so no
// process escapes by forking or re-parenting meanwhile; then SIGTERM with
// SIGCONT, and SIGKILL after a grace. The last line is a JSON receipt. It
// runs as root under bash (mapfile, associative arrays).
const fcE2BTaskStopScript = `rt=$1 port=$2 since=$3
if [[ ! $rt =~ ^[0-9a-f-]{36}$ || ! $port =~ ^[0-9]{1,5}$ || ! $since =~ ^[0-9]{1,12}$ ]]; then
  echo '{"version":2,"error":"invalid arguments"}'; exit 2
fi
self=$$ btime=0
while read -r key value _; do [[ $key == btime ]] && btime=$value; done </proc/stat
hz=$(getconf CLK_TCK 2>/dev/null) || hz=100
declare -A ppid=() pgid=() sid=() start=() runner=() infra=() target=() orphan=()
scan() {
  ppid=() pgid=() sid=() start=() runner=() infra=()
  local d p raw f argv i has_rt has_port
  for d in /proc/[0-9]*; do
    p=${d#/proc/}
    [[ $p == 1 || $p == "$self" ]] && continue
    { read -r raw <"$d/stat"; } 2>/dev/null || continue
    read -r -a f <<<"${raw##*) }"
    [[ ${f[0]} == Z ]] && continue
    ppid[$p]=${f[1]} pgid[$p]=${f[2]} sid[$p]=${f[3]} start[$p]=$(( btime + f[19] / hz ))
    argv=()
    { mapfile -d '' -t argv <"$d/cmdline"; } 2>/dev/null
    has_rt=0 has_port=0
    for ((i = 0; i + 1 < ${#argv[@]}; i++)); do
      [[ ${argv[i]} == --runtime-id && ${argv[i+1]} == "$rt" ]] && has_rt=1
      [[ ${argv[i]} == --health-port && ${argv[i+1]} == "$port" ]] && has_port=1
    done
    (( has_rt && has_port )) && runner[$p]=1
    case " ${argv[0]:-} ${argv[1]:-}" in *" /usr/local/libexec/multica-"*|*" /.fce2b/"*) infra[$p]=1;; esac
  done
}
collect() {
  local p since_eff=$since changed=1 groups sessions
  for p in "${!runner[@]}"; do target[$p]=1; done
  if (( ${#runner[@]} > 0 )); then
    since_eff=
    for p in "${!runner[@]}"; do [[ -z $since_eff ]] || (( start[$p] < since_eff )) && since_eff=${start[$p]}; done
  fi
  for p in "${!ppid[@]}"; do
    if [[ ${ppid[$p]} == 1 && ${sid[$p]} != 1 && -z ${infra[$p]:-} ]] && (( start[$p] >= since_eff )); then
      target[$p]=1 orphan[$p]=1
    fi
  done
  while (( changed )); do
    changed=0 groups=" " sessions=" "
    for p in "${!target[@]}"; do
      [[ -n ${ppid[$p]:-} ]] || continue
      [[ ${sid[$p]} != 1 ]] && sessions+="${sid[$p]} "
      [[ ${pgid[$p]} != 1 ]] && groups+="${pgid[$p]} "
    done
    for p in "${!ppid[@]}"; do
      [[ -n ${target[$p]:-} ]] && continue
      if [[ -n ${target[${ppid[$p]}]:-} || $sessions == *" ${sid[$p]} "* || $groups == *" ${pgid[$p]} "* ]]; then
        target[$p]=1 changed=1
      fi
    done
  done
  unset "target[$self]" "target[1]"
}
alive() { local raw; { read -r raw <"/proc/$1/stat"; } 2>/dev/null || return 1; raw=${raw##*) }; [[ ${raw%% *} != Z ]]; }
survivors() { local p; for p in "${!target[@]}"; do alive "$p" && printf '%s ' "$p"; done; }
count() { set -- $1; echo $#; }
scan; collect
found=${#target[@]} runners=${#runner[@]} terminated=0 killed=0 remaining=0
if (( found > 0 )); then
  kill -STOP "${!target[@]}" 2>/dev/null
  scan; collect
  kill -STOP "${!target[@]}" 2>/dev/null
  found=${#target[@]}
  kill -TERM "${!target[@]}" 2>/dev/null
  kill -CONT "${!target[@]}" 2>/dev/null
  for ((i = 0; i < 20; i++)); do
    [[ -z $(survivors) ]] && break
    sleep 0.25
  done
  left=$(survivors)
  terminated=$(( found - $(count "$left") ))
  if [[ -n $left ]]; then
    kill -KILL $left 2>/dev/null
    for ((i = 0; i < 8; i++)); do
      [[ -z $(survivors) ]] && break
      sleep 0.25
    done
    remaining=$(count "$(survivors)")
    killed=$(( $(count "$left") - remaining ))
  fi
fi
printf '{"version":2,"runners":%d,"orphans":%d,"found":%d,"terminated":%d,"killed":%d,"remaining":%d}\n' "$runners" "${#orphan[@]}" "$found" "$terminated" "$killed" "$remaining"
`

// fcE2BTaskStopReceipt is the script's report for one sandbox.
type fcE2BTaskStopReceipt struct {
	Version    int    `json:"version"`
	Runners    int    `json:"runners"`
	Orphans    int    `json:"orphans"`
	Found      int    `json:"found"`
	Terminated int    `json:"terminated"`
	Killed     int    `json:"killed"`
	Remaining  int    `json:"remaining"`
	Error      string `json:"error"`
}

// fcE2BTaskStopArgs ends the task's processes as root with the same loader
// hardening as the root runner launch.
func fcE2BTaskStopArgs(sandboxID, runtimeID string, healthPort int, since time.Time) []string {
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
		runtimeID, strconv.Itoa(healthPort), strconv.FormatInt(since.Unix(), 10),
	}
}

func parseFCE2BTaskStopReceipt(out string) (fcE2BTaskStopReceipt, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	var receipt fcE2BTaskStopReceipt
	if err := json.Unmarshal([]byte(strings.TrimSpace(lines[len(lines)-1])), &receipt); err != nil || receipt.Version != 2 {
		return fcE2BTaskStopReceipt{}, errors.New("FC/E2B task stop returned no receipt")
	}
	if receipt.Error != "" {
		return receipt, fmt.Errorf("FC/E2B task stop refused: %s", receipt.Error)
	}
	return receipt, nil
}

// fcE2BTaskStopSince is the earliest time the task's processes can have
// started, used for orphans once the runner is gone.
func fcE2BTaskStopSince(task db.AgentTaskQueue) time.Time {
	for _, ts := range []pgtype.Timestamptz{task.DispatchedAt, task.StartedAt, task.CreatedAt} {
		if ts.Valid {
			return ts.Time.Add(-fcE2BTaskStopSinceSlack)
		}
	}
	return time.Now().Add(-fcE2BTaskStopSinceSlack)
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
		ctx, cancel := context.WithTimeout(context.Background(), fcE2BTaskStopSecondPass+2*fcE2BTaskStopBudget)
		defer cancel()
		sleep := sleepWithContext
		if l.sleep != nil {
			sleep = l.sleep
		}
		for pass, wait := range []time.Duration{0, fcE2BTaskStopSecondPass} {
			if err := sleep(ctx, wait); err != nil {
				return
			}
			select {
			case fcE2BTaskStopSlots <- struct{}{}:
			case <-ctx.Done():
				slog.Warn("FC/E2B task stop skipped: stop slots busy", "event", "fc_e2b_task_processes_stopped", "task_id", taskKey)
				return
			}
			l.withCurrentConfig().stopCancelledTaskProcesses(ctx, task.ID, pass+1)
			<-fcE2BTaskStopSlots
		}
	}()
}

// stopCancelledTaskProcesses ends the task's processes in every sandbox its
// start attempts used. It rereads the task: only a cancelled task is stopped.
func (l *FCE2BLauncher) stopCancelledTaskProcesses(ctx context.Context, taskID pgtype.UUID, pass int) {
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
		l.stopTaskProcessesInSandbox(ctx, task, runtime, sandboxID, pass)
	}
}

// stopTaskProcessesInSandbox runs the stop script in one sandbox and logs the
// receipt. The command carries the task's scope, so the SDK rollout applies.
func (l *FCE2BLauncher) stopTaskProcessesInSandbox(ctx context.Context, task db.AgentTaskQueue, rt db.AgentRuntime, sandboxID string, pass int) (fcE2BTaskStopReceipt, error) {
	attrs := []any{"event", "fc_e2b_task_processes_stopped", "task_id", util.UUIDToString(task.ID),
		"agent_id", util.UUIDToString(task.AgentID), "runtime_id", util.UUIDToString(task.RuntimeID), "sandbox_id", sandboxID, "pass", pass}
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
	args := fcE2BTaskStopArgs(sandboxID, util.UUIDToString(task.RuntimeID), fcE2BHealthPortForTask(task.ID), fcE2BTaskStopSince(task))
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
	attrs = append(attrs, "runners", receipt.Runners, "orphans", receipt.Orphans, "found", receipt.Found,
		"terminated", receipt.Terminated, "killed", receipt.Killed, "remaining", receipt.Remaining)
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
