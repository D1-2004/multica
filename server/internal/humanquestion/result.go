// Package humanquestion defines human questions without granting routing authority.
package humanquestion

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"unicode/utf8"
)

const Version = "tag-round-result/v1"

// PromptContract is appended to a Pi run's instructions by its host.
const PromptContract = `When finishing this run, return one complete JSON object with version "tag-round-result/v1" and a nonempty summary of what you actually completed. Do not wrap it in Markdown or append prose.
If missing information, ambiguous people, scope, or required human confirmation prevents further work, stop only the actions that depend on that answer. Preserve completed work in summary and add choice with intent "clarify". Ending this run does not mean the underlying goal is complete.
If the requested work is complete and a useful optional next step exists, you may add choice with intent "suggest". Otherwise omit choice; do not invent a question every round.
choice fields: intent (clarify|suggest), kind (single|multiple|person), question, options (2 to 20 objects with id, label, optional description and emphasis (primary|secondary|none)), allow_custom (boolean, optional), min and max (optional positive integers; zero means omitted). single and person select exactly one; multiple defaults to min=1 and max=the number of options. A person question uses frozen, verified candidates as options; an option id is only a choice identifier, not authorization or an unverified native user identity.
Do not execute an action that requires the answer before receiving it. Do not wait, poll, or restore a session for human input; the host returns the human's words in a fresh run. Ordinary typed answers may add constraints beyond the options.
Never output scene, task, run, actor, session, permission, or other routing/authorization fields. The host supplies those bindings.
Example: {"version":"tag-round-result/v1","summary":"Prepared a draft; have not sent it.","choice":{"intent":"clarify","kind":"single","question":"Which draft should I use?","options":[{"id":"short","label":"Short version"},{"id":"full","label":"Full version"}],"allow_custom":true}}`

// Result is the model-authored part of a completed run. Host bindings stay outside it.
type Result struct {
	Version string  `json:"version"`
	Summary string  `json:"summary"`
	Choice  *Choice `json:"choice,omitempty"`
}

// Choice is either required clarification or an optional next step.
type Choice struct {
	Intent      string   `json:"intent"`
	Kind        string   `json:"kind"`
	Question    string   `json:"question"`
	Options     []Option `json:"options"`
	AllowCustom bool     `json:"allow_custom,omitempty"`
	Min         int      `json:"min,omitempty"`
	Max         int      `json:"max,omitempty"`
}

