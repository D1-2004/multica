package execenv

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

const (
	dwsShimDirName = "dws-shim"
	// DWSWrapArg selects the private dws PATH wrapper in the multica binary.
	DWSWrapArg = "__dws-wrap"
	IssueIDEnv = "MULTICA_ISSUE_ID"
	// The fixed Runtime wrapper delegates directly to the protected binary;
	// PATH shims instead keep passing through the Runtime's identity wrapper.
	dwsProtectedWrapEnv = "MULTICA_DWS_PROTECTED_WRAP"
	dwsShimActiveEnv    = "MULTICA_DWS_SHIM_ACTIVE"
)

const dwsShimScript = `#!/bin/sh
export MULTICA_DWS_SHIM_DIR=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
exec %s ` + DWSWrapArg + ` -- "$@"
`

// IssueEnv returns MULTICA_ISSUE_ID when the task is attached to an Issue.
func IssueEnv(issueID string) map[string]string {
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil
	}
	return map[string]string{IssueIDEnv: issueID}
}

// EnsureDWSShim writes a PATH wrapper that re-execs `multica __dws-wrap` so
// chat send uses the same Go parser as tests, then binds the conversation.
func EnsureDWSShim(envRoot string) (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate multica for dws shim: %w", err)
	}
	return ensureDWSShim(envRoot, self)
}

func ensureDWSShim(envRoot, self string) (string, error) {
	if envRoot == "" || runtime.GOOS == "windows" {
		return "", nil
	}
	dir := filepath.Join(envRoot, dwsShimDirName)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create dws shim dir: %w", err)
	}
	path := filepath.Join(dir, "dws")
	quotedSelf := "'" + strings.ReplaceAll(self, "'", "'\"'\"'") + "'"
	file, err := os.CreateTemp(dir, ".dws-*")
	if err != nil {
		return "", fmt.Errorf("create dws shim: %w", err)
	}
	defer os.Remove(file.Name())
	if _, err := file.WriteString(fmt.Sprintf(dwsShimScript, quotedSelf)); err != nil {
		file.Close()
		return "", fmt.Errorf("write dws shim: %w", err)
	}
	if err := file.Chmod(0o755); err != nil {
		file.Close()
		return "", fmt.Errorf("chmod dws shim: %w", err)
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("close dws shim: %w", err)
	}
	// Warm concurrent tasks may share this directory. Publish a complete
	// script atomically so one task cannot execute another's truncated write.
	if err := os.Rename(file.Name(), path); err != nil {
		return "", fmt.Errorf("publish dws shim: %w", err)
	}
	return dir, nil
}

// ParseDWSSendConversation reports whether args are a chat send and extracts
// --conversation-id / --conversation when present.
func ParseDWSSendConversation(args []string) (conversationID string, send bool) {
	parsed := parseDWSCommand(args)
	return parsed.conversationID, parsed.send
}

// DWSWrapDeps is the testable surface for the dws PATH wrapper.
type DWSWrapDeps struct {
	Args     []string
	Stdout   io.Writer
	Stderr   io.Writer
	Getenv   func(string) string
	LookPath func(string) (string, error)
	Run      func(name string, args []string, stdout, stderr io.Writer) error
	Bind     func(conversationID, evidenceID string) error
	Report   func(protocol.DingTalkSendReceipt) error
}

// MainDWSWrap is the CLI entrypoint for `multica __dws-wrap`.
func MainDWSWrap(args []string) int {
	return RunDWSWrap(DWSWrapDeps{
		Args:   args,
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Getenv: os.Getenv,
		Report: func(receipt protocol.DingTalkSendReceipt) error {
			return reportDWSSendReceipt(os.Getenv, nil, receipt)
		},
		LookPath: func(name string) (string, error) {
			if os.Getenv(dwsProtectedWrapEnv) == "1" {
				return "/usr/local/libexec/dws", nil
			}
			return lookPathExcept(name, os.Getenv("MULTICA_DWS_SHIM_DIR"), os.Getenv("PATH"))
		},
		Run: func(name string, argv []string, stdout, stderr io.Writer) error {
			cmd := exec.Command(name, argv...)
			cmd.Stdout = stdout
			cmd.Stderr = stderr
			cmd.Stdin = os.Stdin
			cmd.Env = append(os.Environ(), dwsShimActiveEnv+"=1")
			return cmd.Run()
		},
		Bind: func(conversationID, evidenceID string) error {
			argv := []string{"assoc", "bind", "--conversation", conversationID}
			if evidenceID != "" {
				argv = append(argv, "--evidence", evidenceID)
			}
			cmd := exec.Command("multica", argv...)
			cmd.Env = os.Environ()
			var buf bytes.Buffer
			cmd.Stderr = &buf
			cmd.Stdout = &buf
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("%w: %s", err, strings.TrimSpace(buf.String()))
			}
			return nil
		},
	})
}

