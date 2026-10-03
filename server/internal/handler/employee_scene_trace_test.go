package handler

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/google/uuid"
	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	openai "github.com/openai/openai-go/v3"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"net/http"
	"strings"
	"testing"
)

func employeeTraceClient(t *testing.T) (*langfuse.Client, *tracetest.InMemoryExporter) {
	t.Helper()
	e := tracetest.NewInMemoryExporter()
	c := langfuse.NewWithExporter(langfuse.Config{}, e)
	t.Cleanup(func() { _ = c.Shutdown(context.Background()) })
	return c, e
}
func employeeTraceAttr(s tracetest.SpanStub, key string) string {
	for _, a := range s.Attributes {
		if string(a.Key) == key {
			return a.Value.Emit()
		}
	}
	return ""
}
func employeeTraceKind(e *tracetest.InMemoryExporter, kind string) []tracetest.SpanStub {
	var out []tracetest.SpanStub
	for _, s := range e.GetSpans() {
		if employeeTraceAttr(s, "langfuse.observation.type") == kind {
			out = append(out, s)
		}
	}
	return out
}

type employeeTraceModel struct{ employeeReplyModelFunc }

func (*employeeTraceModel) DefaultModel() string { return "employee-test-effective-model" }

type employeeChangedTraceModel struct{ employeeReplyModelFunc }

