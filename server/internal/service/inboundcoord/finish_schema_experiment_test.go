package inboundcoord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/multica-ai/multica/server/internal/modelregistry"
	"github.com/openai/openai-go/v3"
)

const experimentSalt = "pri47-test"

// jobIn returns a job id the experiment assigns to arm.
func jobIn(t *testing.T, arm string) string {
	t.Helper()
	for i := 0; i < 1000; i++ {
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("job-%d", i))).String()
		if finishSchemaArm(experimentSalt, id) == arm {
			return id
		}
	}
	t.Fatalf("no job id in arm %s", arm)
	return ""
}

// decisionConfig serves one runtime configuration read: the model, the
// performance switch and, while experiment is on, the experiment in mode.
func decisionConfig(model string, parent bool, experiment *atomic.Bool, mode string) func(pgtype.UUID) DecisionConfig {
	return func(pgtype.UUID) DecisionConfig {
		cfg := DecisionConfig{Model: model, Performance: parent, ConfigSHA256: "cfg-sha", ConfigGeneration: 7}
		if experiment != nil && experiment.Load() {
			cfg.FinishSchema = FinishSchemaExperiment{Enabled: true, Mode: mode, Salt: experimentSalt}
		}
		return cfg
	}
}

func experimentTurn() Turn {
	turn := windowTurn(time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC))
	turn.Message = "6 点"
	return turn
}

func TestFinishSchemaArmIsStableAndBalanced(t *testing.T) {
	counts := map[string]int{}
	moved := 0
	for i := 0; i < 2000; i++ {
		id := uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("balance-%d", i))).String()
		arm := finishSchemaArm(experimentSalt, id)
		if finishSchemaArm(experimentSalt, strings.ToUpper(id)) != arm || finishSchemaArm(experimentSalt, id) != arm {
			t.Fatal("a job must keep its group")
		}
		counts[arm]++
		if finishSchemaArm("another-salt", id) != arm {
			moved++
		}
	}
	if counts[finishSchemaPruned] < 900 || counts[finishSchemaExpanded] < 900 {
		t.Fatalf("groups must be balanced: %v", counts)
	}
	if moved < 800 {
		t.Fatalf("a new salt must reshuffle the groups, moved %d", moved)
	}
}

// firstRoutingRequest runs one decision of job and returns the canonical
// wire body of its first routing request.
func firstRoutingRequest(t *testing.T, c *Coordinator, recorder *wireRecorder, job string) map[string]any {
	t.Helper()
	ctx := context.Background()
	if job != "" {
		ctx = ContextWithTraceID(ctx, job)
	}
	return routingRequestIn(t, c, recorder, ctx)
}

func routingRequestIn(t *testing.T, c *Coordinator, recorder *wireRecorder, ctx context.Context) map[string]any {
	t.Helper()
	before := len(recorder.routing())
	c.Decide(ctx, experimentTurn())
	routing := recorder.routing()
	if len(routing) <= before {
		t.Fatal("the decision must send a routing request")
	}
	var wire map[string]any
	if err := json.Unmarshal(routing[before], &wire); err != nil {
		t.Fatal(err)
	}
	return wire
}

// splitFinish returns the wire body without the finish tool, and the finish tool.
func splitFinish(wire map[string]any) (string, string) {
	rest := map[string]any{}
	for k, v := range wire {
		rest[k] = v
	}
	var kept []any
	finish := ""
	tools, _ := wire["tools"].([]any)
	for _, tool := range tools {
		raw, _ := json.Marshal(tool)
		if strings.Contains(string(raw), `"name":"finish"`) {
			finish = string(raw)
			continue
		}
		kept = append(kept, tool)
	}
	rest["tools"] = kept
	raw, _ := json.Marshal(rest)
	return string(raw), finish
}

func experimentCoordinator(t *testing.T, model string, parent bool, experiment *atomic.Bool) (*Coordinator, *wireRecorder) {
	t.Helper()
	client, recorder := recordingLLM(t)
	c := &Coordinator{LLM: client}
	c.DecisionConfigProvider = decisionConfig(model, parent, experiment, "")
	return c, recorder
}

