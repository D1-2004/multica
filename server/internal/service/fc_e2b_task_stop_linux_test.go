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
	for _, tool := range []string{"/bin/bash", "setsid", "sleep", "getconf"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("%s is unavailable", tool)
		}
	}
	runtimeID := uuid.New().String()
	port, otherPort := 20000+os.Getpid()%9000, 30000+os.Getpid()%9000
	base := 9000 + os.Getpid()%500*10
	sleeper := func(i int) string { return "sleep " + strconv.Itoa(base+i) }
	runner := func(port int, body string) string {
		return fmt.Sprintf(`bash -c '%s' fake-runner --runtime-id %s --provider pi --health-port %d &`, body, runtimeID, port)
	}
	fixture := strings.Join([]string{
		// An orphan left before this task's runner started survives.
		fmt.Sprintf(`setsid bash -c '%s &'`, sleeper(1)),
		"sleep 1.2",
		// This task's runner: a child, a tool session with a member, and an
		// orphan it leaves in its own session.
		runner(port, fmt.Sprintf(`%s & setsid bash -c "%s & exec %s" & setsid bash -c "%s &"; wait`, sleeper(2), sleeper(3), sleeper(4), sleeper(5))),
		// A second process of the same runner that ignores SIGTERM.
		runner(port, fmt.Sprintf(`trap "" TERM; %s & wait`, sleeper(8))),
		// Another task's runner and an unrelated process survive.
		runner(otherPort, fmt.Sprintf(`%s & wait`, sleeper(6))),
		sleeper(7) + " &",
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
			if fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:])); len(fields) > 1 && fields[0] != "Z" {
				pids = append(pids, pid)
			}
		}
		return pids
	}
	parentOf := func(pid int) string {
		stat, _ := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if fields := strings.Fields(string(stat[strings.LastIndexByte(string(stat), ')')+1:])); len(fields) > 1 {
			return fields[1]
		}
		return ""
	}
	alive := func(i int) bool { return len(livePIDs(sleeper(i))) > 0 }
	defer func() {
		for i := 1; i <= 8; i++ {
			for _, pid := range livePIDs(sleeper(i)) {
				_ = syscall.Kill(pid, syscall.SIGKILL)
			}
		}
	}()
	since := time.Now()
	if err := exec.Command("/bin/bash", "-c", fixture).Run(); err != nil {
		t.Fatalf("start fixture: %v", err)
	}
	for i := 1; i <= 8; i++ {
		if !alive(i) {
			t.Fatalf("fixture %d did not start", i)
		}
	}
	// Orphans reach PID 1 only without a subreaper; otherwise skip that case.
	orphanAdopted := len(livePIDs(sleeper(5))) == 1 && parentOf(livePIDs(sleeper(5))[0]) == "1"
	operation, err := parseFCE2BOperation(fcE2BTaskStopArgs("sbx_123", runtimeID, port, since))
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
	for i, want := range map[int]bool{1: true, 2: false, 3: false, 4: false, 6: true, 7: true, 8: false} {
		if alive(i) != want {
			t.Fatalf("process %d alive=%v, want %v (%s)", i, !want, want, out)
		}
	}
	if orphanAdopted && alive(5) {
		t.Fatalf("the task's orphan survived (%s)", out)
	}
}
