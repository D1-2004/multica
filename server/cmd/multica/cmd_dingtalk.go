package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var dingtalkCmd = &cobra.Command{
	Use:   "dingtalk",
	Short: "Manage DingTalk integrations",
}

var dingtalkInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Install a DingTalk bot for an agent",
}

var dingtalkInstallBeginCmd = &cobra.Command{
	Use:   "begin",
	Short: "Start the DingTalk QR-code installation flow",
	RunE:  runDingTalkInstallBegin,
}

var dingtalkInstallStatusCmd = &cobra.Command{
	Use:   "status <session-id>",
	Short: "Get the status of a DingTalk installation session",
	Args:  exactArgs(1),
	RunE:  runDingTalkInstallStatus,
}

func init() {
	dingtalkCmd.AddCommand(dingtalkInstallCmd)
	dingtalkInstallCmd.AddCommand(dingtalkInstallBeginCmd)
	dingtalkInstallCmd.AddCommand(dingtalkInstallStatusCmd)

	dingtalkInstallBeginCmd.Flags().String("agent-id", "", "Agent ID to bind to the DingTalk bot (required)")
	dingtalkInstallBeginCmd.Flags().Bool("allow-unbound", false, "Allow external users to use the bot without binding a Multica account")
	dingtalkInstallBeginCmd.Flags().String("output", "json", "Output format: json")
	dingtalkInstallStatusCmd.Flags().String("output", "json", "Output format: json")
}

func runDingTalkInstallBegin(cmd *cobra.Command, _ []string) error {
	agentID, _ := cmd.Flags().GetString("agent-id")
	if strings.TrimSpace(agentID) == "" {
		return fmt.Errorf("--agent-id is required")
	}
	if output, _ := cmd.Flags().GetString("output"); output != "json" {
		return fmt.Errorf("invalid --output %q: must be json", output)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return err
	}
	allowUnbound, _ := cmd.Flags().GetBool("allow-unbound")

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	query := url.Values{"agent_id": {strings.TrimSpace(agentID)}}
	if allowUnbound {
		query.Set("allow_unbound", "true")
	}
	path := fmt.Sprintf("/api/workspaces/%s/dingtalk/install/begin?%s", url.PathEscape(workspaceID), query.Encode())
	var result map[string]any
	if err := client.PostJSON(ctx, path, map[string]any{}, &result); err != nil {
		return fmt.Errorf("begin DingTalk install: %w", err)
	}
	return cli.PrintJSON(os.Stdout, result)
}

func runDingTalkInstallStatus(cmd *cobra.Command, args []string) error {
	if output, _ := cmd.Flags().GetString("output"); output != "json" {
		return fmt.Errorf("invalid --output %q: must be json", output)
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return err
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	path := fmt.Sprintf("/api/workspaces/%s/dingtalk/install/%s/status", url.PathEscape(workspaceID), url.PathEscape(args[0]))
	var result map[string]any
	if err := client.GetJSON(ctx, path, &result); err != nil {
		return fmt.Errorf("get DingTalk install status: %w", err)
	}
	return cli.PrintJSON(os.Stdout, result)
}
