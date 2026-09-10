package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/util"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type recentStateReaderStub struct {
	historyReader
	rows     []db.ListRecentCoordinatorStateRow
	err      error
	args     []db.ListRecentCoordinatorStateParams
	deadline time.Time
}

func (s *recentStateReaderStub) ListRecentCoordinatorState(ctx context.Context, p db.ListRecentCoordinatorStateParams) ([]db.ListRecentCoordinatorStateRow, error) {
	s.args = append(s.args, p)
	s.deadline, _ = ctx.Deadline()
	return s.rows, s.err
}
func recentStateTurn(t *testing.T) Turn {
	t.Helper()
	_, id := testOwnedIssue(t, testAgentID())
	return Turn{Source: SourceDigitalEmployee, WorkspaceID: util.UUIDToString(id.WorkspaceID), AgentID: testAgentID(), TraceID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", ConversationID: "cid-a", Message: "刚才分了几个任务？"}
}
func decodeRecentState(t *testing.T, raw string) recentCoordinationStateView {
	t.Helper()
	var view recentCoordinationStateView
	if err := json.Unmarshal([]byte(raw), &view); err != nil {
		t.Fatal(err)
	}
	return view
}

func TestRecentCoordinationStateOnlyUsesHostAnchorAndDistinguishesUnknown(t *testing.T) {
	turn := recentStateTurn(t)
	anchor, _ := util.ParseUUID(turn.TraceID)
	for _, tc := range []struct {
		name   string
		rows   []db.ListRecentCoordinatorStateRow
		err    error
		reader bool
		status string
	}{
		{name: "no_reader", status: "not_loaded"},
		{name: "missing_anchor", reader: true, status: "unavailable"},
		{name: "wrong_host_scene", reader: true, rows: []db.ListRecentCoordinatorStateRow{{AnchorID: anchor, AnchorConversationID: "cid-other"}}, status: "unavailable"},
		{name: "database_error", reader: true, err: errors.New("SECRET_DB_ERROR_TOKEN"), status: "unavailable"},
		{name: "valid_anchor_no_prior", reader: true, rows: []db.ListRecentCoordinatorStateRow{{AnchorID: anchor, AnchorConversationID: "cid-a"}}, status: "empty"},
		{name: "foreign_anchor", reader: true, rows: []db.ListRecentCoordinatorStateRow{{AnchorID: pgtype.UUID{Bytes: [16]byte{9}, Valid: true}}}, status: "unavailable"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &recentStateReaderStub{rows: tc.rows, err: tc.err}
			c := &Coordinator{}
			if tc.reader {
				c.Queries = stub
			}
			raw, err := c.readRecentCoordinationState(context.Background(), turn)
			if err != nil {
				t.Fatal(err)
			}
			view := decodeRecentState(t, raw)
			if view.Status != tc.status || strings.Contains(raw, "SECRET_DB_ERROR_TOKEN") {
				t.Fatalf("bad status/error disclosure: %s", raw)
			}
			if tc.reader && (len(stub.args) != 1 || stub.args[0].AnchorJobID != anchor || util.UUIDToString(stub.args[0].WorkspaceID) != turn.WorkspaceID || stub.args[0].AgentID != turn.AgentID) {
				t.Fatalf("Host scope changed: %+v", stub.args)
			}
		})
	}
	for _, mutation := range []func(*Turn){func(x *Turn) { x.TraceID = "not-a-job-id" }, func(x *Turn) { x.WorkspaceID = "" }, func(x *Turn) { x.AgentID = pgtype.UUID{} }, func(x *Turn) { x.ConversationID = "" }} {
		copy := turn
		mutation(&copy)
		stub := &recentStateReaderStub{}
		raw, err := (&Coordinator{Queries: stub}).readRecentCoordinationState(context.Background(), copy)
		if err != nil || decodeRecentState(t, raw).Status != "not_loaded" || len(stub.args) != 0 {
			t.Fatalf("invalid Host anchor triggered query: %s %v", raw, err)
		}
	}
}