// RunDWSWrap observes dws chat send and binds the conversation. Non-send
// commands are passed through. Bind failures are written to stderr and do not
// fail a successful send.
func RunDWSWrap(deps DWSWrapDeps) int {
	args := stripLeadingDashDash(deps.Args)
	parsed := parseDWSCommand(args)
	var policy *protocol.DingTalkMessagePolicy
	if parsed.userSend {
		var err error
		policy, err = dwsMessagePolicyFromEnv(deps.Getenv)
		if err != nil {
			fmt.Fprintln(deps.Stderr, err)
			return 64
		}
		args = RewriteDWSSendAITag(args, policy)
	}
	real, err := deps.LookPath("dws")
	if err != nil {
		fmt.Fprintln(deps.Stderr, "dws: real binary not found on PATH")
		return 127
	}
	cid := parsed.conversationID
	if !parsed.send || parsed.preview {
		if err := deps.Run(real, args, deps.Stdout, deps.Stderr); err != nil {
			return exitCode(err)
		}
		return 0
	}
	managed := policy != nil && policy.PlatformManagedLifecycle
	var receipt protocol.DingTalkSendReceipt
	if managed {
		if deps.Report == nil {
			fmt.Fprintln(deps.Stderr, "DingTalk send receipt reporter unavailable; message was not sent")
			return 69
		}
		receipt, err = newDWSSendReceipt(parsed)
		if err == nil {
			err = deps.Report(receipt)
		}
		if err != nil {
			fmt.Fprintf(deps.Stderr, "DingTalk send intent could not be recorded; message was not sent: %v\n", err)
			return 69
		}
	}
	var captured bytes.Buffer
	runErr := deps.Run(real, args, io.MultiWriter(deps.Stdout, &captured), deps.Stderr)
	if managed {
		receipt = updateDWSSendReceipt(receipt, captured.String(), runErr)
		if err := deps.Report(receipt); err != nil {
			// The persisted pending intent blocks task-finished duplication.
			// Do not turn an accepted send into a retryable CLI failure.
			fmt.Fprintf(deps.Stderr, "DingTalk send receipt update failed; pending delivery remains recorded, do not resend: %v\n", err)
		}
	}
	if runErr != nil {
		return exitCode(runErr)
	}
	gotCID, msgid := ExtractDWSReceipt(captured.String())
	if gotCID != "" {
		cid = gotCID
	}
	if cid == "" && !managed {
		if taskID := extractJSONString(captured.String(), "openTaskId"); plausibleConversationOrEvidenceID(taskID) {
			var statusBuf bytes.Buffer
			statusArgs := append([]string{"chat", "message", "query-send-status", "--open-task-id", taskID, "--format", "json"}, parsed.identityArgs...)
			if err := deps.Run(real, statusArgs, &statusBuf, deps.Stderr); err == nil {
				gotCID, gotMsg := ExtractDWSReceipt(statusBuf.String())
				if gotCID != "" {
					cid = gotCID
				}
				if msgid == "" {
					msgid = gotMsg
				}
			}
		}
	}
	if cid == "" || deps.Bind == nil {
		return 0
	}
	if err := deps.Bind(cid, msgid); err != nil {
		fmt.Fprintf(deps.Stderr, "multica assoc bind failed: %v\n", err)
	}
	return 0
}