func (*employeeChangedTraceModel) DefaultModel() string { return "changed-default-after-restart" }
func TestEmployeeTraceReplyRecordsProviderAndToolOnlyOnce(t *testing.T) {
	f, _, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	f.command.Event.Data.Messages[0].Text = "EXACT_NORMAL_PROMPT"
	c, e := employeeTraceClient(t)
	f.h.EmployeeSceneWorker.Langfuse = c
	if res := employeeHTTP(t, f, dc, uuid.NewString()); res.Code != http.StatusAccepted {
		t.Fatal(res.Body.String())
	}
	var receipt, jobID string
	if err := testPool.QueryRow(ctx, `SELECT receipt_id::text,job_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt, &jobID); err != nil {
		t.Fatal(err)
	}
	var providerRequest openai.ChatCompletionNewParams
	calls := 0
	f.h.EmployeeSceneWorker.model = &employeeTraceModel{employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		calls++
		providerRequest = p
		out := employeeReplyCompletion(t, employeeReplyCall(t, "trace-reply", "reply", map[string]any{"source_ref": receipt + "/message-1", "reply": "精确回答"}))
		out.Usage.PromptTokens = 101
		out.Usage.CompletionTokens = 17
		return out, nil
	})}
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	gens := employeeTraceKind(e, "generation")
	tools := employeeTraceKind(e, "tool")
	roots := employeeTraceKind(e, "agent")
	if len(gens) != 1 || len(tools) != 1 || len(roots) != 1 {
		t.Fatalf("generations=%d tools=%d roots=%d", len(gens), len(tools), len(roots))
	}
	raw, _ := json.Marshal(providerRequest)
	var want, got any
	_ = json.Unmarshal(raw, &want)
	_ = json.Unmarshal([]byte(employeeTraceAttr(gens[0], "langfuse.observation.input")), &got)
	wantRaw, _ := json.Marshal(want)
	gotRaw, _ := json.Marshal(got)
	if string(wantRaw) != string(gotRaw) {
		t.Fatalf("trace is not the exact provider request: %s", gotRaw)
	}
	if providerRequest.Model != "employee-test-effective-model" || employeeTraceAttr(gens[0], "langfuse.observation.model.name") != string(providerRequest.Model) {
		t.Fatal("effective model missing")
	}
	if !strings.Contains(string(raw), "EXACT_NORMAL_PROMPT") || !strings.Contains(string(raw), "parameters") || employeeTraceAttr(gens[0], "gen_ai.usage.input_tokens") != "101" {
		t.Fatal("messages, schemas or usage missing")
	}
	if roots[0].Name != "employee_loop" || roots[0].SpanContext.TraceID().String() != strings.ReplaceAll(jobID, "-", "") {
		t.Fatal("wrong root identity")
	}
	if !strings.Contains(employeeTraceAttr(roots[0], "langfuse.observation.output"), "outbox_committed") {
		t.Fatal("root lacks committed outbox boundary")
	}
	f.h.EmployeeSceneWorker.model = &employeeChangedTraceModel{f.h.EmployeeSceneWorker.model.(*employeeTraceModel).employeeReplyModelFunc}
	if _, err := testPool.Exec(ctx, `UPDATE employee_scene_job SET state='pending',outcome=NULL,available_at=now() WHERE agent_id=$1`, f.agentID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var replayOutcome []byte
	if err := testPool.QueryRow(ctx, `SELECT outcome FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&replayOutcome); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(replayOutcome), "精确回答") {
		t.Fatalf("default model change corrupted replay: %s", replayOutcome)
	}
	if calls != 1 || len(employeeTraceKind(e, "generation")) != 1 || len(employeeTraceKind(e, "tool")) != 1 {
		t.Fatal("journal replay emitted new provider/tool observations")
	}
}
func TestEmployeeTraceJournalFailuresAndNewLeaseAttempts(t *testing.T) {
	f, _, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	if res := employeeHTTP(t, f, dc, uuid.NewString()); res.Code != http.StatusAccepted {
		t.Fatal(res.Body.String())
	}
	w := f.h.EmployeeSceneWorker
	job, err := w.store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	c, e := employeeTraceClient(t)
	trace := c.StartTrace(ctx, langfuse.TraceOptions{TraceID: job.ID, Name: "employee_loop"})
	ctx = langfuse.ContextWithTrace(ctx, trace)
	defer trace.End(langfuse.EndOptions{})
	count := 0
	delegate := employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		count++
		if count == 1 {
			return nil, errors.New("provider unavailable Authorization: Bearer SECRET_SENTINEL")
		}
		return &openai.ChatCompletion{Model: "effective"}, nil
	})
	req := openai.ChatCompletionNewParams{Model: "effective", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("ordinary prompt")}}
	m := &employeeJournalModel{store: w.store, job: job, delegate: delegate}
	if _, err = m.Chat(ctx, req); err == nil {
		t.Fatal("expected provider failure")
	}
	if _, err = m.Chat(ctx, req); err != nil {
		t.Fatal(err)
	}
	replay := &employeeJournalModel{store: w.store, job: job, delegate: delegate}
	_, _ = replay.Chat(ctx, req)
	_, _ = replay.Chat(ctx, req)
	if count != 2 || len(employeeTraceKind(e, "generation")) != 2 {
		t.Fatal("failure or response replay called provider")
	}
	gens := employeeTraceKind(e, "generation")
	if employeeTraceAttr(gens[0], "langfuse.observation.level") != "ERROR" {
		t.Fatal("provider error not marked")
	}
	for _, s := range e.GetSpans() {
		b, _ := json.Marshal(s)
		if strings.Contains(string(b), "SECRET_SENTINEL") {
			t.Fatal("provider secret escaped redaction")
		}
	}
	if _, err = testPool.Exec(ctx, `UPDATE employee_scene_job SET model_journal='[]',model_attempts=0,lease_until=now()-interval '1 second' WHERE id=$1`, job.ID); err != nil {
		t.Fatal(err)
	}
	job2, err := w.store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if job2.Generation == job.Generation {
		t.Fatal("lease generation did not advance")
	}
	m = &employeeJournalModel{store: w.store, job: job2, delegate: delegate}
	if _, err = m.Chat(ctx, req); err != nil {
		t.Fatal(err)
	}
	gens = employeeTraceKind(e, "generation")
	if len(gens) != 3 || gens[0].SpanContext.SpanID() == gens[2].SpanContext.SpanID() {
		t.Fatal("new provider attempt reused generation span")
	}
}
func TestEmployeeTraceSanitizesNestedCredentialsAndConfigLinks(t *testing.T) {
	input := map[string]any{"messages": []any{map[string]any{"content": "ordinary exact prompt https://app.test/dingtalk/configure?link=CONFIG_SENTINEL"}}, "nested": map[string]any{"Authorization": "Bearer AUTH_SENTINEL", "callback_bearer": "CALLBACK_SENTINEL", "access_token": "TOKEN_SENTINEL", "clientSecret": "SECRET_SENTINEL"}, "arguments": `{"token":"ARG_SENTINEL","text":"normal arguments"}`}
	got, _ := json.Marshal(employeeTraceSafe(input))
	for _, secret := range []string{"CONFIG_SENTINEL", "AUTH_SENTINEL", "CALLBACK_SENTINEL", "TOKEN_SENTINEL", "SECRET_SENTINEL", "ARG_SENTINEL"} {
		if strings.Contains(string(got), secret) {
			t.Fatalf("leaked %s: %s", secret, got)
		}
	}
	if !strings.Contains(string(got), "ordinary exact prompt") || !strings.Contains(string(got), "normal arguments") {
		t.Fatal("normal model context removed")
	}
}

