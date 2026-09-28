package main

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime/metrics"
	"strconv"
	"strings"
	"time"
)

// processMemorySampleEnv enables a diagnostic log of the server's and its
// container's memory, used to compare the e2b CLI subprocess transport with
// the in-process SDK. Its value is the log window, e.g. "15s"; unset keeps it
// off.
const processMemorySampleEnv = "MULTICA_PROCESS_MEMORY_SAMPLE"

// processMemoryTick is short enough to see an e2b CLI child, which lives for
// at least the Node startup of several hundred milliseconds.
const processMemoryTick = 250 * time.Millisecond

type processMemorySource struct {
	procRoot   string
	cgroupRoot string
	pid        int
}

type processMemoryPoint struct {
	serverRSSKB int64
	serverHWMKB int64
	// Direct children of the server, split by command name: the e2b CLI runs
	// as "node"; other helpers such as the dws CLI are counted separately.
	nodeChildren      map[int]int64
	otherChildren     map[int]int64
	containerUsage    int64
	containerWorking  int64
	containerPeakSeen int64
}

func (s processMemorySource) sample() processMemoryPoint {
	point := processMemoryPoint{
		nodeChildren:  map[int]int64{},
		otherChildren: map[int]int64{},
	}
	status := readProcStatus(filepath.Join(s.procRoot, strconv.Itoa(s.pid), "status"))
	point.serverRSSKB, point.serverHWMKB = status["VmRSS"], status["VmHWM"]
	entries, _ := os.ReadDir(s.procRoot)
	for _, entry := range entries {
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid == s.pid {
			continue
		}
		command, parent, ok := readProcStat(filepath.Join(s.procRoot, entry.Name(), "stat"))
		if !ok || parent != s.pid {
			continue
		}
		rss := readProcStatus(filepath.Join(s.procRoot, entry.Name(), "status"))["VmRSS"]
		if command == "node" {
			point.nodeChildren[pid] = rss
		} else {
			point.otherChildren[pid] = rss
		}
	}
	point.containerUsage, point.containerWorking, point.containerPeakSeen = readCgroupMemory(s.cgroupRoot)
	return point
}

// readProcStatus returns the kB values of a /proc/<pid>/status file.
func readProcStatus(path string) map[string]int64 {
	values := map[string]int64{}
	file, err := os.Open(path)
	if err != nil {
		return values
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		key, rest, ok := strings.Cut(scanner.Text(), ":")
		if !ok {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			continue
		}
		if n, err := strconv.ParseInt(fields[0], 10, 64); err == nil {
			values[key] = n
		}
	}
	return values
}

// readProcStat returns the command name and parent PID from /proc/<pid>/stat.
func readProcStat(path string) (string, int, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", 0, false
	}
	text := string(raw)
	open, closing := strings.IndexByte(text, '('), strings.LastIndexByte(text, ')')
	if open < 0 || closing < open {
		return "", 0, false
	}
	fields := strings.Fields(text[closing+1:])
	if len(fields) < 2 {
		return "", 0, false
	}
	parent, err := strconv.Atoi(fields[1])
	if err != nil {
		return "", 0, false
	}
	return text[open+1 : closing], parent, true
}

// readCgroupMemory returns the container's memory usage, working set
// (usage minus inactive file cache) and recorded peak in bytes, for cgroup v2
// or v1. Unknown values are -1.
func readCgroupMemory(root string) (usage, working, peak int64) {
	usage, working, peak = -1, -1, -1
	inactiveKey := "inactive_file"
	usagePath, peakPath, statPath := filepath.Join(root, "memory.current"), filepath.Join(root, "memory.peak"), filepath.Join(root, "memory.stat")
	if _, err := os.Stat(usagePath); err != nil {
		v1 := filepath.Join(root, "memory")
		usagePath, peakPath, statPath = filepath.Join(v1, "memory.usage_in_bytes"), filepath.Join(v1, "memory.max_usage_in_bytes"), filepath.Join(v1, "memory.stat")
		inactiveKey = "total_inactive_file"
	}
	usage = readInt64File(usagePath)
	peak = readInt64File(peakPath)
	if usage >= 0 {
		working = usage
		if raw, err := os.ReadFile(statPath); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				fields := strings.Fields(line)
				if len(fields) == 2 && fields[0] == inactiveKey {
					if n, err := strconv.ParseInt(fields[1], 10, 64); err == nil && n <= usage {
						working = usage - n
					}
				}
			}
		}
	}
	return usage, working, peak
}

func readInt64File(path string) int64 {
	raw, err := os.ReadFile(path)
	if err != nil {
		return -1
	}
	n, err := strconv.ParseInt(strings.TrimSpace(string(raw)), 10, 64)
	if err != nil {
		return -1
	}
	return n
}