func TestFinishSchemaArmsDifferOnlyInTheFinishSchema(t *testing.T) {
	for _, model := range []string{"qwen3.7-plus", "deepseek-v4.1-flash"} {
		t.Run(model, func(t *testing.T) {
			var on atomic.Bool
			on.Store(true)
			c, recorder := experimentCoordinator(t, model, true, &on)
			pruned := firstRoutingRequest(t, c, recorder, jobIn(t, finishSchemaPruned))
			expanded := firstRoutingRequest(t, c, recorder, jobIn(t, finishSchemaExpanded))
			prunedRest, prunedFinish := splitFinish(pruned)
			expandedRest, expandedFinish := splitFinish(expanded)
			if prunedRest != expandedRest {
				t.Fatalf("the groups must send the same request apart from the finish schema:\n%s\n%s", prunedRest, expandedRest)
			}
			if prunedFinish == expandedFinish || len(prunedFinish) >= len(expandedFinish) {
				t.Fatalf("only the pruned group prunes the finish schema: pruned %d bytes, expanded %d bytes", len(prunedFinish), len(expandedFinish))
			}
			if pruned["max_completion_tokens"] != float64(4096) || expanded["max_completion_tokens"] != float64(4096) {
				t.Fatalf("both groups keep the finish recovery budget: %v %v", pruned["max_completion_tokens"], expanded["max_completion_tokens"])
			}

			// With the experiment off, every job takes the performance
			// switch's usual path, which is the pruned group's request.
			off, offRecorder := experimentCoordinator(t, model, true, nil)
			usual := firstRoutingRequest(t, off, offRecorder, jobIn(t, finishSchemaExpanded))
			usualRest, usualFinish := splitFinish(usual)
			if usualRest != prunedRest || usualFinish != prunedFinish {
				t.Fatal("with the experiment off the request must be the switch's usual one")
			}
		})
	}
}

func TestFinishSchemaExperimentNeedsThePerformanceSwitch(t *testing.T) {
	logs := captureLogs(t)
	var on atomic.Bool
	on.Store(true)
	c, recorder := experimentCoordinator(t, "qwen3.7-plus", false, &on)
	withExperiment := firstRoutingRequest(t, c, recorder, jobIn(t, finishSchemaPruned))
	base, baseRecorder := experimentCoordinator(t, "qwen3.7-plus", false, nil)
	without := firstRoutingRequest(t, base, baseRecorder, jobIn(t, finishSchemaPruned))
	a, _ := json.Marshal(withExperiment)
	b, _ := json.Marshal(without)
	if string(a) != string(b) {
		t.Fatal("with the performance switch off the experiment must change nothing")
	}
	if withExperiment["max_completion_tokens"] != float64(maxCompletionTokens) {
		t.Fatalf("switch off keeps the old budget, got %v", withExperiment["max_completion_tokens"])
	}
	if strings.Contains(logs.String(), "inbound_coordinator_finish_schema_assigned") {
		t.Fatal("decisions outside the switch are not in the experiment")
	}
}

func TestFinishSchemaGroupHoldsForTheWholeDecision(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("recall", toolAssocRecall, `{"since":"48h"}`),
		assistantTool("done", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"我在。"}]}`),
		assistantTool("done2", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"我在。"}]}`),
	}}
	c := &Coordinator{Chat: chat, Tools: &stubTools{}}
	provider := decisionConfig("qwen3.7-plus", true, &on, "")
	c.DecisionConfigProvider = func(id pgtype.UUID) DecisionConfig {
		cfg := provider(id)
		// The experiment is switched off while the decision is running.
		on.Store(false)
		return cfg
	}
	job := jobIn(t, finishSchemaExpanded)
	c.Decide(ContextWithTraceID(context.Background(), job), experimentTurn())
	finishSize := func(params openai.ChatCompletionNewParams) int {
		for _, tool := range params.Tools {
			if tool.OfFunction != nil && tool.OfFunction.Function.Name == toolFinish {
				raw, _ := json.Marshal(tool)
				return len(raw)
			}
		}
		return 0
	}
	if len(chat.params) < 2 {
		t.Fatalf("the decision needs two routing rounds, got %d", len(chat.params))
	}
	first, second := finishSize(chat.params[0]), finishSize(chat.params[1])
	if first == 0 || second == 0 {
		t.Fatal("each round carries the finish schema")
	}
	// A new decision of the same job after the switch-off takes the usual,
	// pruned path; the running decision kept its expanded group throughout.
	c.Decide(ContextWithTraceID(context.Background(), job), experimentTurn())
	third := finishSize(chat.params[len(chat.params)-1])
	if !(third < first && third < second) {
		t.Fatalf("the running decision keeps its group, the next one follows the switch: rounds %d %d, next %d", first, second, third)
	}
}

