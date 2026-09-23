package service

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/multica-ai/multica/server/internal/dshhost"
)

func TestWriteHostTemplateDoesNotRequireEmployeeHost(t *testing.T) {
	got, err := preferWriteHostTemplate("stable-dsh", "")
	if err != nil || got != "stable-dsh" {
		t.Fatalf("stable got=%q err=%v", got, err)
	}
	got, err = preferWriteHostTemplate("", "employee-image")
	if err != nil || got != "employee-image" {
		t.Fatalf("employee got=%q err=%v", got, err)
	}
	got, err = preferWriteHostTemplate(" stable-dsh ", "employee-image")
	if err != nil || got != "stable-dsh" {
		t.Fatalf("prefer stable got=%q err=%v", got, err)
	}
	if _, err := preferWriteHostTemplate("  ", ""); err == nil {
		t.Fatal("expected missing template")
	}
}

func TestReleaseWriteHostWaitsForConfirmedAbsence(t *testing.T) {
	released := false
	err := releaseWriteHostAfterDestroy("sbx-old", func(string) error {
		return errors.New("absence unconfirmed")
	}, func() error {
		released = true
		return nil
	})
	if err == nil || released {
		t.Fatalf("err=%v released=%v", err, released)
	}
	if !strings.Contains(err.Error(), "destroy unconfirmed") {
		t.Fatal(err)
	}

	destroyed := false
	err = releaseWriteHostAfterDestroy("", func(string) error {
		destroyed = true
		return errors.New("should not run")
	}, func() error {
		released = true
		return nil
	})
	if err != nil || destroyed || !released {
		t.Fatalf("empty id err=%v destroyed=%v released=%v", err, destroyed, released)
	}
}

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
