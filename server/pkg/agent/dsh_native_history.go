package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/multica-ai/multica/server/pkg/dshtrajectory"
)

// Native history is a zero-based log. Its sequence numbers and request IDs
// are owned by DSH; a reconnect must never sort, renumber or infer completion.
type dshNativeEvent struct {
	Type string          `json:"type"`
	Seq  *int64          `json:"seq"`
	Time *int64          `json:"time"`
	Data json.RawMessage `json:"data"`
}
type dshNativeRecord struct {
	Type  string          `json:"type"`
	Event json.RawMessage `json:"event"`
}
type dshNativeSnapshot struct {
	Type    string            `json:"type"`
	Header  json.RawMessage   `json:"header"`
	Cursor  *int64            `json:"cursor"`
	Records []dshNativeRecord `json:"records"`
	HasMore *bool             `json:"hasMore"`
}
type dshNativePage struct {
	Records []dshNativeRecord `json:"records"`
	HasMore *bool             `json:"hasMore"`
}
type dshNativeCall func(context.Context, string, any) (json.RawMessage, error)

func dshNativeHistoryAddress(sessionID string, childParent ...string) map[string]string {
	if len(childParent) == 2 {
		return map[string]string{"kind": "subagent", "parentSessionId": childParent[0], "childSessionId": sessionID, "mode": childParent[1]}
	}
	return map[string]string{"kind": "session", "sessionId": sessionID}
}

func decodeDSHNativeEvent(raw json.RawMessage) (dshNativeEvent, error) {
	var event dshNativeEvent
	if json.Unmarshal(raw, &event) != nil || event.Type == "" || event.Type == "session" || event.Seq == nil || *event.Seq < 0 || event.Time == nil || *event.Time < 0 || *event.Time > 1<<53-1 {
		return event, errors.New("invalid native DSH event envelope")
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(event.Data, &data) != nil || data == nil {
		return event, errors.New("invalid native DSH event data")
	}
	return event, nil
}

// walkDSHNativeBaseline walks backwards at one frozen cursor, then visits
// the complete prefix in source order. A partial page cannot prove that a
// task's previous submission is absent. The caller opens follow first, so
// events committed while pagination runs remain on the same subscription.
func walkDSHNativeBaseline(ctx context.Context, call dshNativeCall, raw json.RawMessage, sessionID, cwd string, visit func(json.RawMessage) error, childParent ...string) (snapshot dshNativeSnapshot, resultErr error) {
	fail := func() (dshNativeSnapshot, error) {
		return snapshot, errors.New("incomplete or mismatched native DSH history")
	}
	if json.Unmarshal(raw, &snapshot) != nil || snapshot.Type != "snapshot" || snapshot.Cursor == nil || *snapshot.Cursor < -1 || snapshot.HasMore == nil {
		return fail()
	}
	var header struct {
		ID              string `json:"id"`
		Version         int    `json:"version"`
		Cwd             string `json:"cwd"`
		IsSeeded        *bool  `json:"isSeeded"`
		DelegationDepth *int   `json:"delegationDepth"`
		ParentSession   string `json:"parentSession"`
		Origin          string `json:"origin"`
	}
	var headerFields map[string]json.RawMessage
	if json.Unmarshal(snapshot.Header, &header) != nil || json.Unmarshal(snapshot.Header, &headerFields) != nil || header.ID != sessionID || header.Cwd != cwd || header.Version != 3 || header.IsSeeded == nil {
		return fail()
	}
	if len(childParent) > 0 {
		if len(childParent) != 2 || childParent[0] == "" || (childParent[1] != "one-shot" && childParent[1] != "continuable") || header.ParentSession != childParent[0] || header.Origin != "subagent" || header.DelegationDepth == nil || *header.DelegationDepth < 1 {
			return fail()
		}
	} else {
		if *header.IsSeeded || header.ParentSession != "" || header.Origin != "" {
			return fail()
		}
		// Official root Sessions omit delegationDepth; explicit null is invalid.
		if _, present := headerFields["delegationDepth"]; present && (header.DelegationDepth == nil || *header.DelegationDepth != 0) {
			return fail()
		}
	}
	// A long-lived Session can exceed the per-task artifact limit. Spool pages
	// privately until the complete prefix is validated, then replay forward.
	// This file is disposable transport buffering, never authoritative state.
	spool, err := os.CreateTemp("", "multica-dsh-history-*")
	if err != nil {
		return snapshot, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, spool.Close(), os.Remove(spool.Name()))
	}()
	type pageSpan struct{ offset, length int64 }
	pages := []pageSpan{}
	var offset int64

	records, more := snapshot.Records, *snapshot.HasMore
	end := *snapshot.Cursor + 1
	snapshot.Records = nil
	for {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		page := make([]json.RawMessage, 0, len(records))
		start := end - int64(len(records))
		if start < 0 || (len(records) == 0 && (more || end != 0)) {
			return fail()
		}
		for i, record := range records {
			if record.Type != "event" {
				return fail()
			}
			event, err := decodeDSHNativeEvent(record.Event)
			if err != nil || *event.Seq != start+int64(i) {
				return fail()
			}
			page = append(page, record.Event)
		}
		encoded, err := json.Marshal(page)
		if err != nil {
			return snapshot, err
		}
		n, err := spool.Write(encoded)
		if err != nil {
			return snapshot, err
		}
		if n != len(encoded) {
			return snapshot, io.ErrShortWrite
		}
		pages = append(pages, pageSpan{offset, int64(n)})
		offset += int64(n)
		if !more {
			if start != 0 {
				return fail()
			}
			break
		}
		if start == 0 {
			return fail()
		}
		response, err := call(ctx, "page", map[string]any{"address": dshNativeHistoryAddress(sessionID, childParent...), "throughSeq": *snapshot.Cursor, "beforeSeq": start, "maxMessages": 50})
		if err != nil {
			return snapshot, err
		}
		var previous dshNativePage
		if json.Unmarshal(response, &previous) != nil || previous.HasMore == nil {
			return fail()
		}
		end, records, more = start, previous.Records, *previous.HasMore
	}
	for i := len(pages) - 1; i >= 0; i-- {
		if err := ctx.Err(); err != nil {
			return snapshot, err
		}
		var page []json.RawMessage
		span := pages[i]
		if err := json.NewDecoder(io.NewSectionReader(spool, span.offset, span.length)).Decode(&page); err != nil {
			return snapshot, err
		}
		for _, event := range page {
			if err := ctx.Err(); err != nil {
				return snapshot, err
			}
			if err := visit(event); err != nil {
				return snapshot, err
			}
		}
	}
	return snapshot, nil
}