func TestFinishSchemaExperimentRecordsEveryDecisionBeforeItsRequests(t *testing.T) {
	logs := captureLogs(t)
	var on atomic.Bool
	on.Store(true)
	c, _ := experimentCoordinator(t, "qwen3.7-plus", true, &on)
	c.BuildID = "dev@abc123"
	job := jobIn(t, finishSchemaExpanded)
	c.Decide(ContextWithTraceID(context.Background(), job), experimentTurn())
	out := logs.String()
	assigned := strings.Index(out, "event=inbound_coordinator_finish_schema_assigned")
	request := strings.Index(out, "event=inbound_coordinator_finish_schema_request")
	if assigned < 0 || request < 0 || assigned > request {
		t.Fatalf("the group is recorded before the first request: %s", out)
	}
	for _, want := range []string{"arm=expanded", "reason=included", "job_id=" + job, "build=dev@abc123", "config_sha256=cfg-sha", "config_generation=7", "finish_schema_sha=", "finish_schema_bytes="} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %q: %s", want, out)
		}
	}

	// A decision that stops before any model request stays in the
	// denominator.
	logs2 := captureLogs(t)
	c.Ready = func(context.Context) (bool, error) { return false, nil }
	c.Decide(ContextWithTraceID(context.Background(), jobIn(t, finishSchemaPruned)), experimentTurn())
	if out := logs2.String(); !strings.Contains(out, "inbound_coordinator_finish_schema_assigned") || strings.Contains(out, "inbound_coordinator_finish_schema_request") {
		t.Fatalf("a decision without a request is still recorded: %s", out)
	}
}

func TestFinishSchemaExperimentExcludesJobsWithoutAnID(t *testing.T) {
	logs := captureLogs(t)
	var on atomic.Bool
	on.Store(true)
	c, recorder := experimentCoordinator(t, "qwen3.7-plus", true, &on)
	wire := firstRoutingRequest(t, c, recorder, "")
	off, offRecorder := experimentCoordinator(t, "qwen3.7-plus", true, nil)
	usual := firstRoutingRequest(t, off, offRecorder, "")
	_, finish := splitFinish(wire)
	_, usualFinish := splitFinish(usual)
	if finish != usualFinish {
		t.Fatal("a decision without a job id takes the usual path")
	}
	if out := logs.String(); !strings.Contains(out, "reason=excluded_no_job_id") || strings.Contains(out, "event=inbound_coordinator_finish_schema_request") {
		t.Fatalf("the exclusion is recorded and no group is claimed: %s", out)
	}
}

func TestFinishSchemaDecisionReadsTheConfigurationOnce(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	c, recorder := experimentCoordinator(t, "qwen3.7-plus", true, &on)
	reads, legacy := 0, 0
	provider := c.DecisionConfigProvider
	c.DecisionConfigProvider = func(id pgtype.UUID) DecisionConfig { reads++; return provider(id) }
	c.ModelProvider = func() string { legacy++; return "other-model" }
	c.FinishRecoveryAgentProvider = func(pgtype.UUID) bool { legacy++; return false }
	c.HistoryPrefetchAgentProvider = func(pgtype.UUID) bool { legacy++; return false }
	wire := firstRoutingRequest(t, c, recorder, jobIn(t, finishSchemaExpanded))
	if reads != 1 || legacy != 0 {
		t.Fatalf("a decision takes model, switch and experiment from one read: reads %d, other reads %d", reads, legacy)
	}
	if wire["model"] != "qwen3.7-plus" || wire["max_completion_tokens"] != float64(4096) {
		t.Fatalf("model and switch come from the same read: %v %v", wire["model"], wire["max_completion_tokens"])
	}
}

