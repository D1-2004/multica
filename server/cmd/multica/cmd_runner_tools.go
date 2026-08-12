package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/multica-ai/multica/server/pkg/runnerprotocol"
)

const (
	runnerFileLimit          = 1 << 20
	runnerSearchFileLimit    = 2 << 20
	runnerOutputLimit        = 1 << 20
	runnerEncodedResultLimit = (2 << 20) - (64 << 10)
)

type runnerToolError struct {
	code    string
	message string
}

func (e *runnerToolError) Error() string { return e.message }

func executeRunnerCall(parent context.Context, call runnerprotocol.Call) runnerprotocol.Result {
	result := runnerprotocol.Result{Type: runnerprotocol.MessageResult, CallID: call.CallID}
	expiresAt, err := time.Parse(time.RFC3339Nano, call.ExpiresAt)
	if err != nil || !expiresAt.After(time.Now()) {
		result.ErrorCode = "runner_call_expired"
		result.ErrorMessage = "Runner call is expired"
		return result
	}
	ctx, cancel := context.WithDeadline(parent, expiresAt)
	defer cancel()
	value, toolErr := runRunnerTool(ctx, call.Roots, call.ToolName, call.Arguments)
	if toolErr != nil {
		result.ErrorCode = toolErr.code
		result.ErrorMessage = toolErr.message
		return result
	}
	raw, err := json.Marshal(value)
	if err != nil {
		result.ErrorCode = "runner_result_encode_failed"
		result.ErrorMessage = "Could not encode Runner result"
		return result
	}
	if len(raw) > runnerEncodedResultLimit {
		result.ErrorCode = "runner_result_too_large"
		result.ErrorMessage = "Runner result exceeds the transport limit"
		return result
	}
	result.Succeeded = true
	result.Result = raw
	return result
}

func decodeRunnerArguments(raw json.RawMessage, out any) *runnerToolError {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(out); err != nil {
		return &runnerToolError{code: "runner_invalid_arguments", message: "Invalid Runner tool arguments"}
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return &runnerToolError{code: "runner_invalid_arguments", message: "Invalid Runner tool arguments"}
	}
	return nil
}

func runRunnerTool(ctx context.Context, roots []string, toolName string, raw json.RawMessage) (any, *runnerToolError) {
	switch toolName {
	case "read_file":
		return runnerReadFile(roots, raw)
	case "write_file":
		return runnerWriteFile(roots, raw)
	case "edit_file":
		return runnerEditFile(roots, raw)
	case "list_directory":
		return runnerListDirectory(roots, raw)
	case "stat":
		return runnerStat(roots, raw)
	case "glob":
		return runnerGlob(ctx, roots, raw)
	case "grep":
		return runnerGrep(ctx, roots, raw)
	case "shell":
		return runnerShell(ctx, roots, raw)
	case "shell_output":
		return runnerShellOutput(raw)
	case "shell_kill":
		return runnerShellKill(raw)
	default:
		return nil, &runnerToolError{code: "runner_unknown_tool", message: "Unknown Runner tool"}
	}
}

type runnerRoot struct {
	configured string
	real       string
}

func runnerRoots(roots []string) ([]runnerRoot, *runnerToolError) {
	out := make([]runnerRoot, 0, len(roots))
	for _, root := range roots {
		absolute, err := filepath.Abs(root)
		if err != nil {
			return nil, &runnerToolError{code: "runner_root_invalid", message: "Runner root is invalid"}
		}
		real, err := filepath.EvalSymlinks(absolute)
		if err != nil {
			return nil, &runnerToolError{code: "runner_root_unavailable", message: "Runner root is unavailable: " + absolute}
		}
		out = append(out, runnerRoot{configured: filepath.Clean(absolute), real: filepath.Clean(real)})
	}
	return out, nil
}