// Only the turn containing the task's persisted RPC request ID owns its
// result. Completed neighbouring turns, socket EOF and prompt acknowledgments
// are not completion evidence. Mixed-request turns fail instead of exporting
// somebody else's output into this task.
type dshNativeTaskHistory struct {
	requestID     string
	nextSeq       int64
	turn          int64
	inTurn        bool
	foreign       bool
	found         bool
	ownTurn       int64
	terminal      string
	ownEvents     []json.RawMessage
	pendingEvents []json.RawMessage
	turnBytes     int
	childModes    map[string]string
}

func (h *dshNativeTaskHistory) accept(raw json.RawMessage) error {
	wasTerminal := h.terminal != ""
	event, err := decodeDSHNativeEvent(raw)
	if err != nil {
		return err
	}
	if *event.Seq != h.nextSeq {
		return errors.New("native DSH history sequence gap")
	}
	h.nextSeq++
	if event.Type == "subagent/catalog" {
		if h.childModes == nil {
			h.childModes = map[string]string{}
		}
		rememberDSHChildMode(h.childModes, raw)
	}
	switch event.Type {
	case "turn/start":
		var data struct {
			Turn *int64 `json:"turn"`
		}
		if json.Unmarshal(event.Data, &data) != nil || data.Turn == nil || *data.Turn < 1 || h.inTurn || *data.Turn <= h.turn {
			return errors.New("invalid native DSH turn start")
		}
		h.turn, h.inTurn, h.foreign = *data.Turn, true, false
		h.pendingEvents = nil
		h.turnBytes = 0
	case "user/message":
		var data struct {
			Source struct {
				Kind  string `json:"kind"`
				RPCID string `json:"rpcId"`
			} `json:"source"`
		}
		if json.Unmarshal(event.Data, &data) != nil || !h.inTurn {
			return errors.New("native DSH user message is outside a turn")
		}
		if data.Source.Kind == "user" && data.Source.RPCID == h.requestID {
			if h.found || h.foreign {
				return errors.New("native DSH request has duplicate or mixed turn ownership")
			}
			h.found, h.ownTurn = true, h.turn
		} else if data.Source.Kind == "user" {
			if h.found && h.ownTurn == h.turn {
				return errors.New("native DSH turn contains another request")
			}
			h.foreign = true
			h.pendingEvents = nil
			h.turnBytes = 0
		} else if data.Source.Kind == "" {
			return errors.New("missing native DSH user message source")
		}
		// Other official source kinds are contextual inputs (plugin, skill
		// catalog/invocation, instructions), not separately admitted RPCs.
	case "turn/end":
		var data struct {
			Turn   *int64 `json:"turn"`
			Reason struct {
				Kind string `json:"kind"`
			} `json:"reason"`
		}
		if json.Unmarshal(event.Data, &data) != nil || data.Turn == nil || !h.inTurn || *data.Turn != h.turn {
			return errors.New("invalid native DSH turn end")
		}
		if h.found && h.ownTurn == h.turn {
			switch data.Reason.Kind {
			case "completed", "aborted", "error", "interrupted":
				h.terminal = data.Reason.Kind
			default:
				return errors.New("unknown native DSH terminal reason")
			}
		}
		h.inTurn = false
	}
	if h.found && h.ownTurn == h.turn && !wasTerminal {
		if len(h.ownEvents) == 0 {
			h.ownEvents = append(h.ownEvents, h.pendingEvents...)
			h.pendingEvents = nil
		}
		h.ownEvents = append(h.ownEvents, raw)
	} else if !h.found && h.inTurn && !h.foreign {
		h.pendingEvents = append(h.pendingEvents, raw)
	}
	if !wasTerminal && ((!h.found && h.inTurn && !h.foreign) || (h.found && h.ownTurn == h.turn)) {
		h.turnBytes += len(raw) + 1
		if h.turnBytes > dshtrajectory.MaxBytes {
			return errors.New("native DSH task trajectory exceeds 32 MiB")
		}
	}
	return nil
}

