package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

func TestChatReplyTemplateInstallAndRemove(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	source := filepath.Join(t.TempDir(), "reply.sh")
	if err := os.WriteFile(source, []byte("#!/bin/sh\nexit 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.Flags().String("script", "", "")
	_ = cmd.Flags().Set("script", source)
	if _, err := captureStdout(t, func() error {
		return runChatReplyTemplateInstall(cmd, []string{"dws-reply"})
	}); err != nil {
		t.Fatalf("install: %v", err)
	}
	installed := filepath.Join(home, ".multica", "chat-reply-templates", "dws-reply", "run")
	info, err := os.Stat(installed)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("installed mode = %o, want 700", info.Mode().Perm())
	}

	if err := runChatReplyTemplateRemove(&cobra.Command{}, []string{"dws-reply"}); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := os.Stat(installed); !os.IsNotExist(err) {
		t.Fatalf("installed template still exists: %v", err)
	}
}

func TestReplyTemplateNameRejectsTraversal(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	if _, err := installedReplyTemplatePath("../escape"); err == nil {
		t.Fatal("expected path traversal name to be rejected")
	}
}
