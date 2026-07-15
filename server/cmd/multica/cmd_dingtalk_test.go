package main

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/spf13/cobra"
)

func newDingTalkTestCommand(use string) *cobra.Command {
	cmd := &cobra.Command{Use: use}
	addTemplateTestFlags(cmd)
	cmd.Flags().Bool("allow-unbound", false, "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func TestDingTalkInstallCommandsRegistered(t *testing.T) {
	for _, name := range []string{"begin", "status"} {
		cmd, _, err := dingtalkCmd.Find([]string{"install", name})
		if err != nil || cmd == nil || cmd.Name() != name {
			t.Fatalf("dingtalk install %s not registered: %v / %#v", name, err, cmd)
		}
	}
	if flag := dingtalkInstallBeginCmd.Flags().Lookup("allow-unbound"); flag == nil {
		t.Fatal("dingtalk install begin --allow-unbound flag not registered")
	}
}

func TestRunDingTalkInstallBegin(t *testing.T) {
	var gotMethod, gotPath, gotAgentID, gotAllowUnbound string
	server := templateTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAgentID = r.URL.Query().Get("agent_id")
		gotAllowUnbound = r.URL.Query().Get("allow_unbound")
		_ = json.NewEncoder(w).Encode(map[string]any{"session_id": "session-1", "qr_code_url": "https://example.test/qr"})
	})
	defer server.Close()

	cmd := newDingTalkTestCommand("begin")
	cmd.Flags().String("agent-id", "", "")
	_ = cmd.Flags().Set("agent-id", "agent-1")
	if err := runDingTalkInstallBegin(cmd, nil); err != nil {
		t.Fatalf("runDingTalkInstallBegin: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/workspaces/ws-123/dingtalk/install/begin" || gotAgentID != "agent-1" {
		t.Fatalf("request = %s %s agent_id=%q", gotMethod, gotPath, gotAgentID)
	}
	if gotAllowUnbound != "" {
		t.Fatalf("allow_unbound = %q, want omitted", gotAllowUnbound)
	}
}

func TestRunDingTalkInstallBeginAllowUnbound(t *testing.T) {
	var gotAllowUnbound string
	server := templateTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		gotAllowUnbound = r.URL.Query().Get("allow_unbound")
		_ = json.NewEncoder(w).Encode(map[string]any{"session_id": "session-1", "qr_code_url": "https://example.test/qr"})
	})
	defer server.Close()

	cmd := newDingTalkTestCommand("begin")
	cmd.Flags().String("agent-id", "", "")
	_ = cmd.Flags().Set("agent-id", "agent-1")
	_ = cmd.Flags().Set("allow-unbound", "true")
	if err := runDingTalkInstallBegin(cmd, nil); err != nil {
		t.Fatalf("runDingTalkInstallBegin: %v", err)
	}
	if gotAllowUnbound != "true" {
		t.Fatalf("allow_unbound = %q, want true", gotAllowUnbound)
	}
}

func TestRunDingTalkInstallStatus(t *testing.T) {
	var gotMethod, gotPath string
	server := templateTestEnv(t, func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending"})
	})
	defer server.Close()

	cmd := newDingTalkTestCommand("status")
	if err := runDingTalkInstallStatus(cmd, []string{"session-1"}); err != nil {
		t.Fatalf("runDingTalkInstallStatus: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/workspaces/ws-123/dingtalk/install/session-1/status" {
		t.Fatalf("request = %s %s", gotMethod, gotPath)
	}
}