func TestEmployeeTraceDispatchIndexesCommittedTask(t *testing.T) {
	f, model, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	c, e := employeeTraceClient(t)
	f.h.EmployeeSceneWorker.Langfuse = c
	if res := employeeHTTP(t, f, dc, uuid.NewString()); res.Code != http.StatusAccepted {
		t.Fatal(res.Body.String())
	}
	var receipt string
	if err := testPool.QueryRow(ctx, `SELECT receipt_id::text FROM employee_event_consumption WHERE agent_id=$1`, f.agentID).Scan(&receipt); err != nil {
		t.Fatal(err)
	}
	model.dispatch = true
	model.sourceRef = receipt + "/message-1"
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var taskID, runID, queueID string
	if err := testPool.QueryRow(ctx, `SELECT task_id::text,id::text,queue_task_id::text FROM employee_task_run WHERE agent_id=$1`, f.agentID).Scan(&taskID, &runID, &queueID); err != nil {
		t.Fatal(err)
	}
	for key, id := range map[string]string{"employee_task_id": taskID, "employee_run_id": runID, "queue_task_id": queueID} {
		found := false
		for _, s := range e.GetSpans() {
			if s.Name == langfuse.IndexObservationName(key, id) {
				found = true
			}
		}
		if !found {
			t.Fatalf("missing dispatch index %s=%s", key, id)
		}
	}
	tools := employeeTraceKind(e, "tool")
	if len(tools) != 1 || !strings.Contains(employeeTraceAttr(tools[0], "langfuse.observation.output"), queueID) {
		t.Fatal("tool lacks committed queue identity")
	}
}

func TestEmployeeTraceRejectedBatchHasEventWithoutToolSpan(t *testing.T) {
	f, _, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	c, e := employeeTraceClient(t)
	f.h.EmployeeSceneWorker.Langfuse = c
	if res := employeeHTTP(t, f, dc, uuid.NewString()); res.Code != http.StatusAccepted {
		t.Fatal(res.Body.String())
	}
	f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		return employeeReplyCompletion(t, employeeReplyCall(t, "reply", "reply", map[string]any{"source_ref": "source", "reply": "answer"}), employeeReplyCall(t, "dispatch", "dispatch_task", map[string]any{"source_ref": "source", "goal": "goal", "prompt": "prompt", "reply": "accepted"})), nil
	})
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	rejects := 0
	for _, s := range e.GetSpans() {
		if s.Name == "tool_batch_rejected" {
			rejects++
		}
	}
	if rejects != 3 || len(employeeTraceKind(e, "generation")) != 3 || len(employeeTraceKind(e, "tool")) != 0 {
		t.Fatalf("rejections=%d generations=%d tools=%d", rejects, len(employeeTraceKind(e, "generation")), len(employeeTraceKind(e, "tool")))
	}
	assertEmployeeReplyNoTasks(t, f)
}

type employeeFailedExporter struct{}

