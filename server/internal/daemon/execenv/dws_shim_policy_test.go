package execenv

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/pkg/protocol"
)

func TestDWSMessagePolicyPreservesArgv(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		args []string
		want []string
	}{
		{"basic", []string{"chat", "message", "send", "--content", "hello"}, []string{"chat", "message", "send", "--content", "hello", "--ai-tag=false"}},
		{"conflicting repeats", []string{"chat", "message", "send", "--ai-tag", "--ai-tag=false", "--ai-tag=true", "--content", "hello"}, []string{"chat", "message", "send", "--content", "hello", "--ai-tag=false"}},
		{"bare bool preserves positional false", []string{"chat", "message", "send", "--ai-tag", "false"}, []string{"chat", "message", "send", "false", "--ai-tag=false"}},
		{"content is not an option", []string{"chat", "message", "send", "--content", "--ai-tag=true"}, []string{"chat", "message", "send", "--content", "--ai-tag=true", "--ai-tag=false"}},
		{"end of options", []string{"chat", "message", "send", "--", "--ai-tag=true", "--help"}, []string{"chat", "message", "send", "--ai-tag=false", "--", "--ai-tag=true", "--help"}},
		{"command values", []string{"--profile", "chat", "chat", "--jq", "send", "message", "send", "--title", "--help", "--content", "a\n'b' $()"}, []string{"--profile", "chat", "chat", "--jq", "send", "message", "send", "--title", "--help", "--content", "a\n'b' $()", "--ai-tag=false"}},
		{"identity user", []string{"chat", "+messages-send", "--as=user", "--text", "bot"}, []string{"chat", "+messages-send", "--as=user", "--text", "bot", "--ai-tag=false"}},
		{"identity defaults user", []string{"chat", "+messages-send", "--markdown", "--as=bot"}, []string{"chat", "+messages-send", "--markdown", "--as=bot", "--ai-tag=false"}},
		{"identity bot", []string{"chat", "+messages-send", "--as", "bot", "--text", "user"}, nil},
		{"identity webhook", []string{"chat", "+messages-send", "--identity=webhook", "--text", "user"}, nil},
		{"help", []string{"chat", "message", "send", "--help"}, nil},
		{"short help", []string{"chat", "message", "send", "-vh"}, nil},
		{"help command", []string{"help", "chat", "message", "send"}, nil},
		{"read with command content", []string{"chat", "message", "list", "--content", "send"}, nil},
		{"other product", []string{"calendar", "send", "--content", "chat"}, nil},
		{"bot atomic", []string{"chat", "message", "send-by-bot", "--text", "hello"}, nil},
		{"unknown before command", []string{"--new-flag", "chat", "message", "send"}, nil},
		{"terminator before command", []string{"--", "chat", "message", "send"}, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			want := tt.want
			if want == nil {
				want = tt.args
			}
			if got := RewriteDWSSendAITag(tt.args, &protocol.DingTalkMessagePolicy{}); !reflect.DeepEqual(got, want) {
				t.Fatalf("got %#v, want %#v", got, want)
			}
			if got := RewriteDWSSendAITag(tt.args, nil); !reflect.DeepEqual(got, tt.args) {
				t.Fatalf("absent policy changed argv: %#v", got)
			}
		})
	}
}

func TestRewriteDWSOriginReplyQuotesOriginConversation(t *testing.T) {
	t.Parallel()
	policy := &protocol.DingTalkMessagePolicy{ReplyToOpenMsgID: "msg-origin", ReplyConversationID: "cid-origin"}
	got := RewriteDWSOriginReply([]string{"chat", "message", "send", "--conversation-id", "cid-origin", "--content", "结论", "--idempotency-key", "k", "--ai-tag=false"}, policy)
	want := []string{"chat", "+messages-reply", "--group", "cid-origin", "--message-id", "msg-origin", "--content", "结论", "--idempotency-key", "k", "--ai-tag=false", "--yes", "--format", "json"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
	outreach := []string{"chat", "message", "send", "--conversation-id", "cid-other", "--content", "请问一下"}
	if got := RewriteDWSOriginReply(outreach, policy); !reflect.DeepEqual(got, outreach) {
		t.Fatalf("rewrote outreach: %#v", got)
	}
	already := []string{"chat", "+messages-reply", "--group", "cid-origin", "--message-id", "msg-other", "--content", "ok"}
	if got := RewriteDWSOriginReply(already, policy); !reflect.DeepEqual(got, already) {
		t.Fatalf("rewrote explicit reply: %#v", got)
	}
}

func TestDWSMessagePolicySupportedEntryPoints(t *testing.T) {
	t.Parallel()
	for _, command := range []string{"chat message send", "chat message reply", "chat send", "chat reply", "chat +send", "chat +dm", "chat +send-to-group", "chat +messages-send", "chat +messages-reply"} {
		t.Run(command, func(t *testing.T) {
			args := append(strings.Fields(command), "--content", "send", "--ai-tag=false")
			got := RewriteDWSSendAITag(args, &protocol.DingTalkMessagePolicy{ShowAITag: true})
			if got[len(got)-1] != "--ai-tag=true" {
				t.Fatalf("policy not enforced: %v", got)
			}
		})
	}
}

// This runs a real test-created executable, so argv boundaries are checked
// across os/exec as well as in the parser. No installed DWS or agent runs.
func TestDWSMessagePolicyFakeCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix CLI fixture")
	}
	t.Parallel()
	fake := filepath.Join(t.TempDir(), "dws")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	for _, show := range []bool{false, true} {
		var output bytes.Buffer
		env := map[string]string{}
		ApplyDWSMessagePolicyEnv(env, &protocol.DingTalkMessagePolicy{ShowAITag: show})
		args := []string{"chat", "message", "send", "--user", "test-user", "--content", "## 内容\n--ai-tag=true '$(false)'", "--ai-tag", "--idempotency-key", "stable-key"}
		code := RunDWSWrap(DWSWrapDeps{
			Args: args, Stdout: &output, Stderr: &bytes.Buffer{},
			Getenv:   func(key string) string { return env[key] },
			LookPath: func(string) (string, error) { return fake, nil },
			Run: func(name string, argv []string, stdout, stderr io.Writer) error {
				cmd := exec.Command(name, argv...)
				cmd.Env = []string{"PATH=/usr/bin:/bin"}
				cmd.Stdout, cmd.Stderr = stdout, stderr
				return cmd.Run()
			},
		})
		if code != 0 {
			t.Fatalf("exit=%d", code)
		}
		got := strings.Split(strings.TrimSuffix(output.String(), "\x00"), "\x00")
		if want := RewriteDWSSendAITag(args, &protocol.DingTalkMessagePolicy{ShowAITag: show}); !reflect.DeepEqual(got, want) {
			t.Fatalf("got %#v, want %#v", got, want)
		}
	}
}

