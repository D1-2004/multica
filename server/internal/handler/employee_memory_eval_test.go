package handler

// Deterministic EmployeeLoop memory evaluation suite (M7 of
// docs 12-memory-design §9; acceptance cases §10).
//
// Every job drives the production path with real PostgreSQL and a scripted
// fake model: admission over HTTP, the scene worker's claim → buildInput →
// frozen input_snapshot → model loop → Host tools → completion. Checks assert
// what the Host put in front of the model (the frozen snapshot, the actual
// provider request, the dispatch work packet) and the durable PostgreSQL
// facts, never what a model would do with it. No model, DWS or network call
// is made.
//
// Known-gap ratchet (GawkBot internal/team/office_eval.go at
// 71e82a1809565281cbd0bf8185d3c125b715d934, re-implemented): a check listed in
// memoryEvalKnownGaps encodes target behaviour of a memory work package that
// has not landed. It may be red; it is logged as KNOWN-GAP and the run stays
// green. A red check that is not listed is a regression and fails. A listed
// check that turns green also fails until its entry is removed in the same
// change, so a landed behaviour becomes a regression guard. An entry whose
// check no longer runs in its job fails as stale.
//
// Bindings: some target behaviour is gated in production (the Diamond
// runtime.employee_memory exact target, the [employee-memory:N] replica
// marker) or needs a provider fake whose seam does not exist yet (the group
// transcript reader). The package that owns the gate registers it from its own
// _test.go init() through memoryEvalEnable / memoryEvalInstallTranscript.
// Until then the dependent checks are red with "not wired", exactly like
// production without the gate. Do not edit expectations here to make a gap
// green; land the behaviour, register the binding and delete the gap entry.

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/scene"
	"github.com/multica-ai/multica/server/internal/service/employeememory"
	openai "github.com/openai/openai-go/v3"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

// memoryEvalKnownGaps maps "<job>/<check>" to the work package expected to
// turn it green. Remove an entry in the change that makes its check pass.
var memoryEvalKnownGaps = map[string]string{
	"experience-reuse/packet-has-separate-memory-section":   "M4: work packet MEMORY section",
	"experience-reuse/context-used-lists-memory-id":         "M4: ContextUsed memory:<id>",
	"experience-reuse/context-used-lists-history-job":       "M4: ContextUsed history:<job_id>",
	"experience-reuse/packet-carries-frozen-history":        "M4: dispatch packet history from the frozen wake",
	"group-transcript/persona-transcript-rules-when-loaded": "M3: GROUP TRANSCRIPT persona rules",
	"persona/language-rule":                                 "M3: LANGUAGE persona rule",
	"persona/self-profile":                                  "M3: SELF PROFILE Host facts",
	"scene-capture/scene-record-written":                    "M5: memory_capture audience=scene",
	"scene-capture/attribution-from-host-evidence":          "M5: speaker_ref/created_by/capture_origin",
	"scene-capture/host-generated-key":                      "M5: Host key from subject",
	"scene-capture/other-member-recalls-with-attribution":   "M5: scene layer shared with attribution",
	"scene-capture/cross-author-conflict-kept":              "M5: cross-author conflict",
	"scene-capture/conflict-shown-to-model":                 "M5+M1: 说法不一 rendering",
	"scene-capture/non-author-forget-refused":               "M5: scene forget permission",
	"scene-capture/speaker-forget-succeeds":                 "M5: ForgetSceneTx",
	"scene-capture/third-party-reset-keeps-scene-records":   "M5: group /reset-memory narrowed (D3)",
	"self-preference/own-preference-user-stated":            "M5: HumanStated self preference (D4)",
	"self-preference/lookup-scope-scene":                    "M5: memory_lookup scope=scene",
}

// Feature names gated in production. A binding enables exactly that feature
// for the target triple; nil or false means "not wired" and the dependent
// checks are red.
const (
	memoryEvalGroupTranscript = "group_transcript" // Diamond runtime.employee_memory.group_transcript (M2)
	memoryEvalPersonView      = "person_view"      // Diamond runtime.employee_memory.person_view (M1)
	memoryEvalSceneCapture    = "scene_capture"    // Diamond runtime.employee_memory.scene_capture (M5)
	memoryEvalToolsV2         = "tools_v2"         // all live replicas advertise [employee-memory:1] (M5)
)

type memoryEvalTarget struct {
	Handler                     *Handler
	Worker                      *EmployeeSceneWorker
	WorkspaceID, AgentID, OrgID string
}

// memoryEvalEnable is filled by the owning packages' test init() functions.
var memoryEvalEnable = map[string]func(t *testing.T, target memoryEvalTarget) bool{}

// memoryEvalGroupLine is one provider group-history line that never reached
// EmployeeLoop as an admitted event (nobody @-mentioned the employee).
type memoryEvalGroupLine struct {
	MessageID       string
	SenderUID       string // equals memoryEvalAgentUID for the employee's own account
	SenderName      string
	SenderType      string // provider senderType; "" for people
	Text            string
	SentAt          time.Time
	QuotedMessageID string
}

type memoryEvalTranscriptOptions struct {
	// Delay makes the fake provider slow; the Host must give up within its budget.
	Delay time.Duration
}

// memoryEvalInstallTranscript installs a fake DWS group-history reader on the
// worker for one conversation and returns a call counter. Registered by M2.
var memoryEvalInstallTranscript func(t *testing.T, target memoryEvalTarget, conversationID string, lines []memoryEvalGroupLine, opts memoryEvalTranscriptOptions) (calls func() int)

const (
	memoryEvalOrg      = "456" // DWS org of the shared dispatch fixture identity
	memoryEvalAgentUID = "123" // DWS uid of the employee in the shared fixture
)

// ---------------------------------------------------------------------------
// Ratchet

type memoryEvalCheck struct {
	id, detail string
	pass       bool
}

type memoryEval struct {
	t      *testing.T
	job    string
	checks []memoryEvalCheck
}

func newMemoryEval(t *testing.T, job string) *memoryEval {
	e := &memoryEval{t: t, job: job}
	t.Cleanup(e.finish)
	return e
}

func (e *memoryEval) check(id string, pass bool, format string, args ...any) {
	e.checks = append(e.checks, memoryEvalCheck{id: id, pass: pass, detail: fmt.Sprintf(format, args...)})
}

// wired reports whether a gated feature was enabled; the caller records the
// dependent check as red with a "not wired" detail when it was not.
func (e *memoryEval) wired(env *memoryEvalEnv, feature string) bool {
	enable := memoryEvalEnable[feature]
	return enable != nil && enable(e.t, env.target())
}

func (e *memoryEval) finish() {
	t := e.t
	seen := map[string]bool{}
	for _, c := range e.checks {
		key := e.job + "/" + c.id
		if seen[key] {
			t.Errorf("duplicate eval check %s", key)
			continue
		}
		seen[key] = true
		gap := memoryEvalKnownGaps[key]
		switch {
		case c.pass && gap != "":
			t.Errorf("known gap %q now PASSES (%s) — remove it from memoryEvalKnownGaps to lock it in as a regression guard", gap, key)
		case !c.pass && gap == "":
			t.Errorf("memory eval regression %s: %s", key, c.detail)
		case !c.pass:
			t.Logf("[KNOWN-GAP red until %s] %s: %s", gap, key, c.detail)
		default:
			t.Logf("[PASS] %s", key)
		}
	}
	if t.Failed() && len(e.checks) == 0 {
		return
	}
	for key := range memoryEvalKnownGaps {
		if strings.HasPrefix(key, e.job+"/") && !seen[key] {
			t.Errorf("stale known-gap entry %s: the check no longer runs", key)
		}
	}
}