func stripLeadingDashDash(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

func lookPathExcept(name, exceptDir, pathEnv string) (string, error) {
	exceptDir = strings.TrimRight(exceptDir, string(os.PathListSeparator))
	var filtered []string
	for _, dir := range filepath.SplitList(pathEnv) {
		if dir == "" || (exceptDir != "" && dir == exceptDir) {
			continue
		}
		filtered = append(filtered, dir)
	}
	search := strings.Join(filtered, string(os.PathListSeparator))
	var lastErr error
	for _, dir := range filepath.SplitList(search) {
		candidate := filepath.Join(dir, name)
		info, err := os.Stat(candidate)
		if err != nil {
			lastErr = err
			continue
		}
		if info.IsDir() {
			continue
		}
		if info.Mode()&0o111 == 0 {
			continue
		}
		return candidate, nil
	}
	if lastErr == nil {
		lastErr = exec.ErrNotFound
	}
	return "", lastErr
}

// LooksLikeDWSChatSend reports whether a tool command/input is an outbound
// DingTalk chat send (not list/search/help).
func LooksLikeDWSChatSend(command string) bool {
	s := strings.ToLower(command)
	if strings.Contains(s, "message list") || strings.Contains(s, "query-send-status") ||
		strings.Contains(s, "search") || strings.Contains(s, "--help") {
		return false
	}
	hasSend := strings.Contains(s, "message send") || strings.Contains(s, "message_send") ||
		strings.Contains(s, "chat send") || strings.Contains(s, "+dm") ||
		strings.Contains(s, "+send") || strings.Contains(s, "send-to-group") ||
		strings.Contains(s, "send-by-bot")
	if !hasSend {
		return false
	}
	return strings.Contains(s, "dws") || strings.Contains(s, "chat") || strings.Contains(s, "dingtalk")
}

// ExtractDWSReceipt reads openConversationId / openMsgId from dws JSON output.
func ExtractDWSReceipt(output string) (conversationID, evidenceID string) {
	conversationID = extractJSONString(output, "openConversationId")
	if conversationID == "" {
		conversationID = extractJSONString(output, "conversationId")
	}
	if conversationID == "" {
		conversationID = extractJSONString(output, "conversation_id")
	}
	evidenceID = extractJSONString(output, "openMsgId")
	if evidenceID == "" {
		evidenceID = extractJSONString(output, "openMessageId")
	}
	if evidenceID == "" {
		evidenceID = extractJSONString(output, "open_msg_id")
	}
	return conversationID, evidenceID
}

var conversationIDKeys = []string{"openConversationId", "conversationId", "conversation_id", "conversation"}
var evidenceIDKeys = []string{"openMsgId", "openMessageId", "open_msg_id", "evidence_id"}

// ExtractConversationFromTool finds the outbound scene from tool output, MCP
// input, or dws argv. Output receipts win over argv so a send that resolved a
// different cid than the flag is bound to the real scene.
func ExtractConversationFromTool(command, output string, input map[string]any) (conversationID, evidenceID string) {
	conversationID, evidenceID = ExtractDWSReceipt(output)
	if conversationID == "" {
		conversationID, evidenceID = ExtractDWSReceipt(command)
	}
	if conversationID == "" || evidenceID == "" {
		gotCID, gotEvidence := harvestJSONReceipt(input, 3)
		if conversationID == "" {
			conversationID = gotCID
		}
		if evidenceID == "" {
			evidenceID = gotEvidence
		}
	}
	if !plausibleConversationOrEvidenceID(conversationID) {
		conversationID = lookupString(input, conversationIDKeys, 3)
	}
	if !plausibleConversationOrEvidenceID(conversationID) {
		conversationID = ""
	}
	if conversationID == "" {
		if parsed, _ := ParseDWSSendConversation(strings.Fields(command)); plausibleConversationOrEvidenceID(parsed) {
			conversationID = parsed
		}
	}
	if !plausibleConversationOrEvidenceID(evidenceID) {
		evidenceID = lookupString(input, evidenceIDKeys, 3)
	}
	if !plausibleConversationOrEvidenceID(evidenceID) {
		evidenceID = ""
	}
	return conversationID, evidenceID
}

func harvestJSONReceipt(v any, depth int) (conversationID, evidenceID string) {
	if v == nil || depth < 0 {
		return "", ""
	}
	switch t := v.(type) {
	case string:
		return ExtractDWSReceipt(t)
	case map[string]any:
		for _, raw := range t {
			cid, evid := harvestJSONReceipt(raw, depth-1)
			if plausibleConversationOrEvidenceID(cid) {
				return cid, evid
			}
		}
	}
	return "", ""
}

func lookupString(v any, keys []string, depth int) string {
	if v == nil || depth < 0 {
		return ""
	}
	m, ok := v.(map[string]any)
	if !ok {
		return ""
	}
	for _, key := range keys {
		raw, ok := m[key]
		if !ok {
			continue
		}
		if s, ok := raw.(string); ok {
			if s = strings.TrimSpace(s); plausibleConversationOrEvidenceID(s) {
				return s
			}
		}
	}
	for _, raw := range m {
		if got := lookupString(raw, keys, depth-1); got != "" {
			return got
		}
	}
	return ""
}

// plausibleConversationOrEvidenceID rejects tool command text that the
// recursive JSON walk used to treat as a conversation id.
func plausibleConversationOrEvidenceID(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" || strings.ContainsAny(s, " \t\n") {
		return false
	}
	if strings.HasPrefix(s, "$") || strings.Contains(s, "dws") || strings.Contains(s, "--") {
		return false
	}
	return true
}

func extractJSONString(body, key string) string {
	needle := `"` + key + `"`
	idx := strings.Index(body, needle)
	if idx < 0 {
		return ""
	}
	rest := body[idx+len(needle):]
	colon := strings.Index(rest, ":")
	if colon < 0 {
		return ""
	}
	rest = strings.TrimSpace(rest[colon+1:])
	if !strings.HasPrefix(rest, `"`) {
		return ""
	}
	rest = rest[1:]
	end := strings.Index(rest, `"`)
	if end < 0 {
		return ""
	}
	return rest[:end]
}

func exitCode(err error) int {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return exitErr.ExitCode()
	}
	return 1
}
