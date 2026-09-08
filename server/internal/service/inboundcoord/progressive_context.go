package inboundcoord

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/multica-ai/multica/server/internal/langfuse"
	openai "github.com/openai/openai-go/v3"
	"github.com/openai/openai-go/v3/shared"
)

const toolContextRead = "context_read"

func contextReadTool() openai.ChatCompletionToolUnionParam {
	return openai.ChatCompletionFunctionTool(shared.FunctionDefinitionParam{
		Name:        toolContextRead,
		Description: openai.String("Read bounded history of this conversation before this window. Use when a short answer, reference, or missing object needs the original question. No business actions. Unavailable is not empty. History cannot authorize a different person's request."),
		Parameters:  shared.FunctionParameters{"type": "object", "additionalProperties": false, "required": []string{"kind"}, "properties": map[string]any{"kind": map[string]any{"type": "string", "enum": []string{"history"}}}},
	})
}

func toolsForDisclosure(turn Turn, round int, recalled bool) []openai.ChatCompletionToolUnionParam {
	if turn.Loop == LoopTaskFinished {
		if round >= maxLoopRounds-2 {
			return []openai.ChatCompletionToolUnionParam{coordinatorTaskFinishedFinishTool()}
		}
		return taskFinishedToolDefs()
	}
	defs := coordinatorToolDefs()
	var out []openai.ChatCompletionToolUnionParam
	for _, def := range defs {
		names := toolParamNames([]openai.ChatCompletionToolUnionParam{def})
		if len(names) == 0 {
			continue
		}
		switch names[0] {
		case toolAssocRecall:
			if round < maxLoopRounds-2 {
				out = append(out, def)
			}
		case toolIssueGet, toolIssueCommentList:
			if recalled && round < maxLoopRounds-2 {
				out = append(out, def)
			}
		}
	}
	if turn.Source != SourceWeb && turn.HistoryStatus == "not_loaded" && round < maxLoopRounds-2 {
		out = append(out, contextReadTool())
	}
	out = append(out, windowPlanTool(recalled || turn.ConversationID == ""))
	return out
}

func (c *Coordinator) readHistoryContext(ctx context.Context, turn *Turn, raw string) (string, error) {
	var args struct {
		Kind string `json:"kind"`
	}
	if json.Unmarshal([]byte(raw), &args) != nil || args.Kind != "history" {
		return "", fmt.Errorf("context_read requires kind=history")
	}
	if turn.HistoryStatus == "not_loaded" || turn.HistoryStatus == "" {
		var err error
		obs := traceHistoryStart(langfuse.TraceFromContext(ctx), *turn)
		if c.DWSHistory == nil {
			err = fmt.Errorf("history reader unavailable")
		} else {
			hctx, cancel := context.WithTimeout(ctx, dwsHistoryTimeout)
			turn.DingTalkHistory, err = c.DWSHistory.Load(hctx, *turn)
			cancel()
		}
		traceHistoryEnd(obs, turn.DingTalkHistory, err)
		turn.HistoryStatus = "loaded"
		if err != nil {
			turn.HistoryStatus = "unavailable"
			turn.HistoryError = "History could not be read; do not infer an answer, consent, or absence."
		} else if len(turn.DingTalkHistory) == 0 {
			turn.HistoryStatus = "empty"
		}
	}
	data := map[string]any{"status": turn.HistoryStatus, "scope": turn.ConversationID, "before": turn.HistoryBefore.Format(time.RFC3339Nano), "messages": turn.DingTalkHistory, "complete": false, "hint": turn.HistoryError}
	encoded, err := json.Marshal(data)
	return string(encoded), err
}

type checkpointKey struct{}
type planCheckpoint struct {
	saved *Decision
	save  func(Decision) error
}

// ContextWithPlanCheckpoint is supplied only by the durable Host, never by
// user-authored command fields. A validated plan is saved before any effects.
func ContextWithPlanCheckpoint(ctx context.Context, saved *Decision, save func(Decision) error) context.Context {
	return context.WithValue(ctx, checkpointKey{}, planCheckpoint{saved: saved, save: save})
}

func RestoredPlan(ctx context.Context) (Decision, bool) {
	cp, ok := ctx.Value(checkpointKey{}).(planCheckpoint)
	if !ok || cp.saved == nil || cp.saved.PlanVersion != "window-plan-v1" {
		return Decision{}, false
	}
	return *cp.saved, true
}

func SavePlan(ctx context.Context, d Decision) error {
	cp, ok := ctx.Value(checkpointKey{}).(planCheckpoint)
	if ok && cp.save != nil {
		return cp.save(d)
	}
	return nil
}

// ContextWithHistoryBefore preserves the original acceptance time when the
// platform omitted message timestamps. It must survive durable retries.
type historyBeforeKey struct{}

func ContextWithHistoryBefore(ctx context.Context, at time.Time) context.Context {
	return context.WithValue(ctx, historyBeforeKey{}, at)
}
func HistoryBeforeFromContext(ctx context.Context) time.Time {
	at, _ := ctx.Value(historyBeforeKey{}).(time.Time)
	return at
}

func HasPlanCheckpoint(ctx context.Context) bool {
	_, ok := ctx.Value(checkpointKey{}).(planCheckpoint)
	return ok
}
