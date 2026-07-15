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

var agentTemplateCmd = &cobra.Command{Use: "template", Short: "Manage workspace agent templates"}

var agentTemplateListCmd = &cobra.Command{
	Use: "list", Short: "List agent templates in the workspace", RunE: runAgentTemplateList,
}
var agentTemplateGetCmd = &cobra.Command{
	Use: "get <slug>", Short: "Get a workspace agent template", Args: exactArgs(1), RunE: runAgentTemplateGet,
}
var agentTemplateCreateFromGitCmd = &cobra.Command{
	Use: "create-from-git <slug>", Short: "Import a workspace agent template from GitHub", Args: exactArgs(1), RunE: runAgentTemplateCreateFromGit,
}
var agentTemplateSyncCmd = &cobra.Command{
	Use: "sync <slug>", Short: "Synchronize a GitHub-backed workspace agent template", Args: exactArgs(1), RunE: runAgentTemplateSync,
}
var agentTemplateDeleteCmd = &cobra.Command{
	Use: "delete <slug>", Short: "Delete a workspace-managed agent template", Args: exactArgs(1), RunE: runAgentTemplateDelete,
}
var agentCreateFromTemplateCmd = &cobra.Command{
	Use: "create-from-template <slug>", Short: "Create an agent from a workspace template", Args: exactArgs(1), RunE: runAgentCreateFromTemplate,
}

func init() {
	agentCmd.AddCommand(agentTemplateCmd, agentCreateFromTemplateCmd)
	agentTemplateCmd.AddCommand(agentTemplateListCmd, agentTemplateGetCmd, agentTemplateCreateFromGitCmd, agentTemplateSyncCmd, agentTemplateDeleteCmd)
	agentTemplateListCmd.Flags().String("output", "table", "Output format: table or json")
	agentTemplateGetCmd.Flags().String("output", "json", "Output format: table or json")
	agentTemplateCreateFromGitCmd.Flags().String("installation-id", "", "Workspace GitHub installation ID (required)")
	agentTemplateCreateFromGitCmd.Flags().String("repository", "", "GitHub repository as owner/name (required)")
	agentTemplateCreateFromGitCmd.Flags().String("ref", "", "Git ref (defaults to the repository default branch)")
	agentTemplateCreateFromGitCmd.Flags().String("output", "json", "Output format: json or table")
	agentTemplateSyncCmd.Flags().String("output", "json", "Output format: json or table")
	agentCreateFromTemplateCmd.Flags().String("runtime-id", "", "Runtime ID (required)")
	agentCreateFromTemplateCmd.Flags().String("name", "", "Agent name (defaults to the template name)")
	agentCreateFromTemplateCmd.Flags().String("description", "", "Agent description (defaults to the template description)")
	agentCreateFromTemplateCmd.Flags().String("output", "json", "Output format: json or table")
}

func agentTemplatesPath(workspaceID string) string {
	return fmt.Sprintf("/api/workspaces/%s/agent-templates", url.PathEscape(workspaceID))
}

func agentTemplatePath(workspaceID, slug string) string {
	return agentTemplatesPath(workspaceID) + "/" + url.PathEscape(slug)
}

func templateClient(cmd *cobra.Command) (*cli.APIClient, string, error) {
	client, err := newAPIClient(cmd)
	if err != nil {
		return nil, "", err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return nil, "", err
	}
	return client, workspaceID, nil
}

