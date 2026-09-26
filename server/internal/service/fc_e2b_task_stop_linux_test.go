//go:build linux

package service

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/google/uuid"
)

// The script runs for real, through the same `/bin/bash -l -c` wrapper both
// transports use, against the process shapes cancellation left behind: tool
// commands in their own process group or session, a child that cleared its
// environment, an orphan re-parented to init, and one that ignores SIGTERM.
func TestFCE2BTaskStopScriptEndsOnlyTheTaskProcessTree(t *testing.T) {
	for _, tool := range []string{"/bin/bash", "setsid", "env", "sleep"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is unavailable", tool)
		}
	}
	task, other := uuid.New().String(), uuid.New().String()
	base := 9000 + os.Getpid()%500*10
	sleeper := func(i int) string { return "sleep " + strconv.Itoa(base+i) }
	fixture := strings.Join([]string{
		// Marked parent with an inheriting child in its group.
		fmt.Sprintf(`env MULTICA_TASK_ID=%s bash -c '%s & exec %s' &`, task, sleeper(1), sleeper(2)),
		// A child in a new session that cleared its environment.
		fmt.Sprintf(`env FC_E2B_TASK_ID=%s bash -c 'setsid env -i %s & exec %s' &`, task, sleeper(3), sleeper(4)),
		// A marked orphan in its own session.
		fmt.Sprintf(`env FC_E2B_TASK_ID=%s bash -c 'setsid %s & exit 0'`, task, sleeper(5)),
		// A marked session leader with an unmarked member.
		fmt.Sprintf(`env FC_E2B_TASK_ID=%s setsid bash -c 'env -i %s & wait' &`, task, sleeper(6)),
		// A marked process that ignores SIGTERM.
		fmt.Sprintf(`env FC_E2B_TASK_ID=%s bash -c 'trap "" TERM; exec %s' &`, task, sleeper(7)),
		// Another task, an unrelated process and a near-miss marker survive.
		fmt.Sprintf(`env FC_E2B_TASK_ID=%s %s &`, other, sleeper(8)),
		sleeper(9) + " &",
		fmt.Sprintf(`env MULTICA_TASK_ID=%s0 %s &`, task, sleeper(10)),
		"sleep 1",
	}, "\n")
	// livePIDs lists running processes whose command line is exactly cmd.
	livePIDs := func(cmd string) []int {
		entries, _ := os.ReadDir("/proc")
		var pids []int
		for _, entry := range entries {
			pid, err := strconv.Atoi(entry.Name())
			if err != nil {
				continue
			}
			line, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
			if err != nil || strings.TrimRight(strings.ReplaceAll(string(line), "\x00", " "), " ") != cmd {
				continue
			}
			stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
			if err != nil {
				continue
			}
			if fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:])); len(fields) > 0 && fields[0] != "Z" {
				pids = append(pids, pid)
			}
		}
		return pids
	}
	alive := func(i int) bool { return len(livePIDs(sleeper(i))) > 0 }
	defer func() {
		for i := 1; i <= 10; i++ {
			for _, pid := range livePIDs(sleeper(i)) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}()
	if err := exec.Command("/bin/bash", "-c", fixture).Run(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	for i := 1; i <= 10; i++ {
		if !alive(i) {
			t.Fatalf("fixture %d did not start", i)
		}
	}
	operation, err := parseFCE2BOperation(fcE2BTaskStopArgs("sbx_123", task))
	if err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("/bin/bash", "-l", "-c", operation.exec.Command).Output()
	if err != nil {
		t.Fatalf("stop script: %v", err)
	}
	receipt, err := parseFCE2BTaskStopReceipt(string(out))
	if err != nil || receipt.Found != 8 || receipt.Killed != 1 || receipt.Remaining != 0 || receipt.Terminated != 7 {
		t.Fatalf("receipt = %#v, %v (%s)", receipt, err, out)
	}
	time.Sleep(300 * time.Millisecond)
	for i := 1; i <= 10; i++ {
		if want := i >= 8; alive(i) != want {
			t.Fatalf("process %d alive=%v, want %v", i, !want, want)
		}
	}
}