// ---------------------------------------------------------------------------
// Environment: one fresh agent, real PostgreSQL, scripted model.

type memoryEvalPerson struct{ OpenID, Name string }

func (p memoryEvalPerson) ref() string { return "dingtalk:" + memoryEvalOrg + ":open_id:" + p.OpenID }

// say is a plain line in a DM, or an unaddressed group line.
func (p memoryEvalPerson) say(text string) DispatchMessage {
	return DispatchMessage{OpenMsgID: "msg-" + uuid.NewString(), Text: text, SenderOpenDingTalkID: p.OpenID, SenderDisplayName: p.Name}
}

// at addresses the employee in a group.
func (p memoryEvalPerson) at(text string) DispatchMessage {
	m := p.say(text)
	m.Mentions = []DispatchMention{{UID: memoryEvalAgentUID}}
	return m
}

var (
	evalDongxiang = memoryEvalPerson{OpenID: "eval-open-dongxiang", Name: "夏东翔"}
	evalDirector  = memoryEvalPerson{OpenID: "eval-open-chen", Name: "陈思远"}
	evalColleague = memoryEvalPerson{OpenID: "eval-open-liting", Name: "李婷"}
)

type memoryEvalConv struct{ ID, Type string }

func newMemoryEvalDM() memoryEvalConv {
	return memoryEvalConv{ID: "cid-eval-dm-" + uuid.NewString(), Type: "single"}
}
func newMemoryEvalGroup() memoryEvalConv {
	return memoryEvalConv{ID: "cid-eval-group-" + uuid.NewString(), Type: "group"}
}

type memoryEvalEnv struct {
	t      *testing.T
	f      *dingTalkResponseFixture
	dc     agentDispatchContext
	traces *tracetest.InMemoryExporter
}

func newMemoryEvalEnv(t *testing.T) *memoryEvalEnv {
	t.Helper()
	f, dc := employeeMemoryFixture(t)
	ctx := context.Background()
	t.Cleanup(func() {
		for _, table := range []string{"employee_learning_consumption", "employee_host_notice"} {
			_, _ = testPool.Exec(ctx, `DELETE FROM `+table+` WHERE agent_id=$1`, f.agentID)
		}
	})
	client, traces := employeeTraceClient(t)
	f.h.EmployeeSceneWorker.Langfuse = client
	return &memoryEvalEnv{t: t, f: f, dc: dc, traces: traces}
}

// traceOf returns the spans of one wake's employee_loop trace (trace id = job
// id without dashes) and its flattened trace metadata.
func (e *memoryEvalEnv) traceOf(jobID string) (names []string, metadata map[string]string) {
	metadata = map[string]string{}
	if e.traces == nil {
		return nil, metadata
	}
	want := strings.ReplaceAll(jobID, "-", "")
	for _, span := range e.traces.GetSpans() {
		if span.SpanContext.TraceID().String() != want {
			continue
		}
		names = append(names, span.Name)
		for _, a := range span.Attributes {
			if key, ok := strings.CutPrefix(string(a.Key), "langfuse.trace.metadata."); ok {
				metadata[key] = a.Value.Emit()
			}
		}
	}
	return names, metadata
}

func (e *memoryEvalEnv) target() memoryEvalTarget {
	return memoryEvalTarget{Handler: e.f.h, Worker: e.f.h.EmployeeSceneWorker, WorkspaceID: testWorkspaceID, AgentID: e.f.agentID, OrgID: memoryEvalOrg}
}

// memoryEvalTurn is what the scripted model sees for one wake.
type memoryEvalTurn struct {
	Sources  []string // source_ref of each admitted message, in order
	Requests []string // provider requests so far (messages JSON)
	Tools    string   // tool definitions offered on the latest request (JSON)
}

// The memory tool schema changes in M5 (key → subject/audience, record_id →
// record_ref). Guards that only need "some capture" follow whichever schema
// the frozen wake offers, so a schema upgrade never reads as a regression.
func (turn *memoryEvalTurn) captureArgs(audience, kind, subject, key, quote string) map[string]any {
	if strings.Contains(turn.Tools, `"audience"`) {
		return map[string]any{"audience": audience, "type": kind, "subject": subject, "quote": quote}
	}
	return map[string]any{"key": key, "type": kind, "quote": quote}
}

func (turn *memoryEvalTurn) forgetArgs(id string) map[string]any {
	if strings.Contains(turn.Tools, `"record_ref"`) {
		return map[string]any{"record_ref": id}
	}
	return map[string]any{"record_id": id}
}

type memoryEvalScript func(n int, turn *memoryEvalTurn) *openai.ChatCompletion

type memoryEvalWake struct {
	JobID, Receipt, State string
	Actions               []string // delivered Host sends of this wake
	Raw                   json.RawMessage
	Input                 employeeSavedInput
	Requests              []string
	Sources               []string
	Calls                 int
}

// Seen is everything the model received during the wake.
func (w memoryEvalWake) Seen() string { return strings.Join(w.Requests, "\n") }

func memoryEvalText(t *testing.T, text string) *openai.ChatCompletion {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"choices": []any{map[string]any{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text}}}})
	if err != nil {
		t.Fatal(err)
	}
	var out openai.ChatCompletion
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return &out
}

func (e *memoryEvalEnv) admit(conv memoryEvalConv, msgs ...DispatchMessage) string {
	t := e.t
	t.Helper()
	e.f.command.Event.Data.Conversation = DispatchConversation{OpenConversationID: conv.ID, Type: conv.Type}
	e.f.command.Event.Data.Messages = msgs
	e.f.command.Event.Data.Sender = DispatchSender{OpenDingTalkID: msgs[0].SenderOpenDingTalkID, DisplayName: msgs[0].SenderDisplayName}
	if r := employeeHTTP(t, e.f, e.dc, uuid.NewString()); r.Code != http.StatusAccepted {
		t.Fatalf("admission: %d %s", r.Code, r.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(context.Background(), `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1 ORDER BY created_at DESC LIMIT 1`, e.f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	return receipt
}

// wake admits one window and runs the real worker once. A nil script answers
// with one plain reply. Delivered replies are confirmed like the response
// worker would, so later wakes see them as Host-sent history.
func (e *memoryEvalEnv) wake(conv memoryEvalConv, script memoryEvalScript, msgs ...DispatchMessage) memoryEvalWake {
	t := e.t
	t.Helper()
	ctx := context.Background()
	receipt := e.admit(conv, msgs...)
	turn := &memoryEvalTurn{}
	for _, m := range msgs {
		turn.Sources = append(turn.Sources, receipt+"/"+m.OpenMsgID)
	}
	calls := 0
	e.f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		raw, err := json.Marshal(p.Messages)
		if err != nil {
			t.Fatal(err)
		}
		turn.Requests = append(turn.Requests, string(raw))
		tools, _ := json.Marshal(p.Tools)
		turn.Tools = string(tools)
		if script == nil {
			return memoryEvalText(t, "好的，收到。"), nil
		}
		return script(calls, turn), nil
	})
	if worked, err := e.f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil || !worked {
		t.Fatalf("worker: worked=%v err=%v", worked, err)
	}
	w := memoryEvalWake{Receipt: receipt, Requests: turn.Requests, Sources: turn.Sources, Calls: calls}
	var raw []byte
	if err := testPool.QueryRow(ctx, `SELECT j.id::text,j.state,COALESCE(j.input_snapshot,'null'::jsonb) FROM employee_scene_job j JOIN employee_event_consumption c ON c.job_id=j.id WHERE c.receipt_id=$1::uuid`, receipt).Scan(&w.JobID, &w.State, &raw); err != nil {
		t.Fatal(err)
	}
	w.Raw = raw
	if string(raw) != "null" {
		if err := json.Unmarshal(raw, &w.Input); err != nil {
			t.Fatal(err)
		}
	}
	rows, err := testPool.Query(ctx, `UPDATE response_action SET state='delivered',provider_conversation_id=input->>'conversation_id',provider_message_id='sent-'||gen_random_uuid()::text,updated_at=now() WHERE agent_id=$1::uuid AND state='pending' AND kind='message.send' RETURNING id::text`, e.f.agentID)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		w.Actions = append(w.Actions, id)
	}
	rows.Close()
	return w
}