func TestFinishSchemaStopLossKeepsEveryDecisionExpanded(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	split, splitRecorder := experimentCoordinator(t, "qwen3.7-plus", true, &on)
	_, expandedFinish := splitFinish(firstRoutingRequest(t, split, splitRecorder, jobIn(t, finishSchemaExpanded)))
	_, prunedFinish := splitFinish(firstRoutingRequest(t, split, splitRecorder, jobIn(t, finishSchemaPruned)))

	logs := captureLogs(t)
	client, recorder := recordingLLM(t)
	c := &Coordinator{LLM: client, DecisionConfigProvider: decisionConfig("qwen3.7-plus", true, &on, finishSchemaModeExpanded)}
	for _, job := range []string{jobIn(t, finishSchemaPruned), jobIn(t, finishSchemaExpanded), ""} {
		wire := firstRoutingRequest(t, c, recorder, job)
		if _, finish := splitFinish(wire); finish != expandedFinish {
			t.Fatalf("the stop-loss keeps the expanded schema for job %q", job)
		}
		if wire["max_completion_tokens"] != float64(4096) {
			t.Fatal("the stop-loss keeps the rest of the performance switch")
		}
	}
	if got := strings.Count(logs.String(), "reason=forced_expanded"); got != 3 {
		t.Fatalf("every decision is recorded as forced expanded, got %d: %s", got, logs.String())
	}

	// Switching the experiment off instead returns to the switch's pruning.
	on.Store(false)
	if _, finish := splitFinish(firstRoutingRequest(t, c, recorder, jobIn(t, finishSchemaExpanded))); finish != prunedFinish {
		t.Fatal("with the experiment off the switch prunes as before")
	}
}

func TestFinishSchemaRecordKeepsTheGroupAcrossClaims(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	c, recorder := experimentCoordinator(t, "qwen3.7-plus", true, &on)
	job := jobIn(t, finishSchemaExpanded)
	var saved []FinishSchemaRecord
	claim := func(previous *FinishSchemaRecord) context.Context {
		return ContextWithFinishSchemaRecord(ContextWithTraceID(context.Background(), job), previous, func(r FinishSchemaRecord) error {
			saved = append(saved, r)
			return nil
		})
	}

	logs := captureLogs(t)
	first := routingRequestIn(t, c, recorder, claim(nil))
	if len(saved) != 1 || saved[0].Arm != finishSchemaExpanded || saved[0].SaltDigest != finishSchemaSaltDigest(experimentSalt) || saved[0].ConfigSHA256 != "cfg-sha" {
		t.Fatalf("the first split decision persists the group: %+v", saved)
	}
	if !strings.Contains(logs.String(), "record=saved") {
		t.Fatalf("the save is recorded: %s", logs.String())
	}

	record := saved[0]
	logs = captureLogs(t)
	again := routingRequestIn(t, c, recorder, claim(&record))
	if len(saved) != 1 || !strings.Contains(logs.String(), "reason=included") || !strings.Contains(logs.String(), "record=reused") {
		t.Fatalf("a later claim reuses the group: %s", logs.String())
	}
	_, firstFinish := splitFinish(first)
	if _, againFinish := splitFinish(again); againFinish != firstFinish {
		t.Fatal("a later claim keeps the group's schema")
	}

	moved := record
	moved.SaltDigest = finishSchemaSaltDigest("earlier-salt")
	logs = captureLogs(t)
	routingRequestIn(t, c, recorder, claim(&moved))
	if !strings.Contains(logs.String(), "reason=excluded_config_changed") {
		t.Fatalf("a claim after a salt change is excluded: %s", logs.String())
	}

	// The experiment was switched off between claims: the job follows the
	// switch and stays recorded as excluded.
	on.Store(false)
	usualCoordinator, usualRecorder := experimentCoordinator(t, "qwen3.7-plus", true, nil)
	_, usual := splitFinish(firstRoutingRequest(t, usualCoordinator, usualRecorder, job))
	logs = captureLogs(t)
	after := routingRequestIn(t, c, recorder, claim(&record))
	if _, finish := splitFinish(after); finish != usual {
		t.Fatal("after the switch-off the job takes the switch's usual schema")
	}
	if out := logs.String(); !strings.Contains(out, "reason=excluded_config_changed") || strings.Contains(out, "event=inbound_coordinator_finish_schema_request") {
		t.Fatalf("the job is recorded as excluded and claims no group: %s", out)
	}
}

