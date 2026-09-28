package handler

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/multica-ai/multica/server/internal/service/inboundcoord"
	db "github.com/multica-ai/multica/server/pkg/db/generated"
)

type windowHistoryLoader struct {
	mu    sync.Mutex
	turns []inboundcoord.Turn
}

func (l *windowHistoryLoader) Load(_ context.Context, turn inboundcoord.Turn) ([]inboundcoord.HistoryLine, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.turns = append(l.turns, turn)
	return []inboundcoord.HistoryLine{{Role: "菲迪", Content: "晚上几点出发？"}}, nil
}

func (l *windowHistoryLoader) loaded() []inboundcoord.Turn {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]inboundcoord.Turn(nil), l.turns...)
}

func windowHistoryCommand(messages ...DispatchMessage) DispatchCommand {
	return DispatchCommand{
		Source: DispatchSource{Type: "digital_employee"},
		Event: DispatchEvent{Domain: "channel", Type: "message.created", Data: DispatchEventData{
			Conversation: DispatchConversation{OpenConversationID: "cid-window", Type: "p2p"},
			Sender:       DispatchSender{DisplayName: "须莫", UID: "uid-sender"},
			Messages:     messages,
		}},
		ExternalIdentity: AgentDispatchExternalIdentity{DWS: &AgentDispatchDWSIdentity{UID: "24710833", OrgID: "439446171"}},
	}
}

func TestCoordinatorHistoryInputsUseTheLatestWindowMessage(t *testing.T) {
	agentID := pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	accepted := time.Date(2026, 9, 29, 1, 0, 0, 0, time.UTC)
	first := accepted.Add(-2 * time.Second)
	latest := accepted.Add(3 * time.Second)
	command := windowHistoryCommand(
		DispatchMessage{OpenMsgID: "msg-1", Text: "几点？", OccurredAt: first.UnixMilli()},
		DispatchMessage{OpenMsgID: "msg-2", Text: "6 点", OccurredAt: latest.UnixMilli()},
	)
	turn := coordinatorHistoryInputs(command, agentID, accepted)
	if !turn.HistoryBefore.Equal(latest) || !turn.MessageTimestamp.Equal(latest) {
		t.Fatalf("cutoff = %s, want the latest window message %s", turn.HistoryBefore, latest)
	}
	if turn.DWSUID != "24710833" || turn.DWSOrgID != "439446171" || turn.ConversationID == "" || turn.AgentID != agentID || len(turn.Utterances) != 2 {
		t.Fatalf("history inputs = %+v", turn)
	}
	if turn.Source != inboundcoord.SourceDigitalEmployee || turn.ChatType != "p2p" || !turn.Addressed {
		t.Fatalf("routing inputs = %+v", turn)
	}

	untimed := coordinatorHistoryInputs(windowHistoryCommand(DispatchMessage{OpenMsgID: "msg-3", Text: "好"}), agentID, accepted)
	if !untimed.HistoryBefore.Equal(accepted) {
		t.Fatalf("untimed window must fall back to the acceptance time, got %s", untimed.HistoryBefore)
	}
}

func TestCommittedCoordinatorJobReadsHistoryDuringItsWindow(t *testing.T) {
	agentID := pgtype.UUID{Bytes: [16]byte{7}, Valid: true}
	created := time.Now().UTC()
	message := DispatchMessage{OpenMsgID: "msg-1", Text: "6 点", OccurredAt: created.UnixMilli()}
	raw, err := json.Marshal(windowHistoryCommand(message))
	if err != nil {
		t.Fatal(err)
	}
	job := db.InboundCoordinatorJob{
		ID:          pgtype.UUID{Bytes: [16]byte{3}, Valid: true},
		AgentID:     agentID,
		Command:     raw,
		CreatedAt:   pgtype.Timestamptz{Time: created, Valid: true},
		AvailableAt: pgtype.Timestamptz{Time: created.Add(80 * time.Millisecond), Valid: true},
	}

	loader := &windowHistoryLoader{}
	coordinator := inboundcoord.New(nil, nil, nil)
	coordinator.DWSHistory = loader
	allowed := false
	coordinator.HistoryPrefetchAgentProvider = func(id pgtype.UUID) bool { return allowed && id == agentID }
	h := &Handler{InboundCoordinator: coordinator}
	h.InboundCoordinatorWorker = NewInboundCoordinatorJobWorker(h)

	// Outside the rollout nothing is read or scheduled.
	h.prefetchCoordinatorWindowHistory(job)
	time.Sleep(150 * time.Millisecond)
	if n := len(loader.loaded()); n != 0 {
		t.Fatalf("disabled agent read %d times during the window", n)
	}
	select {
	case <-h.InboundCoordinatorWorker.notify:
		t.Fatal("disabled agent must not wake the worker early")
	default:
	}

	allowed = true
	h.prefetchCoordinatorWindowHistory(job)
	select {
	case <-h.InboundCoordinatorWorker.notify:
	case <-time.After(2 * time.Second):
		t.Fatal("the replica holding the read must wake its workers when the window closes")
	}
	turns := loader.loaded()
	if len(turns) != 1 {
		t.Fatalf("window reads = %d, want 1", len(turns))
	}
	restored, err := restoreInboundCoordinatorCommand(job.Command, job.EndpointNamespaceID, h.TaskCompletionTargetIdentity)
	if err != nil {
		t.Fatal(err)
	}
	want := coordinatorHistoryInputs(restored, agentID, created)
	got := turns[0]
	if !got.HistoryBefore.Equal(want.HistoryBefore) || got.ConversationID != want.ConversationID || got.DWSUID != want.DWSUID || got.EvidenceID != want.EvidenceID {
		t.Fatalf("window read inputs %+v differ from the claimed decision's %+v", got, want)
	}
}

func TestWindowWakeUpFollowsTheLatestDeadlineOfAJob(t *testing.T) {
	w := NewInboundCoordinatorJobWorker(&Handler{})
	start := time.Now()
	w.WakeAt("job-1", start.Add(60*time.Millisecond))
	// A later message extended the window: the earlier wake-up is dropped.
	w.WakeAt("job-1", start.Add(200*time.Millisecond))
	select {
	case <-w.notify:
		if elapsed := time.Since(start); elapsed < 190*time.Millisecond {
			t.Fatalf("woke at %s, before the extended deadline", elapsed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the extended deadline must still wake the workers")
	}
	select {
	case <-w.notify:
		t.Fatal("a replaced wake-up must not fire")
	case <-time.After(150 * time.Millisecond):
	}
	w.wakeMu.Lock()
	pending := len(w.wakeups)
	w.wakeMu.Unlock()
	if pending != 0 {
		t.Fatalf("fired wake-ups must be forgotten, %d left", pending)
	}
}