func pathWithin(root, target string) bool {
	relative, err := filepath.Rel(root, target)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func resolveRunnerPath(roots []string, requested string, mustExist bool) (string, *runnerToolError) {
	if requested == "" || !filepath.IsAbs(requested) || strings.ContainsRune(requested, '\x00') {
		return "", &runnerToolError{code: "runner_path_invalid", message: "Path must be absolute"}
	}
	target := filepath.Clean(requested)
	rootSet, rootErr := runnerRoots(roots)
	if rootErr != nil {
		return "", rootErr
	}
	for _, root := range rootSet {
		if !pathWithin(root.configured, target) {
			continue
		}
		if mustExist {
			realTarget, err := filepath.EvalSymlinks(target)
			if err != nil {
				return "", &runnerToolError{code: "runner_path_not_found", message: "Path does not exist"}
			}
			if pathWithin(root.real, filepath.Clean(realTarget)) {
				return target, nil
			}
			return "", &runnerToolError{code: "runner_path_outside_root", message: "Path resolves outside the configured root"}
		}

		ancestor := target
		for {
			if _, err := os.Lstat(ancestor); err == nil {
				break
			} else if !errors.Is(err, os.ErrNotExist) {
				return "", &runnerToolError{code: "runner_path_unavailable", message: "Path is unavailable"}
			}
			parent := filepath.Dir(ancestor)
			if parent == ancestor {
				return "", &runnerToolError{code: "runner_path_not_found", message: "No existing parent for path"}
			}
			ancestor = parent
		}
		realAncestor, err := filepath.EvalSymlinks(ancestor)
		if err != nil || !pathWithin(root.real, filepath.Clean(realAncestor)) {
			return "", &runnerToolError{code: "runner_path_outside_root", message: "Path resolves outside the configured root"}
		}
		return target, nil
	}
	return "", &runnerToolError{code: "runner_path_outside_root", message: "Path is outside every configured Runner root"}
}

func runnerReadFile(roots []string, raw json.RawMessage) (any, *runnerToolError) {
	var args struct {
		Path     string `json:"path"`
		Offset   int64  `json:"offset"`
		Limit    int64  `json:"limit"`
		Encoding string `json:"encoding"`
	}
	if err := decodeRunnerArguments(raw, &args); err != nil {
		return nil, err
	}
	if args.Offset < 0 {
		return nil, &runnerToolError{code: "runner_invalid_arguments", message: "offset must not be negative"}
	}
	if args.Limit == 0 {
		args.Limit = runnerFileLimit
	}
	if args.Limit < 1 || args.Limit > runnerFileLimit {
		return nil, &runnerToolError{code: "runner_invalid_arguments", message: "limit must be between 1 and 1048576"}
	}
	if args.Encoding == "" {
		args.Encoding = "utf8"
	}
	if args.Encoding != "utf8" && args.Encoding != "base64" {
		return nil, &runnerToolError{code: "runner_invalid_arguments", message: "encoding must be utf8 or base64"}
	}
	path, pathErr := resolveRunnerPath(roots, args.Path, true)
	if pathErr != nil {
		return nil, pathErr
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, &runnerToolError{code: "runner_not_regular_file", message: "Path is not a regular file"}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, &runnerToolError{code: "runner_read_failed", message: err.Error()}
	}
	defer file.Close()
	if _, err := file.Seek(args.Offset, io.SeekStart); err != nil {
		return nil, &runnerToolError{code: "runner_read_failed", message: err.Error()}
	}
	content, err := io.ReadAll(io.LimitReader(file, args.Limit))
	if err != nil {
		return nil, &runnerToolError{code: "runner_read_failed", message: err.Error()}
	}
	encoded := string(content)
	if args.Encoding == "base64" {
		encoded = base64.StdEncoding.EncodeToString(content)
	} else if !utf8.Valid(content) {
		return nil, &runnerToolError{code: "runner_file_not_utf8", message: "File is not valid UTF-8; read it with encoding=base64"}
	}
	return map[string]any{
		"path": path, "content": encoded, "encoding": args.Encoding,
		"offset": args.Offset, "bytes_read": len(content), "size": info.Size(),
		"truncated": args.Offset+int64(len(content)) < info.Size(),
	}, nil
}

func decodeRunnerContent(content, encoding string) ([]byte, *runnerToolError) {
	if encoding == "" || encoding == "utf8" {
		return []byte(content), nil
	}
	if encoding != "base64" {
		return nil, &runnerToolError{code: "runner_invalid_arguments", message: "encoding must be utf8 or base64"}
	}
	decoded, err := base64.StdEncoding.DecodeString(content)
	if err != nil {
		return nil, &runnerToolError{code: "runner_invalid_arguments", message: "content is not valid base64"}
	}
	return decoded, nil
}

func atomicRunnerWrite(path string, content []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, ".multica-runner-*.tmp")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(content); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpPath, mode); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}

