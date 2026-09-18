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
func (b *dshNativeBackend) childTrajectories(ctx context.Context, rootHeader json.RawMessage, rootEvents []json.RawMessage, rootModes map[string]string) ([]dshtrajectory.ChildDocument, error) {
	type parentRange struct {
		id     string
		header json.RawMessage
		events []json.RawMessage
		modes  map[string]string
	}
	physical, err := dshPhysicalHeader(rootHeader)
	if err != nil {
		return nil, err
	}
	parents := []parentRange{{b.native.SessionID, physical, rootEvents, rootModes}}
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
			mode := parent.modes[ref.ChildSessionID]
			if mode != "one-shot" && mode != "continuable" {
				return nil, errors.New("native DSH child catalog is unavailable")
			}
			var child dshtrajectory.ChildDocument
			childModes := map[string]string{}
			address := dshNativeHistoryAddress(ref.ChildSessionID, parent.id, mode)
			err = b.client.follow(ctx, map[string]any{"address": address}, func(raw json.RawMessage) (bool, error) {
				selected := dshChildRangeCollector{ref: ref}
				snapshot, readErr := walkDSHNativeBaseline(ctx, b.client.call, raw, ref.ChildSessionID, b.native.WorkDir, func(event json.RawMessage) error {
					rememberDSHChildMode(childModes, event)
					return selected.accept(event)
				}, parent.id, mode)
				if readErr != nil {
					return false, readErr
				}
				child, readErr = selected.document(snapshot.Header, parent.header, parent.id)
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
			parents = append(parents, parentRange{child.Scope.SessionID, child.Header, child.Events, childModes})
		}
	}
	return children, nil
}

// Official child history requires both its durable parent and catalog mode.
// Keep catalog identity across turns so a later task can resume the child.
func rememberDSHChildMode(modes map[string]string, raw json.RawMessage) {
	var event struct {
		Type string `json:"type"`
		Data struct {
			Version int    `json:"version"`
			ChildID string `json:"childId"`
			Mode    string `json:"mode"`
		} `json:"data"`
	}
	if json.Unmarshal(raw, &event) == nil && event.Type == "subagent/catalog" && event.Data.Version == 0 && event.Data.ChildID != "" {
		modes[event.Data.ChildID] = event.Data.Mode
	}
}

// Retain only the activation owned by this task while walking the full prefix.
type dshChildRangeCollector struct {
	ref     dshtrajectory.ChildReference
	events  []json.RawMessage
	bytes   int
	done    bool
	closed  bool
	lastSeq int64
}

func (c *dshChildRangeCollector) accept(raw json.RawMessage) error {
	if c.ref.FirstSeq == nil || *c.ref.FirstSeq < 0 {
		return errors.New("invalid native DSH child range")
	}
	event, err := decodeDSHNativeEvent(raw)
	if err != nil {
		return err
	}
	if c.done || *event.Seq < *c.ref.FirstSeq {
		return nil
	}
	// Never widen an interrupted activation into a later activation.
	if *event.Seq > *c.ref.FirstSeq && event.Type == "multica/task-child-start" {
		c.done = true
		return nil
	}
	c.bytes += len(raw) + 1
	if c.bytes > dshtrajectory.MaxBytes {
		return errors.New("native DSH child trajectory exceeds artifact limit")
	}
	c.events = append(c.events, raw)
	c.lastSeq = *event.Seq
	if event.Type == "multica/task-child-end" {
		c.closed, c.done = true, true
	}
	return nil
}

func (c *dshChildRangeCollector) document(header, parentHeader json.RawMessage, parentID string) (dshtrajectory.ChildDocument, error) {
	fail := func() (dshtrajectory.ChildDocument, error) {
		return dshtrajectory.ChildDocument{}, errors.New("invalid native DSH child range")
	}
	if c.ref.FirstSeq == nil || len(c.events) == 0 {
		return fail()
	}
	physical, err := dshPhysicalHeader(header)
	if err != nil {
		return fail()
	}
	child := dshtrajectory.ChildDocument{Header: physical, Events: c.events, Scope: dshtrajectory.ChildScope{SessionID: c.ref.ChildSessionID, ParentSessionID: parentID, ActivationID: c.ref.ActivationID, FirstSeq: *c.ref.FirstSeq, LastSeq: c.lastSeq, Closed: c.closed}}
	if dshtrajectory.ValidateChild(child, parentHeader, c.ref.RequestID) != nil {
		return fail()
	}
	return child, nil
}
