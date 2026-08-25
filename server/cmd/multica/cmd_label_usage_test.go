package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/spf13/cobra"
)

func newLabelUsageTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "usage"}
	cmd.Flags().String("period", "all", "")
	cmd.Flags().String("sort", "recent", "")
	cmd.Flags().String("direction", "desc", "")
	cmd.Flags().String("tz", "", "")
	cmd.Flags().Int("page", 1, "")
	cmd.Flags().Int("page-size", 25, "")
	cmd.Flags().Bool("all", false, "")
	cmd.Flags().String("output", "json", "")
	return cmd
}

func TestRunLabelUsageAllFetchesEveryTaskPage(t *testing.T) {
	var pages []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/labels/"+testLabelUUID+"/usage" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		page, _ := strconv.Atoi(r.URL.Query().Get("page"))
		pages = append(pages, page)
		if r.URL.Query().Get("page_size") != "100" || r.URL.Query().Get("period") != "all" ||
			r.URL.Query().Get("sort") != "recent" || r.URL.Query().Get("direction") != "desc" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"label": map[string]any{
				"id": testLabelUUID, "workspace_id": "ws-1", "name": "OKR", "color": "#000000",
				"resource_type": "issue", "description": "", "created_at": "2026-08-25T00:00:00Z", "updated_at": "2026-08-25T00:00:00Z",
			},
			"period": "all", "timezone": "UTC", "sort": "recent", "direction": "desc",
			"summary": map[string]any{"total_tokens": 30, "total_cost_usd_ticks": 40, "uncosted_tokens": 0, "task_count": 2, "priced_task_count": 2, "unpriced_task_count": 0},
			"daily":   []any{}, "breakdown": []any{},
			"tasks":      []map[string]any{{"task_id": "task-" + strconv.Itoa(page)}},
			"pagination": map[string]any{"page": page, "page_size": 100, "total": 2, "total_pages": 2},
		})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	cmd := newLabelUsageTestCmd()
	_ = cmd.Flags().Set("all", "true")
	out, err := captureStdout(t, func() error { return runLabelUsage(cmd, []string{testLabelUUID}) })
	if err != nil {
		t.Fatalf("runLabelUsage: %v", err)
	}
	if len(pages) != 2 || pages[0] != 1 || pages[1] != 2 {
		t.Fatalf("pages = %v", pages)
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	tasks, _ := response["tasks"].([]any)
	if len(tasks) != 2 {
		t.Fatalf("tasks = %#v", tasks)
	}
	pagination, _ := response["pagination"].(map[string]any)
	if pagination["complete"] != true || pagination["pages_fetched"] != float64(2) {
		t.Fatalf("pagination = %#v", pagination)
	}
}