func (employeeFailedExporter) ExportSpans(context.Context, []sdktrace.ReadOnlySpan) error {
	return errors.New("test exporter unavailable")
}
func (employeeFailedExporter) Shutdown(context.Context) error { return nil }
func TestEmployeeTraceDisabledOrFailedExporterDoesNotChangeBusiness(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "disabled", true: "export_failure"}[enabled], func(t *testing.T) {
			f, model, dc := employeeFixture(t)
			ctx := context.Background()
			f.command.CompletionCallback = nil
			f.command.ResponsePolicy = nil
			if enabled {
				c := langfuse.NewWithExporter(langfuse.Config{}, employeeFailedExporter{})
				defer c.Shutdown(ctx)
				f.h.EmployeeSceneWorker.Langfuse = c
			}
			if res := employeeHTTP(t, f, dc, uuid.NewString()); res.Code != http.StatusAccepted {
				t.Fatal(res.Body.String())
			}
			if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
				t.Fatal(err)
			}
			if model.calls != 1 {
				t.Fatalf("exporter caused %d model calls", model.calls)
			}
			var state string
			var outcome []byte
			if err := testPool.QueryRow(ctx, `SELECT state,outcome FROM employee_scene_job WHERE agent_id=$1`, f.agentID).Scan(&state, &outcome); err != nil {
				t.Fatal(err)
			}
			if state != "completed" || !strings.Contains(string(outcome), "在，需要我帮你做什么？") {
				t.Fatalf("exporter changed outcome: %s %s", state, outcome)
			}
			assertEmployeeReplyNoTasks(t, f)
		})
	}
}

func TestEmployeeTraceReportsPayloadTruncation(t *testing.T) {
	c, e := employeeTraceClient(t)
	ctx := context.Background()
	trace := c.StartTrace(ctx, langfuse.TraceOptions{Name: "employee_loop"})
	ctx = langfuse.ContextWithTrace(ctx, trace)
	req := openai.ChatCompletionNewParams{Model: "effective", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage(strings.Repeat("x", 70000))}}
	obs := employeeTraceGeneration(ctx, employeeentry.Job{ID: uuid.NewString(), Generation: 1}, 0, req)
	employeeTraceEndGeneration(obs, &openai.ChatCompletion{}, nil)
	trace.End(langfuse.EndOptions{})
	gens := employeeTraceKind(e, "generation")
	if len(gens) != 1 || employeeTraceAttr(gens[0], "langfuse.observation.metadata.input_truncated") != "true" || employeeTraceAttr(gens[0], "langfuse.observation.metadata.output_truncated") != "false" {
		t.Fatal("payload completeness metadata missing")
	}
	if !strings.Contains(employeeTraceAttr(gens[0], "langfuse.observation.input"), "[truncated ") {
		t.Fatal("shared payload limit changed")
	}
}

func TestEmployeeTraceAllExportedSurfacesRedactConfigBearers(t *testing.T) {
	c, e := employeeTraceClient(t)
	ctx := context.Background()
	job := employeeentry.Job{ID: uuid.NewString(), Generation: 1}
	trace := employeeTraceStart(ctx, c, job)
	ctx = langfuse.ContextWithTrace(ctx, trace)
	call := employeeloop.ToolCall{Name: "read_task", NativeToolCallID: "secret-test", Arguments: map[string]any{"Authorization": "Bearer TOOL_AUTH_SENTINEL", "prompt": "exact normal tool prompt"}}
	obs := employeeTraceTool(ctx, job, call)
	employeeTraceToolResult(ctx, obs, call, employeeloop.ToolResult{Content: `{"token":"RESULT_TOKEN_SENTINEL","text":"exact normal tool result"}`}, errors.New("callback_bearer=ERROR_BEARER_SENTINEL"))
	text := "https://app.test/dingtalk/configure?link=PLAIN_LINK_SENTINEL " + "https%3A%2F%2Fapp.test%2Fdingtalk%2Fconfigure%3Flink%3DENCODED_LINK_SENTINEL " + "dingtalk://dingtalkclient/page/link?url=https%3A%2F%2Fapp.test%2Fdingtalk%2Fconfigure%3Flink%3DDEEP_LINK_SENTINEL&pc_slide=true"
	employeeTraceFinish(trace, employeeSavedOutcome{Outcome: employeeloop.Outcome{Decision: employeeloop.Decision{Kind: employeeloop.Reply, Reply: text}}}, true, nil)
	raw, _ := json.Marshal(e.GetSpans())
	for _, secret := range []string{"TOOL_AUTH_SENTINEL", "RESULT_TOKEN_SENTINEL", "ERROR_BEARER_SENTINEL", "PLAIN_LINK_SENTINEL", "ENCODED_LINK_SENTINEL", "DEEP_LINK_SENTINEL"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("export leaked %s", secret)
		}
	}
	for _, normal := range []string{"exact normal tool prompt", "exact normal tool result"} {
		if !strings.Contains(string(raw), normal) {
			t.Fatalf("export dropped %s", normal)
		}
	}
}

