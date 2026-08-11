package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func newChatParentTestCommand() *cobra.Command {
	cmd := newChatCommand()
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.AddCommand(
		&cobra.Command{Use: "history", Run: func(*cobra.Command, []string) {}},
		&cobra.Command{Use: "thread [id]", Run: func(*cobra.Command, []string) {}},
	)
	return cmd
}

func TestChatCommandWithoutSubcommandShowsHelp(t *testing.T) {
	cmd := newChatParentTestCommand()
	var output bytes.Buffer
	cmd.SetOut(&output)

	if err := cmd.Execute(); err != nil {
		t.Fatalf("chat command returned an error: %v", err)
	}
	for _, command := range []string{"history", "thread"} {
		if !strings.Contains(output.String(), command) {
			t.Fatalf("chat help does not list %q:\n%s", command, output.String())
		}
	}
}

func TestChatCommandRejectsUnknownSubcommands(t *testing.T) {
	for _, unknown := range []string{"list", "messages", "list/messages"} {
		t.Run(unknown, func(t *testing.T) {
			cmd := newChatParentTestCommand()
			cmd.SetArgs([]string{unknown})

			err := cmd.Execute()
			if err == nil {
				t.Fatalf("chat %s returned success", unknown)
			}
			for _, expected := range []string{
				`unknown command "` + unknown + `" for "chat"`,
				"multica chat history",
				"multica chat thread [id]",
			} {
				if !strings.Contains(err.Error(), expected) {
					t.Fatalf("error %q does not contain %q", err, expected)
				}
			}
		})
	}
}
