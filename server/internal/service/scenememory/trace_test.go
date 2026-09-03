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
	if strings.Join(opts.Tags, ",") != "scene_memory,kind-dm,agent-03000000-0000-0000-0000-000000000000,workspace-02000000-0000-0000-0000-000000000000" {
		t.Fatalf("tags = %v", opts.Tags)
	}
	keys := flushIndexKeys(row)
	if keys["scene_key"] != "cid+abc==" || keys["coord_trace_id"] != "5f3a1b2c-4d5e-4f60-8a71-92b3c4d5e6f7" || keys["agent_id"] != "03000000-0000-0000-0000-000000000000" {
		t.Fatalf("index keys = %v", keys)
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
