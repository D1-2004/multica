package main

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strconv"

	"github.com/multica-ai/multica/server/internal/cli"
	"github.com/multica-ai/multica/server/pkg/protocol"
	"github.com/spf13/cobra"
)

func runIssueRunEvents(cmd *cobra.Command, args []string) error {
	client, err := newAPIClient(cmd)
	if err != nil {
		return err
	}
	since, _ := cmd.Flags().GetInt("since")
	limit, _ := cmd.Flags().GetInt("limit")
	if since < 0 || limit < 1 || limit > 1000 {
		return fmt.Errorf("since must be nonnegative and limit between 1 and 1000")
	}
	ctx, cancel := cli.APIContext(context.Background())
	defer cancel()
	issueID := ""
	if input, _ := cmd.Flags().GetString("issue"); input != "" {
		ref, err := resolveIssueRef(ctx, client, input)
		if err != nil {
			return err
		}
		issueID = ref.ID
	}
	ref, err := resolveTaskRunID(ctx, client, issueID, args[0])
	if err != nil {
		return err
	}
	var page protocol.TaskRunEventPage
	path := "/api/tasks/" + url.PathEscape(ref.ID) + "/events?since=" + strconv.Itoa(since) + "&limit=" + strconv.Itoa(limit)
	if session, _ := cmd.Flags().GetString("session"); session != "" {
		path += "&session_id=" + url.QueryEscape(session)
	}
	if err = client.GetJSON(ctx, path, &page); err != nil {
		return fmt.Errorf("read run events: %w", err)
	}
	output, _ := cmd.Flags().GetString("output")
	if output == "json" {
		return cli.PrintJSON(os.Stdout, page)
	}
	rows := make([][]string, 0, len(page.Events))
	for _, e := range page.Events {
		rows = append(rows, []string{strconv.Itoa(e.Seq), e.Kind, e.Reportability, e.Source.Phase, e.Tool, e.Content})
	}
	cli.PrintTable(os.Stdout, []string{"SEQ", "KIND", "REPORTABILITY", "PHASE", "TOOL", "CONTENT"}, rows)
	return nil
}