// Option identifies a frozen candidate. It does not grant permission to use it.
type Option struct {
	ID          string `json:"id"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Emphasis    string `json:"emphasis,omitempty"`
}

// Decode accepts only the whole final response, never a chunk or stdout log.
// recognized means the top-level version claims this protocol; callers must
// reject a recognized response with an error instead of displaying it as success.
// Plain text, Markdown, nested envelopes and other versions are not control data.
func Decode(raw string) (Result, bool, error) {
	scan := resultScan{decoder: json.NewDecoder(strings.NewReader(raw))}
	scan.decoder.UseNumber()
	first, err := scan.decoder.Token()
	if err != nil || first != json.Delim('{') {
		return Result{}, false, nil
	}
	err = scan.object(0)
	if !scan.recognized {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, true, fmt.Errorf("invalid round result JSON: %w", err)
	}
	if _, err := scan.decoder.Token(); err != io.EOF {
		return Result{}, true, errors.New("round result must contain exactly one complete JSON object")
	}
	if scan.duplicate != "" {
		return Result{}, true, fmt.Errorf("duplicate round result field %q", scan.duplicate)
	}

	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &root); err != nil {
		return Result{}, true, err
	}
	if err := checkFields(root, "version", "summary", "choice"); err != nil {
		return Result{}, true, err
	}
	var result Result
	if err := json.Unmarshal([]byte(raw), &result); err != nil {
		return Result{}, true, fmt.Errorf("invalid round result fields: %w", err)
	}
	if result.Choice != nil {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(root["choice"], &fields); err != nil {
			return Result{}, true, err
		}
		if err := checkFields(fields, "intent", "kind", "question", "options", "allow_custom", "min", "max"); err != nil {
			return Result{}, true, err
		}
		var options []map[string]json.RawMessage
		if err := json.Unmarshal(fields["options"], &options); err != nil {
			return Result{}, true, fmt.Errorf("invalid options: %w", err)
		}
		for _, option := range options {
			if err := checkFields(option, "id", "label", "description", "emphasis"); err != nil {
				return Result{}, true, err
			}
		}
		result.Choice.defaults()
	}
	if err := result.Validate(); err != nil {
		return Result{}, true, err
	}
	return result, true, nil
}

func checkFields(fields map[string]json.RawMessage, allowed ...string) error {
	for key, raw := range fields {
		known := false
		for _, name := range allowed {
			if key == name {
				known = true
				break
			}
		}
		if !known {
			return fmt.Errorf("unknown round result field %q", key)
		}
		if strings.TrimSpace(string(raw)) == "null" && key != "choice" {
			return fmt.Errorf("round result field %q cannot be null", key)
		}
	}
	return nil
}

func (c *Choice) defaults() {
	if c.Min == 0 {
		c.Min = 1
	}
	if c.Max == 0 {
		c.Max = 1
		if c.Kind == "multiple" {
			c.Max = len(c.Options)
		}
	}
}

// Normalized fills omitted bounds without changing the receiver.
func (c Choice) Normalized() Choice {
	c.defaults()
	return c
}

// Validate also validates host-constructed values. Omitted bounds use the same
// defaults as Decode; the receiver is not changed.
func (r Result) Validate() error {
	if r.Version != Version || strings.TrimSpace(r.Summary) == "" {
		return errors.New("round result requires its exact version and a nonempty summary")
	}
	if r.Choice == nil {
		return nil
	}
	return r.Choice.Validate()
}

// Validate checks model or host values under the same frozen-choice contract.
// Zero bounds are treated as omitted; negative or contradictory bounds fail.
func (c Choice) Validate() error {
	c = c.Normalized()
	if c.Intent != "clarify" && c.Intent != "suggest" {
		return errors.New("choice intent must be clarify or suggest")
	}
	if c.Kind != "single" && c.Kind != "multiple" && c.Kind != "person" {
		return errors.New("choice kind must be single, multiple or person")
	}
	if strings.TrimSpace(c.Question) == "" || utf8.RuneCountInString(c.Question) > 2000 {
		return errors.New("choice question must contain 1 to 2000 characters")
	}
	if len(c.Options) < 2 || len(c.Options) > 20 {
		return errors.New("choice requires 2 to 20 options")
	}
	if c.Min < 1 || c.Max < c.Min || c.Max > len(c.Options) {
		return errors.New("choice bounds must satisfy 1 <= min <= max <= option count")
	}
	if c.Kind != "multiple" && (c.Min != 1 || c.Max != 1) {
		return errors.New("single and person choices must select exactly one option")
	}
	seen := make(map[string]bool, len(c.Options))
	for _, option := range c.Options {
		if strings.TrimSpace(option.ID) == "" || option.ID != strings.TrimSpace(option.ID) || utf8.RuneCountInString(option.ID) > 256 {
			return errors.New("option id must contain 1 to 256 characters without surrounding whitespace")
		}
		if seen[option.ID] {
			return fmt.Errorf("duplicate choice option id %q", option.ID)
		}
		seen[option.ID] = true
		if option.Emphasis != "" && option.Emphasis != "primary" && option.Emphasis != "secondary" && option.Emphasis != "none" {
			return errors.New("invalid option emphasis")
		}
		if strings.TrimSpace(option.Label) == "" || utf8.RuneCountInString(option.Label) > 200 || utf8.RuneCountInString(option.Description) > 1000 {
			return errors.New("option requires a label of at most 200 characters and description of at most 1000 characters")
		}
	}
	return nil
}

// resultScan finds only a top-level protocol marker and rejects duplicate keys.
// It keeps scanning after duplicates so a later version cannot hide them.
type resultScan struct {
	decoder    *json.Decoder
	recognized bool
	duplicate  string
}

func (s *resultScan) object(depth int) error {
	keys := make(map[string]bool)
	for s.decoder.More() {
		keyToken, err := s.decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("object key must be a string")
		}
		if keys[key] {
			s.duplicate = key
		}
		keys[key] = true
		value, err := s.decoder.Token()
		if err != nil {
			return err
		}
		if depth == 0 && key == "version" && value == Version {
			s.recognized = true
		}
		if err := s.value(value, depth+1); err != nil {
			return err
		}
	}
	end, err := s.decoder.Token()
	if err != nil {
		return err
	}
	if end != json.Delim('}') {
		return errors.New("incomplete JSON object")
	}
	return nil
}

func (s *resultScan) value(token json.Token, depth int) error {
	if depth > 64 {
		return errors.New("round result nesting exceeds 64 levels")
	}
	switch token {
	case json.Delim('{'):
		return s.object(depth)
	case json.Delim('['):
		for s.decoder.More() {
			value, err := s.decoder.Token()
			if err != nil {
				return err
			}
			if err := s.value(value, depth+1); err != nil {
				return err
			}
		}
		end, err := s.decoder.Token()
		if err != nil {
			return err
		}
		if end != json.Delim(']') {
			return errors.New("incomplete JSON array")
		}
	}
	return nil
}
