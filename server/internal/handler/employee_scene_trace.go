package handler

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strings"

	"github.com/multica-ai/multica/server/internal/employeeentry"
	"github.com/multica-ai/multica/server/internal/langfuse"
	"github.com/multica-ai/multica/server/internal/service/employeeloop"
	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	openai "github.com/openai/openai-go/v3"
)

// Trace payloads keep model context, never the authenticated dispatch envelope.
func employeeTraceStart(ctx context.Context, client *langfuse.Client, job employeeentry.Job) *langfuse.Trace {
	receipts := make([]string, 0, len(job.Items))
	for _, item := range job.Items {
		receipts = append(receipts, item.ReceiptID)
	}
	trace := client.StartTrace(ctx, langfuse.TraceOptions{
		TraceID:    job.ID,
		RootSpanID: langfuse.DeterministicSpanID(fmt.Sprintf("employee:%s:lease:%d", job.ID, job.Generation)),
		Name:       "employee_loop", Type: langfuse.TypeAgent,
		UserID: job.PrincipalID, SessionID: job.Scope.SceneID,
		Tags: []string{"employee_loop", langfuse.Tag("workspace", job.Scope.WorkspaceID), langfuse.Tag("agent", job.Scope.AgentID)},
		Metadata: map[string]any{
			"loop": "employee_loop", "employee_job_id": job.ID, "job_id": job.ID,
			"scene_id": job.Scope.SceneID, "workspace_id": job.Scope.WorkspaceID,
			"agent_id": job.Scope.AgentID, "tenant_org_id": job.Scope.TenantOrgID,
			"receipt_ids": receipts, "lease_generation": job.Generation,
		},
	})
	trace.Index(map[string]string{"employee_job_id": job.ID, "scene_id": job.Scope.SceneID})
	for _, receipt := range receipts {
		trace.Index(map[string]string{"receipt_id": receipt})
	}
	return trace
}
func employeeTraceFinish(trace *langfuse.Trace, saved employeeSavedOutcome, committed bool, err error) {
	state := "incomplete"
	if committed {
		state = "outbox_committed"
		if saved.Outcome.Kind == employeeloop.Quiet {
			state = "quiet_committed"
		}
	}
	if err == nil && saved.Failure != "" {
		err = errors.New(saved.Failure)
	}
	trace.End(langfuse.EndOptions{Output: employeeTraceSafe(map[string]any{"state": state, "decision": saved.Outcome.Decision, "failure": saved.Failure}), Err: employeeTraceError(err)})
}

// employeeTracePayloadMetadata mirrors internal/langfuse's bounded attribute
// size. A reader must check these flags before treating a payload as complete.
func employeeTracePayloadMetadata(key string, value any) map[string]any {
	raw, _ := json.Marshal(value)
	return map[string]any{key + "_bytes": len(raw), key + "_truncated": len(raw) > 64<<10}
}
func employeeTraceGeneration(ctx context.Context, job employeeentry.Job, ordinal int, request openai.ChatCompletionNewParams, extra ...map[string]any) *langfuse.Observation {
	input := employeeTraceSafe(request)
	params := make(map[string]any)
	if fields, ok := input.(map[string]any); ok {
		for k, v := range fields {
			if k != "messages" && k != "tools" && k != "model" {
				params[k] = v
			}
		}
	}
	metadata := employeeTracePayloadMetadata("input", input)
	metadata["ordinal"], metadata["lease_generation"] = ordinal, job.Generation
	for _, fields := range extra {
		for key, value := range fields {
			if _, owned := metadata[key]; !owned {
				metadata[key] = value
			}
		}
	}
	return langfuse.TraceFromContext(ctx).StartObservation(langfuse.ObservationOptions{
		Type: langfuse.TypeGeneration, Name: "employee_model",
		SpanID: langfuse.DeterministicSpanID(fmt.Sprintf("employee:%s:lease:%d:model:%d", job.ID, job.Generation, ordinal)),
		Model:  string(request.Model), Input: input, ModelParameters: params, Metadata: metadata,
	})
}
func employeeTraceEndGeneration(obs *langfuse.Observation, out *openai.ChatCompletion, err error) {
	end := langfuse.EndOptions{Err: employeeTraceError(err)}
	if out != nil {
		end.Output = employeeTraceSafe(out)
		end.Metadata = employeeTracePayloadMetadata("output", end.Output)
		end.Usage = &langfuse.Usage{
			Input: out.Usage.PromptTokens, Output: out.Usage.CompletionTokens, Total: out.Usage.TotalTokens,
			CacheRead: out.Usage.PromptTokensDetails.CachedTokens, Reasoning: out.Usage.CompletionTokensDetails.ReasoningTokens,
		}
	}
	obs.End(end)
}
func employeeTraceTool(ctx context.Context, job employeeentry.Job, call employeeloop.ToolCall) *langfuse.Observation {
	return langfuse.TraceFromContext(ctx).StartObservation(langfuse.ObservationOptions{Type: langfuse.TypeTool, Name: call.Name, SpanID: langfuse.DeterministicSpanID("employee:" + job.ID + ":tool:" + call.NativeToolCallID), Input: employeeTraceSafe(call.Arguments), Metadata: map[string]any{"native_tool_call_id": call.NativeToolCallID}})
}
func employeeTraceToolResult(ctx context.Context, obs *langfuse.Observation, call employeeloop.ToolCall, result employeeloop.ToolResult, err error) {
	obs.End(langfuse.EndOptions{Output: employeeTraceSafe(result), Err: employeeTraceError(err)})
	if call.Name != "dispatch_task" || result.Receipt == "" {
		return
	}
	var ids map[string]string
	if json.Unmarshal([]byte(result.Content), &ids) != nil {
		return
	}
	trace := langfuse.TraceFromContext(ctx)
	keys := map[string]string{"employee_task_id": ids["task_id"], "employee_run_id": ids["run_id"], "queue_task_id": ids["queue_task_id"]}
	trace.Index(keys)
	// Multiple accepted dispatches remain distinct in the tool outputs and index.
	trace.AddMetadata(map[string]any{"last_dispatch": keys})
}
func employeeTraceBatchRejected(ctx context.Context, calls []employeeloop.ToolCall, err error) {
	langfuse.TraceFromContext(ctx).Event(langfuse.ObservationOptions{Name: "tool_batch_rejected", Input: employeeTraceSafe(calls)}, langfuse.EndOptions{Err: employeeTraceError(err)})
}
func employeeTraceError(err error) error {
	if err == nil {
		return nil
	}
	return errors.New(employeeTraceText(err.Error()))
}