func TestEmployeeTraceEmbeddedCredentialJSONIsRedactedEverywhere(t *testing.T) {
	for _, fragment := range []string{
		`{"api_key":"EMBEDDED_CREDENTIAL_SENTINEL"}`,
		`{"access_token": "EMBEDDED_CREDENTIAL_SENTINEL"}`,
		`{"refresh_token":"EMBEDDED_CREDENTIAL_SENTINEL"}`,
		`{"clientSecret":"EMBEDDED_CREDENTIAL_SENTINEL"}`,
		`{"callbackBearer":"EMBEDDED_CREDENTIAL_SENTINEL"}`,
		`{'password': 'EMBEDDED_CREDENTIAL_SENTINEL'}`,
		`{\"api_key\":\"EMBEDDED_CREDENTIAL_SENTINEL\"}`,
	} {
		t.Run(fragment, func(t *testing.T) {
			c, e := employeeTraceClient(t)
			ctx := context.Background()
			job := employeeentry.Job{ID: uuid.NewString(), Generation: 1}
			trace := employeeTraceStart(ctx, c, job)
			ctx = langfuse.ContextWithTrace(ctx, trace)
			text := "exact ordinary prefix 配置如下 " + fragment + " exact ordinary suffix"
			req := openai.ChatCompletionNewParams{Model: "effective", Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage(text)}}
			generation := employeeTraceGeneration(ctx, job, 0, req)
			var response openai.ChatCompletion
			raw, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": text}}}})
			if err := json.Unmarshal(raw, &response); err != nil {
				t.Fatal(err)
			}
			employeeTraceEndGeneration(generation, &response, errors.New(text))
			call := employeeloop.ToolCall{Name: "read_task", NativeToolCallID: "embedded-credential", Arguments: map[string]any{"prompt": text}}
			obs := employeeTraceTool(ctx, job, call)
			employeeTraceToolResult(ctx, obs, call, employeeloop.ToolResult{Content: text}, errors.New(text))
			employeeTraceFinish(trace, employeeSavedOutcome{Outcome: employeeloop.Outcome{Decision: employeeloop.Decision{Kind: employeeloop.Reply, Reply: text}}}, true, errors.New(text))
			for _, span := range e.GetSpans() {
				exported, _ := json.Marshal(span)
				if strings.Contains(string(exported), "EMBEDDED_CREDENTIAL_SENTINEL") {
					t.Fatalf("%s exported embedded credential", span.Name)
				}
				if span.Name == "employee_model" || span.Name == "read_task" || span.Name == "employee_loop" {
					if !strings.Contains(string(exported), "exact ordinary prefix") || !strings.Contains(string(exported), "exact ordinary suffix") {
						t.Fatalf("%s dropped ordinary prompt", span.Name)
					}
				}
			}
		})
	}
}