// processMemoryWindow aggregates the samples of one log window.
type processMemoryWindow struct {
	samples              int
	serverRSSKBMax       int64
	serverHWMKB          int64
	nodeChildrenMax      int
	nodeChildrenRSSKBMax int64
	otherChildrenMax     int
	nodeStarted          int
	otherStarted         int
	containerUsageMax    int64
	containerWorkingMax  int64
	containerWorkingLast int64
	containerPeak        int64
}

type processMemoryAggregator struct {
	window    processMemoryWindow
	seenNode  map[int]struct{}
	seenOther map[int]struct{}
}

func newProcessMemoryAggregator() *processMemoryAggregator {
	return &processMemoryAggregator{seenNode: map[int]struct{}{}, seenOther: map[int]struct{}{}}
}

func (a *processMemoryAggregator) add(point processMemoryPoint) {
	w := &a.window
	if w.samples == 0 {
		w.containerUsageMax, w.containerWorkingMax = -1, -1
	}
	w.samples++
	w.serverRSSKBMax = max(w.serverRSSKBMax, point.serverRSSKB)
	w.serverHWMKB = point.serverHWMKB
	w.nodeChildrenMax = max(w.nodeChildrenMax, len(point.nodeChildren))
	w.otherChildrenMax = max(w.otherChildrenMax, len(point.otherChildren))
	var nodeRSS int64
	for pid, rss := range point.nodeChildren {
		nodeRSS += rss
		if _, ok := a.seenNode[pid]; !ok {
			a.seenNode[pid] = struct{}{}
			w.nodeStarted++
		}
	}
	for pid := range point.otherChildren {
		if _, ok := a.seenOther[pid]; !ok {
			a.seenOther[pid] = struct{}{}
			w.otherStarted++
		}
	}
	w.nodeChildrenRSSKBMax = max(w.nodeChildrenRSSKBMax, nodeRSS)
	w.containerUsageMax = max(w.containerUsageMax, point.containerUsage)
	w.containerWorkingMax = max(w.containerWorkingMax, point.containerWorking)
	w.containerWorkingLast = point.containerWorking
	w.containerPeak = point.containerPeakSeen
	// A child that exited is not counted again if its PID is reused later
	// in the same process lifetime; keep the sets bounded to live children.
	for pid := range a.seenNode {
		if _, live := point.nodeChildren[pid]; !live {
			delete(a.seenNode, pid)
		}
	}
	for pid := range a.seenOther {
		if _, live := point.otherChildren[pid]; !live {
			delete(a.seenOther, pid)
		}
	}
}

// flush returns the finished window and starts the next one.
func (a *processMemoryAggregator) flush() processMemoryWindow {
	window := a.window
	a.window = processMemoryWindow{}
	return window
}

func processMemorySampleWindow() (time.Duration, bool) {
	raw := strings.TrimSpace(os.Getenv(processMemorySampleEnv))
	if raw == "" {
		return 0, false
	}
	window, err := time.ParseDuration(raw)
	if err != nil || window < time.Second {
		slog.Warn("process memory sampler disabled: invalid window", "env", processMemorySampleEnv)
		return 0, false
	}
	return window, true
}

func runProcessMemorySampler(ctx context.Context, window time.Duration) {
	source := processMemorySource{procRoot: "/proc", cgroupRoot: "/sys/fs/cgroup", pid: os.Getpid()}
	aggregator := newProcessMemoryAggregator()
	heap := []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}, {Name: "/memory/classes/total:bytes"}}
	tick := time.NewTicker(processMemoryTick)
	defer tick.Stop()
	flushAt := time.Now().Add(window)
	slog.Info("process memory sampler started", "window", window.String(), "tick", processMemoryTick.String())
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-tick.C:
			aggregator.add(source.sample())
			if now.Before(flushAt) {
				continue
			}
			flushAt = now.Add(window)
			w := aggregator.flush()
			metrics.Read(heap)
			slog.Info("process memory sample",
				"window_s", int(window.Seconds()),
				"samples", w.samples,
				"server_rss_kb_max", w.serverRSSKBMax,
				"server_hwm_kb", w.serverHWMKB,
				"go_heap_objects_bytes", heap[0].Value.Uint64(),
				"go_runtime_total_bytes", heap[1].Value.Uint64(),
				"node_children_max", w.nodeChildrenMax,
				"node_children_rss_kb_max", w.nodeChildrenRSSKBMax,
				"node_children_started", w.nodeStarted,
				"other_children_max", w.otherChildrenMax,
				"other_children_started", w.otherStarted,
				"container_usage_bytes_max", w.containerUsageMax,
				"container_working_set_bytes_max", w.containerWorkingMax,
				"container_working_set_bytes_last", w.containerWorkingLast,
				"container_peak_bytes", w.containerPeak,
			)
		}
	}
}
