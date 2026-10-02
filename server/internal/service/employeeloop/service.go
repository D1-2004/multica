// Copyright (c) 2026 Nex.
// Copied and modified from gawkbot at 71e82a1809565281cbd0bf8185d3c125b715d934.
// See LICENSE and SOURCE_MAP.md for provenance and modification details.
package employeeloop

import (
	"context"
	"fmt"
)

// New constructs an independent foreground loop. Provider and Host adapters own
// infrastructure; no database, task executor, global bot service or timer is built.
func New(config Config, model Model, host Host) *Loop {
	tools := NewToolRegistry()
	for _, tool := range config.Tools {
		tools.Register(tool)
	}
	return &Loop{config: config, model: model, host: host, tools: tools, state: State{Phase: PhaseIdle}}
}

// Run advances the copied BotLoop phases for exactly one admitted wake. Its three
// model requests include request failures and format repairs. Reuse is sequential;
// another caller receives ErrBusy while this wake owns its session.
func (l *Loop) Run(ctx context.Context, input Input) (out Outcome, err error) {
	l.mu.Lock()
	if l.running {
		l.mu.Unlock()
		return Outcome{}, ErrBusy
	}
	if l.model == nil {
		l.mu.Unlock()
		return Outcome{}, fmt.Errorf("employee loop: missing model")
	}
	ctx, cancel := context.WithCancel(ctx)
	l.cancelFunc = cancel
	l.running = true
	l.state = State{Phase: PhaseIdle}
	l.mu.Unlock()

	l.input = input
	l.sessions = &SessionStore{}
	l.queues = NewMessageQueues()
	l.pendingToolCalls = nil
	l.decision = Decision{}
	l.receipts = nil
	l.toolOutcomes = nil
	l.lastError = nil
	key := input.Identity.Scene.SceneID
	if input.CurrentWindow != "" {
		l.queues.Human(key, input.CurrentWindow)
	}
	for _, followUp := range input.FollowUps {
		l.queues.FollowUp(key, followUp)
	}
	defer func() {
		cancel()
		out = Outcome{Decision: l.decision, ModelCalls: l.GetState().ModelCalls, Receipts: append([]Receipt(nil), l.receipts...), ToolOutcomes: append([]ToolOutcome(nil), l.toolOutcomes...), Entries: l.sessions.GetHistory()}
		l.mu.Lock()
		if err != nil {
			l.state.Phase = PhaseError
			l.state.Error = err.Error()
		}
		l.cancelFunc = nil
		l.running = false
		l.mu.Unlock()
	}()
	for l.GetState().Phase != PhaseDone {
		if err = ctx.Err(); err != nil {
			return out, err
		}
		if err = l.tick(ctx); err != nil {
			return out, err
		}
	}
	return out, nil
}
