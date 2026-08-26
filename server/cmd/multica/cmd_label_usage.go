package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/multica-ai/multica/server/internal/cli"
)

var labelUsageCmd = &cobra.Command{
	Use:   "usage <label-ref>",
	Short: "Show token and cost usage for an issue label",
	Args:  exactArgs(1),
	RunE:  runLabelUsage,
}

func init() {
	labelCmd.AddCommand(labelUsageCmd)
	labelUsageCmd.Flags().String("period", "all", "Usage window: 7d, 30d, 90d, or all")
	labelUsageCmd.Flags().String("sort", "recent", "Task sort: cost, tokens, or recent")
	labelUsageCmd.Flags().String("direction", "desc", "Sort direction: asc or desc")
	labelUsageCmd.Flags().String("tz", "", "IANA timezone for date buckets (defaults to the account timezone)")
	labelUsageCmd.Flags().Int("page", 1, "Task page to read")
	labelUsageCmd.Flags().Int("page-size", 25, "Tasks per page (1-100)")
	labelUsageCmd.Flags().Bool("all", false, "Fetch every task page and return one complete response")
	labelUsageCmd.Flags().String("output", "table", "Output format: table or json")
}

func runLabelUsage(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()

	labelRef, err := resolveLabelID(ctx, client, args[0])
	if err != nil {
		return fmt.Errorf("resolve label: %w", err)
	}
	period, _ := cmd.Flags().GetString("period")
	sortBy, _ := cmd.Flags().GetString("sort")
	direction, _ := cmd.Flags().GetString("direction")
	tz, _ := cmd.Flags().GetString("tz")
	page, _ := cmd.Flags().GetInt("page")
	pageSize, _ := cmd.Flags().GetInt("page-size")
	fetchAll, _ := cmd.Flags().GetBool("all")
	if !oneOf(period, "7d", "30d", "90d", "all") {
		return fmt.Errorf("--period must be 7d, 30d, 90d, or all")
	}
	if !oneOf(sortBy, "cost", "tokens", "recent") {
		return fmt.Errorf("--sort must be cost, tokens, or recent")
	}
	if !oneOf(direction, "asc", "desc") {
		return fmt.Errorf("--direction must be asc or desc")
	}
	if page < 1 {
		return fmt.Errorf("--page must be a positive integer")
	}
	if pageSize < 1 || pageSize > 100 {
		return fmt.Errorf("--page-size must be between 1 and 100")
	}
	if fetchAll && cmd.Flags().Changed("page") {
		return fmt.Errorf("--all and --page cannot be used together")
	}
	if fetchAll {
		page = 1
		pageSize = 100
	}

	fetchPage := func(current int) (map[string]any, error) {
		query := url.Values{
			"period":    {period},
			"sort":      {sortBy},
			"direction": {direction},
			"page":      {strconv.Itoa(current)},
			"page_size": {strconv.Itoa(pageSize)},
		}
		if tz != "" {
			query.Set("tz", tz)
		}
		var response map[string]any
		path := "/api/labels/" + url.PathEscape(labelRef.ID) + "/usage?" + query.Encode()
		if err := client.GetJSON(ctx, path, &response); err != nil {
			return nil, err
		}
		return response, nil
	}

	response, err := fetchPage(page)
	if err != nil {
		return fmt.Errorf("get label usage: %w", err)
	}
	if fetchAll {
		allTasks, _ := response["tasks"].([]any)
		pagination, _ := response["pagination"].(map[string]any)
		totalPages := mapInt(pagination, "total_pages")
		pagesFetched := 1
		for current := 2; current <= totalPages; current++ {
			next, err := fetchPage(current)
			if err != nil {
				return fmt.Errorf("get label usage page %d: %w", current, err)
			}
			tasks, _ := next["tasks"].([]any)
			allTasks = append(allTasks, tasks...)
			pagesFetched++
		}
		response["tasks"] = allTasks
		if pagination == nil {
			pagination = map[string]any{}
			response["pagination"] = pagination
		}
		pagination["page"] = 1
		pagination["page_size"] = 100
		pagination["pages_fetched"] = pagesFetched
		pagination["complete"] = true
	}

	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, response)
	}
	if output != "table" {
		return fmt.Errorf("--output must be table or json")
	}

	headers := []string{"TASK", "ISSUE", "AGENT", "STATUS", "TOKENS", "COST_USD_TICKS", "PRICED", "ACTIVITY"}
	rows := make([][]string, 0)
	tasks, _ := response["tasks"].([]any)
	for _, raw := range tasks {
		task, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		rows = append(rows, []string{
			strVal(task, "task_id"),
			strVal(task, "issue_identifier"),
			strVal(task, "agent_name"),
			strVal(task, "status"),
			formatMetadataValue(task["total_tokens"]),
			formatMetadataValue(task["total_cost_usd_ticks"]),
			formatMetadataValue(task["is_priced"]),
			strVal(task, "activity_at"),
		})
	}
	cli.PrintTable(os.Stdout, headers, rows)
	return nil
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func mapInt(values map[string]any, key string) int {
	if values == nil {
		return 0
	}
	switch value := values[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	default:
		return 0
	}
}
