package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

// dtaLoadSmokeCmd is an internal transport used by dingtalk-agent. Keeping it
// hidden avoids presenting the Issue-backed implementation as a public Multica
// workflow while still preserving named-profile authentication in one place.
var dtaLoadSmokeCmd = &cobra.Command{
	Use:    "dta-load-smoke",
	Short:  "Run DTA deployment load verification",
	Hidden: true,
}

var dtaLoadSmokeCreateCmd = &cobra.Command{
	Use:   "create",
	Short: "Create a DTA load verification",
	RunE:  runDTALoadSmokeCreate,
}

var dtaLoadSmokeRunsCmd = &cobra.Command{
	Use:  "runs <issue-id>",
	Args: cobra.ExactArgs(1),
	RunE: runDTALoadSmokeRuns,
}

var dtaLoadSmokeMessagesCmd = &cobra.Command{
	Use:  "messages <issue-id> <task-id>",
	Args: cobra.ExactArgs(2),
	RunE: runDTALoadSmokeMessages,
}

var dtaLoadSmokeCommentsCmd = &cobra.Command{
	Use:  "comments <issue-id>",
	Args: cobra.ExactArgs(1),
	RunE: runDTALoadSmokeComments,
}

var dtaLoadSmokeRetryCmd = &cobra.Command{
	Use:  "retry <issue-id>",
	Args: cobra.ExactArgs(1),
	RunE: runDTALoadSmokeRetry,
}

func init() {
	dtaLoadSmokeCmd.AddCommand(
		dtaLoadSmokeCreateCmd,
		dtaLoadSmokeRunsCmd,
		dtaLoadSmokeMessagesCmd,
		dtaLoadSmokeCommentsCmd,
		dtaLoadSmokeRetryCmd,
	)
	dtaLoadSmokeCreateCmd.Flags().String("agent", "", "Agent UUID")
	dtaLoadSmokeCreateCmd.Flags().String("marker", "", "Unique verification marker")
	dtaLoadSmokeCreateCmd.Flags().StringArray("required-skill", nil, "Required Skill name (repeatable)")
	_ = dtaLoadSmokeCreateCmd.MarkFlagRequired("agent")
	_ = dtaLoadSmokeCreateCmd.MarkFlagRequired("marker")
	dtaLoadSmokeRetryCmd.Flags().String("reason", "", "Retry reason: initial or skills_not_visible")
	_ = dtaLoadSmokeRetryCmd.MarkFlagRequired("reason")
}

func runDTALoadSmokeCreate(cmd *cobra.Command, _ []string) error {
	agentID, _ := cmd.Flags().GetString("agent")
	marker, _ := cmd.Flags().GetString("marker")
	requiredSkills, _ := cmd.Flags().GetStringArray("required-skill")
	return runDTALoadSmokeJSON(cmd, "POST", "/api/dta/load-smokes", map[string]any{
		"agent_id": agentID, "marker": marker, "required_skills": requiredSkills,
	})
}

func runDTALoadSmokeRuns(cmd *cobra.Command, args []string) error {
	return runDTALoadSmokeJSON(cmd, "GET", "/api/dta/load-smokes/"+url.PathEscape(args[0])+"/runs", nil)
}

func runDTALoadSmokeMessages(cmd *cobra.Command, args []string) error {
	path := "/api/dta/load-smokes/" + url.PathEscape(args[0]) + "/runs/" + url.PathEscape(args[1]) + "/messages"
	return runDTALoadSmokeJSON(cmd, "GET", path, nil)
}

func runDTALoadSmokeComments(cmd *cobra.Command, args []string) error {
	return runDTALoadSmokeJSON(cmd, "GET", "/api/dta/load-smokes/"+url.PathEscape(args[0])+"/comments?recent=20", nil)
}

func runDTALoadSmokeRetry(cmd *cobra.Command, args []string) error {
	reason, _ := cmd.Flags().GetString("reason")
	return runDTALoadSmokeJSON(cmd, "POST", "/api/dta/load-smokes/"+url.PathEscape(args[0])+"/retry", map[string]any{"reason": reason})
}

func runDTALoadSmokeJSON(cmd *cobra.Command, method, path string, body any) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	if client.WorkspaceID == "" {
		workspaceID, err := requireWorkspaceID(cmd)
		if err != nil {
			return err
		}
		client.WorkspaceID = workspaceID
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var response any
	switch method {
	case "GET":
		err = client.GetJSON(ctx, path, &response)
	case "POST":
		err = client.PostJSON(ctx, path, body, &response)
	default:
		return fmt.Errorf("unsupported DTA load-smoke method %s", method)
	}
	if err != nil {
		return fmt.Errorf("DTA load verification: %w", err)
	}
	return cli.PrintJSON(os.Stdout, response)
}
