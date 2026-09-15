package dshtrajectory

import (
	"bytes"
	"encoding/json"
	"errors"
)

const MaxChildren = 256

// Each activation has its own interval, including repeated use of one child.
// Closed=false preserves an interrupted prefix without claiming settlement.
type ChildScope struct {
	SessionID       string `json:"sessionId"`
	ParentSessionID string `json:"parentSessionId"`
	ActivationID    string `json:"activationId"`
	FirstSeq        int64  `json:"firstSeq"`
	LastSeq         int64  `json:"lastSeq"`
	Closed          bool   `json:"closed"`
}
type ChildDocument struct {
	Scope  ChildScope
	Header json.RawMessage
	Events []json.RawMessage
}
type ChildReference struct {
	RequestID      string `json:"requestId"`
	ChildSessionID string `json:"childSessionId"`
	ActivationID   string `json:"activationId"`
	FirstSeq       *int64 `json:"firstSeq"`
}

func ChildReferences(events []json.RawMessage, requestID string) ([]ChildReference, error) {
	var refs []ChildReference
	for _, raw := range events {
		var event struct {
			Type string          `json:"type"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &event) != nil {
			return nil, errors.New("invalid child reference event")
		}
		if event.Type != "multica/task-child" {
			continue
		}
		var ref ChildReference
		if json.Unmarshal(event.Data, &ref) != nil || ref.RequestID != requestID || ref.ChildSessionID == "" || len(ref.ChildSessionID) > 160 || ref.ActivationID == "" || len(ref.ActivationID) > 160 || ref.FirstSeq == nil || *ref.FirstSeq < 0 || *ref.FirstSeq > maxSafe {
			return nil, errors.New("invalid task child reference")
		}
		refs = append(refs, ref)
		if len(refs) > MaxChildren {
			return nil, errors.New("too many task child activations")
		}
	}
	return refs, nil
}

// Parse verifies a complete manifest in parent-first breadth-first order. A
// matching Session header alone never authorizes another task's child history.
func Parse(data []byte) (Document, error) {
	fail := func() (Document, error) { return Document{}, errors.New("invalid task-scoped DSH child trajectory") }
	if len(data) == 0 || len(data) > MaxBytes {
		return fail()
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) < 5 {
		return fail()
	}
	var scope Scope
	if json.Unmarshal(lines[0], &scope) != nil {
		return fail()
	}
	if scope.Version == 1 {
		doc, err := parseRoot(data)
		if err != nil {
			return doc, err
		}
		refs, err := ChildReferences(doc.Events, doc.Scope.RequestID)
		if err != nil || len(refs) > 0 || len(scope.Children) > 0 {
			return fail()
		}
		return doc, nil
	}
	if scope.Version != 2 || len(scope.Children) == 0 || len(scope.Children) > MaxChildren || scope.FirstSeq < 0 || scope.LastSeq < scope.FirstSeq || scope.LastSeq > maxSafe {
		return fail()
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(lines[0], &fields) != nil || !requiredJSON(fields, "firstSeq") || !requiredJSON(fields, "lastSeq") {
		return fail()
	}
	var rawChildren []map[string]json.RawMessage
	if json.Unmarshal(fields["children"], &rawChildren) != nil || len(rawChildren) != len(scope.Children) {
		return fail()
	}
	for _, child := range rawChildren {
		if !requiredJSON(child, "firstSeq") || !requiredJSON(child, "lastSeq") || !requiredJSON(child, "closed") {
			return fail()
		}
	}
	rootCount := scope.LastSeq - scope.FirstSeq + 1
	if rootCount > int64(len(lines)-2) {
		return fail()
	}
	rootScope := scope
	rootScope.Version = 1
	rootScope.Children = nil
	first, _ := json.Marshal(rootScope)
	rootBytes := bytes.Join(append([][]byte{first}, lines[1:2+int(rootCount)]...), []byte{'\n'})
	doc, err := parseRoot(rootBytes)
	if err != nil {
		return fail()
	}
	doc.Scope = scope
	type parentRange struct {
		id       string
		header   json.RawMessage
		events   []json.RawMessage
		ancestry map[string]bool
	}
	parents := []parentRange{{scope.SessionID, doc.Header, doc.Events, map[string]bool{scope.SessionID: true}}}
	seen := map[string]bool{}
	ranges := map[string][]ChildScope{}
	childIndex, offset := 0, 2+int(rootCount)
	for parentIndex := 0; parentIndex < len(parents); parentIndex++ {
		parent := parents[parentIndex]
		refs, err := ChildReferences(parent.events, scope.RequestID)
		if err != nil {
			return fail()
		}
		for _, ref := range refs {
			if childIndex >= len(scope.Children) {
				return fail()
			}
			child := scope.Children[childIndex]
			if child.SessionID != ref.ChildSessionID || child.ParentSessionID != parent.id || child.ActivationID != ref.ActivationID || child.FirstSeq != *ref.FirstSeq || child.LastSeq < child.FirstSeq || child.LastSeq > maxSafe || seen[child.ActivationID] || parent.ancestry[child.SessionID] {
				return fail()
			}
			seen[child.ActivationID] = true
			for _, prior := range ranges[child.SessionID] {
				if child.FirstSeq <= prior.LastSeq && prior.FirstSeq <= child.LastSeq {
					return fail()
				}
			}
			ranges[child.SessionID] = append(ranges[child.SessionID], child)
			count := child.LastSeq - child.FirstSeq + 1
			if count > int64(len(lines)-offset-1) {
				return fail()
			}
			segment := ChildDocument{Scope: child, Header: append(json.RawMessage(nil), lines[offset]...)}
			for _, line := range lines[offset+1 : offset+1+int(count)] {
				segment.Events = append(segment.Events, append(json.RawMessage(nil), line...))
			}
			if ValidateChild(segment, parent.header, scope.RequestID) != nil {
				return fail()
			}
			doc.Children = append(doc.Children, segment)
			ancestry := map[string]bool{child.SessionID: true}
			for id := range parent.ancestry {
				ancestry[id] = true
			}
			parents = append(parents, parentRange{child.SessionID, segment.Header, segment.Events, ancestry})
			offset += 1 + int(count)
			childIndex++
		}
	}
	if childIndex != len(scope.Children) || offset != len(lines) {
		return fail()
	}
	return doc, nil
}

// ValidateChild excludes seeded and earlier task content by requiring the exact
// factory-authored activation marker as the first event, with no nested marker.
func ValidateChild(child ChildDocument, parentHeader json.RawMessage, requestID string) error {
	fail := func() error { return errors.New("invalid native DSH child interval") }
	type header struct {
		Type      string `json:"type"`
		Version   int    `json:"version"`
		ID        string `json:"id"`
		Parent    string `json:"parentSession"`
		Origin    string `json:"origin"`
		Cwd       string `json:"cwd"`
		CreatedAt *int64 `json:"createdAt"`
		Seeded    *bool  `json:"isSeeded"`
		Depth     int64  `json:"delegationDepth"`
	}
	var h, p header
	if json.Unmarshal(child.Header, &h) != nil || json.Unmarshal(parentHeader, &p) != nil || h.Type != "session" || h.Version != 3 || h.ID != child.Scope.SessionID || h.Parent != child.Scope.ParentSessionID || h.Parent != p.ID || h.Origin != "subagent" || h.Cwd != p.Cwd || h.CreatedAt == nil || *h.CreatedAt < 0 || *h.CreatedAt > maxSafe || h.Seeded == nil || h.Depth <= p.Depth || h.Depth > maxSafe {
		return fail()
	}
	if child.Scope.FirstSeq < 0 || child.Scope.LastSeq > maxSafe || child.Scope.LastSeq < child.Scope.FirstSeq || child.Scope.LastSeq-child.Scope.FirstSeq+1 != int64(len(child.Events)) || len(child.Events) == 0 {
		return fail()
	}
	closed := false
	for i, raw := range child.Events {
		var e struct {
			Type string          `json:"type"`
			Seq  *int64          `json:"seq"`
			Time *int64          `json:"time"`
			Data json.RawMessage `json:"data"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &e) != nil || e.Type == "" || e.Type == "session" || e.Seq == nil || *e.Seq != child.Scope.FirstSeq+int64(i) || e.Time == nil || *e.Time < 0 || *e.Time > maxSafe || json.Unmarshal(e.Data, &fields) != nil || fields == nil {
			return fail()
		}
		if i == 0 && e.Type != "multica/task-child-start" {
			return fail()
		}
		if e.Type == "multica/task-child-start" || e.Type == "multica/task-child-end" {
			var marker struct {
				RequestID  string `json:"requestId"`
				Parent     string `json:"parentSessionId"`
				Activation string `json:"activationId"`
				First      *int64 `json:"firstSeq"`
			}
			if json.Unmarshal(e.Data, &marker) != nil || marker.RequestID != requestID || marker.Parent != h.Parent || marker.Activation != child.Scope.ActivationID {
				return fail()
			}
			if e.Type == "multica/task-child-start" {
				if i != 0 {
					return fail()
				}
			} else {
				if i != len(child.Events)-1 || marker.First == nil || *marker.First != child.Scope.FirstSeq {
					return fail()
				}
				closed = true
			}
		}
	}
	if closed != child.Scope.Closed {
		return fail()
	}
	return nil
}

func (d Document) EventCount() int {
	n := len(d.Events)
	for _, c := range d.Children {
		n += len(c.Events)
	}
	return n
}

func requiredJSON(fields map[string]json.RawMessage, key string) bool {
	v, ok := fields[key]
	return ok && !bytes.Equal(bytes.TrimSpace(v), []byte("null"))
}
