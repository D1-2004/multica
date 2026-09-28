package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func writeFixture(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fakeProcess(t *testing.T, root string, pid, parent int, command string, rssKB int) {
	t.Helper()
	dir := filepath.Join(root, strconv.Itoa(pid))
	writeFixture(t, filepath.Join(dir, "stat"), strconv.Itoa(pid)+" ("+command+") S "+strconv.Itoa(parent)+" 1 1 0 -1\n")
	writeFixture(t, filepath.Join(dir, "status"), "Name:\t"+command+"\nVmHWM:\t  900000 kB\nVmRSS:\t  "+strconv.Itoa(rssKB)+" kB\n")
}

func TestProcessMemorySourceSeparatesCLIChildren(t *testing.T) {
	proc, cgroup := t.TempDir(), t.TempDir()
	fakeProcess(t, proc, 100, 1, "server", 600000)
	fakeProcess(t, proc, 201, 100, "node", 140000)
	fakeProcess(t, proc, 202, 100, "node", 130000)
	fakeProcess(t, proc, 203, 100, "dws", 20000)
	fakeProcess(t, proc, 300, 1, "next-server (v1", 400000)
	fakeProcess(t, proc, 301, 300, "node", 50000)
	writeFixture(t, filepath.Join(cgroup, "memory.current"), "2000000000\n")
	writeFixture(t, filepath.Join(cgroup, "memory.peak"), "2500000000\n")
	writeFixture(t, filepath.Join(cgroup, "memory.stat"), "anon 1500000000\ninactive_file 300000000\n")

	point := processMemorySource{procRoot: proc, cgroupRoot: cgroup, pid: 100}.sample()
	if point.serverRSSKB != 600000 || point.serverHWMKB != 900000 {
		t.Fatalf("server = %d/%d", point.serverRSSKB, point.serverHWMKB)
	}
	// Only direct children of the server count; next-server's node does not.
	if len(point.nodeChildren) != 2 || len(point.otherChildren) != 1 {
		t.Fatalf("children node=%v other=%v", point.nodeChildren, point.otherChildren)
	}
	if point.containerUsage != 2000000000 || point.containerWorking != 1700000000 || point.containerPeakSeen != 2500000000 {
		t.Fatalf("cgroup = %d/%d/%d", point.containerUsage, point.containerWorking, point.containerPeakSeen)
	}

	aggregator := newProcessMemoryAggregator()
	aggregator.add(point)
	// The first CLI child exits and a new one starts.
	delete(point.nodeChildren, 201)
	point.nodeChildren[204] = 150000
	point.containerWorking = 1600000000
	aggregator.add(point)
	window := aggregator.flush()
	if window.samples != 2 || window.nodeChildrenMax != 2 || window.nodeStarted != 3 || window.nodeChildrenRSSKBMax != 280000 ||
		window.otherStarted != 1 || window.containerWorkingMax != 1700000000 || window.containerWorkingLast != 1600000000 {
		t.Fatalf("window = %#v", window)
	}
	// A child still alive in the next window is not counted as started again.
	aggregator.add(point)
	if next := aggregator.flush(); next.nodeStarted != 0 || next.nodeChildrenMax != 2 {
		t.Fatalf("next window = %#v", next)
	}
}

func TestReadCgroupMemoryV1AndMissing(t *testing.T) {
	cgroup := t.TempDir()
	writeFixture(t, filepath.Join(cgroup, "memory", "memory.usage_in_bytes"), "1000\n")
	writeFixture(t, filepath.Join(cgroup, "memory", "memory.max_usage_in_bytes"), "1500\n")
	writeFixture(t, filepath.Join(cgroup, "memory", "memory.stat"), "total_inactive_file 200\n")
	if usage, working, peak := readCgroupMemory(cgroup); usage != 1000 || working != 800 || peak != 1500 {
		t.Fatalf("v1 = %d/%d/%d", usage, working, peak)
	}
	if usage, working, peak := readCgroupMemory(t.TempDir()); usage != -1 || working != -1 || peak != -1 {
		t.Fatalf("missing = %d/%d/%d", usage, working, peak)
	}
}

func TestProcessMemorySampleWindowIsOffByDefault(t *testing.T) {
	t.Setenv(processMemorySampleEnv, "")
	if _, ok := processMemorySampleWindow(); ok {
		t.Fatal("sampler must be off without configuration")
	}
	t.Setenv(processMemorySampleEnv, "250ms")
	if _, ok := processMemorySampleWindow(); ok {
		t.Fatal("sub-second windows are rejected")
	}
	t.Setenv(processMemorySampleEnv, "15s")
	if window, ok := processMemorySampleWindow(); !ok || window.Seconds() != 15 {
		t.Fatalf("window = %v, %v", window, ok)
	}
}
