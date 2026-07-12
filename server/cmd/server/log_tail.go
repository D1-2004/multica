package main

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// logTailFiles whitelists which runtime log files the tail endpoint may
// serve; the query parameter is a key here, never a path, so the handler
// cannot be steered outside the log directory.
var logTailFiles = map[string]string{
	"backend":   "backend.log",
	"frontend":  "frontend.log",
	"health":    "health.log",
	"bootstrap": "bootstrap.log",
}

const (
	logTailDefaultLines = 200
	logTailMaxLines     = 2000
	// logTailReadBudget bounds how far back the handler reads from the end
	// of the file, regardless of the requested line count.
	logTailReadBudget = int64(4 << 20)
)

// logTailHandler serves the tail of a runtime log file so operators can
// inspect server logs without shelling into the pod. Same access policy as
// realtimeMetricsHandler: with a token, require Authorization: Bearer; with
// no token, only direct loopback callers — proxied requests get 404 so the
// endpoint is not enumerable. Disabled entirely when logDir is unset.
func logTailHandler(token, logDir string) http.HandlerFunc {
	token = strings.TrimSpace(token)
	logDir = strings.TrimSpace(logDir)
	return func(w http.ResponseWriter, r *http.Request) {
		if logDir == "" {
			http.NotFound(w, r)
			return
		}
		if token != "" {
			if !hasBearerToken(r, token) {
				w.Header().Set("WWW-Authenticate", `Bearer realm="logs"`)
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
		} else if !isDirectLoopbackRequest(r) {
			http.NotFound(w, r)
			return
		}

		name := strings.TrimSpace(r.URL.Query().Get("file"))
		if name == "" {
			name = "backend"
		}
		fileName, ok := logTailFiles[name]
		if !ok {
			http.Error(w, "unknown file; use backend|frontend|health|bootstrap", http.StatusBadRequest)
			return
		}

		lines := logTailDefaultLines
		if raw := strings.TrimSpace(r.URL.Query().Get("lines")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				http.Error(w, "lines must be a positive integer", http.StatusBadRequest)
				return
			}
			lines = parsed
		}
		if lines > logTailMaxLines {
			lines = logTailMaxLines
		}
		contains := r.URL.Query().Get("contains")

		tail, err := tailFile(filepath.Join(logDir, fileName), lines, contains)
		if err != nil {
			if os.IsNotExist(err) {
				http.Error(w, "log file not found", http.StatusNotFound)
				return
			}
			http.Error(w, fmt.Sprintf("read log: %v", err), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		for _, line := range tail {
			_, _ = io.WriteString(w, line)
			_, _ = io.WriteString(w, "\n")
		}
	}
}

// tailFile returns up to `lines` trailing lines of the file, optionally
// keeping only lines that contain `contains`. It reads at most
// logTailReadBudget bytes from the end of the file.
func tailFile(path string, lines int, contains string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	offset := int64(0)
	if info.Size() > logTailReadBudget {
		offset = info.Size() - logTailReadBudget
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, err
	}
	data, err := io.ReadAll(io.LimitReader(f, logTailReadBudget))
	if err != nil {
		return nil, err
	}
	// Drop the first (likely partial) line when we started mid-file.
	if offset > 0 {
		if idx := strings.IndexByte(string(data), '\n'); idx >= 0 {
			data = data[idx+1:]
		}
	}

	raw := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	filtered := raw
	if contains != "" {
		filtered = make([]string, 0, len(raw))
		for _, line := range raw {
			if strings.Contains(line, contains) {
				filtered = append(filtered, line)
			}
		}
	}
	if len(filtered) > lines {
		filtered = filtered[len(filtered)-lines:]
	}
	return filtered, nil
}
