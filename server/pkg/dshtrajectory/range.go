// Package dshtrajectory describes an explicitly scoped export of native events.
// Native sequence numbers and event payloads are never rewritten.
package dshtrajectory

import (
	"bytes"
	"encoding/json"
	"errors"
)

const RangeType = "multica/task-trajectory"
const MaxBytes = 32 << 20
const maxSafe = 1<<53 - 1

type Scope struct {
	Type      string `json:"type"`
	Version   int    `json:"version"`
	SessionID string `json:"sessionId"`
	RequestID string `json:"requestId"`
	FirstSeq  int64  `json:"firstSeq"`
	LastSeq   int64  `json:"lastSeq"`
}

type Document struct {
	Scope  Scope
	Header json.RawMessage
	Events []json.RawMessage
}

func IsRange(data []byte) bool {
	line, _, _ := bytes.Cut(data, []byte{'\n'})
	var kind struct {
		Type string `json:"type"`
	}
	return json.Unmarshal(line, &kind) == nil && kind.Type == RangeType
}

// Parse accepts one complete root turn, with exactly one admitted user RPC.
// Children need separately verified lineage and cannot enter this root range.
func Parse(data []byte) (Document, error) {
	var doc Document
	fail := func() (Document, error) { return Document{}, errors.New("invalid task-scoped DSH trajectory") }
	if len(data) == 0 || len(data) > MaxBytes {
		return fail()
	}
	lines := bytes.Split(bytes.TrimSpace(data), []byte{'\n'})
	if len(lines) < 5 || json.Unmarshal(lines[0], &doc.Scope) != nil {
		return fail()
	}
	var scopeFields map[string]json.RawMessage
	if json.Unmarshal(lines[0], &scopeFields) != nil {
		return fail()
	}
	if value, ok := scopeFields["firstSeq"]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
		return fail()
	}
	s := doc.Scope
	if s.Type != RangeType || s.Version != 1 || s.SessionID == "" || s.RequestID == "" || s.FirstSeq < 0 || s.LastSeq > maxSafe || s.LastSeq < s.FirstSeq || s.LastSeq-s.FirstSeq+1 != int64(len(lines)-2) {
		return fail()
	}
	var header struct {
		Type      string `json:"type"`
		Version   int    `json:"version"`
		ID        string `json:"id"`
		CreatedAt *int64 `json:"createdAt"`
		IsSeeded  *bool  `json:"isSeeded"`
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(lines[1], &header) != nil || json.Unmarshal(lines[1], &fields) != nil || header.Type != "session" || header.Version != 3 || header.ID != s.SessionID || header.CreatedAt == nil || *header.CreatedAt < 0 || *header.CreatedAt > maxSafe || header.IsSeeded == nil || *header.IsSeeded {
		return fail()
	}
	if _, ok := fields["parentSession"]; ok {
		return fail()
	}
	if _, ok := fields["origin"]; ok {
		return fail()
	}
	if depth, ok := fields["delegationDepth"]; ok && string(bytes.TrimSpace(depth)) != "0" {
		return fail()
	}
	doc.Header = append(json.RawMessage(nil), lines[1]...)
	var turn int64
	users := 0
	for i, line := range lines[2:] {
		var event struct {
			Type string          `json:"type"`
			Seq  *int64          `json:"seq"`
			Time *int64          `json:"time"`
			Data json.RawMessage `json:"data"`
		}
		var dataFields map[string]json.RawMessage
		if json.Unmarshal(line, &event) != nil || event.Type == "" || event.Type == "session" || event.Seq == nil || *event.Seq != s.FirstSeq+int64(i) || event.Time == nil || *event.Time < 0 || *event.Time > maxSafe || json.Unmarshal(event.Data, &dataFields) != nil || dataFields == nil {
			return fail()
		}
		var details struct {
			Turn   int64 `json:"turn"`
			Source struct {
				Kind  string `json:"kind"`
				RPCID string `json:"rpcId"`
			} `json:"source"`
			Reason struct {
				Kind string `json:"kind"`
			} `json:"reason"`
		}
		if i == 0 && event.Type != "turn/start" || i == len(lines)-3 && event.Type != "turn/end" {
			return fail()
		}
		switch event.Type {
		case "turn/start":
			if json.Unmarshal(event.Data, &details) != nil {
				return fail()
			}
			if i != 0 || details.Turn < 1 || details.Turn > maxSafe {
				return fail()
			}
			turn = details.Turn
		case "turn/end":
			if json.Unmarshal(event.Data, &details) != nil {
				return fail()
			}
			if i != len(lines)-3 || details.Turn != turn {
				return fail()
			}
			switch details.Reason.Kind {
			case "completed", "aborted", "error", "interrupted":
			default:
				return fail()
			}
		case "user/message":
			if json.Unmarshal(event.Data, &details) != nil {
				return fail()
			}
			if details.Source.Kind == "" {
				return fail()
			}
			if details.Source.Kind == "user" {
				users++
				if users != 1 || details.Source.RPCID != s.RequestID {
					return fail()
				}
			}
		}
		doc.Events = append(doc.Events, append(json.RawMessage(nil), line...))
	}
	if users != 1 {
		return fail()
	}
	return doc, nil
}

// Encode validates the same wire contract consumed by the upload endpoint.
func Encode(scope Scope, header json.RawMessage, events []json.RawMessage) ([]byte, error) {
	var out bytes.Buffer
	raw, err := json.Marshal(scope)
	if err != nil {
		return nil, err
	}
	out.Write(raw)
	out.WriteByte('\n')
	out.Write(header)
	out.WriteByte('\n')
	for _, event := range events {
		out.Write(event)
		out.WriteByte('\n')
	}
	if _, err := Parse(out.Bytes()); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