func runnerWriteFile(roots []string, raw json.RawMessage) (any, *runnerToolError) {
	var args struct {
		Path          string `json:"path"`
		Content       string `json:"content"`
		Encoding      string `json:"encoding"`
		CreateParents bool   `json:"create_parents"`
	}
	if err := decodeRunnerArguments(raw, &args); err != nil {
		return nil, err
	}
	content, contentErr := decodeRunnerContent(args.Content, args.Encoding)
	if contentErr != nil {
		return nil, contentErr
	}
	if len(content) > runnerFileLimit {
		return nil, &runnerToolError{code: "runner_file_too_large", message: "File content exceeds 1 MiB"}
	}
	path, pathErr := resolveRunnerPath(roots, args.Path, false)
	if pathErr != nil {
		return nil, pathErr
	}
	if args.CreateParents {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, &runnerToolError{code: "runner_write_failed", message: err.Error()}
		}
	}
	mode := os.FileMode(0o644)
	if info, err := os.Stat(path); err == nil {
		if !info.Mode().IsRegular() {
			return nil, &runnerToolError{code: "runner_not_regular_file", message: "Path is not a regular file"}
		}
		mode = info.Mode().Perm()
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, &runnerToolError{code: "runner_write_failed", message: err.Error()}
	}
	if err := atomicRunnerWrite(path, content, mode); err != nil {
		return nil, &runnerToolError{code: "runner_write_failed", message: err.Error()}
	}
	return map[string]any{"path": path, "bytes_written": len(content)}, nil
}

func runnerEditFile(roots []string, raw json.RawMessage) (any, *runnerToolError) {
	var args struct {
		Path       string `json:"path"`
		OldText    string `json:"old_text"`
		NewText    string `json:"new_text"`
		ReplaceAll bool   `json:"replace_all"`
	}
	if err := decodeRunnerArguments(raw, &args); err != nil {
		return nil, err
	}
	if args.OldText == "" {
		return nil, &runnerToolError{code: "runner_invalid_arguments", message: "old_text must not be empty"}
	}
	path, pathErr := resolveRunnerPath(roots, args.Path, true)
	if pathErr != nil {
		return nil, pathErr
	}
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		return nil, &runnerToolError{code: "runner_not_regular_file", message: "Path is not a regular file"}
	}
	if info.Size() > runnerSearchFileLimit {
		return nil, &runnerToolError{code: "runner_file_not_editable", message: "File must be UTF-8 and no larger than 2 MiB"}
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, &runnerToolError{code: "runner_read_failed", message: err.Error()}
	}
	if len(content) > runnerSearchFileLimit || !utf8.Valid(content) {
		return nil, &runnerToolError{code: "runner_file_not_editable", message: "File must be UTF-8 and no larger than 2 MiB"}
	}
	count := strings.Count(string(content), args.OldText)
	if count == 0 {
		return nil, &runnerToolError{code: "runner_text_not_found", message: "old_text was not found"}
	}
	if !args.ReplaceAll && count != 1 {
		return nil, &runnerToolError{code: "runner_text_not_unique", message: "old_text occurs more than once; set replace_all or provide more context"}
	}
	replacements := 1
	if args.ReplaceAll {
		replacements = count
	}
	updated := strings.Replace(string(content), args.OldText, args.NewText, replacements)
	if err := atomicRunnerWrite(path, []byte(updated), info.Mode().Perm()); err != nil {
		return nil, &runnerToolError{code: "runner_write_failed", message: err.Error()}
	}
	return map[string]any{"path": path, "replacements": replacements}, nil
}

