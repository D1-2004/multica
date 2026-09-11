package scenememory

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

func TestFlushTraceOptionsCarryLookupKeys(t *testing.T) {
	row := db.SceneMemory{
		ID:                      pgtype.UUID{Bytes: [16]byte{1}, Valid: true},
		WorkspaceID:             pgtype.UUID{Bytes: [16]byte{2}, Valid: true},
		AgentID:                 pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
		Platform:                "dingtalk",
		OrgID:                   "org-1",
		SceneKey:                "cid+abc==",
		SceneKind:               "dm",
		SceneTitle:              "冬翔",
		MemoryRevision:          4,
		AttemptCount:            2,
		LastTriggerCoordTraceID: "5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7",
	}
	opts := flushTraceOptions(row, "预发测试智能体", time.Date(2026, 9, 3, 9, 0, 0, 0, time.UTC))
	if opts.Name != flushTraceName || opts.SessionID != "cid+abc==" {
		t.Fatalf("name/session = %q/%q", opts.Name, opts.SessionID)
	}
	if strings.Join(opts.Tags, ",") != "scene_memory,kind-dm,agent-03000000-0000-0000-0000-000000000000,agent_name-预发测试智能体,workspace-02000000-0000-0000-0000-000000000000" {
		t.Fatalf("tags = %v", opts.Tags)
	}
	keys := flushIndexKeys(row)
	if keys["scene_memory_id"] != "01000000-0000-0000-0000-000000000000" || keys["coord_trace_id"] != "5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7" {
		t.Fatalf("index keys = %v", keys)
	}
	for _, covered := range []string{"scene_key", "conversation_id", "agent_id", "workspace_id", "dws_org_id"} {
		if _, indexed := keys[covered]; indexed {
			t.Fatalf("%s is covered by the session or a tag and must not be indexed", covered)
		}
	}
	for key, want := range map[string]any{
		"agent_name":        "预发测试智能体",
		"agent_id":          "03000000-0000-0000-0000-000000000000",
		"workspace_id":      "02000000-0000-0000-0000-000000000000",
		"conversation_id":   "cid+abc==",
		"conversation_name": "冬翔",
		"coord_trace_id":    "5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7",
		"memory_revision":   int64(4),
		"attempt":           int64(2),
		"model":             flushModel,
	} {
		if got := opts.Metadata[key]; got != want {
			t.Errorf("metadata %s = %#v, want %#v", key, got, want)
		}
	}
}

func TestHistoryReadTraceReportsChronologyAndMissingEvidence(t *testing.T) {
	older := time.Date(2026, 9, 8, 10, 0, 0, 0, time.UTC)
	newer := older.Add(time.Minute)
	row := db.SceneMemory{LeaseTargetThroughEvidenceID: "missing", LastTriggerEvidenceID: "visible"}
	output := historyReadTraceOutput(row, HistoryPage{
		Events:      []HistoryEvent{{OccurredAt: newer}, {OccurredAt: older}},
		EvidenceIDs: []string{"visible"}, RawCount: 3, PaginationKnown: true,
		HasMore: true, NextCursor: newer.Add(time.Millisecond),
	})
	for key, want := range map[string]any{
		"oldest": older.Format(time.RFC3339), "newest": newer.Format(time.RFC3339),
		"next_cursor": newer.Add(time.Millisecond).Format(time.RFC3339Nano),
		"raw_count":   3, "event_count": 2, "has_more": true,
		"claimed_evidence_in_page": false, "trigger_evidence_in_page": true,
	} {
		if output[key] != want {
			t.Errorf("%s=%v want %v", key, output[key], want)
		}
	}
}