func TestEmployeeTraceModelConfigLinksPreserveRequestStructure(t *testing.T) {
	f, _, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	if res := employeeHTTP(t, f, dc, uuid.NewString()); res.Code != http.StatusAccepted {
		t.Fatal(res.Body.String())
	}
	worker := f.h.EmployeeSceneWorker
	job, err := worker.store.Claim(ctx)
	if err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{"model":"effective","messages":[{"role":"user","content":"https://example.com/read-this"},{"role":"user","content":"https://app.test/dingtalk/configure?link=MODEL_CONFIG_SENTINEL"},{"role":"user","content":"normal api_key=MODEL_BUSINESS_VALUE"}],"tools":[{"type":"function","function":{"name":"read","description":"https://example.com/schema","parameters":{"type":"object","properties":{"target":{"type":"string","description":"https://app.test/dingtalk/configure?link=SCHEMA_CONFIG_SENTINEL"}},"required":["target"]}}}]}`)
	var request openai.ChatCompletionNewParams
	if err = json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	var captured []byte
	model := &employeeJournalModel{store: worker.store, job: job, delegate: employeeReplyModelFunc(func(_ context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		captured, _ = json.Marshal(p)
		return &openai.ChatCompletion{}, nil
	})}
	if _, err = model.Chat(ctx, request); err != nil {
		t.Fatal(err)
	}
	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
		Tools []map[string]any `json:"tools"`
	}
	if err = json.Unmarshal(captured, &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) != 3 || body.Messages[0].Content != "https://example.com/read-this" || body.Messages[1].Content != "[configuration link]" || body.Messages[2].Content != "normal api_key=MODEL_BUSINESS_VALUE" {
		t.Fatalf("config link stripping changed message boundaries/order/content: %s", captured)
	}
	if len(body.Tools) != 1 || !strings.Contains(string(captured), "https://example.com/schema") || !strings.Contains(string(captured), `"required":["target"]`) {
		t.Fatalf("config link stripping damaged tools: %s", captured)
	}
	for _, secret := range []string{"MODEL_CONFIG_SENTINEL", "SCHEMA_CONFIG_SENTINEL"} {
		if strings.Contains(string(captured), secret) {
			t.Fatalf("provider received config bearer %s", secret)
		}
	}
}

func TestEmployeeTraceActualConversationWindowPreservesSources(t *testing.T) {
	f, base, dc := employeeFixture(t)
	ctx := context.Background()
	f.command.CompletionCallback = nil
	f.command.ResponsePolicy = nil
	const configURL = "https://app.test/dingtalk/configure?link=WINDOW_CONFIG_SENTINEL"
	f.command.Event.Data.Messages = []DispatchMessage{
		{OpenMsgID: "ordinary-url", SenderOpenDingTalkID: "requester-open-id", Text: "https://example.com/read-this"},
		{OpenMsgID: "configuration-url", SenderOpenDingTalkID: "requester-open-id", Text: configURL},
	}
	var captured openai.ChatCompletionNewParams
	f.h.EmployeeSceneWorker.model = employeeReplyModelFunc(func(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		captured = p
		return base.Chat(ctx, p)
	})
	if res := employeeHTTP(t, f, dc, uuid.NewString()); res.Code != http.StatusAccepted {
		t.Fatal(res.Body.String())
	}
	if _, err := f.h.EmployeeSceneWorker.ProcessNext(ctx); err != nil {
		t.Fatal(err)
	}
	var snapshot []byte
	var receipt string
	if err := testPool.QueryRow(ctx, `SELECT j.input_snapshot,c.receipt_id::text FROM employee_scene_job j JOIN employee_event_consumption c ON c.job_id=j.id WHERE j.agent_id=$1`, f.agentID).Scan(&snapshot, &receipt); err != nil {
		t.Fatal(err)
	}
	var input employeeSavedInput
	if err := json.Unmarshal(snapshot, &input); err != nil {
		t.Fatal(err)
	}
	var original openai.ChatCompletionNewParams
	_, err := employeeloop.New(input.Config, employeeReplyModelFunc(func(ctx context.Context, p openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
		original = p
		return base.Chat(ctx, p)
	}), nil).Run(ctx, input.Input)
	if err != nil {
		t.Fatal(err)
	}
	expectedRaw, _ := json.Marshal(original)
	actualRaw, _ := json.Marshal(captured)
	expectedRaw = []byte(strings.ReplaceAll(string(expectedRaw), configURL, "[configuration link]"))
	var expected, actual any
	_ = json.Unmarshal(expectedRaw, &expected)
	_ = json.Unmarshal(actualRaw, &actual)
	expectedRaw, _ = json.Marshal(expected)
	actualRaw, _ = json.Marshal(actual)
	if string(expectedRaw) != string(actualRaw) {
		t.Fatalf("real buildInput/Loop window or schema changed beyond config bearer removal: %s", actualRaw)
	}
	var body struct {
		Messages []struct {
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(actualRaw, &body); err != nil {
		t.Fatal(err)
	}
	found := false
	for _, message := range body.Messages {
		const prefix = "Current conversation window:\n"
		if !strings.HasPrefix(message.Content, prefix) {
			continue
		}
		found = true
		var sources []struct {
			SourceRef string          `json:"source_ref"`
			Message   DispatchMessage `json:"message"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(message.Content, prefix)), &sources); err != nil {
			t.Fatal(err)
		}
		if len(sources) != 2 || sources[0].SourceRef != receipt+"/ordinary-url" || sources[1].SourceRef != receipt+"/configuration-url" || sources[0].Message.Text != "https://example.com/read-this" || sources[1].Message.Text != "[configuration link]" {
			t.Fatalf("source identities/order/content corrupted: %+v", sources)
		}
	}
	if !found {
		t.Fatal("actual Loop current-window content missing")
	}
}