func runnerListDirectory(roots []string, raw json.RawMessage) (any, *runnerToolError) {
	var args struct {
		Path string `json:"path"`
	}
	if err := decodeRunnerArguments(raw, &args); err != nil {
		return nil, err
	}
	path, pathErr := resolveRunnerPath(roots, args.Path, true)
	if pathErr != nil {
		return nil, pathErr
	}
	info, err := os.Stat(path)
	if err != nil || !info.IsDir() {
		return nil, &runnerToolError{code: "runner_not_directory", message: "Path is not a directory"}
	}
	directory, err := os.Open(path)
	if err != nil {
		return nil, &runnerToolError{code: "runner_list_failed", message: err.Error()}
	}
	defer directory.Close()
	entries, err := directory.ReadDir(1001)
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, &runnerToolError{code: "runner_list_failed", message: err.Error()}
	}
	truncated := len(entries) > 1000
	if len(entries) > 1000 {
		entries = entries[:1000]
	}
	items := make([]map[string]any, 0, len(entries))
	for _, entry := range entries {
		info, infoErr := entry.Info()
		if infoErr != nil {
			continue
		}
		items = append(items, map[string]any{
			"name": entry.Name(), "path": filepath.Join(path, entry.Name()),
			"type": runnerFileType(info), "size": info.Size(),
			"modified_at": info.ModTime().UTC().Format(time.RFC3339),
		})
	}
	return map[string]any{"path": path, "entries": items, "count": len(items), "truncated": truncated}, nil
}

func runnerFileType(info os.FileInfo) string {
	if info.IsDir() {
		return "directory"
	}
	if info.Mode().IsRegular() {
		return "file"
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "symlink"
	}
	return "other"
}

func runnerStat(roots []string, raw json.RawMessage) (any, *runnerToolError) {
	var args struct {
		Path string `json:"path"`
	}
	if err := decodeRunnerArguments(raw, &args); err != nil {
		return nil, err
	}
	path, pathErr := resolveRunnerPath(roots, args.Path, true)
	if pathErr != nil {
		return nil, pathErr
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, &runnerToolError{code: "runner_stat_failed", message: err.Error()}
	}
	return map[string]any{
		"path": path, "type": runnerFileType(info), "size": info.Size(),
		"mode": info.Mode().String(), "modified_at": info.ModTime().UTC().Format(time.RFC3339),
	}, nil
}

func globRunnerRegexp(pattern string) (*regexp.Regexp, error) {
	pattern = filepath.ToSlash(pattern)
	if strings.HasPrefix(pattern, "/") || strings.Contains(pattern, "\x00") {
		return nil, errors.New("glob pattern must be relative")
	}
	var out strings.Builder
	out.WriteString("^")
	for i := 0; i < len(pattern); i++ {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				i++
				if i+1 < len(pattern) && pattern[i+1] == '/' {
					i++
					out.WriteString("(?:.*/)?")
				} else {
					out.WriteString(".*")
				}
			} else {
				out.WriteString("[^/]*")
			}
		case '?':
			out.WriteString("[^/]")
		default:
			out.WriteString(regexp.QuoteMeta(string(pattern[i])))
		}
	}
	out.WriteString("$")
	return regexp.Compile(out.String())
}

