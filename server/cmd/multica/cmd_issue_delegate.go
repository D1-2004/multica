package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var issueDelegateCmd = &cobra.Command{
	Use:   "delegate",
	Short: "Delegate the current chat task to an issue-backed background run",
	Long: "Delegate the current task to a new or existing issue while preserving its server-side identity and completion context. " +
		"This command is only available inside a task-scoped Agent execution.",
	RunE: runIssueDelegate,
}

func registerIssueDelegateFlags(cmd *cobra.Command) {
	cmd.Flags().String("task-id", "", "Source task UUID (defaults to MULTICA_TASK_ID and must match it)")
	cmd.Flags().String("issue", "", "Existing issue ID or identifier to continue instead of creating a new issue")
	cmd.Flags().String("title", "", "Stable semantic title for a new issue")
	cmd.Flags().String("description", "", "New issue task description")
	cmd.Flags().Bool("description-stdin", false, "Read new issue task description from stdin")
	cmd.Flags().String("description-file", "", "Read new issue task description from a UTF-8 file inside the task workdir")
	cmd.Flags().String("content", "", "Follow-up content when continuing an existing issue")
	cmd.Flags().Bool("content-stdin", false, "Read existing-issue follow-up content from stdin")
	cmd.Flags().String("content-file", "", "Read existing-issue follow-up content from a UTF-8 file inside the task workdir")
	cmd.Flags().Bool("allow-external-file", false, "Allow description/content files outside the current task workdir")
	cmd.Flags().String("assignee", "", "Agent assignee name for a new issue (fuzzy match)")
	cmd.Flags().String("assignee-id", "", "Agent assignee UUID for a new issue")
	cmd.Flags().String("priority", "", "New issue priority")
	cmd.Flags().String("status", "", "New issue target status (defaults to todo)")
	cmd.Flags().String("output", "json", "Output format: json or table")
}

func init() {
	registerIssueDelegateFlags(issueDelegateCmd)
	issueCmd.AddCommand(issueDelegateCmd)
}

func runIssueDelegate(cmd *cobra.Command, _ []string) error {
	injectedTaskID := strings.TrimSpace(os.Getenv("MULTICA_TASK_ID"))
	if injectedTaskID == "" {
		return fmt.Errorf("issue delegation requires a task-scoped Agent execution with MULTICA_TASK_ID")
	}
	taskID, _ := cmd.Flags().GetString("task-id")
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		taskID = injectedTaskID
	}
	if taskID != injectedTaskID {
		return fmt.Errorf("--task-id must match the daemon-injected MULTICA_TASK_ID")
	}

	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	body := map[string]any{"source_task_id": taskID}
	issueRef, _ := cmd.Flags().GetString("issue")
	if strings.TrimSpace(issueRef) != "" {
		issue, err := resolveIssueRef(ctx, client, issueRef)
		if err != nil {
			return fmt.Errorf("resolve existing issue: %w", err)
		}
		content, hasContent, err := resolveTextFlag(cmd, "content")
		if err != nil {
			return err
		}
		if !hasContent || strings.TrimSpace(content) == "" {
			return fmt.Errorf("--content, --content-stdin, or --content-file is required with --issue")
		}
		if cmd.Flags().Changed("title") || cmd.Flags().Changed("description") ||
			cmd.Flags().Changed("description-stdin") || cmd.Flags().Changed("description-file") ||
			cmd.Flags().Changed("assignee") || cmd.Flags().Changed("assignee-id") {
			return fmt.Errorf("--issue continuation cannot be combined with title, description, or assignee flags")
		}
		body["mode"] = "continue"
		body["issue_id"] = issue.ID
		body["content"] = content
	} else {
		title, _ := cmd.Flags().GetString("title")
		if strings.TrimSpace(title) == "" {
			return fmt.Errorf("--title is required when creating a delegated issue")
		}
		description, hasDescription, err := resolveTextFlag(cmd, "description")
		if err != nil {
			return err
		}
		if !hasDescription || strings.TrimSpace(description) == "" {
			return fmt.Errorf("--description, --description-stdin, or --description-file is required when creating a delegated issue")
		}
		assigneeType, assigneeID, hasAssignee, err := pickAssigneeFromFlags(
			ctx,
			client,
			cmd,
			"assignee",
			"assignee-id",
			assigneeKinds{agent: true},
		)
		if err != nil {
			return fmt.Errorf("resolve assignee: %w", err)
		}
		if !hasAssignee || assigneeType != "agent" {
			return fmt.Errorf("--assignee or --assignee-id must identify an Agent")
		}

		body["mode"] = "create"
		body["title"] = title
		body["description"] = description
		body["assignee_id"] = assigneeID
		if status, _ := cmd.Flags().GetString("status"); status != "" {
			if err := validateIssueStatus(status); err != nil {
				return err
			}
			body["status"] = status
		}
		if priority, _ := cmd.Flags().GetString("priority"); priority != "" {
			if err := validateIssuePriority(priority); err != nil {
				return err
			}
			body["priority"] = priority
		}
	}

	var result map[string]any
	if err := client.PostJSON(ctx, "/api/issue-delegations", body, &result); err != nil {
		return fmt.Errorf("delegate issue: %w", err)
	}
	releaseParent, ok := result["release_parent"].(bool)
	if !ok || !releaseParent {
		return fmt.Errorf("delegate issue: server did not confirm a committed background handoff")
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "table" {
		cli.PrintTable(os.Stdout, []string{"ISSUE", "TASK", "QUEUED", "RELEASE PARENT"}, [][]string{{
			strVal(result, "issue_identifier"),
			strVal(result, "target_task_id"),
			fmt.Sprint(result["queued"]),
			fmt.Sprint(result["release_parent"]),
		}})
		return nil
	}
	return cli.PrintJSON(os.Stdout, result)
}