func TestFinishSchemaCheckpointIsExcluded(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	c, recorder := experimentCoordinator(t, "qwen3.7-plus", true, &on)
	logs := captureLogs(t)
	restored := &Decision{Action: ActionReply, PlanVersion: "window-plan-v1"}
	ctx := ContextWithPlanCheckpoint(ContextWithTraceID(context.Background(), jobIn(t, finishSchemaExpanded)), restored, nil)
	c.Decide(ctx, experimentTurn())
	if recorder.count() != 0 {
		t.Fatal("a restored plan sends no model request")
	}
	if out := logs.String(); !strings.Contains(out, "reason=excluded_checkpoint") || strings.Contains(out, "event=inbound_coordinator_finish_schema_request") {
		t.Fatalf("a restored plan is recorded as excluded: %s", out)
	}
}

func TestFinishSchemaRouteFailureStaysInTheDenominator(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	c, recorder := experimentCoordinator(t, "qwen3.7-plus", true, &on)
	c.RouteProvider = func(context.Context) (*modelregistry.Route, error) { return nil, errors.New("route unavailable") }
	logs := captureLogs(t)
	got := c.Decide(ContextWithTraceID(context.Background(), jobIn(t, finishSchemaPruned)), experimentTurn())
	if got.Action != ActionDeferred || recorder.count() != 0 {
		t.Fatalf("a route failure defers without a request: %#v", got)
	}
	if out := logs.String(); !strings.Contains(out, "reason=included") || !strings.Contains(out, "route_error=true") {
		t.Fatalf("the grouped decision is still recorded: %s", out)
	}
}

func TestFinishSchemaRepairRequestIsRecorded(t *testing.T) {
	var on atomic.Bool
	on.Store(true)
	chat := &scriptedCompleter{rounds: []openai.ChatCompletion{
		assistantTool("bad", toolFinish, `{"actions":[`),
		assistantTool("good", toolFinish, `{"actions":[{"kind":"acknowledge","source_refs":["u1"],"ack_kind":"greeting","reply":"我在。"}]}`),
	}}
	c := &Coordinator{Chat: chat, Tools: &stubTools{}, DecisionConfigProvider: decisionConfig("qwen3.7-plus", true, &on, "")}
	logs := captureLogs(t)
	c.Decide(ContextWithTraceID(context.Background(), jobIn(t, finishSchemaExpanded)), experimentTurn())
	if len(chat.params) != 2 {
		t.Fatalf("the decision needs a route round and a repair, got %d", len(chat.params))
	}
	sizes := map[string]string{}
	for _, line := range strings.Split(logs.String(), "\n") {
		if !strings.Contains(line, "event=inbound_coordinator_finish_schema_request") {
			continue
		}
		fields := map[string]string{}
		for _, part := range strings.Fields(line) {
			if k, v, ok := strings.Cut(part, "="); ok {
				fields[k] = v
			}
		}
		if fields["arm"] != finishSchemaExpanded {
			t.Fatalf("every request carries the decision's group: %s", line)
		}
		sizes[fields["kind"]] = fields["finish_schema_bytes"]
	}
	if sizes["route"] == "" || sizes["finish_repair"] == "" {
		t.Fatalf("the route round and the repair are both recorded: %v", sizes)
	}
	finish := 0
	for _, tool := range chat.params[1].Tools {
		if tool.OfFunction != nil && tool.OfFunction.Function.Name == toolFinish {
			raw, _ := json.Marshal(tool)
			finish = len(raw)
		}
	}
	if sizes["finish_repair"] != fmt.Sprint(finish) {
		t.Fatalf("the repair log records the schema the repair sent: %v vs %d", sizes, finish)
	}
}