func runnerGlob(ctx context.Context, roots []string, raw json.RawMessage) (any, *runnerToolError) {
	var args struct {
		Root       string `json:"root"`
		Pattern    string `json:"pattern"`
		MaxResults int    `json:"max_results"`
	}
	if err := decodeRunnerArguments(raw, &args); err != nil {
		return nil, err
	}
	if args.MaxResults == 0 {
		args.MaxResults = 200
	}
	if args.MaxResults < 1 || args.MaxResults > 1000 {
		return nil, &runnerToolError{code: "runner_invalid_arguments", message: "max_results must be between 1 and 1000"}
	}
	root, pathErr := resolveRunnerPath(roots, args.Root, true)
	if pathErr != nil {
		return nil, pathErr
	}
	matcher, err := globRunnerRegexp(args.Pattern)
	if err != nil {
		return nil, &runnerToolError{code: "runner_invalid_arguments", message: "invalid glob pattern"}
	}
	matches := make([]string, 0, args.MaxResults)
	walkResult := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil || path == root {
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 && entry.IsDir() {
			return filepath.SkipDir
		}
		relative, _ := filepath.Rel(root, path)
		if matcher.MatchString(filepath.ToSlash(relative)) {
			matches = append(matches, path)
			if len(matches) >= args.MaxResults {
				return io.EOF
			}
		}
		return nil
	})
	if errors.Is(walkResult, context.Canceled) || errors.Is(walkResult, context.DeadlineExceeded) {
		return nil, runnerContextError(ctx)
	}
	if walkResult != nil && !errors.Is(walkResult, io.EOF) {
		return nil, &runnerToolError{code: "runner_glob_failed", message: walkResult.Error()}
	}
	sort.Strings(matches)
	return map[string]any{"root": root, "matches": matches, "count": len(matches), "truncated": len(matches) >= args.MaxResults}, nil
}

func runnerGrep(ctx context.Context, roots []string, raw json.RawMessage) (any, *runnerToolError) {
	var args struct {
		Root       string `json:"root"`
		Pattern    string `json:"pattern"`
		MaxResults int    `json:"max_results"`
	}
	if err := decodeRunnerArguments(raw, &args); err != nil {
		return nil, err
	}
	if args.MaxResults == 0 {
		args.MaxResults = 200
	}
	if args.MaxResults < 1 || args.MaxResults > 1000 {
		return nil, &runnerToolError{code: "runner_invalid_arguments", message: "max_results must be between 1 and 1000"}
	}
	root, pathErr := resolveRunnerPath(roots, args.Root, true)
	if pathErr != nil {
		return nil, pathErr
	}
	matcher, err := regexp.Compile(args.Pattern)
	if err != nil {
		return nil, &runnerToolError{code: "runner_invalid_arguments", message: "pattern must be a valid regular expression"}
	}
	matches := make([]map[string]any, 0, args.MaxResults)
	walkResult := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil || entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
			return nil
		}
		info, infoErr := entry.Info()
		if infoErr != nil || !info.Mode().IsRegular() || info.Size() > runnerSearchFileLimit {
			return nil
		}
		file, openErr := os.Open(path)
		if openErr != nil {
			return nil
		}
		scanner := bufio.NewScanner(file)
		scanner.Buffer(make([]byte, 64<<10), runnerSearchFileLimit)
		lineNumber := 0
		for scanner.Scan() {
			lineNumber++
			line := scanner.Text()
			location := matcher.FindStringIndex(line)
			if location == nil {
				continue
			}
			matches = append(matches, map[string]any{
				"path": path, "line": lineNumber, "column": utf8.RuneCountInString(line[:location[0]]) + 1, "text": line,
			})
			if len(matches) >= args.MaxResults {
				break
			}
		}
		_ = file.Close()
		if len(matches) >= args.MaxResults {
			return io.EOF
		}
		return nil
	})
	if errors.Is(walkResult, context.Canceled) || errors.Is(walkResult, context.DeadlineExceeded) {
		return nil, runnerContextError(ctx)
	}
	if walkResult != nil && !errors.Is(walkResult, io.EOF) {
		return nil, &runnerToolError{code: "runner_grep_failed", message: walkResult.Error()}
	}
	return map[string]any{"root": root, "matches": matches, "count": len(matches), "truncated": len(matches) >= args.MaxResults}, nil
}

