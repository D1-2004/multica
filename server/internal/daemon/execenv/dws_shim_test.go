package execenv

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseDWSSendConversation(t *testing.T) {
	t.Parallel()
	cid, send := ParseDWSSendConversation([]string{"chat", "message", "send", "--conversation-id", "cid-a", "--text", "hi"})
	if !send || cid != "cid-a" {
		t.Fatalf("got send=%v cid=%q", send, cid)
	}
	_, send = ParseDWSSendConversation([]string{"chat", "message", "list"})
	if send {
		t.Fatal("list must not look like send")
	}
	cid, send = ParseDWSSendConversation([]string{"chat", "+send", "--conversation", "cid-b"})
	if !send || cid != "cid-b" {
		t.Fatalf("got send=%v cid=%q", send, cid)
	}
	_, send = ParseDWSSendConversation([]string{"calendar", "send", "--conversation-id", "cid-x"})
	if send {
		t.Fatal("non-chat send must not bind")
	}
}

func TestLooksLikeDWSChatSend(t *testing.T) {
	t.Parallel()
	if !LooksLikeDWSChatSend(`dws chat message send --user 1 --content hi`) {
		t.Fatal("send")
	}
	if !LooksLikeDWSChatSend(`{"command":"dws chat +dm --to 冬翔 --content 今晚吃什么"}`) {
		t.Fatal("+dm")
	}
	if !LooksLikeDWSChatSend(`mcp__dingtalk-chat__message_send`) {
		t.Fatal("mcp send")
	}
	if !LooksLikeDWSChatSend(`dws chat send --user 1 --content hi`) {
		t.Fatal("chat send")
	}
	if LooksLikeDWSChatSend(`dws calendar send --conversation-id cid-x`) {
		t.Fatal("calendar send")
	}
	if LooksLikeDWSChatSend(`dws chat message list --conversation-id cid`) {
		t.Fatal("list")
	}
	if LooksLikeDWSChatSend(`dws chat message search-advanced`) {
		t.Fatal("search")
	}
}

func TestExtractDWSReceipt(t *testing.T) {
	t.Parallel()
	cid, mid := ExtractDWSReceipt(`{"result":{"openConversationId":"cid+abc==","openMessageId":"msg-9"}}`)
	if cid != "cid+abc==" || mid != "msg-9" {
		t.Fatalf("cid=%q mid=%q", cid, mid)
	}
}

func TestExtractConversationFromToolPrefersReceiptThenArgv(t *testing.T) {
	t.Parallel()
	cid, mid := ExtractConversationFromTool(
		`dws chat message send --conversation-id cid-flag --content hi`,
		`{"openConversationId":"cid+live==","openMsgId":"msg-live"}`,
		nil,
	)
	if cid != "cid+live==" || mid != "msg-live" {
		t.Fatalf("receipt cid=%q mid=%q", cid, mid)
	}
	cid, mid = ExtractConversationFromTool(
		`Bash dws chat message send --conversation-id cid-flag --content hi`,
		`ok`,
		map[string]any{"result": map[string]any{"conversation_id": "cid-nested"}},
	)
	if cid != "cid-nested" {
		t.Fatalf("nested cid=%q mid=%q", cid, mid)
	}
	cid, _ = ExtractConversationFromTool(
		`dws chat message send --conversation-id cid-flag --content hi`,
		``,
		nil,
	)
	if cid != "cid-flag" {
		t.Fatalf("argv cid=%q", cid)
	}
}

func TestExtractConversationFromToolIgnoresCommandText(t *testing.T) {
	t.Parallel()
	cid, _ := ExtractConversationFromTool(
		`Bash`,
		`ok`,
		map[string]any{"command": `dws chat message send --user 103262 --content 今晚吃什么`},
	)
	if cid != "" {
		t.Fatalf("command text must not become cid, got %q", cid)
	}
	cid, mid := ExtractConversationFromTool(
		`Bash`,
		`{"openConversationId":"cid+live==","openMsgId":"msg-live"}`,
		map[string]any{"command": `dws chat message send --user 103262 --content 今晚吃什么`},
	)
	if cid != "cid+live==" || mid != "msg-live" {
		t.Fatalf("receipt cid=%q mid=%q", cid, mid)
	}
}