var employeeTraceBearer = regexp.MustCompile(`(?i)\bBearer\s+[^\s"'<>]+`)

// Match credential assignments inside ordinary prose as well as JSON fragments.
// Quoted and escaped keys must not bypass the structured-payload redaction.
var employeeTraceCredential = regexp.MustCompile(`(?i)(\b(?:[a-z0-9_.-]*authorization[a-z0-9_.-]*|[a-z0-9_.-]*secret[a-z0-9_.-]*|[a-z0-9_.-]*token|callback[_-]?bearer|api[_-]?key|password)(?:\\?["'])?\s*[=:]\s*)(?:"(?:\\.|[^"\\])*"|'(?:\\.|[^'\\])*'|\\"(?:\\.|[^"\\])*?\\"|[^\s,;}\]]+)`)

func employeeTraceText(s string) string {
	s = employeeConfigLinksInText(s)
	s = employeeTraceBearer.ReplaceAllString(s, "Bearer [redacted]")
	return employeeTraceCredential.ReplaceAllString(s, "${1}[redacted]")
}
func employeeTraceSecretKey(key string) bool {
	key = strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(key))
	return strings.Contains(key, "authorization") || strings.Contains(key, "secret") || key == "token" || strings.HasSuffix(key, "token") || strings.Contains(key, "callbackbearer") || key == "apikey" || key == "password"
}
func employeeTraceSafe(value any) any {
	raw, err := json.Marshal(value)
	if err != nil {
		return "[unserializable trace payload]"
	}
	var data any
	if json.Unmarshal(raw, &data) != nil {
		return nil
	}
	return employeeTraceWalk(data)
}
func employeeTraceWalk(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			if employeeTraceSecretKey(k) {
				out[k] = "[redacted]"
			} else {
				out[k] = employeeTraceWalk(item)
			}
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = employeeTraceWalk(item)
		}
		return out
	case string:
		var nested any
		if json.Unmarshal([]byte(v), &nested) == nil {
			switch nested.(type) {
			case map[string]any, []any:
				clean := employeeTraceWalk(nested)
				if !reflect.DeepEqual(clean, nested) {
					raw, _ := json.Marshal(clean)
					return string(raw)
				}
				return v
			}
		}
		return employeeTraceText(v)
	default:
		return v
	}
}

// A model content string can itself contain prefixed JSON (CurrentWindow),
// including several independently attributed messages. Never let the shared
// URL regexp cross JSON string delimiters. Keep the escaped ampersand together
// so a nested DingTalk deep link still loses its complete pc_slide suffix.
var employeeConfigTextSegment = regexp.MustCompile(`(?:[^"\\]|\\u0026)+`)

func employeeConfigLinksInText(text string) string {
	return employeeConfigTextSegment.ReplaceAllStringFunc(text, inboundcoord.RedactConfigLinks)
}

// employeeModelConfigLinks removes capability links from individual JSON values.
// It must not apply a URL regexp to serialized JSON: a match could cross message
// or tool-schema boundaries and discard unrelated context. Other business values,
// including credentials supplied in user content, are not trace-sanitized here.
func employeeModelConfigLinks(raw []byte) ([]byte, error) {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	changed := false
	var visit func(any) any
	visit = func(value any) any {
		switch v := value.(type) {
		case string:
			clean := employeeConfigLinksInText(v)
			changed = changed || clean != v
			return clean
		case map[string]any:
			for key, item := range v {
				v[key] = visit(item)
			}
		case []any:
			for i, item := range v {
				v[i] = visit(item)
			}
		}
		return value
	}
	value = visit(value)
	if !changed {
		return raw, nil
	}
	return json.Marshal(value)
}
