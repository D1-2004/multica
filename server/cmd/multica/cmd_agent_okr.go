package main

import (
	"context"
	"fmt"
	"net/url"
	"os"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var agentOKRCmd = &cobra.Command{
	Use:   "okr",
	Short: "Inspect an agent's objectives and measured spend",
}

var agentOKRListCmd = &cobra.Command{
	Use:   "list <agent-ref>",
	Short: "List an agent's objectives, key results, labels, and spend",
	Args:  exactArgs(1),
	RunE:  runAgentOKRList,
}

func init() {
	agentCmd.AddCommand(agentOKRCmd)
	agentOKRCmd.AddCommand(agentOKRListCmd)
	agentOKRListCmd.Flags().String("output", "table", "Output format: table or json")
}

func runAgentOKRList(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	agentID, err := resolveAgent(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve agent: %w", err)
	}
	var response map[string]any
	if err := client.GetJSON(ctx, "/api/agents/"+url.PathEscape(agentID)+"/okrs", &response); err != nil {
		return fmt.Errorf("list agent OKRs: %w", err)
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, response)
	}
	if output != "table" {
		return fmt.Errorf("--output must be table or json")
	}

	usageAvailable, _ := response["usage_available"].(bool)
	okrs, _ := response["okrs"].([]any)
	headers := []string{"KIND", "POSITION", "TEXT", "LABEL_ID", "TOKENS", "COST_USD_TICKS", "TASKS", "USAGE"}
	rows := make([][]string, 0)
	appendRow := func(kind string, raw map[string]any) {
		spend, _ := raw["spend"].(map[string]any)
		usage := "unavailable"
		if usageAvailable {
			usage = "available"
		}
		text := strVal(raw, "objective")
		if kind == "KR" {
			text = strVal(raw, "text")
		}
		rows = append(rows, []string{
			kind,
			formatMetadataValue(raw["position"]),
			text,
			strVal(raw, "label_id"),
			formatMetadataValue(spend["total_tokens"]),
			formatMetadataValue(spend["total_cost_usd_ticks"]),
			formatMetadataValue(spend["task_count"]),
			usage,
		})
	}
	for _, raw := range okrs {
		objective, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		appendRow("O", objective)
		keyResults, _ := objective["key_results"].([]any)
		for _, keyResultRaw := range keyResults {
			keyResult, ok := keyResultRaw.(map[string]any)
			if ok {
				appendRow("KR", keyResult)
			}
		}
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}
