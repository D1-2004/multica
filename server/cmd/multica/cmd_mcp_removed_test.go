package main

import "testing"

func TestRootCommandDoesNotRegisterMCPCLI(t *testing.T) {
	for _, command := range rootCmd.Commands() {
		if command.Name() == "mcp" {
			t.Fatal("multica mcp must be removed; task MCP is mounted through native runtime configuration")
		}
	}
}