func TestRecentCoordinationStateCountsAreDurableConfirmationsNotExecution(t *testing.T) {
	turn := recentStateTurn(t)
	anchor, _ := util.ParseUUID(turn.TraceID)
	job, _ := util.ParseUUID("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	receipt := `{"action":"issue_created","issue_id":"cccccccc-cccc-cccc-cccc-cccccccccccc","task_id":"dddddddd-dddd-dddd-dddd-dddddddddddd"}`
	for _, tc := range []struct {
		name                     string
		plan                     bool
		version, action          string
		items                    int32
		keys, completed, results string
		planned, confirmed       int
	}{
		{"missing_plan_completed_job", false, "", "", -1, "", "", "", -1, -1},
		{"plan_only_unknown_confirmations", true, "window-plan-v1", "issue", 2, `["k1","k2"]`, `null`, `null`, 2, -1},
		{"partial_committed", true, "window-plan-v1", "issue", 2, `["k1","k2"]`, `["k1"]`, `[` + receipt + `]`, 2, 1},
		{"deduplicated_receipts_and_keys", true, "window-plan-v1", "issue", 2, `["k1","k2"]`, `["k1","k1"]`, `[` + receipt + `,` + receipt + `]`, 2, 1},
		{"explicit_empty_receipts", true, "window-plan-v1", "issue", 2, `["k1","k2"]`, `[]`, `[]`, 2, 0},
		{"inconsistent_confirmations", true, "window-plan-v1", "issue", 2, `["k1","k2"]`, `["k1","k2"]`, `[` + receipt + `]`, 2, -1},
		{"non_work_plan", true, "window-plan-v1", "reply", -1, `null`, `null`, `null`, 0, -1},
		{"unknown_version", true, "future-plan", "issue", 2, `["k1"]`, `["k1"]`, `[` + receipt + `]`, -1, -1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &recentStateReaderStub{rows: []db.ListRecentCoordinatorStateRow{{AnchorID: anchor, AnchorConversationID: "cid-a", JobID: job, JobStatus: "completed", PlanPresent: tc.plan, PlanVersion: tc.version, PlanAction: tc.action, PlannedWorkCount: tc.items, PlanItemKeys: []byte(tc.keys), CompletedActionKeys: []byte(tc.completed), IssueResults: []byte(tc.results)}}}
			raw, err := (&Coordinator{Queries: stub}).readRecentCoordinationState(context.Background(), turn)
			if err != nil {
				t.Fatal(err)
			}
			v := decodeRecentState(t, raw)
			if len(v.Records) != 1 {
				t.Fatal(raw)
			}
			r := v.Records[0]
			check := func(p *int, want int) {
				t.Helper()
				if (want < 0 && p != nil) || (want >= 0 && (p == nil || *p != want)) {
					t.Fatalf("count mismatch want=%d view=%s", want, raw)
				}
			}
			check(r.PlannedWorkCount, tc.planned)
			check(r.ConfirmedWorkCount, tc.confirmed)
			if !strings.Contains(v.Meaning, "executor/delivery") || v.Complete {
				t.Fatal("metadata falsely claims execution/delivery or full coverage")
			}
		})
	}
}