func TestDWSMessagePolicyInvalidSnapshotDoesNotSend(t *testing.T) {
	t.Parallel()
	called := false
	code := RunDWSWrap(DWSWrapDeps{
		Args:   []string{"chat", "message", "send", "--user", "fixture"},
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		Getenv:   func(string) string { return "broken" },
		LookPath: func(string) (string, error) { called = true; return "", nil },
	})
	if code != 64 || called {
		t.Fatalf("invalid policy executed send: exit=%d, called=%v", code, called)
	}
}

func TestDWSMessagePolicyWarmTaskIsolation(t *testing.T) {
	t.Parallel()
	env := map[string]string{DWSMessagePolicyEnv: "untrusted-custom-env"}
	ApplyDWSMessagePolicyEnv(env, &protocol.DingTalkMessagePolicy{ShowAITag: true})
	first, err := dwsMessagePolicyFromEnv(func(k string) string { return env[k] })
	if err != nil || first == nil || !first.ShowAITag {
		t.Fatalf("first policy: %#v, %v", first, err)
	}
	ApplyDWSMessagePolicyEnv(env, &protocol.DingTalkMessagePolicy{})
	second, err := dwsMessagePolicyFromEnv(func(k string) string { return env[k] })
	if err != nil || second == nil || second.ShowAITag {
		t.Fatalf("second policy: %#v, %v", second, err)
	}
	ApplyDWSMessagePolicyEnv(env, nil)
	if value, exists := env[DWSMessagePolicyEnv]; !exists || value != "" {
		t.Fatalf("legacy task did not mask stale inherited policy: %v", env)
	}
}

func TestDWSMessagePolicyDoesNotObserveBotWebhookOrPreview(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"chat", "+messages-send", "--as=bot", "--text", "fixture"},
		{"chat", "+messages-send", "--identity=webhook", "--text", "fixture"},
		{"chat", "message", "send", "--user", "fixture", "--dry-run"},
		{"chat", "message", "send", "--user", "fixture", "--mock"},
	} {
		calls, binds := 0, 0
		code := RunDWSWrap(DWSWrapDeps{
			Args: args, Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
			Getenv:   func(string) string { return `{"show_ai_tag":false}` },
			LookPath: func(string) (string, error) { return "/unused-fixture", nil },
			Run: func(_ string, _ []string, stdout, _ io.Writer) error {
				calls++
				_, err := io.WriteString(stdout, `{"openConversationId":"fixture-scene","openTaskId":"fixture-task"}`)
				return err
			},
			Bind: func(string, string) error { binds++; return nil },
		})
		if code != 0 || calls != 1 || binds != 0 {
			t.Fatalf("unexpected side effects for %v: exit=%d, calls=%d, binds=%d", args, code, calls, binds)
		}
	}
}

func TestDWSMessagePolicyStatusKeepsIdentity(t *testing.T) {
	t.Parallel()
	var statusArgs []string
	code := RunDWSWrap(DWSWrapDeps{
		Args:   []string{"--profile", "fixture-org", "chat", "message", "send", "--user", "fixture-user"},
		Stdout: &bytes.Buffer{}, Stderr: &bytes.Buffer{},
		LookPath: func(string) (string, error) { return "/unused-fixture", nil },
		Run: func(_ string, args []string, stdout, _ io.Writer) error {
			if len(args) > 2 && args[2] == "query-send-status" {
				statusArgs = args
				return nil
			}
			_, err := io.WriteString(stdout, `{"openTaskId":"fixture-task"}`)
			return err
		},
	})
	if code != 0 || len(statusArgs) < 2 || !reflect.DeepEqual(statusArgs[len(statusArgs)-2:], []string{"--profile", "fixture-org"}) {
		t.Fatalf("status query lost identity: exit=%d, args=%v", code, statusArgs)
	}
}

func TestDWSMessagePolicyShimPinsExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix CLI fixture")
	}
	t.Parallel()
	root := t.TempDir()
	self := filepath.Join(root, "multica 'fixture'")
	if err := os.WriteFile(self, []byte("#!/bin/sh\nprintf '%s\\000' \"$@\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	shim, err := ensureDWSShim(root, self)
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(filepath.Join(shim, "dws"), "chat", "+dm", "--content", "hello '世'\n界")
	cmd.Env = []string{"PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	want := []string{DWSWrapArg, "--", "chat", "+dm", "--content", "hello '世'\n界"}
	if got := strings.Split(strings.TrimSuffix(string(out), "\x00"), "\x00"); !reflect.DeepEqual(got, want) {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}
