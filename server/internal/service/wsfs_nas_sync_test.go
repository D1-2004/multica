package service

import (
	"context"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/dshhost"
)

func TestSharedNASWriteUsesUserOnSharedRoot(t *testing.T) {
	runner := &fakeCommandRunner{out: []string{"{}"}}
	l := &FCE2BLauncher{Runner: runner}
	if err := l.writeSharedNASFile(context.Background(), "sbx-write", "notes/a.txt", []byte("hi")); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls=%d", len(runner.calls))
	}
	args := strings.Join(runner.calls[0].args, " ")
	if !strings.Contains(args, "sandbox exec --user user sbx-write") {
		t.Fatalf("exec: %s", args)
	}
	if !strings.Contains(args, dshhost.WorkspaceSharedRoot+"/notes/a.txt") {
		t.Fatalf("path: %s", args)
	}
	if strings.Contains(args, "--user root") || strings.Contains(args, "/mnt/multica") {
		t.Fatalf("write used the wrong mount or user: %s", args)
	}
}

func TestSharedNASWriteChunks(t *testing.T) {
	runner := &fakeCommandRunner{out: []string{"{}", "{}"}}
	l := &FCE2BLauncher{Runner: runner}
	data := make([]byte, wsfsWriteChunkBytes+3)
	if err := l.writeSharedNASFile(context.Background(), "sbx-write", "big.bin", data); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 2 {
		t.Fatalf("calls=%d", len(runner.calls))
	}
	if !strings.Contains(strings.Join(runner.calls[0].args, " "), `"wb"`) {
		t.Fatal("first chunk should truncate")
	}
	if !strings.Contains(strings.Join(runner.calls[1].args, " "), `"ab"`) {
		t.Fatal("second chunk should append")
	}
}