func TestRecentCoordinationStateSnapshotIsSeparateFromOriginalQuestion(t *testing.T) {
	turn := recentStateTurn(t)
	seq := 0
	raw, err := marshalRecentCoordinationState(recentCoordinationStateView{Status: "not_loaded"})
	if err != nil {
		t.Fatal(err)
	}
	output, err := rememberCoordinationRead(&turn, &seq, toolContextRead, `{"kind":"coordination_state","job_id":"MODEL_SELECTED_JOB"}`, raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	if turn.CoordinationReads[0].Kind != coordinationStateKind || hasCoordinationHistorySnapshot(turn) || turn.HistoryStatus != "" || !strings.Contains(output, `"read_ref":"r1"`) {
		t.Fatalf("coordination metadata became original-question evidence: %+v", turn)
	}
	turn.History = []HistoryLine{{Role: "assistant", Content: "我给你分了三个任务"}}
	turn.HistoryStatus = "loaded"
	if _, err := rememberCoordinationRead(&turn, &seq, toolContextRead, `{"kind":"history"}`, "", nil); err != nil {
		t.Fatal(err)
	}
	if len(turn.CoordinationReads) != 2 || turn.CoordinationReads[1].Kind != "" || !hasCoordinationHistorySnapshot(turn) {
		t.Fatal("history/state read keys collided")
	}
	for _, tc := range []struct {
		ref   string
		valid bool
	}{{"r1", true}, {"r2", false}} {
		candidate := fmt.Sprintf(`{"actions":[{"kind":"report_status","source_refs":["u1"],"state_refs":["%s"],"reply":"当前提交状态还不能确认。"}]}`, tc.ref)
		_, err := parseValidatedWindowPlan(candidate, turn, nil, map[string]struct{}{})
		if (err == nil) != tc.valid {
			t.Fatalf("ref=%s valid=%t err=%v", tc.ref, tc.valid, err)
		}
	}
}

func TestRecentCoordinationStateProjectionIsBoundedAndDropsPayloads(t *testing.T) {
	records := make([]map[string]any, 6)
	for i := range records {
		records[i] = map[string]any{"job_id": fmt.Sprintf("aaaaaaaa-aaaa-aaaa-aaaa-%012d", i), "request_excerpt": strings.Repeat("问题", 1000), "job_status": "completed", "command": "SECRET_COMMAND", "last_error": "SECRET_ERROR", "report": "SECRET_REPORT"}
	}
	input, _ := json.Marshal(map[string]any{"status": "loaded", "records": records, "token": "SECRET_TOKEN"})
	raw, err := normalizeRecentCoordinationState(string(input))
	if err != nil {
		t.Fatal(err)
	}
	view := decodeRecentState(t, raw)
	if utf8.RuneCountInString(raw) > recentCoordinationStateBudget || len(view.Records) > 3 || !view.Truncated || view.Complete || strings.Contains(raw, "SECRET_") {
		t.Fatalf("unbounded/leaky state: %s", raw)
	}
	for _, r := range view.Records {
		if utf8.RuneCountInString(r.RequestExcerpt) > 120 || !r.RequestTruncated {
			t.Fatal("request excerpt clipping is not explicit")
		}
	}
}

func TestRecentCoordinationStateNonLoadedEnvelopeCannotCarryFacts(t *testing.T) {
	for _, status := range []string{"not_loaded", "unavailable", "empty", "invalid"} {
		raw := fmt.Sprintf(`{"status":"%s","records":[{"job_id":"bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb","job_status":"completed","request_excerpt":"SHOULD_NOT_BECOME_FACT","planned_work_count":3,"confirmed_work_count":3,"confirmation_source":"persisted_plan_receipts"}]}`, status)
		output, err := normalizeRecentCoordinationState(raw)
		if err != nil {
			t.Fatal(err)
		}
		view := decodeRecentState(t, output)
		if len(view.Records) != 0 || strings.Contains(output, "SHOULD_NOT_BECOME_FACT") || !view.Truncated {
			t.Fatalf("status=%s retained loaded facts: %s", status, output)
		}
	}
}

func TestRecentCoordinationStateReadTimeoutDoesNotExtendParentDeadline(t *testing.T) {
	turn := recentStateTurn(t)
	for _, parentShorter := range []bool{false, true} {
		ctx := context.Background()
		var parentDeadline time.Time
		if parentShorter {
			parentDeadline = time.Now().Add(200 * time.Millisecond)
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, parentDeadline)
			defer cancel()
		}
		stub := &recentStateReaderStub{err: context.DeadlineExceeded}
		start := time.Now()
		raw, err := (&Coordinator{Queries: stub}).readRecentCoordinationState(ctx, turn)
		if err != nil || decodeRecentState(t, raw).Status != "unavailable" || stub.deadline.IsZero() || stub.deadline.After(start.Add(2100*time.Millisecond)) {
			t.Fatalf("read timeout missing or hidden: %s deadline=%s err=%v", raw, stub.deadline, err)
		}
		if parentShorter && !stub.deadline.Equal(parentDeadline) {
			t.Fatal("state read extended the existing turn deadline")
		}
	}
}