func TestEnsureDWSShimWritesUnixScript(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix shim")
	}
	t.Parallel()
	root := t.TempDir()
	dir, err := EnsureDWSShim(root)
	if err != nil {
		t.Fatal(err)
	}
	if dir == "" {
		t.Fatal("expected shim dir")
	}
	body, err := os.ReadFile(filepath.Join(dir, "dws"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	if !strings.Contains(text, DWSWrapArg) {
		t.Fatalf("shim missing wrap arg: %s", text)
	}
	info, err := os.Stat(filepath.Join(dir, "dws"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("shim not executable: %s", info.Mode())
	}
}

func TestRunDWSWrapBindsChatSend(t *testing.T) {
	t.Parallel()
	var boundCID, boundEvidence string
	var ran []string
	var stdout bytes.Buffer
	code := RunDWSWrap(DWSWrapDeps{
		Args:   []string{"--", "chat", "message", "send", "--conversation-id", "cid-a"},
		Stdout: &stdout,
		Stderr: &bytes.Buffer{},
		LookPath: func(string) (string, error) {
			return "/bin/dws", nil
		},
		Run: func(_ string, args []string, out, _ io.Writer) error {
			ran = args
			_, _ = out.Write([]byte(`{"openMsgId":"msg-1"}`))
			return nil
		},
		Bind: func(conversationID, evidenceID string) error {
			boundCID = conversationID
			boundEvidence = evidenceID
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if len(ran) == 0 || ran[0] != "chat" {
		t.Fatalf("ran=%v", ran)
	}
	if boundCID != "cid-a" || boundEvidence != "msg-1" {
		t.Fatalf("bind cid=%q evidence=%q", boundCID, boundEvidence)
	}
	if !strings.Contains(stdout.String(), "openMsgId") {
		t.Fatalf("stdout=%s", stdout.String())
	}
}

func TestRunDWSWrapQueriesSendStatusForUserSend(t *testing.T) {
	t.Parallel()
	var boundCID, boundEvidence string
	var calls [][]string
	code := RunDWSWrap(DWSWrapDeps{
		Args:   []string{"chat", "message", "send", "--user", "0104644667680872", "--content", "hi"},
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		LookPath: func(string) (string, error) {
			return "/bin/dws", nil
		},
		Run: func(_ string, args []string, out, _ io.Writer) error {
			copied := append([]string{}, args...)
			calls = append(calls, copied)
			if len(args) >= 3 && args[2] == "query-send-status" {
				_, _ = out.Write([]byte(`{"openConversationId":"cid+bEFv==","openMessageId":"msg-9"}`))
				return nil
			}
			_, _ = out.Write([]byte(`{"result":{"openTaskId":"task-1"},"success":true}`))
			return nil
		},
		Bind: func(conversationID, evidenceID string) error {
			boundCID = conversationID
			boundEvidence = evidenceID
			return nil
		},
	})
	if code != 0 {
		t.Fatalf("exit=%d", code)
	}
	if len(calls) != 2 {
		t.Fatalf("calls=%v", calls)
	}
	if boundCID != "cid+bEFv==" || boundEvidence != "msg-9" {
		t.Fatalf("bind cid=%q evidence=%q", boundCID, boundEvidence)
	}
}

func TestRunDWSWrapDoesNotBindList(t *testing.T) {
	t.Parallel()
	bound := false
	code := RunDWSWrap(DWSWrapDeps{
		Args:   []string{"chat", "message", "list"},
		Stdout: &bytes.Buffer{},
		Stderr: &bytes.Buffer{},
		LookPath: func(string) (string, error) {
			return "/bin/dws", nil
		},
		Run: func(string, []string, io.Writer, io.Writer) error {
			return nil
		},
		Bind: func(string, string) error {
			bound = true
			return nil
		},
	})
	if code != 0 || bound {
		t.Fatalf("exit=%d bound=%v", code, bound)
	}
}

func TestRunDWSWrapBindFailureIsVisible(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	code := RunDWSWrap(DWSWrapDeps{
		Args:   []string{"chat", "send", "--conversation-id", "cid-a"},
		Stdout: &bytes.Buffer{},
		Stderr: &stderr,
		LookPath: func(string) (string, error) {
			return "/bin/dws", nil
		},
		Run: func(string, []string, io.Writer, io.Writer) error {
			return nil
		},
		Bind: func(string, string) error {
			return errors.New("task not found")
		},
	})
	if code != 0 {
		t.Fatalf("send should still succeed, exit=%d", code)
	}
	if !strings.Contains(stderr.String(), "assoc bind failed") {
		t.Fatalf("stderr=%s", stderr.String())
	}
}

func TestIssueEnvOmitsEmpty(t *testing.T) {
	t.Parallel()
	if IssueEnv("") != nil {
		t.Fatal("empty issue must not export env")
	}
	env := IssueEnv("issue-1")
	if env[IssueIDEnv] != "issue-1" {
		t.Fatalf("%v", env)
	}
}

// An agent that prepends the task's shim directory under another spelling
// ("workdir/../dws-shim") must not make the wrapper resolve itself.
func TestLookPathExceptSkipsShimDirUnderAnySpelling(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix PATH shim")
	}
	t.Parallel()
	root := t.TempDir()
	shimDir, err := ensureDWSShim(root, "/opt/multica")
	if err != nil || shimDir == "" {
		t.Fatalf("shim: %q %v", shimDir, err)
	}
	workdir := filepath.Join(root, "workdir")
	realDir := filepath.Join(root, "bin")
	for _, dir := range []string{workdir, realDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(realDir, "dws"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "shim-link")
	if err := os.Symlink(shimDir, link); err != nil {
		t.Fatal(err)
	}
	for _, spelling := range []string{workdir + "/../dws-shim", shimDir + "/", link} {
		path := strings.Join([]string{spelling, shimDir, realDir}, string(os.PathListSeparator))
		got, err := lookPathExcept("dws", shimDir, path)
		if err != nil || got != filepath.Join(realDir, "dws") {
			t.Fatalf("PATH entry %q resolved %q %v, want the real dws", spelling, got, err)
		}
	}
}

func TestRunDWSWrapRefusesUnboundedReentry(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	ran := false
	code := RunDWSWrap(DWSWrapDeps{
		Args:   []string{"--", "--help"},
		Stdout: io.Discard,
		Stderr: &stderr,
		Getenv: func(key string) string {
			if key == dwsWrapDepthEnv {
				return "4"
			}
			return ""
		},
		LookPath: func(string) (string, error) { return "/x/dws", nil },
		Run: func(string, []string, io.Writer, io.Writer) error {
			ran = true
			return nil
		},
	})
	if code != 126 || ran || !strings.Contains(stderr.String(), "refusing to recurse") {
		t.Fatalf("code=%d ran=%v stderr=%q", code, ran, stderr.String())
	}
	// A normal depth still passes through.
	code = RunDWSWrap(DWSWrapDeps{
		Args: []string{"--", "--help"}, Stdout: io.Discard, Stderr: io.Discard,
		Getenv:   func(key string) string { return map[string]string{dwsWrapDepthEnv: "1"}[key] },
		LookPath: func(string) (string, error) { return "/x/dws", nil },
		Run:      func(string, []string, io.Writer, io.Writer) error { ran = true; return nil },
	})
	if code != 0 || !ran {
		t.Fatalf("depth 1 passthrough code=%d ran=%v", code, ran)
	}
}
