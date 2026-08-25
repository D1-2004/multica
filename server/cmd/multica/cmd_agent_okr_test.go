package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/spf13/cobra"
)

const testAgentOKRUUID = "22222222-2222-2222-2222-222222222222"

func newAgentOKRListTestCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "list"}
	cmd.Flags().String("output", "json", "")
	return cmd
}

func TestRunAgentOKRListPrintsCompleteResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/agents/"+testAgentOKRUUID+"/okrs" {
			t.Fatalf("request = %s %s", r.Method, r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"usage_available": true,
			"okrs": []map[string]any{{
				"id": "okr-1", "label_id": testLabelUUID, "position": 0,
				"objective": "Ship", "label": "O: Ship", "color": "#6366f1",
				"spend": map[string]any{
					"total_tokens": 10, "total_cost_usd_ticks": 20,
					"uncosted_tokens": 0, "task_count": 1, "unpriced_task_count": 0,
				},
				"key_results": []any{},
			}},
		})
	}))
	defer srv.Close()
	setCLITestServerEnv(t, srv.URL)

	out, err := captureStdout(t, func() error {
		return runAgentOKRList(newAgentOKRListTestCmd(), []string{testAgentOKRUUID})
	})
	if err != nil {
		t.Fatalf("runAgentOKRList: %v", err)
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(out), &response); err != nil {
		t.Fatalf("decode output: %v\n%s", err, out)
	}
	if response["usage_available"] != true {
		t.Fatalf("usage_available = %#v", response["usage_available"])
	}
	okrs, _ := response["okrs"].([]any)
	if len(okrs) != 1 {
		t.Fatalf("okrs = %#v", okrs)
	}
	okr, _ := okrs[0].(map[string]any)
	if okr["label_id"] != testLabelUUID {
		t.Fatalf("label_id = %#v", okr["label_id"])
	}
}
