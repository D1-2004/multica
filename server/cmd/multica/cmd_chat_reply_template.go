package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var replyTemplateNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,99}$`)

var chatTurnCmd = &cobra.Command{
	Use:   "turn <session-id> <turn-id>",
	Short: "Get one Chat Turn, optionally waiting for its terminal status",
	Args:  cobra.ExactArgs(2),
	RunE:  runChatTurn,
}

var chatReplyTemplateCmd = &cobra.Command{
	Use:   "reply-template",
	Short: "Manage sandbox-side final reply templates",
}

var chatReplyTemplateInstallCmd = &cobra.Command{
	Use:   "install <name> --script <path>",
	Short: "Install an executable final reply post-processor",
	Args:  cobra.ExactArgs(1),
	RunE:  runChatReplyTemplateInstall,
}

var chatReplyTemplateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List installed final reply templates",
	Args:  cobra.NoArgs,
	RunE:  runChatReplyTemplateList,
}

var chatReplyTemplateRemoveCmd = &cobra.Command{
	Use:   "remove <name>",
	Short: "Remove an installed final reply template",
	Args:  cobra.ExactArgs(1),
	RunE:  runChatReplyTemplateRemove,
}

func init() {
	chatTurnCmd.Flags().Bool("wait", false, "Wait for a terminal Turn")
	chatTurnCmd.Flags().Bool("deliver", false, "Run the Turn's installed reply template after it becomes terminal")
	chatTurnCmd.Flags().Duration("wait-timeout", 30*time.Minute, "Maximum time to wait")
	chatReplyTemplateInstallCmd.Flags().String("script", "", "Executable script to install")
	chatReplyTemplateCmd.AddCommand(chatReplyTemplateInstallCmd, chatReplyTemplateListCmd, chatReplyTemplateRemoveCmd)
	chatCmd.AddCommand(chatTurnCmd, chatReplyTemplateCmd)
}

func runChatTurn(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	sessionID, err := resolveChatSessionID(ctx, client, args[0])
	if err != nil {
		return err
	}
	wait, _ := cmd.Flags().GetBool("wait")
	deliver, _ := cmd.Flags().GetBool("deliver")
	wait = wait || deliver
	var turn map[string]any
	if wait {
		timeout, _ := cmd.Flags().GetDuration("wait-timeout")
		turn, err = waitForChatTurn(client, sessionID, args[1], timeout)
	} else {
		err = client.GetJSON(ctx, chatTurnPath(sessionID, args[1]), &turn)
	}
	if err != nil {
		return err
	}
	if deliver && strVal(turn, "reply_template") != "" && strVal(turn, "reply_delivery_status") != "delivered" {
		turn, err = executeChatReplyTemplate(client, sessionID, turn)
		if err != nil {
			return err
		}
	}
	return cli.PrintJSON(os.Stdout, turn)
}

func waitForChatTurn(client *cli.APIClient, sessionID, turnID string, timeout time.Duration) (map[string]any, error) {
	if strings.TrimSpace(turnID) == "" {
		return nil, fmt.Errorf("turn id is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		var turn map[string]any
		if err := client.GetJSON(ctx, chatTurnPath(sessionID, turnID), &turn); err != nil {
			return nil, fmt.Errorf("get chat turn: %w", err)
		}
		if isTerminalChatTurn(strVal(turn, "status")) {
			return turn, nil
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("wait for chat turn %s: %w", turnID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func isTerminalChatTurn(status string) bool {
	return status == "completed" || status == "failed" || status == "cancelled"
}

func chatTurnPath(sessionID, turnID string) string {
	return "/api/chat/sessions/" + url.PathEscape(sessionID) + "/turns/" + url.PathEscape(turnID)
}

func replyTemplateRoot() (string, error) {
	root, err := cli.ProfileDir("")
	if err != nil {
		return "", err
	}
	return filepath.Join(root, "chat-reply-templates"), nil
}

func installedReplyTemplatePath(name string) (string, error) {
	if !replyTemplateNamePattern.MatchString(name) {
		return "", fmt.Errorf("invalid reply template name %q", name)
	}
	root, err := replyTemplateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, name, "run"), nil
}

func runChatReplyTemplateInstall(cmd *cobra.Command, args []string) error {
	source, _ := cmd.Flags().GetString("script")
	if strings.TrimSpace(source) == "" {
		return fmt.Errorf("--script is required")
	}
	data, err := os.ReadFile(source)
	if err != nil {
		return fmt.Errorf("read reply template script: %w", err)
	}
	destination, err := installedReplyTemplatePath(args[0])
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create reply template directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(destination), ".run-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o700); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, destination); err != nil {
		return fmt.Errorf("install reply template: %w", err)
	}
	return cli.PrintJSON(os.Stdout, map[string]any{"name": args[0], "path": destination})
}

func runChatReplyTemplateList(_ *cobra.Command, _ []string) error {
	root, err := replyTemplateRoot()
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return cli.PrintJSON(os.Stdout, []string{})
	}
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			if _, err := os.Stat(filepath.Join(root, entry.Name(), "run")); err == nil {
				names = append(names, entry.Name())
			}
		}
	}
	sort.Strings(names)
	return cli.PrintJSON(os.Stdout, names)
}

func runChatReplyTemplateRemove(_ *cobra.Command, args []string) error {
	path, err := installedReplyTemplatePath(args[0])
	if err != nil {
		return err
	}
	if err := os.RemoveAll(filepath.Dir(path)); err != nil {
		return fmt.Errorf("remove reply template: %w", err)
	}
	return nil
}

func executeChatReplyTemplate(client *cli.APIClient, sessionID string, turn map[string]any) (map[string]any, error) {
	name := strVal(turn, "reply_template")
	script, err := installedReplyTemplatePath(name)
	if err != nil {
		return turn, err
	}
	payload, err := json.Marshal(turn)
	if err != nil {
		return turn, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, script)
	command.Stdin = bytes.NewReader(payload)
	command.Env = append(os.Environ(),
		"MULTICA_CHAT_SESSION_ID="+sessionID,
		"MULTICA_CHAT_TURN_ID="+strVal(turn, "id"),
		"MULTICA_CHAT_REPLY_TEMPLATE="+name,
	)
	output, runErr := command.CombinedOutput()
	status := "delivered"
	errorText := ""
	if runErr != nil {
		status = "failed"
		errorText = strings.TrimSpace(string(output))
		if errorText == "" {
			errorText = runErr.Error()
		}
	}
	deliveryPath := chatTurnPath(sessionID, strVal(turn, "id")) + "/delivery"
	var updated map[string]any
	if err := client.PostJSON(context.Background(), deliveryPath, map[string]any{"status": status, "error": errorText}, &updated); err != nil {
		return turn, fmt.Errorf("record reply delivery: %w", err)
	}
	if runErr != nil {
		return updated, fmt.Errorf("reply template %s failed: %s", name, errorText)
	}
	return updated, nil
}
