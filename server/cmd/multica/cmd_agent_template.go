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

var agentTemplateCmd = &cobra.Command{
	Use:   "template",
	Short: "Browse Git-backed agent templates",
}

var agentTemplateListCmd = &cobra.Command{
	Use:   "list",
	Short: "List Git-backed agent templates available to the workspace",
	RunE:  runAgentTemplateList,
}

var agentTemplateGetCmd = &cobra.Command{
	Use:   "get <template-key>",
	Short: "Get a Git-backed agent template and its repository defaults",
	Args:  exactArgs(1),
	RunE:  runAgentTemplateGet,
}

var agentCreateFromTemplateCmd = &cobra.Command{
	Use:   "create-from-template <template-key>",
	Short: "Create an agent from a Git-backed template",
	Args:  exactArgs(1),
	RunE:  runAgentCreateFromTemplate,
}

func init() {
	agentCmd.AddCommand(agentTemplateCmd)
	agentCmd.AddCommand(agentCreateFromTemplateCmd)
	agentTemplateCmd.AddCommand(agentTemplateListCmd)
	agentTemplateCmd.AddCommand(agentTemplateGetCmd)

	agentTemplateListCmd.Flags().String("output", "table", "Output format: table or json")
	agentTemplateGetCmd.Flags().String("output", "json", "Output format: table or json")

	agentCreateFromTemplateCmd.Flags().String("runtime-id", "", "Runtime ID (required)")
	agentCreateFromTemplateCmd.Flags().String("name", "", "Agent name (defaults to the Git template manifest name)")
	agentCreateFromTemplateCmd.Flags().String("description", "", "Agent description (defaults to the Git template manifest description)")
	agentCreateFromTemplateCmd.Flags().String("output", "json", "Output format: json or table")
}

func gitAgentTemplatesPath(workspaceID string) string {
	return fmt.Sprintf("/api/workspaces/%s/git-agent-templates", url.PathEscape(workspaceID))
}

func gitAgentTemplatePath(workspaceID, templateKey string) string {
	return gitAgentTemplatesPath(workspaceID) + "/" + url.PathEscape(templateKey)
}

func runAgentTemplateList(cmd *cobra.Command, _ []string) error {
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
	var response struct {
		Templates []map[string]any `json:"templates"`
	}
	if err := client.GetJSON(ctx, gitAgentTemplatesPath(workspaceID), &response); err != nil {
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
	for _, template := range response.Templates {
		available := "no"
		if value, _ := template["available"].(bool); value {
			available = "yes"
		}
		rows = append(rows, []string{
			strVal(template, "key"),
			strVal(template, "display_name"),
			strVal(template, "repository"),
			strVal(template, "ref"),
			available,
			strVal(template, "description"),
		})
	}
	cli.PrintTable(os.Stdout, []string{"KEY", "NAME", "REPOSITORY", "REF", "AVAILABLE", "DESCRIPTION"}, rows)
	return nil
}

func runAgentTemplateGet(cmd *cobra.Command, args []string) error {
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
	var template map[string]any
	if err := client.GetJSON(ctx, gitAgentTemplatePath(workspaceID, args[0]), &template); err != nil {
		return fmt.Errorf("get agent template: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, template)
	}
	if output != "table" {
		return fmt.Errorf("invalid --output %q: must be table or json", output)
	}
	cli.PrintTable(os.Stdout,
		[]string{"KEY", "DEFAULT NAME", "REPOSITORY", "REF", "SHA", "AVAILABLE", "DESCRIPTION"},
		[][]string{{
			strVal(template, "key"),
			strVal(template, "default_agent_name"),
			strVal(template, "repository"),
			strVal(template, "ref"),
			strVal(template, "resolved_sha"),
			fmt.Sprint(template["available"]),
			strVal(template, "default_description"),
		}},
	)
	return nil
}

func runAgentCreateFromTemplate(cmd *cobra.Command, args []string) error {
	runtimeID, _ := cmd.Flags().GetString("runtime-id")
	if strings.TrimSpace(runtimeID) == "" {
		return fmt.Errorf("--runtime-id is required")
	}
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	workspaceID, err := requireWorkspaceID(cmd)
	if err != nil {
		return err
	}

	body := map[string]any{"runtime_id": strings.TrimSpace(runtimeID)}
	if cmd.Flags().Changed("name") {
		name, _ := cmd.Flags().GetString("name")
		body["name"] = name
	}
	if cmd.Flags().Changed("description") {
		description, _ := cmd.Flags().GetString("description")
		body["description"] = description
	}

	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	var result map[string]any
	if err := client.PostJSON(ctx, gitAgentTemplatePath(workspaceID, args[0])+"/agents", body, &result); err != nil {
		return fmt.Errorf("create agent from template: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, result)
	}
	if output != "table" {
		return fmt.Errorf("invalid --output %q: must be table or json", output)
	}
	cli.PrintTable(os.Stdout, []string{"AGENT ID", "NAME", "TEMPLATE"}, [][]string{{
		strVal(result, "agent_id"), strVal(result, "name"), strVal(result, "template_key"),
	}})
	return nil
}