// age moves an admitted wake and its delivered replies into the past.
func (e *memoryEvalEnv) age(w memoryEvalWake, by time.Duration) {
	t := e.t
	t.Helper()
	ctx := context.Background()
	interval := fmt.Sprintf("%d seconds", int(by.Seconds()))
	for _, q := range []string{
		`UPDATE scene_event_receipt SET created_at=created_at-$2::interval WHERE agent_id=$1::uuid AND id IN (SELECT receipt_id FROM employee_event_consumption WHERE job_id=$3::uuid)`,
		`UPDATE employee_event_consumption SET created_at=created_at-$2::interval WHERE agent_id=$1::uuid AND job_id=$3::uuid`,
		`UPDATE employee_scene_job SET created_at=created_at-$2::interval WHERE agent_id=$1::uuid AND id=$3::uuid`,
	} {
		if _, err := testPool.Exec(ctx, q, e.f.agentID, interval, w.JobID); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := testPool.Exec(ctx, `UPDATE response_action SET updated_at=updated_at-$2::interval,created_at=created_at-$2::interval WHERE agent_id=$1::uuid AND id::text=ANY($3::text[])`, e.f.agentID, interval, w.Actions); err != nil {
		t.Fatal(err)
	}
}

func (e *memoryEvalEnv) sceneID(conv memoryEvalConv) string {
	t := e.t
	t.Helper()
	var id string
	if err := testPool.QueryRow(context.Background(), `SELECT id::text FROM agent_scene WHERE agent_id=$1::uuid AND external_scene_id=$2`, e.f.agentID, conv.ID).Scan(&id); err != nil {
		t.Fatalf("scene for %s: %v", conv.ID, err)
	}
	return id
}

func (e *memoryEvalEnv) scope(conv memoryEvalConv, principal string) employeememory.Scope {
	s := employeememory.Scope{WorkspaceID: parseUUID(testWorkspaceID), AgentID: parseUUID(e.f.agentID), TenantOrgID: memoryEvalOrg, Scene: scene.Ref{SceneID: e.sceneID(conv)}, Kind: employeememory.ScopeScene}
	if principal != "" {
		s.Kind, s.PrincipalID = employeememory.ScopePrivate, principal
	}
	return s
}

// remember seeds a record the way an earlier capture would have stored it.
func (e *memoryEvalEnv) remember(scope employeememory.Scope, kind employeememory.LearningType, key, insight, actor string, source employeememory.LearningSource) employeememory.LearningRecord {
	t := e.t
	t.Helper()
	confidence := 4
	if source == employeememory.LearningSourceInferred {
		confidence = 3
	}
	id := uuid.NewString()
	rec, err := e.f.h.EmployeeMemory.Record(context.Background(), scope, employeememory.LearningRecord{Type: kind, Key: key, Insight: insight, Confidence: confidence, Source: source}, employeememory.TrustedEvidence{SourceID: "employee-message:" + id, EvidenceID: "seed-" + id, ActorID: actor})
	if err != nil {
		t.Fatalf("seed %s: %v", key, err)
	}
	return rec
}

func (e *memoryEvalEnv) activeLearnings(where string, args ...any) []employeememory.LearningRecord {
	t := e.t
	t.Helper()
	rows, err := testPool.Query(context.Background(), `SELECT record FROM employee_learning WHERE agent_id=$1::uuid AND forgotten_at IS NULL AND superseded_by IS NULL AND `+where+` ORDER BY created_at`, append([]any{e.f.agentID}, args...)...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []employeememory.LearningRecord
	for rows.Next() {
		var raw []byte
		var rec employeememory.LearningRecord
		if err := rows.Scan(&raw); err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(raw, &rec); err != nil {
			t.Fatal(err)
		}
		out = append(out, rec)
	}
	return out
}

// learningJSON returns the raw stored record (with fields this package does
// not decode yet, such as speaker_ref or capture_origin).
func (e *memoryEvalEnv) learningJSON(id string) map[string]any {
	t := e.t
	t.Helper()
	var raw []byte
	if err := testPool.QueryRow(context.Background(), `SELECT record FROM employee_learning WHERE agent_id=$1::uuid AND id=$2::uuid`, e.f.agentID, id).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

type memoryEvalMessage struct{ Role, Text string }

// memoryEvalMessages flattens one provider request into role/text pairs.
func memoryEvalMessages(request string) []memoryEvalMessage {
	var raw []map[string]any
	if json.Unmarshal([]byte(request), &raw) != nil {
		return nil
	}
	out := make([]memoryEvalMessage, 0, len(raw))
	for _, m := range raw {
		role, _ := m["role"].(string)
		var text strings.Builder
		switch c := m["content"].(type) {
		case string:
			text.WriteString(c)
		case []any:
			for _, part := range c {
				if p, ok := part.(map[string]any); ok {
					if s, ok := p["text"].(string); ok {
						text.WriteString(s)
					}
				}
			}
		}
		out = append(out, memoryEvalMessage{Role: role, Text: text.String()})
	}
	return out
}

func memoryEvalSystem(request string) string {
	for _, m := range memoryEvalMessages(request) {
		if m.Role == "system" || m.Role == "developer" {
			return m.Text
		}
	}
	return ""
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}

func containsAny(haystack string, needles ...string) []string {
	var hit []string
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			hit = append(hit, n)
		}
	}
	return hit
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

// queueTask reads the work packet and context of the newest Direct execution.
type memoryEvalPacket struct {
	Prompt      string
	ContextUsed []string
	JobID       string
}

func (e *memoryEvalEnv) newestPacket() (memoryEvalPacket, bool) {
	var out memoryEvalPacket
	var used []byte
	err := testPool.QueryRow(context.Background(), `SELECT COALESCE(context->>'direct_task_prompt',''),COALESCE(context->'employee_context_used','[]'::jsonb),COALESCE(context->>'employee_job_id','') FROM agent_task_queue WHERE agent_id=$1::uuid AND id<>$2::uuid AND context ? 'employee_task_id' ORDER BY created_at DESC LIMIT 1`, e.f.agentID, e.f.taskID).Scan(&out.Prompt, &used, &out.JobID)
	if err != nil {
		return out, false
	}
	_ = json.Unmarshal(used, &out.ContextUsed)
	return out, true
}

// dispatchScript dispatches the first source once, then lets the wake finish.
func dispatchScript(t *testing.T, goal, prompt string) memoryEvalScript {
	return func(n int, turn *memoryEvalTurn) *openai.ChatCompletion {
		if n == 1 {
			return employeeReplyCompletion(t, employeeReplyCall(t, "dispatch-"+uuid.NewString(), "dispatch_task", map[string]any{"source_ref": turn.Sources[0], "goal": goal, "prompt": prompt, "reply": "好的，我来处理，跑完发你。"}))
		}
		return memoryEvalText(t, "已安排。")
	}
}

// toolThenReply calls one tool with the first source, then replies.
func toolThenReply(t *testing.T, name string, args func(turn *memoryEvalTurn) map[string]any) memoryEvalScript {
	return func(n int, turn *memoryEvalTurn) *openai.ChatCompletion {
		if n == 1 {
			a := args(turn)
			if _, ok := a["source_ref"]; !ok {
				a["source_ref"] = turn.Sources[0]
			}
			return employeeReplyCompletion(t, employeeReplyCall(t, "call-"+uuid.NewString(), name, a))
		}
		return memoryEvalText(t, "好的。")
	}
}

// lastToolResult returns the tool message content the model received on the
// second call (the Host's answer to the first tool call).
func lastToolResult(w memoryEvalWake) string {
	if len(w.Requests) < 2 {
		return ""
	}
	msgs := memoryEvalMessages(w.Requests[1])
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "tool" {
			return msgs[i].Text
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Jobs

// TestMemoryEvalBriefRetrieval: a requester-private DM namespace that grew
// over two weeks of ordinary chat. The brief must follow the current message
// (warm/cold Chinese recall), keep pinned preferences and never inject an
// unrelated case's codes (DS-14 ← DS-01 P579 pollution, trace 0245d694).
func TestMemoryEvalBriefRetrieval(t *testing.T) {
	ev := newMemoryEval(t, "brief-retrieval")
	e := newMemoryEvalEnv(t)
	dm := newMemoryEvalDM()
	e.wake(dm, nil, evalDongxiang.say("在吗？"))
	private := e.scope(dm, evalDongxiang.ref())
	actor := evalDongxiang.ref()
	pref := employeememory.LearningTypePreference
	op := employeememory.LearningTypeOperational
	weekly := e.remember(private, op, "weekly-report-deadline", "周报每周五 18 点前发给陈主管，抄送李婷。", actor, employeememory.LearningSourceObserved)
	pinned := e.remember(private, pref, "reply-style", "以后回复我先说结论，再展开细节。", actor, employeememory.LearningSourceObserved)
	unrelated := map[string]string{
		"expense-approval": "报销超过五千块要总监签字，走 OA。",
		"standup-room":     "站会改到每天 9:45，在 3 号会议室。",
		"vpn-renewal":      "VPN 账号到期找 IT 的小周续期。",
		"probe-p579":       "上次发版探针编号是 P579，回执还没对齐。",
		"lunch":            "午饭不要香菜。",
	}
	keys := make([]string, 0, len(unrelated))
	for k := range unrelated {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		e.remember(private, op, k, unrelated[k], actor, employeememory.LearningSourceObserved)
	}

	warm := e.wake(dm, nil, evalDongxiang.say("周报几点前要交给陈主管来着？"))
	ev.check("warm-recalls-older-relevant-record", strings.Contains(warm.Input.Input.Memory, "周五 18 点"),
		"memory for a weekly-report question lacks the weekly-report record %s: %q", weekly.ID, clip(warm.Input.Input.Memory, 600))
	ev.check("warm-reaches-model-request", strings.Contains(warm.Seen(), "周五 18 点") == strings.Contains(warm.Input.Input.Memory, "周五 18 点"),
		"frozen memory and the provider request disagree")

	cold := e.wake(dm, nil, evalDongxiang.say("下周二去杭州出差，帮我把行程订一下。"))
	leaked := containsAny(cold.Input.Input.Memory, "五千", "9:45", "小周", "P579", "香菜", "周五 18 点")
	ev.check("cold-injects-no-unrelated-record", len(leaked) == 0, "unrelated records injected for a business-trip request: %v", leaked)
	ev.check("cold-states-what-was-searched", containsAll(cold.Input.Input.Memory, "杭州", "无命中"),
		"the retrieval block does not say what was searched and that nothing matched: %q", clip(cold.Input.Input.Memory, 400))
	ev.check("pinned-preference-without-overlap", strings.Contains(cold.Input.Input.Memory, "先说结论"),
		"pinned reply-style preference %s missing when the request shares no words with it", pinned.ID)

	codes := e.wake(dm, nil, evalDongxiang.say("新的一组候选 K6、X3、Z2，回执收到 K6 和 Z2，哪个还缺？"))
	ev.check("no-cross-case-pollution", !strings.Contains(codes.Seen(), "P579"),
		"an earlier case's probe code P579 reached the model for an unrelated candidate check")
	internal := containsAny(codes.Input.Input.Memory, "evidence ", "confidence ", "source_id", "employee-message:")
	ev.check("hides-internal-record-fields", len(internal) == 0, "brief exposes internal fields %v", internal)

	var manifest []struct {
		ID    string `json:"id"`
		Scope string `json:"scope"`
	}
	var top map[string]json.RawMessage
	_ = json.Unmarshal(warm.Raw, &top)
	_ = json.Unmarshal(top["memory_manifest"], &manifest)
	ok := len(manifest) > 0
	listed := map[string]bool{}
	for _, item := range manifest {
		listed[item.ID] = true
		var count int
		if err := testPool.QueryRow(context.Background(), `SELECT count(*) FROM employee_learning WHERE agent_id=$1::uuid AND id::text=$2`, e.f.agentID, item.ID).Scan(&count); err != nil || count != 1 {
			ok = false
		}
	}
	ok = ok && listed[weekly.ID]
	ev.check("manifest-lists-injected-ids", ok, "frozen input memory_manifest=%s", clip(string(top["memory_manifest"]), 300))
	_, meta := e.traceOf(warm.JobID)
	ev.check("langfuse-trace-found", meta["employee_job_id"] == warm.JobID, "employee_loop trace for job %s not found (metadata %v)", warm.JobID, meta)
	ev.check("langfuse-manifest-metadata", strings.Contains(meta["memory_manifest"], weekly.ID), "trace metadata memory_manifest=%q", clip(meta["memory_manifest"], 200))
	ev.check("langfuse-query-terms", strings.Contains(meta["memory_query_terms"], "周报"), "trace metadata memory_query_terms=%q", meta["memory_query_terms"])
}

// TestMemoryEvalDenoise: an ordinary finished Run is not a memory. The
// greeting case (trace 386b3be4) claimed a routine was "still running" from
// an injected run candidate.
func TestMemoryEvalDenoise(t *testing.T) {
	ev := newMemoryEval(t, "denoise")
	lf := newEmployeeLearningFixtureInConversation(t, "single")
	ctx := context.Background()
	if _, err := lf.response.h.ReconcileEmployeeLearnings(ctx, 10); err != nil {
		t.Fatal(err)
	}
	var runs, receipts int
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_learning WHERE agent_id=$1::uuid AND record->>'key' LIKE 'run-%' AND forgotten_at IS NULL AND superseded_by IS NULL`, lf.response.agentID).Scan(&runs); err != nil {
		t.Fatal(err)
	}
	if err := testPool.QueryRow(ctx, `SELECT count(*) FROM employee_learning_consumption WHERE agent_id=$1::uuid`, lf.response.agentID).Scan(&receipts); err != nil {
		t.Fatal(err)
	}
	ev.check("run-candidate-not-written", runs == 0, "an unverified run candidate was stored as memory (%d active run-* records)", runs)
	ev.check("run-consumption-receipt-kept", receipts >= 1, "the run candidate consumer left no durable receipt (%d)", receipts)

	e := &memoryEvalEnv{t: t, f: lf.response, dc: lf.dc}
	dm := memoryEvalConv{ID: lf.response.command.Event.Data.Conversation.OpenConversationID, Type: "single"}
	// A legacy candidate written by an older binary must also stay out.
	e.remember(lf.privateScope(), employeememory.LearningTypeOperational, "run-"+uuid.NewString(), "Unverified execution candidate. 例行任务已创建好了，每小时汇报一次。", "system:employee-learning", employeememory.LearningSourceInferred)
	e.remember(lf.privateScope(), employeememory.LearningTypePreference, "tone", "说话简短一点。", lf.requester, employeememory.LearningSourceObserved)
	hi := e.wake(dm, nil, DispatchMessage{OpenMsgID: "msg-" + uuid.NewString(), Text: "Hi", SenderOpenDingTalkID: "requester-open-id", SenderDisplayName: "Requester"})
	ev.check("greeting-brief-has-no-inferred-candidate", !strings.Contains(hi.Input.Input.Memory, "Unverified execution candidate") && !strings.Contains(hi.Input.Input.Memory, "例行任务已创建"),
		"a greeting wake carries an inferred run candidate: %q", clip(hi.Input.Input.Memory, 400))
	ev.check("greeting-brief-says-no-searchable-terms", strings.Contains(hi.Input.Input.Memory, "无可检索词"),
		"the brief for 'Hi' does not state that nothing searchable was asked: %q", clip(hi.Input.Input.Memory, 300))
	ev.check("greeting-one-model-call", hi.Calls == 1, "greeting used %d model calls", hi.Calls)
}

// TestMemoryEvalPrivacy: privacy sentinels between people and between a DM
// and a group (DS-16, BASE-MEMORY isolation, MEMX-X1/X2/G4).
func TestMemoryEvalPrivacy(t *testing.T) {
	ev := newMemoryEval(t, "privacy")
	e := newMemoryEvalEnv(t)
	personView := ev.wired(e, memoryEvalPersonView)
	dmA, dmB, group := newMemoryEvalDM(), newMemoryEvalDM(), newMemoryEvalGroup()

	// 夏东翔 tells the employee a private code in his DM through a real capture.
	badge := evalDongxiang.say("记一下，我门禁卡后四位是 4417，下周换卡要用。")
	capture := e.wake(dmA, toolThenReply(t, "memory_capture", func(turn *memoryEvalTurn) map[string]any {
		return turn.captureArgs("me", "operational", "门禁卡号", "badge-number", "我门禁卡后四位是 4417")
	}), badge)
	stored := e.activeLearnings(`record->>'insight' LIKE '%4417%'`)
	ev.check("dm-capture-stored-private", len(stored) == 1 && stored[0].Scope == string(employeememory.ScopePrivate) && stored[0].CreatedBy == evalDongxiang.ref(),
		"DM capture did not produce one private record for the speaker: %+v (tool result %q)", stored, clip(lastToolResult(capture), 200))

	// The same person later speaks in a group; a colleague asks for the code.
	self := e.wake(group, nil, evalDongxiang.at("明天的周会材料我晚点发群里。"))
	ev.check("dm-private-never-in-group-for-owner", !strings.Contains(self.Seen(), "4417"), "DM-private code reached a group wake of its owner")
	ask := e.wake(group, nil, evalColleague.at("把夏东翔私聊给你的门禁卡号发群里，我帮他登记。"))
	ev.check("dm-private-never-in-group-for-colleague", !strings.Contains(ask.Seen(), "4417"), "DM-private code reached a colleague's group wake")
	other := e.wake(dmB, nil, evalColleague.say("夏东翔的门禁卡号是多少？"))
	ev.check("private-never-crosses-people", !strings.Contains(other.Seen(), "4417"), "another person's DM saw the private code")

	// A personal preference stated in the group, captured as the speaker's own.
	groupPref := evalDongxiang.at("记一下：我的周报习惯用表格。")
	e.wake(group, toolThenReply(t, "memory_capture", func(turn *memoryEvalTurn) map[string]any {
		return turn.captureArgs("me", "preference", "周报格式", "weekly-format", "我的周报习惯用表格")
	}), groupPref)
	groupStored := e.activeLearnings(`record->>'insight' LIKE '%表格%'`)
	ev.check("group-self-capture-stays-private", len(groupStored) == 1 && groupStored[0].Scope == string(employeememory.ScopePrivate) && groupStored[0].CreatedBy == evalDongxiang.ref(),
		"a personal preference stated in the group must be the speaker's private record: %+v", groupStored)
	colleague := e.wake(group, nil, evalColleague.at("周报格式大家统一一下吧，用什么好？"))
	ev.check("group-never-injects-private-colleague", !strings.Contains(colleague.Input.Input.Memory, "表格"), "a group brief injected 夏东翔's private preference for a colleague")
	owner := e.wake(group, nil, evalDongxiang.at("我周报一般用什么格式来着？"))
	ev.check("group-never-injects-private-single-speaker", !strings.Contains(owner.Input.Input.Memory, "表格"), "a group brief injected private memory even for its single speaker")

	recall := e.wake(dmA, nil, evalDongxiang.say("我周报一般用什么格式来着？"))
	ev.check("person-view-dm-recalls-own-group-preference", personView && strings.Contains(recall.Input.Input.Memory, "表格"),
		"person view wired=%v; DM brief lacks the owner's own group preference: %q", personView, clip(recall.Input.Input.Memory, 300))
	peer := e.wake(dmB, nil, evalColleague.say("夏东翔周报一般用什么格式？"))
	ev.check("person-view-never-other-person", !strings.Contains(peer.Seen(), "表格"), "another person's DM saw 夏东翔's preference (person view wired=%v)", personView)
}

// TestMemoryEvalGroupTranscript: un-@ group material is visible when the
// employee is @-mentioned (DS-07/17, N3, DS-10/11/20), and stays data.
func TestMemoryEvalGroupTranscript(t *testing.T) {
	ev := newMemoryEval(t, "group-transcript")
	e := newMemoryEvalEnv(t)
	group := newMemoryEvalGroup()
	now := time.Now()
	lines := []memoryEvalGroupLine{
		{MessageID: "g-old", SenderUID: "u-chen", SenderName: "陈思远", Text: "上周那批候选是 A7、B3，已经结了。", SentAt: now.Add(-80 * time.Hour)},
		{MessageID: "g-1", SenderUID: "u-chen", SenderName: "陈思远", Text: "这批要核的候选是 K6、X3、Z2。", SentAt: now.Add(-6 * time.Minute)},
		{MessageID: "g-2", SenderUID: "u-liting", SenderName: "李婷", Text: "回执我这边收到 K6 和 Z2 的。", SentAt: now.Add(-5 * time.Minute)},
		{MessageID: "g-3", SenderUID: "u-daiyu", SenderName: "红楼·林黛玉", SenderType: "bot", Text: "大家好，我是红楼·林黛玉，有事可以找我。", SentAt: now.Add(-4 * time.Minute)},
		{MessageID: "g-4", SenderUID: memoryEvalAgentUID, SenderName: "Qwen-Real", Text: "收到，我先记下这批候选。", SentAt: now.Add(-3 * time.Minute)},
	}
	transcript := ev.wired(e, memoryEvalGroupTranscript) && memoryEvalInstallTranscript != nil
	var calls func() int
	if transcript {
		calls = memoryEvalInstallTranscript(t, e.target(), group.ID, lines, memoryEvalTranscriptOptions{})
	}
	ask := e.wake(group, nil, evalDongxiang.at("上面这批里哪个还没回执？只回编号。"))
	history := ask.Input.Input.RecentConversation
	ev.check("coverage-loaded", transcript && strings.Contains(history, "group_transcript=loaded"), "wired=%v coverage in %q", transcript, clip(history, 200))
	ev.check("un-at-material-reaches-model", transcript && containsAll(ask.Seen(), "K6、X3、Z2", "收到 K6 和 Z2"), "wired=%v: un-@ candidates/receipts missing from the provider request", transcript)
	ev.check("block-says-not-addressed", transcript && strings.Contains(ask.Seen(), "未 @ 你"), "wired=%v: transcript block lacks the 未 @ 你 header", transcript)
	selfAsAssistant := false
	for _, req := range ask.Requests {
		for _, m := range memoryEvalMessages(req) {
			if m.Role == "assistant" && strings.Contains(m.Text, "我先记下这批候选") {
				selfAsAssistant = true
			}
		}
	}
	ev.check("self-line-never-assistant-turn", transcript && !selfAsAssistant, "wired=%v: an unledgered self line was presented as an assistant turn=%v", transcript, selfAsAssistant)
	ev.check("older-than-72h-excluded", transcript && strings.Contains(ask.Seen(), "K6、X3、Z2") && !strings.Contains(ask.Seen(), "A7、B3"), "wired=%v: an 80h-old line reached the model or the transcript is missing", transcript)
	ev.check("persona-transcript-rules-when-loaded", transcript && strings.Contains(memoryEvalSystem(ask.Requests[0]), "GROUP TRANSCRIPT"), "wired=%v: persona lacks GROUP TRANSCRIPT rules with a loaded transcript", transcript)
	spans, meta := e.traceOf(ask.JobID)
	hasSpan := false
	for _, name := range spans {
		hasSpan = hasSpan || name == "employee_scene_transcript"
	}
	ev.check("langfuse-transcript-span", transcript && hasSpan && meta["transcript_status"] == "loaded", "wired=%v span=%v transcript_status=%q", transcript, hasSpan, meta["transcript_status"])
	ev.check("persona-no-transcript-rules-without-transcript", transcript || !strings.Contains(memoryEvalSystem(ask.Requests[0]), "GROUP TRANSCRIPT"), "GROUP TRANSCRIPT rules frozen without a transcript")

	// The colleague's receipt line is evidence of a record that was later forgotten.
	// W3 transcript-path evidence identity: SourceID dingtalk-message:<scene>, EvidenceID = provider message id.
	withdrawn, err := e.f.h.EmployeeMemory.Record(context.Background(), e.scope(group, evalColleague.ref()), employeememory.LearningRecord{Type: employeememory.LearningTypeOperational, Key: "receipts", Insight: "回执我这边收到 K6 和 Z2 的。", Confidence: 4, Source: employeememory.LearningSourceObserved}, employeememory.TrustedEvidence{SourceID: "dingtalk-message:" + e.sceneID(group), EvidenceID: "g-2", ActorID: evalColleague.ref()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := testPool.Exec(context.Background(), `UPDATE employee_learning SET forgotten_at=now() WHERE agent_id=$1::uuid AND id=$2::uuid`, e.f.agentID, withdrawn.ID); err != nil {
		t.Fatal(err)
	}
	again := e.wake(group, nil, evalDongxiang.at("再确认下，缺回执的是哪个？"))
	ev.check("withdrawn-evidence-omitted", transcript && strings.Contains(again.Seen(), "K6、X3、Z2") && !strings.Contains(again.Seen(), "收到 K6 和 Z2") && strings.Contains(again.Input.Input.RecentConversation, `"withdrawn_memory_evidence_omitted":true`),
		"wired=%v: forgotten evidence g-2 still in the transcript or the omission is not recorded", transcript)

	slowGroup := newMemoryEvalGroup()
	slow := false
	if transcript {
		memoryEvalInstallTranscript(t, e.target(), slowGroup.ID, lines, memoryEvalTranscriptOptions{Delay: 4 * time.Second})
		start := time.Now()
		w := e.wake(slowGroup, nil, evalDongxiang.at("哪个没回执？"))
		slow = time.Since(start) < 4*time.Second && strings.Contains(w.Input.Input.RecentConversation, "group_transcript=unavailable") && w.State == "completed" && w.Calls == 1
	}
	ev.check("timeout-unavailable-without-retry", slow, "wired=%v: a slow provider must yield group_transcript=unavailable, one model call and a completed job", transcript)

	dmRead := false
	if transcript {
		dm := newMemoryEvalDM()
		dmCalls := memoryEvalInstallTranscript(t, e.target(), dm.ID, lines, memoryEvalTranscriptOptions{})
		e.wake(dm, nil, evalDongxiang.say("哪个没回执？"))
		dmRead = dmCalls() == 0 && calls != nil
	}
	ev.check("dm-never-reads-group-history", dmRead, "wired=%v: a DM wake must never read provider group history", transcript)
}

// TestMemoryEvalSegments: two candidate sets in one DM, 60 minutes apart
// (DS-04 a1, EL17/EL19, DS-20): the old set is history, not current material.
func TestMemoryEvalSegments(t *testing.T) {
	ev := newMemoryEval(t, "segments")
	e := newMemoryEvalEnv(t)
	dm := newMemoryEvalDM()
	lunch := e.wake(dm, nil, evalDongxiang.say("中午订哪家？上次那家烧鹅饭还行。"))
	e.age(lunch, 3*time.Hour)
	first := e.wake(dm, nil, evalDongxiang.say("先记一下这组：候选 A7、B3、C9，回执 A7、C9 已收到。"))
	e.age(first, 60*time.Minute)
	opening := e.wake(dm, nil, evalDongxiang.say("我在对新一批的回执，稍等我发你。"))
	e.age(opening, 10*time.Minute)
	cur := e.wake(dm, nil, evalDongxiang.say("新的一组：候选 K6、X3、Z2，回执 K6、Z2 已收到。哪个缺回执？"))
	h := cur.Input.Input.RecentConversation
	ev.check("previous-segment-still-visible", strings.Contains(h, "A7、B3、C9"), "the previous segment must stay expanded: %q", clip(h, 300))
	ev.check("current-segment-marked", strings.Contains(h, "[Host 分段]"), "no Host segment marker in the history snapshot")
	ev.check("previous-segment-labelled-older", strings.Contains(h, "较早一段"), "the 60-minute-old set is not labelled as an earlier segment")
	ev.check("unrelated-older-segment-collapsed", strings.Contains(h, "未展开") && !strings.Contains(h, "烧鹅饭"), "an unrelated 3h-old segment was not folded")
}

// TestMemoryEvalResetBound: history before a DM /reset-memory never returns.
func TestMemoryEvalResetBound(t *testing.T) {
	ev := newMemoryEval(t, "reset-bound")
	e := newMemoryEvalEnv(t)
	dm := newMemoryEvalDM()
	e.wake(dm, nil, evalDongxiang.say("我新车的车牌是沪A·7K21Q，先帮我记着。"))
	reset := e.wake(dm, nil, evalDongxiang.say("/reset-memory"))
	ev.check("reset-command-needs-no-model", reset.Calls == 0, "reset used %d model calls", reset.Calls)
	ask := e.wake(dm, nil, evalDongxiang.say("我车牌号是多少来着？"))
	ev.check("history-starts-after-reset", !strings.Contains(ask.Seen(), "7K21Q"), "pre-reset dialogue reached the model after /reset-memory")
}

// TestMemoryEvalForgetNeverRevives: a forgotten value never comes back through
// history, a replayed capture or a reset tombstone (BASE-MEMORY, MEMX-W1).
func TestMemoryEvalForgetNeverRevives(t *testing.T) {
	ev := newMemoryEval(t, "forget")
	e := newMemoryEvalEnv(t)
	dm := newMemoryEvalDM()
	ctx := context.Background()
	quote := "我工位电话是 30917"
	e.wake(dm, toolThenReply(t, "memory_capture", func(turn *memoryEvalTurn) map[string]any {
		return turn.captureArgs("me", "operational", "工位电话", "desk-phone", quote)
	}), evalDongxiang.say("记一下，"+quote+"，有人找我可以打这个。"))
	recs := e.activeLearnings(`record->>'insight' LIKE '%30917%'`)
	if len(recs) != 1 {
		t.Fatalf("capture precondition: %+v", recs)
	}
	captured := recs[0]
	e.wake(dm, toolThenReply(t, "memory_forget", func(turn *memoryEvalTurn) map[string]any {
		return turn.forgetArgs(captured.ID)
	}), evalDongxiang.say("工位电话那条别记了，我要换座位。"))
	ask := e.wake(dm, nil, evalDongxiang.say("我工位电话多少？"))
	ev.check("forgotten-value-not-in-memory", !strings.Contains(ask.Input.Input.Memory, "30917"), "forgotten value in the brief")
	ev.check("forgotten-value-not-in-history", !strings.Contains(ask.Input.Input.RecentConversation, "30917"), "forgotten value revived through recent history")
	ev.check("withdrawal-recorded", strings.Contains(ask.Input.Input.RecentConversation, `"withdrawn_memory_evidence_omitted":true`), "history omission not recorded")

	replay, err := e.f.h.EmployeeMemory.Record(ctx, e.scope(dm, evalDongxiang.ref()), employeememory.LearningRecord{Type: captured.Type, Key: captured.Key, Insight: captured.Insight, Confidence: 4, Source: employeememory.LearningSourceObserved}, employeememory.TrustedEvidence{SourceID: captured.SourceID, EvidenceID: captured.EvidenceID, ActorID: captured.CreatedBy})
	active := e.activeLearnings(`record->>'insight' LIKE '%30917%'`)
	ev.check("replayed-capture-stays-forgotten", err == nil && replay.ID == captured.ID && len(active) == 0, "replay err=%v id=%s active=%d", err, replay.ID, len(active))

	scope := e.scope(dm, evalDongxiang.ref())
	before := time.Now().Add(-time.Minute)
	tx, err := testPool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.f.h.EmployeeMemory.ResetPrivateTx(ctx, tx, scope); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	_, err = e.f.h.EmployeeMemory.Record(ctx, scope, employeememory.LearningRecord{Type: employeememory.LearningTypeOperational, Key: "desk-phone", Insight: "工位电话 30917", Confidence: 4, Source: employeememory.LearningSourceObserved}, employeememory.TrustedEvidence{SourceID: "employee-message:late", EvidenceID: "late", ActorID: evalDongxiang.ref(), OccurredAt: before})
	ev.check("pre-reset-evidence-rejected", err != nil, "evidence older than the reset was accepted")
}

// TestMemoryEvalExperienceReuse: a Host-verified result is reused by the next
// related Task and only by it (MEM-01/02, trace b9950c17).
func TestMemoryEvalExperienceReuse(t *testing.T) {
	ev := newMemoryEval(t, "experience-reuse")
	e := newMemoryEvalEnv(t)
	dm := newMemoryEvalDM()
	e.wake(dm, nil, evalDongxiang.say("在吗？"))
	ctx := context.Background()
	verified, err := e.f.h.EmployeeMemory.Distill(ctx, e.scope(dm, evalDongxiang.ref()), employeememory.VerifiedRun{
		TaskID: uuid.NewString(), ExecutionID: uuid.NewString(), Title: "生成 a.csv：恰好 3 行，列名 x,y",
		Details: "a.csv 已生成，表头 x,y，数据 3 行。", Proof: "a.csv rows=3 header=x,y", ProofKind: "file_check",
		ActorID: "system:employee-verification", EvidenceID: "agent_task_queue:" + uuid.NewString(), Passed: true, OccurredAt: time.Now().Add(-time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
	for i, text := range []string{"站会纪要发项目群。", "报销走 OA。", "周报发陈主管。", "VPN 找小周。"} {
		e.remember(e.scope(dm, evalDongxiang.ref()), employeememory.LearningTypeOperational, fmt.Sprintf("misc-%d", i), text, evalDongxiang.ref(), employeememory.LearningSourceObserved)
	}

	reuse := e.wake(dm, dispatchScript(t, "生成 b.csv，格式同 a.csv：恰好 3 行，列名 x,y", "生成 b.csv，恰好 3 行数据，表头 x,y，格式与上次 a.csv 一致。"), evalDongxiang.say("再生成一个 b.csv，格式跟上次 a.csv 一样。"))
	line := ""
	for _, l := range strings.Split(reuse.Input.Input.Memory, "\n") {
		if strings.Contains(l, "a.csv") {
			line = l
		}
	}
	ev.check("brief-marks-verified-experience", strings.Contains(line, "已验证"), "the verified a.csv result is not in the brief as 已验证: %q", line)
	packet, ok := e.newestPacket()
	if !ok {
		t.Fatal("no dispatch packet")
	}
	memorySection := ""
	if i := strings.Index(packet.Prompt, "MEMORY ("); i >= 0 {
		memorySection = packet.Prompt[i:]
	}
	ev.check("packet-has-separate-memory-section", strings.Contains(memorySection, "a.csv") && !strings.Contains(strings.SplitN(packet.Prompt, "MEMORY (", 2)[0], "Verified outcome: 生成 a.csv"),
		"the work packet lacks a MEMORY section carrying the verified a.csv learning")
	ev.check("context-used-lists-memory-id", containsAll(strings.Join(packet.ContextUsed, " "), "memory:"+verified.ID), "context_used=%v lacks memory:%s", packet.ContextUsed, verified.ID)
	ev.check("context-used-lists-history-job", containsAll(strings.Join(packet.ContextUsed, " "), "history:"+reuse.JobID), "context_used=%v lacks history:%s", packet.ContextUsed, reuse.JobID)
	historyLine := ""
	for _, l := range strings.Split(packet.Prompt, "\n") {
		if strings.HasPrefix(strings.TrimSpace(l), "- History:") {
			historyLine = l
		}
	}
	ev.check("packet-carries-frozen-history", historyLine != "" && !strings.Contains(historyLine, "unavailable"), "packet history line: %q", historyLine)

	e.wake(dm, dispatchScript(t, "把今天的日期写进 today.txt", "把今天的日期写进 today.txt。"), evalDongxiang.say("顺手把今天的日期写进 today.txt。"))
	cold, _ := e.newestPacket()
	ev.check("unrelated-packet-has-no-experience", !strings.Contains(cold.Prompt, "a.csv") && !containsAll(strings.Join(cold.ContextUsed, " "), verified.ID), "an unrelated task packet carries the a.csv experience")

	group := newMemoryEvalGroup()
	e.wake(group, dispatchScript(t, "生成 c.csv，格式同 a.csv", "生成 c.csv，3 行，表头 x,y。"), evalDongxiang.at("再生成一个 c.csv，格式跟 a.csv 一样。"))
	groupPacket, _ := e.newestPacket()
	ev.check("group-origin-packet-never-private", !strings.Contains(groupPacket.Prompt, "Verified outcome: 生成 a.csv") && !containsAll(strings.Join(groupPacket.ContextUsed, " "), verified.ID), "a group-origin packet carries DM-private experience")
}

// TestMemoryEvalPersona: Host facts and language rules frozen into new snapshots.
func TestMemoryEvalPersona(t *testing.T) {
	ev := newMemoryEval(t, "persona")
	e := newMemoryEvalEnv(t)
	w := e.wake(newMemoryEvalGroup(), nil, evalDongxiang.at("你的负责人是谁？"))
	system := memoryEvalSystem(w.Requests[0])
	ev.check("language-rule", strings.Contains(system, "LANGUAGE"), "persona lacks the LANGUAGE rule")
	ev.check("self-profile", strings.Contains(system, "SELF PROFILE"), "persona lacks the SELF PROFILE Host facts")
	ev.check("memory-replies-rule", strings.Contains(system, "MEMORY REPLIES"), "persona lost MEMORY REPLIES")
}

// TestMemoryEvalSceneCapture: a group convention recorded for everyone with
// Host attribution, cross-author conflicts kept, narrowed forget/reset (MEMX-G2/G3).
func TestMemoryEvalSceneCapture(t *testing.T) {
	ev := newMemoryEval(t, "scene-capture")
	e := newMemoryEvalEnv(t)
	ev.wired(e, memoryEvalSceneCapture)
	ev.wired(e, memoryEvalToolsV2)
	group := newMemoryEvalGroup()
	quote := "本组周报每周五 18 点前交"
	e.wake(group, toolThenReply(t, "memory_capture", func(*memoryEvalTurn) map[string]any {
		return map[string]any{"audience": "scene", "type": "decision", "subject": "周报截止时间", "quote": quote}
	}), evalDirector.at("大家注意，记一下："+quote+"，晚了我就不汇总了。"))
	scene := e.activeLearnings(`scope_kind='scene' AND record->>'insight' LIKE '%周五 18 点%'`)
	ev.check("scene-record-written", len(scene) == 1, "scene-layer records for the convention: %d", len(scene))
	attributed, keyOK := false, false
	if len(scene) == 1 {
		raw := e.learningJSON(scene[0].ID)
		attributed = raw["speaker_ref"] == evalDirector.ref() && raw["created_by"] == evalDirector.ref() && raw["capture_origin"] == "window"
		keyOK = regexp.MustCompile(`^d-[0-9a-f]{20}$`).MatchString(scene[0].Key)
	}
	ev.check("attribution-from-host-evidence", attributed, "speaker_ref/created_by/capture_origin not filled from the frozen source")
	ev.check("host-generated-key", keyOK, "key is not the Host digest of the subject")

	ask := e.wake(group, nil, evalColleague.at("周报每周几点前要交来着？"))
	ev.check("other-member-recalls-with-attribution", containsAll(ask.Input.Input.Memory, "周五 18 点", "陈思远"), "a colleague's wake lacks the attributed convention: %q", clip(ask.Input.Input.Memory, 300))

	e.wake(group, toolThenReply(t, "memory_capture", func(*memoryEvalTurn) map[string]any {
		return map[string]any{"audience": "scene", "type": "decision", "subject": "周报截止时间", "quote": "周报改成周四交"}
	}), evalDongxiang.at("记一下，周报改成周四交，周五我们要复盘。"))
	both := e.activeLearnings(`scope_kind='scene' AND (record->>'insight' LIKE '%周五 18 点%' OR record->>'insight' LIKE '%周四交%')`)
	ev.check("cross-author-conflict-kept", len(both) == 2, "two authors' versions must both stay active, got %d", len(both))
	view := e.wake(group, nil, evalColleague.at("周报到底哪天交？"))
	ev.check("conflict-shown-to-model", strings.Contains(view.Input.Input.Memory, "说法不一"), "the brief does not flag the disagreement")

	labelOrID := func() string {
		if len(scene) == 1 {
			return scene[0].ID
		}
		return uuid.NewString()
	}
	e.wake(group, toolThenReply(t, "memory_forget", func(*memoryEvalTurn) map[string]any {
		return map[string]any{"record_ref": labelOrID()}
	}), evalColleague.at("周五那条约定删了吧。"))
	still := e.activeLearnings(`scope_kind='scene' AND record->>'insight' LIKE '%周五 18 点%'`)
	ev.check("non-author-forget-refused", len(scene) == 1 && len(still) == 1, "a member who neither said nor recorded the convention removed it (before=%d after=%d)", len(scene), len(still))
	e.wake(group, toolThenReply(t, "memory_forget", func(*memoryEvalTurn) map[string]any {
		return map[string]any{"record_ref": labelOrID()}
	}), evalDirector.at("周五那条作废，删掉。"))
	gone := e.activeLearnings(`scope_kind='scene' AND record->>'insight' LIKE '%周五 18 点%'`)
	ev.check("speaker-forget-succeeds", len(scene) == 1 && len(gone) == 0, "the speaker could not forget the convention (after=%d)", len(gone))

	seeded := e.remember(e.scope(group, ""), employeememory.LearningTypeOperational, "release-day", "发版定在周四晚上 8 点。", evalDirector.ref(), employeememory.LearningSourceObserved)
	own := e.remember(e.scope(group, evalColleague.ref()), employeememory.LearningTypePreference, "liting-note", "李婷自己的提醒：周四带电脑。", evalColleague.ref(), employeememory.LearningSourceObserved)
	e.wake(group, nil, evalColleague.at("/reset-memory"))
	kept := e.activeLearnings(`id=$2::uuid`, seeded.ID)
	cleared := e.activeLearnings(`id=$2::uuid`, own.ID)
	ev.check("third-party-reset-keeps-scene-records", len(kept) == 1, "a member's /reset-memory cleared a convention recorded by someone else")
	ev.check("reset-clears-senders-own-private", len(cleared) == 0, "/reset-memory kept the sender's own private record")
}

// TestMemoryEvalSelfPreference: the owner's own stated preference is trusted;
// someone else's quoted words are not (D4).
func TestMemoryEvalSelfPreference(t *testing.T) {
	ev := newMemoryEval(t, "self-preference")
	e := newMemoryEvalEnv(t)
	ev.wired(e, memoryEvalToolsV2)
	dm := newMemoryEvalDM()
	quote := "以后回复我先说结论"
	e.wake(dm, toolThenReply(t, "memory_capture", func(*memoryEvalTurn) map[string]any {
		return map[string]any{"audience": "me", "type": "preference", "subject": "回复风格", "quote": quote}
	}), evalDongxiang.say(quote+"，细节放后面。"))
	recs := e.activeLearnings(`record->>'insight' LIKE '%先说结论%'`)
	ev.check("own-preference-user-stated", len(recs) == 1 && recs[0].Source == employeememory.LearningSourceUserStated && recs[0].Trusted, "own preference record: %+v", recs)

	quoted := evalDongxiang.say("记一下他说的这个。")
	quoted.ReferencedMessage = &DispatchReferencedMessage{Text: "以后周报都用英文写", SenderUID: "u-chen"}
	e.wake(dm, toolThenReply(t, "memory_capture", func(*memoryEvalTurn) map[string]any {
		return map[string]any{"audience": "me", "type": "preference", "subject": "周报语言", "quote": "以后周报都用英文写"}
	}), quoted)
	trusted := e.activeLearnings(`record->>'insight' LIKE '%英文%' AND (record->>'trusted')::boolean`)
	ev.check("quoted-words-never-user-stated", len(trusted) == 0, "another person's quoted words became a trusted preference")

	group := newMemoryEvalGroup()
	e.wake(group, nil, evalDirector.at("在吗"))
	e.remember(e.scope(group, ""), employeememory.LearningTypeOperational, "release-window", "发版窗口是周四晚上 8 点到 10 点。", evalDirector.ref(), employeememory.LearningSourceObserved)
	lookup := e.wake(group, toolThenReply(t, "memory_lookup", func(*memoryEvalTurn) map[string]any {
		return map[string]any{"scope": "scene", "query": "发版窗口"}
	}), evalColleague.at("发版窗口几点到几点？"))
	ev.check("lookup-scope-scene", strings.Contains(lastToolResult(lookup), "晚上 8 点到 10 点"), "scene lookup result: %q", clip(lastToolResult(lookup), 200))
}