func runAgentTemplateList(cmd *cobra.Command, _ []string) error {
	client, workspaceID, err := templateClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var response struct {
		Templates []map[string]any `json:"templates"`
	}
	if err := client.GetJSON(ctx, agentTemplatesPath(workspaceID), &response); err != nil {
		return fmt.Errorf("list agent templates: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, response.Templates)
	}
	if output != "table" {
		return fmt.Errorf("invalid --output %q: must be table or json", output)
	}
	rows := make([][]string, 0, len(response.Templates))
	for _, item := range response.Templates {
		rows = append(rows, []string{strVal(item, "slug"), strVal(item, "display_name"), strVal(item, "source_type"), strVal(item, "description")})
	}
	cli.PrintTable(os.Stdout, []string{"SLUG", "NAME", "SOURCE", "DESCRIPTION"}, rows)
	return nil
}

func runAgentTemplateGet(cmd *cobra.Command, args []string) error {
	client, workspaceID, err := templateClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var item map[string]any
	if err := client.GetJSON(ctx, agentTemplatePath(workspaceID, args[0]), &item); err != nil {
		return fmt.Errorf("get agent template: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, item)
	}
	if output != "table" {
		return fmt.Errorf("invalid --output %q: must be table or json", output)
	}
	cli.PrintTable(os.Stdout, []string{"SLUG", "NAME", "SOURCE", "DESCRIPTION"}, [][]string{{strVal(item, "slug"), strVal(item, "display_name"), strVal(item, "source_type"), strVal(item, "description")}})
	return nil
}

func runAgentTemplateCreateFromGit(cmd *cobra.Command, args []string) error {
	installationID, _ := cmd.Flags().GetString("installation-id")
	repository, _ := cmd.Flags().GetString("repository")
	if strings.TrimSpace(installationID) == "" {
		return fmt.Errorf("--installation-id is required")
	}
	if strings.TrimSpace(repository) == "" {
		return fmt.Errorf("--repository is required")
	}
	client, workspaceID, err := templateClient(cmd)
	if err != nil {
		return err
	}
	ref, _ := cmd.Flags().GetString("ref")
	body := map[string]any{"slug": args[0], "installation_id": strings.TrimSpace(installationID), "repository": strings.TrimSpace(repository)}
	if strings.TrimSpace(ref) != "" {
		body["ref"] = strings.TrimSpace(ref)
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var result map[string]any
	if err := client.PostJSON(ctx, agentTemplatesPath(workspaceID)+"/github", body, &result); err != nil {
		return fmt.Errorf("create GitHub agent template: %w", err)
	}
	return printTemplateMutation(cmd, result)
}

func runAgentTemplateSync(cmd *cobra.Command, args []string) error {
	client, workspaceID, err := templateClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var result map[string]any
	if err := client.PostJSON(ctx, agentTemplatePath(workspaceID, args[0])+"/sync", map[string]any{}, &result); err != nil {
		return fmt.Errorf("sync agent template: %w", err)
	}
	return printTemplateMutation(cmd, result)
}

func runAgentTemplateDelete(cmd *cobra.Command, args []string) error {
	client, workspaceID, err := templateClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	if err := client.DeleteJSON(ctx, agentTemplatePath(workspaceID, args[0])); err != nil {
		return fmt.Errorf("delete agent template: %w", err)
	}
	fmt.Fprintf(os.Stdout, "Deleted agent template %s\n", args[0])
	return nil
}

func runAgentCreateFromTemplate(cmd *cobra.Command, args []string) error {
	runtimeID, _ := cmd.Flags().GetString("runtime-id")
	if strings.TrimSpace(runtimeID) == "" {
		return fmt.Errorf("--runtime-id is required")
	}
	client, workspaceID, err := templateClient(cmd)
	if err != nil {
		return err
	}
	body := map[string]any{"runtime_id": strings.TrimSpace(runtimeID)}
	if cmd.Flags().Changed("name") {
		value, _ := cmd.Flags().GetString("name")
		body["name"] = value
	}
	if cmd.Flags().Changed("description") {
		value, _ := cmd.Flags().GetString("description")
		body["description"] = value
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var result map[string]any
	if err := client.PostJSON(ctx, agentTemplatePath(workspaceID, args[0])+"/agents", body, &result); err != nil {
		return fmt.Errorf("create agent from template: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	if output != "table" {
		return fmt.Errorf("invalid --output %q: must be table or json", output)
	}
	cli.PrintTable(os.Stdout, []string{"AGENT ID", "TEMPLATE"}, [][]string{{strVal(result, "agent_id"), strVal(result, "template_slug")}})
	return nil
}

func printTemplateMutation(cmd *cobra.Command, result map[string]any) error {
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	if output != "table" {
		return fmt.Errorf("invalid --output %q: must be table or json", output)
	}
	item := result
	if nested, ok := result["template"].(map[string]any); ok {
		item = nested
	}
	cli.PrintTable(os.Stdout, []string{"SLUG", "NAME", "SOURCE"}, [][]string{{strVal(item, "slug"), strVal(item, "display_name"), strVal(item, "source_type")}})
	return nil
}
