package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestRunIssueRunEventsCursorAndMalformedResponse(t *testing.T) {
	for _, broken := range []bool{false, true} {
		t.Run(map[bool]string{false: "cursor", true: "malformed"}[broken], func(t *testing.T) {
			id := "abcd1234-0000-0000-0000-000000000000"
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/api/tasks/"+id+"/events" || r.URL.Query().Get("since") != "7" || r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("session_id") != "ses/one+two" {
					t.Error("wrong cursor request", r.URL)
				}
				w.Header().Set("Content-Type", "application/json")
				if broken {
					_, _ = w.Write([]byte(`{"events":[{"seq":"invalid"}],"next_seq":8}`))
					return
				}
				_, _ = w.Write([]byte(`{"events":[{"seq":8,"kind":"assistant_text","reportability":"progress"}],"next_seq":8,"has_more":true}`))
			}))
			defer srv.Close()
			t.Setenv("MULTICA_SERVER_URL", srv.URL)
			t.Setenv("MULTICA_WORKSPACE_ID", "ws-1")
			t.Setenv("MULTICA_TOKEN", "test-token")
			cmd := &cobra.Command{Use: "run-events"}
			cmd.Flags().String("output", "json", "")
			cmd.Flags().Int("since", 7, "")
			cmd.Flags().Int("limit", 2, "")
			cmd.Flags().String("issue", "", "")
			cmd.Flags().String("session", "ses/one+two", "")
			output, err := captureStdout(t, func() error { return runIssueRunEvents(cmd, []string{id}) })
			if broken {
				if err == nil {
					t.Fatal("malformed response accepted")
				}
				return
			}
			if err != nil || !strings.Contains(output, `"next_seq": 8`) || !strings.Contains(output, `"has_more": true`) {
				t.Fatal(output, err)
			}
		})
	}
}