func TestEmployeeTraceNestedWindowConfigLinkFormats(t *testing.T) {
	for _, link := range []string{
		"https://app.test/dingtalk/configure?link=NESTED_CONFIG_SENTINEL",
		"https%3A%2F%2Fapp.test%2Fdingtalk%2Fconfigure%3Flink%3DNESTED_CONFIG_SENTINEL",
		"dingtalk://dingtalkclient/page/link?url=https%3A%2F%2Fapp.test%2Fdingtalk%2Fconfigure%3Flink%3DNESTED_CONFIG_SENTINEL&pc_slide=true",
	} {
		t.Run(link, func(t *testing.T) {
			sources := []map[string]any{{"source_ref": "receipt/one", "text": "https://example.com/ordinary"}, {"source_ref": "receipt/two", "text": link}}
			raw, _ := json.Marshal(sources)
			actual := employeeConfigLinksInText("Current conversation window:\n" + string(raw))
			sources[1]["text"] = "[configuration link]"
			expected, _ := json.Marshal(sources)
			if actual != "Current conversation window:\n"+string(expected) {
				t.Fatalf("nested link stripping changed window: %s", actual)
			}
		})
	}
}

func TestEmployeeTraceReplaysPreUpgradeBlankModelJournal(t *testing.T) {
	for _, failed := range []bool{false, true} {
		t.Run(map[bool]string{false: "saved_response", true: "saved_failure"}[failed], func(t *testing.T) {
			f, _, dc := employeeFixture(t)
			ctx := context.Background()
			f.command.CompletionCallback = nil
			f.command.ResponsePolicy = nil
			if res := employeeHTTP(t, f, dc, uuid.NewString()); res.Code != http.StatusAccepted {
				t.Fatal(res.Body.String())
			}
			worker := f.h.EmployeeSceneWorker
			job, err := worker.store.Claim(ctx)
			if err != nil {
				t.Fatal(err)
			}
			request := openai.ChatCompletionNewParams{Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("legacy checkpoint request https://example.com/read-this https://app.test/dingtalk/configure?link=LEGACY_CONFIG_SENTINEL")}}
			raw, err := json.Marshal(request)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = worker.store.BeginModel(ctx, job, 0, raw); err != nil {
				t.Fatal(err)
			}
			if failed {
				err = worker.store.SaveModelFailure(ctx, job, 0, "legacy provider failure")
			} else {
				completion := employeeReplyCompletion(t, employeeReplyCall(t, "legacy-accepted-call", "dispatch_task", map[string]any{"source_ref": "legacy-source", "goal": "accepted goal", "prompt": "accepted prompt", "reply": "accepted reply"}))
				response, _ := json.Marshal(completion)
				err = worker.store.SaveModel(ctx, job, 0, response)
			}
			if err != nil {
				t.Fatal(err)
			}
			c, e := employeeTraceClient(t)
			trace := employeeTraceStart(ctx, c, job)
			ctx = langfuse.ContextWithTrace(ctx, trace)
			defer trace.End(langfuse.EndOptions{})
			calls, aborts := 0, 0
			model := &employeeJournalModel{store: worker.store, job: job, abort: func() { aborts++ }, delegate: &employeeTraceModel{employeeReplyModelFunc(func(context.Context, openai.ChatCompletionNewParams) (*openai.ChatCompletion, error) {
				calls++
				return nil, errors.New("replay must not request provider")
			})}}
			completion, err := model.Chat(ctx, request)
			if failed {
				var recorded *employeeentry.ModelFailure
				if !errors.As(err, &recorded) || recorded.Message != "legacy provider failure" {
					t.Fatalf("legacy failure lost: %v", err)
				}
			} else if err != nil || completion == nil || len(completion.Choices) != 1 || completion.Choices[0].Message.ToolCalls[0].ID != "legacy-accepted-call" {
				t.Fatalf("legacy accepted response lost: %v", err)
			}
			if calls != 0 || aborts != 0 || len(employeeTraceKind(e, "generation")) != 0 {
				t.Fatalf("legacy replay provider=%d aborts=%d generations=%d", calls, aborts, len(employeeTraceKind(e, "generation")))
			}
		})
	}
}
