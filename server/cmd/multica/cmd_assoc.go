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

var assocCmd = &cobra.Command{
	Use:   "assoc",
	Short: "Recall Issue/Task associations for a conversation or issue",
}

var assocRecallCmd = &cobra.Command{
	Use:   "recall",
	Short: "Recall related tasks by conversation, issue, and time window",
	RunE:  runAssocRecall,
}

var assocBindCmd = &cobra.Command{
	Use:   "bind",
	Short: "Bind an outbound DingTalk conversation to the current Issue task",
	RunE:  runAssocBind,
}

var assocEventsCmd = &cobra.Command{
	Use:   "events",
	Short: "List scene-tagged events for a DingTalk conversation",
	RunE:  runAssocEvents,
}

func init() {
	assocCmd.AddCommand(assocRecallCmd)
	assocCmd.AddCommand(assocBindCmd)
	assocCmd.AddCommand(assocEventsCmd)
	assocRecallCmd.Flags().String("since", "", "Required time window start (RFC3339 or 24h/48h/7d)")
	assocRecallCmd.Flags().String("until", "", "Optional window end (RFC3339, default now)")
	assocRecallCmd.Flags().String("conversation", "", "DingTalk openConversationId")
	assocRecallCmd.Flags().String("person", "", "DingTalk uid (optional)")
	assocRecallCmd.Flags().String("issue", "", "Issue identifier or UUID")
	assocRecallCmd.Flags().Bool("current-issue", false, "Use MULTICA_ISSUE_ID from the current task")
	assocRecallCmd.Flags().String("agent-id", "", "Agent UUID (required unless running inside a task)")
	assocRecallCmd.Flags().String("q", "", "Keyword filter on task purpose")
	assocRecallCmd.Flags().String("intent", "", "Optional intent filter")
	assocRecallCmd.Flags().Int("limit", 20, "Maximum results (max 50)")
	assocRecallCmd.Flags().String("output", "json", "Output format: json")

	assocBindCmd.Flags().String("conversation", "", "DingTalk openConversationId (required)")
	assocBindCmd.Flags().String("evidence", "", "Optional message id for dedup")
	assocBindCmd.Flags().String("person", "", "Optional DingTalk uid")
	assocBindCmd.Flags().String("kind", "", "Conversation kind (dm or group); required for a conversation the agent has no scene for yet, never guessed")
	assocBindCmd.Flags().String("purpose", "", "Optional precise purpose when creating the Issue task node")
	assocBindCmd.Flags().String("output", "json", "Output format: json")

	assocEventsCmd.Flags().String("since", "", "Required time window start (RFC3339 or 24h/48h/7d)")
	assocEventsCmd.Flags().String("conversation", "", "DingTalk openConversationId (required)")
	assocEventsCmd.Flags().String("agent-id", "", "Agent UUID (required unless running inside a task)")
	assocEventsCmd.Flags().Int("limit", 20, "Maximum results (max 50)")
	assocEventsCmd.Flags().String("output", "json", "Output format: json")
}

func runAssocBind(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	conversation, _ := cmd.Flags().GetString("conversation")
	if strings.TrimSpace(conversation) == "" {
		return fmt.Errorf("--conversation is required")
	}
	body := map[string]any{
		"conversation_id": strings.TrimSpace(conversation),
	}
	if v, _ := cmd.Flags().GetString("evidence"); strings.TrimSpace(v) != "" {
		body["evidence_id"] = strings.TrimSpace(v)
	}
	if v, _ := cmd.Flags().GetString("person"); strings.TrimSpace(v) != "" {
		body["person_id"] = strings.TrimSpace(v)
	}
	if v, _ := cmd.Flags().GetString("kind"); strings.TrimSpace(v) != "" {
		body["kind"] = strings.TrimSpace(v)
	}
	if v, _ := cmd.Flags().GetString("purpose"); strings.TrimSpace(v) != "" {
		body["purpose"] = strings.TrimSpace(v)
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err := client.PostJSON(ctx, "/api/assoc/bind-outbound", body, &out); err != nil {
		return err
	}
	return cli.PrintJSON(cmd.OutOrStdout(), out)
}

func runAssocRecall(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if client.WorkspaceID == "" {
		if _, err := requireWorkspaceID(cmd); err != nil {
			return err
		}
	}

	since, _ := cmd.Flags().GetString("since")
	if strings.TrimSpace(since) == "" {
		return fmt.Errorf("--since is required (for example 48h or an RFC3339 timestamp)")
	}

	params := url.Values{}
	params.Set("since", since)
	if v, _ := cmd.Flags().GetString("until"); v != "" {
		params.Set("until", v)
	}
	if v, _ := cmd.Flags().GetString("conversation"); v != "" {
		params.Set("conversation_id", v)
	}
	if v, _ := cmd.Flags().GetString("person"); v != "" {
		params.Set("person_id", v)
	}
	issue, _ := cmd.Flags().GetString("issue")
	useCurrent, _ := cmd.Flags().GetBool("current-issue")
	if useCurrent {
		current := strings.TrimSpace(os.Getenv("MULTICA_ISSUE_ID"))
		if current == "" {
			return fmt.Errorf("--current-issue requires MULTICA_ISSUE_ID")
		}
		issue = current
	}
	if issue != "" {
		params.Set("issue", issue)
	}
	if v, _ := cmd.Flags().GetString("q"); v != "" {
		params.Set("q", v)
	}
	if v, _ := cmd.Flags().GetString("intent"); v != "" {
		params.Set("intent", v)
	}
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		params.Set("limit", fmt.Sprintf("%d", v))
	}
	if agentID, _ := cmd.Flags().GetString("agent-id"); agentID != "" {
		params.Set("agent_id", agentID)
	} else if client.AgentID != "" {
		params.Set("agent_id", client.AgentID)
	}

	if params.Get("conversation_id") == "" && params.Get("issue") == "" && params.Get("q") == "" {
		return fmt.Errorf("provide --conversation, --issue, --current-issue, or --q")
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	path := "/api/assoc/recall?" + params.Encode()
	var out map[string]any
	if err := client.GetJSON(ctx, path, &out); err != nil {
		return err
	}
	return cli.PrintJSON(cmd.OutOrStdout(), out)
}

func runAssocEvents(cmd *cobra.Command, _ []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	conversation, _ := cmd.Flags().GetString("conversation")
	if strings.TrimSpace(conversation) == "" {
		return fmt.Errorf("--conversation is required")
	}
	since, _ := cmd.Flags().GetString("since")
	if strings.TrimSpace(since) == "" {
		return fmt.Errorf("--since is required (for example 48h or an RFC3339 timestamp)")
	}
	params := url.Values{}
	params.Set("conversation_id", strings.TrimSpace(conversation))
	params.Set("since", since)
	if v, _ := cmd.Flags().GetInt("limit"); v > 0 {
		params.Set("limit", fmt.Sprintf("%d", v))
	}
	if agentID, _ := cmd.Flags().GetString("agent-id"); agentID != "" {
		params.Set("agent_id", agentID)
	} else if client.AgentID != "" {
		params.Set("agent_id", client.AgentID)
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var out map[string]any
	if err := client.GetJSON(ctx, "/api/assoc/events?"+params.Encode(), &out); err != nil {
		return err
	}
	return cli.PrintJSON(cmd.OutOrStdout(), out)
}
