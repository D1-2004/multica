package agent

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/multica-ai/multica/server/pkg/dshtrajectory"
)

func dshPhysicalHeader(header json.RawMessage) (json.RawMessage, error) {
	var physical map[string]json.RawMessage
	if json.Unmarshal(header, &physical) != nil || physical == nil {
		return nil, errors.New("invalid native DSH trajectory header")
	}
	physical["type"] = json.RawMessage(`"session"`)
	return json.Marshal(physical)
}

// The parent references immutable activation offsets, not entire child files.
// Follow/page freeze a prefix; a later activation cannot widen this task's range.
func (b *dshNativeBackend) childTrajectories(ctx context.Context, rootHeader json.RawMessage, rootEvents []json.RawMessage) ([]dshtrajectory.ChildDocument, error) {
	type parentRange struct {
		id     string
		header json.RawMessage
		events []json.RawMessage
	}
	physical, err := dshPhysicalHeader(rootHeader)
	if err != nil {
		return nil, err
	}
	parents := []parentRange{{b.native.SessionID, physical, rootEvents}}
	children := []dshtrajectory.ChildDocument{}
	seen := map[string]bool{}
	size := len(physical)
	for _, raw := range rootEvents {
		size += len(raw) + 1
	}
	for i := 0; i < len(parents); i++ {
		parent := parents[i]
		refs, err := dshtrajectory.ChildReferences(parent.events, b.native.RequestID)
		if err != nil {
			return nil, err
		}
		for _, ref := range refs {
			if len(children) >= dshtrajectory.MaxChildren || seen[ref.ActivationID] || !dshNativeID.MatchString(ref.ChildSessionID) || ref.ChildSessionID == b.native.SessionID {
				return nil, errors.New("invalid native DSH child graph")
			}
			seen[ref.ActivationID] = true
			var child dshtrajectory.ChildDocument
			err = b.client.follow(ctx, map[string]any{"address": map[string]string{"kind": "session", "sessionId": ref.ChildSessionID}}, func(raw json.RawMessage) (bool, error) {
				snapshot, events, readErr := readDSHNativeBaseline(ctx, b.client.call, raw, ref.ChildSessionID, b.native.WorkDir, parent.id)
				if readErr != nil {
					return false, readErr
				}
				child, readErr = selectDSHChildRange(snapshot.Header, events, parent.header, parent.id, ref)
				return true, readErr
			})
			if err != nil {
				return nil, errors.New("native DSH child trajectory could not be verified")
			}
			size += len(child.Header) + 1
			for _, raw := range child.Events {
				size += len(raw) + 1
			}
			if size > dshtrajectory.MaxBytes {
				return nil, errors.New("native DSH task trajectories exceed artifact limit")
			}
			children = append(children, child)
			parents = append(parents, parentRange{child.Scope.SessionID, child.Header, child.Events})
		}
	}
	return children, nil
}

func selectDSHChildRange(header json.RawMessage, events []json.RawMessage, parentHeader json.RawMessage, parentID string, ref dshtrajectory.ChildReference) (dshtrajectory.ChildDocument, error) {
	fail := func() (dshtrajectory.ChildDocument, error) {
		return dshtrajectory.ChildDocument{}, errors.New("invalid native DSH child range")
	}
	if ref.FirstSeq == nil || *ref.FirstSeq < 0 || *ref.FirstSeq >= int64(len(events)) {
		return fail()
	}
	physical, err := dshPhysicalHeader(header)
	if err != nil {
		return fail()
	}
	child := dshtrajectory.ChildDocument{Header: physical, Scope: dshtrajectory.ChildScope{SessionID: ref.ChildSessionID, ParentSessionID: parentID, ActivationID: ref.ActivationID, FirstSeq: *ref.FirstSeq}}
	for i := *ref.FirstSeq; i < int64(len(events)); i++ {
		event, err := decodeDSHNativeEvent(events[i])
		if err != nil || *event.Seq != i {
			return fail()
		}
		// An unclosed activation is an interrupted prefix. Never include the next
		// activation, even when it was resumed by a later task in the same Session.
		if i > *ref.FirstSeq && event.Type == "multica/task-child-start" {
			break
		}
		child.Events = append(child.Events, events[i])
		child.Scope.LastSeq = i
		if event.Type == "multica/task-child-end" {
			child.Scope.Closed = true
			break
		}
	}
	if dshtrajectory.ValidateChild(child, parentHeader, ref.RequestID) != nil {
		return fail()
	}
	return child, nil
}