func (h *dshNativeTaskHistory) trajectory(header json.RawMessage, sessionID string, children ...dshtrajectory.ChildDocument) ([]byte, error) {
	if !h.found || h.terminal == "" || len(h.ownEvents) == 0 {
		return nil, errors.New("native DSH task has no complete trajectory")
	}
	raw, err := dshPhysicalHeader(header)
	if err != nil {
		return nil, err
	}
	first, err := decodeDSHNativeEvent(h.ownEvents[0])
	if err != nil {
		return nil, err
	}
	last, err := decodeDSHNativeEvent(h.ownEvents[len(h.ownEvents)-1])
	if err != nil {
		return nil, err
	}
	return dshtrajectory.Encode(dshtrajectory.Scope{Type: dshtrajectory.RangeType, Version: 1, SessionID: sessionID, RequestID: h.requestID, FirstSeq: *first.Seq, LastSeq: *last.Seq}, raw, h.ownEvents, children...)
}
func (h *dshNativeTaskHistory) result(sessionID string) (Result, error) {
	if !h.found || h.terminal == "" {
		return Result{}, errors.New("native DSH request has no terminal event")
	}
	result := Result{SessionID: sessionID, Status: "failed"}
	switch h.terminal {
	case "completed":
		result.Status = "completed"
	case "aborted":
		result.Status = "cancelled"
	default:
		result.Error = fmt.Sprintf("native DSH turn %s", h.terminal)
	}
	for _, raw := range h.ownEvents {
		event, _ := decodeDSHNativeEvent(raw)
		if event.Type != "assistant/message" {
			continue
		}
		var data struct {
			Message struct {
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(event.Data, &data) != nil {
			return Result{}, errors.New("invalid native DSH assistant message")
		}
		text := ""
		for _, part := range data.Message.Content {
			if part.Type == "text" {
				text += part.Text
			}
		}
		if text != "" {
			result.Output = text
		}
	}
	return result, nil
}
