package main

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var chatListCmd = &cobra.Command{
	Use:   "list",
	Short: "List your Chat Sessions",
	Args:  cobra.NoArgs,
	RunE:  runChatList,
}

var chatGetCmd = &cobra.Command{
	Use:   "get <session-id>",
	Short: "Get a Chat Session by UUID or short prefix",
	Args:  cobra.ExactArgs(1),
	RunE:  runChatGet,
}

var chatMessagesCmd = &cobra.Command{
	Use:   "messages <session-id>",
	Short: "List messages in a Chat Session",
	Args:  cobra.ExactArgs(1),
	RunE:  runChatMessages,
}

var chatStartCmd = &cobra.Command{
	Use:   "start --agent <name-or-id> [--title <title>] <message|->",
	Short: "Start a Chat Session and send its first message",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runChatStart,
}

var chatSendCmd = &cobra.Command{
	Use:   "send <session-id> <message|->",
	Short: "Send a message to a Chat Session",
	Args:  cobra.MinimumNArgs(2),
	RunE:  runChatSend,
}

func init() {
	for _, c := range []*cobra.Command{chatListCmd, chatGetCmd, chatMessagesCmd} {
		c.Flags().String("output", "table", "Output format: table or json")
	}
	chatStartCmd.Flags().String("agent", "", "Agent name or UUID")
	chatStartCmd.Flags().String("title", "", "Optional Session title")
	chatCmd.AddCommand(chatListCmd, chatGetCmd, chatMessagesCmd, chatStartCmd, chatSendCmd)
}

func runChatList(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	sessions, err := fetchChatSessions(ctx, client)
	if err != nil {
		return err
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, sessions)
	}
	rows := make([][]string, 0, len(sessions))
	for _, session := range sessions {
		rows = append(rows, []string{
			strVal(session, "id"),
			strVal(session, "agent_id"),
			strVal(session, "status"),
			strVal(session, "title"),
			strVal(session, "updated_at"),
		})
	}
	cli.PrintTable(os.Stdout, []string{"ID", "AGENT", "STATUS", "TITLE", "UPDATED"}, rows)
	return nil
}

func runChatGet(cmd *cobra.Command, args []string) error {
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
	var session map[string]any
	if err := client.GetJSON(ctx, "/api/chat/sessions/"+url.PathEscape(sessionID), &session); err != nil {
		return fmt.Errorf("get chat session: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, session)
	}
	cli.PrintTable(os.Stdout, []string{"ID", "AGENT", "STATUS", "TITLE", "CREATED", "UPDATED"}, [][]string{{
		strVal(session, "id"), strVal(session, "agent_id"), strVal(session, "status"),
		strVal(session, "title"), strVal(session, "created_at"), strVal(session, "updated_at"),
	}})
	return nil
}

func runChatMessages(cmd *cobra.Command, args []string) error {
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
	var messages []map[string]any
	path := "/api/chat/sessions/" + url.PathEscape(sessionID) + "/messages"
	if err := client.GetJSON(ctx, path, &messages); err != nil {
		return fmt.Errorf("list chat messages: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, messages)
	}
	rows := make([][]string, 0, len(messages))
	for _, message := range messages {
		rows = append(rows, []string{
			strVal(message, "role"), strVal(message, "content"),
			strVal(message, "task_id"), strVal(message, "created_at"),
		})
	}
	cli.PrintTable(os.Stdout, []string{"ROLE", "CONTENT", "TASK", "CREATED"}, rows)
	return nil
}

func runChatStart(cmd *cobra.Command, args []string) error {
	agentRef, _ := cmd.Flags().GetString("agent")
	agentRef = strings.TrimSpace(agentRef)
	if agentRef == "" {
		return fmt.Errorf("--agent is required")
	}
	message, err := readChatMessage(cmd, args)
	if err != nil {
		return err
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	agentID, err := resolveAgent(ctx, client, agentRef)
	if err != nil {
		return fmt.Errorf("resolve chat agent: %w", err)
	}
	title, _ := cmd.Flags().GetString("title")
	var session map[string]any
	if err := client.PostJSON(ctx, "/api/chat/sessions", map[string]any{
		"agent_id": agentID,
		"title":    strings.TrimSpace(title),
	}, &session); err != nil {
		return fmt.Errorf("create chat session: %w", err)
	}
	sessionID := strVal(session, "id")
	if sessionID == "" {
		return fmt.Errorf("create chat session: response did not include session id")
	}
	sent, err := sendChatSessionMessage(ctx, client, sessionID, message)
	if err != nil {
		return fmt.Errorf("chat session %s was created, but sending its first message failed: %w", sessionID, err)
	}
	return cli.PrintJSON(os.Stdout, chatSendOutput(sessionID, sent))
}

func runChatSend(cmd *cobra.Command, args []string) error {
	message, err := readChatMessage(cmd, args[1:])
	if err != nil {
		return err
	}
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
	sent, err := sendChatSessionMessage(ctx, client, sessionID, message)
	if err != nil {
		return err
	}
	return cli.PrintJSON(os.Stdout, chatSendOutput(sessionID, sent))
}

func fetchChatSessions(ctx context.Context, client *cli.APIClient) ([]map[string]any, error) {
	var sessions []map[string]any
	if err := client.GetJSON(ctx, "/api/chat/sessions", &sessions); err != nil {
		return nil, fmt.Errorf("list chat sessions: %w", err)
	}
	return sessions, nil
}

func resolveChatSessionID(ctx context.Context, client *cli.APIClient, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if uuidRegexp.MatchString(ref) {
		return ref, nil
	}
	if len(ref) < 4 {
		return "", fmt.Errorf("chat session prefix must contain at least 4 characters")
	}
	sessions, err := fetchChatSessions(ctx, client)
	if err != nil {
		return "", err
	}
	lower := strings.ToLower(ref)
	var matches []string
	for _, session := range sessions {
		id := strVal(session, "id")
		if strings.HasPrefix(strings.ToLower(id), lower) {
			matches = append(matches, id)
		}
	}
	switch len(matches) {
	case 0:
		return "", fmt.Errorf("no chat session found with prefix %q", ref)
	case 1:
		return matches[0], nil
	default:
		return "", fmt.Errorf("chat session prefix %q is ambiguous; matches: %s", ref, strings.Join(matches, ", "))
	}
}

func readChatMessage(cmd *cobra.Command, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("message is required")
	}
	message := strings.Join(args, " ")
	if len(args) == 1 && args[0] == "-" {
		data, err := io.ReadAll(cmd.InOrStdin())
		if err != nil {
			return "", fmt.Errorf("read chat message from stdin: %w", err)
		}
		message = string(data)
	}
	message = strings.TrimSpace(message)
	if message == "" {
		return "", fmt.Errorf("message cannot be empty")
	}
	return message, nil
}

func sendChatSessionMessage(ctx context.Context, client *cli.APIClient, sessionID, content string) (map[string]any, error) {
	var sent map[string]any
	path := "/api/chat/sessions/" + url.PathEscape(sessionID) + "/messages"
	if err := client.PostJSON(ctx, path, map[string]any{"content": content}, &sent); err != nil {
		return nil, fmt.Errorf("send chat message: %w", err)
	}
	return sent, nil
}

func chatSendOutput(sessionID string, sent map[string]any) map[string]any {
	collected, _ := sent["collected"].(bool)
	return map[string]any{
		"session_id": sessionID,
		"message_id": strVal(sent, "message_id"),
		"task_id":    strVal(sent, "task_id"),
		"collected":  collected,
	}
}