func runnerContextError(ctx context.Context) *runnerToolError {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return &runnerToolError{code: "runner_call_expired", message: "Runner call is expired"}
	}
	return &runnerToolError{code: "runner_call_cancelled", message: "Runner call was cancelled"}
}

type synchronizedLimitedBuffer struct {
	mu        sync.Mutex
	buffer    bytes.Buffer
	limit     int
	truncated bool
}

func (b *synchronizedLimitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	original := len(p)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.truncated = true
		return original, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buffer.Write(p)
	return original, nil
}

func (b *synchronizedLimitedBuffer) snapshot() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buffer.String(), b.truncated
}

type runnerBackgroundProcess struct {
	id      string
	command *exec.Cmd
	output  *synchronizedLimitedBuffer

	mu       sync.RWMutex
	done     bool
	exitCode int
	error    string
}

var runnerProcessRegistry = struct {
	sync.RWMutex
	items map[string]*runnerBackgroundProcess
}{items: make(map[string]*runnerBackgroundProcess)}

func runnerShell(ctx context.Context, roots []string, raw json.RawMessage) (any, *runnerToolError) {
	var args struct {
		Command        string `json:"command"`
		CWD            string `json:"cwd"`
		Background     bool   `json:"background"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	}
	if err := decodeRunnerArguments(raw, &args); err != nil {
		return nil, err
	}
	if strings.TrimSpace(args.Command) == "" {
		return nil, &runnerToolError{code: "runner_invalid_arguments", message: "command is required"}
	}
	cwd, pathErr := resolveRunnerPath(roots, args.CWD, true)
	if pathErr != nil {
		return nil, pathErr
	}
	info, err := os.Stat(cwd)
	if err != nil || !info.IsDir() {
		return nil, &runnerToolError{code: "runner_invalid_cwd", message: "cwd must be a directory in a configured root"}
	}
	if args.TimeoutSeconds == 0 {
		args.TimeoutSeconds = 60
	}
	if args.TimeoutSeconds < 1 || args.TimeoutSeconds > 600 {
		return nil, &runnerToolError{code: "runner_invalid_arguments", message: "timeout_seconds must be between 1 and 600"}
	}
	if args.Background {
		return startRunnerBackgroundShell(cwd, args.Command)
	}
	commandCtx, cancel := context.WithTimeout(ctx, time.Duration(args.TimeoutSeconds)*time.Second)
	defer cancel()
	command := exec.CommandContext(commandCtx, "/bin/sh", "-lc", args.Command)
	command.Dir = cwd
	if err := configureRunnerProcessGroup(command); err != nil {
		return nil, &runnerToolError{code: "runner_shell_start_failed", message: err.Error()}
	}
	command.Cancel = func() error { return killRunnerProcessGroup(command) }
	output := &synchronizedLimitedBuffer{limit: runnerOutputLimit}
	command.Stdout = output
	command.Stderr = output
	err = command.Run()
	text, truncated := output.snapshot()
	exitCode := 0
	if command.ProcessState != nil {
		exitCode = command.ProcessState.ExitCode()
	}
	if commandCtx.Err() == context.DeadlineExceeded {
		return nil, &runnerToolError{code: "runner_shell_timeout", message: "Shell command exceeded its timeout"}
	}
	return map[string]any{
		"exit_code": exitCode, "output": text, "truncated": truncated,
		"succeeded": err == nil,
	}, nil
}

func newRunnerProcessID() (string, error) {
	raw := make([]byte, 12)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return "rp_" + hex.EncodeToString(raw), nil
}

func startRunnerBackgroundShell(cwd, shellCommand string) (any, *runnerToolError) {
	id, err := newRunnerProcessID()
	if err != nil {
		return nil, &runnerToolError{code: "runner_shell_start_failed", message: err.Error()}
	}
	command := exec.Command("/bin/sh", "-lc", shellCommand)
	command.Dir = cwd
	if err := configureRunnerProcessGroup(command); err != nil {
		return nil, &runnerToolError{code: "runner_shell_start_failed", message: err.Error()}
	}
	output := &synchronizedLimitedBuffer{limit: runnerOutputLimit}
	command.Stdout = output
	command.Stderr = output
	if err := command.Start(); err != nil {
		return nil, &runnerToolError{code: "runner_shell_start_failed", message: err.Error()}
	}
	process := &runnerBackgroundProcess{id: id, command: command, output: output, exitCode: -1}
	runnerProcessRegistry.Lock()
	runnerProcessRegistry.items[id] = process
	runnerProcessRegistry.Unlock()
	go func() {
		err := command.Wait()
		process.mu.Lock()
		process.done = true
		if command.ProcessState != nil {
			process.exitCode = command.ProcessState.ExitCode()
		}
		if err != nil {
			process.error = err.Error()
		}
		process.mu.Unlock()
	}()
	return map[string]any{"process_id": id, "pid": command.Process.Pid, "started": true}, nil
}

func getRunnerBackgroundProcess(id string) (*runnerBackgroundProcess, *runnerToolError) {
	runnerProcessRegistry.RLock()
	process := runnerProcessRegistry.items[id]
	runnerProcessRegistry.RUnlock()
	if process == nil {
		return nil, &runnerToolError{code: "runner_process_not_found", message: "Background process was not found"}
	}
	return process, nil
}

func runnerShellOutput(raw json.RawMessage) (any, *runnerToolError) {
	var args struct {
		ProcessID string `json:"process_id"`
	}
	if err := decodeRunnerArguments(raw, &args); err != nil {
		return nil, err
	}
	process, processErr := getRunnerBackgroundProcess(args.ProcessID)
	if processErr != nil {
		return nil, processErr
	}
	output, truncated := process.output.snapshot()
	process.mu.RLock()
	defer process.mu.RUnlock()
	return map[string]any{
		"process_id": args.ProcessID, "done": process.done, "exit_code": process.exitCode,
		"error": process.error, "output": output, "truncated": truncated,
	}, nil
}

func runnerShellKill(raw json.RawMessage) (any, *runnerToolError) {
	var args struct {
		ProcessID string `json:"process_id"`
	}
	if err := decodeRunnerArguments(raw, &args); err != nil {
		return nil, err
	}
	process, processErr := getRunnerBackgroundProcess(args.ProcessID)
	if processErr != nil {
		return nil, processErr
	}
	process.mu.RLock()
	done := process.done
	process.mu.RUnlock()
	if done {
		return map[string]any{"process_id": args.ProcessID, "stopped": false, "already_done": true}, nil
	}
	if err := killRunnerProcessGroup(process.command); err != nil {
		return nil, &runnerToolError{code: "runner_process_kill_failed", message: err.Error()}
	}
	return map[string]any{"process_id": args.ProcessID, "stopped": true}, nil
}

func stopAllRunnerBackgroundProcesses() {
	runnerProcessRegistry.RLock()
	processes := make([]*runnerBackgroundProcess, 0, len(runnerProcessRegistry.items))
	for _, process := range runnerProcessRegistry.items {
		processes = append(processes, process)
	}
	runnerProcessRegistry.RUnlock()
	for _, process := range processes {
		process.mu.RLock()
		done := process.done
		process.mu.RUnlock()
		if !done {
			_ = killRunnerProcessGroup(process.command)
		}
	}
}
