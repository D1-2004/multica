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
