//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The script runs for real, through the same `/bin/bash -l -c` wrapper both
// transports use, against two tasks sharing a sandbox. The processes live in
// a session whose live leader is not the task's, as the runner does under
// envd. Run as root, the processes belong to another user, so the task marker
// is read the way FC sandboxes allow: under that user's uid, without
// CAP_SYS_PTRACE.
func TestFCE2BTaskStopScriptEndsOnlyTheTaskProcesses(t *testing.T) {
	tools := []string{"/bin/bash", "setsid", "sleep", "env", "sh"}
	if os.Geteuid() == 0 {
		// Root reads other users' environments only through setpriv; without
		// it every marker is unreadable and nothing is proven.
		tools = append(tools, "setpriv")
	}
	for _, tool := range tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is unavailable", tool)
		}
	}
	runtimeID, otherRuntimeID := uuid.New().String(), uuid.New().String()
	taskID, otherTaskID := uuid.New().String(), uuid.New().String()
	port, otherPort := 20000+os.Getpid()%9000, 30000+os.Getpid()%9000
	base := 9000 + os.Getpid()%400*10
	n := func(i int) int { return base + i }
	dir, err := os.MkdirTemp("", "fc-e2b-task-stop-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	if err := os.Chmod(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(dir, "fixture.sh")
	if err := os.WriteFile(fixture, []byte(fmt.Sprintf(`case $1 in
target)
  # A child, a tool session with a member, and orphans in their own sessions:
  # marked by the inherited task id, with MULTICA_TASK_ID blanked as for A2A
  # children, and one that cleared its environment.
  sleep %[2]d &
  setsid bash -c 'sleep %[3]d & exec sleep %[4]d' &
  setsid bash -c 'sleep %[5]d &'
  MULTICA_TASK_ID= setsid bash -c 'sleep %[10]d &'
  env -i PATH="$PATH" setsid sh -c 'sleep %[11]d &'
  # Cleared orphans left in a tool session, its leader alive and gone.
  setsid bash -c 'env -i PATH="$PATH" sh -c "sleep %[12]d &"; exec sleep %[13]d' &
  setsid bash -c 'env -i PATH="$PATH" sh -c "sleep %[14]d &"; sleep %[15]d &'
  wait;;
stubborn)
  trap '' TERM
  sleep %[8]d &
  wait;;
other)
  # PRI-61: another task's orphan, started after this task's runner.
  sleep %[9]d &
  setsid bash -c 'sleep %[6]d &'
  wait;;
collide)
  sleep %[16]d &
  wait;;
service)
  setsid bash -c '(exec -a /usr/local/libexec/multica-provider-http-proxy sleep %[17]d) &';;
esac
`, 0, n(2), n(3), n(4), n(5), n(6), 0, n(8), n(9), n(10), n(11), n(12), n(13), n(14), n(15), n(16), n(17))), 0o644); err != nil {
		t.Fatal(err)
	}
	mine := fmt.Sprintf("MULTICA_TASK_ID=%s FC_E2B_TASK_ID=%s", taskID, taskID)
	theirs := fmt.Sprintf("MULTICA_TASK_ID=%s FC_E2B_TASK_ID=%s", otherTaskID, otherTaskID)
	runner := func(env, role, runtimeID string, port int) string {
		return fmt.Sprintf("env %s bash %s %s --runtime-id %s --provider pi --health-port %d &", env, fixture, role, runtimeID, port)
	}
	// A runner on the task's runtime and port whose environment cannot be
	// read, not even under its own uid: a non-dumpable process. It may be
	// another task's on a colliding port, so it is not proven.
	unreadableMarker := fmt.Sprintf("time.sleep(%d)", n(18))
	unreadableRunner := ""
	if _, err := exec.LookPath("python3"); err == nil {
		unreadableRunner = fmt.Sprintf("env %s python3 -c 'import ctypes, time; ctypes.CDLL(None).prctl(4, 0, 0, 0, 0); %s' --runtime-id %s --provider pi --health-port %d &",
			theirs, unreadableMarker, runtimeID, port)
	}
	harness := exec.Command("/bin/bash", "-c", strings.Join([]string{
		// An orphan without a task id, left before the runners.
		fmt.Sprintf("setsid bash -c 'sleep %d &'", n(1)),
		runner(mine, "target", runtimeID, port),
		// A second process of the runner that ignores SIGTERM.
		runner(mine, "stubborn", runtimeID, port),
		runner(theirs, "other", otherRuntimeID, otherPort),
		// Another task's runner on a colliding health port.
		runner(theirs, "collide", runtimeID, port),
		unreadableRunner,
		// A shared sandbox service that still carries this task's id.
		fmt.Sprintf("env %s bash %s service", mine, fixture),
		fmt.Sprintf("sleep %d &", n(7)),
		fmt.Sprintf("exec sleep %d", n(0)),
	}, "\n"))
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "MULTICA_TASK_ID=") && !strings.HasPrefix(kv, "FC_E2B_TASK_ID=") {
			harness.Env = append(harness.Env, kv)
		}
	}
	harness.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if os.Geteuid() == 0 {
		harness.SysProcAttr.Credential = &syscall.Credential{Uid: 65534, Gid: 65534}
	}
	// processes lists running processes whose command line matches.
	processes := func(match func(argv []string) bool) []int {
		entries, _ := os.ReadDir("/proc")
		var pids []int
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			line, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
			if err != nil || !match(strings.Split(strings.TrimRight(string(line), "\x00"), "\x00")) {
				continue
			}
			stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if err != nil {
				continue
			}
			if fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:])); len(fields) > 1 && fields[0] != "Z" {
				pids = append(pids, pid)
			}
		}
		return pids
	}
	// livePIDs lists running processes started as `<prog> <arg>`.
	livePIDs := func(arg int) []int {
		return processes(func(argv []string) bool { return len(argv) == 2 && argv[1] == strconv.Itoa(arg) })
	}
	unreadablePIDs := func() []int {
		return processes(func(argv []string) bool { return len(argv) > 2 && strings.Contains(argv[2], unreadableMarker) })
	}
	alive := func(i int) bool { return len(livePIDs(n(i))) > 0 }
	all := []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17}
	defer func() {
		for _, i := range all {
			for _, pid := range livePIDs(n(i)) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
		for _, pid := range unreadablePIDs() {
			_ = syscall.Kill(pid, syscall.SIGKILL)
		}
		_ = harness.Wait()
	}()
	if err := harness.Start(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		started := 0
		for _, i := range all {
			if alive(i) {
				started++
			}
		}
		if started == len(all) && (unreadableRunner == "" || len(unreadablePIDs()) > 0) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("fixture started %d of %d processes", started, len(all))
		}
	}
	// Let the short-lived session leaders exit.
	time.Sleep(500 * time.Millisecond)
	operation, err := parseFCE2BOperation(fcE2BTaskStopArgs("sbx_123", runtimeID, port, taskID))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/bash", "-l", "-c", operation.exec.Command).Output()
	if err != nil {
		t.Fatalf("stop script: %v (%s)", err, out)
	}
	receipt, err := parseFCE2BTaskStopReceipt(string(out))
	if err != nil || receipt.Runners != 2 || receipt.Remaining != 0 || receipt.Killed != 2 {
		t.Fatalf("receipt = %#v, %v (%s)", receipt, err, out)
	}
	time.Sleep(300 * time.Millisecond)
	survive := map[int]bool{
		0: true, 1: true, 7: true, // the session leader and processes with no task id
		6: true, 9: true, 16: true, // another task's orphan, child and colliding runner
		11: true, // this task's orphan that cleared its environment: unprovable
		17: true, // a shared service outside the runner's tree
	}
	for _, i := range all {
		if alive(i) != survive[i] {
			t.Errorf("process %d alive=%v, want %v", i, alive(i), survive[i])
		}
	}
	if unreadableRunner == "" {
		t.Log("python3 is unavailable: the unreadable runner was not exercised")
	} else if len(unreadablePIDs()) == 0 {
		t.Error("a runner whose environment could not be read was ended")
	} else if receipt.Quiescent || receipt.UnresolvedRunners == 0 {
		t.Error("an unreadable matching runner must hold the steer exit barrier")
	}
	t.Logf("receipt as uid %d: %s", os.Geteuid(), strings.TrimSpace(string(out)))
}
